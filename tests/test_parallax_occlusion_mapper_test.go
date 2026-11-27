package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-b-15"
)

func defaultPOMConfig() impl.POMConfig {
	return impl.POMConfig{
		MaxSteps:      16,
		MaxDepthScale: 1,
		MaxTexSize:    256,
		MaxVariants:   8,
		MaxLOD:        4,
		FadeNear:      2,
		FadeFar:       20,
		ParallelZ:     0.05,
	}
}

func newMapper(t *testing.T) *impl.ParallaxOcclusionMapper {
	t.Helper()
	m, err := impl.NewParallaxOcclusionMapper(defaultPOMConfig())
	if err != nil {
		t.Fatalf("new mapper: %v", err)
	}
	return m
}

func flatHeightMap(id string, w, h int, value float32) impl.HeightMap {
	values := make([]float32, w*h)
	for i := range values {
		values[i] = value
	}
	return impl.HeightMap{ID: id, Width: w, Height: h, Cols: 1, Rows: 1, Values: values}
}

func configuredMapper(t *testing.T) *impl.ParallaxOcclusionMapper {
	t.Helper()
	m := newMapper(t)
	if err := m.SetHeightMap("brick", flatHeightMap("brick", 2, 2, 0.5)); err != nil {
		t.Fatalf("set height map: %v", err)
	}
	if err := m.SetSteps(8); err != nil {
		t.Fatalf("set steps: %v", err)
	}
	if err := m.SetDepthScale(0.5); err != nil {
		t.Fatalf("set depth scale: %v", err)
	}
	if err := m.SetFadeRange(2, 20); err != nil {
		t.Fatalf("set fade range: %v", err)
	}
	return m
}

func TestSetHeightMapValidates(t *testing.T) {
	m := newMapper(t)
	if err := m.SetHeightMap("   ", flatHeightMap("brick", 2, 2, 0.5)); !errors.Is(err, impl.ErrBadMaterial) {
		t.Fatalf("blank id must fail with ErrBadMaterial, got %v", err)
	}
	bad := []impl.HeightMap{
		{ID: "brick", Width: 0, Height: 2, Values: []float32{0.5, 0.5}},
		{ID: "brick", Width: 2, Height: 2, Values: []float32{0.5, 0.5}},
		{ID: "brick", Width: 2, Height: 2, Values: []float32{0.5, 0.5, 0.5, 1.5}},
		{ID: "brick", Width: 2, Height: 2, Values: []float32{0.5, -0.1, 0.5, 0.5}},
		{ID: "brick", Width: 512, Height: 2, Values: make([]float32, 1024)},
	}
	for i, tex := range bad {
		if err := m.SetHeightMap("brick", tex); !errors.Is(err, impl.ErrBadHeightMap) {
			t.Fatalf("bad height map %d must fail with ErrBadHeightMap, got %v", i, err)
		}
	}
	if err := m.SetHeightMap("brick", flatHeightMap("brick", 2, 2, 0.5)); err != nil {
		t.Fatalf("well formed height map must be accepted, got %v", err)
	}
	if got := m.Stats().Materials; got != 1 {
		t.Fatalf("material count = %d, want 1", got)
	}
	if err := m.SetHeightMap("brick", flatHeightMap("brick", 4, 4, 0.25)); err != nil {
		t.Fatalf("replacing a height map must be accepted, got %v", err)
	}
	if got := m.Stats().Materials; got != 1 {
		t.Fatalf("material count after replace = %d, want 1", got)
	}
}

func TestSetStepsValidates(t *testing.T) {
	m := newMapper(t)
	for _, n := range []int{0, -3, 17} {
		if err := m.SetSteps(n); !errors.Is(err, impl.ErrBadSteps) {
			t.Fatalf("steps %d must fail with ErrBadSteps, got %v", n, err)
		}
	}
	if err := m.SetSteps(8); err != nil {
		t.Fatalf("steps 8 must be accepted, got %v", err)
	}
	if got := m.Steps(); got != 8 {
		t.Fatalf("steps = %d, want 8", got)
	}
	if err := m.SetMaxSteps(0); !errors.Is(err, impl.ErrBadSteps) {
		t.Fatalf("a zero step ceiling must fail with ErrBadSteps, got %v", err)
	}
}

