package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch51-go-game-c-02"
)

func solidFrame(w, h int, sub impl.Subsampling, uVal, vVal float64) impl.YUVFrame {
	cw := (w + 1) / 2
	ch := (h + 1) / 2
	if sub == impl.Sub422 {
		ch = h
	}
	f := impl.YUVFrame{W: w, H: h, ColorSpace: "yuv"}
	f.Y = make([]float64, w*h)
	for i := range f.Y {
		f.Y[i] = float64(i)
	}
	f.U = make([]float64, cw*ch)
	f.V = make([]float64, cw*ch)
	for i := range f.U {
		f.U[i] = uVal
		f.V[i] = vVal
	}
	return f
}

func TestNewUpsamplerDefaults(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	if u.Subsampling() != impl.Sub420 {
		t.Fatalf("default subsampling = %q, want %q", u.Subsampling(), impl.Sub420)
	}
	if u.Phase() != impl.PhaseCentered {
		t.Fatalf("default phase = %q, want %q", u.Phase(), impl.PhaseCentered)
	}
	if u.Edge() != impl.EdgeDirectional {
		t.Fatalf("default edge mode = %q, want %q", u.Edge(), impl.EdgeDirectional)
	}
	if u.BlockCount() != 0 {
		t.Fatalf("block count on a fresh upsampler = %d, want 0", u.BlockCount())
	}
}

func TestUpsample420ConstantPlanes(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	frame := solidFrame(4, 4, impl.Sub420, 1, 2)
	out, err := u.Upsample(frame)
	if err != nil {
		t.Fatalf("upsampling a 4:2:0 frame must succeed, got %v", err)
	}
	if out.W != 4 || out.H != 4 {
		t.Fatalf("output size = %dx%d, want 4x4", out.W, out.H)
	}
	if len(out.Y) != 16 || len(out.U) != 16 || len(out.V) != 16 {
		t.Fatalf("plane lengths = %d/%d/%d, want 16 each", len(out.Y), len(out.U), len(out.V))
	}
	for i := range out.Y {
		if out.Y[i] != frame.Y[i] {
			t.Fatalf("luma sample %d = %v, want %v", i, out.Y[i], frame.Y[i])
		}
		if out.U[i] != 1 || out.V[i] != 2 {
			t.Fatalf("sample %d chroma = %v/%v, want 1/2", i, out.U[i], out.V[i])
		}
	}
}

func TestUpsample422KeepsFullHeightChroma(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{Subsampling: impl.Sub422})
	frame := solidFrame(4, 4, impl.Sub422, 0, 0)
	for i := range frame.U {
		frame.U[i] = float64(i * 10)
	}
	out, err := u.Upsample(frame)
	if err != nil {
		t.Fatalf("upsampling a 4:2:2 frame must succeed, got %v", err)
	}
	want := [][]float64{
		{0, 2.5, 7.5, 10},
		{20, 22.5, 27.5, 30},
		{40, 42.5, 47.5, 50},
		{60, 62.5, 67.5, 70},
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			got := out.U[y*4+x]
			if math.Abs(got-want[y][x]) > 1e-9 {
				t.Fatalf("U(%d,%d) = %v, want %v", x, y, got, want[y][x])
			}
		}
	}
	for i := range out.V {
		if out.V[i] != 0 {
			t.Fatalf("V sample %d = %v, want 0", i, out.V[i])
		}
	}
}

func TestUpsampleCenteredBilinearRamp(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	frame := impl.YUVFrame{W: 4, H: 2, ColorSpace: "yuv"}
	frame.Y = make([]float64, 8)
	frame.U = []float64{0, 10}
	frame.V = []float64{0, 0}
	out, err := u.Upsample(frame)
	if err != nil {
		t.Fatalf("upsampling a horizontal chroma ramp must succeed, got %v", err)
	}
	want := []float64{0, 2.5, 7.5, 10}
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			got := out.U[y*4+x]
			if math.Abs(got-want[x]) > 1e-9 {
				t.Fatalf("U(%d,%d) = %v, want %v", x, y, got, want[x])
			}
		}
	}
}

func TestUpsampleRejectsPlaneLengthMismatch(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	frame := solidFrame(4, 4, impl.Sub420, 1, 2)
	badY := frame
	badY.Y = frame.Y[:15]
	if _, err := u.Upsample(badY); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("short luma plane err = %v, want ErrBadFrame", err)
	}
	badU := frame
	badU.U = frame.U[:3]
	if _, err := u.Upsample(badU); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("short chroma plane err = %v, want ErrBadFrame", err)
	}
	badSpace := frame
	badSpace.ColorSpace = "rgb"
	if _, err := u.Upsample(badSpace); err != nil {
		t.Fatalf("a frame tagged rgb is still a valid plane set, got %v", err)
	}
}

