// Package task fronts the realm KDC and orchestrates the pre-authentication
// step of the Kerberos AS exchange, including replay accounting.
package task

import (
	"bytes"
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
	"time"
)

// Reply statuses carried by AsReply.
const (
	StatusContinue = "continue"
	StatusIssued   = "issued"
)

// Reason codes attached to a reply and to a recorded failure.
const (
	ReasonOK               = "ok"
	ReasonPreauthRequired  = "preauth_required"
	ReasonSkewFuture       = "skew_future"
	ReasonSkewPast         = "skew_past"
	ReasonReplay           = "replay"
	ReasonWeakEType        = "weak_etype"
	ReasonUnsupportedEType = "unsupported_etype"
	ReasonArmorMismatch    = "armor_mismatch"
	ReasonPendingLimit     = "pending_limit"
	ReasonReplayFull       = "replay_full"
	ReasonFailureLimit     = "failure_limit"
	ReasonUnknownSession   = "unknown_session"
)

// Errors surfaced by the guard.
var (
	ErrBadRequest       = errors.New("client, realm and tenant are required")
	ErrUnknownSession   = errors.New("unknown as exchange session")
	ErrClockSkewFuture  = errors.New("timestamp is too far in the future")
	ErrClockSkewPast    = errors.New("timestamp is too far in the past")
	ErrReplay           = errors.New("preauth timestamp already recorded")
	ErrReplayCacheFull  = errors.New("replay cache is full")
	ErrWeakEType        = errors.New("etype is below the policy floor")
	ErrUnsupportedEType = errors.New("etype is not enabled for this realm")
	ErrArmorMismatch    = errors.New("fast armor binding does not match the pending request")
	ErrPendingLimit     = errors.New("too many pending sessions for this account")
	ErrTooManyFailures  = errors.New("account is locked after repeated preauth failures")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// RealmPolicy is the start up configuration of the guard for one tenant.
type RealmPolicy struct {
	Tenant        string
	Realm         string
	AllowedETypes []int
	MinEType      int
	ClockSkew     time.Duration
	MaxReplay     int
	MaxPending    int
	MaxFailures   int
	RequireFAST   bool
	Now           func() time.Time
}

// Policy is the read only tenant view returned by Policy.
type Policy struct {
	Tenant      string
	Realm       string
	MinEType    int
	ETypes      []int
	ClockSkew   time.Duration
	RequireFAST bool
}

// AsRequest is one inbound AS-REQ that has not presented pre-authentication.
type AsRequest struct {
	Client string
	Realm  string
	Tenant string
	Nonce  string
	FAST   bool
	Armor  string
}

// PreauthData is the PA-ENC-TIMESTAMP blob a client sends on the second leg.
type PreauthData struct {
	Client string
	Realm  string
	EType  int
	Time   time.Time
	FAST   bool
	Armor  string
}

// AsReply is the answer handed back to the KDC front end.
type AsReply struct {
	SessionID string
	Status    string
	Reason    string
	EType     int
	Salt      string
	ETypes    []int
	Seq       int64
}

// TicketStats is the per tenant counter view.
type TicketStats struct {
	Tenant   string
	Issued   int
	Rejected int
	Pending  int
	Failures int
}

type pendingSession struct {
	request AsRequest
	seq     int64
}

type replayKey struct {
	client    string
	realm     string
	timestamp int64
	etype     int
}

type snapshotReplay struct {
	Client    string `json:"client"`
	Realm     string `json:"realm"`
	Timestamp int64  `json:"timestamp"`
	EType     int    `json:"etype"`
}

type snapshotPending struct {
	SessionID string    `json:"session_id"`
	Request   AsRequest `json:"request"`
	Seq       int64     `json:"seq"`
}

type snapshotState struct {
	Version  int               `json:"version"`
	Seq      int64             `json:"seq"`
	Failures map[string]int    `json:"failures"`
	Replay   []snapshotReplay  `json:"replay"`
	Pending  []snapshotPending `json:"pending"`
	Issued   int               `json:"issued"`
	Rejected int               `json:"rejected"`
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"sha256"`
}

const snapshotVersion = 1

// AsPreauthGuard owns the pending sessions, the replay cache and the per
// account failure counters.
type AsPreauthGuard struct {
	mu sync.Mutex

	cfg           RealmPolicy
	etypes        []int
	allowed       map[int]struct{}
	pending       map[string]pendingSession
	pendingCounts map[string]int
	failures      map[string]int
	replay        map[replayKey]struct{}
	seq           int64
	issued        int
	rejected      int
}

// NewAsPreauthGuard builds a guard for one tenant policy.
func NewAsPreauthGuard(cfg RealmPolicy) *AsPreauthGuard {
	cfg.AllowedETypes = sortedETypes(cfg.AllowedETypes)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	allowed := make(map[int]struct{}, len(cfg.AllowedETypes))
	for _, etype := range cfg.AllowedETypes {
		allowed[etype] = struct{}{}
	}
	return &AsPreauthGuard{
		cfg:           cfg,
		etypes:        append([]int(nil), cfg.AllowedETypes...),
		allowed:       allowed,
		pending:       make(map[string]pendingSession),
		pendingCounts: make(map[string]int),
		failures:      make(map[string]int),
		replay:        make(map[replayKey]struct{}),
	}
}

// Policy returns the resolved, read only view for one tenant.
func (g *AsPreauthGuard) Policy(tenant string) Policy {
	if tenant != g.cfg.Tenant {
		return Policy{}
	}
	return Policy{
		Tenant:      g.cfg.Tenant,
		Realm:       g.cfg.Realm,
		MinEType:    g.cfg.MinEType,
		ETypes:      append([]int(nil), g.etypes...),
		ClockSkew:   g.cfg.ClockSkew,
		RequireFAST: g.cfg.RequireFAST,
	}
}

// Begin opens a pre-authentication session and asks the client for a timestamp.
func (g *AsPreauthGuard) Begin(req AsRequest) (AsReply, error) {
	if strings.TrimSpace(req.Client) == "" || strings.TrimSpace(req.Realm) == "" || strings.TrimSpace(req.Tenant) == "" {
		return AsReply{}, ErrBadRequest
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if req.Tenant != g.cfg.Tenant || req.Realm != g.cfg.Realm {
		return AsReply{}, ErrBadRequest
	}
	if g.lockedLocked(req.Client) {
		g.rejected++
		return AsReply{Reason: ReasonFailureLimit}, ErrTooManyFailures
	}
	if g.pendingCounts[req.Client] >= g.cfg.MaxPending {
		g.rejected++
		return AsReply{Reason: ReasonPendingLimit}, ErrPendingLimit
	}

	g.seq++
	sessionID := fmt.Sprintf("as-%d", g.seq)
	g.pending[sessionID] = pendingSession{request: req, seq: g.seq}
	g.pendingCounts[req.Client]++

	return AsReply{
		SessionID: sessionID,
		Status:    StatusContinue,
		Reason:    ReasonPreauthRequired,
		Salt:      saltFor(req.Realm, req.Client),
		ETypes:    append([]int(nil), g.etypes...),
		Seq:       g.seq,
	}, nil
}

// Preauth validates PA-ENC-TIMESTAMP and issues the reply on success.
func (g *AsPreauthGuard) Preauth(sessionID string, pa PreauthData) (AsReply, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	pending, ok := g.pending[sessionID]
	if !ok {
		// A retransmitted request can arrive after the first copy consumed its
		// session. The replay cache, not the missing session, is authoritative.
		if pa.Client != "" && pa.Realm != "" && g.replayContainsLocked(pa.Client, pa.Realm, pa.Time, pa.EType) {
			return AsReply{Reason: ReasonReplay}, ErrReplay
		}
		return AsReply{Reason: ReasonUnknownSession}, ErrUnknownSession
	}

	if strings.TrimSpace(pa.Client) == "" || strings.TrimSpace(pa.Realm) == "" {
		return AsReply{}, ErrBadRequest
	}
	if pa.Client != pending.request.Client || pa.Realm != pending.request.Realm {
		return AsReply{}, ErrBadRequest
	}
	if g.lockedLocked(pending.request.Client) {
		g.rejected++
		return AsReply{Reason: ReasonFailureLimit}, ErrTooManyFailures
	}
	if pending.request.FAST != pa.FAST || (pending.request.FAST && pending.request.Armor != pa.Armor) {
		g.recordFailureLocked(pending.request.Client)
		g.rejected++
		return AsReply{Reason: ReasonArmorMismatch}, ErrArmorMismatch
	}
	if _, ok := g.allowed[pa.EType]; !ok {
		g.recordFailureLocked(pending.request.Client)
		g.rejected++
		return AsReply{Reason: ReasonUnsupportedEType}, ErrUnsupportedEType
	}
	if pa.EType < g.cfg.MinEType {
		g.recordFailureLocked(pending.request.Client)
		g.rejected++
		return AsReply{Reason: ReasonWeakEType}, ErrWeakEType
	}

	skew := pa.Time.Sub(g.cfg.Now())
	if skew > g.cfg.ClockSkew {
		g.recordFailureLocked(pending.request.Client)
		g.rejected++
		return AsReply{Reason: ReasonSkewFuture}, ErrClockSkewFuture
	}
	if skew < -g.cfg.ClockSkew {
		g.recordFailureLocked(pending.request.Client)
		g.rejected++
		return AsReply{Reason: ReasonSkewPast}, ErrClockSkewPast
	}

	if err := g.checkAndInsertReplayLocked(pa.Client, pa.Realm, pa.Time, pa.EType); err != nil {
		g.rejected++
		if errors.Is(err, ErrReplayCacheFull) {
			return AsReply{Reason: ReasonReplayFull}, err
		}
		return AsReply{Reason: ReasonReplay}, err
	}

	delete(g.pending, sessionID)
	g.removePendingCountLocked(pending.request.Client)
	g.seq++
	g.issued++
	return AsReply{
		SessionID: sessionID,
		Status:    StatusIssued,
		Reason:    ReasonOK,
		EType:     pa.EType,
		Salt:      saltFor(pending.request.Realm, pending.request.Client),
		Seq:       g.seq,
	}, nil
}

// CheckReplay decides and records one (client, realm, timestamp, etype) tuple
// in a single atomic step.
func (g *AsPreauthGuard) CheckReplay(client, realm string, ts time.Time, etype int) error {
	if strings.TrimSpace(client) == "" || strings.TrimSpace(realm) == "" {
		return ErrBadRequest
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.checkAndInsertReplayLocked(client, realm, ts, etype)
}

// Pending reads the request waiting behind a session id.
func (g *AsPreauthGuard) Pending(sessionID string) (AsRequest, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	pending, ok := g.pending[sessionID]
	return pending.request, ok
}

// Failures reads the accumulated failure count of one client.
func (g *AsPreauthGuard) Failures(client string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.failures[client]
}

// Stats reports the counters of one tenant.
func (g *AsPreauthGuard) Stats(tenant string) TicketStats {
	if tenant != g.cfg.Tenant {
		return TicketStats{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	failures := 0
	for _, count := range g.failures {
		failures += count
	}
	return TicketStats{
		Tenant:   g.cfg.Tenant,
		Issued:   g.issued,
		Rejected: g.rejected,
		Pending:  len(g.pending),
		Failures: failures,
	}
}

// ReplaySize reports how many tuples the replay cache holds.
func (g *AsPreauthGuard) ReplaySize() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.replay)
}

// Snapshot serialises replay entries, failures and the sequence watermark.
func (g *AsPreauthGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	state := snapshotState{
		Version:  snapshotVersion,
		Seq:      g.seq,
		Failures: make(map[string]int, len(g.failures)),
		Replay:   make([]snapshotReplay, 0, len(g.replay)),
		Pending:  make([]snapshotPending, 0, len(g.pending)),
		Issued:   g.issued,
		Rejected: g.rejected,
	}
	for client, count := range g.failures {
		state.Failures[client] = count
	}
	for key := range g.replay {
		state.Replay = append(state.Replay, snapshotReplay{
			Client:    key.client,
			Realm:     key.realm,
			Timestamp: key.timestamp,
			EType:     key.etype,
		})
	}
	for sessionID, pending := range g.pending {
		state.Pending = append(state.Pending, snapshotPending{
			SessionID: sessionID,
			Request:   pending.request,
			Seq:       pending.seq,
		})
	}
	g.mu.Unlock()

	sort.Slice(state.Replay, func(i, j int) bool {
		a, b := state.Replay[i], state.Replay[j]
		if a.Client != b.Client {
			return a.Client < b.Client
		}
		if a.Realm != b.Realm {
			return a.Realm < b.Realm
		}
		if a.Timestamp != b.Timestamp {
			return a.Timestamp < b.Timestamp
		}
		return a.EType < b.EType
	})
	sort.Slice(state.Pending, func(i, j int) bool {
		return state.Pending[i].SessionID < state.Pending[j].SessionID
	})

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	return json.Marshal(snapshotEnvelope{
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	})
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *AsPreauthGuard) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var envelope snapshotEnvelope
	if err := decodeStrict(data, &envelope); err != nil || envelope.Version != snapshotVersion || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	checksum, err := hex.DecodeString(envelope.Checksum)
	if err != nil || len(checksum) != len(sum) || subtle.ConstantTimeCompare(checksum, sum[:]) != 1 {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := decodeStrict(envelope.Payload, &state); err != nil || state.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if state.Seq < 0 || state.Issued < 0 || state.Rejected < 0 {
		return ErrBadSnapshot
	}

	failures := make(map[string]int, len(state.Failures))
	for client, count := range state.Failures {
		if strings.TrimSpace(client) == "" || count <= 0 || count > g.cfg.MaxFailures {
			return ErrBadSnapshot
		}
		failures[client] = count
	}

	if len(state.Replay) > g.cfg.MaxReplay {
		return ErrBadSnapshot
	}
	replay := make(map[replayKey]struct{}, len(state.Replay))
	for _, entry := range state.Replay {
		if strings.TrimSpace(entry.Client) == "" || strings.TrimSpace(entry.Realm) == "" {
			return ErrBadSnapshot
		}
		key := replayKey{client: entry.Client, realm: entry.Realm, timestamp: entry.Timestamp, etype: entry.EType}
		if _, duplicate := replay[key]; duplicate {
			return ErrBadSnapshot
		}
		replay[key] = struct{}{}
	}

	pending := make(map[string]pendingSession, len(state.Pending))
	pendingCounts := make(map[string]int)
	for _, entry := range state.Pending {
		req := entry.Request
		if entry.SessionID == "" || strings.TrimSpace(req.Client) == "" || strings.TrimSpace(req.Realm) == "" || strings.TrimSpace(req.Tenant) == "" {
			return ErrBadSnapshot
		}
		if req.Tenant != g.cfg.Tenant || req.Realm != g.cfg.Realm {
			return ErrBadSnapshot
		}
		if entry.Seq <= 0 || entry.Seq > state.Seq {
			return ErrBadSnapshot
		}
		if _, duplicate := pending[entry.SessionID]; duplicate {
			return ErrBadSnapshot
		}
		pendingCounts[req.Client]++
		if pendingCounts[req.Client] > g.cfg.MaxPending {
			return ErrBadSnapshot
		}
		pending[entry.SessionID] = pendingSession{request: req, seq: entry.Seq}
	}

	g.mu.Lock()
	g.seq = state.Seq
	g.failures = failures
	g.replay = replay
	g.pending = pending
	g.pendingCounts = pendingCounts
	g.issued = state.Issued
	g.rejected = state.Rejected
	g.mu.Unlock()
	return nil
}

func (g *AsPreauthGuard) lockedLocked(client string) bool {
	return g.failures[client] >= g.cfg.MaxFailures
}

func (g *AsPreauthGuard) recordFailureLocked(client string) {
	if client == "" {
		return
	}
	if g.failures[client] >= g.cfg.MaxFailures {
		return
	}
	g.failures[client]++
}

func (g *AsPreauthGuard) removePendingCountLocked(client string) {
	count := g.pendingCounts[client] - 1
	if count <= 0 {
		delete(g.pendingCounts, client)
		return
	}
	g.pendingCounts[client] = count
}

func (g *AsPreauthGuard) replayContainsLocked(client, realm string, ts time.Time, etype int) bool {
	_, ok := g.replay[replayKeyFor(client, realm, ts, etype)]
	return ok
}

func (g *AsPreauthGuard) checkAndInsertReplayLocked(client, realm string, ts time.Time, etype int) error {
	key := replayKeyFor(client, realm, ts, etype)
	if _, exists := g.replay[key]; exists {
		return ErrReplay
	}
	if len(g.replay) >= g.cfg.MaxReplay {
		return ErrReplayCacheFull
	}
	g.replay[key] = struct{}{}
	return nil
}

func replayKeyFor(client, realm string, ts time.Time, etype int) replayKey {
	return replayKey{client: client, realm: realm, timestamp: ts.UTC().UnixNano(), etype: etype}
}

func saltFor(realm, client string) string {
	return realm + "/" + client
}

func sortedETypes(etypes []int) []int {
	unique := make(map[int]struct{}, len(etypes))
	for _, etype := range etypes {
		unique[etype] = struct{}{}
	}
	sorted := make([]int, 0, len(unique))
	for etype := range unique {
		sorted = append(sorted, etype)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sorted)))
	return sorted
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("snapshot contains multiple values")
		}
		return err
	}
	return nil
}
