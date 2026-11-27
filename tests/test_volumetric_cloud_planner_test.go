package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch51-go-game-b-02"
)

func baseCloudConfig() impl.CloudConfig {
	return impl.CloudConfig{
		BaseAltitude:      100,
		TopAltitude:       200,
		CoverageThreshold: 0.4,
		MaxSteps:          64,
		PlanRows:          8,
		ShadowBuckets:     4,
		AngleToleranceDeg: 15,
	}
}

func flatWeather(v float64) impl.WeatherMap {
	return impl.WeatherMap{
		Width:    2,
		Height:   2,
		ExtentX:  100,
		ExtentY:  100,
		Coverage: []float64{v, v, v, v},
	}
}

func cloudCamera() impl.Camera {
	return impl.Camera{
		Position: impl.Vec3{X: 50, Y: 50, Z: 0},
		Forward:  impl.Vec3{Z: 1},
		FovDeg:   60,
	}
}

func closeTo(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

func TestBuildProducesVolume(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	vol, err := c.Build(flatWeather(1), cloudCamera())
	if err != nil {
		t.Fatalf("build with a valid weather map must succeed, got %v", err)
	}
	if vol.Bounds.Min != (impl.Vec3{X: 0, Y: 0, Z: 100}) {
		t.Fatalf("bounds min = %+v, want (0,0,100)", vol.Bounds.Min)
	}
	if vol.Bounds.Max != (impl.Vec3{X: 100, Y: 100, Z: 200}) {
		t.Fatalf("bounds max = %+v, want (100,100,200)", vol.Bounds.Max)
	}
	if !closeTo(vol.Coverage, 1) {
		t.Fatalf("volume coverage = %v, want the weather mean 1", vol.Coverage)
	}
	if !closeTo(vol.Threshold, 0.4) {
		t.Fatalf("volume threshold = %v, want 0.4", vol.Threshold)
	}
	if vol.Resolution != 4 || len(vol.Cells) != 64 {
		t.Fatalf("resolution=%d cells=%d, want 4 and 64", vol.Resolution, len(vol.Cells))
	}
	for i, v := range vol.Cells {
		if v < 0 || v > 1 {
			t.Fatalf("cell %d = %v, want a density in [0,1]", i, v)
		}
	}
	bounds, ok := c.CloudBounds()
	if !ok || bounds != vol.Bounds {
		t.Fatalf("cloud bounds = %+v ok=%v, want %+v", bounds, ok, vol.Bounds)
	}
}

func TestBuildRejectsBadConfig(t *testing.T) {
	cases := map[string]func(*impl.CloudConfig){
		"empty band":         func(c *impl.CloudConfig) { c.TopAltitude = c.BaseAltitude },
		"inverted band":      func(c *impl.CloudConfig) { c.TopAltitude = 50 },
		"threshold high":     func(c *impl.CloudConfig) { c.CoverageThreshold = 1.5 },
		"threshold low":      func(c *impl.CloudConfig) { c.CoverageThreshold = -0.2 },
		"no steps":           func(c *impl.CloudConfig) { c.MaxSteps = 0 },
		"no rows":            func(c *impl.CloudConfig) { c.PlanRows = 0 },
		"no buckets":         func(c *impl.CloudConfig) { c.ShadowBuckets = 0 },
		"no angle tolerance": func(c *impl.CloudConfig) { c.AngleToleranceDeg = 0 },
	}
	for name, mutate := range cases {
		cfg := baseCloudConfig()
		mutate(&cfg)
		c := impl.NewCloudVolume(cfg)
		if _, err := c.Build(flatWeather(1), cloudCamera()); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("%s: build error = %v, want ErrBadConfig", name, err)
		}
		if _, ok := c.CloudBounds(); ok {
			t.Fatalf("%s: rejected build must not expose bounds", name)
		}
	}
}

