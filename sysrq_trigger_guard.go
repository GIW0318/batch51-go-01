// Package task guards the magic SysRq trigger surface: it grades keys, keeps
// the trigger mask by generation and hands out confirmation tickets.
package task

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Key tiers.
const (
	TierAllow   = "allow"
	TierConfirm = "confirm"
	TierDeny    = "deny"
)

// Audit outcomes.
const (
	OutcomeWritten  = "written"
	OutcomeDeduped  = "deduped"
	OutcomeRejected = "rejected"
)

// Mask bits. A mask value of one means every bit is enabled.
const (
	MaskLow     uint64 = 1 << 0
	MaskPersist uint64 = 1 << 1
	MaskCrash   uint64 = 1 << 2
	MaskAll     uint64 = MaskLow | MaskPersist | MaskCrash
)

// Errors reported by the guard.
var (
	ErrUnknownKey      = errors.New("trigger key is not known")
	ErrKeyDisabled     = errors.New("trigger key is disabled by the current mask")
	ErrKeyForbidden    = errors.New("trigger key is permanently forbidden")
	ErrBadMask         = errors.New("trigger mask is not a usable decimal value")
	ErrCallerDenied    = errors.New("caller lacks the required capability")
	ErrLocked          = errors.New("trigger guard is locked at the current level")
	ErrBadLockLevel    = errors.New("lock level is out of range")
	ErrTicketRequired  = errors.New("a confirmation ticket is required")
	ErrTicketInvalid   = errors.New("confirmation ticket is not valid")
	ErrTicketScope     = errors.New("a ticket cannot be issued for this key")
	ErrTicketLimit     = errors.New("outstanding confirmation tickets reached the cap")
	ErrGuardFaulted    = errors.New("guard already handled a crash class key")
	ErrPortUnavailable = errors.New("trigger port is not writable")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

const (
	defaultMaxAudit    = 64
	defaultMaxTickets  = 8
	defaultDedupWindow = 100 * time.Millisecond
	defaultTicketTTL   = 30 * time.Second
	snapshotVersion    = 1
)

// Config bounds the guard.
type Config struct {
	RequiredCap uint64
	MaxAudit    int
	MaxTickets  int
	DedupWindow time.Duration
	TicketTTL   time.Duration
	Now         func() time.Time
}

// Caller is the process asking for a trigger.
type Caller struct {
	PID  int
	UID  int
	Caps uint64
}

// Request is one trigger attempt.
type Request struct {
	Keys   string
	Caller Caller
	Ticket string
}

// Result is the outcome of one accepted or deduplicated trigger.
type Result struct {
	Keys       string
	Written    bool
	Deduped    bool
	Generation uint32
}

// Ticket is a one shot confirmation for a confirm tier key.
type Ticket struct {
	Token      string
	Keys       string
	Generation uint32
}

// KeyVerdict explains how one key of a request is graded.
type KeyVerdict struct {
	Key     string
	Tier    string
	Enabled bool
}

// AuditEntry is one line of the trigger audit log.
type AuditEntry struct {
	Keys      string
	CallerPID int
	Outcome   string
}

// TriggerPort is the write side of the kernel trigger interface.
type TriggerPort interface {
	Write(keys string) error
}

type keySpec struct {
	tier  string
	mask  uint64
	crash bool
}

var sysrqKeys = buildKeyTable()

func buildKeyTable() map[byte]keySpec {
	keys := make(map[byte]keySpec)

	// Informational and low impact keys.
	for _, key := range []byte("defhlmnpqrtvw") {
		keys[key] = keySpec{tier: TierAllow, mask: MaskLow}
	}
	for key := byte('0'); key <= '9'; key++ {
		keys[key] = keySpec{tier: TierAllow, mask: MaskLow}
	}

	// Keys that touch persistent state need confirmation.
	for _, key := range []byte("jsu") {
		keys[key] = keySpec{tier: TierConfirm, mask: MaskPersist}
	}

	// Reboot, crash and power-off are confirm tier and fault the guard after a
	// successful write.
	for _, key := range []byte("bco") {
		keys[key] = keySpec{tier: TierConfirm, mask: MaskCrash, crash: true}
	}

	// These are never made available through the whitelist.
	for _, key := range []byte("ik") {
		keys[key] = keySpec{tier: TierDeny, mask: MaskAll}
	}

	// 'e' and 'f' are explicitly low-risk in the operational policy above.
	keys['e'] = keySpec{tier: TierAllow, mask: MaskLow}
	keys['f'] = keySpec{tier: TierAllow, mask: MaskLow}
	return keys
}

type ticketRecord struct {
	keys       string
	generation uint32
	expiresAt  time.Time
}

type snapshotState struct {
	Version    int          `json:"version"`
	Mask       uint64       `json:"mask"`
	Generation uint32       `json:"generation"`
	LockLevel  int          `json:"lock_level"`
	Faulted    bool         `json:"faulted"`
	Audit      []AuditEntry `json:"audit"`
}

// SysrqTriggerGuard owns the mask generation, the tickets and the audit log.
type SysrqTriggerGuard struct {
	cfg  Config
	port TriggerPort

	mu         sync.Mutex
	mask       uint64
	generation uint32
	lockLevel  int
	faulted    bool
	audit      []AuditEntry
	tickets    map[string]ticketRecord
	dedup      map[string]time.Time
}

var ticketFallbackSeq atomic.Uint64

// NewSysrqTriggerGuard builds a guard over one trigger port.
func NewSysrqTriggerGuard(cfg Config, port TriggerPort) *SysrqTriggerGuard {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxAudit <= 0 {
		cfg.MaxAudit = defaultMaxAudit
	}
	if cfg.MaxTickets <= 0 {
		cfg.MaxTickets = defaultMaxTickets
	}
	if cfg.DedupWindow <= 0 {
		cfg.DedupWindow = defaultDedupWindow
	}
	if cfg.TicketTTL <= 0 {
		cfg.TicketTTL = defaultTicketTTL
	}
	return &SysrqTriggerGuard{
		cfg:        cfg,
		port:       port,
		mask:       MaskAll,
		generation: 1,
		tickets:    make(map[string]ticketRecord),
		dedup:      make(map[string]time.Time),
	}
}

// SetMask installs a new mask and bumps the mask generation.
func (g *SysrqTriggerGuard) SetMask(mask string) error {
	parsed, err := parseMask(mask)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if parsed == g.mask {
		return nil
	}
	g.mask = parsed
	g.generation++
	// A ticket issued under a previous whitelist must never become usable
	// again, even if the same mask is installed later.
	g.tickets = make(map[string]ticketRecord)
	return nil
}

func parseMask(mask string) (uint64, error) {
	if mask == "" {
		return 0, ErrBadMask
	}
	for i := 0; i < len(mask); i++ {
		if mask[i] < '0' || mask[i] > '9' {
			return 0, ErrBadMask
		}
	}
	value, err := strconv.ParseUint(mask, 10, 64)
	if err != nil || value > MaskAll {
		return 0, ErrBadMask
	}
	if value == 1 {
		return MaskAll, nil
	}
	return value, nil
}

// Mask reads the current mask as a canonical decimal bit value.
func (g *SysrqTriggerGuard) Mask() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strconv.FormatUint(g.mask, 10)
}

