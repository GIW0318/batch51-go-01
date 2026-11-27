// Package task governs ioctl admission: how a request number is decoded into
// direction, size, type and number, when a buffer length is acceptable, and
// how the pass through whitelist of one device generation stays isolated from
// the next.
package task

import (
	"errors"
	"sync"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Direction bits of an ioctl request number.
const (
	DirNone      uint32 = 0
	DirWrite     uint32 = 1
	DirRead      uint32 = 2
	DirReadWrite uint32 = 3
)

// Bit layout of an ioctl request number.
const (
	IocNrShift   = 0
	IocTypeShift = 8
	IocSizeShift = 16
	IocDirShift  = 30

	IocNrMask   = 0xff
	IocTypeMask = 0xff
	IocSizeMask = 0x3fff
	IocDirMask  = 0x3
)

// Reason codes attached to every decision and audit entry.
const (
	ReasonAllowed    = "rule_allowed"
	ReasonNoRule     = "no_rule"
	ReasonRuleDenied = "rule_denied"
	ReasonRevoked    = "rule_revoked"
	ReasonPrivilege  = "privilege_required"
	ReasonBadLength  = "buffer_length_mismatch"
	ReasonBadCode    = "request_code_invalid"
	ReasonForwarded  = "forwarded"
)

// Errors returned by the guard.
var (
	ErrBlankDevice   = errors.New("device name is required")
	ErrEmptyRuleSet  = errors.New("rule set has no rules")
	ErrBlankRule     = errors.New("rule id is required")
	ErrDuplicateRule = errors.New("duplicate rule id")
	ErrRuleMismatch  = errors.New("rule does not match its request code")
	ErrUnknownDevice = errors.New("unknown device")
	ErrUnknownRule   = errors.New("unknown rule")
	ErrNotAllowed    = errors.New("request is not allowed on this device")
	ErrNoHandler     = errors.New("device has no forwarding handler")
	ErrBadBuffer     = errors.New("forward buffer does not match the request")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// GuardConfig bounds the audit trail of one device.
type GuardConfig struct {
	MaxAuditPerDevice int
}

// Cred is the caller identity used for admission.
type Cred struct {
	UID      int
	CapAdmin bool
	Gen      uint64
}

// IoctlRequest is one call: a request number plus the buffer length the caller
// intends to hand over.
type IoctlRequest struct {
	Code   uint32
	BufLen int
}

// IoctlInfo is the decoded form of a request number.
type IoctlInfo struct {
	Dir  uint32
	Size uint32
	Type uint8
	Nr   uint8
}

// Rule is one whitelist entry of one device.
type Rule struct {
	ID           string `json:"id"`
	Code         uint32 `json:"code"`
	Name         string `json:"name"`
	Allow        bool   `json:"allow"`
	RequireAdmin bool   `json:"require_admin"`
	Revoked      bool   `json:"revoked"`
	Gen          uint64 `json:"gen"`
}

// RuleSet is what Register installs for one device.
type RuleSet struct {
	Device string
	Rules  []Rule
}

// Decision is the explainable outcome of one Check.
type Decision struct {
	Device string `json:"device"`
	Code   uint32 `json:"code"`
	Dir    uint32 `json:"dir"`
	Size   uint32 `json:"size"`
	Type   uint8  `json:"type"`
	Nr     uint8  `json:"nr"`
	Allow  bool   `json:"allow"`
	RuleID string `json:"rule_id"`
	Reason string `json:"reason"`
	Gen    uint64 `json:"gen"`
}

// AuditEntry is one line of the per device audit trail.
type AuditEntry struct {
	Seq    uint64    `json:"seq"`
	Device string    `json:"device"`
	Code   uint32    `json:"code"`
	RuleID string    `json:"rule_id"`
	Allow  bool      `json:"allow"`
	Reason string    `json:"reason"`
	Gen    uint64    `json:"gen"`
	At     time.Time `json:"at"`
}

// Handler serves a forwarded ioctl on the device side.
type Handler func(req IoctlRequest, in []byte) ([]byte, error)

// deviceRecord is the mutable per device policy state.
type deviceRecord struct {
	Device   string       `json:"device"`
	Gen      uint64       `json:"gen"`
	Rules    []Rule       `json:"rules"`
	Audit    []AuditEntry `json:"audit"`
	AuditSeq uint64       `json:"audit_seq"`
}

// IoctlPolicyGuard owns the per device policy tables and the audit trails.
type IoctlPolicyGuard struct {
	mu       sync.Mutex
	cfg      GuardConfig
	devices  map[string]*deviceRecord
	handlers map[string]Handler
}

// NewIoctlPolicyGuard builds a guard with the given limits.
func NewIoctlPolicyGuard(cfg GuardConfig) *IoctlPolicyGuard {
	return &IoctlPolicyGuard{cfg: cfg}
}

// Register installs a rule set on a device and starts a new generation.
func (g *IoctlPolicyGuard) Register(device string, ruleSet RuleSet) error {
	return errNotImplemented
}

// Check decides whether one request may reach the device.
func (g *IoctlPolicyGuard) Check(device string, req IoctlRequest, cred Cred) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Forward passes an allowed request through to the device handler.
func (g *IoctlPolicyGuard) Forward(device string, req IoctlRequest, buf []byte) ([]byte, error) {
	return nil, errNotImplemented
}

// Revoke disables one rule of one device.
func (g *IoctlPolicyGuard) Revoke(device string, ruleID string) error {
	return errNotImplemented
}

// Audit returns the audit trail of one device, oldest first.
func (g *IoctlPolicyGuard) Audit(device string) []AuditEntry {
	return nil
}

// SetHandler installs the device side handler used by Forward.
func (g *IoctlPolicyGuard) SetHandler(device string, h Handler) error {
	return errNotImplemented
}

// Decode splits a request number into direction, size, type and number.
func (g *IoctlPolicyGuard) Decode(code uint32) IoctlInfo {
	return IoctlInfo{}
}

// ReplaceDevice swaps the device behind a name and starts a new generation.
func (g *IoctlPolicyGuard) ReplaceDevice(device string) (uint64, error) {
	return 0, errNotImplemented
}

// Rules returns a copy of the rule set of one device.
func (g *IoctlPolicyGuard) Rules(device string) []Rule {
	return nil
}

// Generation reads the current generation of one device.
func (g *IoctlPolicyGuard) Generation(device string) (uint64, error) {
	return 0, errNotImplemented
}

// Snapshot serialises the rule sets, the generations and the audit trails.
func (g *IoctlPolicyGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *IoctlPolicyGuard) Restore(data []byte) error {
	return errNotImplemented
}
