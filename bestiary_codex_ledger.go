// Package task keeps the monster codex of every account: discoveries from
// several sources, regional completion with a daily gain cap and version
// driven backfill.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// SourceKind tells how an entry was learned.
type SourceKind string

// Source kinds accepted by Discover.
const (
	SourceKill    SourceKind = "kill"
	SourceObserve SourceKind = "observe"
	SourceShare   SourceKind = "share"
)

// Errors reported by the ledger.
var (
	ErrBlankAccount = errors.New("account id is required")
	ErrUnknownEntry = errors.New("codex entry is unknown at this version")
	ErrBadSource    = errors.New("unknown discovery source")
	ErrBadVersion   = errors.New("codex version must be positive")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// CodexConfig is the static codex table of one game build.
type CodexConfig struct {
	Version         int
	DailyRegionGain int
	EntryRegion     map[string]string
	EntryVersion    map[string]int
	RegionEntries   map[string][]string
}

// Source is the provenance of one discovery.
type Source struct {
	Kind        SourceKind
	FromAccount string
}

// KillRecord is one historical kill replayed by Backfill.
type KillRecord struct {
	EntryID string
	At      time.Time
	Source  SourceKind
}

// Discovery describes what one Discover call changed.
type Discovery struct {
	AccountID string
	EntryID   string
	RegionID  string
	First     bool
	Counted   bool
	Deferred  int
	Seq       int64
	At        time.Time
}

// EntryState is the per account record of one entry.
type EntryState struct {
	EntryID    string
	RegionID   string
	Sources    []string
	Introduced int
	Hidden     bool
	FirstAt    time.Time
}

// Progress reports regional completion for one account.
type Progress struct {
	RegionID   string
	Discovered int
	Total      int
	Percent    int
	Backlog    int
	Complete   bool
}

// BackfillResult reports how a historical replay was applied.
type BackfillResult struct {
	Added    []string
	Skipped  []string
	Rewarded int
}

// CodexLedger owns the per account codex state.
type CodexLedger struct {
	cfg CodexConfig
}

// NewCodexLedger builds a ledger from the static codex table.
func NewCodexLedger(cfg CodexConfig) *CodexLedger {
	return &CodexLedger{cfg: cfg}
}

// Discover records one discovery of an entry by an account.
func (l *CodexLedger) Discover(accountID, entryID string, source Source, at time.Time) (Discovery, error) {
	return Discovery{}, errNotImplemented
}

// Progress reports how much of a region the account has completed.
func (l *CodexLedger) Progress(accountID, regionID string) (Progress, error) {
	return Progress{}, errNotImplemented
}

// Backfill replays historical kills for one account.
func (l *CodexLedger) Backfill(accountID string, records []KillRecord) (BackfillResult, error) {
	return BackfillResult{}, errNotImplemented
}

// SetVersion switches the ledger to another codex version.
func (l *CodexLedger) SetVersion(v int) error {
	return errNotImplemented
}

// Version returns the codex version currently in force.
func (l *CodexLedger) Version() int {
	return 0
}

// Entry reads the account record of one entry.
func (l *CodexLedger) Entry(accountID, entryID string) (EntryState, bool) {
	return EntryState{}, false
}

// Pending lists the discoveries waiting for the next day, oldest first.
func (l *CodexLedger) Pending(accountID, regionID string) ([]string, error) {
	return nil, errNotImplemented
}

// Rollover applies the backlog of one day under that day's cap.
func (l *CodexLedger) Rollover(accountID string, day time.Time) (int, error) {
	return 0, errNotImplemented
}

// Hidden lists the entries moved to the hidden zone by a version rollback.
func (l *CodexLedger) Hidden(accountID string) ([]string, error) {
	return nil, errNotImplemented
}

// Snapshot serialises every account record and the backlog.
func (l *CodexLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the ledger state with a snapshot, all or nothing.
func (l *CodexLedger) Restore(data []byte) error {
	return errNotImplemented
}
