package task

import (
	"errors"
	"math"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-b-01"
)

func baseLimits() impl.BakeLimits {
	return impl.BakeLimits{MaxProfiles: 4, MaxRows: 32, RowBudget: 16}
}

func budgetLimits(rowBudget int) impl.BakeLimits {
	return impl.BakeLimits{MaxProfiles: 4, MaxRows: 32, RowBudget: rowBudget}
}

func baseScatter() impl.ScatterConfig {
	return impl.ScatterConfig{
		Scatter:      impl.Spectrum{R: 0.9, G: 0.6, B: 0.45},
		MeanFreePath: impl.Spectrum{R: 1.2, G: 0.8, B: 0.5},
		Curve: []impl.ThicknessPoint{
			{Thickness: 0, Radius: 0},
			{Thickness: 2, Radius: 4},
		},
		Curvature:    0,
		ThicknessMin: 0.1,
		ThicknessMax: 1.9,
		MaxRadius:    10,
		Rows:         8,
	}
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

func TestBakeProducesCompleteTable(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	table, err := b.Bake("skin-hero", baseScatter())
	if err != nil {
		t.Fatalf("bake with a valid config must succeed, got %v", err)
	}
	if !table.Complete {
		t.Fatalf("table complete = false with a budget of 16 rows for 8 rows")
	}
	if len(table.Rows) != 8 {
		t.Fatalf("baked rows = %d, want 8", len(table.Rows))
	}
	if table.Version != 1 {
		t.Fatalf("first committed version = %d, want 1", table.Version)
	}
	if table.ID != "skin-hero" {
		t.Fatalf("table id = %q, want skin-hero", table.ID)
	}
	for i, row := range table.Rows {
		if row.Index != i {
			t.Fatalf("row %d carries index %d", i, row.Index)
		}
		want := 0.1 + 1.8*float64(i)/7
		if !almostEqual(row.Thickness, want) {
			t.Fatalf("row %d thickness = %v, want %v", i, row.Thickness, want)
		}
	}
	version, err := b.Version("skin-hero")
	if err != nil || version != 1 {
		t.Fatalf("committed version = %d err=%v, want 1 and nil", version, err)
	}
	if got := b.Count(); got != 1 {
		t.Fatalf("committed table count = %d, want 1", got)
	}
}

func TestBakeNormalizesChannels(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	table, err := b.Bake("skin-norm", baseScatter())
	if err != nil {
		t.Fatalf("bake: %v", err)
	}
	var sumR, sumG, sumB float64
	for _, row := range table.Rows {
		sumR += row.Diffusion.R
		sumG += row.Diffusion.G
		sumB += row.Diffusion.B
	}
	if !almostEqual(sumR, 1) || !almostEqual(sumG, 1) || !almostEqual(sumB, 1) {
		t.Fatalf("channel sums = (%v,%v,%v), want each channel normalized to 1", sumR, sumG, sumB)
	}
	if !almostEqual(table.TotalEnergy.R, 1) || !almostEqual(table.TotalEnergy.G, 1) || !almostEqual(table.TotalEnergy.B, 1) {
		t.Fatalf("total energy = %+v, want one unit per channel", table.TotalEnergy)
	}
}

func TestBakeMapsThicknessToMonotonicRadius(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	table, err := b.Bake("skin-radius", baseScatter())
	if err != nil {
		t.Fatalf("bake: %v", err)
	}
	prev := -1.0
	for i, row := range table.Rows {
		if row.Radius < prev {
			t.Fatalf("row %d radius %v drops below the previous %v", i, row.Radius, prev)
		}
		prev = row.Radius
		want := 2 * row.Thickness
		if !almostEqual(row.Radius, want) {
			t.Fatalf("row %d radius = %v, want the curve value %v", i, row.Radius, want)
		}
		if row.Radius < 0 || row.Radius > baseScatter().MaxRadius {
			t.Fatalf("row %d radius %v leaves the [0,%v] range", i, row.Radius, baseScatter().MaxRadius)
		}
	}
}

func TestBakeRejectsBadConfig(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	cases := map[string]func(*impl.ScatterConfig){
		"single row":        func(c *impl.ScatterConfig) { c.Rows = 1 },
		"zero mfp":          func(c *impl.ScatterConfig) { c.MeanFreePath.G = 0 },
		"negative scatter":  func(c *impl.ScatterConfig) { c.Scatter.B = -0.1 },
		"scatter over one":  func(c *impl.ScatterConfig) { c.Scatter.R = 1.5 },
		"short curve":       func(c *impl.ScatterConfig) { c.Curve = c.Curve[:1] },
		"descending radius": func(c *impl.ScatterConfig) { c.Curve[1].Radius = -3 },
		"flat thickness":    func(c *impl.ScatterConfig) { c.Curve[1].Thickness = 0 },
		"bad band":          func(c *impl.ScatterConfig) { c.ThicknessMin = 2 },
		"bad max radius":    func(c *impl.ScatterConfig) { c.MaxRadius = 0 },
	}
	for name, mutate := range cases {
		cfg := baseScatter()
		mutate(&cfg)
		if _, err := b.Bake("skin-bad", cfg); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("%s: bake error = %v, want ErrBadConfig", name, err)
		}
	}
	if got := b.Count(); got != 0 {
		t.Fatalf("rejected configs committed %d tables, want 0", got)
	}
}

