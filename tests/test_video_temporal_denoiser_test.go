package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-c-09"
)

func flatFrame(w, h int, value float32) impl.Frame {
	y := make([]float32, w*h)
	for i := range y {
		y[i] = value
	}
	return impl.Frame{Width: w, Height: h, Y: y}
}

func mustDenoise(t *testing.T, d *impl.VideoTemporalDenoiser, f impl.Frame) impl.Frame {
	t.Helper()
	out, err := d.Denoise(f)
	if err != nil {
		t.Fatalf("denoise: %v", err)
	}
	if out.Width != f.Width || out.Height != f.Height {
		t.Fatalf("output frame is %dx%d, want %dx%d", out.Width, out.Height, f.Width, f.Height)
	}
	if len(out.Y) != len(f.Y) {
		t.Fatalf("output frame holds %d samples, want %d", len(out.Y), len(f.Y))
	}
	return out
}

func TestNewDenoiserDefaults(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	stats := d.Stats()
	if stats.Strength != 3 {
		t.Fatalf("default strength = %d, want 3", stats.Strength)
	}
	if stats.BlockSize != impl.BlockMedium {
		t.Fatalf("default block size = %d, want %d", stats.BlockSize, impl.BlockMedium)
	}
	if stats.HistoryDepth != 4 {
		t.Fatalf("default history depth = %d, want 4", stats.HistoryDepth)
	}
	if stats.Frames != 0 || stats.SceneCuts != 0 || stats.Resets != 0 {
		t.Fatalf("fresh counters = %d frames, %d cuts, %d resets; want zero", stats.Frames, stats.SceneCuts, stats.Resets)
	}
}

func TestSetStrengthValidatesRange(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetStrength(impl.MinStrength - 1); !errors.Is(err, impl.ErrStrengthRange) {
		t.Fatalf("strength below the window must fail with ErrStrengthRange, got %v", err)
	}
	if err := d.SetStrength(impl.MaxStrength + 1); !errors.Is(err, impl.ErrStrengthRange) {
		t.Fatalf("strength above the window must fail with ErrStrengthRange, got %v", err)
	}
	if err := d.SetStrength(impl.MaxStrength); err != nil {
		t.Fatalf("the upper bound must be accepted, got %v", err)
	}
	if got := d.Stats().Strength; got != impl.MaxStrength {
		t.Fatalf("strength = %d, want %d", got, impl.MaxStrength)
	}
}

func TestSetBlockSizeValidatesSupportedSizes(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetBlockSize(12); !errors.Is(err, impl.ErrBlockSize) {
		t.Fatalf("unsupported block size must fail with ErrBlockSize, got %v", err)
	}
	if err := d.SetBlockSize(impl.BlockSmall); err != nil {
		t.Fatalf("eight pixel blocks must be accepted, got %v", err)
	}
	if got := d.Stats().BlockSize; got != impl.BlockSmall {
		t.Fatalf("block size = %d, want %d", got, impl.BlockSmall)
	}
}

func TestSetHistoryDepthValidatesRange(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetHistoryDepth(0); !errors.Is(err, impl.ErrHistoryDepth) {
		t.Fatalf("zero depth must fail with ErrHistoryDepth, got %v", err)
	}
	if err := d.SetHistoryDepth(impl.MaxHistoryDepth + 1); !errors.Is(err, impl.ErrHistoryDepth) {
		t.Fatalf("depth above the window must fail with ErrHistoryDepth, got %v", err)
	}
	if err := d.SetHistoryDepth(2); err != nil {
		t.Fatalf("two frames must be accepted, got %v", err)
	}
	if got := d.Stats().HistoryDepth; got != 2 {
		t.Fatalf("history depth = %d, want 2", got)
	}
}

func TestFirstFramePassesThrough(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	out := mustDenoise(t, d, flatFrame(16, 16, 42))
	for i, v := range out.Y {
		if v != 42 {
			t.Fatalf("first frame sample %d = %v, want the untouched input 42", i, v)
		}
	}
	if got := d.Stats().Frames; got != 1 {
		t.Fatalf("frame counter = %d, want 1", got)
	}
}

