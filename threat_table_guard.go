// Package task keeps the threat table of every monster in a dungeon. It
// accumulates threat per unit, resolves the current target, honours taunts and
// moves a share of the threat from one unit to another.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

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

const snapshotVersion = 1

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

type threatRow struct {
	unitID   string
	threat   int64
	lastTick int64
	order    int64
}

type tauntWindow struct {
	unitID    string
	startTick int64
	untilTick int64
}

type monsterTable struct {
	rows  map[string]*threatRow
	taunt *tauntWindow
}

// ThreatTableGuard owns the per monster threat tables, the taunt windows and
// the dead unit flags of one dungeon instance.
type ThreatTableGuard struct {
	cfg ThreatConfig

	mu        sync.RWMutex
	monsters  map[string]*monsterTable
	dead      map[string]struct{}
	audit     []AuditEntry
	seq       int64
	nextOrder int64
	stats     GuardStats
}

type snapshotEntry struct {
	UnitID   string `json:"unit_id"`
	Threat   int64  `json:"threat"`
	LastTick int64  `json:"last_tick"`
	Order    int64  `json:"order"`
	Dead     bool   `json:"dead"`
}

type snapshotTaunt struct {
	UnitID    string `json:"unit_id"`
	StartTick int64  `json:"start_tick"`
	UntilTick int64  `json:"until_tick"`
}

type snapshotMonster struct {
	Entries []snapshotEntry `json:"entries"`
	Taunt   *snapshotTaunt  `json:"taunt,omitempty"`
}

type snapshotData struct {
	Version   int                        `json:"version"`
	Monsters  map[string]snapshotMonster `json:"monsters"`
	DeadUnits []string                   `json:"dead_units"`
	Seq       int64                      `json:"seq"`
	NextOrder int64                      `json:"next_order"`
	Audit     []AuditEntry               `json:"audit"`
	Stats     GuardStats                 `json:"stats"`
}

// NewThreatTableGuard validates the configuration and builds a guard.
func NewThreatTableGuard(cfg ThreatConfig) (*ThreatTableGuard, error) {
	if cfg.MaxMonsters <= 0 || cfg.MaxUnitsPerTable <= 0 || cfg.MaxAudit <= 0 {
		return nil, ErrBadConfig
	}
	return &ThreatTableGuard{
		cfg:      cfg,
		monsters: make(map[string]*monsterTable),
		dead:     make(map[string]struct{}),
	}, nil
}

