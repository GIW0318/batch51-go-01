package task

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-r-08"
)

func openGrid(w, h int) impl.Grid {
	return impl.Grid{Width: w, Height: h}
}

func baseCrowdConfig() impl.CrowdConfig {
	return impl.CrowdConfig{
		MaxUnits:        64,
		DensityLimit:    2,
		RecomputeBudget: 64,
		MaxObstacles:    8,
		MaxAudit:        16,
	}
}

func newCrowdGuard(t *testing.T, grid impl.Grid, cfg impl.CrowdConfig) *impl.NavmeshCrowdGuard {
	t.Helper()
	g, err := impl.NewNavmeshCrowdGuard(grid, cfg)
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	return g
}

func wallGrid() impl.Grid {
	g := impl.Grid{Width: 5, Height: 5, Blocked: make([]bool, 25)}
	for y := 0; y <= 3; y++ {
		g.Blocked[y*5+1] = true
	}
	return g
}

func cellBlocked(g impl.Grid, c impl.Cell) bool {
	if len(g.Blocked) == 0 {
		return false
	}
	return g.Blocked[c.Y*g.Width+c.X]
}

func TestNewGuardValidatesGridAndConfig(t *testing.T) {
	badGrids := []impl.Grid{
		{Width: 0, Height: 4},
		{Width: 4, Height: 0},
		{Width: 2, Height: 2, Blocked: make([]bool, 3)},
	}
	for i, grid := range badGrids {
		if _, err := impl.NewNavmeshCrowdGuard(grid, baseCrowdConfig()); !errors.Is(err, impl.ErrBadGrid) {
			t.Fatalf("grid %d must fail with ErrBadGrid, got %v", i, err)
		}
	}
	badConfigs := []impl.CrowdConfig{
		{MaxUnits: 0, DensityLimit: 2, RecomputeBudget: 8, MaxObstacles: 2, MaxAudit: 4},
		{MaxUnits: 8, DensityLimit: 0, RecomputeBudget: 8, MaxObstacles: 2, MaxAudit: 4},
		{MaxUnits: 8, DensityLimit: 2, RecomputeBudget: 0, MaxObstacles: 2, MaxAudit: 4},
		{MaxUnits: 8, DensityLimit: 2, RecomputeBudget: 8, MaxObstacles: 0, MaxAudit: 4},
		{MaxUnits: 8, DensityLimit: 2, RecomputeBudget: 8, MaxObstacles: 2, MaxAudit: 0},
	}
	for i, cfg := range badConfigs {
		if _, err := impl.NewNavmeshCrowdGuard(openGrid(5, 5), cfg); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("config %d must fail with ErrBadConfig, got %v", i, err)
		}
	}
	g := newCrowdGuard(t, openGrid(5, 5), baseCrowdConfig())
	if st := g.Stats(); st != (impl.GuardStats{}) {
		t.Fatalf("fresh stats = %+v, want all counters zero", st)
	}
	if got := len(g.Obstacles()); got != 0 {
		t.Fatalf("fresh obstacles = %d, want 0", got)
	}
	if g.Degraded() {
		t.Fatalf("a fresh guard must not report a degraded avoidance")
	}
}

