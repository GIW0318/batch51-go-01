// Package task settles one equipment enhancement attempt at a time: it books
// the materials, rolls the curve, moves the item level, tracks the per item
// pity counter, spends protective charms and replays idempotent requests.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Errors reported by the ledger.
var (
	ErrBadCurve               = errors.New("enhancement curve is invalid")
	ErrBadPity                = errors.New("pity configuration is invalid")
	ErrBadInput               = errors.New("player, item and request key are required")
	ErrBadLevel               = errors.New("level is outside the curve")
	ErrMaxLevel               = errors.New("item is already at the maximum level")
	ErrDamaged                = errors.New("item must be repaired before the next attempt")
	ErrNotDamaged             = errors.New("item is not damaged")
	ErrInsufficientMaterials  = errors.New("not enough materials")
	ErrNoCharm                = errors.New("no protective charm in the bag")
	ErrUnknownItem            = errors.New("unknown item")
	ErrBadSnapshot            = errors.New("snapshot payload is invalid")
)

// EnhancementCurve is the level table of one enhancement line. Rates holds one
// success chance per mille for every level below MaxLevel.
type EnhancementCurve struct {
	ID                  string
	MaxLevel            int
	Rates               []int
	MaterialsPerAttempt int
	GoldPerAttempt      int64
	DowngradeOnFail     bool
}

// PityConfig is the guaranteed success rule. A zero Threshold disables it.
type PityConfig struct {
	Threshold      int
	ResetOnSuccess bool
}

// LedgerConfig bounds one ledger.
type LedgerConfig struct {
	Seed       int64
	MaxHistory int
	MaxPity    int
}

// AttemptResult is the booked outcome of one attempt.
type AttemptResult struct {
	Player         string
	ItemID         string
	IdemKey        string
	FromLevel      int
	ToLevel        int
	Success        bool
	PityApplied    bool
	CharmUsed      bool
	MaterialsSpent int
	GoldSpent      int64
	Replayed       bool
}

// Odds is the read only view of the next attempt on one item.
type Odds struct {
	ItemID          string
	Level           int
	SuccessPerMille int
	PityCount       int
	PityThreshold   int
	Materials       int
	Gold            int64
}

// AttemptRecord is one entry of the per item log.
type AttemptRecord struct {
	Seq         int64
	Player      string
	ItemID      string
	FromLevel   int
	ToLevel     int
	Success     bool
	PityApplied bool
	CharmUsed   bool
}

// EnhanceStats summarises one ledger.
type EnhanceStats struct {
	Items      int
	Attempts   int
	Successes  int
	Failures   int
	Replays    int
	CharmsUsed int
}

// EnhancementAttemptLedger owns the item levels, the material bags and the
// request log.
type EnhancementAttemptLedger struct {
	cfg LedgerConfig
}

// NewEnhancementAttemptLedger builds a ledger with the given bounds.
func NewEnhancementAttemptLedger(cfg LedgerConfig) *EnhancementAttemptLedger {
	return &EnhancementAttemptLedger{cfg: cfg}
}

// SetCurve installs the enhancement curve.
func (l *EnhancementAttemptLedger) SetCurve(c EnhancementCurve) error {
	return errNotImplemented
}

// SetPity installs the guaranteed success rule.
func (l *EnhancementAttemptLedger) SetPity(p PityConfig) error {
	return errNotImplemented
}

// GrantMaterials adds materials to one player bag.
func (l *EnhancementAttemptLedger) GrantMaterials(player string, n int) error {
	return errNotImplemented
}

// GrantCharms adds protective charms to one player bag.
func (l *EnhancementAttemptLedger) GrantCharms(player string, n int) error {
	return errNotImplemented
}

// Materials reads the material count of one player.
func (l *EnhancementAttemptLedger) Materials(player string) int {
	return 0
}

// Charms reads the charm count of one player.
func (l *EnhancementAttemptLedger) Charms(player string) int {
	return 0
}

// SetLevel overwrites the level of one item.
func (l *EnhancementAttemptLedger) SetLevel(itemID string, level int) error {
	return errNotImplemented
}

// Level reads the level of one item.
func (l *EnhancementAttemptLedger) Level(itemID string) (int, error) {
	return 0, errNotImplemented
}

// Preview reports the odds and the cost of the next attempt.
func (l *EnhancementAttemptLedger) Preview(player string, itemID string) (Odds, error) {
	return Odds{}, errNotImplemented
}

// Attempt books one enhancement attempt.
func (l *EnhancementAttemptLedger) Attempt(player string, itemID string, useCharm bool, idemKey string) (AttemptResult, error) {
	return AttemptResult{}, errNotImplemented
}

// Repair clears the damaged state of one item.
func (l *EnhancementAttemptLedger) Repair(itemID string) error {
	return errNotImplemented
}

// History reads the attempt log of one item, oldest first.
func (l *EnhancementAttemptLedger) History(itemID string) ([]AttemptRecord, error) {
	return nil, errNotImplemented
}

// Stats reads the counters of the ledger.
func (l *EnhancementAttemptLedger) Stats() EnhanceStats {
	return EnhanceStats{}
}

// Snapshot serialises curves, levels, bags and the request log.
func (l *EnhancementAttemptLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *EnhancementAttemptLedger) Restore(data []byte) error {
	return errNotImplemented
}
