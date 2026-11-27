package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-b-11"
)

func baseFurConfig() impl.FurConfig {
	return impl.FurConfig{
		MaxShells:      12,
		ShellBudget:    40,
		SampleBudget:   4000,
		LODDistances:   []float32{5, 15},
		LODShells:      []int{8, 4, 2},
		HysteresisBand: 1,
		FurLengthMax:   0.12,
		MinStep:        0.002,
	}
}

func configuredFurPipeline(t *testing.T) *impl.FurShellPipeline {
	t.Helper()
	p := impl.NewFurShellPipeline()
	if err := p.Configure(baseFurConfig()); err != nil {
		t.Fatalf("configure pipeline: %v", err)
	}
	return p
}

func TestPlanLayersPicksShellCountByDistance(t *testing.T) {
	p := configuredFurPipeline(t)
	if err := p.SetGroomer("fur-a", impl.GroomData{ID: "fur-a", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	if err := p.BeginFrame(1); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	cases := []struct {
		dist     float32
		band     int
		full     int
		stepWant float32
	}{
		{2, 0, 8, 0.01},
		{10, 1, 4, 0.02},
		{40, 2, 2, 0.04},
	}
	for _, c := range cases {
		plan, err := p.PlanLayers(impl.ViewParams{ViewportID: fmt.Sprintf("vp-%v", c.dist), GroomID: "fur-a", CameraDist: c.dist})
		if err != nil {
			t.Fatalf("plan at distance %v: %v", c.dist, err)
		}
		if plan.Band != c.band {
			t.Fatalf("distance %v selected band %d, want %d", c.dist, plan.Band, c.band)
		}
		if plan.FullShells != c.full {
			t.Fatalf("distance %v planned %d shells, want %d", c.dist, plan.FullShells, c.full)
		}
		if plan.Shells != c.full || plan.Trimmed != 0 {
			t.Fatalf("distance %v plan shells=%d trimmed=%d, want %d and 0", c.dist, plan.Shells, plan.Trimmed, c.full)
		}
		if plan.Generation != 1 {
			t.Fatalf("plan generation = %d, want 1", plan.Generation)
		}
		if diff := plan.Step - c.stepWant; diff > 1e-5 || diff < -1e-5 {
			t.Fatalf("distance %v step = %v, want %v", c.dist, plan.Step, c.stepWant)
		}
	}
}

func TestConfigureRejectsBadLODTableAndLimits(t *testing.T) {
	bad := []impl.FurConfig{
		{MaxShells: 1, ShellBudget: 40, SampleBudget: 4000, LODDistances: []float32{5}, LODShells: []int{2, 1}, FurLengthMax: 0.12, MinStep: 0.002},
		{MaxShells: 12, ShellBudget: 0, SampleBudget: 4000, LODDistances: []float32{5}, LODShells: []int{2, 1}, FurLengthMax: 0.12, MinStep: 0.002},
		{MaxShells: 12, ShellBudget: 40, SampleBudget: 4000, LODDistances: []float32{5, 15}, LODShells: []int{8}, FurLengthMax: 0.12, MinStep: 0.002},
		{MaxShells: 12, ShellBudget: 40, SampleBudget: 4000, LODDistances: []float32{15, 5}, LODShells: []int{8, 4, 2}, FurLengthMax: 0.12, MinStep: 0.002},
		{MaxShells: 12, ShellBudget: 40, SampleBudget: 4000, LODDistances: []float32{5, 15}, LODShells: []int{2, 4, 8}, FurLengthMax: 0.12, MinStep: 0.002},
		{MaxShells: 12, ShellBudget: 40, SampleBudget: 4000, LODDistances: []float32{5, 15}, LODShells: []int{8, 4, 2}, HysteresisBand: 5, FurLengthMax: 0.12, MinStep: 0.002},
		{MaxShells: 12, ShellBudget: 40, SampleBudget: 4000, LODDistances: []float32{5, 15}, LODShells: []int{8, 4, 2}, HysteresisBand: 1, FurLengthMax: 0, MinStep: 0.002},
	}
	for i, cfg := range bad {
		p := impl.NewFurShellPipeline()
		if err := p.Configure(cfg); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("config %d must be rejected with ErrBadConfig, got %v", i, err)
		}
	}
	p := impl.NewFurShellPipeline()
	if err := p.Configure(baseFurConfig()); err != nil {
		t.Fatalf("valid configuration must be accepted, got %v", err)
	}
	if _, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-x", CameraDist: 3}); errors.Is(err, impl.ErrNotConfigured) {
		t.Fatalf("a configured pipeline must leave the not configured state")
	}
}