// MaskGeneration reads the generation the mask was installed with.
func (g *SysrqTriggerGuard) MaskGeneration() uint32 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.generation
}

// Explain grades every key of a request without touching the port.
func (g *SysrqTriggerGuard) Explain(keys string) ([]KeyVerdict, error) {
	specs, err := classifyKeys(keys)
	if err != nil {
		return nil, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	verdicts := make([]KeyVerdict, len(specs))
	for i, spec := range specs {
		verdicts[i] = KeyVerdict{
			Key:     string(keys[i]),
			Tier:    spec.tier,
			Enabled: spec.tier != TierDeny && g.mask&spec.mask != 0,
		}
	}
	return verdicts, nil
}

// IssueTicket mints a one shot ticket for a confirm tier key.
func (g *SysrqTriggerGuard) IssueTicket(keys string, caller Caller) (Ticket, error) {
	specs, err := classifyKeys(keys)
	if err != nil {
		return Ticket{}, err
	}
	if containsForbidden(specs) {
		return Ticket{}, ErrKeyForbidden
	}
	if !hasCapability(caller, g.cfg.RequiredCap) {
		return Ticket{}, ErrCallerDenied
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.lockErrorLocked(specs); err != nil {
		return Ticket{}, err
	}
	if keysDisabled(g.mask, specs) {
		return Ticket{}, ErrKeyDisabled
	}
	if !needsConfirmation(specs) {
		return Ticket{}, ErrTicketScope
	}

	now := g.cfg.Now()
	g.purgeExpiredTicketsLocked(now)
	if len(g.tickets) >= g.cfg.MaxTickets {
		return Ticket{}, ErrTicketLimit
	}

	token := newTicketToken()
	g.tickets[token] = ticketRecord{
		keys:       keys,
		generation: g.generation,
		expiresAt:  now.Add(g.cfg.TicketTTL),
	}
	return Ticket{Token: token, Keys: keys, Generation: g.generation}, nil
}

// Trigger validates one request and, when allowed, writes to the port.
func (g *SysrqTriggerGuard) Trigger(req Request) (Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	result := Result{Keys: req.Keys, Generation: g.generation}
	reject := func(err error) (Result, error) {
		g.recordLocked(req.Keys, req.Caller.PID, OutcomeRejected)
		return result, err
	}

	if g.faulted {
		return reject(ErrGuardFaulted)
	}
	specs, err := classifyKeys(req.Keys)
	if err != nil {
		return reject(err)
	}
	if containsForbidden(specs) {
		return reject(ErrKeyForbidden)
	}
	if !hasCapability(req.Caller, g.cfg.RequiredCap) {
		return reject(ErrCallerDenied)
	}
	if err := g.lockErrorLocked(specs); err != nil {
		return reject(err)
	}
	if keysDisabled(g.mask, specs) {
		return reject(ErrKeyDisabled)
	}

	now := g.cfg.Now()
	if req.Ticket == "" {
		if needsConfirmation(specs) {
			return reject(ErrTicketRequired)
		}
	} else {
		ticket, ok := g.tickets[req.Ticket]
		if !ok {
			return reject(ErrTicketInvalid)
		}
		if ticket.generation != g.generation || ticket.keys != req.Keys || !now.Before(ticket.expiresAt) {
			delete(g.tickets, req.Ticket)
			return reject(ErrTicketInvalid)
		}
		// Consume before writing so a concurrent replay cannot use the same
		// ticket, and so a failed port cannot leave a ticket usable later.
		delete(g.tickets, req.Ticket)
	}

	if last, ok := g.dedup[req.Keys]; ok && now.Sub(last) < g.cfg.DedupWindow {
		result.Deduped = true
		g.recordLocked(req.Keys, req.Caller.PID, OutcomeDeduped)
		return result, nil
	}

	if err := g.writePortLocked(req.Keys); err != nil {
		return reject(err)
	}
	g.dedup[req.Keys] = now
	if containsCrash(specs) {
		g.faulted = true
		g.tickets = make(map[string]ticketRecord)
	}
	result.Written = true
	g.recordLocked(req.Keys, req.Caller.PID, OutcomeWritten)
	return result, nil
}

// SetLockLevel changes the emergency lock level of the guard.
func (g *SysrqTriggerGuard) SetLockLevel(level int) error {
	if level < 0 || level > 2 {
		return ErrBadLockLevel
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lockLevel = level
	return nil
}

// LockLevel reads the current emergency lock level.
func (g *SysrqTriggerGuard) LockLevel() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lockLevel
}

// Faulted reports whether a crash class key was already handled.
func (g *SysrqTriggerGuard) Faulted() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.faulted
}

// Audit returns a copy of the audit log, oldest first.
func (g *SysrqTriggerGuard) Audit() []AuditEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	entries := make([]AuditEntry, len(g.audit))
	copy(entries, g.audit)
	return entries
}