func TestBuildRejectsBadWeather(t *testing.T) {
	cases := map[string]impl.WeatherMap{
		"short grid":        {Width: 2, Height: 2, ExtentX: 10, ExtentY: 10, Coverage: []float64{1, 1, 1}},
		"zero width":        {Width: 0, Height: 2, ExtentX: 10, ExtentY: 10, Coverage: []float64{1, 1}},
		"zero extent":       {Width: 2, Height: 2, ExtentX: 0, ExtentY: 10, Coverage: []float64{1, 1, 1, 1}},
		"coverage over one": {Width: 2, Height: 2, ExtentX: 10, ExtentY: 10, Coverage: []float64{1, 1, 1, 1.2}},
	}
	for name, weather := range cases {
		c := impl.NewCloudVolume(baseCloudConfig())
		if _, err := c.Build(weather, cloudCamera()); !errors.Is(err, impl.ErrBadWeather) {
			t.Fatalf("%s: build error = %v, want ErrBadWeather", name, err)
		}
	}
}

func TestCoverageBilinearInterpolates(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	weather := impl.WeatherMap{
		Width: 2, Height: 2, ExtentX: 10, ExtentY: 10,
		Coverage: []float64{0, 1, 1, 0},
	}
	if _, err := c.Build(weather, cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	corners := []struct {
		x, y float64
		want float64
	}{
		{0, 0, 0},
		{10, 0, 1},
		{0, 10, 1},
		{10, 10, 0},
		{5, 5, 0.5},
		{-5, 5, 0.5},
		{15, 5, 0.5},
	}
	for _, tc := range corners {
		got, err := c.CoverageAt(tc.x, tc.y)
		if err != nil {
			t.Fatalf("coverage at (%v,%v): %v", tc.x, tc.y, err)
		}
		if !closeTo(got, tc.want) {
			t.Fatalf("coverage at (%v,%v) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
}

func TestDensityZeroOutsideAltitudeBand(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	if _, err := c.Build(flatWeather(1), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, z := range []float64{99.9, 100, 200, 200.1, 260} {
		got, err := c.DensityAt(impl.Vec3{X: 50, Y: 50, Z: z})
		if err != nil {
			t.Fatalf("density at z=%v: %v", z, err)
		}
		if got != 0 {
			t.Fatalf("density at z=%v = %v, want the hard boundary value 0", z, got)
		}
	}
	inside, err := c.DensityAt(impl.Vec3{X: 50, Y: 50, Z: 150})
	if err != nil {
		t.Fatalf("density inside the band: %v", err)
	}
	if inside <= 0 {
		t.Fatalf("density inside a fully covered band = %v, want a positive value", inside)
	}
}

func TestDensityHonoursCoverageThreshold(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	if _, err := c.Build(flatWeather(0.2), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	got, err := c.DensityAt(impl.Vec3{X: 50, Y: 50, Z: 150})
	if err != nil {
		t.Fatalf("density: %v", err)
	}
	if got != 0 {
		t.Fatalf("density under a coverage of 0.2 with threshold 0.4 = %v, want 0", got)
	}
	cov, err := c.CoverageAt(50, 50)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if !closeTo(cov, 0.2) {
		t.Fatalf("coverage = %v, want the raw weather value 0.2", cov)
	}
}

func TestDensityIsDeterministicAndVaries(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	if _, err := c.Build(flatWeather(1), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	pos := impl.Vec3{X: 37, Y: 61, Z: 143}
	first, err := c.DensityAt(pos)
	if err != nil {
		t.Fatalf("density: %v", err)
	}
	second, err := c.DensityAt(pos)
	if err != nil {
		t.Fatalf("density: %v", err)
	}
	if first != second {
		t.Fatalf("density at one position changed between calls: %v then %v", first, second)
	}
	seen := map[float64]bool{}
	for x := 5.0; x < 100; x += 25 {
		for y := 5.0; y < 100; y += 25 {
			for z := 105.0; z < 200; z += 25 {
				v, err := c.DensityAt(impl.Vec3{X: x, Y: y, Z: z})
				if err != nil {
					t.Fatalf("density: %v", err)
				}
				if v < 0 || v > 1 {
					t.Fatalf("density %v at (%v,%v,%v) leaves [0,1]", v, x, y, z)
				}
				seen[v] = true
			}
		}
	}
	if len(seen) < 2 {
		t.Fatalf("density field is flat: %d distinct values over the lattice", len(seen))
	}
}

func TestPlanStepsRespectsBudget(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	if _, err := c.Build(flatWeather(1), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	plan := c.PlanSteps(impl.Viewport{Width: 1920, Height: 1080}, 100)
	if len(plan) != 8 {
		t.Fatalf("plan rows = %d, want 8", len(plan))
	}
	total := 0
	prev := -1
	for i, step := range plan {
		if step.Row != i {
			t.Fatalf("plan entry %d carries row %d", i, step.Row)
		}
		if step.Steps < 1 {
			t.Fatalf("row %d got %d steps, every planned row needs at least one", i, step.Steps)
		}
		if prev >= 0 && step.Steps > prev {
			t.Fatalf("row %d steps %d exceed the nearer row %d, near rows must get more work", i, step.Steps, prev)
		}
		prev = step.Steps
		total += step.Steps
		if !closeTo(step.Start, float64(i)/8) || !closeTo(step.End, float64(i+1)/8) {
			t.Fatalf("row %d range = [%v,%v], want [%v,%v]", i, step.Start, step.End, float64(i)/8, float64(i+1)/8)
		}
	}
	if total != 100 {
		t.Fatalf("planned steps = %d, want the whole budget 100", total)
	}
	if plan[0].Steps <= plan[7].Steps {
		t.Fatalf("nearest row steps %d must exceed farthest row steps %d", plan[0].Steps, plan[7].Steps)
	}
}

func TestPlanStepsTightBudget(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	if got := c.PlanSteps(impl.Viewport{Width: 640, Height: 480}, 10); len(got) != 0 {
		t.Fatalf("plan before build = %v, want none", got)
	}
	if _, err := c.Build(flatWeather(1), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	plan := c.PlanSteps(impl.Viewport{Width: 640, Height: 480}, 3)
	if len(plan) != 8 {
		t.Fatalf("plan rows = %d, want 8", len(plan))
	}
	total := 0
	for i, step := range plan {
		want := 0
		if i < 3 {
			want = 1
		}
		if step.Steps != want {
			t.Fatalf("row %d got %d steps with a budget of 3 over 8 rows, want %d", i, step.Steps, want)
		}
		total += step.Steps
	}
	if total != 3 {
		t.Fatalf("planned steps = %d, want the tight budget 3", total)
	}
}

func TestPlanStepsUsesDefaultBudget(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	if _, err := c.Build(flatWeather(1), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	plan := c.PlanSteps(impl.Viewport{Width: 800, Height: 600}, 0)
	if len(plan) != 8 {
		t.Fatalf("plan rows = %d, want 8", len(plan))
	}
	total := 0
	for _, step := range plan {
		if step.Steps < 1 {
			t.Fatalf("row %d got %d steps with the default budget", step.Row, step.Steps)
		}
		total += step.Steps
	}
	if total != 64 {
		t.Fatalf("planned steps = %d, want the configured MaxSteps 64", total)
	}
}

func TestSampleShadowFullTransmittanceOutside(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	if _, err := c.SampleShadow(impl.Vec3{X: 50, Y: 50, Z: 300}, impl.Vec3{Z: 1}); !errors.Is(err, impl.ErrNotBuilt) {
		t.Fatalf("shadow sample before build must fail with ErrNotBuilt, got %v", err)
	}
	if _, err := c.Build(flatWeather(1), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, err := c.SampleShadow(impl.Vec3{X: 50, Y: 50, Z: 300}, impl.Vec3{}); !errors.Is(err, impl.ErrBadLight) {
		t.Fatalf("zero light direction must fail with ErrBadLight, got %v", err)
	}
	got, err := c.SampleShadow(impl.Vec3{X: 50, Y: 50, Z: 300}, impl.Vec3{Z: 1})
	if err != nil {
		t.Fatalf("shadow sample above the band: %v", err)
	}
	if got != 1 {
		t.Fatalf("transmittance above the cloud = %v, want 1", got)
	}
}

func TestSampleShadowAttenuatesInside(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	if _, err := c.Build(flatWeather(1), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	got, err := c.SampleShadow(impl.Vec3{X: 50, Y: 50, Z: 150}, impl.Vec3{Z: 1})
	if err != nil {
		t.Fatalf("shadow sample inside the cloud: %v", err)
	}
	if got <= 0 || got >= 1 {
		t.Fatalf("transmittance inside a dense cloud = %v, want a value in (0,1)", got)
	}
}

func TestShadowCacheReusesSameDirection(t *testing.T) {
	c := impl.NewCloudVolume(baseCloudConfig())
	if _, err := c.Build(flatWeather(1), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	pos := impl.Vec3{X: 20, Y: 30, Z: 140}
	dir := impl.Vec3{X: 0.2, Y: 0, Z: 1}
	first, err := c.SampleShadow(pos, dir)
	if err != nil {
		t.Fatalf("first shadow sample: %v", err)
	}
	stats := c.ShadowCache()
	if stats.Misses != 1 || stats.Hits != 0 {
		t.Fatalf("after one sample hits=%d misses=%d, want 0 and 1", stats.Hits, stats.Misses)
	}
	second, err := c.SampleShadow(pos, dir)
	if err != nil {
		t.Fatalf("second shadow sample: %v", err)
	}
	if !closeTo(first, second) {
		t.Fatalf("cached transmittance = %v, want the first value %v", second, first)
	}
	stats = c.ShadowCache()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("after a repeated sample hits=%d misses=%d, want 1 and 1", stats.Hits, stats.Misses)
	}
}

func TestInvalidateRegionForcesResample(t *testing.T) {
	fresh := impl.NewCloudVolume(baseCloudConfig())
	if err := fresh.InvalidateRegion(impl.Bounds{Min: impl.Vec3{}, Max: impl.Vec3{X: 1, Y: 1, Z: 1}}); !errors.Is(err, impl.ErrNotBuilt) {
		t.Fatalf("invalidate before build must fail with ErrNotBuilt, got %v", err)
	}
	c := impl.NewCloudVolume(baseCloudConfig())
	if _, err := c.Build(flatWeather(1), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	pos := impl.Vec3{X: 25, Y: 25, Z: 125}
	dir := impl.Vec3{Z: 1}
	if _, err := c.SampleShadow(pos, dir); err != nil {
		t.Fatalf("shadow sample: %v", err)
	}
	box := impl.Bounds{
		Min: impl.Vec3{X: 0, Y: 0, Z: 100},
		Max: impl.Vec3{X: 50, Y: 50, Z: 150},
	}
	if err := c.InvalidateRegion(box); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if got := c.ShadowCache().Invalidated; got < 1 {
		t.Fatalf("invalidated entries = %d, want at least the cached sample", got)
	}
	if _, err := c.SampleShadow(pos, dir); err != nil {
		t.Fatalf("shadow sample after invalidate: %v", err)
	}
	stats := c.ShadowCache()
	if stats.Misses != 2 || stats.Hits != 0 {
		t.Fatalf("after invalidation hits=%d misses=%d, want 0 and 2", stats.Hits, stats.Misses)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	cfg := baseCloudConfig()
	c := impl.NewCloudVolume(cfg)
	if _, err := c.Build(flatWeather(0.8), cloudCamera()); err != nil {
		t.Fatalf("build: %v", err)
	}
	before, err := c.SampleShadow(impl.Vec3{X: 40, Y: 40, Z: 130}, impl.Vec3{Z: 1})
	if err != nil {
		t.Fatalf("shadow sample: %v", err)
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewCloudVolume(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	cov, err := restored.CoverageAt(40, 40)
	if err != nil {
		t.Fatalf("coverage after restore: %v", err)
	}
	if !closeTo(cov, 0.8) {
		t.Fatalf("restored coverage = %v, want 0.8", cov)
	}
	after, err := restored.SampleShadow(impl.Vec3{X: 40, Y: 40, Z: 130}, impl.Vec3{Z: 1})
	if err != nil {
		t.Fatalf("shadow sample after restore: %v", err)
	}
	if !closeTo(before, after) {
		t.Fatalf("restored transmittance = %v, want %v", after, before)
	}
	if err := restored.Restore([]byte("clouds")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	cov, err = restored.CoverageAt(40, 40)
	if err != nil || !closeTo(cov, 0.8) {
		t.Fatalf("failed restore changed the live volume: coverage=%v err=%v", cov, err)
	}
}
