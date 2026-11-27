// Package task guards the magic SysRq trigger surface: it grades keys, keeps
// the trigger mask by generation and hands out confirmation tickets.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

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

// SysrqTriggerGuard owns the mask generation, the tickets and the audit log.
type SysrqTriggerGuard struct {
	cfg  Config
	port TriggerPort
}

// NewSysrqTriggerGuard builds a guard over one trigger port.
func NewSysrqTriggerGuard(cfg Config, port TriggerPort) *SysrqTriggerGuard {
	return &SysrqTriggerGuard{cfg: cfg, port: port}
}

// SetMask installs a new mask and bumps the mask generation.
func (g *SysrqTriggerGuard) SetMask(mask string) error {
	return errNotImplemented
}

// Mask reads the current mask as a canonical decimal bit value.
func (g *SysrqTriggerGuard) Mask() string {
	return ""
}

// MaskGeneration reads the generation the mask was installed with.
func (g *SysrqTriggerGuard) MaskGeneration() uint32 {
	return 0
}

// Explain grades every key of a request without touching the port.
func (g *SysrqTriggerGuard) Explain(keys string) ([]KeyVerdict, error) {
	return nil, errNotImplemented
}

// IssueTicket mints a one shot ticket for a confirm tier key.
func (g *SysrqTriggerGuard) IssueTicket(keys string, caller Caller) (Ticket, error) {
	return Ticket{}, errNotImplemented
}

// Trigger validates one request and, when allowed, writes to the port.
func (g *SysrqTriggerGuard) Trigger(req Request) (Result, error) {
	return Result{}, errNotImplemented
}

// SetLockLevel changes the emergency lock level of the guard.
func (g *SysrqTriggerGuard) SetLockLevel(level int) error {
	return errNotImplemented
}

// LockLevel reads the current emergency lock level.
func (g *SysrqTriggerGuard) LockLevel() int {
	return 0
}

// Faulted reports whether a crash class key was already handled.
func (g *SysrqTriggerGuard) Faulted() bool {
	return false
}

// Audit returns a copy of the audit log, oldest first.
func (g *SysrqTriggerGuard) Audit() []AuditEntry {
	return nil
}

// Snapshot serialises the mask generation, the lock level, the fault state and
// the audit log.
func (g *SysrqTriggerGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *SysrqTriggerGuard) Restore(data []byte) error {
	return errNotImplemented
}
