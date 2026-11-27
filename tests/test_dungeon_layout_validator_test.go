package task

import (
	"errors"
	"reflect"
	"testing"

	impl "gsb/batch51-go-game-a-04"
)

func dungeonLimits() impl.Limits {
	return impl.Limits{MaxRooms: 16, MaxEdges: 12, MaxVisit: 100}
}

func layoutConfig() impl.LayoutConfig {
	return impl.LayoutConfig{RoomCount: 9, DoorCount: 5, KeyCount: 2, Version: 1}
}

func linearLayout() impl.Layout {
	return impl.Layout{
		Rooms: []impl.Room{
			{ID: 0, Kind: impl.RoomEntry},
			{ID: 1, Kind: impl.RoomNormal},
			{ID: 2, Kind: impl.RoomBoss},
		},
		Keys: []impl.KeyToken{{ID: "k1", Room: 0}},
		Doors: []impl.Door{
			{ID: 0, From: 0, To: 1, Key: "k1"},
			{ID: 1, From: 1, To: 2},
		},
		Entry:   0,
		Boss:    2,
		Version: 1,
	}
}

func TestAnalyzeLinearLayoutIsComplete(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	report, err := v.AnalyzeLayout(linearLayout())
	if err != nil {
		t.Fatalf("linear layout must analyse, got %v", err)
	}
	if !reflect.DeepEqual(report.Reachable, []int{0, 1, 2}) {
		t.Fatalf("reachable = %v, want [0 1 2]", report.Reachable)
	}
	if len(report.Unreachable) != 0 {
		t.Fatalf("unreachable = %v, want none", report.Unreachable)
	}
	if len(report.Locked) != 0 || len(report.MissingKeys) != 0 {
		t.Fatalf("locked=%v missing=%v, want both empty", report.Locked, report.MissingKeys)
	}
	if len(report.Deadlocks) != 0 {
		t.Fatalf("deadlocks = %v, want none in an acyclic layout", report.Deadlocks)
	}
	if !report.Complete {
		t.Fatalf("report must be complete when every room is reachable")
	}
}

func TestAnalyzeReportsMissingKey(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	layout := linearLayout()
	layout.Keys = nil
	layout.Doors[0].Key = ""
	report, err := v.AnalyzeLayout(layout)
	if err != nil {
		t.Fatalf("analyse layout with a locked door: %v", err)
	}
	if !reflect.DeepEqual(report.Reachable, []int{0, 1}) {
		t.Fatalf("reachable = %v, want [0 1]", report.Reachable)
	}
	if !reflect.DeepEqual(report.Unreachable, []int{2}) {
		t.Fatalf("unreachable = %v, want [2]", report.Unreachable)
	}
	if !reflect.DeepEqual(report.Locked, []int{1}) {
		t.Fatalf("locked doors = %v, want [1]", report.Locked)
	}
	if !reflect.DeepEqual(report.MissingKeys, []string{"k2"}) {
		t.Fatalf("missing keys = %v, want [k2]", report.MissingKeys)
	}
	if report.Complete {
		t.Fatalf("a layout with an unreachable boss must not be complete")
	}
}

func TestAnalyzeHandlesReachableCycle(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	layout := impl.Layout{
		Rooms: []impl.Room{
			{ID: 0, Kind: impl.RoomEntry},
			{ID: 1, Kind: impl.RoomNormal},
			{ID: 2, Kind: impl.RoomBoss},
		},
		Doors: []impl.Door{
			{ID: 0, From: 0, To: 1, Bidirectional: true},
			{ID: 1, From: 1, To: 2, Bidirectional: true},
			{ID: 2, From: 2, To: 0, Bidirectional: true},
		},
		Entry:   0,
		Boss:    2,
		Version: 1,
	}
	report, err := v.AnalyzeLayout(layout)
	if err != nil {
		t.Fatalf("cyclic but reachable layout must analyse, got %v", err)
	}
	if !reflect.DeepEqual(report.Reachable, []int{0, 1, 2}) {
		t.Fatalf("reachable = %v, want all three rooms", report.Reachable)
	}
	if !report.Complete {
		t.Fatalf("a reachable cycle must not stop the analysis")
	}
	if len(report.Deadlocks) != 0 {
		t.Fatalf("deadlocks = %v, want none because every room is reachable", report.Deadlocks)
	}
}

func TestAnalyzeRejectsBadEntry(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	layout := linearLayout()
	layout.Entry = 1
	if _, err := v.AnalyzeLayout(layout); !errors.Is(err, impl.ErrBadLayout) {
		t.Fatalf("an entry that is not room 0 must fail with ErrBadLayout, got %v", err)
	}
}

func TestAnalyzeRejectsDoorToUnknownRoom(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	layout := linearLayout()
	layout.Doors[1].To = 99
	if _, err := v.AnalyzeLayout(layout); !errors.Is(err, impl.ErrBadLayout) {
		t.Fatalf("a door to an unknown room must fail with ErrBadLayout, got %v", err)
	}
}