func TestObstacleLifecycle(t *testing.T) {
	cfg := baseCrowdConfig()
	cfg.MaxObstacles = 1
	g := newCrowdGuard(t, openGrid(5, 5), cfg)
	if err := g.AddObstacle(impl.Obstacle{ID: "  ", Region: impl.Region{MinX: 1, MinY: 1, MaxX: 1, MaxY: 1}}); !errors.Is(err, impl.ErrBadObstacle) {
		t.Fatalf("blank obstacle id must fail with ErrBadObstacle, got %v", err)
	}
	if err := g.AddObstacle(impl.Obstacle{ID: "pillar", Region: impl.Region{MinX: 3, MinY: 3, MaxX: 1, MaxY: 1}}); !errors.Is(err, impl.ErrBadObstacle) {
		t.Fatalf("reversed obstacle region must fail with ErrBadObstacle, got %v", err)
	}
	if err := g.AddObstacle(impl.Obstacle{ID: "gate", Region: impl.Region{MinX: 4, MinY: 4, MaxX: 9, MaxY: 9}}); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("out of grid obstacle must fail with ErrOutOfRange, got %v", err)
	}
	pillar := impl.Obstacle{ID: "pillar", Region: impl.Region{MinX: 3, MinY: 3, MaxX: 3, MaxY: 3}}
	if err := g.AddObstacle(pillar); err != nil {
		t.Fatalf("add obstacle: %v", err)
	}
	if got := len(g.Obstacles()); got != 1 {
		t.Fatalf("obstacles = %d, want 1", got)
	}
	if err := g.AddObstacle(pillar); !errors.Is(err, impl.ErrObstacleExists) {
		t.Fatalf("duplicate obstacle must fail with ErrObstacleExists, got %v", err)
	}
	if err := g.AddObstacle(impl.Obstacle{ID: "gate", Region: impl.Region{MinX: 4, MinY: 4, MaxX: 4, MaxY: 4}}); !errors.Is(err, impl.ErrObstacleLimit) {
		t.Fatalf("second obstacle must fail with ErrObstacleLimit, got %v", err)
	}
	if err := g.RemoveObstacle("ghost"); !errors.Is(err, impl.ErrUnknownObstacle) {
		t.Fatalf("removing an unknown obstacle must fail with ErrUnknownObstacle, got %v", err)
	}
	if err := g.RemoveObstacle("pillar"); err != nil {
		t.Fatalf("remove obstacle: %v", err)
	}
	if got := len(g.Obstacles()); got != 0 {
		t.Fatalf("obstacles after removal = %d, want 0", got)
	}
}

func TestFindPathStraightLine(t *testing.T) {
	g := newCrowdGuard(t, openGrid(5, 5), baseCrowdConfig())
	got, err := g.FindPath(impl.PathRequest{UnitID: "hero", From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 0, Y: 3}})
	if err != nil {
		t.Fatalf("find path: %v", err)
	}
	want := []impl.Cell{{X: 0, Y: 0}, {X: 0, Y: 1}, {X: 0, Y: 2}, {X: 0, Y: 3}}
	if !reflect.DeepEqual(got.Points, want) {
		t.Fatalf("path = %v, want %v", got.Points, want)
	}
	if got.Cost != 3 {
		t.Fatalf("path cost = %d, want 3", got.Cost)
	}
	if !got.Reached || got.Partial {
		t.Fatalf("path flags reached=%v partial=%v, want true/false", got.Reached, got.Partial)
	}
}

func TestFindPathAvoidsBlockedCells(t *testing.T) {
	grid := wallGrid()
	g := newCrowdGuard(t, grid, baseCrowdConfig())
	got, err := g.FindPath(impl.PathRequest{UnitID: "hero", From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 2, Y: 0}})
	if err != nil {
		t.Fatalf("find path around the wall: %v", err)
	}
	if !got.Reached {
		t.Fatalf("path must reach the target, got %+v", got)
	}
	if len(got.Points) < 2 {
		t.Fatalf("path has %d points, want at least start and end", len(got.Points))
	}
	if got.Points[0] != (impl.Cell{X: 0, Y: 0}) || got.Points[len(got.Points)-1] != (impl.Cell{X: 2, Y: 0}) {
		t.Fatalf("path endpoints = %v..%v, want (0,0)..(2,0)", got.Points[0], got.Points[len(got.Points)-1])
	}
	if got.Cost != len(got.Points)-1 {
		t.Fatalf("path cost = %d, want %d steps", got.Cost, len(got.Points)-1)
	}
	if got.Cost <= 2 {
		t.Fatalf("path cost = %d, want a detour longer than the direct two steps", got.Cost)
	}
	for _, c := range got.Points {
		if cellBlocked(grid, c) {
			t.Fatalf("path crosses the blocked cell %v", c)
		}
	}
}

func TestFindPathRejectsBadRequest(t *testing.T) {
	g := newCrowdGuard(t, openGrid(5, 5), baseCrowdConfig())
	if _, err := g.FindPath(impl.PathRequest{UnitID: "hero", From: impl.Cell{X: -1, Y: 0}, To: impl.Cell{X: 1, Y: 1}}); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("out of range start must fail with ErrOutOfRange, got %v", err)
	}
	if _, err := g.FindPath(impl.PathRequest{UnitID: "hero", From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 5, Y: 1}}); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("out of range target must fail with ErrOutOfRange, got %v", err)
	}
	if _, err := g.FindPath(impl.PathRequest{UnitID: " ", From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 1, Y: 1}}); !errors.Is(err, impl.ErrBadUnit) {
		t.Fatalf("blank unit must fail with ErrBadUnit, got %v", err)
	}
	if got := g.Stats(); got.Paths != 0 {
		t.Fatalf("rejected requests produced %d paths, want 0", got.Paths)
	}
}

