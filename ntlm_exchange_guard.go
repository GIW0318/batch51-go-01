// Package task arbitrates Windows compatible NTLM login handshakes for the
// edge authentication gateway: one negotiate per connection, a challenge that
// is bound to the connection and the source endpoint, and a single consume of
// the authenticated identity.
package task

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Negotiation flags carried by ChallengeMsg and AuthenticateMsg.
const (
	FlagUnicode      uint32 = 0x00000001
	FlagOEM          uint32 = 0x00000002
	FlagSign         uint32 = 0x00000010
	FlagSeal         uint32 = 0x00000020
	FlagLMKey        uint32 = 0x00000080
	FlagNTLM         uint32 = 0x00000200
	FlagAnonymous    uint32 = 0x00000800
	FlagExtSession   uint32 = 0x00080000
	FlagChannelBound uint32 = 0x02000000
)

// Handshake states reported by ChallengeFor and Explain style lookups.
const (
	HandshakePending  = "pending"
	HandshakeAccepted = "accepted"
	HandshakeRejected = "rejected"
	HandshakeAborted  = "aborted"
	HandshakeEvicted  = "evicted"
)

// Audit action names written by every state transition.
const (
	ActionBegin  = "begin"
	ActionAccept = "accept"
	ActionAbort  = "abort"
	ActionReject = "reject"
)

// Errors returned by the guard.
var (
	ErrEmptyConnection     = errors.New("connection id is required")
	ErrHandshakeNotFound   = errors.New("handshake not found")
	ErrHandshakeReused     = errors.New("connection already negotiated")
	ErrHandshakeConsumed   = errors.New("challenge already consumed")
	ErrHandshakeAborted    = errors.New("handshake was aborted")
	ErrHandshakeLost       = errors.New("handshake was evicted")
	ErrSourceLimit         = errors.New("too many pending handshakes from this source")
	ErrPendingLimit        = errors.New("pending handshake table is full")
	ErrSourceBlocked       = errors.New("source reputation exhausted")
	ErrWeakResponse        = errors.New("only NTLMv2 responses are accepted")
	ErrAnonymousSession    = errors.New("anonymous session refused")
	ErrDowngradeRefused    = errors.New("negotiation downgrade refused")
	ErrShortResponse       = errors.New("response blob is too short")
	ErrChallengeMismatch   = errors.New("challenge does not belong to this connection")
	ErrRelayAttempt        = errors.New("challenge replayed from another connection")
	ErrMicMismatch         = errors.New("message integrity check failed")
	ErrChannelMismatch     = errors.New("channel binding value differs")
	ErrChannelBindingsGone = errors.New("channel bindings omitted")
	ErrBadSnapshot         = errors.New("snapshot payload is invalid")
)

// Endpoint is the network origin of one inbound connection.
type Endpoint struct {
	Addr string `json:"addr"`
	Host string `json:"host"`
}

// GuardConfig bounds the exchange tables.
type GuardConfig struct {
	MaxPending            int
	MaxPerSource          int
	MaxReputation         int
	MaxAuditEvents        int
	RequireChannelBinding bool
}

// ChallengeMsg is the server challenge handed back for one connection.
type ChallengeMsg struct {
	ConnectionID string `json:"connection_id"`
	Nonce        string `json:"nonce"`
	Flags        uint32 `json:"flags"`
	SourceAddr   string `json:"source_addr"`
}

// AuthenticateMsg is the client answer for one live challenge.
type AuthenticateMsg struct {
	User            string `json:"user"`
	Domain          string `json:"domain"`
	Version         int    `json:"version"`
	Flags           uint32 `json:"flags"`
	ChallengeNonce  string `json:"challenge_nonce"`
	ClientNonce     string `json:"client_nonce"`
	Response        string `json:"response"`
	MIC             string `json:"mic"`
	ChannelBindings []byte `json:"channel_bindings"`
}

// Principal is the identity produced by a completed handshake.
type Principal struct {
	Account  string `json:"account"`
	Domain   string `json:"domain"`
	Source   string `json:"source"`
	ConnID   string `json:"conn_id"`
	Channel  string `json:"channel"`
	Sequence int64  `json:"sequence"`
}

