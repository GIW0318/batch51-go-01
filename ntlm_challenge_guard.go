// Package task keeps the NTLM negotiation of a reverse proxy honest: it mints
// one shot challenges per connection and account, refuses negotiation flag
// downgrades, checks channel binding, and replays the three message handshake
// across connection reuse and process restarts.
package task

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Negotiation flags carried in a challenge and echoed back by the client.
const (
	FlagUnicode         uint32 = 0x00000001
	FlagSign            uint32 = 0x00000010
	FlagSeal            uint32 = 0x00000020
	FlagNTLM            uint32 = 0x00000200
	FlagAlwaysSign      uint32 = 0x00008000
	FlagExtendedSession uint32 = 0x00080000
	FlagTargetInfo      uint32 = 0x00800000
	FlagVersion         uint32 = 0x02000000
	Flag128             uint32 = 0x20000000
	FlagKeyExch         uint32 = 0x40000000
)

// Outcomes written to the per connection audit log.
const (
	OutcomeAccepted = "accepted"
	OutcomeRejected = "rejected"
	OutcomeExpired  = "expired"
)

// Errors returned by the guard.
var (
	ErrBadIdentity            = errors.New("connection id and account are required")
	ErrUnknownChallenge       = errors.New("no challenge was ever issued for this connection and account")
	ErrChallengeExpired       = errors.New("challenge has expired")
	ErrChallengeReused        = errors.New("challenge has already been answered")
	ErrChallengeMismatch      = errors.New("challenge does not belong to this connection")
	ErrDowngradeRefused       = errors.New("client flags dropped a flag that the server forced on")
	ErrWeakResponse           = errors.New("only NTLMv2 extended session responses are accepted")
	ErrMicInvalid             = errors.New("message integrity check failed")
	ErrChannelBindingRequired = errors.New("channel binding value is required")
	ErrChannelBindingMismatch = errors.New("channel binding or echoed target info does not match")
	ErrAnonymousRefused       = errors.New("anonymous negotiation is not allowed")
	ErrAccountLocked          = errors.New("account is locked after too many failed responses")
	ErrPendingLimit           = errors.New("pending challenge table is full")
	ErrBadSnapshot            = errors.New("snapshot payload is invalid")
)

// GuardConfig bounds the challenge ledger of one guard instance.
type GuardConfig struct {
	// ChallengeTTL is how long a minted challenge stays usable.
	ChallengeTTL time.Duration
	// MaxPending caps the number of unanswered challenges held at once.
	MaxPending int
	// MaxAttempts is the number of failed responses tolerated per account.
	MaxAttempts int
	// Now is the clock used for issue and expiry decisions.
	Now func() time.Time
}

// Policy is the per account negotiation policy.
type Policy struct {
	Account               string
	RequireSigning        bool
	RequireNTLMv2         bool
	RequireChannelBinding bool
}

// Challenge is one minted server challenge.
type Challenge struct {
	ConnID     string
	Account    string
	Nonce      string
	TargetInfo string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	Flags      uint32
	Generation uint64
}

// Response is the third message of the handshake as seen by the proxy.
type Response struct {
	ConnID         string
	Account        string
	Nonce          string
	ClientFlags    uint32
	TargetInfoEcho string
	ChannelBinding string
	MicValid       bool
	NtlmV2         bool
}

// Result reports one accepted negotiation.
type Result struct {
	ConnID  string
	Account string
	Flags   uint32
	Seq     uint64
	At      time.Time
}

// AuditEvent is one line of the per connection negotiation log.
type AuditEvent struct {
	ConnID  string
	Account string
	Outcome string
	Reason  string
	Seq     uint64
	At      time.Time
}

// GuardStats counts what the guard has seen since the process started.
type GuardStats struct {
	Issued   int
	Accepted int
	Rejected int
	Expired  int
	Locked   int
}

type challengeStatus string

const (
	statusPending  challengeStatus = "pending"
	statusConsumed challengeStatus = "consumed"
	statusExpired  challengeStatus = "expired"
)

type challengeRecord struct {
	Challenge Challenge
	Status    challengeStatus
}

const (
	snapshotMagic   = "ntlm-challenge-guard"
	snapshotVersion = 1
)

type snapshotEnvelope struct {
	Version  int    `json:"version"`
	Payload  string `json:"payload"`
	Checksum string `json:"checksum"`
}