func TestUpsampleRejectsZeroDimensions(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	for _, size := range [][2]int{{0, 4}, {4, 0}, {-2, 4}} {
		frame := impl.YUVFrame{W: size[0], H: size[1]}
		if _, err := u.Upsample(frame); !errors.Is(err, impl.ErrBadFrame) {
			t.Fatalf("size %v err = %v, want ErrBadFrame", size, err)
		}
	}
}

func TestUpsampleRejectsPixelBudget(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{MaxPixels: 4})
	frame := solidFrame(4, 4, impl.Sub420, 1, 2)
	if _, err := u.Upsample(frame); !errors.Is(err, impl.ErrPixelBudget) {
		t.Fatalf("oversized frame err = %v, want ErrPixelBudget", err)
	}
	small := solidFrame(2, 2, impl.Sub420, 1, 2)
	if _, err := u.Upsample(small); err != nil {
		t.Fatalf("a frame inside the pixel budget must succeed, got %v", err)
	}
}

func TestSetSubsamplingValidation(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	if err := u.SetSubsampling(impl.Sub422); err != nil {
		t.Fatalf("switching to 4:2:2 must succeed, got %v", err)
	}
	if u.Subsampling() != impl.Sub422 {
		t.Fatalf("subsampling after switch = %q, want %q", u.Subsampling(), impl.Sub422)
	}
	if err := u.SetSubsampling(impl.Subsampling("411")); !errors.Is(err, impl.ErrBadSubsampling) {
		t.Fatalf("unknown layout err = %v, want ErrBadSubsampling", err)
	}
	if u.Subsampling() != impl.Sub422 {
		t.Fatalf("subsampling changed by a rejected switch = %q, want %q", u.Subsampling(), impl.Sub422)
	}
}

func TestSetPhaseValidationAndGetter(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	if err := u.SetPhase(impl.PhaseCosited); err != nil {
		t.Fatalf("switching to cosited must succeed, got %v", err)
	}
	if u.Phase() != impl.PhaseCosited {
		t.Fatalf("phase after switch = %q, want %q", u.Phase(), impl.PhaseCosited)
	}
	if err := u.SetPhase(impl.Phase("diagonal")); !errors.Is(err, impl.ErrBadPhase) {
		t.Fatalf("unknown phase err = %v, want ErrBadPhase", err)
	}
	if u.Phase() != impl.PhaseCosited {
		t.Fatalf("phase changed by a rejected switch = %q, want %q", u.Phase(), impl.PhaseCosited)
	}
}

func TestSetEdgeModeValidationAndGetter(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	if err := u.SetEdgeMode(impl.EdgeBilinear); err != nil {
		t.Fatalf("switching to bilinear must succeed, got %v", err)
	}
	if u.Edge() != impl.EdgeBilinear {
		t.Fatalf("edge mode after switch = %q, want %q", u.Edge(), impl.EdgeBilinear)
	}
	if err := u.SetEdgeMode(impl.EdgeMode("bicubic")); !errors.Is(err, impl.ErrBadEdge) {
		t.Fatalf("unknown edge mode err = %v, want ErrBadEdge", err)
	}
	if u.Edge() != impl.EdgeBilinear {
		t.Fatalf("edge mode changed by a rejected switch = %q, want %q", u.Edge(), impl.EdgeBilinear)
	}
}

func TestHandleOddSizeEvenPlan(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	plan, err := u.HandleOddSize(4, 4)
	if err != nil {
		t.Fatalf("planning an even 4:2:0 frame must succeed, got %v", err)
	}
	if plan.W != 4 || plan.H != 4 {
		t.Fatalf("plan output size = %dx%d, want 4x4", plan.W, plan.H)
	}
	if plan.ChromaW != 2 || plan.ChromaH != 2 {
		t.Fatalf("chroma grid = %dx%d, want 2x2", plan.ChromaW, plan.ChromaH)
	}
	if plan.PadRight != 0 || plan.PadBottom != 0 {
		t.Fatalf("padding for an even frame = %d/%d, want 0/0", plan.PadRight, plan.PadBottom)
	}
	if plan.Blocks != 1 {
		t.Fatalf("blocks for an even frame = %d, want 1", plan.Blocks)
	}
	if _, err := u.HandleOddSize(4, 0); !errors.Is(err, impl.ErrBadSize) {
		t.Fatalf("zero height err = %v, want ErrBadSize", err)
	}
	if _, err := u.HandleOddSize(-2, 4); !errors.Is(err, impl.ErrBadSize) {
		t.Fatalf("negative width err = %v, want ErrBadSize", err)
	}
}