// AddThreat adds threat to one unit on a monster table.
func (g *ThreatTableGuard) AddThreat(monsterID, unitID string, amount int64, atTick int64) error {
	if strings.TrimSpace(monsterID) == "" || strings.TrimSpace(unitID) == "" || amount <= 0 || atTick < 0 {
		return ErrBadThreat
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, dead := g.dead[unitID]; dead {
		return ErrUnitDead
	}

	table, exists := g.monsters[monsterID]
	if !exists {
		if len(g.monsters) >= g.cfg.MaxMonsters {
			return ErrUnitLimit
		}
		table = &monsterTable{rows: make(map[string]*threatRow)}
		g.monsters[monsterID] = table
	}

	row, exists := table.rows[unitID]
	if !exists {
		if len(table.rows) >= g.cfg.MaxUnitsPerTable {
			return ErrUnitLimit
		}
		g.nextOrder++
		row = &threatRow{unitID: unitID, order: g.nextOrder}
		table.rows[unitID] = row
	}

	row.threat = addInt64Saturating(row.threat, amount)
	if atTick > row.lastTick {
		row.lastTick = atTick
	}
	if row.order == 0 {
		g.nextOrder++
		row.order = g.nextOrder
	}

	g.stats.Adds++
	g.recordAudit(AuditAddThreat, monsterID, unitID)
	return nil
}

// Table returns the threat rows of a monster, highest threat first.
func (g *ThreatTableGuard) Table(monsterID string) []ThreatEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()

	table := g.monsters[monsterID]
	if table == nil || len(table.rows) == 0 {
		return nil
	}

	out := make([]ThreatEntry, 0, len(table.rows))
	for _, row := range table.rows {
		_, dead := g.dead[row.unitID]
		out = append(out, ThreatEntry{
			MonsterID: monsterID,
			UnitID:    row.unitID,
			Threat:    row.threat,
			LastTick:  row.lastTick,
			Dead:      dead,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := table.rows[out[i].UnitID], table.rows[out[j].UnitID]
		return rowBefore(left, right)
	})
	return out
}

// CurrentTarget resolves the unit a monster should attack at the given tick.
func (g *ThreatTableGuard) CurrentTarget(monsterID string, atTick int64) (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	table := g.monsters[monsterID]
	if table == nil {
		return "", false
	}
	if table.taunt != nil && atTick >= table.taunt.startTick && atTick < table.taunt.untilTick {
		if row := table.rows[table.taunt.unitID]; row != nil {
			if _, dead := g.dead[row.unitID]; !dead {
				return row.unitID, true
			}
		}
	}

	row := g.highestLivingRowLocked(table)
	if row == nil {
		return "", false
	}
	return row.unitID, true
}

// Taunt forces a monster to attack one unit until the given tick.
func (g *ThreatTableGuard) Taunt(monsterID, unitID string, atTick, untilTick int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	table := g.monsters[monsterID]
	if table == nil || table.rows[unitID] == nil {
		return ErrUnitNotOnTable
	}
	if untilTick <= atTick {
		return ErrBadTaunt
	}

	table.taunt = &tauntWindow{unitID: unitID, startTick: atTick, untilTick: untilTick}
	g.stats.Taunts++
	g.recordAudit(AuditTaunt, monsterID, unitID)
	return nil
}

// ClearTaunt removes the forced target of a monster.
func (g *ThreatTableGuard) ClearTaunt(monsterID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	table := g.monsters[monsterID]
	if table == nil {
		return ErrUnknownMonster
	}
	if table.taunt != nil {
		unitID := table.taunt.unitID
		table.taunt = nil
		g.recordAudit(AuditClearTaunt, monsterID, unitID)
	}
	return nil
}

// TauntRemaining reports how many ticks of the taunt window are left.
func (g *ThreatTableGuard) TauntRemaining(monsterID string, atTick int64) int64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	table := g.monsters[monsterID]
	if table == nil || table.taunt == nil {
		return 0
	}
	taunt := table.taunt
	if atTick < taunt.startTick || atTick >= taunt.untilTick {
		return 0
	}
	return taunt.untilTick - atTick
}

// MarkDead flags a unit as dead so it stops competing for the target.
func (g *ThreatTableGuard) MarkDead(unitID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.dead[unitID]; exists {
		return nil
	}
	g.dead[unitID] = struct{}{}
	g.recordAudit(AuditMarkDead, "", unitID)
	return nil
}

// Revive clears the dead flag of a unit.
func (g *ThreatTableGuard) Revive(unitID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.dead[unitID]; !exists {
		return nil
	}
	delete(g.dead, unitID)
	g.recordAudit(AuditRevive, "", unitID)
	return nil
}

// TransferThreat moves a share of one unit's threat to another unit.
func (g *ThreatTableGuard) TransferThreat(monsterID, from, to string, permille int) error {
	if permille <= 0 || permille > 1000 || from == to {
		return ErrBadTransfer
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	table := g.monsters[monsterID]
	if table == nil {
		return ErrUnitNotOnTable
	}
	source := table.rows[from]
	target := table.rows[to]
	if source == nil || target == nil {
		return ErrUnitNotOnTable
	}

	amount := scaleInt64(source.threat, permille)
	source.threat -= amount
	target.threat = addInt64Saturating(target.threat, amount)
	g.stats.Transfers++
	g.recordAudit(AuditTransfer, monsterID, from)
	return nil
}

// ResetTable clears the threat table and taunt of one monster.
func (g *ThreatTableGuard) ResetTable(monsterID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.monsters[monsterID]; !exists {
		return ErrUnknownMonster
	}
	delete(g.monsters, monsterID)
	g.stats.Resets++
	g.recordAudit(AuditResetTable, monsterID, "")
	return nil
}

// HighestThreat returns the highest living row of a monster table.
func (g *ThreatTableGuard) HighestThreat(monsterID string) (ThreatEntry, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	table := g.monsters[monsterID]
	if table == nil {
		return ThreatEntry{}, false
	}
	row := g.highestLivingRowLocked(table)
	if row == nil {
		return ThreatEntry{}, false
	}
	return ThreatEntry{
		MonsterID: monsterID,
		UnitID:    row.unitID,
		Threat:    row.threat,
		LastTick:  row.lastTick,
		Dead:      false,
	}, true
}

// ClearAll clears every monster table and returns how many were dropped.
func (g *ThreatTableGuard) ClearAll() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	count := len(g.monsters)
	g.monsters = make(map[string]*monsterTable)
	if count > 0 {
		g.recordAudit(AuditClearAll, "", "")
	}
	return count
}