type snapshotRecord struct {
	Status    challengeStatus `json:"status"`
	Challenge Challenge       `json:"challenge"`
}

type snapshotState struct {
	Magic          string                  `json:"magic"`
	Version        int                     `json:"version"`
	Policies       map[string]Policy       `json:"policies"`
	Records        []snapshotRecord        `json:"records"`
	Failures       map[string]int          `json:"failures"`
	Locked         map[string]bool         `json:"locked"`
	LastGeneration uint64                  `json:"last_generation"`
	AcceptSeq      uint64                  `json:"accept_seq"`
	EventSeq       uint64                  `json:"event_seq"`
	Audit          map[string][]AuditEvent `json:"audit"`
	Stats          GuardStats              `json:"stats"`
}

// NtlmChallengeGuard owns the challenge ledger, the account policies and the
// failure counters.
type NtlmChallengeGuard struct {
	mu sync.Mutex

	cfg      GuardConfig
	policies map[string]Policy

	challenges    map[string]*challengeRecord
	pendingByConn map[string]map[string]*challengeRecord
	pendingCount  int

	failures map[string]int
	locked   map[string]bool

	lastGeneration uint64
	acceptSeq      uint64
	eventSeq       uint64

	audit map[string][]AuditEvent
	stats GuardStats
}

// NewNtlmChallengeGuard builds a guard with the given bounds and clock.
func NewNtlmChallengeGuard(cfg GuardConfig) *NtlmChallengeGuard {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &NtlmChallengeGuard{
		cfg:           cfg,
		policies:      make(map[string]Policy),
		challenges:    make(map[string]*challengeRecord),
		pendingByConn: make(map[string]map[string]*challengeRecord),
		failures:      make(map[string]int),
		locked:        make(map[string]bool),
		audit:         make(map[string][]AuditEvent),
	}
}