func TestSetDepthScaleValidates(t *testing.T) {
	m := newMapper(t)
	for _, s := range []float32{0, -0.5, 1.5} {
		if err := m.SetDepthScale(s); !errors.Is(err, impl.ErrBadScale) {
			t.Fatalf("depth scale %v must fail with ErrBadScale, got %v", s, err)
		}
	}
	if err := m.SetDepthScale(0.5); err != nil {
		t.Fatalf("depth scale 0.5 must be accepted, got %v", err)
	}
	if got := m.DepthScale(); got != 0.5 {
		t.Fatalf("depth scale = %v, want 0.5", got)
	}
}

func TestSetFadeRangeValidates(t *testing.T) {
	m := newMapper(t)
	bad := [][2]float32{{5, 5}, {8, 4}, {-1, 4}}
	for i, pair := range bad {
		if err := m.SetFadeRange(pair[0], pair[1]); !errors.Is(err, impl.ErrBadFade) {
			t.Fatalf("fade range %d must fail with ErrBadFade, got %v", i, err)
		}
	}
	if err := m.SetFadeRange(2, 20); err != nil {
		t.Fatalf("fade range 2..20 must be accepted, got %v", err)
	}
}

func TestSampleUnknownMaterialFails(t *testing.T) {
	m := configuredMapper(t)
	if _, err := m.Sample(impl.MaterialRef{ID: "missing", Distance: 3}, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{Z: 1}); !errors.Is(err, impl.ErrUnknownMaterial) {
		t.Fatalf("an unknown material must fail with ErrUnknownMaterial, got %v", err)
	}
	if _, err := m.HeightAt(impl.MaterialRef{ID: "missing"}, impl.Vec2{U: 0.5, V: 0.5}); !errors.Is(err, impl.ErrUnknownMaterial) {
		t.Fatalf("HeightAt on an unknown material must fail with ErrUnknownMaterial, got %v", err)
	}
	if _, err := m.Sample(impl.MaterialRef{ID: "brick", Cell: 4, Distance: 3}, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{Z: 1}); !errors.Is(err, impl.ErrBadMaterial) {
		t.Fatalf("a cell outside the atlas grid must fail with ErrBadMaterial, got %v", err)
	}
}

func TestSampleRejectsDegenerateView(t *testing.T) {
	m := configuredMapper(t)
	if _, err := m.Sample(impl.MaterialRef{ID: "brick", Distance: 3}, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{}); !errors.Is(err, impl.ErrBadView) {
		t.Fatalf("a zero length view vector must fail with ErrBadView, got %v", err)
	}
}

func TestSampleAppliesParallaxOffset(t *testing.T) {
	m := configuredMapper(t)
	material := impl.MaterialRef{ID: "brick", Distance: 2}
	res, err := m.Sample(material, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{X: 0.5, Z: 0.8})
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	// offset = (0.5/0.8) * 0.5 * 1 * 0.5 = 0.15625, applied against the view x
	wantU := float32(0.5 - 0.15625)
	if diff := res.UV.U - wantU; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("displaced u = %v, want %v", res.UV.U, wantU)
	}
	if diff := res.UV.V - 0.5; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("displaced v = %v, want the untouched 0.5", res.UV.V)
	}
	if diff := res.Height - 0.5; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("sampled height = %v, want the flat 0.5", res.Height)
	}
	if diff := res.Fade - 1; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("fade at the near plane = %v, want 1", res.Fade)
	}
	if res.Steps != 8 {
		t.Fatalf("steps at full parallax weight = %d, want 8", res.Steps)
	}
	if res.Clipped {
		t.Fatalf("an interior sample must not be clipped")
	}
}