func TestDenoiseRejectsMalformedFrames(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if _, err := d.Denoise(impl.Frame{Width: 16, Height: 16}); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("missing samples must fail with ErrBadFrame, got %v", err)
	}
	if _, err := d.Denoise(impl.Frame{Width: 0, Height: 16, Y: []float32{}}); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("zero width must fail with ErrBadFrame, got %v", err)
	}
	if _, err := d.Denoise(impl.Frame{Width: 16, Height: 16, Y: make([]float32, 255)}); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("sample count mismatch must fail with ErrBadFrame, got %v", err)
	}
}

func TestDenoiseRejectsOversizeFrames(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	big := impl.Frame{Width: 4096, Height: 2161}
	if big.Width*big.Height <= impl.MaxPixels {
		t.Fatalf("test fixture must exceed the pixel budget")
	}
	if _, err := d.Denoise(big); !errors.Is(err, impl.ErrFrameTooLarge) {
		t.Fatalf("a frame above the pixel budget must fail with ErrFrameTooLarge, got %v", err)
	}
	if got := d.Stats().Frames; got != 0 {
		t.Fatalf("rejected frames must not advance the counter, got %d", got)
	}
}

func TestDenoiseBlendsStaticHistory(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetStrength(5); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	mustDenoise(t, d, flatFrame(16, 16, 0))
	out := mustDenoise(t, d, flatFrame(16, 16, 100))
	for i, v := range out.Y {
		if v < 19.9 || v > 20.1 {
			t.Fatalf("sample %d = %v, want 0.2 of the current frame plus 0.8 of the zero history", i, v)
		}
	}
}

func TestDenoiseAccumulatesAtTheConfiguredDepth(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetStrength(5); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	if err := d.SetHistoryDepth(2); err != nil {
		t.Fatalf("set history depth: %v", err)
	}
	mustDenoise(t, d, flatFrame(16, 16, 0))
	second := mustDenoise(t, d, flatFrame(16, 16, 100))
	if second.Y[0] < 19.9 || second.Y[0] > 20.1 {
		t.Fatalf("second frame sample = %v, want 20", second.Y[0])
	}
	third := mustDenoise(t, d, flatFrame(16, 16, 200))
	if third.Y[0] < 79.9 || third.Y[0] > 80.1 {
		t.Fatalf("third frame sample = %v, want 80 from the two frame history mean 50", third.Y[0])
	}
}

func TestMotionVectorsShiftTheHistorySample(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetStrength(3); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	first := flatFrame(32, 16, 0)
	for y := 0; y < first.Height; y++ {
		for x := 16; x < first.Width; x++ {
			first.Y[y*first.Width+x] = 100
		}
	}
	mustDenoise(t, d, first)
	if err := d.SetMotionVectors(impl.MotionField{
		BlockSize: impl.BlockMedium,
		Cols:      2,
		Rows:      1,
		VX:        []int{16, -16},
		VY:        []int{0, 0},
	}); err != nil {
		t.Fatalf("set motion vectors: %v", err)
	}
	out := mustDenoise(t, d, flatFrame(32, 16, 50))
	left := out.Y[0]
	right := out.Y[16]
	if left < 74.9 || left > 75.1 {
		t.Fatalf("left block sample = %v, want 75 from the shifted bright history", left)
	}
	if right < 24.9 || right > 25.1 {
		t.Fatalf("right block sample = %v, want 25 from the shifted dark history", right)
	}
}

func TestSetMotionVectorsValidatesTheGrid(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetMotionVectors(impl.MotionField{BlockSize: 12, Cols: 1, Rows: 1, VX: []int{0}, VY: []int{0}}); !errors.Is(err, impl.ErrBadMotionField) {
		t.Fatalf("unsupported motion block size must fail with ErrBadMotionField, got %v", err)
	}
	if err := d.SetMotionVectors(impl.MotionField{BlockSize: impl.BlockMedium, Cols: 2, Rows: 1, VX: []int{0}, VY: []int{0, 0}}); !errors.Is(err, impl.ErrBadMotionField) {
		t.Fatalf("vector slice length mismatch must fail with ErrBadMotionField, got %v", err)
	}
	if err := d.SetMotionVectors(impl.MotionField{BlockSize: impl.BlockMedium, Cols: 1, Rows: 1, VX: []int{impl.MaxMotion + 1}, VY: []int{0}}); !errors.Is(err, impl.ErrBadMotionField) {
		t.Fatalf("vector beyond the motion window must fail with ErrBadMotionField, got %v", err)
	}
	if err := d.SetMotionVectors(impl.MotionField{BlockSize: impl.BlockMedium, Cols: 1, Rows: 1, VX: []int{0}, VY: []int{0}}); err != nil {
		t.Fatalf("a valid grid must be accepted, got %v", err)
	}
}