// SetPolicy stores the negotiation policy of one account.
func (g *NtlmChallengeGuard) SetPolicy(p Policy) error {
	if isBadIdentity(p.Account) {
		return ErrBadIdentity
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.policies[p.Account] = p
	return nil
}

// Policy returns the stored policy of one account, or the default policy.
func (g *NtlmChallengeGuard) Policy(account string) Policy {
	g.mu.Lock()
	defer g.mu.Unlock()

	if p, ok := g.policies[account]; ok {
		return p
	}
	return Policy{
		Account:        account,
		RequireSigning: true,
		RequireNTLMv2:  true,
	}
}

// Challenge mints a challenge for one connection and account, answering the
// first negotiation message.
func (g *NtlmChallengeGuard) Challenge(connID, account string, clientFlags uint32) (Challenge, error) {
	if isBadIdentity(connID) || isBadIdentity(account) {
		return Challenge{}, ErrBadIdentity
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.locked[account] {
		return Challenge{}, ErrAccountLocked
	}

	g.resetConnectionForAccountLocked(connID, account)
	if g.pendingCount >= g.cfg.MaxPending {
		return Challenge{}, ErrPendingLimit
	}

	issuedAt := g.cfg.Now()
	generation := g.lastGeneration + 1
	nonce := g.nextNonceLocked(generation)
	challenge := Challenge{
		ConnID:     connID,
		Account:    account,
		Nonce:      nonce,
		TargetInfo: "target:" + randomHex(24),
		IssuedAt:   issuedAt,
		ExpiresAt:  issuedAt.Add(g.cfg.ChallengeTTL),
		Flags:      clientFlags | forcedFlags(),
		Generation: generation,
	}

	record := &challengeRecord{Challenge: challenge, Status: statusPending}
	g.challenges[nonce] = record
	if g.pendingByConn[connID] == nil {
		g.pendingByConn[connID] = make(map[string]*challengeRecord)
	}
	g.pendingByConn[connID][nonce] = record
	g.pendingCount++
	g.lastGeneration = generation
	g.stats.Issued++

	return challenge, nil
}

// Verify checks the third negotiation message against the minted challenge.
func (g *NtlmChallengeGuard) Verify(resp Response) (Result, error) {
	if isBadIdentity(resp.ConnID) || isBadIdentity(resp.Account) {
		return Result{}, ErrBadIdentity
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.cfg.Now()
	if g.locked[resp.Account] {
		return g.rejectLocked(resp.ConnID, resp.Account, now, ErrAccountLocked)
	}

	record, ok := g.challenges[resp.Nonce]
	if !ok {
		return g.rejectLocked(resp.ConnID, resp.Account, now, ErrUnknownChallenge)
	}
	if record.Challenge.ConnID != resp.ConnID || record.Challenge.Account != resp.Account {
		return g.rejectLocked(resp.ConnID, resp.Account, now, ErrChallengeMismatch)
	}

	switch record.Status {
	case statusConsumed:
		return g.rejectLocked(resp.ConnID, resp.Account, now, ErrChallengeReused)
	case statusExpired:
		return Result{}, ErrChallengeExpired
	}

	if !now.Before(record.Challenge.ExpiresAt) {
		g.retireExpiredLocked(record, now)
		return Result{}, ErrChallengeExpired
	}

	required := forcedFlags()
	if resp.ClientFlags&required != required {
		return g.rejectLocked(resp.ConnID, resp.Account, now, ErrDowngradeRefused)
	}
	if !resp.NtlmV2 {
		return g.rejectLocked(resp.ConnID, resp.Account, now, ErrWeakResponse)
	}
	if !resp.MicValid {
		return g.rejectLocked(resp.ConnID, resp.Account, now, ErrMicInvalid)
	}

	policy := g.policies[resp.Account]
	if policy.RequireChannelBinding && strings.TrimSpace(resp.ChannelBinding) == "" {
		return g.rejectLocked(resp.ConnID, resp.Account, now, ErrChannelBindingRequired)
	}
	if resp.TargetInfoEcho != record.Challenge.TargetInfo {
		return g.rejectLocked(resp.ConnID, resp.Account, now, ErrChannelBindingMismatch)
	}

	record.Status = statusConsumed
	g.removePendingLocked(record)
	g.failures[resp.Account] = 0
	g.acceptSeq++
	g.stats.Accepted++
	g.appendAuditLocked(resp.ConnID, resp.Account, OutcomeAccepted, "", now)

	return Result{
		ConnID:  resp.ConnID,
		Account: resp.Account,
		Flags:   resp.ClientFlags,
		Seq:     g.acceptSeq,
		At:      now,
	}, nil
}

// ResetConnection drops every pending challenge of one connection and reports
// how many entries were removed.
func (g *NtlmChallengeGuard) ResetConnection(connID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.resetConnectionLocked(connID)
}

// Pending reports how many unanswered challenges one connection holds.
func (g *NtlmChallengeGuard) Pending(connID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pendingByConn[connID])
}

// Expire sweeps challenges that are past their deadline and reports how many
// entries were retired.
func (g *NtlmChallengeGuard) Expire(at time.Time) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	var expired []*challengeRecord
	for _, record := range g.challenges {
		if record.Status == statusPending && !at.Before(record.Challenge.ExpiresAt) {
			expired = append(expired, record)
		}
	}
	for _, record := range expired {
		g.retireExpiredLocked(record, at)
	}
	return len(expired)
}

// Audit returns the negotiation log of one connection, oldest first.
func (g *NtlmChallengeGuard) Audit(connID string) []AuditEvent {
	g.mu.Lock()
	defer g.mu.Unlock()

	events := g.audit[connID]
	if len(events) == 0 {
		return nil
	}
	out := make([]AuditEvent, len(events))
	copy(out, events)
	return out
}

// Stats returns the guard wide counters.
func (g *NtlmChallengeGuard) Stats() GuardStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// Snapshot serialises the challenge ledger, the counters and the sequence
// watermark as one blob.
func (g *NtlmChallengeGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	state := snapshotState{
		Magic:          snapshotMagic,
		Version:        snapshotVersion,
		Policies:       make(map[string]Policy, len(g.policies)),
		Records:        make([]snapshotRecord, 0, len(g.challenges)),
		Failures:       make(map[string]int, len(g.failures)),
		Locked:         make(map[string]bool, len(g.locked)),
		LastGeneration: g.lastGeneration,
		AcceptSeq:      g.acceptSeq,
		EventSeq:       g.eventSeq,
		Audit:          make(map[string][]AuditEvent, len(g.audit)),
		Stats:          g.stats,
	}
	for account, policy := range g.policies {
		state.Policies[account] = policy
	}
	for _, record := range g.challenges {
		state.Records = append(state.Records, snapshotRecord{
			Status:    record.Status,
			Challenge: record.Challenge,
		})
	}
	sort.Slice(state.Records, func(i, j int) bool {
		a, b := state.Records[i].Challenge, state.Records[j].Challenge
		if a.Generation != b.Generation {
			return a.Generation < b.Generation
		}
		return a.Nonce < b.Nonce
	})
	for account, count := range g.failures {
		state.Failures[account] = count
	}
	for account, locked := range g.locked {
		state.Locked[account] = locked
	}
	for connID, events := range g.audit {
		state.Audit[connID] = append([]AuditEvent(nil), events...)
	}

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Payload:  base64.StdEncoding.EncodeToString(payload),
		Checksum: hex.EncodeToString(sum[:]),
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole guard state with a snapshot, all or nothing.
func (g *NtlmChallengeGuard) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if !decodeStrictJSON(data, &envelope) {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || envelope.Payload == "" || envelope.Checksum == "" {
		return ErrBadSnapshot
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(payload)
	if !strings.EqualFold(envelope.Checksum, hex.EncodeToString(sum[:])) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if !decodeStrictJSON(payload, &state) {
		return ErrBadSnapshot
	}
	if state.Magic != snapshotMagic || state.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if g.cfg.MaxAttempts < 0 || g.cfg.MaxPending < 0 {
		return ErrBadSnapshot
	}

	challenges := make(map[string]*challengeRecord, len(state.Records))
	pendingByConn := make(map[string]map[string]*challengeRecord)
	pendingCount := 0
	var maxGeneration uint64
	for _, stored := range state.Records {
		ch := stored.Challenge
		if isBadIdentity(ch.ConnID) || isBadIdentity(ch.Account) || len(ch.Nonce) < 16 {
			return ErrBadSnapshot
		}
		if ch.Generation == 0 || ch.TargetInfo == "" || ch.Flags&forcedFlags() != forcedFlags() {
			return ErrBadSnapshot
		}
		if stored.Status != statusPending && stored.Status != statusConsumed && stored.Status != statusExpired {
			return ErrBadSnapshot
		}
		if _, exists := challenges[ch.Nonce]; exists {
			return ErrBadSnapshot
		}
		if ch.Generation > maxGeneration {
			maxGeneration = ch.Generation
		}
		record := &challengeRecord{Challenge: ch, Status: stored.Status}
		challenges[ch.Nonce] = record
		if stored.Status == statusPending {
			if pendingByConn[ch.ConnID] == nil {
				pendingByConn[ch.ConnID] = make(map[string]*challengeRecord)
			}
			pendingByConn[ch.ConnID][ch.Nonce] = record
			pendingCount++
		}
	}
	if pendingCount > g.cfg.MaxPending {
		return ErrBadSnapshot
	}
	if state.LastGeneration < maxGeneration {
		return ErrBadSnapshot
	}

	for account, policy := range state.Policies {
		if isBadIdentity(account) || policy.Account != account {
			return ErrBadSnapshot
		}
	}
	failures := make(map[string]int, len(state.Failures))
	for account, count := range state.Failures {
		if isBadIdentity(account) || count < 0 || count > g.cfg.MaxAttempts {
			return ErrBadSnapshot
		}
		failures[account] = count
	}
	locked := make(map[string]bool, len(state.Locked))
	for account, value := range state.Locked {
		if isBadIdentity(account) {
			return ErrBadSnapshot
		}
		if value {
			locked[account] = true
		}
	}

	if state.Stats.Issued < 0 || state.Stats.Accepted < 0 || state.Stats.Rejected < 0 ||
		state.Stats.Expired < 0 || state.Stats.Locked < 0 {
		return ErrBadSnapshot
	}

	audit := make(map[string][]AuditEvent, len(state.Audit))
	var maxEventSeq uint64
	for connID, events := range state.Audit {
		if isBadIdentity(connID) {
			return ErrBadSnapshot
		}
		for _, event := range events {
			if event.ConnID != connID || isBadIdentity(event.Account) {
				return ErrBadSnapshot
			}
			if event.Outcome != OutcomeAccepted && event.Outcome != OutcomeRejected && event.Outcome != OutcomeExpired {
				return ErrBadSnapshot
			}
			if event.Outcome != OutcomeAccepted && event.Reason == "" {
				return ErrBadSnapshot
			}
			if event.Seq > maxEventSeq {
				maxEventSeq = event.Seq
			}
		}
		if len(events) > 0 {
			audit[connID] = append([]AuditEvent(nil), events...)
		}
	}
	if state.EventSeq < maxEventSeq {
		return ErrBadSnapshot
	}

	policies := make(map[string]Policy, len(state.Policies))
	for account, policy := range state.Policies {
		policies[account] = policy
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.policies = policies
	g.challenges = challenges
	g.pendingByConn = pendingByConn
	g.pendingCount = pendingCount
	g.failures = failures
	g.locked = locked
	g.lastGeneration = state.LastGeneration
	g.acceptSeq = state.AcceptSeq
	g.eventSeq = state.EventSeq
	g.audit = audit
	g.stats = state.Stats
	return nil
}

func decodeStrictJSON(data []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

func isBadIdentity(value string) bool {
	return strings.TrimSpace(value) == ""
}

func forcedFlags() uint32 {
	return FlagSign | FlagExtendedSession | FlagTargetInfo
}

func randomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		now := time.Now().UnixNano()
		for i := range buf {
			buf[i] = byte(now >> uint((i%8)*8))
		}
	}
	return hex.EncodeToString(buf)
}

func (g *NtlmChallengeGuard) nextNonceLocked(generation uint64) string {
	for {
		nonce := fmt.Sprintf("%016x%s", generation, randomHex(16))
		if _, exists := g.challenges[nonce]; !exists {
			return nonce
		}
	}
}

func (g *NtlmChallengeGuard) resetConnectionForAccountLocked(connID, account string) {
	for _, record := range g.pendingByConn[connID] {
		if record.Challenge.Account != account {
			g.resetConnectionLocked(connID)
			return
		}
	}
}

func (g *NtlmChallengeGuard) resetConnectionLocked(connID string) int {
	records := g.pendingByConn[connID]
	if len(records) == 0 {
		delete(g.pendingByConn, connID)
		return 0
	}
	for nonce := range records {
		delete(g.challenges, nonce)
	}
	g.pendingCount -= len(records)
	delete(g.pendingByConn, connID)
	return len(records)
}

func (g *NtlmChallengeGuard) removePendingLocked(record *challengeRecord) {
	connRecords := g.pendingByConn[record.Challenge.ConnID]
	if connRecords == nil {
		return
	}
	if _, exists := connRecords[record.Challenge.Nonce]; !exists {
		return
	}
	delete(connRecords, record.Challenge.Nonce)
	g.pendingCount--
	if len(connRecords) == 0 {
		delete(g.pendingByConn, record.Challenge.ConnID)
	}
}

func (g *NtlmChallengeGuard) retireExpiredLocked(record *challengeRecord, at time.Time) {
	if record.Status != statusPending {
		return
	}
	record.Status = statusExpired
	g.removePendingLocked(record)
	g.stats.Expired++
	g.appendAuditLocked(record.Challenge.ConnID, record.Challenge.Account,
		OutcomeExpired, ErrChallengeExpired.Error(), at)
}

func (g *NtlmChallengeGuard) rejectLocked(connID, account string, at time.Time, reason error) (Result, error) {
	g.stats.Rejected++
	g.noteFailureLocked(account)
	g.appendAuditLocked(connID, account, OutcomeRejected, reason.Error(), at)
	return Result{}, reason
}

func (g *NtlmChallengeGuard) noteFailureLocked(account string) {
	if g.locked[account] {
		return
	}
	if g.cfg.MaxAttempts <= 0 {
		g.failures[account] = 0
		g.locked[account] = true
		g.stats.Locked++
		return
	}
	next := g.failures[account] + 1
	if next >= g.cfg.MaxAttempts {
		next = g.cfg.MaxAttempts
		g.locked[account] = true
		g.stats.Locked++
	}
	g.failures[account] = next
}

func (g *NtlmChallengeGuard) appendAuditLocked(connID, account, outcome, reason string, at time.Time) {
	g.eventSeq++
	g.audit[connID] = append(g.audit[connID], AuditEvent{
		ConnID:  connID,
		Account: account,
		Outcome: outcome,
		Reason:  reason,
		Seq:     g.eventSeq,
		At:      at,
	})
}
