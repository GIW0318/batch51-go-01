package task

import (
	"errors"
	"math"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-c-14"
)

func mustSource(t *testing.T, planner *impl.AutoReframePlanner) {
	t.Helper()
	if err := planner.SetSource(1920, 1080); err != nil {
		t.Fatalf("setting the source size must succeed, got %v", err)
	}
}

func flatTrack(x float64, from, to int64) []impl.SubjectSample {
	return []impl.SubjectSample{
		{TS: from, X: x, Y: 540, Confidence: 0.9},
		{TS: to, X: x, Y: 540, Confidence: 0.9},
	}
}

func closeEnough(a, b float64) bool {
	return math.Abs(a-b) < 0.5
}

func TestDefaults(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	if got := p.TargetAspect(); math.Abs(got-impl.DefaultAspect) > 1e-9 {
		t.Fatalf("default aspect = %v, want %v", got, impl.DefaultAspect)
	}
	if got := p.Smoothing(); got != impl.DefaultSmoothing {
		t.Fatalf("default smoothing = %d, want %d", got, impl.DefaultSmoothing)
	}
	if got := p.KeyframeInterval(); got != impl.DefaultInterval {
		t.Fatalf("default keyframe interval = %d, want %d", got, impl.DefaultInterval)
	}
	if st := p.Stats(); st.Plans != 0 || st.Keyframes != 0 {
		t.Fatalf("fresh stats = %+v, want all counters zero", st)
	}
	if _, err := p.Plan(0, 60); !errors.Is(err, impl.ErrNoSource) {
		t.Fatalf("planning without a source size must fail with ErrNoSource, got %v", err)
	}
}

func TestSetSourceValidation(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	if err := p.SetSource(0, 1080); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("a zero width source must fail with ErrBadSource, got %v", err)
	}
	if err := p.SetSource(1920, -1); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("a negative height source must fail with ErrBadSource, got %v", err)
	}
	if err := p.SetSource(1920, 1080); err != nil {
		t.Fatalf("a valid source must be accepted, got %v", err)
	}
	if _, err := p.Plan(0, 60); !errors.Is(err, impl.ErrNoTrack) {
		t.Fatalf("planning without a subject track must fail with ErrNoTrack, got %v", err)
	}
}

func TestSetTargetAspectValidation(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	if err := p.SetTargetAspect(0.05); !errors.Is(err, impl.ErrBadAspect) {
		t.Fatalf("a tiny aspect must fail with ErrBadAspect, got %v", err)
	}
	if err := p.SetTargetAspect(11); !errors.Is(err, impl.ErrBadAspect) {
		t.Fatalf("a huge aspect must fail with ErrBadAspect, got %v", err)
	}
	if got := p.TargetAspect(); math.Abs(got-impl.DefaultAspect) > 1e-9 {
		t.Fatalf("rejected aspects changed the live value to %v", got)
	}
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("a portrait aspect must be accepted, got %v", err)
	}
	if got := p.TargetAspect(); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("aspect = %v, want 0.5", got)
	}
}

func TestSetSubjectTrackValidation(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	if err := p.SetSubjectTrack(nil); !errors.Is(err, impl.ErrBadTrack) {
		t.Fatalf("an empty track must fail with ErrBadTrack, got %v", err)
	}
	backwards := []impl.SubjectSample{{TS: 30, X: 10, Confidence: 0.9}, {TS: 20, X: 20, Confidence: 0.9}}
	if err := p.SetSubjectTrack(backwards); !errors.Is(err, impl.ErrBadTrack) {
		t.Fatalf("decreasing sample timestamps must fail with ErrBadTrack, got %v", err)
	}
	overConfident := []impl.SubjectSample{{TS: 0, X: 10, Confidence: 1.4}}
	if err := p.SetSubjectTrack(overConfident); !errors.Is(err, impl.ErrBadTrack) {
		t.Fatalf("a confidence above one must fail with ErrBadTrack, got %v", err)
	}
	budget := impl.NewAutoReframePlanner(impl.Limits{MaxSamples: 2})
	tooMany := []impl.SubjectSample{
		{TS: 0, X: 10, Confidence: 0.9},
		{TS: 10, X: 20, Confidence: 0.9},
		{TS: 20, X: 30, Confidence: 0.9},
	}
	if err := budget.SetSubjectTrack(tooMany); !errors.Is(err, impl.ErrSampleBudget) {
		t.Fatalf("a track above the sample budget must fail with ErrSampleBudget, got %v", err)
	}
}

