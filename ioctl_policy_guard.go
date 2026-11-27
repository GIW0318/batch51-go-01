// Package task governs ioctl admission: how a request number is decoded into
// direction, size, type and number, when a buffer length is acceptable, and
// how the pass through whitelist of one device generation stays isolated from
// the next.
package task

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

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
	nextGen  uint64
}

// NewIoctlPolicyGuard builds a guard with the given limits.
func NewIoctlPolicyGuard(cfg GuardConfig) *IoctlPolicyGuard {
	return &IoctlPolicyGuard{cfg: cfg, devices: map[string]*deviceRecord{}, handlers: map[string]Handler{}, nextGen: 1}
}

// Register installs a rule set on a device and starts a new generation.
func (g *IoctlPolicyGuard) Register(device string, ruleSet RuleSet) error {
	if strings.TrimSpace(device) == "" {
		return ErrBlankDevice
	}
	if len(ruleSet.Rules) == 0 {
		return ErrEmptyRuleSet
	}
	seen := make(map[string]bool, len(ruleSet.Rules))
	for _, rule := range ruleSet.Rules {
		if strings.TrimSpace(rule.ID) == "" {
			return ErrBlankRule
		}
		if seen[rule.ID] {
			return ErrDuplicateRule
		}
		seen[rule.ID] = true
		info := decodeCode(rule.Code)
		if info.Dir == DirNone && info.Size != 0 {
			return ErrRuleMismatch
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	gen := g.nextGen
	g.nextGen++
	rules := make([]Rule, len(ruleSet.Rules))
	for i, rule := range ruleSet.Rules {
		rule.Gen = gen
		rules[i] = rule
	}
	if rec, ok := g.devices[device]; ok {
		rec.Gen = gen
		rec.Rules = rules
	} else {
		g.devices[device] = &deviceRecord{Device: device, Gen: gen, Rules: rules}
	}
	return nil
}

// Check decides whether one request may reach the device.
func (g *IoctlPolicyGuard) Check(device string, req IoctlRequest, cred Cred) (Decision, error) {
	if strings.TrimSpace(device) == "" {
		return Decision{}, ErrBlankDevice
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, ok := g.devices[device]
	if !ok {
		return Decision{}, ErrUnknownDevice
	}
	info := decodeCode(req.Code)
	dec := Decision{
		Device: device,
		Code:   req.Code,
		Dir:    info.Dir,
		Size:   info.Size,
		Type:   info.Type,
		Nr:     info.Nr,
		Gen:    rec.Gen,
	}
	if info.Dir == DirNone && info.Size != 0 {
		dec.Reason = ReasonBadCode
		g.appendAudit(rec, dec)
		return dec, nil
	}
	rule := findRule(rec, req.Code)
	if rule == nil {
		dec.Reason = ReasonNoRule
		g.appendAudit(rec, dec)
		return dec, nil
	}
	dec.RuleID = rule.ID
	switch {
	case rule.Revoked:
		dec.Reason = ReasonRevoked
	case rule.RequireAdmin && !cred.CapAdmin:
		dec.Reason = ReasonPrivilege
	case req.BufLen != int(info.Size):
		dec.Reason = ReasonBadLength
	case !rule.Allow:
		dec.Reason = ReasonRuleDenied
	default:
		dec.Allow = true
		dec.Reason = ReasonAllowed
	}
	g.appendAudit(rec, dec)
	return dec, nil
}

// Forward passes an allowed request through to the device handler.
func (g *IoctlPolicyGuard) Forward(device string, req IoctlRequest, buf []byte) ([]byte, error) {
	if strings.TrimSpace(device) == "" {
		return nil, ErrBlankDevice
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, ok := g.devices[device]
	if !ok {
		return nil, ErrUnknownDevice
	}
	info := decodeCode(req.Code)
	if info.Dir == DirNone && info.Size != 0 {
		return nil, ErrBadBuffer
	}
	rule := findRule(rec, req.Code)
	if rule == nil || rule.Revoked || !rule.Allow {
		dec := Decision{Device: device, Code: req.Code, Dir: info.Dir, Size: info.Size, Type: info.Type, Nr: info.Nr, Gen: rec.Gen}
		switch {
		case rule == nil:
			dec.Reason = ReasonNoRule
		case rule.Revoked:
			dec.RuleID = rule.ID
			dec.Reason = ReasonRevoked
		default:
			dec.RuleID = rule.ID
			dec.Reason = ReasonRuleDenied
		}
		g.appendAudit(rec, dec)
		return nil, ErrNotAllowed
	}
	if req.BufLen != int(info.Size) || len(buf) != req.BufLen {
		dec := Decision{Device: device, Code: req.Code, Dir: info.Dir, Size: info.Size, Type: info.Type, Nr: info.Nr, Gen: rec.Gen, RuleID: rule.ID, Reason: ReasonBadLength}
		g.appendAudit(rec, dec)
		return nil, ErrBadBuffer
	}
	h := g.handlers[device]
	if h == nil {
		return nil, ErrNoHandler
	}
	private := make([]byte, len(buf))
	copy(private, buf)
	out, err := h(req, private)
	if err != nil {
		return nil, err
	}
	dec := Decision{Device: device, Code: req.Code, Dir: info.Dir, Size: info.Size, Type: info.Type, Nr: info.Nr, Gen: rec.Gen, Allow: true, RuleID: rule.ID, Reason: ReasonForwarded}
	g.appendAudit(rec, dec)
	return out, nil
}

// Revoke disables one rule of one device.
func (g *IoctlPolicyGuard) Revoke(device string, ruleID string) error {
	if strings.TrimSpace(device) == "" {
		return ErrBlankDevice
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, ok := g.devices[device]
	if !ok {
		return ErrUnknownDevice
	}
	for i := range rec.Rules {
		if rec.Rules[i].ID == ruleID && rec.Rules[i].Gen == rec.Gen {
			rec.Rules[i].Revoked = true
			return nil
		}
	}
	return ErrUnknownRule
}

// Audit returns the audit trail of one device, oldest first.
func (g *IoctlPolicyGuard) Audit(device string) []AuditEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, ok := g.devices[device]
	if !ok {
		return nil
	}
	out := make([]AuditEntry, len(rec.Audit))
	copy(out, rec.Audit)
	return out
}

// SetHandler installs the device side handler used by Forward.
func (g *IoctlPolicyGuard) SetHandler(device string, h Handler) error {
	if strings.TrimSpace(device) == "" {
		return ErrBlankDevice
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.devices[device]; !ok {
		return ErrUnknownDevice
	}
	g.handlers[device] = h
	return nil
}

// Decode splits a request number into direction, size, type and number.
func (g *IoctlPolicyGuard) Decode(code uint32) IoctlInfo {
	return decodeCode(code)
}

// ReplaceDevice swaps the device behind a name and starts a new generation.
func (g *IoctlPolicyGuard) ReplaceDevice(device string) (uint64, error) {
	if strings.TrimSpace(device) == "" {
		return 0, ErrBlankDevice
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, ok := g.devices[device]
	if !ok {
		return 0, ErrUnknownDevice
	}
	gen := g.nextGen
	g.nextGen++
	rec.Gen = gen
	rec.Rules = nil
	delete(g.handlers, device)
	return gen, nil
}

// Rules returns a copy of the rule set of one device.
func (g *IoctlPolicyGuard) Rules(device string) []Rule {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, ok := g.devices[device]
	if !ok {
		return nil
	}
	out := make([]Rule, len(rec.Rules))
	copy(out, rec.Rules)
	return out
}

// Generation reads the current generation of one device.
func (g *IoctlPolicyGuard) Generation(device string) (uint64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, ok := g.devices[device]
	if !ok {
		return 0, ErrUnknownDevice
	}
	return rec.Gen, nil
}

// snapshotMagic tags a payload produced by Snapshot.
const snapshotMagic = "ioctl-policy-guard/v1"

// snapshot is the serialised form of the whole guard state.
type snapshot struct {
	Magic   string          `json:"magic"`
	NextGen uint64          `json:"next_gen"`
	Devices []*deviceRecord `json:"devices"`
}

// Snapshot serialises the rule sets, the generations and the audit trails.
func (g *IoctlPolicyGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	snap := snapshot{Magic: snapshotMagic, NextGen: g.nextGen}
	for _, rec := range g.devices {
		clone := &deviceRecord{Device: rec.Device, Gen: rec.Gen, AuditSeq: rec.AuditSeq}
		clone.Rules = append([]Rule(nil), rec.Rules...)
		clone.Audit = append([]AuditEntry(nil), rec.Audit...)
		snap.Devices = append(snap.Devices, clone)
	}
	return json.Marshal(snap)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *IoctlPolicyGuard) Restore(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Magic != snapshotMagic || snap.NextGen == 0 {
		return ErrBadSnapshot
	}
	devices := make(map[string]*deviceRecord, len(snap.Devices))
	for _, rec := range snap.Devices {
		if rec == nil || strings.TrimSpace(rec.Device) == "" || rec.Gen == 0 || rec.Gen >= snap.NextGen {
			return ErrBadSnapshot
		}
		if _, dup := devices[rec.Device]; dup {
			return ErrBadSnapshot
		}
		devices[rec.Device] = rec
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.devices = devices
	g.handlers = map[string]Handler{}
	g.nextGen = snap.NextGen
	return nil
}

// decodeCode splits a request number into direction, size, type and number.
func decodeCode(code uint32) IoctlInfo {
	return IoctlInfo{
		Dir:  (code >> IocDirShift) & IocDirMask,
		Size: (code >> IocSizeShift) & IocSizeMask,
		Type: uint8((code >> IocTypeShift) & IocTypeMask),
		Nr:   uint8((code >> IocNrShift) & IocNrMask),
	}
}

// findRule locates the live rule of the current generation for a request code.
func findRule(rec *deviceRecord, code uint32) *Rule {
	for i := range rec.Rules {
		if rec.Rules[i].Code == code && rec.Rules[i].Gen == rec.Gen {
			return &rec.Rules[i]
		}
	}
	return nil
}

// appendAudit records one decision on the device trail with a strict sequence.
func (g *IoctlPolicyGuard) appendAudit(rec *deviceRecord, dec Decision) {
	rec.AuditSeq++
	rec.Audit = append(rec.Audit, AuditEntry{
		Seq:    rec.AuditSeq,
		Device: rec.Device,
		Code:   dec.Code,
		RuleID: dec.RuleID,
		Allow:  dec.Allow,
		Reason: dec.Reason,
		Gen:    rec.Gen,
		At:     time.Now(),
	})
	if max := g.cfg.MaxAuditPerDevice; max > 0 && len(rec.Audit) > max {
		rec.Audit = rec.Audit[len(rec.Audit)-max:]
	}
}