func TestBakeEnforcesRowLimit(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	cfg := baseScatter()
	cfg.Rows = 33
	if _, err := b.Bake("skin-tall", cfg); !errors.Is(err, impl.ErrRowLimit) {
		t.Fatalf("33 rows over a cap of 32 must fail with ErrRowLimit, got %v", err)
	}
	if got := b.Count(); got != 0 {
		t.Fatalf("row limited bake committed %d tables, want 0", got)
	}
}

func TestSampleAtRowThicknessMatchesTable(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	table, err := b.Bake("skin-sample", baseScatter())
	if err != nil {
		t.Fatalf("bake: %v", err)
	}
	for _, row := range table.Rows {
		got, err := b.Sample("skin-sample", 0, row.Thickness)
		if err != nil {
			t.Fatalf("sample at row %d: %v", row.Index, err)
		}
		if !almostEqual(got.R, row.Diffusion.R) || !almostEqual(got.G, row.Diffusion.G) || !almostEqual(got.B, row.Diffusion.B) {
			t.Fatalf("sample at row %d = %+v, want the baked row %+v", row.Index, got, row.Diffusion)
		}
	}
}

func TestSampleClampsThicknessAndRejectsNegativeRadius(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	table, err := b.Bake("skin-clamp", baseScatter())
	if err != nil {
		t.Fatalf("bake: %v", err)
	}
	first := table.Rows[0]
	got, err := b.Sample("skin-clamp", 0, -5)
	if err != nil {
		t.Fatalf("sample below the band: %v", err)
	}
	if !almostEqual(got.R, first.Diffusion.R) {
		t.Fatalf("sample below the band = %+v, want the first row %+v", got, first.Diffusion)
	}
	last := table.Rows[len(table.Rows)-1]
	got, err = b.Sample("skin-clamp", 0, 99)
	if err != nil {
		t.Fatalf("sample above the band: %v", err)
	}
	if !almostEqual(got.G, last.Diffusion.G) {
		t.Fatalf("sample above the band = %+v, want the last row %+v", got, last.Diffusion)
	}
	if _, err := b.Sample("skin-clamp", -1, 0.5); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("negative radius must fail with ErrBadConfig, got %v", err)
	}
}

func TestSampleUnknownProfileFails(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	if _, err := b.Sample("skin-missing", 0, 0.5); !errors.Is(err, impl.ErrUnknownProfile) {
		t.Fatalf("sampling an unbaked profile must fail with ErrUnknownProfile, got %v", err)
	}
	if _, err := b.Sample("", 0, 0.5); !errors.Is(err, impl.ErrUnknownProfile) {
		t.Fatalf("sampling an empty id must fail with ErrUnknownProfile, got %v", err)
	}
}