func TestSetSafeRectsValidation(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	if err := p.SetSafeRects([]impl.Rect{{X: 0, Y: 0, W: 0, H: 40}}); !errors.Is(err, impl.ErrBadSafeRect) {
		t.Fatalf("a zero width safe rect must fail with ErrBadSafeRect, got %v", err)
	}
	if err := p.SetSafeRects([]impl.Rect{{X: -5, Y: 0, W: 40, H: 40}}); !errors.Is(err, impl.ErrBadSafeRect) {
		t.Fatalf("a negative safe rect origin must fail with ErrBadSafeRect, got %v", err)
	}
	if err := p.SetSafeRects([]impl.Rect{{X: 100, Y: 900, W: 400, H: 120}}); err != nil {
		t.Fatalf("a valid safe rect must be accepted, got %v", err)
	}
}

func TestPlanKeyframeCount(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetSubjectTrack(flatTrack(400, 0, 200)); err != nil {
		t.Fatalf("set track: %v", err)
	}
	if err := p.SetKeyframeInterval(25); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	plan, err := p.Plan(0, 100)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Keyframes) != 5 {
		t.Fatalf("keyframe count = %d, want 5", len(plan.Keyframes))
	}
	wantTS := []int64{0, 25, 50, 75, 100}
	for i, ts := range wantTS {
		if plan.Keyframes[i].TS != ts {
			t.Fatalf("keyframe %d ts = %d, want %d", i, plan.Keyframes[i].TS, ts)
		}
	}
	if plan.Frames != 101 {
		t.Fatalf("plan frames = %d, want end minus start plus one = 101", plan.Frames)
	}
	if plan.SourceW != 1920 || plan.SourceH != 1080 {
		t.Fatalf("plan source = %dx%d, want 1920x1080", plan.SourceW, plan.SourceH)
	}
}

func TestPlanCropSize(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetSubjectTrack(flatTrack(900, 0, 100)); err != nil {
		t.Fatalf("set track: %v", err)
	}
	plan, err := p.Plan(0, 60)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Keyframes) == 0 {
		t.Fatalf("plan must carry keyframes")
	}
	for i, kf := range plan.Keyframes {
		if !closeEnough(kf.W, 540) || !closeEnough(kf.H, 1080) {
			t.Fatalf("keyframe %d window = %.1fx%.1f, want 540x1080 for aspect 0.5", i, kf.W, kf.H)
		}
		if !closeEnough(kf.Y, 0) {
			t.Fatalf("keyframe %d y = %.1f, want the full height window at 0", i, kf.Y)
		}
	}
}

func TestPlanClampsWindowToSource(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetSmoothing(0); err != nil {
		t.Fatalf("set smoothing: %v", err)
	}
	if err := p.SetKeyframeInterval(50); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	track := []impl.SubjectSample{
		{TS: 0, X: 0, Y: 540, Confidence: 0.9},
		{TS: 100, X: 1920, Y: 540, Confidence: 0.9},
	}
	if err := p.SetSubjectTrack(track); err != nil {
		t.Fatalf("set track: %v", err)
	}
	plan, err := p.Plan(0, 100)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Keyframes) != 3 {
		t.Fatalf("keyframe count = %d, want 3", len(plan.Keyframes))
	}
	want := []float64{0, 690, 1380}
	for i, x := range want {
		if !closeEnough(plan.Keyframes[i].X, x) {
			t.Fatalf("keyframe %d x = %.1f, want %.1f inside the source", i, plan.Keyframes[i].X, x)
		}
	}
}

