package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-c-12"
)

func hdFrame(id int) impl.Frame {
	return impl.Frame{ID: id, Width: 320, Height: 160}
}

func bitsFor(perBlock int) int {
	return 200 * perBlock
}

func TestNewAllocatorDefaults(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	lo, hi := a.QPRange()
	if lo != impl.DefaultQPMin || hi != impl.DefaultQPMax {
		t.Fatalf("default qp range = %d..%d, want %d..%d", lo, hi, impl.DefaultQPMin, impl.DefaultQPMax)
	}
	if got := a.SmoothWindow(); got != impl.DefaultSmooth {
		t.Fatalf("default smoothing window = %d, want %d", got, impl.DefaultSmooth)
	}
	if _, _, ok := a.Roi(); ok {
		t.Fatalf("a fresh allocator must not report a region of interest")
	}
	st := a.Stats()
	if st.Frames != 0 || st.Committed != 0 || st.Clamped != 0 || st.ROIFrames != 0 {
		t.Fatalf("fresh stats = %+v, want all counters zero", st)
	}
	table, err := a.PlanQP(hdFrame(1), bitsFor(300))
	if err != nil {
		t.Fatalf("planning with defaults must succeed, got %v", err)
	}
	if table.Base != 32 {
		t.Fatalf("base qp for 300 bits per block = %d, want 32", table.Base)
	}
	if len(table.QP) != 200 {
		t.Fatalf("block count = %d, want 200", len(table.QP))
	}
}

func TestSetRoiRejectsBadInput(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	if err := a.SetRoi(impl.Rect{X: 0, Y: 0, W: 0, H: 40}, 1); !errors.Is(err, impl.ErrBadRect) {
		t.Fatalf("a zero width roi must fail with ErrBadRect, got %v", err)
	}
	if err := a.SetRoi(impl.Rect{X: 0, Y: 0, W: 40, H: 0}, 1); !errors.Is(err, impl.ErrBadRect) {
		t.Fatalf("a zero height roi must fail with ErrBadRect, got %v", err)
	}
	if err := a.SetRoi(impl.Rect{X: -8, Y: 0, W: 40, H: 40}, 1); !errors.Is(err, impl.ErrBadRect) {
		t.Fatalf("a negative origin must fail with ErrBadRect, got %v", err)
	}
	if err := a.SetRoi(impl.Rect{X: 0, Y: 0, W: 40, H: 40}, 0); !errors.Is(err, impl.ErrBadWeight) {
		t.Fatalf("a zero weight must fail with ErrBadWeight, got %v", err)
	}
	if err := a.SetRoi(impl.Rect{X: 0, Y: 0, W: 40, H: 40}, 4.5); !errors.Is(err, impl.ErrBadWeight) {
		t.Fatalf("a weight above the cap must fail with ErrBadWeight, got %v", err)
	}
	if _, _, ok := a.Roi(); ok {
		t.Fatalf("rejected roi calls must not install a region")
	}
	if err := a.SetRoi(impl.Rect{X: 16, Y: 16, W: 64, H: 32}, 1.5); err != nil {
		t.Fatalf("a valid roi must be accepted, got %v", err)
	}
	rect, weight, ok := a.Roi()
	if !ok || rect != (impl.Rect{X: 16, Y: 16, W: 64, H: 32}) || weight != 1.5 {
		t.Fatalf("stored roi = %+v weight=%v ok=%v, want the accepted values", rect, weight, ok)
	}
}

func TestPlanQPBaseFromTargetBits(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	rich, err := a.PlanQP(hdFrame(1), bitsFor(1024))
	if err != nil {
		t.Fatalf("plan rich frame: %v", err)
	}
	if rich.Base != 20 {
		t.Fatalf("base qp at 1024 bits per block = %d, want 20", rich.Base)
	}
	for i, qp := range rich.QP {
		if qp != 22 {
			t.Fatalf("block %d qp = %d, want the base plus two for a plain block", i, qp)
		}
	}
	poor, err := a.PlanQP(hdFrame(2), bitsFor(300))
	if err != nil {
		t.Fatalf("plan poor frame: %v", err)
	}
	if poor.Base != 32 {
		t.Fatalf("base qp at 300 bits per block = %d, want 32", poor.Base)
	}
	mid, err := a.PlanQP(hdFrame(3), bitsFor(128))
	if err != nil {
		t.Fatalf("plan mid frame: %v", err)
	}
	if mid.Base != 38 {
		t.Fatalf("base qp at 128 bits per block = %d, want 38", mid.Base)
	}
	if mid.BlockCols != 20 || mid.BlockRows != 10 || mid.FrameID != 3 {
		t.Fatalf("table grid = %dx%d frame=%d, want 20x10 frame 3", mid.BlockCols, mid.BlockRows, mid.FrameID)
	}
}