func TestHeightAtBilinearSampling(t *testing.T) {
	m := newMapper(t)
	tex := impl.HeightMap{ID: "checker", Width: 2, Height: 2, Cols: 1, Rows: 1, Values: []float32{0.2, 0.4, 0.6, 0.8}}
	if err := m.SetHeightMap("checker", tex); err != nil {
		t.Fatalf("set height map: %v", err)
	}
	material := impl.MaterialRef{ID: "checker"}
	cases := []struct {
		uv   impl.Vec2
		want float32
	}{
		{impl.Vec2{U: 0.25, V: 0.25}, 0.2},
		{impl.Vec2{U: 0.75, V: 0.25}, 0.4},
		{impl.Vec2{U: 0.25, V: 0.75}, 0.6},
		{impl.Vec2{U: 0.75, V: 0.75}, 0.8},
		{impl.Vec2{U: 0.5, V: 0.5}, 0.5},
	}
	for _, c := range cases {
		got, err := m.HeightAt(material, c.uv)
		if err != nil {
			t.Fatalf("height at %+v: %v", c.uv, err)
		}
		if diff := got - c.want; diff > 1e-5 || diff < -1e-5 {
			t.Fatalf("height at %+v = %v, want %v", c.uv, got, c.want)
		}
	}
}

func TestSampleStepsScaleWithDistance(t *testing.T) {
	m := configuredMapper(t)
	near := impl.MaterialRef{ID: "brick", Distance: 2}
	mid := impl.MaterialRef{ID: "brick", Distance: 11}
	far := impl.MaterialRef{ID: "brick", Distance: 20}
	nearRes, err := m.Sample(near, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{X: 0.5, Z: 0.8})
	if err != nil {
		t.Fatalf("near sample: %v", err)
	}
	if nearRes.Steps != 8 || nearRes.Fade != 1 {
		t.Fatalf("near sample steps=%d fade=%v, want 8 and 1", nearRes.Steps, nearRes.Fade)
	}
	midRes, err := m.Sample(mid, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{X: 0.5, Z: 0.8})
	if err != nil {
		t.Fatalf("mid sample: %v", err)
	}
	if midRes.Steps != 4 {
		t.Fatalf("mid sample steps = %d, want round(8*0.5) = 4", midRes.Steps)
	}
	if diff := midRes.Fade - 0.5; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("mid sample fade = %v, want (20-11)/(20-2) = 0.5", midRes.Fade)
	}
	farRes, err := m.Sample(far, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{X: 0.5, Z: 0.8})
	if err != nil {
		t.Fatalf("far sample: %v", err)
	}
	if farRes.Steps != 1 || farRes.Fade != 0 {
		t.Fatalf("far sample steps=%d fade=%v, want 1 and 0", farRes.Steps, farRes.Fade)
	}
	if diff := farRes.UV.U - 0.5; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("far sample u = %v, want the base uv once parallax fades out", farRes.UV.U)
	}
}

func TestSampleCountsStats(t *testing.T) {
	m := configuredMapper(t)
	material := impl.MaterialRef{ID: "brick", Distance: 2}
	total := 0
	for i := 0; i < 3; i++ {
		res, err := m.Sample(material, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{X: 0.5, Z: 0.8})
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		total += res.Steps
	}
	st := m.Stats()
	if st.Samples != 3 {
		t.Fatalf("sample counter = %d, want 3", st.Samples)
	}
	if st.StepTotal != total {
		t.Fatalf("step total = %d, want the %d steps spent", st.StepTotal, total)
	}
	if total != 24 {
		t.Fatalf("three full weight samples spent %d steps, want 24", total)
	}
	if st.Materials != 1 {
		t.Fatalf("material counter = %d, want 1", st.Materials)
	}
}

func TestSetMaxStepsClampsSteps(t *testing.T) {
	m := configuredMapper(t)
	if err := m.SetMaxSteps(4); err != nil {
		t.Fatalf("set max steps: %v", err)
	}
	if got := m.Steps(); got != 4 {
		t.Fatalf("effective steps after the ceiling = %d, want 4", got)
	}
	res, err := m.Sample(impl.MaterialRef{ID: "brick", Distance: 2}, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{X: 0.5, Z: 0.8})
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if res.Steps != 4 {
		t.Fatalf("sample steps = %d, want the clamped 4", res.Steps)
	}
	if err := m.SetMaxSteps(32); err != nil {
		t.Fatalf("raising the ceiling must be accepted, got %v", err)
	}
	if got := m.Steps(); got != 4 {
		t.Fatalf("steps = %d, want the authored 4 after the ceiling was raised", got)
	}
}