// AuditEvent is one recorded transition of one connection.
type AuditEvent struct {
	ConnID string `json:"conn_id"`
	Source string `json:"source"`
	Action string `json:"action"`
	Reason string `json:"reason"`
	Seq    int64  `json:"seq"`
}

// GuardStats aggregates the counters of the whole guard.
type GuardStats struct {
	Begun    int `json:"begun"`
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
	Aborted  int `json:"aborted"`
	Relayed  int `json:"relayed"`
	Evicted  int `json:"evicted"`
	Pending  int `json:"pending"`
}

// NtlmExchangeGuard owns the pending handshake table, the audit log and the
// per source reputation counters.
type NtlmExchangeGuard struct {
	mu sync.Mutex

	cfg GuardConfig

	records map[string]*handshake
	pending map[string]*handshake
	nonces  map[string]*handshake

	sourcePending   map[string]int
	sourceFailures  map[string]int
	accountFailures map[string]int

	audit   []AuditEvent
	nextSeq int64
	order   uint64

	counters guardCounters
}

// NewNtlmExchangeGuard builds a guard with the given caps.
func NewNtlmExchangeGuard(cfg GuardConfig) *NtlmExchangeGuard {
	cfg = normalizeGuardConfig(cfg)
	return &NtlmExchangeGuard{
		cfg:             cfg,
		records:         make(map[string]*handshake),
		pending:         make(map[string]*handshake),
		nonces:          make(map[string]*handshake),
		sourcePending:   make(map[string]int),
		sourceFailures:  make(map[string]int),
		accountFailures: make(map[string]int),
	}
}