func TestPlanQPClampsToRange(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	if err := a.SetQPRange(30, 34); err != nil {
		t.Fatalf("setting an inner qp range must succeed, got %v", err)
	}
	table, err := a.PlanQP(hdFrame(1), bitsFor(50))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if table.Base != 34 {
		t.Fatalf("base qp after clamping = %d, want the upper bound 34", table.Base)
	}
	for i, qp := range table.QP {
		if qp != 34 {
			t.Fatalf("block %d qp = %d, want it clamped to 34", i, qp)
		}
	}
	if lo, hi := a.QPRange(); lo != 30 || hi != 34 {
		t.Fatalf("qp range = %d..%d, want 30..34", lo, hi)
	}
}

func TestPlanQPRoiLowersQP(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	if err := a.SetRoi(impl.Rect{X: 0, Y: 0, W: 160, H: 160}, 1); err != nil {
		t.Fatalf("set roi: %v", err)
	}
	table, err := a.PlanQP(hdFrame(1), bitsFor(300))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if table.ROIBlocks != 100 {
		t.Fatalf("roi blocks = %d, want the 10 left columns of 10 rows", table.ROIBlocks)
	}
	if table.QP[0] != 28 {
		t.Fatalf("roi block qp = %d, want 32 minus 4", table.QP[0])
	}
	if table.QP[9] != 28 {
		t.Fatalf("last roi block qp = %d, want 28", table.QP[9])
	}
	if table.QP[10] != 34 {
		t.Fatalf("first background block qp = %d, want 32 plus 2", table.QP[10])
	}
	if table.QP[199] != 34 {
		t.Fatalf("last background block qp = %d, want 34", table.QP[199])
	}
}

func TestPlanQPRejectsRoiOutsideFrame(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	if err := a.SetRoi(impl.Rect{X: 300, Y: 0, W: 40, H: 160}, 1); err != nil {
		t.Fatalf("setting a roi may succeed before the frame is known, got %v", err)
	}
	if _, err := a.PlanQP(hdFrame(1), bitsFor(300)); !errors.Is(err, impl.ErrRoiOutOfFrame) {
		t.Fatalf("a roi crossing the right edge must fail with ErrRoiOutOfFrame, got %v", err)
	}
	if err := a.SetRoi(impl.Rect{X: 0, Y: 0, W: 400, H: 400}, 1); err != nil {
		t.Fatalf("set oversized roi: %v", err)
	}
	if _, err := a.PlanQP(hdFrame(1), bitsFor(300)); !errors.Is(err, impl.ErrRoiOutOfFrame) {
		t.Fatalf("a roi larger than the frame must fail with ErrRoiOutOfFrame, got %v", err)
	}
	if st := a.Stats(); st.Frames != 0 {
		t.Fatalf("rejected plans must not be counted, frames=%d", st.Frames)
	}
}

func TestPlanQPSaliencyLowersQP(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	values := make([]uint8, 200)
	for i := range values {
		values[i] = 64
	}
	if err := a.SetSaliencyMap(impl.SaliencyMap{Width: 20, Height: 10, Values: values}); err != nil {
		t.Fatalf("setting a uniform saliency map must succeed, got %v", err)
	}
	table, err := a.PlanQP(hdFrame(1), bitsFor(300))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for i, qp := range table.QP {
		if qp != 28 {
			t.Fatalf("block %d qp = %d, want 32 minus 4 under the uniform saliency map", i, qp)
		}
	}
	if table.ROIBlocks != 0 {
		t.Fatalf("roi blocks = %d, want 0 because only the saliency map is set", table.ROIBlocks)
	}
}

func TestSetSaliencyMapValidation(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	values := make([]uint8, 200)
	for i := range values {
		values[i] = 64
	}
	if err := a.SetSaliencyMap(impl.SaliencyMap{Width: 20, Height: 10, Values: values}); err != nil {
		t.Fatalf("valid map: %v", err)
	}
	if err := a.SetSaliencyMap(impl.SaliencyMap{Width: 20, Height: 10, Values: values[:199]}); !errors.Is(err, impl.ErrBadSaliency) {
		t.Fatalf("a sample count mismatch must fail with ErrBadSaliency, got %v", err)
	}
	table, err := a.PlanQP(hdFrame(1), bitsFor(300))
	if err != nil {
		t.Fatalf("plan after a rejected map: %v", err)
	}
	if table.QP[0] != 28 {
		t.Fatalf("block 0 qp = %d, want the previous map to stay in force", table.QP[0])
	}
	small := impl.NewRoiBitrateAllocator(impl.Limits{MaxSamples: 100})
	if err := small.SetSaliencyMap(impl.SaliencyMap{Width: 20, Height: 10, Values: values}); !errors.Is(err, impl.ErrSampleBudget) {
		t.Fatalf("a map above the sample budget must fail with ErrSampleBudget, got %v", err)
	}
}

