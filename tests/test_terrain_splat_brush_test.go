package task

import (
	"errors"
	"math"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-b-16"
)

func testLimits() impl.BrushLimits {
	return impl.BrushLimits{
		BlockSize:               8,
		MaxDirtyBlocksPerStroke: 4,
		MaxDabsPerStroke:        8,
		MaxSnapshotBytes:        1 << 20,
	}
}

func newBrush() *impl.TerrainSplatBrush {
	return impl.NewTerrainSplatBrush(testLimits())
}

func openStroke(t *testing.T, b *impl.TerrainSplatBrush, x, y, layer int) impl.StrokeID {
	t.Helper()
	id, err := b.BeginStroke(impl.StrokeStart{
		Terrain: 0,
		X:       x,
		Y:       y,
		Layer:   layer,
		Radius:  2,
		Spacing: 1,
	})
	if err != nil {
		t.Fatalf("BeginStroke(%d,%d,%d) must succeed, got %v", x, y, layer, err)
	}
	return id
}

func TestBeginStrokeAssignsIncreasingIDs(t *testing.T) {
	b := newBrush()
	first := openStroke(t, b, 1, 1, impl.LayerGrass)
	second := openStroke(t, b, 4, 4, impl.LayerSand)
	third := openStroke(t, b, 7, 7, impl.LayerRock)

	if !(first < second && second < third) {
		t.Fatalf("stroke ids must increase, got %d %d %d", first, second, third)
	}
	if first != impl.StrokeID(1) {
		t.Fatalf("first stroke id = %d, want 1", first)
	}
	active := b.ActiveStrokes()
	if len(active) != 3 {
		t.Fatalf("active strokes = %v, want three entries", active)
	}
	for i := 1; i < len(active); i++ {
		if active[i] <= active[i-1] {
			t.Fatalf("active strokes must be ascending, got %v", active)
		}
	}
	if st := b.Stats(); st.Active != 3 {
		t.Fatalf("stats active = %d, want 3", st.Active)
	}
}

func TestBeginStrokeValidation(t *testing.T) {
	b := newBrush()
	cases := []struct {
		name string
		st   impl.StrokeStart
		want error
	}{
		{"layer too high", impl.StrokeStart{Terrain: 0, Layer: impl.LayerCount, Radius: 2, Spacing: 1}, impl.ErrBadLayer},
		{"layer negative", impl.StrokeStart{Terrain: 0, Layer: -1, Radius: 2, Spacing: 1}, impl.ErrBadLayer},
		{"radius zero", impl.StrokeStart{Terrain: 0, Layer: 0, Radius: 0, Spacing: 1}, impl.ErrBadDab},
		{"spacing zero", impl.StrokeStart{Terrain: 0, Layer: 0, Radius: 2, Spacing: 0}, impl.ErrBadDab},
		{"negative coordinate", impl.StrokeStart{Terrain: 0, X: -3, Layer: 0, Radius: 2, Spacing: 1}, impl.ErrBadDab},
	}
	for _, tc := range cases {
		if _, err := b.BeginStroke(tc.st); !errors.Is(err, tc.want) {
			t.Fatalf("%s: BeginStroke error = %v, want %v", tc.name, err, tc.want)
		}
	}
	if st := b.Stats(); st.Active != 0 {
		t.Fatalf("rejected strokes must not become active, active = %d", st.Active)
	}
}