func TestSetGroomerValidatesAndBumpsGeneration(t *testing.T) {
	p := configuredFurPipeline(t)
	bad := []struct {
		id    string
		groom impl.GroomData
	}{
		{"   ", impl.GroomData{ID: "   ", FurLength: 0.05, BaseAlpha: 0.5}},
		{"fur-a", impl.GroomData{ID: "fur-b", FurLength: 0.05, BaseAlpha: 0.5}},
		{"fur-a", impl.GroomData{ID: "fur-a", FurLength: 0, BaseAlpha: 0.5}},
		{"fur-a", impl.GroomData{ID: "fur-a", FurLength: -0.02, BaseAlpha: 0.5}},
		{"fur-a", impl.GroomData{ID: "fur-a", FurLength: 0.5, BaseAlpha: 0.5}},
		{"fur-a", impl.GroomData{ID: "fur-a", FurLength: 0.05, BaseAlpha: 0}},
		{"fur-a", impl.GroomData{ID: "fur-a", FurLength: 0.05, BaseAlpha: 1.5}},
	}
	for i, c := range bad {
		if err := p.SetGroomer(c.id, c.groom); !errors.Is(err, impl.ErrBadGroom) {
			t.Fatalf("bad groom %d must be rejected with ErrBadGroom, got %v", i, err)
		}
	}
	if _, err := p.Generation("fur-a"); !errors.Is(err, impl.ErrUnknownGroom) {
		t.Fatalf("a rejected groom must not be registered, got %v", err)
	}
	if err := p.SetGroomer("fur-a", impl.GroomData{ID: "fur-a", FurLength: 0.05, BaseAlpha: 0.5}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	gen, err := p.Generation("fur-a")
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if gen != 1 {
		t.Fatalf("first generation = %d, want 1", gen)
	}
	if err := p.SetGroomer("fur-a", impl.GroomData{ID: "fur-a", FurLength: 0.06, BaseAlpha: 0.7}); err != nil {
		t.Fatalf("replace groomer: %v", err)
	}
	gen, err = p.Generation("fur-a")
	if err != nil {
		t.Fatalf("generation after replace: %v", err)
	}
	if gen != 2 {
		t.Fatalf("generation after one replacement = %d, want 2", gen)
	}
}

func TestPlanLayersRejectsUnknownGroomAndBadView(t *testing.T) {
	p := configuredFurPipeline(t)
	if err := p.SetGroomer("fur-a", impl.GroomData{ID: "fur-a", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	if err := p.BeginFrame(1); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	if _, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-404", CameraDist: 3}); !errors.Is(err, impl.ErrUnknownGroom) {
		t.Fatalf("unknown groom must fail with ErrUnknownGroom, got %v", err)
	}
	if _, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-a", CameraDist: 0}); !errors.Is(err, impl.ErrBadView) {
		t.Fatalf("zero camera distance must fail with ErrBadView, got %v", err)
	}
	if _, err := p.PlanLayers(impl.ViewParams{ViewportID: "", GroomID: "fur-a", CameraDist: 3}); !errors.Is(err, impl.ErrBadView) {
		t.Fatalf("blank viewport id must fail with ErrBadView, got %v", err)
	}
}

func TestPlanBeforeConfigureFails(t *testing.T) {
	p := impl.NewFurShellPipeline()
	if err := p.SetGroomer("fur-a", impl.GroomData{ID: "fur-a", FurLength: 0.08, BaseAlpha: 0.8}); !errors.Is(err, impl.ErrNotConfigured) {
		t.Fatalf("set groomer before configure must fail with ErrNotConfigured, got %v", err)
	}
	if _, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-a", CameraDist: 3}); !errors.Is(err, impl.ErrNotConfigured) {
		t.Fatalf("plan before configure must fail with ErrNotConfigured, got %v", err)
	}
	if err := p.BeginFrame(1); !errors.Is(err, impl.ErrNotConfigured) {
		t.Fatalf("begin frame before configure must fail with ErrNotConfigured, got %v", err)
	}
}