func TestPlanQPEnforcesBlockBudget(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{MaxBlocks: 100})
	if _, err := a.PlanQP(hdFrame(1), bitsFor(300)); !errors.Is(err, impl.ErrBlockBudget) {
		t.Fatalf("200 blocks against a budget of 100 must fail with ErrBlockBudget, got %v", err)
	}
	if st := a.Stats(); st.Frames != 0 {
		t.Fatalf("rejected plans must not be counted, frames=%d", st.Frames)
	}
	if _, err := a.PlanQP(impl.Frame{ID: 2, Width: 0, Height: 160}, bitsFor(300)); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("a zero width frame must fail with ErrBadFrame, got %v", err)
	}
	if _, err := a.PlanQP(hdFrame(3), 0); !errors.Is(err, impl.ErrBadBits) {
		t.Fatalf("a zero bit target must fail with ErrBadBits, got %v", err)
	}
}

func TestSmoothingClampsDelta(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	first, err := a.PlanQP(hdFrame(1), bitsFor(300))
	if err != nil {
		t.Fatalf("plan first frame: %v", err)
	}
	if first.QP[0] != 34 {
		t.Fatalf("first frame qp = %d, want 34", first.QP[0])
	}
	if err := a.Commit(first); err != nil {
		t.Fatalf("commit first frame: %v", err)
	}
	second, err := a.PlanQP(hdFrame(2), bitsFor(1024))
	if err != nil {
		t.Fatalf("plan second frame: %v", err)
	}
	if second.Base != 20 {
		t.Fatalf("second base qp = %d, want 20 from the rich target", second.Base)
	}
	if second.QP[0] != 32 {
		t.Fatalf("second frame qp = %d, want the step limited 32 instead of 22", second.QP[0])
	}
	st := a.Stats()
	if st.Clamped == 0 {
		t.Fatalf("stats must count the step limited blocks, got %+v", st)
	}
	if st.Committed != 1 || st.LastFrameID != 1 {
		t.Fatalf("stats committed=%d last=%d, want 1 and 1", st.Committed, st.LastFrameID)
	}
}

func TestSetQPRangeAndWindowValidation(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	if err := a.SetQPRange(40, 20); !errors.Is(err, impl.ErrBadQPRange) {
		t.Fatalf("an inverted range must fail with ErrBadQPRange, got %v", err)
	}
	if err := a.SetQPRange(10, 10); !errors.Is(err, impl.ErrBadQPRange) {
		t.Fatalf("a zero width range must fail with ErrBadQPRange, got %v", err)
	}
	if err := a.SetQPRange(-1, 30); !errors.Is(err, impl.ErrBadQPRange) {
		t.Fatalf("a negative lower bound must fail with ErrBadQPRange, got %v", err)
	}
	if err := a.SetQPRange(10, 52); !errors.Is(err, impl.ErrBadQPRange) {
		t.Fatalf("an upper bound above 51 must fail with ErrBadQPRange, got %v", err)
	}
	if lo, hi := a.QPRange(); lo != 10 || hi != 45 {
		t.Fatalf("rejected ranges must not replace the live one, got %d..%d", lo, hi)
	}
	if err := a.SetSmoothWindow(-1); !errors.Is(err, impl.ErrBadWindow) {
		t.Fatalf("a negative window must fail with ErrBadWindow, got %v", err)
	}
	if got := a.SmoothWindow(); got != 4 {
		t.Fatalf("rejected window changed the live value to %d", got)
	}
	if err := a.SetSmoothWindow(8); err != nil {
		t.Fatalf("a valid window must be accepted, got %v", err)
	}
	if got := a.SmoothWindow(); got != 8 {
		t.Fatalf("window = %d, want 8", got)
	}
}

