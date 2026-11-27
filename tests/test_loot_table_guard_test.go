package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-r-06"
)

func newLootGuard(t *testing.T, threshold int) *impl.LootTableGuard {
	t.Helper()
	g, err := impl.NewLootTableGuard(impl.Limits{
		MaxRollsPerBatch:   10,
		MaxEntriesPerTable: 8,
		MaxAudit:           16,
		PityQuality:        impl.QualityEpic,
		PityThreshold:      threshold,
	})
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	return g
}

func forestTable() impl.LootTable {
	return impl.LootTable{
		TableID:    "forest-loot",
		SceneGroup: "forest",
		Entries: []impl.LootEntry{
			{ItemID: "oak-log", Quality: impl.QualityCommon, Weight: 60},
			{ItemID: "moon-shard", Quality: impl.QualityRare, Weight: 40},
			{ItemID: "world-seed", Quality: impl.QualityEpic, Weight: 5},
		},
	}
}

func setupForest(t *testing.T, g *impl.LootTableGuard) {
	t.Helper()
	if err := g.RegisterScene("forest-1", "forest"); err != nil {
		t.Fatalf("register forest scene: %v", err)
	}
	if err := g.RegisterTable(forestTable()); err != nil {
		t.Fatalf("register forest table: %v", err)
	}
}

type fixedRand struct {
	values []int
	next   int
}

func (f *fixedRand) Intn(n int) int {
	v := f.values[f.next%len(f.values)]
	f.next++
	if v < 0 {
		v = 0
	}
	return v % n
}

func TestNewGuardValidatesLimits(t *testing.T) {
	base := impl.Limits{MaxRollsPerBatch: 10, MaxEntriesPerTable: 8, MaxAudit: 16, PityQuality: impl.QualityEpic, PityThreshold: 3}
	cases := []impl.Limits{base, base, base, base, base}
	cases[0].MaxRollsPerBatch = 0
	cases[1].MaxEntriesPerTable = 0
	cases[2].MaxAudit = 0
	cases[3].PityQuality = "legendary"
	cases[4].PityThreshold = 0
	for i, limits := range cases {
		if _, err := impl.NewLootTableGuard(limits); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("invalid limits %d must fail with ErrBadConfig, got %v", i, err)
		}
	}
	g := newLootGuard(t, 3)
	if st := g.Stats(); st.Tables != 0 || st.Scenes != 0 || st.Rolls != 0 || st.Drops != 0 || st.Forced != 0 || st.Switches != 0 {
		t.Fatalf("fresh stats = %+v, want all counters zero", st)
	}
}

func TestRegisterSceneValidatesAndIsIdempotent(t *testing.T) {
	g := newLootGuard(t, 3)
	if err := g.RegisterScene("   ", "forest"); !errors.Is(err, impl.ErrBadScene) {
		t.Fatalf("a blank scene id must fail with ErrBadScene, got %v", err)
	}
	if err := g.RegisterScene("forest-1", "  "); !errors.Is(err, impl.ErrBadScene) {
		t.Fatalf("a blank scene group must fail with ErrBadScene, got %v", err)
	}
	if err := g.RegisterScene("forest-1", "forest"); err != nil {
		t.Fatalf("register scene: %v", err)
	}
	if err := g.RegisterScene("forest-1", "forest"); err != nil {
		t.Fatalf("re-registering the same mapping must be idempotent, got %v", err)
	}
	if err := g.RegisterScene("forest-1", "cave"); !errors.Is(err, impl.ErrSceneExists) {
		t.Fatalf("moving a scene to another group must fail with ErrSceneExists, got %v", err)
	}
	group, ok := g.SceneGroupOf("forest-1")
	if !ok || group != "forest" {
		t.Fatalf("scene group = %q ok=%v, want forest", group, ok)
	}
	if _, ok := g.SceneGroupOf("forest-404"); ok {
		t.Fatalf("an unknown scene must not report a group")
	}
}