func TestBakeHonoursRowBudget(t *testing.T) {
	b := impl.NewSubsurfaceProfile(budgetLimits(3))
	table, err := b.Bake("skin-slow", baseScatter())
	if err != nil {
		t.Fatalf("budgeted bake: %v", err)
	}
	if table.Complete {
		t.Fatalf("3 baked rows out of 8 must not be complete")
	}
	if len(table.Rows) != 3 {
		t.Fatalf("baked rows = %d, want the row budget 3", len(table.Rows))
	}
	if _, ok := b.Profile("skin-slow"); ok {
		t.Fatalf("a partial bake must not be served through Profile")
	}
	progress, ok := b.Pending("skin-slow")
	if !ok {
		t.Fatalf("expected a pending bake for skin-slow")
	}
	if progress.Baked != 3 || progress.Total != 8 {
		t.Fatalf("pending progress = %d/%d, want 3/8", progress.Baked, progress.Total)
	}
	progress, err = b.Advance(4)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if progress.Baked != 7 || progress.Complete {
		t.Fatalf("advance(4) reached %d rows complete=%v, want 7 and false", progress.Baked, progress.Complete)
	}
	progress, err = b.Advance(0)
	if err != nil {
		t.Fatalf("advance with the default budget: %v", err)
	}
	if !progress.Complete || progress.Baked != 8 {
		t.Fatalf("final advance reached %d rows complete=%v, want 8 and true", progress.Baked, progress.Complete)
	}
	committed, ok := b.Profile("skin-slow")
	if !ok || len(committed.Rows) != 8 || !committed.Complete {
		t.Fatalf("committed table = %+v ok=%v, want 8 complete rows", committed, ok)
	}
	if _, err := b.Advance(1); !errors.Is(err, impl.ErrNoPendingBake) {
		t.Fatalf("advance without a pending bake must fail with ErrNoPendingBake, got %v", err)
	}
}

func TestRebakeKeepsOldTableUntilCommit(t *testing.T) {
	b := impl.NewSubsurfaceProfile(budgetLimits(2))
	if _, err := b.Bake("skin-rebake", baseScatter()); err != nil {
		t.Fatalf("first bake: %v", err)
	}
	for {
		progress, err := b.Advance(2)
		if err != nil {
			t.Fatalf("finish first bake: %v", err)
		}
		if progress.Complete {
			break
		}
	}
	old, _ := b.Profile("skin-rebake")

	changed := baseScatter()
	changed.MeanFreePath = impl.Spectrum{R: 2, G: 1.5, B: 1}
	changed.Curve = []impl.ThicknessPoint{{Thickness: 0, Radius: 0}, {Thickness: 2, Radius: 6}}

	same, err := b.ChangedRows("skin-rebake", baseScatter())
	if err != nil {
		t.Fatalf("changed rows for the same config: %v", err)
	}
	if len(same) != 0 {
		t.Fatalf("unchanged config reported rows %v, want none", same)
	}
	rows, err := b.ChangedRows("skin-rebake", changed)
	if err != nil {
		t.Fatalf("changed rows: %v", err)
	}
	if len(rows) != 8 {
		t.Fatalf("changed rows = %v, want all 8 rows", rows)
	}
	for i, row := range rows {
		if row != i {
			t.Fatalf("changed rows = %v, want ascending 0..7", rows)
		}
	}

	partial, err := b.Bake("skin-rebake", changed)
	if err != nil {
		t.Fatalf("second bake: %v", err)
	}
	if partial.Complete || len(partial.Rows) != 2 {
		t.Fatalf("second bake = %d rows complete=%v, want 2 and false", len(partial.Rows), partial.Complete)
	}
	served, ok := b.Profile("skin-rebake")
	if !ok || served.Version != old.Version {
		t.Fatalf("while the new table is unfinished Profile returned version %d ok=%v, want version %d", served.Version, ok, old.Version)
	}
	mid, err := b.Sample("skin-rebake", 0, 0.5)
	if err != nil {
		t.Fatalf("sample during the rebake: %v", err)
	}
	wantMid := interpolateRows(old, 0.5)
	if !almostEqual(mid.R, wantMid.R) || !almostEqual(mid.G, wantMid.G) || !almostEqual(mid.B, wantMid.B) {
		t.Fatalf("sample during the rebake = %+v, want the old table value %+v", mid, wantMid)
	}
	for {
		progress, err := b.Advance(2)
		if err != nil {
			t.Fatalf("finish second bake: %v", err)
		}
		if progress.Complete {
			break
		}
	}
	fresh, ok := b.Profile("skin-rebake")
	if !ok || fresh.Version != old.Version+1 {
		t.Fatalf("committed version = %d, want %d", fresh.Version, old.Version+1)
	}
	if !almostEqual(fresh.MeanFreePath.R, 2) {
		t.Fatalf("committed mean free path = %+v, want the new config", fresh.MeanFreePath)
	}
}