func TestFindPathBudgetExhausted(t *testing.T) {
	cfg := baseCrowdConfig()
	cfg.RecomputeBudget = 2
	g := newCrowdGuard(t, openGrid(5, 5), cfg)
	if _, err := g.FindPath(impl.PathRequest{UnitID: "hero", From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 0, Y: 4}}); !errors.Is(err, impl.ErrBudgetExhausted) {
		t.Fatalf("a five step path with a budget of two must fail with ErrBudgetExhausted, got %v", err)
	}
	if got := g.Stats(); got.Paths != 0 {
		t.Fatalf("paths after budget exhaustion = %d, want 0", got)
	}
}

func TestClaimAndReleaseCell(t *testing.T) {
	g := newCrowdGuard(t, openGrid(5, 5), baseCrowdConfig())
	target := impl.Cell{X: 1, Y: 1}
	first, err := g.ClaimCell(target, "tank-1", 5)
	if err != nil {
		t.Fatalf("claim cell: %v", err)
	}
	if !first.Granted || first.Owner != "tank-1" || first.Cell != target {
		t.Fatalf("claim result = %+v, want granted to tank-1 on %v", first, target)
	}
	if got := g.CrowdAt(target); got != 1 {
		t.Fatalf("crowd at the claimed cell = %d, want 1", got)
	}
	again, err := g.ClaimCell(target, "tank-1", 5)
	if err != nil {
		t.Fatalf("repeat claim: %v", err)
	}
	if !again.Granted {
		t.Fatalf("a repeated claim by the owner must stay granted, got %+v", again)
	}
	if got := g.CrowdAt(target); got != 1 {
		t.Fatalf("crowd after the repeated claim = %d, want 1", got)
	}
	if err := g.ReleaseCell(target, "tank-1"); err != nil {
		t.Fatalf("release cell: %v", err)
	}
	if got := g.CrowdAt(target); got != 0 {
		t.Fatalf("crowd after release = %d, want 0", got)
	}
	if err := g.ReleaseCell(target, "tank-1"); !errors.Is(err, impl.ErrNotOwner) {
		t.Fatalf("releasing twice must fail with ErrNotOwner, got %v", err)
	}
}

func TestClaimRejectsBlockedCell(t *testing.T) {
	grid := wallGrid()
	g := newCrowdGuard(t, grid, baseCrowdConfig())
	if _, err := g.ClaimCell(impl.Cell{X: 1, Y: 0}, "tank-1", 1); !errors.Is(err, impl.ErrCellBlocked) {
		t.Fatalf("claiming a static wall must fail with ErrCellBlocked, got %v", err)
	}
	if _, err := g.ClaimCell(impl.Cell{X: 5, Y: 0}, "tank-1", 1); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("claiming an out of range cell must fail with ErrOutOfRange, got %v", err)
	}
	if _, err := g.ClaimCell(impl.Cell{X: 2, Y: 2}, "  ", 1); !errors.Is(err, impl.ErrBadUnit) {
		t.Fatalf("claiming with a blank unit must fail with ErrBadUnit, got %v", err)
	}
	if err := g.AddObstacle(impl.Obstacle{ID: "pillar", Region: impl.Region{MinX: 3, MinY: 3, MaxX: 3, MaxY: 3}}); err != nil {
		t.Fatalf("add obstacle: %v", err)
	}
	if _, err := g.ClaimCell(impl.Cell{X: 3, Y: 3}, "tank-1", 1); !errors.Is(err, impl.ErrCellBlocked) {
		t.Fatalf("claiming a dynamically blocked cell must fail with ErrCellBlocked, got %v", err)
	}
}

func TestCrowdAtCountsDistinctUnits(t *testing.T) {
	g := newCrowdGuard(t, openGrid(5, 5), baseCrowdConfig())
	cell := impl.Cell{X: 2, Y: 2}
	for _, unit := range []string{"tank-1", "dps-1", "healer-1"} {
		if _, err := g.ClaimCell(cell, unit, 1); err != nil {
			t.Fatalf("claim for %s: %v", unit, err)
		}
	}
	if got := g.CrowdAt(cell); got != 3 {
		t.Fatalf("crowd at the shared cell = %d, want 3", got)
	}
	if got := g.CrowdAt(impl.Cell{X: 0, Y: 0}); got != 0 {
		t.Fatalf("crowd at an empty cell = %d, want 0", got)
	}
}