func TestRegisterTableValidatesEntries(t *testing.T) {
	g := newLootGuard(t, 3)
	bad := []impl.LootTable{
		{TableID: "  ", SceneGroup: "forest", Entries: forestTable().Entries},
		{TableID: "forest-loot", SceneGroup: "  ", Entries: forestTable().Entries},
		{TableID: "forest-loot", SceneGroup: "forest"},
		{TableID: "forest-loot", SceneGroup: "forest", Entries: []impl.LootEntry{{ItemID: "oak-log", Quality: "legendary", Weight: 10}}},
		{TableID: "forest-loot", SceneGroup: "forest", Entries: []impl.LootEntry{{ItemID: "oak-log", Quality: impl.QualityCommon, Weight: -1}}},
		{TableID: "forest-loot", SceneGroup: "forest", Entries: []impl.LootEntry{
			{ItemID: "oak-log", Quality: impl.QualityCommon, Weight: 10},
			{ItemID: "oak-log", Quality: impl.QualityRare, Weight: 10},
		}},
	}
	for i, table := range bad {
		if err := g.RegisterTable(table); !errors.Is(err, impl.ErrBadTable) {
			t.Fatalf("invalid table %d must fail with ErrBadTable, got %v", i, err)
		}
	}
	if err := g.RegisterTable(forestTable()); err != nil {
		t.Fatalf("register table: %v", err)
	}
	if err := g.RegisterTable(forestTable()); !errors.Is(err, impl.ErrTableExists) {
		t.Fatalf("a duplicate table id must fail with ErrTableExists, got %v", err)
	}
	table, ok := g.TableOf("forest-loot")
	if !ok {
		t.Fatalf("a registered table must be readable")
	}
	if len(table.Entries) != 3 || table.SceneGroup != "forest" {
		t.Fatalf("stored table = %+v, want three entries in the forest group", table)
	}
}

func TestRegisterTableRequiresPityQuality(t *testing.T) {
	g := newLootGuard(t, 3)
	withoutPity := impl.LootTable{
		TableID:    "forest-loot",
		SceneGroup: "forest",
		Entries: []impl.LootEntry{
			{ItemID: "oak-log", Quality: impl.QualityCommon, Weight: 60},
			{ItemID: "moon-shard", Quality: impl.QualityRare, Weight: 40},
		},
	}
	if err := g.RegisterTable(withoutPity); !errors.Is(err, impl.ErrBadTable) {
		t.Fatalf("a table without the pity quality must fail with ErrBadTable, got %v", err)
	}
	zeroPity := forestTable()
	zeroPity.Entries[2].Weight = 0
	if err := g.RegisterTable(zeroPity); !errors.Is(err, impl.ErrBadTable) {
		t.Fatalf("a pity quality with no weight must fail with ErrBadTable, got %v", err)
	}
}

func TestRollValidatesRequest(t *testing.T) {
	g := newLootGuard(t, 3)
	setupForest(t, g)
	if err := g.RegisterScene("cave-1", "cave"); err != nil {
		t.Fatalf("register cave scene: %v", err)
	}
	if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "loot-404", Count: 1}); !errors.Is(err, impl.ErrUnknownTable) {
		t.Fatalf("an unknown table must fail with ErrUnknownTable, got %v", err)
	}
	if _, err := g.Roll(impl.RollRequest{PlayerID: "  ", SceneID: "forest-1", TableID: "forest-loot", Count: 1}); !errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("a blank player must fail with ErrBadRequest, got %v", err)
	}
	if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 0}); !errors.Is(err, impl.ErrBadCount) {
		t.Fatalf("a zero roll count must fail with ErrBadCount, got %v", err)
	}
	if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 11}); !errors.Is(err, impl.ErrBadCount) {
		t.Fatalf("a count over the batch budget must fail with ErrBadCount, got %v", err)
	}
	if _, err := g.Roll(impl.RollRequest{PlayerID: "ghost", SceneID: "forest-1", TableID: "forest-loot", Count: 1}); !errors.Is(err, impl.ErrBadScene) {
		t.Fatalf("a player without a scene must fail with ErrBadScene, got %v", err)
	}
	if err := g.SwitchScene("p1", "cave-1"); err != nil {
		t.Fatalf("switch to the cave: %v", err)
	}
	if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "cave-1", TableID: "forest-loot", Count: 1}); !errors.Is(err, impl.ErrSceneMismatch) {
		t.Fatalf("a table from another group must fail with ErrSceneMismatch, got %v", err)
	}
}