func TestUpsampleBlockCountMatchesPlan(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{BlockRows: 2})
	frame := solidFrame(4, 4, impl.Sub420, 1, 2)
	if _, err := u.Upsample(frame); err != nil {
		t.Fatalf("upsample must succeed, got %v", err)
	}
	if u.BlockCount() != 2 {
		t.Fatalf("block count for four rows with two rows per block = %d, want 2", u.BlockCount())
	}
	plan, err := u.HandleOddSize(4, 4)
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	if plan.BlockRows != 2 || plan.Blocks != 2 {
		t.Fatalf("plan rows=%d blocks=%d, want 2 and 2", plan.BlockRows, plan.Blocks)
	}
}

func TestSampleMatchesUpsampleOutput(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	frame := impl.YUVFrame{W: 4, H: 2, ColorSpace: "yuv"}
	frame.Y = []float64{9, 8, 7, 6, 5, 4, 3, 2}
	frame.U = []float64{0, 10}
	frame.V = []float64{4, 8}
	out, err := u.Upsample(frame)
	if err != nil {
		t.Fatalf("upsample must succeed, got %v", err)
	}
	for _, pos := range [][2]int{{0, 0}, {1, 0}, {2, 0}, {3, 1}} {
		x, y := pos[0], pos[1]
		yy, uu, vv, err := u.Sample(frame, x, y)
		if err != nil {
			t.Fatalf("sample at %v must succeed, got %v", pos, err)
		}
		if yy != out.Y[y*4+x] || math.Abs(uu-out.U[y*4+x]) > 1e-9 || math.Abs(vv-out.V[y*4+x]) > 1e-9 {
			t.Fatalf("sample at %v = %v/%v/%v, want %v/%v/%v", pos, yy, uu, vv, out.Y[y*4+x], out.U[y*4+x], out.V[y*4+x])
		}
	}
	if _, _, _, err := u.Sample(frame, 4, 0); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("out of range sample err = %v, want ErrBadFrame", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{})
	if err := u.SetPhase(impl.PhaseCosited); err != nil {
		t.Fatalf("set phase must succeed, got %v", err)
	}
	if err := u.SetSubsampling(impl.Sub422); err != nil {
		t.Fatalf("set subsampling must succeed, got %v", err)
	}
	if err := u.SetEdgeMode(impl.EdgeBilinear); err != nil {
		t.Fatalf("set edge mode must succeed, got %v", err)
	}
	data, err := u.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewChromaUpsampler(impl.ChromaConfig{MaxPixels: 1 << 8})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if restored.Phase() != impl.PhaseCosited || restored.Subsampling() != impl.Sub422 || restored.Edge() != impl.EdgeBilinear {
		t.Fatalf("restored layout = %q/%q/%q, want cosited/420/bilinear", restored.Phase(), restored.Subsampling(), restored.Edge())
	}
	if _, err := restored.Upsample(solidFrame(2, 2, impl.Sub422, 3, 4)); err != nil {
		t.Fatalf("upsampling after restore must succeed, got %v", err)
	}
	if err := restored.Restore([]byte{0, 1, 2}); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot err = %v, want ErrBadSnapshot", err)
	}
	if restored.Phase() != impl.PhaseCosited {
		t.Fatalf("phase changed by a rejected snapshot = %q, want %q", restored.Phase(), impl.PhaseCosited)
	}
}

func TestResetClearsCounters(t *testing.T) {
	u := impl.NewChromaUpsampler(impl.ChromaConfig{BlockRows: 4})
	if _, err := u.Upsample(solidFrame(4, 4, impl.Sub420, 1, 2)); err != nil {
		t.Fatalf("upsample must succeed, got %v", err)
	}
	if u.BlockCount() == 0 {
		t.Fatalf("block count after one upsample = 0, want a positive count")
	}
	if err := u.Reset(); err != nil {
		t.Fatalf("reset must succeed, got %v", err)
	}
	if u.BlockCount() != 0 {
		t.Fatalf("block count after reset = %d, want 0", u.BlockCount())
	}
	if u.Phase() != impl.PhaseCentered || u.Subsampling() != impl.Sub420 {
		t.Fatalf("layout changed by reset: %q/%q", u.Phase(), u.Subsampling())
	}
}
