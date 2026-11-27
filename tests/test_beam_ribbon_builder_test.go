package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch51-go-game-b-09"
)

func ribbonLimits() impl.RibbonLimits {
	return impl.RibbonLimits{
		MaxSegments:       8,
		MaxVertices:       256,
		MaxBeams:          8,
		MinPixelWidth:     0.5,
		PixelsPerUnit:     100,
		DefaultWidth:      0.5,
		BreakAngleDeg:     45,
		ParallelThreshold: 0.98,
	}
}

func straightPath() []impl.Vec3 {
	return []impl.Vec3{{X: 0}, {X: 1}, {X: 2}}
}

func topCamera() impl.Camera {
	return impl.Camera{
		Pos:     impl.Vec3{X: 1, Y: 10},
		Forward: impl.Vec3{Y: -1},
		Up:      impl.Vec3{Z: 1},
		FovY:    60,
	}
}

func boxFrustum(min, max float64) impl.Frustum {
	return impl.Frustum{Planes: []impl.Plane{
		{Normal: impl.Vec3{X: 1}, Offset: -max},
		{Normal: impl.Vec3{X: -1}, Offset: min},
		{Normal: impl.Vec3{Y: 1}, Offset: -max},
		{Normal: impl.Vec3{Y: -1}, Offset: min},
		{Normal: impl.Vec3{Z: 1}, Offset: -max},
		{Normal: impl.Vec3{Z: -1}, Offset: min},
	}}
}

func distance(a, b impl.Vec3) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	dz := a.Z - b.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func TestSetWidthCurveRejectsBadKeys(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	if err := b.SetWidthCurve("default", nil); !errors.Is(err, impl.ErrBadCurve) {
		t.Fatalf("an empty curve must fail with ErrBadCurve, got %v", err)
	}
	unsorted := []impl.Key{{T: 0.8, Width: 1}, {T: 0.2, Width: 1}}
	if err := b.SetWidthCurve("default", unsorted); !errors.Is(err, impl.ErrBadCurve) {
		t.Fatalf("an unsorted curve must fail with ErrBadCurve, got %v", err)
	}
	duplicated := []impl.Key{{T: 0.5, Width: 1}, {T: 0.5, Width: 2}}
	if err := b.SetWidthCurve("default", duplicated); !errors.Is(err, impl.ErrBadCurve) {
		t.Fatalf("a duplicated key time must fail with ErrBadCurve, got %v", err)
	}
	outOfRange := []impl.Key{{T: 0, Width: 1}, {T: 1.5, Width: 1}}
	if err := b.SetWidthCurve("default", outOfRange); !errors.Is(err, impl.ErrBadCurve) {
		t.Fatalf("a key beyond the path interval must fail with ErrBadCurve, got %v", err)
	}
	zeroWidth := []impl.Key{{T: 0, Width: 0}, {T: 1, Width: 1}}
	if err := b.SetWidthCurve("default", zeroWidth); !errors.Is(err, impl.ErrBadCurve) {
		t.Fatalf("a non positive width must fail with ErrBadCurve, got %v", err)
	}
	if err := b.SetWidthCurve("   ", []impl.Key{{T: 0, Width: 1}}); !errors.Is(err, impl.ErrBadBeam) {
		t.Fatalf("a blank beam id must fail with ErrBadBeam, got %v", err)
	}
}

func TestWidthAtInterpolatesLinearly(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	keys := []impl.Key{{T: 0, Width: 0.5}, {T: 1, Width: 1.5}}
	if err := b.SetWidthCurve("default", keys); err != nil {
		t.Fatalf("set width curve: %v", err)
	}
	mid, err := b.WidthAt("default", 0.25)
	if err != nil {
		t.Fatalf("width at a quarter: %v", err)
	}
	if math.Abs(mid-0.75) > 1e-9 {
		t.Fatalf("width at t=0.25 = %v, want 0.75", mid)
	}
	low, err := b.WidthAt("default", -1)
	if err != nil {
		t.Fatalf("width below the interval: %v", err)
	}
	if math.Abs(low-0.5) > 1e-9 {
		t.Fatalf("width at t=-1 = %v, want the first key 0.5", low)
	}
	high, err := b.WidthAt("default", 2)
	if err != nil {
		t.Fatalf("width above the interval: %v", err)
	}
	if math.Abs(high-1.5) > 1e-9 {
		t.Fatalf("width at t=2 = %v, want the last key 1.5", high)
	}
	if _, err := b.WidthAt("ghost", 0.5); !errors.Is(err, impl.ErrUnknownBeam) {
		t.Fatalf("a beam without a curve must fail with ErrUnknownBeam, got %v", err)
	}
}

func TestSetBudgetRejectsNonPositive(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	if err := b.SetBudget(0); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("a zero vertex budget must fail with ErrBadBudget, got %v", err)
	}
	if err := b.SetBudget(-4); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("a negative vertex budget must fail with ErrBadBudget, got %v", err)
	}
	if err := b.SetBudget(64); err != nil {
		t.Fatalf("set a positive budget: %v", err)
	}
	if _, total := b.Budget(); total != 64 {
		t.Fatalf("budget total = %d, want 64", total)
	}
}

