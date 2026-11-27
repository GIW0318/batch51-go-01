// Package task proxies directory binds: it keeps a small pool of upstream
// connections per security level, re-binds a pooled connection before it
// serves another identity, normalises entry names, and forwards the upstream
// failure reason without ever caching or leaking the credential.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SecurityLevel selects how a bind is protected on the wire.
type SecurityLevel int

// Supported security levels.
const (
	LevelAnonymous SecurityLevel = 0
	LevelSimple    SecurityLevel = 1
	LevelTLS       SecurityLevel = 2
)

// Errors returned by the proxy guard.
var (
	ErrBadDN           = errors.New("directory entry name is malformed")
	ErrBadLevel        = errors.New("security level is not supported")
	ErrEmptyPassword   = errors.New("a non anonymous bind needs a password")
	ErrPoolExhausted   = errors.New("connection pool for this level is full")
	ErrUnknownConn     = errors.New("connection is not known to the pool")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
	ErrUpstreamRefused = errors.New("upstream refused the bind")
)

// UpstreamReply is what the directory server answers for one bind attempt.
type UpstreamReply struct {
	// Code is the directory result code, zero meaning success.
	Code int
	// Message is the diagnostic text the server attached to the result.
	Message string
	// RemainingAttempts is the server side lockout hint. It stays inside the
	// proxy and is never handed to the client.
	RemainingAttempts int
}

// Upstream is the directory server the proxy forwards binds to.
type Upstream interface {
	Bind(dn, password string, level SecurityLevel) (UpstreamReply, error)
}

// ProxyConfig bounds one proxy guard.
type ProxyConfig struct {
	Upstream Upstream
	// MaxConnsPerLevel caps the live connections of one security level.
	MaxConnsPerLevel int
	// RebindBudget is how many rebind attempts a stale connection may spend.
	RebindBudget int
	// LockoutWindow is how long a refused identity is remembered.
	LockoutWindow time.Duration
	// Now is the clock used for pool bookkeeping.
	Now func() time.Time
}

// ConnInfo is the public view of one pooled connection. It never carries the
// credential that was last used on it.
type ConnInfo struct {
	ID     string
	DN     string
	Level  SecurityLevel
	Bound  bool
	Binds  int
	Stale  bool
	Seq    uint64
	IdleAt time.Time
}

// PoolState counts the connections of one security level.
type PoolState struct {
	Level SecurityLevel
	InUse int
	Idle  int
	Total int
}

// ProxyStats counts what the proxy has done since the process started.
type ProxyStats struct {
	Binds      int
	Rebinds    int
	PoolHits   int
	PoolMisses int
	Failures   int
	Released   int
}

// BindError forwards an upstream refusal. It keeps the result code and the
// diagnostic text but never the credential.
type BindError struct {
	Code    int
	Message string
}

// Error renders the forwarded refusal.
func (e *BindError) Error() string {
	return fmt.Sprintf("directory bind rejected: code=%d %s", e.Code, e.Message)
}

// Unwrap lets callers classify a directory refusal as an upstream refusal
// while still recovering the concrete result code with errors.As.
func (e *BindError) Unwrap() error { return ErrUpstreamRefused }

const redactedCredential = "[REDACTED]"

type pooledConn struct {
	info  ConnInfo
	inUse bool
	busy  bool // an upstream bind currently owns this connection
}

type levelPool struct {
	conns    map[string]*pooledConn
	inFlight int // binds for this level currently at the upstream
	creating int // new connections reserved but not yet added to conns
}

type lockoutEntry struct {
	until             time.Time
	code              int
	message           string
	transportError    string
	remainingAttempts int
}

const snapshotVersion = 1

type snapshotConn struct {
	ID     string        `json:"id"`
	DN     string        `json:"dn"`
	Level  SecurityLevel `json:"level"`
	Bound  bool          `json:"bound"`
	Binds  int           `json:"binds"`
	Stale  bool          `json:"stale"`
	Seq    uint64        `json:"seq"`
	IdleAt time.Time     `json:"idle_at"`
	InUse  bool          `json:"in_use"`
}

