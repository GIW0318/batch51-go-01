package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch51-go-game-b-18"
)

func testCamera() impl.Camera {
	return impl.Camera{
		Position:  impl.Vec3{},
		Forward:   impl.Vec3{X: 0, Y: 0, Z: 1},
		Up:        impl.Vec3{X: 0, Y: 1, Z: 0},
		FovDeg:    90,
		ViewportW: 1920,
		ViewportH: 1080,
	}
}

func onAxisSource(brightness float32) impl.FlareSource {
	return impl.FlareSource{
		Position:   impl.Vec3{X: 0, Y: 0, Z: 10},
		Brightness: brightness,
		Radius:     1,
		Group:      "sun",
	}
}

func newScheduler(limits impl.FlareLimits) *impl.LensFlareScheduler {
	return impl.NewLensFlareScheduler(limits)
}

func mustPlan(t *testing.T, s *impl.LensFlareScheduler, cam impl.Camera) []impl.FlareInstance {
	t.Helper()
	instances, err := s.Plan(cam)
	if err != nil {
		t.Fatalf("Plan must succeed, got %v", err)
	}
	return instances
}

func near(got, want float32, tol float64) bool {
	return math.Abs(float64(got-want)) <= tol
}

func TestRegisterSourceValidation(t *testing.T) {
	s := newScheduler(impl.FlareLimits{MaxSources: 2})
	if err := s.RegisterSource("", onAxisSource(1)); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("empty id = %v, want ErrBadSource", err)
	}
	if err := s.RegisterSource("dark", impl.FlareSource{Position: impl.Vec3{Z: 10}, Brightness: 0, Radius: 1}); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("zero brightness = %v, want ErrBadSource", err)
	}
	if err := s.RegisterSource("thin", impl.FlareSource{Position: impl.Vec3{Z: 10}, Brightness: 1, Radius: -1}); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("negative radius = %v, want ErrBadSource", err)
	}
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	if err := s.RegisterSource("sun", onAxisSource(2)); !errors.Is(err, impl.ErrSourceExists) {
		t.Fatalf("duplicate id = %v, want ErrSourceExists", err)
	}
	if err := s.RegisterSource("moon", onAxisSource(1)); err != nil {
		t.Fatalf("second source must be accepted, got %v", err)
	}
	if err := s.RegisterSource("extra", onAxisSource(1)); !errors.Is(err, impl.ErrSourceLimit) {
		t.Fatalf("source over the cap = %v, want ErrSourceLimit", err)
	}
	if got := s.SourceCount(); got != 2 {
		t.Fatalf("source count = %d, want 2", got)
	}
}

func TestUpdateSourceReplacesAndValidates(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.UpdateSource("ghost", onAxisSource(1)); !errors.Is(err, impl.ErrUnknownSource) {
		t.Fatalf("UpdateSource on an unknown id = %v, want ErrUnknownSource", err)
	}
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	if err := s.UpdateSource("sun", impl.FlareSource{Position: impl.Vec3{Z: 10}, Brightness: -2, Radius: 1}); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("UpdateSource with a negative brightness = %v, want ErrBadSource", err)
	}
	before := mustPlan(t, s, testCamera())
	if len(before) != 1 {
		t.Fatalf("instances before the update = %d, want 1", len(before))
	}
	if err := s.UpdateSource("sun", onAxisSource(4)); err != nil {
		t.Fatalf("UpdateSource must succeed, got %v", err)
	}
	after := mustPlan(t, s, testCamera())
	if len(after) != 1 {
		t.Fatalf("instances after the update = %d, want 1", len(after))
	}
	if !(after[0].Scale > before[0].Scale) {
		t.Fatalf("scale after raising the brightness = %v, want more than %v", after[0].Scale, before[0].Scale)
	}
	if got := s.SourceCount(); got != 1 {
		t.Fatalf("source count = %d, want 1", got)
	}
}