func TestRollReturnsRequestedCount(t *testing.T) {
	g := newLootGuard(t, 50)
	setupForest(t, g)
	if err := g.SwitchScene("p1", "forest-1"); err != nil {
		t.Fatalf("switch scene: %v", err)
	}
	result, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 5, Source: &fixedRand{values: []int{0}}})
	if err != nil {
		t.Fatalf("roll: %v", err)
	}
	if len(result.Drops) != 5 {
		t.Fatalf("drops = %d, want the requested 5", len(result.Drops))
	}
	for i, drop := range result.Drops {
		if drop.ItemID != "oak-log" || drop.Quality != impl.QualityCommon || drop.Pity {
			t.Fatalf("drop %d = %+v, want a plain oak-log", i, drop)
		}
	}
	if result.Seq <= 0 {
		t.Fatalf("roll sequence = %d, want a positive number", result.Seq)
	}
	if st := g.Stats(); st.Rolls != 1 || st.Drops != 5 {
		t.Fatalf("stats = %+v, want one roll and five drops", st)
	}
}

func TestRollUsesInjectedSource(t *testing.T) {
	g := newLootGuard(t, 5)
	setupForest(t, g)
	if err := g.SwitchScene("p1", "forest-1"); err != nil {
		t.Fatalf("switch scene: %v", err)
	}
	source := &fixedRand{values: []int{0, 60, 104}}
	want := []struct {
		item    string
		quality string
	}{
		{"oak-log", impl.QualityCommon},
		{"moon-shard", impl.QualityRare},
		{"world-seed", impl.QualityEpic},
	}
	for i, w := range want {
		result, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: source})
		if err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
		if len(result.Drops) != 1 || result.Drops[0].ItemID != w.item || result.Drops[0].Quality != w.quality {
			t.Fatalf("roll %d drops = %+v, want %s/%s", i, result.Drops, w.item, w.quality)
		}
	}
}

func TestRollsAreIndependent(t *testing.T) {
	g := newLootGuard(t, 50)
	setupForest(t, g)
	for _, player := range []string{"p1", "p2"} {
		if err := g.SwitchScene(player, "forest-1"); err != nil {
			t.Fatalf("switch %s: %v", player, err)
		}
	}
	first, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 2, Source: &fixedRand{values: []int{0}}})
	if err != nil {
		t.Fatalf("roll p1: %v", err)
	}
	second, err := g.Roll(impl.RollRequest{PlayerID: "p2", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}})
	if err != nil {
		t.Fatalf("roll p2: %v", err)
	}
	for _, drop := range append(first.Drops, second.Drops...) {
		if drop.ItemID != "oak-log" {
			t.Fatalf("independent rolls produced %s, want the same oak-log for both players", drop.ItemID)
		}
	}
	table, _ := g.TableOf("forest-loot")
	if len(table.Entries) != 3 {
		t.Fatalf("a roll changed the table, entries = %d", len(table.Entries))
	}
	if st := g.Stats(); st.Drops != 3 {
		t.Fatalf("drop counter = %d, want 3", st.Drops)
	}
}

func TestPityAccumulatesAndForces(t *testing.T) {
	g := newLootGuard(t, 3)
	setupForest(t, g)
	if err := g.SwitchScene("p1", "forest-1"); err != nil {
		t.Fatalf("switch scene: %v", err)
	}
	for i := 1; i <= 3; i++ {
		result, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}})
		if err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
		if result.Drops[0].Pity || result.Forced != 0 {
			t.Fatalf("roll %d without a threshold must not be forced, got %+v", i, result)
		}
		state, ok := g.PityOf("p1")
		if !ok || state.Counter != i || state.Threshold != 3 || state.SceneGroup != "forest" {
			t.Fatalf("pity after roll %d = %+v ok=%v, want counter %d in forest", i, state, ok, i)
		}
	}
	forced, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}})
	if err != nil {
		t.Fatalf("forced roll: %v", err)
	}
	if forced.PityBefore != 3 || forced.PityAfter != 0 || forced.Forced != 1 {
		t.Fatalf("forced roll counters = %+v, want before 3 after 0 forced 1", forced)
	}
	if len(forced.Drops) != 1 || forced.Drops[0].ItemID != "world-seed" || !forced.Drops[0].Pity {
		t.Fatalf("forced drops = %+v, want one world-seed marked as pity", forced.Drops)
	}
	after, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}})
	if err != nil {
		t.Fatalf("roll after the guarantee: %v", err)
	}
	if after.Drops[0].Pity || after.PityAfter != 1 {
		t.Fatalf("roll after the guarantee = %+v, want a normal drop and counter 1", after)
	}
	if st := g.Stats(); st.Forced != 1 {
		t.Fatalf("forced counter = %d, want 1", st.Forced)
	}
}