func TestAvoidStepDegradesUnderDensity(t *testing.T) {
	cfg := baseCrowdConfig()
	cfg.DensityLimit = 2
	g := newCrowdGuard(t, openGrid(5, 5), cfg)
	desired := impl.Cell{X: 1, Y: 1}
	if _, err := g.ClaimCell(desired, "tank-1", 1); err != nil {
		t.Fatalf("claim first: %v", err)
	}
	if _, err := g.ClaimCell(desired, "tank-2", 1); err != nil {
		t.Fatalf("claim second: %v", err)
	}
	step, err := g.AvoidStep("dps-1", impl.Cell{X: 0, Y: 0}, desired)
	if err != nil {
		t.Fatalf("avoid step at the density limit: %v", err)
	}
	if step != (impl.Cell{X: 0, Y: 0}) {
		t.Fatalf("crowded step = %v, want the unit to stay at (0,0)", step)
	}
	if !g.Degraded() {
		t.Fatalf("a step blocked by crowd density must report degraded avoidance")
	}
	if err := g.ReleaseCell(desired, "tank-2"); err != nil {
		t.Fatalf("release second: %v", err)
	}
	step, err = g.AvoidStep("dps-1", impl.Cell{X: 0, Y: 0}, desired)
	if err != nil {
		t.Fatalf("avoid step after the crowd thinned: %v", err)
	}
	if step != desired {
		t.Fatalf("step after release = %v, want %v", step, desired)
	}
	if g.Degraded() {
		t.Fatalf("degraded flag must clear once the crowd falls below the limit")
	}
	if _, err := g.AvoidStep("dps-1", impl.Cell{X: 0, Y: 0}, impl.Cell{X: 9, Y: 9}); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("avoid step outside the grid must fail with ErrOutOfRange, got %v", err)
	}
}

func TestRecomputeOnlyAffectedPaths(t *testing.T) {
	g := newCrowdGuard(t, openGrid(6, 6), baseCrowdConfig())
	routeA, err := g.FindPath(impl.PathRequest{UnitID: "unit-a", From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 0, Y: 3}})
	if err != nil {
		t.Fatalf("find path for unit-a: %v", err)
	}
	routeB, err := g.FindPath(impl.PathRequest{UnitID: "unit-b", From: impl.Cell{X: 3, Y: 0}, To: impl.Cell{X: 3, Y: 3}})
	if err != nil {
		t.Fatalf("find path for unit-b: %v", err)
	}
	if err := g.AddObstacle(impl.Obstacle{ID: "pillar", Region: impl.Region{MinX: 0, MinY: 1, MaxX: 0, MaxY: 2}}); err != nil {
		t.Fatalf("add obstacle: %v", err)
	}
	report, err := g.Recompute(impl.Region{MinX: 0, MinY: 1, MaxX: 0, MaxY: 2})
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if !reflect.DeepEqual(report.Units, []string{"unit-a"}) {
		t.Fatalf("affected units = %v, want [unit-a]", report.Units)
	}
	if report.Recomputed != 1 {
		t.Fatalf("recomputed routes = %d, want 1", report.Recomputed)
	}
	afterB, err := g.FindPath(impl.PathRequest{UnitID: "unit-b", From: impl.Cell{X: 3, Y: 0}, To: impl.Cell{X: 3, Y: 3}})
	if err != nil {
		t.Fatalf("find path for unit-b again: %v", err)
	}
	if !reflect.DeepEqual(routeB.Points, afterB.Points) {
		t.Fatalf("unit-b path changed from %v to %v", routeB.Points, afterB.Points)
	}
	if routeA.Points[0] != (impl.Cell{X: 0, Y: 0}) {
		t.Fatalf("unit-a path lost its start cell: %v", routeA.Points)
	}
}

func TestRecomputeRegionValidation(t *testing.T) {
	g := newCrowdGuard(t, openGrid(6, 6), baseCrowdConfig())
	if _, err := g.Recompute(impl.Region{MinX: 4, MinY: 4, MaxX: 2, MaxY: 2}); !errors.Is(err, impl.ErrBadRegion) {
		t.Fatalf("reversed region must fail with ErrBadRegion, got %v", err)
	}
	if _, err := g.Recompute(impl.Region{MinX: 0, MinY: 0, MaxX: 6, MaxY: 1}); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("region outside the grid must fail with ErrOutOfRange, got %v", err)
	}
}