// Audit returns the recorded actions, oldest first.
func (g *ThreatTableGuard) Audit() []AuditEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()

	out := make([]AuditEntry, len(g.audit))
	copy(out, g.audit)
	return out
}

// Stats reports the counters of the guard.
func (g *ThreatTableGuard) Stats() GuardStats {
	g.mu.RLock()
	defer g.mu.RUnlock()

	stats := g.stats
	stats.Monsters = len(g.monsters)
	stats.DeadUnits = len(g.dead)
	return stats
}

// Snapshot serialises the threat tables, the taunts and the dead unit set.
func (g *ThreatTableGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	data := snapshotData{
		Version:   snapshotVersion,
		Monsters:  make(map[string]snapshotMonster, len(g.monsters)),
		DeadUnits: make([]string, 0, len(g.dead)),
		Seq:       g.seq,
		NextOrder: g.nextOrder,
		Audit:     append([]AuditEntry(nil), g.audit...),
		Stats:     g.stats,
	}
	data.Stats.Monsters = len(g.monsters)
	data.Stats.DeadUnits = len(g.dead)

	for unitID := range g.dead {
		data.DeadUnits = append(data.DeadUnits, unitID)
	}
	sort.Strings(data.DeadUnits)

	for monsterID, table := range g.monsters {
		state := snapshotMonster{
			Entries: make([]snapshotEntry, 0, len(table.rows)),
		}
		for _, row := range table.rows {
			_, dead := g.dead[row.unitID]
			state.Entries = append(state.Entries, snapshotEntry{
				UnitID:   row.unitID,
				Threat:   row.threat,
				LastTick: row.lastTick,
				Order:    row.order,
				Dead:     dead,
			})
		}
		sort.Slice(state.Entries, func(i, j int) bool {
			return snapshotEntryBefore(state.Entries[i], state.Entries[j])
		})
		if table.taunt != nil {
			state.Taunt = &snapshotTaunt{
				UnitID:    table.taunt.unitID,
				StartTick: table.taunt.startTick,
				UntilTick: table.taunt.untilTick,
			}
		}
		data.Monsters[monsterID] = state
	}

	return json.Marshal(data)
}

// Restore replaces the guard state with a snapshot, all or nothing.
func (g *ThreatTableGuard) Restore(data []byte) error {
	restored, err := g.decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.monsters = restored.monsters
	g.dead = restored.dead
	g.seq = restored.seq
	g.nextOrder = restored.nextOrder
	g.audit = restored.audit
	g.stats = restored.stats
	return nil
}

func (g *ThreatTableGuard) recordAudit(action, monsterID, unitID string) {
	g.seq++
	g.audit = append(g.audit, AuditEntry{
		Seq:       g.seq,
		Action:    action,
		MonsterID: monsterID,
		UnitID:    unitID,
	})
	if len(g.audit) > g.cfg.MaxAudit {
		trimmed := make([]AuditEntry, g.cfg.MaxAudit)
		copy(trimmed, g.audit[len(g.audit)-g.cfg.MaxAudit:])
		g.audit = trimmed
	}
}

type decodedSnapshot struct {
	monsters  map[string]*monsterTable
	dead      map[string]struct{}
	seq       int64
	nextOrder int64
	audit     []AuditEntry
	stats     GuardStats
}