func TestSwitchSceneInheritsPityWithinGroup(t *testing.T) {
	g := newLootGuard(t, 50)
	setupForest(t, g)
	if err := g.RegisterScene("forest-2", "forest"); err != nil {
		t.Fatalf("register second scene: %v", err)
	}
	if err := g.SwitchScene("p1", "forest-1"); err != nil {
		t.Fatalf("switch to forest-1: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}}); err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
	}
	if err := g.SwitchScene("p1", "forest-2"); err != nil {
		t.Fatalf("switch to forest-2: %v", err)
	}
	state, ok := g.PityOf("p1")
	if !ok || state.Counter != 2 || state.SceneGroup != "forest" {
		t.Fatalf("pity after the scene change = %+v ok=%v, want counter 2 in forest", state, ok)
	}
	if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-2", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}}); err != nil {
		t.Fatalf("roll in the second scene: %v", err)
	}
	if state, _ := g.PityOf("p1"); state.Counter != 3 {
		t.Fatalf("pity after the roll in the second scene = %d, want it to continue at 3", state.Counter)
	}
	scene, ok := g.CurrentScene("p1")
	if !ok || scene != "forest-2" {
		t.Fatalf("current scene = %q ok=%v, want forest-2", scene, ok)
	}
	if err := g.SwitchScene("p1", "scene-404"); !errors.Is(err, impl.ErrUnknownScene) {
		t.Fatalf("switching to an unknown scene must fail with ErrUnknownScene, got %v", err)
	}
	if err := g.SwitchScene("   ", "forest-2"); !errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("switching a blank player must fail with ErrBadRequest, got %v", err)
	}
}

