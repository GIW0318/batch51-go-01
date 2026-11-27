package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-c-10"
)

func plane(w, h int, value float32) impl.Plane {
	data := make([]float32, w*h)
	for i := range data {
		data[i] = value
	}
	return impl.Plane{Width: w, Height: h, Data: data}
}

func planeRow(values ...float32) impl.Plane {
	return impl.Plane{Width: len(values), Height: 1, Data: values}
}

func planeCol(values ...float32) impl.Plane {
	return impl.Plane{Width: 1, Height: len(values), Data: values}
}

func mustUpsample(t *testing.T, u *impl.ChromaUpsamplerP5, y, uPlane, vPlane impl.Plane) impl.Chroma444 {
	t.Helper()
	out, err := u.Upsample(y, uPlane, vPlane)
	if err != nil {
		t.Fatalf("upsample: %v", err)
	}
	if out.Width != y.Width || out.Height != y.Height {
		t.Fatalf("output is %dx%d, want the luma size %dx%d", out.Width, out.Height, y.Width, y.Height)
	}
	if len(out.Y) != y.Width*y.Height || len(out.U) != y.Width*y.Height || len(out.V) != y.Width*y.Height {
		t.Fatalf("output plane lengths = %d/%d/%d, want %d", len(out.Y), len(out.U), len(out.V), y.Width*y.Height)
	}
	return out
}

func TestNewUpsamplerDefaults(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	stats := u.Stats()
	if stats.Siting != impl.SitingCenter {
		t.Fatalf("default siting = %q, want %q", stats.Siting, impl.SitingCenter)
	}
	if stats.Matrix != impl.ColorSpaceBT709 {
		t.Fatalf("default matrix = %q, want %q", stats.Matrix, impl.ColorSpaceBT709)
	}
	if stats.BitDepth != 10 {
		t.Fatalf("default bit depth = %d, want 10", stats.BitDepth)
	}
	if !stats.FullRange {
		t.Fatalf("input must default to full range")
	}
	if stats.EdgeGuided {
		t.Fatalf("edge guidance must default to off")
	}
	if stats.Frames != 0 || stats.Samples != 0 {
		t.Fatalf("fresh counters = %d frames, %d samples; want zero", stats.Frames, stats.Samples)
	}
}

func TestSetSitingValidatesTheValue(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	if err := u.SetSiting("diagonal"); !errors.Is(err, impl.ErrBadSiting) {
		t.Fatalf("unknown siting must fail with ErrBadSiting, got %v", err)
	}
	if got := u.Stats().Siting; got != impl.SitingCenter {
		t.Fatalf("rejected siting must keep the old value, got %q", got)
	}
	if err := u.SetSiting(impl.SitingLeft); err != nil {
		t.Fatalf("left siting must be accepted, got %v", err)
	}
	if got := u.Stats().Siting; got != impl.SitingLeft {
		t.Fatalf("siting = %q, want %q", got, impl.SitingLeft)
	}
}

func TestSetMatrixValidatesTheValue(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	if err := u.SetMatrix("bt2020"); !errors.Is(err, impl.ErrBadMatrix) {
		t.Fatalf("unknown matrix must fail with ErrBadMatrix, got %v", err)
	}
	if err := u.SetMatrix(impl.ColorSpaceBT601); err != nil {
		t.Fatalf("BT.601 must be accepted, got %v", err)
	}
	if got := u.Stats().Matrix; got != impl.ColorSpaceBT601 {
		t.Fatalf("matrix = %q, want %q", got, impl.ColorSpaceBT601)
	}
}