func TestStatsCounters(t *testing.T) {
	cfg := baseCrowdConfig()
	cfg.DensityLimit = 1
	g := newCrowdGuard(t, openGrid(5, 5), cfg)
	for _, unit := range []string{"unit-a", "unit-b"} {
		if _, err := g.FindPath(impl.PathRequest{UnitID: unit, From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 0, Y: 2}}); err != nil {
			t.Fatalf("find path for %s: %v", unit, err)
		}
	}
	cell := impl.Cell{X: 1, Y: 1}
	if _, err := g.ClaimCell(cell, "unit-a", 1); err != nil {
		t.Fatalf("claim unit-a: %v", err)
	}
	if _, err := g.ClaimCell(cell, "unit-b", 1); err != nil {
		t.Fatalf("claim unit-b: %v", err)
	}
	if err := g.ReleaseCell(cell, "unit-a"); err != nil {
		t.Fatalf("release unit-a: %v", err)
	}
	if _, err := g.AvoidStep("unit-c", impl.Cell{X: 0, Y: 0}, cell); err != nil {
		t.Fatalf("avoid step: %v", err)
	}
	st := g.Stats()
	if st.Queries != 2 || st.Paths != 2 || st.Claims != 2 || st.Releases != 1 || st.Degraded != 1 {
		t.Fatalf("stats = %+v, want queries 2, paths 2, claims 2, releases 1, degraded 1", st)
	}
}

func TestSnapshotRestoreKeepsRoutesAndClaims(t *testing.T) {
	g := newCrowdGuard(t, openGrid(5, 5), baseCrowdConfig())
	if err := g.AddObstacle(impl.Obstacle{ID: "pillar", Region: impl.Region{MinX: 4, MinY: 4, MaxX: 4, MaxY: 4}}); err != nil {
		t.Fatalf("add obstacle: %v", err)
	}
	cell := impl.Cell{X: 2, Y: 2}
	if _, err := g.ClaimCell(cell, "tank-1", 1); err != nil {
		t.Fatalf("claim tank-1: %v", err)
	}
	if _, err := g.ClaimCell(cell, "tank-2", 1); err != nil {
		t.Fatalf("claim tank-2: %v", err)
	}
	if _, err := g.FindPath(impl.PathRequest{UnitID: "tank-1", From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 3, Y: 3}}); err != nil {
		t.Fatalf("find path: %v", err)
	}
	before := g.Stats()
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newCrowdGuard(t, openGrid(5, 5), baseCrowdConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Obstacles()); got != 1 {
		t.Fatalf("restored obstacles = %d, want 1", got)
	}
	if got := restored.CrowdAt(cell); got != 2 {
		t.Fatalf("restored crowd at %v = %d, want 2", cell, got)
	}
	if got := restored.Stats(); got != before {
		t.Fatalf("restored stats = %+v, want %+v", got, before)
	}
	route, err := restored.FindPath(impl.PathRequest{UnitID: "tank-1", From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 3, Y: 3}})
	if err != nil || !route.Reached {
		t.Fatalf("find path after restore = %+v/%v, want a reached route", route, err)
	}
	if err := restored.Restore([]byte("{broken-navigation-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.CrowdAt(cell); got != 2 {
		t.Fatalf("a rejected restore changed the crowd to %d, want 2", got)
	}
}

func TestConcurrentClaimsAndPaths(t *testing.T) {
	cfg := baseCrowdConfig()
	cfg.MaxUnits = 64
	cfg.DensityLimit = 4
	g := newCrowdGuard(t, openGrid(6, 6), cfg)
	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			unit := "unit-" + string(rune('a'+i))
			if _, err := g.ClaimCell(impl.Cell{X: 2, Y: 2}, unit, 1); err != nil {
				t.Errorf("concurrent claim %d: %v", i, err)
			}
			if _, err := g.FindPath(impl.PathRequest{UnitID: unit, From: impl.Cell{X: 0, Y: 0}, To: impl.Cell{X: 5, Y: 5}}); err != nil {
				t.Errorf("concurrent path %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := g.CrowdAt(impl.Cell{X: 2, Y: 2}); got != workers {
		t.Fatalf("crowd after the race = %d, want %d", got, workers)
	}
	if st := g.Stats(); st.Claims != workers || st.Paths != workers {
		t.Fatalf("stats after the race = %+v, want %d claims and paths", st, workers)
	}
}
