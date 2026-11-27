package task

import (
	"errors"
	"math"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-b-04"
)

func baseFurLimits() impl.FurLimits {
	return impl.FurLimits{
		MaxLayers:      4,
		LayerSpacing:   0.05,
		MaxVertices:    1000,
		ShadowLayers:   2,
		ShadowDistance: 10,
	}
}

func flatFurMesh() impl.Mesh {
	return impl.Mesh{Vertices: []impl.Vertex{
		{Position: impl.Vec3{X: 0, Y: 10, Z: 0}, Normal: impl.Vec3{Z: 1}, UV: impl.UV{U: 0, V: 0}},
		{Position: impl.Vec3{X: 1, Y: 10, Z: 0}, Normal: impl.Vec3{Z: 1}, UV: impl.UV{U: 1, V: 0}},
		{Position: impl.Vec3{X: 0, Y: 10, Z: 1}, Normal: impl.Vec3{Z: 1}, UV: impl.UV{U: 0, V: 1}},
		{Position: impl.Vec3{X: 1, Y: 10, Z: 1}, Normal: impl.Vec3{Z: 1}, UV: impl.UV{U: 1, V: 1}},
	}}
}

func flatDensity(v float64) impl.DensityMap {
	return impl.DensityMap{Width: 2, Height: 2, Values: []float64{v, v, v, v}}
}

func cornerDensity() impl.DensityMap {
	return impl.DensityMap{Width: 2, Height: 2, Values: []float64{0, 1, 1, 0}}
}