// Begin opens the single negotiate step of one connection.
func (g *NtlmExchangeGuard) Begin(connID string, src Endpoint) (ChallengeMsg, error) {
	connID = strings.TrimSpace(connID)
	if connID == "" {
		return ChallengeMsg{}, ErrEmptyConnection
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.records[connID]; exists {
		return ChallengeMsg{}, ErrHandshakeReused
	}
	if g.sourceFailures[src.Addr] >= g.cfg.MaxReputation {
		return ChallengeMsg{}, ErrSourceBlocked
	}
	if g.sourcePending[src.Addr] >= g.cfg.MaxPerSource {
		return ChallengeMsg{}, ErrSourceLimit
	}

	if len(g.pending) >= g.cfg.MaxPending {
		if err := g.evictOldestLocked(); err != nil {
			return ChallengeMsg{}, err
		}
	}

	nonce, err := newChallengeNonce(g.nonces)
	if err != nil {
		return ChallengeMsg{}, err
	}

	g.order++
	flags := FlagUnicode | FlagNTLM | FlagExtSession
	if g.cfg.RequireChannelBinding {
		flags |= FlagChannelBound
	}
	h := &handshake{
		ConnID: connID,
		Source: src,
		Nonce:  nonce,
		Flags:  flags,
		State:  HandshakePending,
		Order:  g.order,
	}
	g.records[connID] = h
	g.pending[connID] = h
	g.nonces[nonce] = h
	g.sourcePending[src.Addr]++
	g.counters.Begun++
	g.recordLocked(connID, src.Addr, ActionBegin, "")

	return challengeOf(h), nil
}

// Accept consumes the challenge with one authenticate message.
func (g *NtlmExchangeGuard) Accept(connID string, msg AuthenticateMsg) (Principal, error) {
	connID = strings.TrimSpace(connID)

	g.mu.Lock()
	defer g.mu.Unlock()

	h, ok := g.records[connID]
	if !ok {
		return Principal{}, ErrHandshakeNotFound
	}
	switch h.State {
	case HandshakePending:
	case HandshakeAccepted, HandshakeRejected:
		return Principal{}, ErrHandshakeConsumed
	case HandshakeAborted:
		return Principal{}, ErrHandshakeAborted
	case HandshakeEvicted:
		return Principal{}, ErrHandshakeLost
	default:
		return Principal{}, ErrHandshakeNotFound
	}

	if msg.ChallengeNonce != h.Nonce {
		g.counters.Relayed++
		return Principal{}, g.rejectLocked(h, msg, ErrRelayAttempt)
	}

	const mandatory = FlagUnicode | FlagNTLM | FlagExtSession
	if msg.Flags&mandatory != mandatory || msg.Flags&(FlagLMKey|FlagOEM) != 0 {
		return Principal{}, g.rejectLocked(h, msg, ErrDowngradeRefused)
	}
	if msg.Version != 2 {
		return Principal{}, g.rejectLocked(h, msg, ErrWeakResponse)
	}
	if strings.TrimSpace(msg.User) == "" || msg.Flags&FlagAnonymous != 0 {
		return Principal{}, g.rejectLocked(h, msg, ErrAnonymousSession)
	}
	if len(msg.Response) < 32 {
		return Principal{}, g.rejectLocked(h, msg, ErrShortResponse)
	}

	registered := len(h.Channel) != 0
	supplied := len(msg.ChannelBindings) != 0
	requiresBinding := g.cfg.RequireChannelBinding ||
		h.Flags&FlagChannelBound != 0 ||
		msg.Flags&FlagChannelBound != 0
	if requiresBinding && (!registered || !supplied) {
		return Principal{}, g.rejectLocked(h, msg, ErrChannelBindingsGone)
	}
	if registered != supplied {
		return Principal{}, g.rejectLocked(h, msg, ErrChannelBindingsGone)
	}
	if registered {
		serverDigest := sha256.Sum256(h.Channel)
		clientDigest := sha256.Sum256(msg.ChannelBindings)
		if subtle.ConstantTimeCompare(serverDigest[:], clientDigest[:]) != 1 {
			return Principal{}, g.rejectLocked(h, msg, ErrChannelMismatch)
		}
	}

	micInput := h.Nonce + "|" + msg.ClientNonce + "|" + msg.User
	micSum := sha256.Sum256([]byte(micInput))
	wantMIC := hex.EncodeToString(micSum[:])
	if subtle.ConstantTimeCompare([]byte(wantMIC), []byte(msg.MIC)) != 1 {
		return Principal{}, g.rejectLocked(h, msg, ErrMicMismatch)
	}

	channel := ""
	if registered {
		sum := sha256.Sum256(h.Channel)
		channel = hex.EncodeToString(sum[:])
	}
	g.terminateLocked(h, HandshakeAccepted)
	g.counters.Accepted++
	seq := g.recordLocked(h.ConnID, h.Source.Addr, ActionAccept, "")

	return Principal{
		Account:  strings.TrimSpace(msg.User),
		Domain:   strings.TrimSpace(msg.Domain),
		Source:   h.Source.Addr,
		ConnID:   h.ConnID,
		Channel:  channel,
		Sequence: seq,
	}, nil
}

// Abort drops a pending handshake and records the operator reason.
func (g *NtlmExchangeGuard) Abort(connID, reason string) error {
	connID = strings.TrimSpace(connID)

	g.mu.Lock()
	defer g.mu.Unlock()

	h, ok := g.records[connID]
	if !ok {
		return ErrHandshakeNotFound
	}
	if h.State != HandshakePending {
		return nil
	}

	g.terminateLocked(h, HandshakeAborted)
	g.counters.Aborted++
	g.recordLocked(h.ConnID, h.Source.Addr, ActionAbort, reason)
	return nil
}

// BindChannel registers the TLS channel binding value of one connection.
func (g *NtlmExchangeGuard) BindChannel(connID string, cbt []byte) error {
	connID = strings.TrimSpace(connID)

	g.mu.Lock()
	defer g.mu.Unlock()

	h, ok := g.records[connID]
	if !ok || h.State != HandshakePending {
		return ErrHandshakeNotFound
	}
	if len(cbt) == 0 {
		return ErrChannelBindingsGone
	}
	h.Channel = append(h.Channel[:0], cbt...)
	return nil
}

// ChallengeFor reads the live challenge of one connection.
func (g *NtlmExchangeGuard) ChallengeFor(connID string) (ChallengeMsg, bool) {
	connID = strings.TrimSpace(connID)

	g.mu.Lock()
	defer g.mu.Unlock()

	h, ok := g.records[connID]
	if !ok || h.State != HandshakePending {
		return ChallengeMsg{}, false
	}
	return challengeOf(h), true
}

// Pending reports how many handshakes are waiting for an authenticate message.
func (g *NtlmExchangeGuard) Pending() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pending)
}

