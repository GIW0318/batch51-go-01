package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-r-10"
)

func baseThreatConfig() impl.ThreatConfig {
	return impl.ThreatConfig{MaxMonsters: 8, MaxUnitsPerTable: 16, MaxAudit: 16}
}

func newThreatGuard(t *testing.T, cfg impl.ThreatConfig) *impl.ThreatTableGuard {
	t.Helper()
	g, err := impl.NewThreatTableGuard(cfg)
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	return g
}

func addRows(t *testing.T, g *impl.ThreatTableGuard, monsterID string, rows [][3]interface{}) {
	t.Helper()
	for _, row := range rows {
		unit := row[0].(string)
		amount := row[1].(int64)
		tick := row[2].(int64)
		if err := g.AddThreat(monsterID, unit, amount, tick); err != nil {
			t.Fatalf("add threat for %s: %v", unit, err)
		}
	}
}

func TestNewGuardValidatesConfig(t *testing.T) {
	bad := []impl.ThreatConfig{
		{MaxMonsters: 0, MaxUnitsPerTable: 8, MaxAudit: 8},
		{MaxMonsters: 4, MaxUnitsPerTable: 0, MaxAudit: 8},
		{MaxMonsters: 4, MaxUnitsPerTable: 8, MaxAudit: 0},
	}
	for i, cfg := range bad {
		if _, err := impl.NewThreatTableGuard(cfg); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("config %d must fail with ErrBadConfig, got %v", i, err)
		}
	}
	g := newThreatGuard(t, baseThreatConfig())
	if st := g.Stats(); st != (impl.GuardStats{}) {
		t.Fatalf("fresh stats = %+v, want all counters zero", st)
	}
	if got := len(g.Table("boss")); got != 0 {
		t.Fatalf("fresh table rows = %d, want 0", got)
	}
	if _, ok := g.CurrentTarget("boss", 0); ok {
		t.Fatalf("an empty threat table must not report a target")
	}
}

func TestAddThreatValidation(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	cases := []struct {
		name string
		mon  string
		unit string
		amt  int64
		tick int64
		want error
	}{
		{"blank monster", "  ", "unit-a", 10, 1, impl.ErrBadThreat},
		{"blank unit", "boss", "  ", 10, 1, impl.ErrBadThreat},
		{"zero amount", "boss", "unit-a", 0, 1, impl.ErrBadThreat},
		{"negative amount", "boss", "unit-a", -5, 1, impl.ErrBadThreat},
		{"negative tick", "boss", "unit-a", 10, -1, impl.ErrBadThreat},
	}
	for _, tc := range cases {
		if err := g.AddThreat(tc.mon, tc.unit, tc.amt, tc.tick); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := g.MarkDead("unit-x"); err != nil {
		t.Fatalf("mark unit-x dead: %v", err)
	}
	if err := g.AddThreat("boss", "unit-x", 10, 1); !errors.Is(err, impl.ErrUnitDead) {
		t.Fatalf("threat for a dead unit must fail with ErrUnitDead, got %v", err)
	}
	if got := len(g.Table("boss")); got != 0 {
		t.Fatalf("rejected adds left %d rows, want 0", got)
	}
}

func TestThreatAccumulatesPerUnit(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-a", int64(50), int64(20)},
	})
	table := g.Table("boss")
	if len(table) != 1 {
		t.Fatalf("table rows = %d, want 1", len(table))
	}
	if table[0].Threat != 150 {
		t.Fatalf("accumulated threat = %d, want 150", table[0].Threat)
	}
	if table[0].LastTick != 20 {
		t.Fatalf("last tick = %d, want 20", table[0].LastTick)
	}
	top, ok := g.HighestThreat("boss")
	if !ok || top.UnitID != "unit-a" || top.Threat != 150 {
		t.Fatalf("highest threat = %+v/%v, want unit-a with 150", top, ok)
	}
	if st := g.Stats(); st.Adds != 2 || st.Monsters != 1 {
		t.Fatalf("stats = %+v, want 2 adds on 1 monster", st)
	}
}

func TestTableOrderedByThreat(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-b", int64(300), int64(20)},
		{"unit-c", int64(200), int64(30)},
	})
	table := g.Table("boss")
	if len(table) != 3 {
		t.Fatalf("table rows = %d, want 3", len(table))
	}
	want := []string{"unit-b", "unit-c", "unit-a"}
	for i, unit := range want {
		if table[i].UnitID != unit {
			t.Fatalf("row %d = %s, want %s", i, table[i].UnitID, unit)
		}
	}
	if table[0].Threat != 300 || table[2].Threat != 100 {
		t.Fatalf("table threats = %d..%d, want 300..100", table[0].Threat, table[2].Threat)
	}
}

func TestCurrentTargetPicksHighestThreat(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-b", int64(300), int64(20)},
	})
	target, ok := g.CurrentTarget("boss", 25)
	if !ok || target != "unit-b" {
		t.Fatalf("current target = %q/%v, want unit-b/true", target, ok)
	}
	if _, ok := g.CurrentTarget("boss-404", 25); ok {
		t.Fatalf("an unknown monster must not report a target")
	}
}