func TestSetBitDepthValidatesTheRange(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	if err := u.SetBitDepth(impl.MinBitDepth - 1); !errors.Is(err, impl.ErrBadBitDepth) {
		t.Fatalf("depth below the window must fail with ErrBadBitDepth, got %v", err)
	}
	if err := u.SetBitDepth(impl.MaxBitDepth + 1); !errors.Is(err, impl.ErrBadBitDepth) {
		t.Fatalf("depth above the window must fail with ErrBadBitDepth, got %v", err)
	}
	if err := u.SetBitDepth(impl.MinBitDepth); err != nil {
		t.Fatalf("eight bits must be accepted, got %v", err)
	}
	if got := u.Stats().BitDepth; got != impl.MinBitDepth {
		t.Fatalf("bit depth = %d, want %d", got, impl.MinBitDepth)
	}
}

func TestSetRangeTogglesFullRange(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	if !u.Stats().FullRange {
		t.Fatalf("full range must start enabled")
	}
	if err := u.SetRange(false); err != nil {
		t.Fatalf("switch to limited range: %v", err)
	}
	if u.Stats().FullRange {
		t.Fatalf("stats still report full range after switching to limited")
	}
}

func TestSetEdgeGuidedTogglesTheFlag(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	if u.Stats().EdgeGuided {
		t.Fatalf("edge guidance must start off")
	}
	if err := u.SetEdgeGuided(true); err != nil {
		t.Fatalf("enable edge guidance: %v", err)
	}
	if !u.Stats().EdgeGuided {
		t.Fatalf("stats must report edge guidance as enabled")
	}
}

func TestUpsampleRejectsEmptyPlanes(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	y := impl.Plane{Width: 4, Height: 4}
	if _, err := u.Upsample(y, plane(2, 2, 0.5), plane(2, 2, 0.5)); !errors.Is(err, impl.ErrEmptyPlane) {
		t.Fatalf("a luma plane without samples must fail with ErrEmptyPlane, got %v", err)
	}
	if _, err := u.Upsample(plane(4, 4, 0.5), impl.Plane{Width: 2, Height: 2}, plane(2, 2, 0.5)); !errors.Is(err, impl.ErrEmptyPlane) {
		t.Fatalf("a chroma plane without samples must fail with ErrEmptyPlane, got %v", err)
	}
}

func TestUpsampleRejectsPlaneMismatch(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	if _, err := u.Upsample(plane(4, 4, 0.5), plane(3, 3, 0.4), plane(2, 2, 0.4)); !errors.Is(err, impl.ErrPlaneMismatch) {
		t.Fatalf("a 3x3 chroma plane for a 4x4 luma frame must fail with ErrPlaneMismatch, got %v", err)
	}
	if _, err := u.Upsample(plane(4, 4, 0.5), plane(2, 2, 0.4), plane(1, 1, 0.4)); !errors.Is(err, impl.ErrPlaneMismatch) {
		t.Fatalf("mismatched chroma planes must fail with ErrPlaneMismatch, got %v", err)
	}
}

func TestUpsampleRejectsOversizeFrames(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	y := impl.Plane{Width: 1920, Height: 1081}
	if y.Width*y.Height <= impl.MaxPixels {
		t.Fatalf("test fixture must exceed the pixel budget")
	}
	if _, err := u.Upsample(y, plane(2, 2, 0.5), plane(2, 2, 0.5)); !errors.Is(err, impl.ErrFrameTooLarge) {
		t.Fatalf("a frame above the pixel budget must fail with ErrFrameTooLarge, got %v", err)
	}
	if got := u.Stats().Frames; got != 0 {
		t.Fatalf("rejected frames must not advance the counter, got %d", got)
	}
}

func TestUpsampleReplicatesAUniformChromaSample(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	y := plane(2, 2, 0.5)
	out := mustUpsample(t, u, y, plane(1, 1, 0.3), plane(1, 1, 0.7))
	for i := range out.Y {
		if out.Y[i] != 0.5 {
			t.Fatalf("luma sample %d = %v, want the untouched input 0.5", i, out.Y[i])
		}
		if out.U[i] < 0.2999 || out.U[i] > 0.3001 {
			t.Fatalf("chroma U sample %d = %v, want 0.3", i, out.U[i])
		}
		if out.V[i] < 0.6999 || out.V[i] > 0.7001 {
			t.Fatalf("chroma V sample %d = %v, want 0.7", i, out.V[i])
		}
	}
}