// Stats returns a snapshot of the counters.
func (g *NtlmExchangeGuard) Stats() GuardStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return GuardStats{
		Begun:    g.counters.Begun,
		Accepted: g.counters.Accepted,
		Rejected: g.counters.Rejected,
		Aborted:  g.counters.Aborted,
		Relayed:  g.counters.Relayed,
		Evicted:  g.counters.Evicted,
		Pending:  len(g.pending),
	}
}

// Audit returns the recorded transitions of one connection, oldest first.
func (g *NtlmExchangeGuard) Audit(connID string) []AuditEvent {
	connID = strings.TrimSpace(connID)

	g.mu.Lock()
	defer g.mu.Unlock()

	var events []AuditEvent
	for _, event := range g.audit {
		if event.ConnID == connID {
			events = append(events, event)
		}
	}
	return events
}

// ReputationFor returns the accumulated failure score of one source address.
func (g *NtlmExchangeGuard) ReputationFor(addr string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sourceFailures[addr]
}

// Snapshot serialises challenges, reputation counters and the audit sequence.
func (g *NtlmExchangeGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	state := snapshotState{
		NextSequence: g.nextSeq,
		NextOrder:    g.order,
		Counters:     g.counters,
		Audit:        append([]AuditEvent(nil), g.audit...),
		Sources:      cloneIntMap(g.sourceFailures),
		Accounts:     cloneIntMap(g.accountFailures),
	}
	state.Handshakes = make([]handshake, 0, len(g.records))
	for _, h := range g.records {
		state.Handshakes = append(state.Handshakes, cloneHandshake(h))
	}
	g.mu.Unlock()

	sort.Slice(state.Handshakes, func(i, j int) bool {
		if state.Handshakes[i].Order == state.Handshakes[j].Order {
			return state.Handshakes[i].ConnID < state.Handshakes[j].ConnID
		}
		return state.Handshakes[i].Order < state.Handshakes[j].Order
	})

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(stateJSON)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		State:    json.RawMessage(stateJSON),
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *NtlmExchangeGuard) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := decodeStrictJSON(data, &envelope); err != nil {
		return badSnapshot(err)
	}
	if envelope.Version != snapshotVersion || len(envelope.State) == 0 {
		return badSnapshot(errors.New("unsupported snapshot version"))
	}
	sum := sha256.Sum256(envelope.State)
	if subtle.ConstantTimeCompare([]byte(envelope.Checksum), []byte(hex.EncodeToString(sum[:]))) != 1 {
		return badSnapshot(errors.New("snapshot checksum mismatch"))
	}

	var state snapshotState
	if err := decodeStrictJSON(envelope.State, &state); err != nil {
		return badSnapshot(err)
	}

	records := make(map[string]*handshake, len(state.Handshakes))
	pending := make(map[string]*handshake)
	nonces := make(map[string]*handshake)
	sourcePending := make(map[string]int)
	seenOrders := make(map[uint64]struct{}, len(state.Handshakes))
	var maxOrder uint64

	for i := range state.Handshakes {
		saved := state.Handshakes[i]
		if strings.TrimSpace(saved.ConnID) == "" || strings.TrimSpace(saved.ConnID) != saved.ConnID {
			return badSnapshot(errors.New("invalid connection id"))
		}
		if _, exists := records[saved.ConnID]; exists {
			return badSnapshot(errors.New("duplicate connection id"))
		}
		if !validChallengeNonce(saved.Nonce) {
			return badSnapshot(errors.New("invalid challenge nonce"))
		}
		if _, exists := nonces[saved.Nonce]; exists {
			return badSnapshot(errors.New("duplicate challenge nonce"))
		}
		if saved.Order == 0 {
			return badSnapshot(errors.New("invalid handshake order"))
		}
		if _, exists := seenOrders[saved.Order]; exists {
			return badSnapshot(errors.New("duplicate handshake order"))
		}
		if !validHandshakeState(saved.State) {
			return badSnapshot(errors.New("invalid handshake state"))
		}

		h := saved
		h.Channel = append([]byte(nil), saved.Channel...)
		records[h.ConnID] = &h
		nonces[h.Nonce] = &h
		if h.Order > maxOrder {
			maxOrder = h.Order
		}
		seenOrders[h.Order] = struct{}{}
		if h.State == HandshakePending {
			pending[h.ConnID] = &h
			sourcePending[h.Source.Addr]++
		}
	}

	if state.NextSequence < 0 || (state.NextOrder != 0 && state.NextOrder < maxOrder) {
		return badSnapshot(errors.New("invalid sequence state"))
	}
	if state.NextOrder == 0 && maxOrder != 0 {
		return badSnapshot(errors.New("missing handshake order sequence"))
	}
	if !validCounters(state.Counters) {
		return badSnapshot(errors.New("invalid counters"))
	}
	if len(state.Audit) > g.cfg.MaxAuditEvents {
		return badSnapshot(errors.New("too many audit events"))
	}
	var lastAuditSeq int64
	for _, event := range state.Audit {
		if event.Seq <= 0 || event.Seq > state.NextSequence || event.Seq <= lastAuditSeq {
			return badSnapshot(errors.New("invalid audit sequence"))
		}
		if !validAuditAction(event.Action) {
			return badSnapshot(errors.New("invalid audit action"))
		}
		lastAuditSeq = event.Seq
	}
	for addr, score := range state.Sources {
		if score < 0 || score > g.cfg.MaxReputation {
			return badSnapshot(errors.New("invalid source reputation"))
		}
		if addr == "" {
			return badSnapshot(errors.New("invalid source address"))
		}
	}
	for account, score := range state.Accounts {
		if score < 0 || score > g.cfg.MaxReputation || account == "" {
			return badSnapshot(errors.New("invalid account reputation"))
		}
	}
	if len(pending) > g.cfg.MaxPending {
		return badSnapshot(errors.New("too many pending handshakes"))
	}
	for _, count := range sourcePending {
		if count > g.cfg.MaxPerSource {
			return badSnapshot(errors.New("too many handshakes from one source"))
		}
	}

	g.mu.Lock()
	g.records = records
	g.pending = pending
	g.nonces = nonces
	g.sourcePending = sourcePending
	g.sourceFailures = cloneIntMap(state.Sources)
	g.accountFailures = cloneIntMap(state.Accounts)
	g.audit = append([]AuditEvent(nil), state.Audit...)
	g.nextSeq = state.NextSequence
	g.order = state.NextOrder
	g.counters = state.Counters
	g.mu.Unlock()
	return nil
}