func TestBuildRejectsShortPath(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	if _, err := b.Build([]impl.Vec3{{X: 1}}, topCamera()); !errors.Is(err, impl.ErrShortPath) {
		t.Fatalf("a single point path must fail with ErrShortPath, got %v", err)
	}
	if _, err := b.Build(nil, topCamera()); !errors.Is(err, impl.ErrShortPath) {
		t.Fatalf("an empty path must fail with ErrShortPath, got %v", err)
	}
}

func TestBuildRejectsDegeneratePath(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	path := []impl.Vec3{{X: 0}, {X: 0}, {X: 1}}
	if _, err := b.Build(path, topCamera()); !errors.Is(err, impl.ErrDegeneratePath) {
		t.Fatalf("a zero length segment must fail with ErrDegeneratePath, got %v", err)
	}
}

func TestBuildRejectsBadCamera(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	camera := impl.Camera{Pos: impl.Vec3{Y: 10}, Up: impl.Vec3{Z: 1}}
	if _, err := b.Build(straightPath(), camera); !errors.Is(err, impl.ErrBadCamera) {
		t.Fatalf("a camera without a forward axis must fail with ErrBadCamera, got %v", err)
	}
}

func TestBuildProducesVertexPairs(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	ribbon, err := b.Build(straightPath(), topCamera())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if ribbon.Segments != 1 {
		t.Fatalf("segments of a straight path = %d, want 1", ribbon.Segments)
	}
	if ribbon.BeamID != impl.DefaultBeam {
		t.Fatalf("ribbon beam id = %q, want %q", ribbon.BeamID, impl.DefaultBeam)
	}
	if len(ribbon.Vertices) != 6 {
		t.Fatalf("vertices of a three point path = %d, want 6", len(ribbon.Vertices))
	}
	if len(ribbon.Widths) != 3 {
		t.Fatalf("widths of a three point path = %d, want 3", len(ribbon.Widths))
	}
	path := straightPath()
	for i := 0; i < 3; i++ {
		left := ribbon.Vertices[2*i]
		right := ribbon.Vertices[2*i+1]
		mid := impl.Vec3{
			X: (left.X + right.X) / 2,
			Y: (left.Y + right.Y) / 2,
			Z: (left.Z + right.Z) / 2,
		}
		if distance(mid, path[i]) > 1e-9 {
			t.Fatalf("middle of vertex pair %d = %+v, want the path point %+v", i, mid, path[i])
		}
		if math.Abs(distance(left, right)-0.5) > 1e-9 {
			t.Fatalf("width of vertex pair %d = %v, want the default 0.5", i, distance(left, right))
		}
	}
}

func TestBuildUsesWidthCurve(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	keys := []impl.Key{{T: 0, Width: 0.4}, {T: 1, Width: 0.8}}
	if err := b.SetWidthCurve(impl.DefaultBeam, keys); err != nil {
		t.Fatalf("set width curve: %v", err)
	}
	ribbon, err := b.Build(straightPath(), topCamera())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []float64{0.4, 0.6, 0.8}
	for i, width := range want {
		if math.Abs(ribbon.Widths[i]-width) > 1e-9 {
			t.Fatalf("width %d = %v, want %v from the linear curve", i, ribbon.Widths[i], width)
		}
		if math.Abs(distance(ribbon.Vertices[2*i], ribbon.Vertices[2*i+1])-width) > 1e-9 {
			t.Fatalf("vertex pair %d spans %v, want %v", i, distance(ribbon.Vertices[2*i], ribbon.Vertices[2*i+1]), width)
		}
	}
}

func TestBuildBreaksOnSharpTurn(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	path := []impl.Vec3{{X: 0}, {X: 1}, {X: 1, Y: 1}, {X: 1, Y: 2}}
	ribbon, err := b.Build(path, topCamera())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if ribbon.Segments != 2 {
		t.Fatalf("segments for a ninety degree turn with a forty five degree threshold = %d, want 2", ribbon.Segments)
	}
	if len(ribbon.Vertices) != 10 {
		t.Fatalf("vertices = %d, want two pairs per path point plus one duplicated break point", len(ribbon.Vertices))
	}
	if len(ribbon.Widths) != 5 {
		t.Fatalf("width samples = %d, want five for two segments sharing the break point", len(ribbon.Widths))
	}
}

func TestBuildRejectsTooManySegments(t *testing.T) {
	limits := ribbonLimits()
	limits.MaxSegments = 1
	b := impl.NewBeamRibbon(limits)
	path := []impl.Vec3{{X: 0}, {X: 1}, {X: 1, Y: 1}, {Y: 1}}
	if _, err := b.Build(path, topCamera()); !errors.Is(err, impl.ErrTooManySegments) {
		t.Fatalf("three segments against a limit of one must fail with ErrTooManySegments, got %v", err)
	}
}