func TestBuildVariantMergesConcurrentBuilds(t *testing.T) {
	m := configuredMapper(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]impl.POMVariant, 0, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := m.BuildVariant(2)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				results = append(results, impl.POMVariant{LOD: -1})
				return
			}
			results = append(results, v)
		}()
	}
	wg.Wait()
	if len(results) != 8 {
		t.Fatalf("variant results = %d, want 8", len(results))
	}
	first := results[0]
	if first.LOD != 2 {
		t.Fatalf("variant lod = %d, want 2", first.LOD)
	}
	for i, v := range results {
		if v != first {
			t.Fatalf("variant %d = %+v, want the same merged variant %+v", i, v, first)
		}
	}
	st := m.Stats()
	if st.Builds != 1 {
		t.Fatalf("variant builds = %d, want exactly one real build", st.Builds)
	}
	if st.Merged != 7 {
		t.Fatalf("merged build calls = %d, want 7", st.Merged)
	}
	if st.Variants != 1 {
		t.Fatalf("cached variants = %d, want 1", st.Variants)
	}
	cached, ok := m.Variant(2)
	if !ok {
		t.Fatalf("the built variant must be readable")
	}
	if cached != first {
		t.Fatalf("cached variant = %+v, want %+v", cached, first)
	}
}

func TestBuildVariantValidatesLOD(t *testing.T) {
	m := configuredMapper(t)
	for _, lod := range []int{-1, 5} {
		if _, err := m.BuildVariant(lod); !errors.Is(err, impl.ErrBadLOD) {
			t.Fatalf("lod %d must fail with ErrBadLOD, got %v", lod, err)
		}
	}
	if _, err := m.BuildVariant(4); err != nil {
		t.Fatalf("the top lod must be accepted, got %v", err)
	}
	empty := newMapper(t)
	if _, err := empty.BuildVariant(0); !errors.Is(err, impl.ErrBadMaterial) {
		t.Fatalf("building before any height map must fail with ErrBadMaterial, got %v", err)
	}
}

func TestSnapshotRestoreKeepsVariantsAndFade(t *testing.T) {
	m := configuredMapper(t)
	if _, err := m.BuildVariant(1); err != nil {
		t.Fatalf("build variant: %v", err)
	}
	before, err := m.Sample(impl.MaterialRef{ID: "brick", Distance: 11}, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{X: 0.5, Z: 0.8})
	if err != nil {
		t.Fatalf("sample before the snapshot: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored, err := impl.NewParallaxOcclusionMapper(defaultPOMConfig())
	if err != nil {
		t.Fatalf("new restored mapper: %v", err)
	}
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Steps(); got != 8 {
		t.Fatalf("restored steps = %d, want 8", got)
	}
	if got := restored.DepthScale(); got != 0.5 {
		t.Fatalf("restored depth scale = %v, want 0.5", got)
	}
	if _, ok := restored.Variant(1); !ok {
		t.Fatalf("restored mapper lost the cached variant")
	}
	after, err := restored.Sample(impl.MaterialRef{ID: "brick", Distance: 11}, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{X: 0.5, Z: 0.8})
	if err != nil {
		t.Fatalf("sample after restore: %v", err)
	}
	if after != before {
		t.Fatalf("restored sample = %+v, want %+v", after, before)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	m := configuredMapper(t)
	if err := m.Restore([]byte("{\"materials\":")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := m.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := m.Steps(); got != 8 {
		t.Fatalf("live steps after a rejected restore = %d, want 8", got)
	}
	if _, err := m.Sample(impl.MaterialRef{ID: "brick", Distance: 2}, impl.Vec2{U: 0.5, V: 0.5}, impl.Vec3{X: 0.5, Z: 0.8}); err != nil {
		t.Fatalf("the live mapper must keep sampling, got %v", err)
	}
}