type snapshotPayload struct {
	Version     int            `json:"version"`
	NextSeq     uint64         `json:"next_seq"`
	Connections []snapshotConn `json:"connections"`
	Stats       ProxyStats     `json:"stats"`
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

// LdapBindProxyGuard owns the connection pool, the per identity locks and the
// counters.
type LdapBindProxyGuard struct {
	cfg ProxyConfig

	mu       sync.Mutex
	changed  *sync.Cond
	conns    map[string]*pooledConn
	pools    [3]levelPool
	nextSeq  uint64
	stats    ProxyStats
	lockouts map[string]lockoutEntry
	gen      uint64

	identityLocks sync.Map // map[string]*sync.Mutex
}

// NewLdapBindProxyGuard builds a proxy guard in front of one directory.
func NewLdapBindProxyGuard(cfg ProxyConfig) *LdapBindProxyGuard {
	if cfg.MaxConnsPerLevel < 0 {
		cfg.MaxConnsPerLevel = 0
	}
	if cfg.RebindBudget < 0 {
		cfg.RebindBudget = 0
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	g := &LdapBindProxyGuard{
		cfg:      cfg,
		conns:    make(map[string]*pooledConn),
		lockouts: make(map[string]lockoutEntry),
	}
	g.changed = sync.NewCond(&g.mu)
	for level := range g.pools {
		g.pools[level].conns = make(map[string]*pooledConn)
	}
	return g
}

func validLevel(level SecurityLevel) bool {
	return level >= LevelAnonymous && level <= LevelTLS
}

// NormalizeDN trims the entry name, lowercases every attribute type and
// collapses the spaces around the separators.
func (g *LdapBindProxyGuard) NormalizeDN(dn string) (string, error) {
	dn = strings.TrimSpace(dn)
	if dn == "" {
		return "", ErrBadDN
	}

	parts := strings.Split(dn, ",")
	normalized := make([]string, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return "", ErrBadDN
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			return "", ErrBadDN
		}
		attribute := strings.TrimSpace(part[:eq])
		if attribute == "" {
			return "", ErrBadDN
		}
		value := strings.TrimSpace(part[eq+1:])
		normalized[i] = strings.ToLower(attribute) + "=" + value
	}
	return strings.Join(normalized, ","), nil
}

func (g *LdapBindProxyGuard) lockIdentity(identity string) func() {
	value, _ := g.identityLocks.LoadOrStore(identity, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func (g *LdapBindProxyGuard) identityKey(dn string) string {
	return dn
}

func (g *LdapBindProxyGuard) now() time.Time {
	return g.cfg.Now()
}

func (g *LdapBindProxyGuard) effectiveRebindRetries() int {
	if g.cfg.RebindBudget <= 0 {
		return 0
	}
	// A connection the upstream closed gets one retry, never a retry storm.
	return 1
}

func sanitizeDiagnostic(message, password string) string {
	if password == "" {
		return message
	}
	return strings.ReplaceAll(message, password, redactedCredential)
}

func shouldRetryStale(reply UpstreamReply, err error) bool {
	if err != nil {
		return true
	}
	switch reply.Code {
	case 52, 53, 81, 91: // unavailable, unwilling, busy, connect error
		return true
	default:
		return false
	}
}

func (g *LdapBindProxyGuard) bindFailure(reply UpstreamReply, err error, password string) error {
	if reply.Code != 0 {
		message := sanitizeDiagnostic(reply.Message, password)
		return &BindError{Code: reply.Code, Message: message}
	}
	if err != nil {
		message := sanitizeDiagnostic(err.Error(), password)
		if message == "" {
			message = ErrUpstreamRefused.Error()
		}
		return fmt.Errorf("%w: %s", ErrUpstreamRefused, message)
	}
	return &BindError{Code: reply.Code, Message: sanitizeDiagnostic(reply.Message, password)}
}

func (g *LdapBindProxyGuard) callUpstream(dn, password string, level SecurityLevel) (UpstreamReply, error) {
	if g.cfg.Upstream == nil {
		return UpstreamReply{}, ErrUpstreamRefused
	}
	return g.cfg.Upstream.Bind(dn, password, level)
}

func (g *LdapBindProxyGuard) callWithStaleRetry(dn, password string, level SecurityLevel, stale bool) (UpstreamReply, error) {
	reply, err := g.callUpstream(dn, password, level)
	if !stale || !shouldRetryStale(reply, err) {
		return reply, err
	}
	for attempt := 0; attempt < g.effectiveRebindRetries(); attempt++ {
		reply, err = g.callUpstream(dn, password, level)
		if !shouldRetryStale(reply, err) {
			break
		}
	}
	return reply, err
}

// lockedOut returns the remembered upstream failure for an identity. It never
// returns the server-side remaining-attempt counter.
func (g *LdapBindProxyGuard) lockedOut(identity string) error {
	if g.cfg.LockoutWindow <= 0 {
		return nil
	}
	entry, ok := g.lockouts[identity]
	if !ok {
		return nil
	}
	if !g.now().Before(entry.until) {
		delete(g.lockouts, identity)
		return nil
	}
	if entry.transportError != "" {
		return fmt.Errorf("%w: %s", ErrUpstreamRefused, entry.transportError)
	}
	return &BindError{Code: entry.code, Message: entry.message}
}

func (g *LdapBindProxyGuard) rememberRefusal(identity string, reply UpstreamReply, err error, password string) {
	if identity == "" || g.cfg.LockoutWindow <= 0 {
		return
	}
	entry := lockoutEntry{
		until:             g.now().Add(g.cfg.LockoutWindow),
		remainingAttempts: reply.RemainingAttempts,
	}
	if reply.Code != 0 {
		entry.code = reply.Code
		entry.message = sanitizeDiagnostic(reply.Message, password)
	} else if err != nil {
		entry.transportError = sanitizeDiagnostic(err.Error(), password)
	}
	g.lockouts[identity] = entry
}

func (g *LdapBindProxyGuard) clearRefusal(identity string) {
	delete(g.lockouts, identity)
}

// pickIdle chooses an idle connection for the level. Connections that already
// carry the identity win; healthy connections win over stale ones. Sequence
// order makes the choice deterministic.
func (g *LdapBindProxyGuard) pickIdle(level SecurityLevel, identity string) *pooledConn {
	pool := &g.pools[level]
	var sameHealthy, sameStale, otherHealthy, otherStale *pooledConn
	for _, conn := range pool.conns {
		if conn.inUse || conn.busy {
			continue
		}
		same := conn.info.DN == identity
		switch {
		case same && !conn.info.Stale:
			if sameHealthy == nil || conn.info.Seq < sameHealthy.info.Seq {
				sameHealthy = conn
			}
		case same && conn.info.Stale:
			if sameStale == nil || conn.info.Seq < sameStale.info.Seq {
				sameStale = conn
			}
		case !same && !conn.info.Stale:
			if otherHealthy == nil || conn.info.Seq < otherHealthy.info.Seq {
				otherHealthy = conn
			}
		case !same && conn.info.Stale:
			if otherStale == nil || conn.info.Seq < otherStale.info.Seq {
				otherStale = conn
			}
		}
	}
	switch {
	case sameHealthy != nil:
		return sameHealthy
	case sameStale != nil:
		return sameStale
	case otherHealthy != nil:
		return otherHealthy
	default:
		return otherStale
	}
}

func (g *LdapBindProxyGuard) nextConnectionID() (string, uint64) {
	g.nextSeq++
	return "conn-" + strconv.FormatUint(g.nextSeq, 10), g.nextSeq
}

func (g *LdapBindProxyGuard) finishBind(
	conn *pooledConn,
	isNew bool,
	generation uint64,
	level SecurityLevel,
	identity string,
	password string,
	reply UpstreamReply,
	upstreamErr error,
) (string, error) {
	if generation != g.gen {
		if conn != nil {
			conn.busy = false
		}
		g.changed.Broadcast()
		if upstreamErr == nil && reply.Code == 0 {
			return "", ErrUnknownConn
		}
		return "", g.bindFailure(reply, upstreamErr, password)
	}

	pool := &g.pools[level]
	pool.inFlight--
	if isNew {
		pool.creating--
	}
	if conn != nil {
		conn.busy = false
	}

	if upstreamErr != nil || reply.Code != 0 {
		if conn != nil {
			delete(g.conns, conn.info.ID)
			delete(pool.conns, conn.info.ID)
		}
		g.stats.Failures++
		g.rememberRefusal(identity, reply, upstreamErr, password)
		g.changed.Broadcast()
		return "", g.bindFailure(reply, upstreamErr, password)
	}

	if conn == nil {
		conn = &pooledConn{}
		id, seq := g.nextConnectionID()
		conn.info.ID = id
		conn.info.Seq = seq
	}
	conn.info.DN = identity
	conn.info.Level = level
	conn.info.Bound = true
	conn.info.Binds++
	conn.info.Stale = false
	conn.info.IdleAt = time.Time{}
	conn.inUse = true
	conn.busy = false
	g.conns[conn.info.ID] = conn
	pool.conns[conn.info.ID] = conn
	g.clearRefusal(identity)
	g.changed.Broadcast()
	return conn.info.ID, nil
}

// Bind authenticates one identity and returns the connection that now carries
// it.
func (g *LdapBindProxyGuard) Bind(dn, password string, level SecurityLevel) (string, error) {
	if !validLevel(level) {
		return "", ErrBadLevel
	}

	normalized, err := g.normalizeForLevel(dn, level)
	if err != nil {
		return "", err
	}
	if level != LevelAnonymous && password == "" {
		return "", ErrEmptyPassword
	}

	identity := g.identityKey(normalized)
	unlock := g.lockIdentity(identity)
	defer unlock()

	g.mu.Lock()
	if err := g.lockedOut(identity); err != nil {
		g.mu.Unlock()
		return "", err
	}

	pool := &g.pools[level]
	if pool.inFlight >= g.cfg.MaxConnsPerLevel {
		g.mu.Unlock()
		return "", ErrPoolExhausted
	}

	conn := g.pickIdle(level, identity)
	isNew := conn == nil
	if conn == nil {
		if len(pool.conns)+pool.creating >= g.cfg.MaxConnsPerLevel {
			g.mu.Unlock()
			return "", ErrPoolExhausted
		}
		pool.creating++
		g.stats.PoolMisses++
	} else {
		conn.busy = true
		conn.inUse = true
		g.stats.PoolHits++
	}
	pool.inFlight++
	g.stats.Binds++
	generation := g.gen
	stale := conn != nil && conn.info.Stale
	g.mu.Unlock()

	reply, upstreamErr := g.callWithStaleRetry(normalized, password, level, stale)

	g.mu.Lock()
	id, err := g.finishBind(conn, isNew, generation, level, identity, password, reply, upstreamErr)
	g.mu.Unlock()
	return id, err
}

func (g *LdapBindProxyGuard) normalizeForLevel(dn string, level SecurityLevel) (string, error) {
	if level == LevelAnonymous && strings.TrimSpace(dn) == "" {
		return "", nil
	}
	return g.NormalizeDN(dn)
}

// Rebind moves a pooled connection to another identity before it is reused.
func (g *LdapBindProxyGuard) Rebind(connID, dn, password string) error {
	g.mu.Lock()
	conn, ok := g.conns[connID]
	if !ok {
		g.mu.Unlock()
		return ErrUnknownConn
	}
	level := conn.info.Level
	g.mu.Unlock()

	normalized, err := g.normalizeForLevel(dn, level)
	if err != nil {
		return err
	}
	if level != LevelAnonymous && password == "" {
		return ErrEmptyPassword
	}

	identity := g.identityKey(normalized)
	unlock := g.lockIdentity(identity)
	defer unlock()

	g.mu.Lock()
	for {
		conn, ok = g.conns[connID]
		if !ok {
			g.mu.Unlock()
			return ErrUnknownConn
		}
		if !conn.busy {
			break
		}
		g.changed.Wait()
	}
	if err := g.lockedOut(identity); err != nil {
		g.mu.Unlock()
		return err
	}
	pool := &g.pools[level]
	if pool.inFlight >= g.cfg.MaxConnsPerLevel {
		g.mu.Unlock()
		return ErrPoolExhausted
	}
	conn.busy = true
	pool.inFlight++
	g.stats.Rebinds++
	generation := g.gen
	stale := conn.info.Stale
	g.mu.Unlock()

	reply, upstreamErr := g.callWithStaleRetry(normalized, password, level, stale)

	g.mu.Lock()
	err = g.finishRebind(conn, generation, level, identity, password, reply, upstreamErr)
	g.mu.Unlock()
	return err
}

func (g *LdapBindProxyGuard) finishRebind(
	conn *pooledConn,
	generation uint64,
	level SecurityLevel,
	identity string,
	password string,
	reply UpstreamReply,
	upstreamErr error,
) error {
	if generation != g.gen {
		conn.busy = false
		g.changed.Broadcast()
		if upstreamErr == nil && reply.Code == 0 {
			return nil
		}
		return g.bindFailure(reply, upstreamErr, password)
	}

	pool := &g.pools[level]
	pool.inFlight--
	conn.busy = false
	if upstreamErr != nil || reply.Code != 0 {
		delete(g.conns, conn.info.ID)
		delete(pool.conns, conn.info.ID)
		g.stats.Failures++
		g.rememberRefusal(identity, reply, upstreamErr, password)
		g.changed.Broadcast()
		return g.bindFailure(reply, upstreamErr, password)
	}

	conn.info.DN = identity
	conn.info.Bound = true
	conn.info.Binds++
	conn.info.Stale = false
	g.clearRefusal(identity)
	g.changed.Broadcast()
	return nil
}

// Release returns a connection to the idle side of the pool.
func (g *LdapBindProxyGuard) Release(connID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		conn, ok := g.conns[connID]
		if !ok {
			return ErrUnknownConn
		}
		if conn.busy {
			g.changed.Wait()
			continue
		}
		if conn.inUse {
			conn.inUse = false
			conn.info.IdleAt = g.now()
			g.stats.Released++
			g.changed.Broadcast()
		}
		return nil
	}
}

// Conn reads one pooled connection.
func (g *LdapBindProxyGuard) Conn(connID string) (ConnInfo, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	conn, ok := g.conns[connID]
	if !ok {
		return ConnInfo{}, false
	}
	return conn.info, true
}

// PoolState reports the counters of one security level.
func (g *LdapBindProxyGuard) PoolState(level SecurityLevel) PoolState {
	state := PoolState{Level: level}
	if !validLevel(level) {
		return state
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, conn := range g.pools[level].conns {
		state.Total++
		if conn.inUse {
			state.InUse++
		} else {
			state.Idle++
		}
	}
	return state
}

// MarkStale records that the upstream closed a pooled connection.
func (g *LdapBindProxyGuard) MarkStale(connID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	conn, ok := g.conns[connID]
	if !ok {
		return ErrUnknownConn
	}
	conn.info.Stale = true
	return nil
}

// Stats returns the proxy wide counters.
func (g *LdapBindProxyGuard) Stats() ProxyStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// Snapshot serialises the pool, the counters and the sequence watermark.
func (g *LdapBindProxyGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	payload := snapshotPayload{
		Version:     snapshotVersion,
		NextSeq:     g.nextSeq,
		Connections: make([]snapshotConn, 0, len(g.conns)),
		Stats:       g.stats,
	}
	for _, conn := range g.conns {
		payload.Connections = append(payload.Connections, snapshotConn{
			ID:     conn.info.ID,
			DN:     conn.info.DN,
			Level:  conn.info.Level,
			Bound:  conn.info.Bound,
			Binds:  conn.info.Binds,
			Stale:  conn.info.Stale,
			Seq:    conn.info.Seq,
			IdleAt: conn.info.IdleAt,
			InUse:  conn.inUse,
		})
	}
	sort.Slice(payload.Connections, func(i, j int) bool {
		return payload.Connections[i].Seq < payload.Connections[j].Seq
	})
	state, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	sum := sha256.Sum256(state)
	return json.Marshal(snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		State:    state,
	})
}

func snapshotIDForSeq(seq uint64) string {
	return "conn-" + strconv.FormatUint(seq, 10)
}

func nonNegativeStats(stats ProxyStats) bool {
	return stats.Binds >= 0 && stats.Rebinds >= 0 && stats.PoolHits >= 0 &&
		stats.PoolMisses >= 0 && stats.Failures >= 0 && stats.Released >= 0
}

func decodeStrictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
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

// Restore replaces the pool with a snapshot, all or nothing.
func (g *LdapBindProxyGuard) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := decodeStrictJSON(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || len(envelope.State) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.State)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := decodeStrictJSON(envelope.State, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != snapshotVersion || !nonNegativeStats(payload.Stats) {
		return ErrBadSnapshot
	}

	type restored struct {
		conns map[string]*pooledConn
		pools [3]levelPool
	}
	next := &restored{
		conns: make(map[string]*pooledConn, len(payload.Connections)),
	}
	for i := range next.pools {
		next.pools[i].conns = make(map[string]*pooledConn)
	}

	watermark := payload.NextSeq
	for _, saved := range payload.Connections {
		if saved.ID == "" || saved.Seq == 0 || saved.Binds < 1 || !saved.Bound {
			return ErrBadSnapshot
		}
		if saved.ID != snapshotIDForSeq(saved.Seq) {
			return ErrBadSnapshot
		}
		if !validLevel(saved.Level) {
			return ErrBadSnapshot
		}
		normalized, err := g.normalizeForLevel(saved.DN, saved.Level)
		if err != nil || normalized != saved.DN {
			return ErrBadSnapshot
		}
		if _, exists := next.conns[saved.ID]; exists {
			return ErrBadSnapshot
		}
		if saved.Seq > watermark {
			return ErrBadSnapshot
		}

		conn := &pooledConn{
			info: ConnInfo{
				ID:     saved.ID,
				DN:     saved.DN,
				Level:  saved.Level,
				Bound:  saved.Bound,
				Binds:  saved.Binds,
				Stale:  saved.Stale,
				Seq:    saved.Seq,
				IdleAt: saved.IdleAt,
			},
			inUse: saved.InUse,
		}
		next.conns[conn.info.ID] = conn
		next.pools[saved.Level].conns[conn.info.ID] = conn
	}

	for level := SecurityLevel(0); level <= LevelTLS; level++ {
		if len(next.pools[level].conns) > g.cfg.MaxConnsPerLevel {
			return ErrBadSnapshot
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if watermark < g.nextSeq {
		watermark = g.nextSeq
	}
	g.conns = next.conns
	g.pools = next.pools
	g.nextSeq = watermark
	g.stats = payload.Stats
	g.lockouts = make(map[string]lockoutEntry)
	g.gen++
	g.changed.Broadcast()
	return nil
}