func TestNotifySceneCutDropsTheHistory(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetStrength(5); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	mustDenoise(t, d, flatFrame(16, 16, 0))
	second := mustDenoise(t, d, flatFrame(16, 16, 100))
	if second.Y[0] < 19.9 || second.Y[0] > 20.1 {
		t.Fatalf("second frame sample = %v, want the blended 20 before the cut", second.Y[0])
	}
	if err := d.NotifySceneCut(); err != nil {
		t.Fatalf("notify scene cut: %v", err)
	}
	if got := d.Stats().SceneCuts; got != 1 {
		t.Fatalf("scene cut counter = %d, want 1", got)
	}
	out := mustDenoise(t, d, flatFrame(16, 16, 300))
	for i, v := range out.Y {
		if v != 300 {
			t.Fatalf("post cut sample %d = %v, want the untouched new shot 300", i, v)
		}
	}
}

func TestResetClearsHistoryAndCounters(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	mustDenoise(t, d, flatFrame(16, 16, 10))
	mustDenoise(t, d, flatFrame(16, 16, 20))
	if err := d.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	stats := d.Stats()
	if stats.Frames != 0 {
		t.Fatalf("frame counter after reset = %d, want 0", stats.Frames)
	}
	if stats.Resets != 1 {
		t.Fatalf("reset counter = %d, want 1", stats.Resets)
	}
	out := mustDenoise(t, d, flatFrame(16, 16, 7))
	for i, v := range out.Y {
		if v != 7 {
			t.Fatalf("sample %d after reset = %v, want the untouched input 7", i, v)
		}
	}
}

func TestStatsCountsFramesAndBlocks(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetBlockSize(impl.BlockMedium); err != nil {
		t.Fatalf("set block size: %v", err)
	}
	mustDenoise(t, d, flatFrame(32, 32, 5))
	mustDenoise(t, d, flatFrame(32, 32, 6))
	stats := d.Stats()
	if stats.Frames != 2 {
		t.Fatalf("frame counter = %d, want 2", stats.Frames)
	}
	if stats.Blocks != 4 {
		t.Fatalf("block count = %d, want 4 blocks for a 32x32 frame with 16 pixel blocks", stats.Blocks)
	}
}

func TestSnapshotRestoreKeepsTheHistory(t *testing.T) {
	d := impl.NewVideoTemporalDenoiser()
	if err := d.SetStrength(5); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	if err := d.SetHistoryDepth(3); err != nil {
		t.Fatalf("set history depth: %v", err)
	}
	mustDenoise(t, d, flatFrame(16, 16, 0))
	mustDenoise(t, d, flatFrame(16, 16, 100))
	data, err := d.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewVideoTemporalDenoiser()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Stats().HistoryDepth; got != 3 {
		t.Fatalf("restored history depth = %d, want 3", got)
	}
	if got := restored.Stats().Frames; got != 2 {
		t.Fatalf("restored frame counter = %d, want 2", got)
	}
	next := mustDenoise(t, d, flatFrame(16, 16, 200))
	resumed := mustDenoise(t, restored, flatFrame(16, 16, 200))
	if next.Y[0] != resumed.Y[0] {
		t.Fatalf("sample after restore = %v, want the uninterrupted value %v", resumed.Y[0], next.Y[0])
	}
	if err := restored.Restore([]byte("{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Stats().Frames; got != 3 {
		t.Fatalf("rejected restore must not touch live state, frame counter = %d", got)
	}
}