// Snapshot serialises the mask generation, the lock level, the fault state and
// the audit log.
func (g *SysrqTriggerGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := snapshotState{
		Version:    snapshotVersion,
		Mask:       g.mask,
		Generation: g.generation,
		LockLevel:  g.lockLevel,
		Faulted:    g.faulted,
		Audit:      make([]AuditEntry, len(g.audit)),
	}
	copy(state.Audit, g.audit)
	return json.Marshal(state)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *SysrqTriggerGuard) Restore(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return ErrBadSnapshot
	}
	if state.Version != snapshotVersion || state.Mask > MaskAll || state.Generation == 0 || state.LockLevel < 0 || state.LockLevel > 2 {
		return ErrBadSnapshot
	}
	audit := make([]AuditEntry, len(state.Audit))
	copy(audit, state.Audit)
	if len(audit) > g.cfg.MaxAudit {
		trimmed := make([]AuditEntry, g.cfg.MaxAudit)
		copy(trimmed, audit[len(audit)-g.cfg.MaxAudit:])
		audit = trimmed
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.mask = state.Mask
	g.generation = state.Generation
	g.lockLevel = state.LockLevel
	g.faulted = state.Faulted
	g.audit = audit
	g.tickets = make(map[string]ticketRecord)
	g.dedup = make(map[string]time.Time)
	return nil
}