func TestPlanProjectsOnAxisSourceToCenter(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.RegisterSource("sun", onAxisSource(2)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	instances := mustPlan(t, s, testCamera())
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	got := instances[0]
	if got.ID != "sun" {
		t.Fatalf("instance id = %q, want sun", got.ID)
	}
	if !near(got.NdcX, 0, 1e-4) || !near(got.NdcY, 0, 1e-4) {
		t.Fatalf("source on the view axis projected to (%v,%v), want the screen centre", got.NdcX, got.NdcY)
	}
	if !near(got.Intensity, impl.DefaultFadeIn, 1e-5) {
		t.Fatalf("first frame intensity = %v, want %v", got.Intensity, impl.DefaultFadeIn)
	}
	if !near(got.Scale, 0.2, 1e-5) {
		t.Fatalf("scale = %v, want brightness times radius over depth = 0.2", got.Scale)
	}
	if got.Order != 0 {
		t.Fatalf("order = %d, want 0", got.Order)
	}
}

func TestOffscreenSourceFadesOutButKeepsState(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	cam := testCamera()
	for i := 0; i < 4; i++ {
		mustPlan(t, s, cam)
	}
	if got := s.Intensity("sun"); !near(got, 1, 1e-5) {
		t.Fatalf("intensity after four visible frames = %v, want 1", got)
	}
	moved := onAxisSource(1)
	moved.Position = impl.Vec3{X: 4000, Y: 0, Z: 10}
	if err := s.UpdateSource("sun", moved); err != nil {
		t.Fatalf("UpdateSource must succeed, got %v", err)
	}
	instances := mustPlan(t, s, cam)
	if len(instances) != 0 {
		t.Fatalf("off screen source produced %d instances, want 0", len(instances))
	}
	if got := s.Intensity("sun"); !near(got, 1-impl.DefaultFadeOut, 1e-5) {
		t.Fatalf("intensity one frame after leaving the viewport = %v, want %v", got, 1-impl.DefaultFadeOut)
	}
	previous := s.Intensity("sun")
	for i := 0; i < 4; i++ {
		mustPlan(t, s, cam)
		current := s.Intensity("sun")
		if current > previous+1e-6 {
			t.Fatalf("intensity rose while off screen: %v after %v", current, previous)
		}
		previous = current
	}
	if got := s.Intensity("sun"); got > 1e-3 {
		t.Fatalf("intensity after five off screen frames = %v, want 0", got)
	}
	if got := s.SourceCount(); got != 1 {
		t.Fatalf("off screen source was dropped, source count = %d, want 1", got)
	}
}

func TestFadeInRampsFromZero(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	cam := testCamera()
	first := mustPlan(t, s, cam)
	if len(first) != 1 || !near(first[0].Intensity, impl.DefaultFadeIn, 1e-5) {
		t.Fatalf("first plan = %+v, want a single instance at %v", first, impl.DefaultFadeIn)
	}
	second := mustPlan(t, s, cam)
	if len(second) != 1 || !near(second[0].Intensity, 2*impl.DefaultFadeIn, 1e-5) {
		t.Fatalf("second plan = %+v, want a single instance at %v", second, 2*impl.DefaultFadeIn)
	}
	for i := 0; i < 8; i++ {
		mustPlan(t, s, cam)
	}
	if got := s.Intensity("sun"); !near(got, 1, 1e-5) {
		t.Fatalf("intensity must saturate at 1, got %v", got)
	}
}

func TestSetFadeRatesValidatesAndApplies(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.SetFadeRates(0, 0.2); !errors.Is(err, impl.ErrBadFade) {
		t.Fatalf("zero fade in = %v, want ErrBadFade", err)
	}
	if err := s.SetFadeRates(0.5, 1.5); !errors.Is(err, impl.ErrBadFade) {
		t.Fatalf("fade out above one = %v, want ErrBadFade", err)
	}
	if err := s.SetFadeRates(0.5, 0.1); err != nil {
		t.Fatalf("SetFadeRates must succeed, got %v", err)
	}
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	instances := mustPlan(t, s, testCamera())
	if len(instances) != 1 || !near(instances[0].Intensity, 0.5, 1e-5) {
		t.Fatalf("first plan with fade in 0.5 = %+v, want intensity 0.5", instances)
	}
}

func TestInstanceCapTruncatesByScoreThenID(t *testing.T) {
	s := newScheduler(impl.FlareLimits{MaxInstances: 2})
	if err := s.SetMaxInstances(0); !errors.Is(err, impl.ErrBadLimit) {
		t.Fatalf("SetMaxInstances(0) = %v, want ErrBadLimit", err)
	}
	for id, brightness := range map[string]float32{"a": 1, "b": 2, "c": 3} {
		if err := s.RegisterSource(id, onAxisSource(brightness)); err != nil {
			t.Fatalf("RegisterSource(%q) must succeed, got %v", id, err)
		}
	}
	instances := mustPlan(t, s, testCamera())
	if len(instances) != 2 {
		t.Fatalf("instances = %d, want 2", len(instances))
	}
	if instances[0].ID != "c" || instances[1].ID != "b" {
		t.Fatalf("kept %q and %q, want the two brightest sources c and b", instances[0].ID, instances[1].ID)
	}
	if instances[0].Order != 0 || instances[1].Order != 1 {
		t.Fatalf("orders = %d,%d, want 0,1", instances[0].Order, instances[1].Order)
	}
	tied := newScheduler(impl.FlareLimits{MaxInstances: 1})
	for _, id := range []string{"bbb", "aaa"} {
		if err := tied.RegisterSource(id, onAxisSource(1)); err != nil {
			t.Fatalf("RegisterSource(%q) must succeed, got %v", id, err)
		}
	}
	kept := mustPlan(t, tied, testCamera())
	if len(kept) != 1 || kept[0].ID != "aaa" {
		t.Fatalf("tie on equal scores kept %+v, want aaa first by id", kept)
	}
}

func TestPriorityReportsLastPlanOrder(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.RegisterSource("bright", onAxisSource(5)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	if err := s.RegisterSource("dim", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	mustPlan(t, s, testCamera())
	if got := s.Priority("bright"); got != 0 {
		t.Fatalf("priority of the brightest source = %d, want 0", got)
	}
	if got := s.Priority("dim"); got != 1 {
		t.Fatalf("priority of the dim source = %d, want 1", got)
	}
	if got := s.Priority("ghost"); got >= 0 {
		t.Fatalf("priority of an unknown source = %d, want a negative value", got)
	}
}

func TestQueryBudgetDefersToNextFrame(t *testing.T) {
	s := newScheduler(impl.FlareLimits{MaxQueriesPerFrame: 1})
	for _, id := range []string{"left", "right"} {
		src := onAxisSource(1)
		src.Position = impl.Vec3{X: 1, Y: 0, Z: 10}
		if err := s.RegisterSource(id, src); err != nil {
			t.Fatalf("RegisterSource(%q) must succeed, got %v", id, err)
		}
	}
	cam := testCamera()
	instances := mustPlan(t, s, cam)
	if len(instances) != 2 {
		t.Fatalf("instances = %d, want both sources to keep their last state", len(instances))
	}
	st := s.Stats()
	if st.Queries != 1 {
		t.Fatalf("queries on the first frame = %d, want 1", st.Queries)
	}
	if st.Deferred != 1 {
		t.Fatalf("deferred sources = %d, want 1", st.Deferred)
	}
	mustPlan(t, s, cam)
	if st = s.Stats(); st.Queries != 1 {
		t.Fatalf("queries on the second frame = %d, want the deferred source to be served", st.Queries)
	}
}

func TestOcclusionResultFadesOutGradually(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.RegisterSource("lamp", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	cam := testCamera()
	for i := 0; i < 4; i++ {
		mustPlan(t, s, cam)
	}
	if got := s.Intensity("lamp"); !near(got, 1, 1e-5) {
		t.Fatalf("intensity before the occlusion report = %v, want 1", got)
	}
	if err := s.SetOcclusionResult("lamp", 0); err != nil {
		t.Fatalf("SetOcclusionResult must succeed, got %v", err)
	}
	instances := mustPlan(t, s, cam)
	if len(instances) != 1 {
		t.Fatalf("instances right after the occlusion report = %d, want the flare to fade instead of vanish", len(instances))
	}
	if !(instances[0].Intensity < 1) {
		t.Fatalf("intensity right after the occlusion report = %v, want a fade out step", instances[0].Intensity)
	}
	if !near(instances[0].Intensity, 1-impl.DefaultFadeOut, 1e-5) {
		t.Fatalf("intensity after one occluded frame = %v, want %v", instances[0].Intensity, 1-impl.DefaultFadeOut)
	}
	for i := 0; i < 4; i++ {
		mustPlan(t, s, cam)
	}
	if got := s.Intensity("lamp"); got > 1e-3 {
		t.Fatalf("intensity after five occluded frames = %v, want 0", got)
	}
}

func TestSetOcclusionResultValidation(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.SetOcclusionResult("ghost", 1); !errors.Is(err, impl.ErrUnknownSource) {
		t.Fatalf("occlusion for an unknown source = %v, want ErrUnknownSource", err)
	}
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	if err := s.SetOcclusionResult("sun", 1.5); !errors.Is(err, impl.ErrBadOcclusion) {
		t.Fatalf("visibility 1.5 = %v, want ErrBadOcclusion", err)
	}
	if err := s.SetOcclusionResult("sun", -0.1); !errors.Is(err, impl.ErrBadOcclusion) {
		t.Fatalf("visibility -0.1 = %v, want ErrBadOcclusion", err)
	}
	if err := s.SetOcclusionResult("sun", 1); err != nil {
		t.Fatalf("visibility 1 must be accepted, got %v", err)
	}
}

func TestPlanRejectsBadCamera(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	cases := []struct {
		name string
		cam  impl.Camera
	}{
		{"zero fov", func() impl.Camera { c := testCamera(); c.FovDeg = 0; return c }()},
		{"zero viewport width", func() impl.Camera { c := testCamera(); c.ViewportW = 0; return c }()},
		{"zero viewport height", func() impl.Camera { c := testCamera(); c.ViewportH = 0; return c }()},
		{"zero forward", func() impl.Camera { c := testCamera(); c.Forward = impl.Vec3{}; return c }()},
	}
	for _, tc := range cases {
		if _, err := s.Plan(tc.cam); !errors.Is(err, impl.ErrBadCamera) {
			t.Fatalf("%s: Plan error = %v, want ErrBadCamera", tc.name, err)
		}
	}
	mustPlan(t, s, testCamera())
}

func TestSnapshotRestorePreservesFade(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	cam := testCamera()
	mustPlan(t, s, cam)
	mustPlan(t, s, cam)
	if got := s.Intensity("sun"); !near(got, 0.5, 1e-5) {
		t.Fatalf("intensity before the snapshot = %v, want 0.5", got)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot must succeed, got %v", err)
	}
	other := newScheduler(impl.FlareLimits{})
	if err := other.Restore(data); err != nil {
		t.Fatalf("Restore must succeed, got %v", err)
	}
	if got := other.SourceCount(); got != 1 {
		t.Fatalf("source count after Restore = %d, want 1", got)
	}
	instances := mustPlan(t, other, cam)
	if len(instances) != 1 || !near(instances[0].Intensity, 0.75, 1e-5) {
		t.Fatalf("first plan after Restore = %+v, want intensity 0.75", instances)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.Restore([]byte("not a flare scheduler snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("Restore of a corrupt payload = %v, want ErrBadSnapshot", err)
	}
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("scheduler must stay usable after a failed Restore, got %v", err)
	}
	if got := s.SourceCount(); got != 1 {
		t.Fatalf("source count after a failed Restore = %d, want 1", got)
	}
}

func TestStatsCountsFrameWork(t *testing.T) {
	s := newScheduler(impl.FlareLimits{})
	if err := s.RegisterSource("sun", onAxisSource(1)); err != nil {
		t.Fatalf("RegisterSource must succeed, got %v", err)
	}
	mustPlan(t, s, testCamera())
	st := s.Stats()
	if st.Frame != 1 {
		t.Fatalf("frame = %d, want 1", st.Frame)
	}
	if st.Sources != 1 || st.Visible != 1 || st.Instances != 1 || st.Queries != 1 {
		t.Fatalf("stats after one plan = %+v, want one visible source, one query and one instance", st)
	}
	mustPlan(t, s, testCamera())
	if st = s.Stats(); st.Frame != 2 {
		t.Fatalf("frame = %d, want 2", st.Frame)
	}
}