func TestPlanSmoothingAveragesRawPositions(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetSmoothing(2); err != nil {
		t.Fatalf("set smoothing: %v", err)
	}
	if err := p.SetKeyframeInterval(20); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	track := []impl.SubjectSample{
		{TS: 0, X: 400, Y: 540, Confidence: 0.9},
		{TS: 40, X: 400, Y: 540, Confidence: 0.9},
		{TS: 60, X: 900, Y: 540, Confidence: 0.8},
		{TS: 80, X: 900, Y: 540, Confidence: 0.8},
	}
	if err := p.SetSubjectTrack(track); err != nil {
		t.Fatalf("set track: %v", err)
	}
	plan, err := p.Plan(0, 80)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := []float64{130, 130, 130, 380, 630}
	if len(plan.Keyframes) != len(want) {
		t.Fatalf("keyframe count = %d, want %d", len(plan.Keyframes), len(want))
	}
	for i, x := range want {
		if !closeEnough(plan.Keyframes[i].X, x) {
			t.Fatalf("keyframe %d x = %.1f, want %.1f from the two frame average", i, plan.Keyframes[i].X, x)
		}
	}
}

func TestPlanSafeRectShiftsWindow(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetKeyframeInterval(25); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	if err := p.SetSubjectTrack(flatTrack(400, 0, 100)); err != nil {
		t.Fatalf("set track: %v", err)
	}
	if err := p.SetSafeRects([]impl.Rect{{X: 1400, Y: 900, W: 200, H: 100}}); err != nil {
		t.Fatalf("set safe rects: %v", err)
	}
	plan, err := p.Plan(0, 100)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Keyframes) != 5 {
		t.Fatalf("keyframe count = %d, want 5", len(plan.Keyframes))
	}
	for i, kf := range plan.Keyframes {
		if !closeEnough(kf.X, 1060) {
			t.Fatalf("keyframe %d x = %.1f, want 1060 so the safe rect stays visible", i, kf.X)
		}
	}
	if plan.SafeShifts != 5 {
		t.Fatalf("safe shifts = %d, want one per keyframe", plan.SafeShifts)
	}
	if st := p.Stats(); st.SafeShifts != 5 {
		t.Fatalf("stats safe shifts = %d, want 5", st.SafeShifts)
	}
}

func TestPlanRejectsBadRange(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetSubjectTrack(flatTrack(400, 0, 200)); err != nil {
		t.Fatalf("set track: %v", err)
	}
	if _, err := p.Plan(-1, 100); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("a negative start must fail with ErrBadRange, got %v", err)
	}
	if _, err := p.Plan(50, 50); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("an empty range must fail with ErrBadRange, got %v", err)
	}
	if _, err := p.Plan(80, 20); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("an inverted range must fail with ErrBadRange, got %v", err)
	}
	if st := p.Stats(); st.Plans != 0 {
		t.Fatalf("rejected plans must not be counted, plans=%d", st.Plans)
	}
}

func TestPlanEnforcesKeyframeBudget(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{MaxKeyframes: 3})
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetSubjectTrack(flatTrack(400, 0, 200)); err != nil {
		t.Fatalf("set track: %v", err)
	}
	if err := p.SetKeyframeInterval(1); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	if _, err := p.Plan(0, 100); !errors.Is(err, impl.ErrKeyframeBudget) {
		t.Fatalf("101 keyframes against a budget of 3 must fail with ErrKeyframeBudget, got %v", err)
	}
	if st := p.Stats(); st.Plans != 0 {
		t.Fatalf("rejected plans must not be counted, plans=%d", st.Plans)
	}
}