func classifyKeys(keys string) ([]keySpec, error) {
	if keys == "" {
		return nil, ErrUnknownKey
	}
	specs := make([]keySpec, len(keys))
	for i := 0; i < len(keys); i++ {
		spec, ok := sysrqKeys[keys[i]]
		if !ok {
			return nil, ErrUnknownKey
		}
		specs[i] = spec
	}
	return specs, nil
}

func containsForbidden(specs []keySpec) bool {
	for _, spec := range specs {
		if spec.tier == TierDeny {
			return true
		}
	}
	return false
}

func containsCrash(specs []keySpec) bool {
	for _, spec := range specs {
		if spec.crash {
			return true
		}
	}
	return false
}

func needsConfirmation(specs []keySpec) bool {
	for _, spec := range specs {
		if spec.tier == TierConfirm {
			return true
		}
	}
	return false
}

func keysDisabled(mask uint64, specs []keySpec) bool {
	for _, spec := range specs {
		if mask&spec.mask == 0 {
			return true
		}
	}
	return false
}

func hasCapability(caller Caller, required uint64) bool {
	return caller.Caps&required == required
}

func (g *SysrqTriggerGuard) lockErrorLocked(specs []keySpec) error {
	switch g.lockLevel {
	case 2:
		return ErrLocked
	case 1:
		for _, spec := range specs {
			if spec.tier != TierAllow {
				return ErrLocked
			}
		}
	}
	return nil
}

func (g *SysrqTriggerGuard) purgeExpiredTicketsLocked(now time.Time) {
	for token, ticket := range g.tickets {
		if !now.Before(ticket.expiresAt) {
			delete(g.tickets, token)
		}
	}
}

func (g *SysrqTriggerGuard) writePortLocked(keys string) (err error) {
	if g.port == nil {
		return ErrPortUnavailable
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: trigger port panic: %v", ErrPortUnavailable, recovered)
		}
	}()
	if err := g.port.Write(keys); err != nil {
		return fmt.Errorf("%w: %w", ErrPortUnavailable, err)
	}
	return nil
}

func (g *SysrqTriggerGuard) recordLocked(keys string, callerPID int, outcome string) {
	if g.cfg.MaxAudit <= 0 {
		return
	}
	g.audit = append(g.audit, AuditEntry{Keys: keys, CallerPID: callerPID, Outcome: outcome})
	if len(g.audit) > g.cfg.MaxAudit {
		g.audit = append([]AuditEntry(nil), g.audit[len(g.audit)-g.cfg.MaxAudit:]...)
	}
}

func newTicketToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("ticket-%d-%d", time.Now().UnixNano(), ticketFallbackSeq.Add(1))
}