func (g *ThreatTableGuard) decodeSnapshot(data []byte) (decodedSnapshot, error) {
	if len(data) == 0 {
		return decodedSnapshot{}, ErrBadSnapshot
	}

	var snap snapshotData
	if err := json.Unmarshal(data, &snap); err != nil {
		return decodedSnapshot{}, ErrBadSnapshot
	}
	if snap.Version != snapshotVersion || snap.Seq < 0 || snap.NextOrder < 0 || len(snap.Monsters) > g.cfg.MaxMonsters {
		return decodedSnapshot{}, ErrBadSnapshot
	}
	if snap.Stats.Monsters < 0 || snap.Stats.Adds < 0 || snap.Stats.Taunts < 0 ||
		snap.Stats.Transfers < 0 || snap.Stats.DeadUnits < 0 || snap.Stats.Resets < 0 {
		return decodedSnapshot{}, ErrBadSnapshot
	}

	decoded := decodedSnapshot{
		monsters:  make(map[string]*monsterTable, len(snap.Monsters)),
		dead:      make(map[string]struct{}, len(snap.DeadUnits)),
		seq:       snap.Seq,
		nextOrder: snap.NextOrder,
		audit:     append([]AuditEntry(nil), snap.Audit...),
		stats:     snap.Stats,
	}
	decoded.stats.Monsters = 0
	decoded.stats.DeadUnits = 0

	for _, unitID := range snap.DeadUnits {
		if strings.TrimSpace(unitID) == "" {
			return decodedSnapshot{}, ErrBadSnapshot
		}
		decoded.dead[unitID] = struct{}{}
	}

	var maxOrder int64
	for monsterID, monster := range snap.Monsters {
		if strings.TrimSpace(monsterID) == "" || len(monster.Entries) > g.cfg.MaxUnitsPerTable {
			return decodedSnapshot{}, ErrBadSnapshot
		}
		table := &monsterTable{rows: make(map[string]*threatRow, len(monster.Entries))}
		for _, entry := range monster.Entries {
			if strings.TrimSpace(entry.UnitID) == "" || entry.Threat < 0 || entry.LastTick < 0 || entry.Order < 0 {
				return decodedSnapshot{}, ErrBadSnapshot
			}
			if _, duplicate := table.rows[entry.UnitID]; duplicate {
				return decodedSnapshot{}, ErrBadSnapshot
			}
			table.rows[entry.UnitID] = &threatRow{
				unitID:   entry.UnitID,
				threat:   entry.Threat,
				lastTick: entry.LastTick,
				order:    entry.Order,
			}
			if entry.Order > maxOrder {
				maxOrder = entry.Order
			}
			if entry.Dead {
				decoded.dead[entry.UnitID] = struct{}{}
			}
		}
		if monster.Taunt != nil {
			taunt := monster.Taunt
			if strings.TrimSpace(taunt.UnitID) == "" || taunt.UntilTick <= taunt.StartTick {
				return decodedSnapshot{}, ErrBadSnapshot
			}
			if table.rows[taunt.UnitID] == nil {
				return decodedSnapshot{}, ErrBadSnapshot
			}
			table.taunt = &tauntWindow{
				unitID:    taunt.UnitID,
				startTick: taunt.StartTick,
				untilTick: taunt.UntilTick,
			}
		}
		decoded.monsters[monsterID] = table
	}

	if maxOrder > decoded.nextOrder {
		return decodedSnapshot{}, ErrBadSnapshot
	}
	if len(decoded.audit) > g.cfg.MaxAudit {
		return decodedSnapshot{}, ErrBadSnapshot
	}
	var lastSeq int64
	for _, entry := range decoded.audit {
		if entry.Seq <= lastSeq || entry.Seq > decoded.seq || strings.TrimSpace(entry.Action) == "" {
			return decodedSnapshot{}, ErrBadSnapshot
		}
		lastSeq = entry.Seq
	}
	return decoded, nil
}

func (g *ThreatTableGuard) highestLivingRowLocked(table *monsterTable) *threatRow {
	var best *threatRow
	for _, row := range table.rows {
		if _, dead := g.dead[row.unitID]; dead {
			continue
		}
		if best == nil || rowBefore(row, best) {
			best = row
		}
	}
	return best
}

func rowBefore(left, right *threatRow) bool {
	if left.threat != right.threat {
		return left.threat > right.threat
	}
	if left.lastTick != right.lastTick {
		return left.lastTick > right.lastTick
	}
	return left.order > right.order
}

func snapshotEntryBefore(left, right snapshotEntry) bool {
	if left.Threat != right.Threat {
		return left.Threat > right.Threat
	}
	if left.LastTick != right.LastTick {
		return left.LastTick > right.LastTick
	}
	return left.Order > right.Order
}

func addInt64Saturating(left, right int64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if right > maxInt64-left {
		return maxInt64
	}
	return left + right
}

func scaleInt64(value int64, permille int) int64 {
	if value <= 0 || permille <= 0 {
		return 0
	}
	if permille > 1000 {
		permille = 1000
	}
	whole := value / 1000 * int64(permille)
	remainder := value % 1000 * int64(permille) / 1000
	return whole + remainder
}