const (
	defaultMaxPending     = 128
	defaultMaxPerSource   = 16
	defaultMaxReputation  = 100
	defaultMaxAuditEvents = 1024
	snapshotVersion       = 1
)

type handshake struct {
	ConnID  string   `json:"connection_id"`
	Source  Endpoint `json:"source"`
	Nonce   string   `json:"nonce"`
	Flags   uint32   `json:"flags"`
	Channel []byte   `json:"channel,omitempty"`
	State   string   `json:"state"`
	Order   uint64   `json:"order"`
}

type guardCounters struct {
	Begun    int `json:"begun"`
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
	Aborted  int `json:"aborted"`
	Relayed  int `json:"relayed"`
	Evicted  int `json:"evicted"`
}

type snapshotState struct {
	NextSequence int64          `json:"next_sequence"`
	NextOrder    uint64         `json:"next_order"`
	Counters     guardCounters  `json:"counters"`
	Handshakes   []handshake    `json:"handshakes"`
	Sources      map[string]int `json:"sources"`
	Accounts     map[string]int `json:"accounts"`
	Audit        []AuditEvent   `json:"audit"`
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

func normalizeGuardConfig(cfg GuardConfig) GuardConfig {
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = defaultMaxPending
	}
	if cfg.MaxPerSource <= 0 {
		cfg.MaxPerSource = defaultMaxPerSource
	}
	if cfg.MaxReputation <= 0 {
		cfg.MaxReputation = defaultMaxReputation
	}
	if cfg.MaxAuditEvents <= 0 {
		cfg.MaxAuditEvents = defaultMaxAuditEvents
	}
	return cfg
}