func TestResetPityClearsCounter(t *testing.T) {
	g := newLootGuard(t, 50)
	setupForest(t, g)
	if err := g.SwitchScene("p1", "forest-1"); err != nil {
		t.Fatalf("switch scene: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}}); err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
	}
	if err := g.ResetPity("p1"); err != nil {
		t.Fatalf("reset pity: %v", err)
	}
	if state, _ := g.PityOf("p1"); state.Counter != 0 {
		t.Fatalf("pity after reset = %d, want 0", state.Counter)
	}
	if err := g.ResetPity("ghost"); !errors.Is(err, impl.ErrUnknownPlayer) {
		t.Fatalf("resetting an unknown player must fail with ErrUnknownPlayer, got %v", err)
	}
}

func TestAuditLogIsCappedAndOrdered(t *testing.T) {
	g, err := impl.NewLootTableGuard(impl.Limits{
		MaxRollsPerBatch:   10,
		MaxEntriesPerTable: 8,
		MaxAudit:           4,
		PityQuality:        impl.QualityEpic,
		PityThreshold:      50,
	})
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	if err := g.RegisterScene("forest-1", "forest"); err != nil {
		t.Fatalf("register scene: %v", err)
	}
	if err := g.RegisterTable(forestTable()); err != nil {
		t.Fatalf("register table: %v", err)
	}
	if err := g.SwitchScene("p1", "forest-1"); err != nil {
		t.Fatalf("switch scene: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}}); err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
	}
	audit := g.Audit()
	if len(audit) != 4 {
		t.Fatalf("audit len = %d, want the configured cap 4", len(audit))
	}
	for i := 1; i < len(audit); i++ {
		if audit[i].Seq <= audit[i-1].Seq {
			t.Fatalf("audit sequence must strictly increase, got %+v", audit)
		}
	}
	last := audit[len(audit)-1]
	if last.Action != impl.AuditRoll || last.PlayerID != "p1" {
		t.Fatalf("newest audit entry = %+v, want the last roll of p1", last)
	}
}

func TestSnapshotRestoreKeepsPity(t *testing.T) {
	g := newLootGuard(t, 3)
	setupForest(t, g)
	if err := g.SwitchScene("p1", "forest-1"); err != nil {
		t.Fatalf("switch scene: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}}); err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newLootGuard(t, 3)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	state, ok := restored.PityOf("p1")
	if !ok || state.Counter != 2 || state.SceneGroup != "forest" {
		t.Fatalf("restored pity = %+v ok=%v, want counter 2 in forest", state, ok)
	}
	if scene, ok := restored.CurrentScene("p1"); !ok || scene != "forest-1" {
		t.Fatalf("restored scene = %q ok=%v, want forest-1", scene, ok)
	}
	if table, ok := restored.TableOf("forest-loot"); !ok || len(table.Entries) != 3 {
		t.Fatalf("restored table = %+v ok=%v, want the three entries", table, ok)
	}
	third, err := restored.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}})
	if err != nil {
		t.Fatalf("roll after restore: %v", err)
	}
	if third.PityBefore != 2 || third.PityAfter != 3 || third.Forced != 0 {
		t.Fatalf("roll after restore = %+v, want the counter to continue from 2 to 3", third)
	}
	forced, err := restored.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}})
	if err != nil {
		t.Fatalf("forced roll after restore: %v", err)
	}
	if forced.Forced != 1 || !forced.Drops[0].Pity || forced.PityAfter != 0 {
		t.Fatalf("forced roll after restore = %+v, want the guarantee to fire", forced)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newLootGuard(t, 50)
	setupForest(t, g)
	if err := g.SwitchScene("p1", "forest-1"); err != nil {
		t.Fatalf("switch scene: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.Roll(impl.RollRequest{PlayerID: "p1", SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: &fixedRand{values: []int{0}}}); err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
	}
	auditBefore := len(g.Audit())
	if err := g.Restore([]byte("{corrupt-state")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if state, ok := g.PityOf("p1"); !ok || state.Counter != 2 {
		t.Fatalf("pity after a rejected restore = %+v ok=%v, want counter 2", state, ok)
	}
	if _, ok := g.TableOf("forest-loot"); !ok {
		t.Fatalf("a rejected restore must leave the registered table in place")
	}
	if got := len(g.Audit()); got != auditBefore {
		t.Fatalf("audit len after a rejected restore = %d, want %d", got, auditBefore)
	}
}

func TestConcurrentRollsKeepCounters(t *testing.T) {
	g := newLootGuard(t, 50)
	setupForest(t, g)
	const players = 8
	const perPlayer = 3
	for i := 0; i < players; i++ {
		if err := g.SwitchScene(fmt.Sprintf("p%d", i), "forest-1"); err != nil {
			t.Fatalf("switch player %d: %v", i, err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, players)
	for i := 0; i < players; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			player := fmt.Sprintf("p%d", i)
			source := &fixedRand{values: []int{0}}
			for n := 0; n < perPlayer; n++ {
				if _, err := g.Roll(impl.RollRequest{PlayerID: player, SceneID: "forest-1", TableID: "forest-loot", Count: 1, Source: source}); err != nil {
					errs[i] = err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("player %d failed: %v", i, err)
		}
	}
	for i := 0; i < players; i++ {
		state, ok := g.PityOf(fmt.Sprintf("p%d", i))
		if !ok || state.Counter != perPlayer {
			t.Fatalf("player %d pity = %+v ok=%v, want counter %d", i, state, ok, perPlayer)
		}
	}
	if st := g.Stats(); st.Rolls != players*perPlayer || st.Drops != players*perPlayer {
		t.Fatalf("stats after the race = %+v, want %d rolls and drops", st, players*perPlayer)
	}
}
