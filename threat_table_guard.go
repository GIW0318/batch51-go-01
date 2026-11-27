// Package task keeps the threat table of every monster in a dungeon. It
// accumulates threat per unit, resolves the current target, honours taunts and
// moves a share of the threat from one unit to another.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Audit actions appended by the guard.
const (
	AuditAddThreat  = "add_threat"
	AuditTaunt      = "taunt"
	AuditClearTaunt = "clear_taunt"
	AuditMarkDead   = "mark_dead"
	AuditRevive     = "revive"
	AuditTransfer   = "transfer_threat"
	AuditResetTable = "reset_table"
	AuditClearAll   = "clear_all"
)

// Errors returned by the guard.
var (
	ErrBadConfig      = errors.New("threat configuration is invalid")
	ErrBadThreat      = errors.New("threat entry is invalid")
	ErrUnitDead       = errors.New("unit is dead")
	ErrUnitLimit      = errors.New("threat table unit limit reached")
	ErrUnknownMonster = errors.New("monster not found")
	ErrUnitNotOnTable = errors.New("unit is not on the threat table")
	ErrBadTaunt       = errors.New("taunt window is invalid")
	ErrBadTransfer    = errors.New("threat transfer is invalid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// ThreatConfig bounds the monster table and the audit log.
type ThreatConfig struct {
	MaxMonsters      int
	MaxUnitsPerTable int
	MaxAudit         int
}

// ThreatEntry is one row of a monster threat table.
type ThreatEntry struct {
	MonsterID string
	UnitID    string
	Threat    int64
	LastTick  int64
	Dead      bool
}

// AuditEntry is one recorded guard action.
type AuditEntry struct {
	Seq       int64
	Action    string
	MonsterID string
	UnitID    string
}

// GuardStats counts the work done by the guard.
type GuardStats struct {
	Monsters  int
	Adds      int
	Taunts    int
	Transfers int
	DeadUnits int
	Resets    int
}

// ThreatTableGuard owns the per monster threat tables, the taunt windows and
// the dead unit flags of one dungeon instance.
type ThreatTableGuard struct {
	cfg ThreatConfig
}

// NewThreatTableGuard validates the configuration and builds a guard.
func NewThreatTableGuard(cfg ThreatConfig) (*ThreatTableGuard, error) {
	return nil, errNotImplemented
}

// AddThreat adds threat to one unit on a monster table.
func (g *ThreatTableGuard) AddThreat(monsterID, unitID string, amount int64, atTick int64) error {
	return errNotImplemented
}

// Table returns the threat rows of a monster, highest threat first.
func (g *ThreatTableGuard) Table(monsterID string) []ThreatEntry {
	return nil
}

// CurrentTarget resolves the unit a monster should attack at the given tick.
func (g *ThreatTableGuard) CurrentTarget(monsterID string, atTick int64) (string, bool) {
	return "", false
}

// Taunt forces a monster to attack one unit until the given tick.
func (g *ThreatTableGuard) Taunt(monsterID, unitID string, atTick, untilTick int64) error {
	return errNotImplemented
}

// ClearTaunt removes the forced target of a monster.
func (g *ThreatTableGuard) ClearTaunt(monsterID string) error {
	return errNotImplemented
}

// TauntRemaining reports how many ticks of the taunt window are left.
func (g *ThreatTableGuard) TauntRemaining(monsterID string, atTick int64) int64 {
	return 0
}

// MarkDead flags a unit as dead so it stops competing for the target.
func (g *ThreatTableGuard) MarkDead(unitID string) error {
	return errNotImplemented
}

// Revive clears the dead flag of a unit.
func (g *ThreatTableGuard) Revive(unitID string) error {
	return errNotImplemented
}

// TransferThreat moves a share of one unit's threat to another unit.
func (g *ThreatTableGuard) TransferThreat(monsterID, from, to string, permille int) error {
	return errNotImplemented
}

// ResetTable clears the threat table and taunt of one monster.
func (g *ThreatTableGuard) ResetTable(monsterID string) error {
	return errNotImplemented
}

// HighestThreat returns the highest living row of a monster table.
func (g *ThreatTableGuard) HighestThreat(monsterID string) (ThreatEntry, bool) {
	return ThreatEntry{}, false
}

// ClearAll clears every monster table and returns how many were dropped.
func (g *ThreatTableGuard) ClearAll() int {
	return 0
}

// Audit returns the recorded actions, oldest first.
func (g *ThreatTableGuard) Audit() []AuditEntry {
	return nil
}

// Stats reports the counters of the guard.
func (g *ThreatTableGuard) Stats() GuardStats {
	return GuardStats{}
}

// Snapshot serialises the threat tables, the taunts and the dead unit set.
func (g *ThreatTableGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the guard state with a snapshot, all or nothing.
func (g *ThreatTableGuard) Restore(data []byte) error {
	return errNotImplemented
}
