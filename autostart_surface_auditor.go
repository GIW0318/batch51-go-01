// Package task inventories the autostart surface of a host: systemd units and
// timers, cron and at jobs, shell startup files, desktop autostart entries,
// udev rules and module load config. Entries merge by name across sources, keep
// every declaring source, and are diffed against a stored baseline.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Supported autostart sources.
const (
	SourceSystemdSystem = "systemd_system"
	SourceSystemdUser   = "systemd_user"
	SourceCron          = "cron"
	SourceAt            = "at"
	SourceShellProfile  = "shell_profile"
	SourceDesktop       = "desktop_autostart"
	SourceUdev          = "udev"
	SourceModules       = "modules_load"
)

// Delta kinds reported by Diff.
const (
	DeltaAdded     = "added"
	DeltaModified  = "modified"
	DeltaRemoved   = "removed"
	DeltaTimestamp = "timestamp_only"
)

// Scopes accepted by Scan, Baseline and Diff.
const (
	ScopeAll    = "all"
	ScopeSystem = "system"
	ScopeUser   = "user"
)

// Errors returned by the auditor.
var (
	ErrBadSource        = errors.New("autostart source is unknown")
	ErrBadItem          = errors.New("autostart item path is required")
	ErrUnknownScope     = errors.New("audit scope is unknown")
	ErrScanLimit        = errors.New("autostart entry limit reached")
	ErrNoBaseline       = errors.New("no baseline recorded for this scope")
	ErrUnknownEntry     = errors.New("autostart entry is unknown")
	ErrProtectedEntry   = errors.New("autostart entry is protected")
	ErrBadUntil         = errors.New("allowance expiry is required")
	ErrAllowanceLimit   = errors.New("allowance limit reached")
	ErrUnknownAllowance = errors.New("allowance is not recorded")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// AuditConfig bounds the inventory and the retained baselines.
type AuditConfig struct {
	MaxEntries    int
	MaxBaselines  int
	MaxAllowances int
	Protected     []string
}

// Item is one declaration collected from a source.
type Item struct {
	Source  string
	Path    string
	Name    string
	Content string
	MTime   time.Time
}

// Entry is one merged autostart entry.
type Entry struct {
	ID        string
	Name      string
	Path      string
	Digest    string
	Sources   []string
	Effective string
	MTime     time.Time
	Protected bool
}

// Delta is one difference between the current scan and the baseline.
type Delta struct {
	Kind  string
	ID    string
	Name  string
	Path  string
	Entry Entry
}

// Allowance is one temporary acceptance of a known entry.
type Allowance struct {
	EntryID string
	Until   time.Time
}

// AuditStats counts the inventory and the performed actions.
type AuditStats struct {
	Entries    int
	Baselines  int
	Allowances int
	Removals   int64
}

// AutostartSurfaceAuditor owns the collected items, baselines and allowances.
type AutostartSurfaceAuditor struct {
	cfg AuditConfig
}

// NewAutostartSurfaceAuditor builds an auditor with the given caps.
func NewAutostartSurfaceAuditor(cfg AuditConfig) *AutostartSurfaceAuditor {
	return &AutostartSurfaceAuditor{cfg: cfg}
}

// Register feeds one batch of declarations collected from the sources.
func (a *AutostartSurfaceAuditor) Register(items []Item) error {
	return errNotImplemented
}

// Scan inventories one scope and returns the merged entries by name.
func (a *AutostartSurfaceAuditor) Scan(scope string) ([]Entry, error) {
	return nil, errNotImplemented
}

// Baseline stores the current inventory of one scope for later diffing.
func (a *AutostartSurfaceAuditor) Baseline(scope string) error {
	return errNotImplemented
}

// Diff compares the current inventory with the newest baseline of one scope.
func (a *AutostartSurfaceAuditor) Diff(scope string) ([]Delta, error) {
	return nil, errNotImplemented
}

// Allow accepts one known entry until the given time.
func (a *AutostartSurfaceAuditor) Allow(entryID string, until time.Time) error {
	return errNotImplemented
}

// RevokeAllow drops one recorded allowance.
func (a *AutostartSurfaceAuditor) RevokeAllow(entryID string) error {
	return errNotImplemented
}

// AllowList lists the recorded allowances, oldest first.
func (a *AutostartSurfaceAuditor) AllowList() []Allowance {
	return nil
}

// Remove takes one entry out of the inventory through its source.
func (a *AutostartSurfaceAuditor) Remove(entryID string) error {
	return errNotImplemented
}

// Entries lists the current merged entries of one scope.
func (a *AutostartSurfaceAuditor) Entries(scope string) []Entry {
	return nil
}

// Entry reads one merged entry by id.
func (a *AutostartSurfaceAuditor) Entry(id string) (Entry, bool) {
	return Entry{}, false
}

// SourceNames lists the supported autostart sources.
func (a *AutostartSurfaceAuditor) SourceNames() []string {
	return nil
}

// Stats reads the auditor counters.
func (a *AutostartSurfaceAuditor) Stats() AuditStats {
	return AuditStats{}
}

// Snapshot serialises items, baselines, removals and allowances.
func (a *AutostartSurfaceAuditor) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *AutostartSurfaceAuditor) Restore(data []byte) error {
	return errNotImplemented
}