func TestBuildRespectsMinPixelWidth(t *testing.T) {
	limits := ribbonLimits()
	limits.DefaultWidth = 0.05
	b := impl.NewBeamRibbon(limits)
	ribbon, err := b.Build(straightPath(), topCamera())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for i := 0; i < 3; i++ {
		span := distance(ribbon.Vertices[2*i], ribbon.Vertices[2*i+1])
		if span < 0.1999 {
			t.Fatalf("pair %d spans %v, want at least the min pixel width 0.2 at ten metres", i, span)
		}
		if ribbon.Widths[i] < 0.1999 {
			t.Fatalf("width %d = %v, want at least 0.2 after the pixel floor", i, ribbon.Widths[i])
		}
	}
}

func TestBuildRegistersBeamForCull(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	if _, err := b.Build(straightPath(), topCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := b.Stats().Builds; got != 1 {
		t.Fatalf("build counter = %d, want 1", got)
	}
	visible := b.Cull(boxFrustum(-5, 5))
	if len(visible) != 1 || visible[0] != impl.DefaultBeam {
		t.Fatalf("visible beams = %v, want only %q", visible, impl.DefaultBeam)
	}
	if got := b.Stats().Culled; got != 0 {
		t.Fatalf("cull counter = %d, want 0 when the beam is inside the view", got)
	}
}

func TestCullDropsBeamsOutsideView(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	if _, err := b.Build(straightPath(), topCamera()); err != nil {
		t.Fatalf("build the default beam: %v", err)
	}
	if err := b.SetActiveBeam("far-beam", 5); err != nil {
		t.Fatalf("select the far beam: %v", err)
	}
	farPath := []impl.Vec3{{X: 100}, {X: 101}, {X: 102}}
	if _, err := b.Build(farPath, topCamera()); err != nil {
		t.Fatalf("build the far beam: %v", err)
	}
	visible := b.Cull(boxFrustum(-5, 5))
	if len(visible) != 1 || visible[0] != impl.DefaultBeam {
		t.Fatalf("visible beams = %v, want only %q", visible, impl.DefaultBeam)
	}
	if got := b.Stats().Culled; got != 1 {
		t.Fatalf("cull counter = %d, want 1 for the far beam", got)
	}
	if err := b.SetActiveBeam("zzz", 1); err != nil {
		t.Fatalf("select a third beam: %v", err)
	}
	if _, err := b.Build([]impl.Vec3{{X: 0.5}, {X: 1.5}}, topCamera()); err != nil {
		t.Fatalf("build the third beam: %v", err)
	}
	visible = b.Cull(boxFrustum(-5, 5))
	if len(visible) != 2 || visible[0] != impl.DefaultBeam || visible[1] != impl.BeamID("zzz") {
		t.Fatalf("visible beams = %v, want the two inside beams in ascending order", visible)
	}
}

func TestReleaseRemovesBeam(t *testing.T) {
	b := impl.NewBeamRibbon(ribbonLimits())
	if _, err := b.Build(straightPath(), topCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := b.Release(impl.DefaultBeam); err != nil {
		t.Fatalf("release the default beam: %v", err)
	}
	if got := len(b.Cull(boxFrustum(-5, 5))); got != 0 {
		t.Fatalf("visible beams after the release = %d, want 0", got)
	}
	if got := b.Stats().Released; got != 1 {
		t.Fatalf("release counter = %d, want 1", got)
	}
	if err := b.Release(impl.DefaultBeam); !errors.Is(err, impl.ErrUnknownBeam) {
		t.Fatalf("releasing an unregistered beam must fail with ErrUnknownBeam, got %v", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	limits := ribbonLimits()
	b := impl.NewBeamRibbon(limits)
	keys := []impl.Key{{T: 0, Width: 0.4}, {T: 1, Width: 0.8}}
	if err := b.SetWidthCurve(impl.DefaultBeam, keys); err != nil {
		t.Fatalf("set width curve: %v", err)
	}
	if err := b.SetBudget(128); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	if _, err := b.Build(straightPath(), topCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	data, err := b.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewBeamRibbon(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	width, err := restored.WidthAt(impl.DefaultBeam, 0.5)
	if err != nil {
		t.Fatalf("width after the restore: %v", err)
	}
	if math.Abs(width-0.6) > 1e-9 {
		t.Fatalf("width after the restore = %v, want 0.6", width)
	}
	if _, total := restored.Budget(); total != 128 {
		t.Fatalf("budget after the restore = %d, want 128", total)
	}
	if got := restored.Stats().Builds; got != 1 {
		t.Fatalf("build counter after the restore = %d, want 1", got)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	kept, err := restored.WidthAt(impl.DefaultBeam, 0.5)
	if err != nil || math.Abs(kept-0.6) > 1e-9 {
		t.Fatalf("a rejected restore changed the width to %v (err %v), want 0.6", kept, err)
	}
}