func TestValidateRejectsOutOfBoundsPlan(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetSubjectTrack(flatTrack(400, 0, 100)); err != nil {
		t.Fatalf("set track: %v", err)
	}
	if err := p.SetKeyframeInterval(25); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	plan, err := p.Plan(0, 100)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := p.Validate(plan); err != nil {
		t.Fatalf("a freshly planned path must validate, got %v", err)
	}
	outside := plan
	outside.Keyframes = append([]impl.Keyframe(nil), plan.Keyframes...)
	outside.Keyframes[0].X = 1500
	if err := p.Validate(outside); !errors.Is(err, impl.ErrInvalidPlan) {
		t.Fatalf("a window past the right edge must fail with ErrInvalidPlan, got %v", err)
	}
	unordered := plan
	unordered.Keyframes = append([]impl.Keyframe(nil), plan.Keyframes...)
	unordered.Keyframes[1].TS = unordered.Keyframes[0].TS
	if err := p.Validate(unordered); !errors.Is(err, impl.ErrInvalidPlan) {
		t.Fatalf("non increasing keyframe timestamps must fail with ErrInvalidPlan, got %v", err)
	}
	if err := p.Validate(impl.ReframePlan{}); !errors.Is(err, impl.ErrInvalidPlan) {
		t.Fatalf("an empty plan must fail with ErrInvalidPlan, got %v", err)
	}
}

func TestSnapshotRestoreKeepsTrack(t *testing.T) {
	limits := impl.Limits{MaxKeyframes: 64}
	p := impl.NewAutoReframePlanner(limits)
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetKeyframeInterval(25); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	if err := p.SetSubjectTrack(flatTrack(400, 0, 100)); err != nil {
		t.Fatalf("set track: %v", err)
	}
	if err := p.SetSafeRects([]impl.Rect{{X: 1400, Y: 900, W: 200, H: 100}}); err != nil {
		t.Fatalf("set safe rects: %v", err)
	}
	want, err := p.Plan(0, 50)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	r := impl.NewAutoReframePlanner(limits)
	if err := r.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if st := r.Stats(); st.Plans != 1 || st.Keyframes != len(want.Keyframes) {
		t.Fatalf("restored stats = %+v, want 1 plan with %d keyframes", st, len(want.Keyframes))
	}
	got, err := r.Plan(0, 50)
	if err != nil {
		t.Fatalf("plan after restore: %v", err)
	}
	if len(got.Keyframes) != len(want.Keyframes) {
		t.Fatalf("restored keyframes = %d, want %d", len(got.Keyframes), len(want.Keyframes))
	}
	for i := range want.Keyframes {
		if got.Keyframes[i] != want.Keyframes[i] {
			t.Fatalf("keyframe %d after restore = %+v, want %+v", i, got.Keyframes[i], want.Keyframes[i])
		}
	}
	if err := r.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if st := r.Stats(); st.Plans != 1 {
		t.Fatalf("a rejected restore must not touch the live state, plans=%d", st.Plans)
	}
}

func TestConcurrentPlan(t *testing.T) {
	p := impl.NewAutoReframePlanner(impl.Limits{})
	mustSource(t, p)
	if err := p.SetTargetAspect(0.5); err != nil {
		t.Fatalf("set aspect: %v", err)
	}
	if err := p.SetKeyframeInterval(25); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	if err := p.SetSubjectTrack(flatTrack(400, 0, 100)); err != nil {
		t.Fatalf("set track: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	keyframes := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			plan, err := p.Plan(0, 50)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				return
			}
			keyframes += len(plan.Keyframes)
		}()
	}
	wg.Wait()
	if failed != 0 {
		t.Fatalf("%d concurrent plans failed, want 0", failed)
	}
	if keyframes != 24 {
		t.Fatalf("total keyframes = %d, want 8 plans of 3 keyframes", keyframes)
	}
	if st := p.Stats(); st.Plans != 8 || st.Keyframes != 24 {
		t.Fatalf("stats plans=%d keyframes=%d, want 8 and 24", st.Plans, st.Keyframes)
	}
}