func TestAddDabRejectsUnknownAndClosedStroke(t *testing.T) {
	b := newBrush()
	if err := b.AddDab(impl.StrokeID(99), impl.Dab{Seq: 1, X: 0, Y: 0, Layer: 0, Weight: 1, Radius: 1}); !errors.Is(err, impl.ErrUnknownStroke) {
		t.Fatalf("AddDab on unknown stroke = %v, want ErrUnknownStroke", err)
	}
	id := openStroke(t, b, 2, 2, impl.LayerRock)
	if err := b.AddDab(id, impl.Dab{Seq: 1, X: 2, Y: 2, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("AddDab on open stroke must succeed, got %v", err)
	}
	if _, err := b.EndStroke(id); err != nil {
		t.Fatalf("EndStroke must succeed, got %v", err)
	}
	if err := b.AddDab(id, impl.Dab{Seq: 2, X: 3, Y: 2, Layer: impl.LayerRock, Weight: 1, Radius: 1}); !errors.Is(err, impl.ErrStrokeClosed) {
		t.Fatalf("AddDab after EndStroke = %v, want ErrStrokeClosed", err)
	}
}

func TestEndStrokeNormalisesWeightsToFixedPoint(t *testing.T) {
	b := newBrush()
	id := openStroke(t, b, 3, 3, impl.LayerRock)
	if err := b.AddDab(id, impl.Dab{Seq: 1, X: 3, Y: 3, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("first dab must be accepted, got %v", err)
	}
	if err := b.AddDab(id, impl.Dab{Seq: 2, X: 3, Y: 3, Layer: impl.LayerGrass, Weight: 3, Radius: 1}); err != nil {
		t.Fatalf("second dab must be accepted, got %v", err)
	}
	res, err := b.EndStroke(id)
	if err != nil {
		t.Fatalf("EndStroke must succeed, got %v", err)
	}
	if res.Dabs != 2 {
		t.Fatalf("committed dab count = %d, want 2", res.Dabs)
	}
	if len(res.Blocks) != 1 || res.Blocks[0] != (impl.BlockCoord{BX: 0, BY: 0}) {
		t.Fatalf("dirty blocks = %+v, want exactly block 0,0", res.Blocks)
	}
	rock, err := b.SampleWeight(3, 3, impl.LayerRock)
	if err != nil {
		t.Fatalf("SampleWeight must succeed, got %v", err)
	}
	grass, err := b.SampleWeight(3, 3, impl.LayerGrass)
	if err != nil {
		t.Fatalf("SampleWeight must succeed, got %v", err)
	}
	if math.Abs(float64(rock)-0.25) > 1e-4 {
		t.Fatalf("rock weight = %v, want 0.25", rock)
	}
	if math.Abs(float64(grass)-0.75) > 1e-4 {
		t.Fatalf("grass weight = %v, want 0.75", grass)
	}
	if q := float64(rock) * float64(impl.WeightScale); math.Abs(q-math.Round(q)) > 0.05 {
		t.Fatalf("rock weight %v is not a fixed point multiple: scaled = %v", rock, q)
	}
	sand, err := b.SampleWeight(3, 3, impl.LayerSand)
	if err != nil {
		t.Fatalf("SampleWeight must succeed, got %v", err)
	}
	snow, err := b.SampleWeight(3, 3, impl.LayerSnow)
	if err != nil {
		t.Fatalf("SampleWeight must succeed, got %v", err)
	}
	if sand != 0 || snow != 0 {
		t.Fatalf("untouched layers must stay at zero, got sand=%v snow=%v", sand, snow)
	}
	total := rock + grass + sand + snow
	if math.Abs(float64(total)-1) > 1e-6 {
		t.Fatalf("layer weights must add up to one, got %v", total)
	}
	if untouched, err := b.SampleWeight(9, 9, impl.LayerRock); err != nil || untouched != 0 {
		t.Fatalf("untouched pixel weight = %v (err %v), want 0 and no error", untouched, err)
	}
}

func TestSampleWeightRejectsBadLayer(t *testing.T) {
	b := newBrush()
	if _, err := b.SampleWeight(0, 0, impl.LayerCount); !errors.Is(err, impl.ErrBadLayer) {
		t.Fatalf("SampleWeight layer %d = %v, want ErrBadLayer", impl.LayerCount, err)
	}
	if _, err := b.SampleWeight(0, 0, -1); !errors.Is(err, impl.ErrBadLayer) {
		t.Fatalf("SampleWeight layer -1 = %v, want ErrBadLayer", err)
	}
	if _, err := b.SampleWeight(0, 0, impl.LayerSnow); err != nil {
		t.Fatalf("SampleWeight on an empty brush must succeed with zero, got %v", err)
	}
}

func TestDirtyBlocksSortedAndUnique(t *testing.T) {
	b := newBrush()
	id := openStroke(t, b, 0, 0, impl.LayerRock)
	points := []struct{ x, y int }{
		{20, 4},  // block 2,0
		{0, 0},   // block 0,0
		{4, 20},  // block 0,2
		{25, 25}, // block 3,3
		{1, 1},   // block 0,0 again
	}
	for i, p := range points {
		if err := b.AddDab(id, impl.Dab{Seq: i + 1, X: p.x, Y: p.y, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
			t.Fatalf("dab %d at %d,%d must be accepted, got %v", i+1, p.x, p.y, err)
		}
	}
	res, err := b.EndStroke(id)
	if err != nil {
		t.Fatalf("EndStroke must succeed, got %v", err)
	}
	want := []impl.BlockCoord{{BX: 0, BY: 0}, {BX: 0, BY: 2}, {BX: 2, BY: 0}, {BX: 3, BY: 3}}
	if len(res.Blocks) != len(want) {
		t.Fatalf("dirty blocks = %+v, want %+v", res.Blocks, want)
	}
	for i := range want {
		if res.Blocks[i] != want[i] {
			t.Fatalf("dirty blocks = %+v, want %+v", res.Blocks, want)
		}
	}
	if got := b.DirtyBlocks(); len(got) != len(want) {
		t.Fatalf("DirtyBlocks = %+v, want %+v", got, want)
	}
	if st := b.Stats(); st.DirtyBlocks != len(want) {
		t.Fatalf("stats dirty blocks = %d, want %d", st.DirtyBlocks, len(want))
	}
}

func TestUndoRestoresPreviousWeights(t *testing.T) {
	b := newBrush()
	first := openStroke(t, b, 5, 5, impl.LayerRock)
	if err := b.AddDab(first, impl.Dab{Seq: 1, X: 5, Y: 5, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("first dab must be accepted, got %v", err)
	}
	if _, err := b.EndStroke(first); err != nil {
		t.Fatalf("first EndStroke must succeed, got %v", err)
	}
	second := openStroke(t, b, 5, 5, impl.LayerGrass)
	if err := b.AddDab(second, impl.Dab{Seq: 1, X: 5, Y: 5, Layer: impl.LayerGrass, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("second dab must be accepted, got %v", err)
	}
	if _, err := b.EndStroke(second); err != nil {
		t.Fatalf("second EndStroke must succeed, got %v", err)
	}
	rock, err := b.SampleWeight(5, 5, impl.LayerRock)
	if err != nil {
		t.Fatalf("SampleWeight must succeed, got %v", err)
	}
	if math.Abs(float64(rock)-0.5) > 1e-4 {
		t.Fatalf("rock weight after two strokes = %v, want 0.5", rock)
	}
	if err := b.Undo(second); err != nil {
		t.Fatalf("Undo must succeed, got %v", err)
	}
	rock, err = b.SampleWeight(5, 5, impl.LayerRock)
	if err != nil {
		t.Fatalf("SampleWeight after undo must succeed, got %v", err)
	}
	grass, err := b.SampleWeight(5, 5, impl.LayerGrass)
	if err != nil {
		t.Fatalf("SampleWeight after undo must succeed, got %v", err)
	}
	if math.Abs(float64(rock)-1) > 1e-4 {
		t.Fatalf("rock weight after undo = %v, want 1", rock)
	}
	if grass != 0 {
		t.Fatalf("grass weight after undo = %v, want 0", grass)
	}
	if st := b.Stats(); st.Undone != 1 {
		t.Fatalf("stats undone = %d, want 1", st.Undone)
	}
	if err := b.Undo(second); !errors.Is(err, impl.ErrUnknownStroke) {
		t.Fatalf("second Undo = %v, want ErrUnknownStroke", err)
	}
}

func TestCancelActiveStrokeDiscardsDabs(t *testing.T) {
	b := newBrush()
	id := openStroke(t, b, 6, 6, impl.LayerSand)
	if err := b.AddDab(id, impl.Dab{Seq: 1, X: 6, Y: 6, Layer: impl.LayerSand, Weight: 2, Radius: 1}); err != nil {
		t.Fatalf("dab must be accepted, got %v", err)
	}
	if err := b.CancelStroke(id); err != nil {
		t.Fatalf("CancelStroke on an open stroke must succeed, got %v", err)
	}
	if w, err := b.SampleWeight(6, 6, impl.LayerSand); err != nil || w != 0 {
		t.Fatalf("cancelled stroke left weight %v (err %v), want 0", w, err)
	}
	if blocks := b.DirtyBlocks(); len(blocks) != 0 {
		t.Fatalf("cancelled stroke left dirty blocks %+v", blocks)
	}
	st := b.Stats()
	if st.Cancelled != 1 || st.Active != 0 || st.Committed != 0 {
		t.Fatalf("stats after cancel = %+v, want cancelled 1 and nothing active", st)
	}
	if err := b.CancelStroke(id); !errors.Is(err, impl.ErrUnknownStroke) {
		t.Fatalf("second CancelStroke = %v, want ErrUnknownStroke", err)
	}
}

func TestDirtyBlockLimitRejectsWholeStroke(t *testing.T) {
	b := impl.NewTerrainSplatBrush(impl.BrushLimits{
		BlockSize:               8,
		MaxDirtyBlocksPerStroke: 1,
		MaxDabsPerStroke:        8,
		MaxSnapshotBytes:        1 << 20,
	})
	id := openStroke(t, b, 0, 0, impl.LayerRock)
	if err := b.AddDab(id, impl.Dab{Seq: 1, X: 0, Y: 0, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("first dab must be accepted, got %v", err)
	}
	if err := b.AddDab(id, impl.Dab{Seq: 2, X: 100, Y: 100, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("second dab must be accepted, got %v", err)
	}
	if _, err := b.EndStroke(id); !errors.Is(err, impl.ErrDirtyLimit) {
		t.Fatalf("EndStroke over the dirty block cap = %v, want ErrDirtyLimit", err)
	}
	if w, err := b.SampleWeight(0, 0, impl.LayerRock); err != nil || w != 0 {
		t.Fatalf("rejected stroke wrote weight %v (err %v), want 0", w, err)
	}
	if blocks := b.DirtyBlocks(); len(blocks) != 0 {
		t.Fatalf("rejected stroke left dirty blocks %+v", blocks)
	}
	if st := b.Stats(); st.Committed != 0 {
		t.Fatalf("rejected stroke counted as committed, stats = %+v", st)
	}
}

func TestSnapshotByteLimitRejectsWholeStroke(t *testing.T) {
	const blockBytes = 8 * 8 * 4 * 4 * 2
	b := impl.NewTerrainSplatBrush(impl.BrushLimits{
		BlockSize:               8,
		MaxDirtyBlocksPerStroke: 4,
		MaxDabsPerStroke:        8,
		MaxSnapshotBytes:        blockBytes,
	})
	single := openStroke(t, b, 1, 1, impl.LayerRock)
	if err := b.AddDab(single, impl.Dab{Seq: 1, X: 1, Y: 1, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("dab must be accepted, got %v", err)
	}
	res, err := b.EndStroke(single)
	if err != nil {
		t.Fatalf("one block stroke must fit the budget, got %v", err)
	}
	if res.Bytes != blockBytes {
		t.Fatalf("stroke bytes = %d, want %d", res.Bytes, blockBytes)
	}
	double := openStroke(t, b, 1, 1, impl.LayerRock)
	if err := b.AddDab(double, impl.Dab{Seq: 1, X: 1, Y: 1, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("dab must be accepted, got %v", err)
	}
	if err := b.AddDab(double, impl.Dab{Seq: 2, X: 100, Y: 1, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("dab must be accepted, got %v", err)
	}
	if _, err := b.EndStroke(double); !errors.Is(err, impl.ErrSnapshotLimit) {
		t.Fatalf("two block stroke = %v, want ErrSnapshotLimit", err)
	}
	if st := b.Stats(); st.Committed != 1 {
		t.Fatalf("rejected stroke must not be committed, stats = %+v", st)
	}
}

func TestDabLimitEnforced(t *testing.T) {
	b := impl.NewTerrainSplatBrush(impl.BrushLimits{
		BlockSize:               8,
		MaxDirtyBlocksPerStroke: 4,
		MaxDabsPerStroke:        2,
		MaxSnapshotBytes:        1 << 20,
	})
	id := openStroke(t, b, 2, 2, impl.LayerGrass)
	for seq := 1; seq <= 2; seq++ {
		if err := b.AddDab(id, impl.Dab{Seq: seq, X: 2, Y: 2, Layer: impl.LayerGrass, Weight: 1, Radius: 1}); err != nil {
			t.Fatalf("dab %d must be accepted, got %v", seq, err)
		}
	}
	if err := b.AddDab(id, impl.Dab{Seq: 3, X: 2, Y: 2, Layer: impl.LayerGrass, Weight: 1, Radius: 1}); !errors.Is(err, impl.ErrDabLimit) {
		t.Fatalf("third dab = %v, want ErrDabLimit", err)
	}
	if res, err := b.EndStroke(id); err != nil || res.Dabs != 2 {
		t.Fatalf("EndStroke after the dab cap = (%+v, %v), want two dabs and no error", res, err)
	}
}

func TestFlushDerivedRecordsDirtyBlocks(t *testing.T) {
	b := newBrush()
	id := openStroke(t, b, 3, 3, impl.LayerRock)
	if err := b.AddDab(id, impl.Dab{Seq: 1, X: 3, Y: 3, Layer: impl.LayerRock, Weight: 1, Radius: 1}); err != nil {
		t.Fatalf("dab must be accepted, got %v", err)
	}
	if _, err := b.EndStroke(id); err != nil {
		t.Fatalf("EndStroke must succeed, got %v", err)
	}
	block := impl.BlockCoord{BX: 0, BY: 0}
	if err := b.FlushDerived([]impl.BlockCoord{block}); err != nil {
		t.Fatalf("FlushDerived on a dirty block must succeed, got %v", err)
	}
	if st := b.Stats(); st.DerivedBlocks != 1 {
		t.Fatalf("derived blocks = %d, want 1", st.DerivedBlocks)
	}
	if err := b.FlushDerived([]impl.BlockCoord{block}); err != nil {
		t.Fatalf("repeated FlushDerived must succeed, got %v", err)
	}
	if st := b.Stats(); st.DerivedBlocks != 1 {
		t.Fatalf("repeated flush counted twice, derived blocks = %d", st.DerivedBlocks)
	}
	if err := b.FlushDerived([]impl.BlockCoord{{BX: 9, BY: 9}}); !errors.Is(err, impl.ErrUnknownBlock) {
		t.Fatalf("FlushDerived on a clean block = %v, want ErrUnknownBlock", err)
	}
	if st := b.Stats(); st.DerivedBlocks != 1 {
		t.Fatalf("failed flush must not record anything, derived blocks = %d", st.DerivedBlocks)
	}
}

func TestSnapshotRestorePreservesWeights(t *testing.T) {
	b := newBrush()
	id := openStroke(t, b, 7, 7, impl.LayerSnow)
	if err := b.AddDab(id, impl.Dab{Seq: 1, X: 7, Y: 7, Layer: impl.LayerSnow, Weight: 2, Radius: 1}); err != nil {
		t.Fatalf("dab must be accepted, got %v", err)
	}
	if err := b.AddDab(id, impl.Dab{Seq: 2, X: 9, Y: 7, Layer: impl.LayerSand, Weight: 2, Radius: 1}); err != nil {
		t.Fatalf("dab must be accepted, got %v", err)
	}
	if _, err := b.EndStroke(id); err != nil {
		t.Fatalf("EndStroke must succeed, got %v", err)
	}
	before, err := b.SampleWeight(7, 7, impl.LayerSnow)
	if err != nil {
		t.Fatalf("SampleWeight must succeed, got %v", err)
	}
	if math.Abs(float64(before)-0.5) > 1e-4 {
		t.Fatalf("snow weight before snapshot = %v, want 0.5", before)
	}
	data, err := b.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot must succeed, got %v", err)
	}
	other := newBrush()
	if err := other.Restore(data); err != nil {
		t.Fatalf("Restore must succeed, got %v", err)
	}
	after, err := other.SampleWeight(7, 7, impl.LayerSnow)
	if err != nil {
		t.Fatalf("SampleWeight after restore must succeed, got %v", err)
	}
	if after != before {
		t.Fatalf("snow weight after restore = %v, want %v", after, before)
	}
	if got, want := len(other.DirtyBlocks()), len(b.DirtyBlocks()); got != want {
		t.Fatalf("dirty blocks after restore = %d, want %d", got, want)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	b := newBrush()
	if err := b.Restore([]byte("this is not a brush snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("Restore of a corrupt payload = %v, want ErrBadSnapshot", err)
	}
	id, err := b.BeginStroke(impl.StrokeStart{Terrain: 0, X: 1, Y: 1, Layer: impl.LayerRock, Radius: 2, Spacing: 1})
	if err != nil {
		t.Fatalf("brush must stay usable after a failed Restore, got %v", err)
	}
	if id != impl.StrokeID(1) {
		t.Fatalf("brush state changed by a failed Restore, first stroke id = %d", id)
	}
}

func TestConcurrentStrokesCommitIndependently(t *testing.T) {
	b := newBrush()
	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			layer := i % impl.LayerCount
			id, err := b.BeginStroke(impl.StrokeStart{Terrain: i, X: i * 16, Y: 0, Layer: layer, Radius: 2, Spacing: 1})
			if err != nil {
				errs[i] = err
				return
			}
			if err := b.AddDab(id, impl.Dab{Seq: 1, X: i * 16, Y: 0, Layer: layer, Weight: 1, Radius: 1}); err != nil {
				errs[i] = err
				return
			}
			_, errs[i] = b.EndStroke(id)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d failed: %v", i, err)
		}
	}
	for i := 0; i < workers; i++ {
		w, err := b.SampleWeight(i*16, 0, i%impl.LayerCount)
		if err != nil {
			t.Fatalf("SampleWeight(%d) must succeed, got %v", i*16, err)
		}
		if math.Abs(float64(w)-1) > 1e-4 {
			t.Fatalf("weight at x=%d = %v, want 1", i*16, w)
		}
	}
	if st := b.Stats(); st.Committed != workers {
		t.Fatalf("committed strokes = %d, want %d", st.Committed, workers)
	}
}