func TestCommitRequiresPlan(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	bogus := impl.QPTable{FrameID: 9, BlockCols: 20, BlockRows: 10, Base: 32, QP: make([]int, 200)}
	if err := a.Commit(bogus); !errors.Is(err, impl.ErrUnknownPlan) {
		t.Fatalf("committing an unplanned frame must fail with ErrUnknownPlan, got %v", err)
	}
	table, err := a.PlanQP(hdFrame(7), bitsFor(300))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := a.Commit(table); err != nil {
		t.Fatalf("committing a planned table must succeed, got %v", err)
	}
	if st := a.Stats(); st.Committed != 1 || st.LastFrameID != 7 {
		t.Fatalf("stats committed=%d last=%d, want 1 and 7", st.Committed, st.LastFrameID)
	}
	if err := a.Commit(table); !errors.Is(err, impl.ErrStaleFrame) {
		t.Fatalf("committing the same frame twice must fail with ErrStaleFrame, got %v", err)
	}
	mismatched, err := a.PlanQP(hdFrame(8), bitsFor(300))
	if err != nil {
		t.Fatalf("plan 8: %v", err)
	}
	mismatched.BlockRows = 9
	if err := a.Commit(mismatched); !errors.Is(err, impl.ErrBadTable) {
		t.Fatalf("a table whose grid differs from the plan must fail with ErrBadTable, got %v", err)
	}
}

func TestCommitStatsCountROIFrames(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	for id := 1; id <= 2; id++ {
		table, err := a.PlanQP(hdFrame(id), bitsFor(300))
		if err != nil {
			t.Fatalf("plan %d: %v", id, err)
		}
		if id == 1 {
			if err := a.Commit(table); err != nil {
				t.Fatalf("commit: %v", err)
			}
		}
	}
	if st := a.Stats(); st.Frames != 2 || st.Committed != 1 || st.ROIFrames != 0 {
		t.Fatalf("stats frames=%d committed=%d roi=%d, want 2/1/0", st.Frames, st.Committed, st.ROIFrames)
	}
	if err := a.SetRoi(impl.Rect{X: 0, Y: 0, W: 64, H: 64}, 2); err != nil {
		t.Fatalf("set roi: %v", err)
	}
	table, err := a.PlanQP(hdFrame(3), bitsFor(300))
	if err != nil {
		t.Fatalf("plan 3: %v", err)
	}
	if table.QP[0] != 24 {
		t.Fatalf("block 0 qp = %d, want 32 minus 8 for weight two", table.QP[0])
	}
	if table.ROIBlocks != 16 {
		t.Fatalf("roi blocks = %d, want the 4x4 blocks of a 64x64 roi", table.ROIBlocks)
	}
	if st := a.Stats(); st.ROIFrames != 1 {
		t.Fatalf("roi frame count = %d, want 1", st.ROIFrames)
	}
}

func TestSnapshotRestoreKeepsHistory(t *testing.T) {
	limits := impl.Limits{MaxBlocks: 4096}
	a := impl.NewRoiBitrateAllocator(limits)
	first, err := a.PlanQP(hdFrame(1), bitsFor(300))
	if err != nil {
		t.Fatalf("plan first: %v", err)
	}
	if err := a.Commit(first); err != nil {
		t.Fatalf("commit first: %v", err)
	}
	if err := a.SetQPRange(20, 40); err != nil {
		t.Fatalf("set range: %v", err)
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	want, err := a.PlanQP(hdFrame(2), bitsFor(1024))
	if err != nil {
		t.Fatalf("plan on the source allocator: %v", err)
	}
	r := impl.NewRoiBitrateAllocator(limits)
	if err := r.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if lo, hi := r.QPRange(); lo != 20 || hi != 40 {
		t.Fatalf("restored range = %d..%d, want 20..40", lo, hi)
	}
	if got := r.SmoothWindow(); got != 4 {
		t.Fatalf("restored window = %d, want 4", got)
	}
	if st := r.Stats(); st.Committed != 1 || st.LastFrameID != 1 {
		t.Fatalf("restored stats committed=%d last=%d, want 1 and 1", st.Committed, st.LastFrameID)
	}
	got, err := r.PlanQP(hdFrame(2), bitsFor(1024))
	if err != nil {
		t.Fatalf("plan after restore: %v", err)
	}
	if got.QP[0] != want.QP[0] {
		t.Fatalf("qp after restore = %d, want the same step limited %d", got.QP[0], want.QP[0])
	}
	if err := r.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if st := r.Stats(); st.Committed != 1 {
		t.Fatalf("a rejected restore must not touch the live state, committed=%d", st.Committed)
	}
}

func TestConcurrentPlanQP(t *testing.T) {
	a := impl.NewRoiBitrateAllocator(impl.Limits{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	blocks := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			table, err := a.PlanQP(hdFrame(i+1), bitsFor(300))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				return
			}
			blocks += len(table.QP)
		}(i)
	}
	wg.Wait()
	if failed != 0 {
		t.Fatalf("%d concurrent plans failed, want 0", failed)
	}
	if blocks != 1600 {
		t.Fatalf("total planned blocks = %d, want 8 frames of 200 blocks", blocks)
	}
	if st := a.Stats(); st.Frames != 8 {
		t.Fatalf("stats frames = %d, want 8", st.Frames)
	}
}