func TestBuildShellsOffsetsAndAlphaFalloff(t *testing.T) {
	p := configuredFurPipeline(t)
	if err := p.SetGroomer("fur-b", impl.GroomData{ID: "fur-b", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	if err := p.BeginFrame(1); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	plan, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp-main", GroomID: "fur-b", CameraDist: 2})
	if err != nil {
		t.Fatalf("plan layers: %v", err)
	}
	shells, err := p.BuildShells(plan)
	if err != nil {
		t.Fatalf("build shells: %v", err)
	}
	if len(shells) != 8 {
		t.Fatalf("built %d shells, want 8", len(shells))
	}
	for i, sh := range shells {
		if sh.Index != i {
			t.Fatalf("shell %d reports index %d, want the ascending stack", i, sh.Index)
		}
		wantOffset := float32(i) * 0.01
		if diff := sh.Offset - wantOffset; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("shell %d offset = %v, want %v", i, sh.Offset, wantOffset)
		}
		if sh.GroomID != "fur-b" || sh.ViewportID != "vp-main" {
			t.Fatalf("shell %d carries groom=%q viewport=%q", i, sh.GroomID, sh.ViewportID)
		}
		if sh.Culled {
			t.Fatalf("shell %d must not be culled when the budget is free", i)
		}
		wantAlpha := 0.8 * float32(8-i) / 8
		if diff := sh.Alpha - wantAlpha; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("shell %d alpha = %v, want %v", i, sh.Alpha, wantAlpha)
		}
	}
}