func TestUpsampleCentreSitingWeights(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	y := plane(4, 1, 0.5)
	out := mustUpsample(t, u, y, planeRow(0, 1), planeRow(0, 0))
	want := []float32{0, 0.25, 0.75, 1}
	for i := range want {
		if out.U[i] < want[i]-1e-6 || out.U[i] > want[i]+1e-6 {
			t.Fatalf("centre sited U sample %d = %v, want %v", i, out.U[i], want[i])
		}
	}
}

func TestUpsampleLeftSitingWeights(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	if err := u.SetSiting(impl.SitingLeft); err != nil {
		t.Fatalf("set left siting: %v", err)
	}
	y := plane(4, 1, 0.5)
	out := mustUpsample(t, u, y, planeRow(0, 1), planeRow(0, 0))
	want := []float32{0, 0.5, 1, 1}
	for i := range want {
		if out.U[i] < want[i]-1e-6 || out.U[i] > want[i]+1e-6 {
			t.Fatalf("left sited U sample %d = %v, want %v", i, out.U[i], want[i])
		}
	}
}

func TestUpsampleInterpolatesRows(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	y := plane(1, 4, 0.5)
	out := mustUpsample(t, u, y, planeCol(0, 1), planeCol(0, 0))
	want := []float32{0, 0.25, 0.75, 1}
	for row := range want {
		if out.U[row] < want[row]-1e-6 || out.U[row] > want[row]+1e-6 {
			t.Fatalf("centre sited U row %d = %v, want %v", row, out.U[row], want[row])
		}
	}
}

func TestStatsCountsFramesAndSamples(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	mustUpsample(t, u, plane(4, 4, 0.5), plane(2, 2, 0.2), plane(2, 2, 0.6))
	mustUpsample(t, u, plane(4, 4, 0.6), plane(2, 2, 0.3), plane(2, 2, 0.7))
	stats := u.Stats()
	if stats.Frames != 2 {
		t.Fatalf("frame counter = %d, want 2", stats.Frames)
	}
	if stats.Samples != 32 {
		t.Fatalf("sample counter = %d, want 32 for two 4x4 frames", stats.Samples)
	}
	if err := u.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := u.Stats().Frames; got != 0 {
		t.Fatalf("frame counter after reset = %d, want 0", got)
	}
}

func TestSnapshotRestoreKeepsSettings(t *testing.T) {
	u := impl.NewChromaUpsamplerP5()
	if err := u.SetSiting(impl.SitingLeft); err != nil {
		t.Fatalf("set siting: %v", err)
	}
	if err := u.SetBitDepth(12); err != nil {
		t.Fatalf("set bit depth: %v", err)
	}
	if err := u.SetRange(false); err != nil {
		t.Fatalf("set range: %v", err)
	}
	if err := u.SetEdgeGuided(true); err != nil {
		t.Fatalf("set edge guidance: %v", err)
	}
	mustUpsample(t, u, plane(4, 4, 0.5), plane(2, 2, 0.2), plane(2, 2, 0.6))
	data, err := u.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewChromaUpsamplerP5()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := restored.Stats()
	if got.Siting != impl.SitingLeft || got.BitDepth != 12 || got.FullRange || !got.EdgeGuided {
		t.Fatalf("restored settings = %+v, want left siting, twelve bits, limited range, edge guidance on", got)
	}
	if got.Frames != 1 {
		t.Fatalf("restored frame counter = %d, want 1", got.Frames)
	}
	if err := restored.Restore([]byte("{bad")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Stats().BitDepth; got != 12 {
		t.Fatalf("rejected restore must not touch live state, bit depth = %d", got)
	}
}