func TestTauntForcesTarget(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-b", int64(300), int64(20)},
	})
	if err := g.Taunt("boss", "unit-a", 40, 100); err != nil {
		t.Fatalf("taunt: %v", err)
	}
	if got := g.TauntRemaining("boss", 50); got != 50 {
		t.Fatalf("taunt remaining at tick 50 = %d, want 50", got)
	}
	target, ok := g.CurrentTarget("boss", 50)
	if !ok || target != "unit-a" {
		t.Fatalf("target during the taunt = %q/%v, want unit-a/true", target, ok)
	}
	if got := g.TauntRemaining("boss", 100); got != 0 {
		t.Fatalf("taunt remaining at the expiry tick = %d, want 0", got)
	}
	target, ok = g.CurrentTarget("boss", 100)
	if !ok || target != "unit-b" {
		t.Fatalf("target after the taunt = %q/%v, want unit-b/true", target, ok)
	}
}

func TestTauntValidation(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{{"unit-a", int64(100), int64(10)}})
	if err := g.Taunt("boss", "ghost", 0, 20); !errors.Is(err, impl.ErrUnitNotOnTable) {
		t.Fatalf("taunting an unknown unit must fail with ErrUnitNotOnTable, got %v", err)
	}
	if err := g.Taunt("boss", "unit-a", 10, 10); !errors.Is(err, impl.ErrBadTaunt) {
		t.Fatalf("an empty taunt window must fail with ErrBadTaunt, got %v", err)
	}
}

func TestClearTaunt(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-b", int64(300), int64(20)},
	})
	if err := g.Taunt("boss", "unit-a", 0, 100); err != nil {
		t.Fatalf("taunt: %v", err)
	}
	if err := g.ClearTaunt("boss"); err != nil {
		t.Fatalf("clear taunt: %v", err)
	}
	target, ok := g.CurrentTarget("boss", 10)
	if !ok || target != "unit-b" {
		t.Fatalf("target after clearing the taunt = %q/%v, want unit-b/true", target, ok)
	}
	if err := g.ClearTaunt("boss"); err != nil {
		t.Fatalf("clearing an empty taunt must be idempotent, got %v", err)
	}
	if err := g.ClearTaunt("boss-404"); !errors.Is(err, impl.ErrUnknownMonster) {
		t.Fatalf("clearing the taunt of an unknown monster must fail with ErrUnknownMonster, got %v", err)
	}
}

func TestMarkDeadExcludesFromTarget(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-b", int64(300), int64(20)},
		{"unit-c", int64(200), int64(30)},
	})
	if err := g.MarkDead("unit-b"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	target, ok := g.CurrentTarget("boss", 40)
	if !ok || target != "unit-c" {
		t.Fatalf("target after the top unit died = %q/%v, want unit-c/true", target, ok)
	}
	top, ok := g.HighestThreat("boss")
	if !ok || top.UnitID != "unit-c" {
		t.Fatalf("highest living threat = %+v/%v, want unit-c", top, ok)
	}
	table := g.Table("boss")
	if len(table) != 3 {
		t.Fatalf("table rows after the death = %d, want the record kept", len(table))
	}
	found := false
	for _, row := range table {
		if row.UnitID == "unit-b" {
			found = true
			if !row.Dead {
				t.Fatalf("unit-b row must be flagged dead, got %+v", row)
			}
		}
	}
	if !found {
		t.Fatalf("the dead unit must stay on the table")
	}
	if st := g.Stats(); st.DeadUnits != 1 {
		t.Fatalf("dead unit counter = %d, want 1", st.DeadUnits)
	}
}

func TestReviveRestoresPresence(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-b", int64(300), int64(20)},
	})
	if err := g.MarkDead("unit-b"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	if err := g.Revive("unit-b"); err != nil {
		t.Fatalf("revive: %v", err)
	}
	target, ok := g.CurrentTarget("boss", 40)
	if !ok || target != "unit-b" {
		t.Fatalf("target after the revive = %q/%v, want unit-b/true", target, ok)
	}
	if err := g.Revive("unit-b"); err != nil {
		t.Fatalf("reviving twice must be idempotent, got %v", err)
	}
}

func TestTransferThreatValidation(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-b", int64(300), int64(20)},
	})
	for _, pct := range []int{0, -10, 1001} {
		if err := g.TransferThreat("boss", "unit-b", "unit-a", pct); !errors.Is(err, impl.ErrBadTransfer) {
			t.Fatalf("permille %d must fail with ErrBadTransfer, got %v", pct, err)
		}
	}
	if err := g.TransferThreat("boss", "unit-a", "unit-a", 500); !errors.Is(err, impl.ErrBadTransfer) {
		t.Fatalf("transferring to the same unit must fail with ErrBadTransfer, got %v", err)
	}
	if err := g.TransferThreat("boss", "ghost-from", "unit-a", 500); !errors.Is(err, impl.ErrUnitNotOnTable) {
		t.Fatalf("an unknown source must fail with ErrUnitNotOnTable, got %v", err)
	}
	if err := g.TransferThreat("boss", "unit-b", "ghost-to", 500); !errors.Is(err, impl.ErrUnitNotOnTable) {
		t.Fatalf("an unknown target must fail with ErrUnitNotOnTable, got %v", err)
	}
}