func TestAnalyzeRejectsDuplicateDoorIDs(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	layout := linearLayout()
	layout.Doors[1].ID = 0
	if _, err := v.AnalyzeLayout(layout); !errors.Is(err, impl.ErrBadLayout) {
		t.Fatalf("duplicate door ids must fail with ErrBadLayout, got %v", err)
	}
}

func TestAnalyzeRejectsMissingBoss(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	layout := linearLayout()
	layout.Boss = 42
	if _, err := v.AnalyzeLayout(layout); !errors.Is(err, impl.ErrBadLayout) {
		t.Fatalf("a boss room outside the room list must fail with ErrBadLayout, got %v", err)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	first, err := v.Generate(42, layoutConfig())
	if err != nil {
		t.Fatalf("generate seed 42: %v", err)
	}
	if len(first.Rooms) != 9 {
		t.Fatalf("generated rooms = %d, want 9", len(first.Rooms))
	}
	for i, room := range first.Rooms {
		if room.ID != i {
			t.Fatalf("room %d id = %d, want %d", i, room.ID, i)
		}
	}
	if first.Rooms[0].Kind != impl.RoomEntry {
		t.Fatalf("room 0 kind = %q, want %q", first.Rooms[0].Kind, impl.RoomEntry)
	}
	if first.Boss < 1 || first.Boss >= len(first.Rooms) {
		t.Fatalf("boss room = %d, want an index inside the room list", first.Boss)
	}
	if first.Rooms[first.Boss].Kind != impl.RoomBoss {
		t.Fatalf("boss room kind = %q, want %q", first.Rooms[first.Boss].Kind, impl.RoomBoss)
	}
	if len(first.Doors) != 5 || len(first.Keys) != 2 {
		t.Fatalf("generated doors/keys = %d/%d, want 5 and 2", len(first.Doors), len(first.Keys))
	}
	second, err := v.Generate(42, layoutConfig())
	if err != nil {
		t.Fatalf("generate seed 42 again: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("the same seed generated two different layouts")
	}
}

func TestValidateClassifiesEveryRoom(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	report, err := v.Validate(7, layoutConfig())
	if err != nil {
		t.Fatalf("validate seed 7: %v", err)
	}
	if got := len(report.Reachable) + len(report.Unreachable); got != 9 {
		t.Fatalf("reachable plus unreachable = %d, want every room once", got)
	}
	if !v.IsReachable(report, 0) {
		t.Fatalf("the entry room must always be reachable")
	}
	for i := 1; i < len(report.Reachable); i++ {
		if report.Reachable[i] <= report.Reachable[i-1] {
			t.Fatalf("reachable set is not sorted ascending: %v", report.Reachable)
		}
	}
	for i := 1; i < len(report.Unreachable); i++ {
		if report.Unreachable[i] <= report.Unreachable[i-1] {
			t.Fatalf("unreachable set is not sorted ascending: %v", report.Unreachable)
		}
	}
	if _, ok := v.LayoutOf(7); !ok {
		t.Fatalf("validate must keep the generated layout for seed 7")
	}
}

func TestValidateIsDeterministic(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	first, err := v.Validate(11, layoutConfig())
	if err != nil {
		t.Fatalf("validate seed 11: %v", err)
	}
	second, err := v.Validate(11, layoutConfig())
	if err != nil {
		t.Fatalf("validate seed 11 again: %v", err)
	}
	if !reflect.DeepEqual(first.Reachable, second.Reachable) {
		t.Fatalf("reachable sets differ between runs: %v vs %v", first.Reachable, second.Reachable)
	}
	if !reflect.DeepEqual(first.Unreachable, second.Unreachable) {
		t.Fatalf("unreachable sets differ between runs: %v vs %v", first.Unreachable, second.Unreachable)
	}
	if !reflect.DeepEqual(first.Locked, second.Locked) {
		t.Fatalf("locked doors differ between runs: %v vs %v", first.Locked, second.Locked)
	}
	if !reflect.DeepEqual(first.MissingKeys, second.MissingKeys) {
		t.Fatalf("missing keys differ between runs: %v vs %v", first.MissingKeys, second.MissingKeys)
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	tooSmall := layoutConfig()
	tooSmall.RoomCount = 1
	if _, err := v.Validate(1, tooSmall); !errors.Is(err, impl.ErrInvalidConfig) {
		t.Fatalf("a single room layout must fail with ErrInvalidConfig, got %v", err)
	}
	badKeys := layoutConfig()
	badKeys.KeyCount = -1
	if _, err := v.Validate(1, badKeys); !errors.Is(err, impl.ErrInvalidConfig) {
		t.Fatalf("a negative key count must fail with ErrInvalidConfig, got %v", err)
	}
	badVersion := layoutConfig()
	badVersion.Version = 0
	if _, err := v.Validate(1, badVersion); !errors.Is(err, impl.ErrInvalidConfig) {
		t.Fatalf("a zero layout version must fail with ErrInvalidConfig, got %v", err)
	}
}

func TestValidateEnforcesRoomAndEdgeLimits(t *testing.T) {
	small := impl.NewDungeonLayoutValidator(impl.Limits{MaxRooms: 4, MaxEdges: 4, MaxVisit: 100})
	if _, err := small.Validate(3, layoutConfig()); !errors.Is(err, impl.ErrRoomLimit) {
		t.Fatalf("nine rooms against a four room limit must fail with ErrRoomLimit, got %v", err)
	}
	cfg := layoutConfig()
	cfg.RoomCount = 4
	cfg.DoorCount = 9
	if _, err := small.Validate(3, cfg); !errors.Is(err, impl.ErrEdgeLimit) {
		t.Fatalf("nine doors against a four edge limit must fail with ErrEdgeLimit, got %v", err)
	}
}

func TestValidateStopsAtVisitBudget(t *testing.T) {
	tight := impl.NewDungeonLayoutValidator(impl.Limits{MaxRooms: 16, MaxEdges: 12, MaxVisit: 2})
	report, err := tight.Validate(7, layoutConfig())
	if !errors.Is(err, impl.ErrBudgetExceeded) {
		t.Fatalf("a two step budget must stop the analysis, got %v", err)
	}
	if !report.Truncated {
		t.Fatalf("report must be marked truncated")
	}
	if report.Complete {
		t.Fatalf("a truncated report must not be complete")
	}
	if report.Visited > 2 {
		t.Fatalf("visited rooms = %d, want at most the budget 2", report.Visited)
	}
	if len(report.Token) == 0 {
		t.Fatalf("truncated analysis must hand out a resume token")
	}
}

func TestSuggestRepairOnlyAddsDoorsAndKeys(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	layout := linearLayout()
	layout.Keys = nil
	layout.Doors[0].Key = ""
	report, err := v.AnalyzeLayout(layout)
	if err != nil {
		t.Fatalf("analyse layout with a locked door: %v", err)
	}
	repairs := v.SuggestRepair(report)
	if len(repairs) == 0 {
		t.Fatalf("a layout with an unreachable boss needs at least one repair")
	}
	foundKey := false
	for _, repair := range repairs {
		if repair.Kind != impl.RepairAddDoor && repair.Kind != impl.RepairAddKey {
			t.Fatalf("repair = %+v, only add_door and add_key are allowed", repair)
		}
		if repair.Kind == impl.RepairAddKey && repair.Key == "k2" {
			foundKey = true
		}
	}
	if !foundKey {
		t.Fatalf("repairs = %+v, want an add_key repair for k2", repairs)
	}
	good := linearLayout()
	complete, err := v.AnalyzeLayout(good)
	if err != nil {
		t.Fatalf("analyse complete layout: %v", err)
	}
	if got := v.SuggestRepair(complete); len(got) != 0 {
		t.Fatalf("a complete layout needs no repair, got %+v", got)
	}
}

func TestSnapshotRestoreKeepsLayoutsAndTokens(t *testing.T) {
	v := impl.NewDungeonLayoutValidator(dungeonLimits())
	if _, err := v.Generate(42, layoutConfig()); err != nil {
		t.Fatalf("generate seed 42: %v", err)
	}
	if _, err := v.Validate(7, layoutConfig()); err != nil {
		t.Fatalf("validate seed 7: %v", err)
	}
	data, err := v.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewDungeonLayoutValidator(dungeonLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, ok := restored.LayoutOf(42); !ok {
		t.Fatalf("restored validator lost the generated layout of seed 42")
	}
	graph, err := restored.DependencyGraph(42)
	if err != nil {
		t.Fatalf("dependency graph after restore: %v", err)
	}
	if len(graph.Nodes) != 9 {
		t.Fatalf("graph nodes = %d, want 9", len(graph.Nodes))
	}
	if _, err := restored.DependencyGraph(999); !errors.Is(err, impl.ErrUnknownSeed) {
		t.Fatalf("an unknown seed must fail with ErrUnknownSeed, got %v", err)
	}
	tight := impl.NewDungeonLayoutValidator(impl.Limits{MaxRooms: 16, MaxEdges: 12, MaxVisit: 2})
	truncated, err := tight.Validate(7, layoutConfig())
	if !errors.Is(err, impl.ErrBudgetExceeded) {
		t.Fatalf("tight budget must truncate, got %v", err)
	}
	if _, err := restored.Resume(truncated.Token, 0); !errors.Is(err, impl.ErrInvalidBudget) {
		t.Fatalf("a zero budget must fail with ErrInvalidBudget, got %v", err)
	}
	if _, err := restored.Resume(nil, 5); !errors.Is(err, impl.ErrBadToken) {
		t.Fatalf("an empty token must fail with ErrBadToken, got %v", err)
	}
	if _, err := restored.Resume(impl.ResumeToken("garbage"), 5); !errors.Is(err, impl.ErrBadToken) {
		t.Fatalf("a malformed token must fail with ErrBadToken, got %v", err)
	}
	if err := restored.Restore([]byte("]")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := restored.LayoutOf(42); !ok {
		t.Fatalf("failed restore must keep the live layouts")
	}
}