func interpolateRows(table impl.ProfileTable, thickness float64) impl.Spectrum {
	rows := table.Rows
	if thickness <= rows[0].Thickness {
		return rows[0].Diffusion
	}
	last := rows[len(rows)-1]
	if thickness >= last.Thickness {
		return last.Diffusion
	}
	for i := 0; i+1 < len(rows); i++ {
		if thickness < rows[i+1].Thickness {
			span := rows[i+1].Thickness - rows[i].Thickness
			alpha := (thickness - rows[i].Thickness) / span
			return impl.Spectrum{
				R: rows[i].Diffusion.R + (rows[i+1].Diffusion.R-rows[i].Diffusion.R)*alpha,
				G: rows[i].Diffusion.G + (rows[i+1].Diffusion.G-rows[i].Diffusion.G)*alpha,
				B: rows[i].Diffusion.B + (rows[i+1].Diffusion.B-rows[i].Diffusion.B)*alpha,
			}
		}
	}
	return last.Diffusion
}

func TestInvalidateRemovesProfile(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	if _, err := b.Bake("skin-drop", baseScatter()); err != nil {
		t.Fatalf("bake: %v", err)
	}
	if err := b.Invalidate("skin-drop"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, ok := b.Profile("skin-drop"); ok {
		t.Fatalf("invalidated profile must disappear from Profile")
	}
	if _, err := b.Sample("skin-drop", 0, 0.5); !errors.Is(err, impl.ErrUnknownProfile) {
		t.Fatalf("sampling an invalidated profile must fail with ErrUnknownProfile, got %v", err)
	}
	if err := b.Invalidate("skin-drop"); !errors.Is(err, impl.ErrUnknownProfile) {
		t.Fatalf("invalidating twice must fail with ErrUnknownProfile, got %v", err)
	}
	if _, err := b.ChangedRows("skin-drop", baseScatter()); !errors.Is(err, impl.ErrUnknownProfile) {
		t.Fatalf("changed rows on a dropped profile must fail with ErrUnknownProfile, got %v", err)
	}

	if _, err := b.Bake("skin-cancel", baseScatter()); err != nil {
		t.Fatalf("cancel bake start: %v", err)
	}
	if err := b.Invalidate("skin-cancel"); err != nil {
		t.Fatalf("invalidating a pending bake must succeed, got %v", err)
	}
	if _, err := b.Advance(4); !errors.Is(err, impl.ErrNoPendingBake) {
		t.Fatalf("advance after cancelling the only job must fail with ErrNoPendingBake, got %v", err)
	}
}

func TestProfileLimitEnforced(t *testing.T) {
	b := impl.NewSubsurfaceProfile(impl.BakeLimits{MaxProfiles: 2, MaxRows: 32, RowBudget: 16})
	for _, id := range []string{"skin-a", "skin-b"} {
		if _, err := b.Bake(id, baseScatter()); err != nil {
			t.Fatalf("bake %s: %v", id, err)
		}
	}
	if _, err := b.Bake("skin-c", baseScatter()); !errors.Is(err, impl.ErrProfileLimit) {
		t.Fatalf("third profile over a cap of 2 must fail with ErrProfileLimit, got %v", err)
	}
	if got := b.Count(); got != 2 {
		t.Fatalf("committed tables = %d, want 2", got)
	}
}

func TestConcurrentSampleDuringRebake(t *testing.T) {
	b := impl.NewSubsurfaceProfile(budgetLimits(2))
	if _, err := b.Bake("skin-live", baseScatter()); err != nil {
		t.Fatalf("first bake: %v", err)
	}
	for {
		progress, err := b.Advance(2)
		if err != nil {
			t.Fatalf("finish first bake: %v", err)
		}
		if progress.Complete {
			break
		}
	}
	changed := baseScatter()
	changed.Scatter = impl.Spectrum{R: 0.5, G: 0.4, B: 0.3}
	if _, err := b.Bake("skin-live", changed); err != nil {
		t.Fatalf("second bake: %v", err)
	}
	var wg sync.WaitGroup
	failures := make(chan string, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 60; j++ {
				got, err := b.Sample("skin-live", 0, 0.75)
				if err != nil {
					failures <- "sample error: " + err.Error()
					return
				}
				for _, v := range []float64{got.R, got.G, got.B} {
					if v < 0 || v > 1 {
						failures <- "sample channel out of range"
						return
					}
				}
			}
		}()
	}
	for {
		progress, err := b.Advance(2)
		if err != nil {
			wg.Wait()
			t.Fatalf("advance under readers: %v", err)
		}
		if progress.Complete {
			break
		}
	}
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Fatalf("concurrent sample failed: %s", msg)
	}
	served, ok := b.Profile("skin-live")
	if !ok || served.Version != 2 {
		t.Fatalf("after the concurrent rebake version = %d ok=%v, want 2", served.Version, ok)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	limits := budgetLimits(3)
	b := impl.NewSubsurfaceProfile(limits)
	if _, err := b.Bake("skin-snap", baseScatter()); err != nil {
		t.Fatalf("bake: %v", err)
	}
	for {
		progress, err := b.Advance(3)
		if err != nil {
			t.Fatalf("finish bake: %v", err)
		}
		if progress.Complete {
			break
		}
	}
	before, err := b.Sample("skin-snap", 0.25, 0.7)
	if err != nil {
		t.Fatalf("sample before snapshot: %v", err)
	}
	data, err := b.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewSubsurfaceProfile(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Count(); got != 1 {
		t.Fatalf("restored table count = %d, want 1", got)
	}
	version, err := restored.Version("skin-snap")
	if err != nil || version != 1 {
		t.Fatalf("restored version = %d err=%v, want 1", version, err)
	}
	after, err := restored.Sample("skin-snap", 0.25, 0.7)
	if err != nil {
		t.Fatalf("sample after restore: %v", err)
	}
	if !almostEqual(before.R, after.R) || !almostEqual(before.B, after.B) {
		t.Fatalf("restored sample = %+v, want %+v", after, before)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	b := impl.NewSubsurfaceProfile(baseLimits())
	if _, err := b.Bake("skin-safe", baseScatter()); err != nil {
		t.Fatalf("bake: %v", err)
	}
	before, err := b.Sample("skin-safe", 0, 0.5)
	if err != nil {
		t.Fatalf("sample before restore: %v", err)
	}
	if err := b.Restore([]byte("{not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if err := b.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("nil payload must fail with ErrBadSnapshot, got %v", err)
	}
	after, err := b.Sample("skin-safe", 0, 0.5)
	if err != nil || !almostEqual(before.R, after.R) {
		t.Fatalf("failed restore changed the live table: before %+v after %+v err=%v", before, after, err)
	}
}
