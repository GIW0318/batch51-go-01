// Package task keeps the collection codex of every account: it books discovery
// events, unlocks variants, awards finished sets once, converts surplus copies
// and remaps the entries when the catalog version changes.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Errors reported by the codex.
var (
	ErrBadEntry      = errors.New("catalog entry is invalid")
	ErrDuplicateEntry = errors.New("catalog entry already exists")
	ErrUnknownEntry  = errors.New("unknown catalog entry")
	ErrBadSet        = errors.New("set definition is invalid")
	ErrDuplicateSet  = errors.New("set already exists")
	ErrUnknownSet    = errors.New("unknown set")
	ErrBadEvent      = errors.New("discovery event is invalid")
	ErrBadAccount    = errors.New("account id is required")
	ErrBadQty        = errors.New("quantity is outside the allowed range")
	ErrNoDuplicate   = errors.New("entry has no surplus copy to convert")
	ErrDailyLimit    = errors.New("daily conversion budget reached")
	ErrSetIncomplete = errors.New("set is not complete yet")
	ErrAlreadyClaimed = errors.New("set reward was already claimed")
	ErrUnknownVersion = errors.New("unknown catalog version")
	ErrBadVersion    = errors.New("catalog version order is invalid")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// CatalogEntry is one collectible of the catalog.
type CatalogEntry struct {
	ID      string
	Name    string
	SetID   string
	Hidden  bool
	Removed bool
}

// EntrySet is one set of the catalog and its one shot reward.
type EntrySet struct {
	ID           string
	EntryIDs     []string
	RewardID     string
	RewardAmount int
}

// VersionSpec is one catalog version and the entry ids it renames or removes.
type VersionSpec struct {
	ID      string
	Order   int
	Renames map[string]string
	Removed []string
}

// DiscoverEvent is one reported discovery.
type DiscoverEvent struct {
	EventID string
	EntryID string
	Variant string
	Qty     int
}

// DiscoverResult is the booked outcome of one discovery.
type DiscoverResult struct {
	EntryID         string
	Count           int
	Duplicate       bool
	VariantUnlocked bool
	CompletedSets   []string
	Replayed        bool
}

// SetReward is the answer of one claim.
type SetReward struct {
	SetID          string
	RewardID       string
	Amount         int
	AlreadyClaimed bool
	Missing        []string
}

// ConvertResult is the answer of one duplicate conversion.
type ConvertResult struct {
	EntryID    string
	Consumed   int
	Produced   int
	DailyUsed  int
	DailyLimit int
}

// Reason explains one entry of the codex.
type Reason struct {
	Kind   string
	Detail string
}

// CodexStats summarises one ledger.
type CodexStats struct {
	Entries       int
	Sets          int
	Accounts      int
	Discovered    int
	Variants      int
	SetsCompleted int
	SetsClaimed   int
	Converted     int
	Legacy        int
}

// CodexConfig bounds one ledger.
type CodexConfig struct {
	MaxEntriesPerAccount int
	MaxEvents            int
	MaxDailyConvert      int
	ConvertRate          int
}

// CollectionCodexLedger owns the catalog, the account codexes and the version
// table.
type CollectionCodexLedger struct {
	cfg CodexConfig
}

// NewCollectionCodexLedger builds a ledger with the given bounds.
func NewCollectionCodexLedger(cfg CodexConfig) *CollectionCodexLedger {
	return &CollectionCodexLedger{cfg: cfg}
}

// RegisterEntry adds one catalog entry.
func (l *CollectionCodexLedger) RegisterEntry(e CatalogEntry) error {
	return errNotImplemented
}

// RegisterSet adds one set and its reward.
func (l *CollectionCodexLedger) RegisterSet(s EntrySet) error {
	return errNotImplemented
}

// RegisterVersion adds one catalog version.
func (l *CollectionCodexLedger) RegisterVersion(v VersionSpec) error {
	return errNotImplemented
}

// Discover books one discovery event.
func (l *CollectionCodexLedger) Discover(acct string, ev DiscoverEvent) (DiscoverResult, error) {
	return DiscoverResult{}, errNotImplemented
}

// Reveal lifts the hidden flag of one entry for one account.
func (l *CollectionCodexLedger) Reveal(acct string, entryID string) error {
	return errNotImplemented
}

// UnlockVariant unlocks one variant of one entry.
func (l *CollectionCodexLedger) UnlockVariant(acct string, entryID string, variant string) error {
	return errNotImplemented
}

// ClaimSet pays the reward of one finished set.
func (l *CollectionCodexLedger) ClaimSet(acct string, setID string) (SetReward, error) {
	return SetReward{}, errNotImplemented
}

// ConvertDuplicate turns surplus copies into shards.
func (l *CollectionCodexLedger) ConvertDuplicate(acct string, entryID string, qty int) (ConvertResult, error) {
	return ConvertResult{}, errNotImplemented
}

// Migrate remaps the codex of one account to a newer catalog version.
func (l *CollectionCodexLedger) Migrate(acct string, fromVer, toVer string) error {
	return errNotImplemented
}

// Explain lists the reasons behind one entry of the codex.
func (l *CollectionCodexLedger) Explain(acct string, entryID string) []Reason {
	return nil
}

// Stats reads the counters of the ledger.
func (l *CollectionCodexLedger) Stats() CodexStats {
	return CodexStats{}
}

// Snapshot serialises the catalog, the version table and the account codexes.
func (l *CollectionCodexLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *CollectionCodexLedger) Restore(data []byte) error {
	return errNotImplemented
}