func TestResetTableAndClearAll(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{{"unit-a", int64(100), int64(10)}})
	if err := g.ResetTable("boss-404"); !errors.Is(err, impl.ErrUnknownMonster) {
		t.Fatalf("resetting an unknown monster must fail with ErrUnknownMonster, got %v", err)
	}
	if err := g.ResetTable("boss"); err != nil {
		t.Fatalf("reset table: %v", err)
	}
	if got := len(g.Table("boss")); got != 0 {
		t.Fatalf("table rows after reset = %d, want 0", got)
	}
	if _, ok := g.CurrentTarget("boss", 10); ok {
		t.Fatalf("a reset table must not report a target")
	}
	addRows(t, g, "boss", [][3]interface{}{{"unit-a", int64(100), int64(10)}})
	addRows(t, g, "add", [][3]interface{}{{"unit-b", int64(50), int64(10)}})
	if got := g.ClearAll(); got != 2 {
		t.Fatalf("clear all dropped %d monsters, want 2", got)
	}
	if got := len(g.Table("boss")); got != 0 {
		t.Fatalf("boss table after clear all = %d rows, want 0", got)
	}
	if got := len(g.Table("add")); got != 0 {
		t.Fatalf("add table after clear all = %d rows, want 0", got)
	}
}

func TestAuditAndStats(t *testing.T) {
	cfg := baseThreatConfig()
	cfg.MaxAudit = 3
	g := newThreatGuard(t, cfg)
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-b", int64(300), int64(20)},
	})
	if err := g.Taunt("boss", "unit-a", 30, 90); err != nil {
		t.Fatalf("taunt: %v", err)
	}
	if err := g.TransferThreat("boss", "unit-b", "unit-a", 250); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	audit := g.Audit()
	if len(audit) != 3 {
		t.Fatalf("audit len = %d, want the configured cap 3", len(audit))
	}
	for i := 1; i < len(audit); i++ {
		if audit[i].Seq <= audit[i-1].Seq {
			t.Fatalf("audit sequence must strictly increase, got %+v", audit)
		}
	}
	if audit[len(audit)-1].Action != impl.AuditTransfer {
		t.Fatalf("newest audit action = %q, want transfer_threat", audit[len(audit)-1].Action)
	}
	st := g.Stats()
	if st.Monsters != 1 || st.Adds != 2 || st.Taunts != 1 || st.Transfers != 1 {
		t.Fatalf("stats = %+v, want 1 monster, 2 adds, 1 taunt, 1 transfer", st)
	}
}

func TestSnapshotRestoreKeepsThreatState(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	addRows(t, g, "boss", [][3]interface{}{
		{"unit-a", int64(100), int64(10)},
		{"unit-b", int64(300), int64(20)},
		{"unit-c", int64(200), int64(30)},
	})
	if err := g.MarkDead("unit-c"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	if err := g.Taunt("boss", "unit-a", 40, 100); err != nil {
		t.Fatalf("taunt the living unit: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newThreatGuard(t, baseThreatConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	table := restored.Table("boss")
	if len(table) != 3 {
		t.Fatalf("restored table rows = %d, want 3", len(table))
	}
	foundDead := false
	for _, row := range table {
		if row.UnitID == "unit-c" && row.Dead {
			foundDead = true
		}
	}
	if !foundDead {
		t.Fatalf("restored table lost the dead flag of unit-c: %+v", table)
	}
	if got := restored.TauntRemaining("boss", 50); got != 50 {
		t.Fatalf("restored taunt remaining = %d, want 50", got)
	}
	target, ok := restored.CurrentTarget("boss", 50)
	if !ok || target != "unit-a" {
		t.Fatalf("restored target during the taunt = %q/%v, want unit-a", target, ok)
	}
	if err := restored.Restore([]byte("{broken-threat-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(restored.Table("boss")); got != 3 {
		t.Fatalf("a rejected restore changed the table to %d rows, want 3", got)
	}
}

func TestConcurrentAddThreat(t *testing.T) {
	g := newThreatGuard(t, baseThreatConfig())
	const workers = 32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := g.AddThreat("boss", "tank-1", 10, int64(i+1)); err != nil {
				t.Errorf("concurrent add %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	table := g.Table("boss")
	if len(table) != 1 {
		t.Fatalf("table rows after the race = %d, want 1", len(table))
	}
	if table[0].Threat != 10*workers {
		t.Fatalf("threat after the race = %d, want %d", table[0].Threat, 10*workers)
	}
	if st := g.Stats(); st.Adds != workers {
		t.Fatalf("add counter after the race = %d, want %d", st.Adds, workers)
	}
}