func newChallengeNonce(existing map[string]*handshake) (string, error) {
	for {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", fmt.Errorf("generate challenge: %w", err)
		}
		nonce := hex.EncodeToString(raw[:])
		if _, used := existing[nonce]; !used {
			return nonce, nil
		}
	}
}

func challengeOf(h *handshake) ChallengeMsg {
	return ChallengeMsg{
		ConnectionID: h.ConnID,
		Nonce:        h.Nonce,
		Flags:        h.Flags,
		SourceAddr:   h.Source.Addr,
	}
}

func (g *NtlmExchangeGuard) evictOldestLocked() error {
	var oldest *handshake
	for _, h := range g.pending {
		if oldest == nil || h.Order < oldest.Order {
			oldest = h
		}
	}
	if oldest == nil {
		return ErrPendingLimit
	}
	g.terminateLocked(oldest, HandshakeEvicted)
	g.counters.Evicted++
	g.recordLocked(oldest.ConnID, oldest.Source.Addr, ActionReject, "handshake evicted")
	return nil
}

func (g *NtlmExchangeGuard) rejectLocked(h *handshake, msg AuthenticateMsg, cause error) error {
	g.terminateLocked(h, HandshakeRejected)
	g.counters.Rejected++
	g.chargeFailureLocked(h.Source.Addr, accountKey(msg.Domain, msg.User))
	g.recordLocked(h.ConnID, h.Source.Addr, ActionReject, cause.Error())
	return cause
}

func (g *NtlmExchangeGuard) terminateLocked(h *handshake, state string) {
	if h.State != HandshakePending {
		return
	}
	delete(g.pending, h.ConnID)
	delete(g.nonces, h.Nonce)
	if count := g.sourcePending[h.Source.Addr]; count <= 1 {
		delete(g.sourcePending, h.Source.Addr)
	} else {
		g.sourcePending[h.Source.Addr] = count - 1
	}
	h.State = state
}

func (g *NtlmExchangeGuard) chargeFailureLocked(addr, account string) {
	if addr != "" && g.sourceFailures[addr] < g.cfg.MaxReputation {
		g.sourceFailures[addr]++
	}
	if account != "" && g.accountFailures[account] < g.cfg.MaxReputation {
		g.accountFailures[account]++
	}
}

func (g *NtlmExchangeGuard) recordLocked(connID, source, action, reason string) int64 {
	g.nextSeq++
	event := AuditEvent{
		ConnID: connID,
		Source: source,
		Action: action,
		Reason: reason,
		Seq:    g.nextSeq,
	}
	g.audit = append(g.audit, event)
	if len(g.audit) > g.cfg.MaxAuditEvents {
		drop := len(g.audit) - g.cfg.MaxAuditEvents
		g.audit = append([]AuditEvent(nil), g.audit[drop:]...)
	}
	return event.Seq
}

func accountKey(domain, user string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return ""
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return strings.ToLower(user)
	}
	return strings.ToLower(domain + `\` + user)
}

func cloneIntMap(src map[string]int) map[string]int {
	dst := make(map[string]int, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneHandshake(h *handshake) handshake {
	clone := *h
	clone.Channel = append([]byte(nil), h.Channel...)
	return clone
}

func validChallengeNonce(nonce string) bool {
	if len(nonce) != 32 || strings.ToLower(nonce) != nonce {
		return false
	}
	_, err := hex.DecodeString(nonce)
	return err == nil
}

func validHandshakeState(state string) bool {
	switch state {
	case HandshakePending, HandshakeAccepted, HandshakeRejected, HandshakeAborted, HandshakeEvicted:
		return true
	default:
		return false
	}
}

func validAuditAction(action string) bool {
	switch action {
	case ActionBegin, ActionAccept, ActionAbort, ActionReject:
		return true
	default:
		return false
	}
}

func validCounters(counters guardCounters) bool {
	return counters.Begun >= 0 &&
		counters.Accepted >= 0 &&
		counters.Rejected >= 0 &&
		counters.Aborted >= 0 &&
		counters.Relayed >= 0 &&
		counters.Evicted >= 0
}

func decodeStrictJSON(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing json value")
		}
		return err
	}
	return nil
}

func badSnapshot(cause error) error {
	return fmt.Errorf("%w: %v", ErrBadSnapshot, cause)
}