func near(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

func TestTargetLayersFollowDensity(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	cases := []struct {
		density float64
		want    int
	}{
		{1, 4},
		{0.5, 2},
		{0.25, 1},
		{0, 0},
	}
	for _, tc := range cases {
		got, err := g.TargetLayers(tc.density)
		if err != nil {
			t.Fatalf("target layers for %v: %v", tc.density, err)
		}
		if got != tc.want {
			t.Fatalf("target layers for density %v = %d, want %d", tc.density, got, tc.want)
		}
	}
	if _, err := g.TargetLayers(1.5); !errors.Is(err, impl.ErrBadDensity) {
		t.Fatalf("density over one must fail with ErrBadDensity, got %v", err)
	}
	if _, err := g.TargetLayers(-0.1); !errors.Is(err, impl.ErrBadDensity) {
		t.Fatalf("negative density must fail with ErrBadDensity, got %v", err)
	}
}

func TestGenerateAlphaFadesAndTipTapers(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	set, err := g.Generate(flatFurMesh(), flatDensity(1))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(set.Layers) != 4 {
		t.Fatalf("layers = %d, want 4", len(set.Layers))
	}
	wantAlpha := []float64{0.8, 0.6, 0.4, 0.2}
	for i, layer := range set.Layers {
		if layer.Index != i {
			t.Fatalf("layer %d carries index %d", i, layer.Index)
		}
		if !near(layer.Alpha, wantAlpha[i]) {
			t.Fatalf("layer %d alpha = %v, want %v", i, layer.Alpha, wantAlpha[i])
		}
		if layer.Vertices != 4 {
			t.Fatalf("layer %d vertices = %d, want 4", i, layer.Vertices)
		}
		if layer.Tip != (i == 3) {
			t.Fatalf("layer %d tip = %v, want only the outermost layer tapered", i, layer.Tip)
		}
	}
	if set.VertexCount != 16 {
		t.Fatalf("vertex count = %d, want 4 layers * 4 vertices", set.VertexCount)
	}
	if set.Shadow {
		t.Fatalf("a main shell set must not be flagged as shadow")
	}
	if set.SkinVisible {
		t.Fatalf("a fully dense mesh must not report visible skin")
	}
}

func TestGenerateOffsetsAlongNormal(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	set, err := g.Generate(flatFurMesh(), flatDensity(1))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for i, layer := range set.Layers {
		want := 0.05 * float64(i+1)
		if !near(layer.Distance, want) {
			t.Fatalf("layer %d distance = %v, want %v", i, layer.Distance, want)
		}
		if !near(layer.Offset.X, 0) || !near(layer.Offset.Y, 0) || !near(layer.Offset.Z, want) {
			t.Fatalf("layer %d offset = %+v, want (0,0,%v) along the normal", i, layer.Offset, want)
		}
	}
}

func TestGenerateAppliesGravityDirection(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	if err := g.SetOffsetPolicy(impl.OffsetPolicy{Gravity: impl.Vec3{Y: -1}, MaxOffset: 1}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	set, err := g.Generate(flatFurMesh(), flatDensity(1))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	layer := set.Layers[0]
	wantUnit := 1 / math.Sqrt2
	if !near(layer.Offset.X, 0) {
		t.Fatalf("offset x = %v, want 0", layer.Offset.X)
	}
	if !near(layer.Offset.Y, -0.05*wantUnit) {
		t.Fatalf("offset y = %v, want %v", layer.Offset.Y, -0.05*wantUnit)
	}
	if !near(layer.Offset.Z, 0.05*wantUnit) {
		t.Fatalf("offset z = %v, want %v", layer.Offset.Z, 0.05*wantUnit)
	}
}

func TestGenerateRespectsVertexBudget(t *testing.T) {
	limits := baseFurLimits()
	limits.MaxVertices = 8
	g := impl.NewFurShellGenerator(limits)
	set, err := g.Generate(flatFurMesh(), flatDensity(1))
	if err != nil {
		t.Fatalf("generate under the vertex budget: %v", err)
	}
	if len(set.Layers) != 2 {
		t.Fatalf("layers after the budget cut = %d, want 2", len(set.Layers))
	}
	if set.VertexCount != 8 {
		t.Fatalf("vertex count = %d, want the hard cap 8", set.VertexCount)
	}
	wantAlpha := []float64{1 - 1.0/3, 1 - 2.0/3}
	for i, layer := range set.Layers {
		if !near(layer.Alpha, wantAlpha[i]) {
			t.Fatalf("layer %d alpha = %v, want %v after the cut", i, layer.Alpha, wantAlpha[i])
		}
	}
}

func TestGenerateRejectsVertexBudgetOverflow(t *testing.T) {
	limits := baseFurLimits()
	limits.MaxVertices = 3
	g := impl.NewFurShellGenerator(limits)
	if _, err := g.Generate(flatFurMesh(), flatDensity(1)); !errors.Is(err, impl.ErrVertexBudget) {
		t.Fatalf("four vertices over a budget of three must fail with ErrVertexBudget, got %v", err)
	}
}

func TestGenerateRejectsBadMesh(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	if _, err := g.Generate(impl.Mesh{}, flatDensity(1)); !errors.Is(err, impl.ErrBadMesh) {
		t.Fatalf("empty mesh must fail with ErrBadMesh, got %v", err)
	}
	zeroNormal := flatFurMesh()
	zeroNormal.Vertices[0].Normal = impl.Vec3{}
	if _, err := g.Generate(zeroNormal, flatDensity(1)); !errors.Is(err, impl.ErrBadMesh) {
		t.Fatalf("zero normal must fail with ErrBadMesh, got %v", err)
	}
	badUV := flatFurMesh()
	badUV.Vertices[1].UV.U = 1.5
	if _, err := g.Generate(badUV, flatDensity(1)); !errors.Is(err, impl.ErrBadMesh) {
		t.Fatalf("uv outside the unit square must fail with ErrBadMesh, got %v", err)
	}
}

func TestGenerateRejectsBadDensity(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	short := impl.DensityMap{Width: 2, Height: 2, Values: []float64{1, 1, 1}}
	if _, err := g.Generate(flatFurMesh(), short); !errors.Is(err, impl.ErrBadDensity) {
		t.Fatalf("short density grid must fail with ErrBadDensity, got %v", err)
	}
	noSize := impl.DensityMap{Values: []float64{1}}
	if _, err := g.Generate(flatFurMesh(), noSize); !errors.Is(err, impl.ErrBadDensity) {
		t.Fatalf("zero sized density map must fail with ErrBadDensity, got %v", err)
	}
	tooBig := impl.DensityMap{Width: 2, Height: 2, Values: []float64{1, 1, 1, 1.2}}
	if _, err := g.Generate(flatFurMesh(), tooBig); !errors.Is(err, impl.ErrBadDensity) {
		t.Fatalf("density over one must fail with ErrBadDensity, got %v", err)
	}
}

func TestSampleDensityBilinear(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	if _, err := g.SampleDensity(impl.UV{U: 0.5, V: 0.5}); !errors.Is(err, impl.ErrNotReady) {
		t.Fatalf("sampling before the first generate must fail with ErrNotReady, got %v", err)
	}
	if _, err := g.Generate(flatFurMesh(), cornerDensity()); err != nil {
		t.Fatalf("generate: %v", err)
	}
	cases := []struct {
		uv   impl.UV
		want float64
	}{
		{impl.UV{U: 0, V: 0}, 0},
		{impl.UV{U: 1, V: 0}, 1},
		{impl.UV{U: 0, V: 1}, 1},
		{impl.UV{U: 1, V: 1}, 0},
		{impl.UV{U: 0.5, V: 0.5}, 0.5},
		{impl.UV{U: -1, V: 0.5}, 0.5},
	}
	for _, tc := range cases {
		got, err := g.SampleDensity(tc.uv)
		if err != nil {
			t.Fatalf("sample density at %+v: %v", tc.uv, err)
		}
		if !near(got, tc.want) {
			t.Fatalf("sample density at %+v = %v, want %v", tc.uv, got, tc.want)
		}
	}
}

func TestBuildShadowShellsCapsLayers(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	main, err := g.Generate(flatFurMesh(), flatDensity(1))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	shadow, err := g.BuildShadowShells(main, 3)
	if err != nil {
		t.Fatalf("build shadow shells: %v", err)
	}
	if !shadow.Shadow {
		t.Fatalf("shadow set must be flagged as shadow")
	}
	if len(shadow.Layers) != 2 {
		t.Fatalf("shadow layers = %d, want min(3, ShadowLayers=2) = 2", len(shadow.Layers))
	}
	total := 0
	for i, layer := range shadow.Layers {
		if layer.Index != i {
			t.Fatalf("shadow layer %d carries index %d", i, layer.Index)
		}
		total += layer.Vertices
	}
	if shadow.VertexCount != total || total != 8 {
		t.Fatalf("shadow vertex count = %d (sum %d), want 8", shadow.VertexCount, total)
	}
}

func TestBuildShadowShellsRejectsBadLayers(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	main, err := g.Generate(flatFurMesh(), flatDensity(1))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, maxLayers := range []int{0, -2} {
		if _, err := g.BuildShadowShells(main, maxLayers); !errors.Is(err, impl.ErrBadLayers) {
			t.Fatalf("max layers %d must fail with ErrBadLayers, got %v", maxLayers, err)
		}
	}
}

func TestBuildShadowShellsSkipsFarObjects(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	main, err := g.Generate(flatFurMesh(), flatDensity(1))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	far := main
	far.Distance = 50
	shadow, err := g.BuildShadowShells(far, 2)
	if err != nil {
		t.Fatalf("far shadow shells must be skipped without an error, got %v", err)
	}
	if !shadow.Shadow {
		t.Fatalf("skipped shadow set must still be flagged as shadow")
	}
	if len(shadow.Layers) != 0 || shadow.VertexCount != 0 {
		t.Fatalf("far shadow set = %d layers %d vertices, want an empty set", len(shadow.Layers), shadow.VertexCount)
	}
}

func TestOffsetPolicyValidation(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	if err := g.SetOffsetPolicy(impl.OffsetPolicy{MaxOffset: -1}); !errors.Is(err, impl.ErrBadPolicy) {
		t.Fatalf("negative max offset must fail with ErrBadPolicy, got %v", err)
	}
	policy := impl.OffsetPolicy{Gravity: impl.Vec3{X: 0, Y: -0.5}, Wind: impl.Vec3{X: 0.25}, MaxOffset: 2}
	if err := g.SetOffsetPolicy(policy); err != nil {
		t.Fatalf("valid policy: %v", err)
	}
	if got := g.OffsetPolicy(); got != policy {
		t.Fatalf("stored policy = %+v, want %+v", got, policy)
	}
	main, err := g.Generate(flatFurMesh(), flatDensity(1))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := g.LayerAt(main, 4); !errors.Is(err, impl.ErrBadLayers) {
		t.Fatalf("layer 4 of a four layer set must fail with ErrBadLayers, got %v", err)
	}
	layer, err := g.LayerAt(main, 1)
	if err != nil || layer.Index != 1 {
		t.Fatalf("layer at 1 = %+v err=%v, want index 1", layer, err)
	}
	if got := g.VertexTotal(main); got != main.VertexCount {
		t.Fatalf("vertex total = %d, want %d", got, main.VertexCount)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	policy := impl.OffsetPolicy{Gravity: impl.Vec3{Y: -0.2}, MaxOffset: 1}
	if err := g.SetOffsetPolicy(policy); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if _, err := g.Generate(flatFurMesh(), flatDensity(0.75)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewFurShellGenerator(baseFurLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.OffsetPolicy(); got != policy {
		t.Fatalf("restored policy = %+v, want %+v", got, policy)
	}
	got, err := restored.SampleDensity(impl.UV{U: 0.5, V: 0.5})
	if err != nil {
		t.Fatalf("sample density after restore: %v", err)
	}
	if !near(got, 0.75) {
		t.Fatalf("restored density = %v, want 0.75", got)
	}
	if err := restored.Restore([]byte("fur")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if got, err := restored.SampleDensity(impl.UV{U: 0.5, V: 0.5}); err != nil || !near(got, 0.75) {
		t.Fatalf("failed restore changed the live density: %v err=%v", got, err)
	}
}

func TestConcurrentGenerateAndSnapshot(t *testing.T) {
	g := impl.NewFurShellGenerator(baseFurLimits())
	mesh := flatFurMesh()
	var wg sync.WaitGroup
	failures := make(chan string, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			density := 0.25 * float64(i%4+1)
			for j := 0; j < 20; j++ {
				set, err := g.Generate(mesh, flatDensity(density))
				if err != nil {
					failures <- "generate: " + err.Error()
					return
				}
				if set.VertexCount == 0 || len(set.Layers) == 0 {
					failures <- "empty shell set"
					return
				}
				if _, err := g.Snapshot(); err != nil {
					failures <- "snapshot: " + err.Error()
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Fatalf("concurrent fur work failed: %s", msg)
	}
}