func TestBuildShellsRejectsStaleGeneration(t *testing.T) {
	p := configuredFurPipeline(t)
	if err := p.SetGroomer("fur-c", impl.GroomData{ID: "fur-c", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	if err := p.BeginFrame(1); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	stale, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-c", CameraDist: 2})
	if err != nil {
		t.Fatalf("plan layers: %v", err)
	}
	if err := p.SetGroomer("fur-c", impl.GroomData{ID: "fur-c", FurLength: 0.06, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("replace groomer: %v", err)
	}
	if _, err := p.BuildShells(stale); !errors.Is(err, impl.ErrStalePlan) {
		t.Fatalf("building an older generation plan must fail with ErrStalePlan, got %v", err)
	}
	fresh, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-c", CameraDist: 2})
	if err != nil {
		t.Fatalf("plan after the replacement: %v", err)
	}
	if fresh.Generation != stale.Generation+1 {
		t.Fatalf("fresh plan generation = %d, want %d", fresh.Generation, stale.Generation+1)
	}
	if _, err := p.BuildShells(fresh); err != nil {
		t.Fatalf("building the fresh plan must succeed, got %v", err)
	}
}

func TestFrameShellBudgetTrimsFarShellsFirst(t *testing.T) {
	cfg := baseFurConfig()
	cfg.ShellBudget = 10
	p := impl.NewFurShellPipeline()
	if err := p.Configure(cfg); err != nil {
		t.Fatalf("configure pipeline: %v", err)
	}
	if err := p.SetGroomer("fur-d", impl.GroomData{ID: "fur-d", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	if err := p.BeginFrame(1); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	first, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp-a", GroomID: "fur-d", CameraDist: 2})
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}
	if first.Shells != 8 || first.Trimmed != 0 {
		t.Fatalf("first plan shells=%d trimmed=%d, want 8 and 0", first.Shells, first.Trimmed)
	}
	second, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp-b", GroomID: "fur-d", CameraDist: 2})
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if second.Shells != 2 || second.Trimmed != 6 {
		t.Fatalf("second plan shells=%d trimmed=%d, want 2 and 6", second.Shells, second.Trimmed)
	}
	if second.FullShells != 8 {
		t.Fatalf("second plan full shells = %d, want 8", second.FullShells)
	}
	if diff := second.Step - 0.01; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("second plan step = %v, want the untrimmed spacing 0.01", second.Step)
	}
	shells, err := p.BuildShells(second)
	if err != nil {
		t.Fatalf("build the trimmed plan: %v", err)
	}
	if len(shells) != 2 {
		t.Fatalf("trimmed plan built %d shells, want 2", len(shells))
	}
	third, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp-c", GroomID: "fur-d", CameraDist: 2})
	if err != nil {
		t.Fatalf("third plan: %v", err)
	}
	if third.Shells != 0 || third.Trimmed != 8 {
		t.Fatalf("third plan shells=%d trimmed=%d, want 0 and 8", third.Shells, third.Trimmed)
	}
}

func TestResolveAlphaScalesByDensity(t *testing.T) {
	p := configuredFurPipeline(t)
	if err := p.SetGroomer("fur-e", impl.GroomData{ID: "fur-e", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	tex := impl.DensityMap{ID: "fur-e", Width: 2, Height: 2, Values: []float32{0.25, 0.5, 0.75, 1}}
	if err := p.SetDensity("fur-e", tex); err != nil {
		t.Fatalf("set density: %v", err)
	}
	if err := p.BeginFrame(1); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	plan, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-e", CameraDist: 2})
	if err != nil {
		t.Fatalf("plan layers: %v", err)
	}
	if _, err := p.BuildShells(plan); err != nil {
		t.Fatalf("build shells: %v", err)
	}
	root, err := p.ResolveAlpha(0, impl.Vec2{U: 0.25, V: 0.25})
	if err != nil {
		t.Fatalf("resolve root alpha: %v", err)
	}
	if diff := root - 0.2; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("root alpha at the low corner = %v, want 0.8*0.25 = 0.2", root)
	}
	dense, err := p.ResolveAlpha(0, impl.Vec2{U: 0.75, V: 0.75})
	if err != nil {
		t.Fatalf("resolve dense alpha: %v", err)
	}
	if diff := dense - 0.8; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("root alpha at the dense corner = %v, want 0.8", dense)
	}
	tip, err := p.ResolveAlpha(7, impl.Vec2{U: 0.25, V: 0.25})
	if err != nil {
		t.Fatalf("resolve tip alpha: %v", err)
	}
	if diff := tip - 0.025; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("tip alpha = %v, want 0.1*0.25 = 0.025", tip)
	}
}

func TestResolveAlphaRejectsUnknownShell(t *testing.T) {
	p := configuredFurPipeline(t)
	if err := p.SetGroomer("fur-f", impl.GroomData{ID: "fur-f", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	if err := p.BeginFrame(1); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	plan, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-f", CameraDist: 2})
	if err != nil {
		t.Fatalf("plan layers: %v", err)
	}
	if _, err := p.BuildShells(plan); err != nil {
		t.Fatalf("build shells: %v", err)
	}
	if _, err := p.ResolveAlpha(8, impl.Vec2{U: 0.5, V: 0.5}); !errors.Is(err, impl.ErrNoShell) {
		t.Fatalf("shell 8 of an eight shell plan must fail with ErrNoShell, got %v", err)
	}
	if _, err := p.ResolveAlpha(-1, impl.Vec2{U: 0.5, V: 0.5}); !errors.Is(err, impl.ErrNoShell) {
		t.Fatalf("negative shell index must fail with ErrNoShell, got %v", err)
	}
}

func TestSetDensityRejectsMalformedMap(t *testing.T) {
	p := configuredFurPipeline(t)
	if err := p.SetGroomer("fur-g", impl.GroomData{ID: "fur-g", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	if err := p.SetDensity("fur-404", impl.DensityMap{Width: 1, Height: 1, Values: []float32{0.5}}); !errors.Is(err, impl.ErrUnknownGroom) {
		t.Fatalf("density for an unknown groom must fail with ErrUnknownGroom, got %v", err)
	}
	bad := []impl.DensityMap{
		{Width: 0, Height: 2, Values: []float32{0.5, 0.5}},
		{Width: 2, Height: 2, Values: []float32{0.5, 0.5}},
		{Width: 2, Height: 2, Values: []float32{0.5, 0.5, 0.5, 1.5}},
		{Width: 2, Height: 2, Values: []float32{0.5, -0.1, 0.5, 0.5}},
	}
	for i, tex := range bad {
		if err := p.SetDensity("fur-g", tex); !errors.Is(err, impl.ErrBadDensity) {
			t.Fatalf("bad density map %d must fail with ErrBadDensity, got %v", i, err)
		}
	}
	if err := p.SetDensity("fur-g", impl.DensityMap{Width: 2, Height: 2, Values: []float32{0, 0.5, 0.5, 1}}); err != nil {
		t.Fatalf("well formed density map must be accepted, got %v", err)
	}
}

func TestStatsReportFrameCounters(t *testing.T) {
	p := configuredFurPipeline(t)
	for _, id := range []string{"fur-h", "fur-i"} {
		if err := p.SetGroomer(id, impl.GroomData{ID: id, FurLength: 0.08, BaseAlpha: 0.9}); err != nil {
			t.Fatalf("set groomer %s: %v", id, err)
		}
	}
	if err := p.BeginFrame(7); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	total := 0
	for i, id := range []string{"fur-h", "fur-i"} {
		plan, err := p.PlanLayers(impl.ViewParams{ViewportID: fmt.Sprintf("vp-%d", i), GroomID: id, CameraDist: 2})
		if err != nil {
			t.Fatalf("plan for %s: %v", id, err)
		}
		shells, err := p.BuildShells(plan)
		if err != nil {
			t.Fatalf("build for %s: %v", id, err)
		}
		total += len(shells)
	}
	st := p.Stats()
	if st.Frame != 7 {
		t.Fatalf("stats frame = %d, want 7", st.Frame)
	}
	if st.Grooms != 2 {
		t.Fatalf("stats grooms = %d, want 2", st.Grooms)
	}
	if st.Plans != 2 {
		t.Fatalf("stats plans = %d, want 2", st.Plans)
	}
	if st.Shells != total || total != 16 {
		t.Fatalf("stats shells = %d, want the %d built instances", st.Shells, total)
	}
	if err := p.BeginFrame(8); err != nil {
		t.Fatalf("begin second frame: %v", err)
	}
	after := p.Stats()
	if after.Frame != 8 || after.Shells != 0 || after.Plans != 0 {
		t.Fatalf("new frame counters frame=%d shells=%d plans=%d, want 8, 0 and 0", after.Frame, after.Shells, after.Plans)
	}
	if after.Grooms != 2 {
		t.Fatalf("grooms must survive the frame rollover, got %d", after.Grooms)
	}
}

func TestConcurrentGroomerUpdatesAreAtomic(t *testing.T) {
	p := configuredFurPipeline(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = p.SetGroomer("fur-z", impl.GroomData{ID: "fur-z", FurLength: 0.01 * float32(i+1), BaseAlpha: 0.5})
		}(i)
	}
	wg.Wait()
	gen, err := p.Generation("fur-z")
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if gen != 8 {
		t.Fatalf("generation after eight replacements = %d, want 8", gen)
	}
	if err := p.BeginFrame(1); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	plan, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-z", CameraDist: 2})
	if err != nil {
		t.Fatalf("plan after the race: %v", err)
	}
	if plan.Generation != 8 {
		t.Fatalf("plan generation = %d, want 8", plan.Generation)
	}
	if plan.FullShells != 8 {
		t.Fatalf("plan full shells = %d, want 8", plan.FullShells)
	}
	shells, err := p.BuildShells(plan)
	if err != nil {
		t.Fatalf("build after the race: %v", err)
	}
	if len(shells) != 8 {
		t.Fatalf("built %d shells, want 8", len(shells))
	}
}

func TestSnapshotRestoreKeepsGroomersAndGeneration(t *testing.T) {
	p := configuredFurPipeline(t)
	if err := p.SetGroomer("fur-s", impl.GroomData{ID: "fur-s", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	if err := p.SetGroomer("fur-s", impl.GroomData{ID: "fur-s", FurLength: 0.06, BaseAlpha: 0.7}); err != nil {
		t.Fatalf("replace groomer: %v", err)
	}
	if err := p.SetDensity("fur-s", impl.DensityMap{Width: 2, Height: 2, Values: []float32{1, 1, 1, 1}}); err != nil {
		t.Fatalf("set density: %v", err)
	}
	if err := p.BeginFrame(3); err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	if _, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-s", CameraDist: 10}); err != nil {
		t.Fatalf("plan layers: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewFurShellPipeline()
	if err := restored.Configure(baseFurConfig()); err != nil {
		t.Fatalf("configure restored pipeline: %v", err)
	}
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	gen, err := restored.Generation("fur-s")
	if err != nil {
		t.Fatalf("generation after restore: %v", err)
	}
	if gen != 2 {
		t.Fatalf("restored generation = %d, want 2", gen)
	}
	if err := restored.BeginFrame(9); err != nil {
		t.Fatalf("begin frame after restore: %v", err)
	}
	plan, err := restored.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-s", CameraDist: 10})
	if err != nil {
		t.Fatalf("plan after restore: %v", err)
	}
	if plan.FullShells != 4 || plan.Generation != 2 {
		t.Fatalf("restored plan shells=%d generation=%d, want 4 and 2", plan.FullShells, plan.Generation)
	}
	shells, err := restored.BuildShells(plan)
	if err != nil {
		t.Fatalf("build after restore: %v", err)
	}
	if len(shells) != 4 {
		t.Fatalf("restored plan built %d shells, want 4", len(shells))
	}
	if err := restored.SetGroomer("fur-s", impl.GroomData{ID: "fur-s", FurLength: 0.05, BaseAlpha: 0.6}); err != nil {
		t.Fatalf("replace after restore: %v", err)
	}
	gen, err = restored.Generation("fur-s")
	if err != nil {
		t.Fatalf("generation after restore and replace: %v", err)
	}
	if gen != 3 {
		t.Fatalf("generation after restore and replace = %d, want 3", gen)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	p := configuredFurPipeline(t)
	if err := p.SetGroomer("fur-t", impl.GroomData{ID: "fur-t", FurLength: 0.08, BaseAlpha: 0.8}); err != nil {
		t.Fatalf("set groomer: %v", err)
	}
	if err := p.Restore([]byte("{\"grooms\":")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := p.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	gen, err := p.Generation("fur-t")
	if err != nil {
		t.Fatalf("live groom must survive a rejected restore, got %v", err)
	}
	if gen != 1 {
		t.Fatalf("live generation after a rejected restore = %d, want 1", gen)
	}
	if err := p.BeginFrame(1); err != nil {
		t.Fatalf("begin frame after a rejected restore: %v", err)
	}
	if _, err := p.PlanLayers(impl.ViewParams{ViewportID: "vp", GroomID: "fur-t", CameraDist: 2}); err != nil {
		t.Fatalf("plan after a rejected restore: %v", err)
	}
}
