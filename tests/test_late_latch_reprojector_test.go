package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-game-b-05"
)

func baseReprojectConfig() impl.ReprojectConfig {
	return impl.ReprojectConfig{
		MaxParallax:   1,
		MaskStep:      2,
		MaxQuality:    impl.QualityHigh,
		LostThreshold: 2,
		RecoverFrames: 3,
		PixelPerMeter: 100,
		MaxShift:      8,
	}
}

func testFrameImage(frameID int64) impl.FrameImage {
	return impl.FrameImage{
		FrameID: frameID,
		Width:   2,
		Height:  2,
		Pixels:  make([]uint8, 4),
	}
}

func originPose() impl.Pose {
	return impl.Pose{Position: impl.Vec3{}}
}

func TestLatchRequiresBeginFrame(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	if _, err := g.Latch(originPose(), time.Now()); !errors.Is(err, impl.ErrNoFrame) {
		t.Fatalf("latch before any frame must fail with ErrNoFrame, got %v", err)
	}
	frame, err := g.BeginFrame()
	if err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	if frame != 1 {
		t.Fatalf("first frame id = %d, want 1", frame)
	}
	id, err := g.Latch(originPose(), time.Now())
	if err != nil {
		t.Fatalf("latch: %v", err)
	}
	if id <= 0 {
		t.Fatalf("latch id = %d, want a positive id", id)
	}
}

func TestLatchInfoKeepsPose(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	frame, err := g.BeginFrame()
	if err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	pose := impl.Pose{Position: impl.Vec3{X: 1, Y: 2, Z: 3}, Yaw: 5, Pitch: -2, Roll: 0.5}
	if _, err := g.Latch(pose, time.Now()); err != nil {
		t.Fatalf("latch: %v", err)
	}
	got, ok := g.LatchInfo(frame)
	if !ok {
		t.Fatalf("latch info for frame %d is missing", frame)
	}
	if got != pose {
		t.Fatalf("latched pose = %+v, want %+v", got, pose)
	}
	if _, ok := g.LatchInfo(frame + 100); ok {
		t.Fatalf("unknown frame must not resolve a pose")
	}
	if _, ok := g.LatchInfo(0); ok {
		t.Fatalf("frame zero must not resolve a pose")
	}
}

func TestLatchIDsIncreaseAcrossFrames(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	var last impl.LatchID
	for i := 0; i < 3; i++ {
		frame, err := g.BeginFrame()
		if err != nil {
			t.Fatalf("begin frame %d: %v", i, err)
		}
		id, err := g.Latch(impl.Pose{Yaw: float64(i)}, time.Now())
		if err != nil {
			t.Fatalf("latch frame %d: %v", frame, err)
		}
		if id <= last {
			t.Fatalf("latch id %d does not advance past %d", id, last)
		}
		last = id
	}
}

func TestReprojectAppliesRotation(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	frame, err := g.BeginFrame()
	if err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	if _, err := g.Latch(originPose(), time.Now()); err != nil {
		t.Fatalf("latch: %v", err)
	}
	latest := impl.Pose{Yaw: 10}
	image, stats, err := g.Reproject(testFrameImage(frame), latest)
	if err != nil {
		t.Fatalf("reproject: %v", err)
	}
	if stats.Mode != impl.ModeRotationParallax {
		t.Fatalf("mode = %q, want %q for a still headset", stats.Mode, impl.ModeRotationParallax)
	}
	if stats.RotDelta != 10 {
		t.Fatalf("rotation delta = %v, want 10 degrees", stats.RotDelta)
	}
	if stats.Parallax != 0 {
		t.Fatalf("parallax = %v, want 0", stats.Parallax)
	}
	if image.Rotation.X != 10 {
		t.Fatalf("image yaw rotation = %v, want 10", image.Rotation.X)
	}
	if image.ShiftX != 0 || image.ShiftY != 0 {
		t.Fatalf("image shift = (%v,%v), want none", image.ShiftX, image.ShiftY)
	}
	if stats.FrameID != frame {
		t.Fatalf("stats frame id = %d, want %d", stats.FrameID, frame)
	}
	if stats.Quality != impl.QualityHigh {
		t.Fatalf("stats quality = %q, want high", stats.Quality)
	}
}

func TestReprojectAppliesParallaxShift(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	frame, err := g.BeginFrame()
	if err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	if _, err := g.Latch(originPose(), time.Now()); err != nil {
		t.Fatalf("latch: %v", err)
	}
	latest := impl.Pose{Position: impl.Vec3{X: 0.1}}
	image, stats, err := g.Reproject(testFrameImage(frame), latest)
	if err != nil {
		t.Fatalf("reproject: %v", err)
	}
	if stats.Mode != impl.ModeRotationParallax {
		t.Fatalf("mode = %q, want %q inside the parallax limit", stats.Mode, impl.ModeRotationParallax)
	}
	if stats.Parallax < 0.1-1e-9 || stats.Parallax > 0.1+1e-9 {
		t.Fatalf("parallax = %v, want 0.1", stats.Parallax)
	}
	if image.ShiftX != 8 || image.ShiftY != 0 {
		t.Fatalf("shift = (%v,%v), want (8,0) after the MaxShift clamp", image.ShiftX, image.ShiftY)
	}
}

func TestReprojectRejectsBadImage(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	frame, err := g.BeginFrame()
	if err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	if _, err := g.Latch(originPose(), time.Now()); err != nil {
		t.Fatalf("latch: %v", err)
	}
	cases := map[string]impl.FrameImage{
		"empty pixels":    {FrameID: frame, Width: 2, Height: 2},
		"zero width":      {FrameID: frame, Width: 0, Height: 2, Pixels: make([]uint8, 4)},
		"short pixel row": {FrameID: frame, Width: 2, Height: 2, Pixels: make([]uint8, 3)},
	}
	for name, image := range cases {
		if _, _, err := g.Reproject(image, originPose()); !errors.Is(err, impl.ErrBadImage) {
			t.Fatalf("%s: reproject error = %v, want ErrBadImage", name, err)
		}
	}
}

func TestReprojectWithoutLatch(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	frame, err := g.BeginFrame()
	if err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	if _, _, err := g.Reproject(testFrameImage(frame), originPose()); !errors.Is(err, impl.ErrNoLatch) {
		t.Fatalf("reproject without a latch must fail with ErrNoLatch, got %v", err)
	}
	if _, err := g.Latch(originPose(), time.Now()); err != nil {
		t.Fatalf("latch: %v", err)
	}
	if _, _, err := g.Reproject(testFrameImage(frame+50), originPose()); !errors.Is(err, impl.ErrNoLatch) {
		t.Fatalf("reproject for an unlatched frame must fail with ErrNoLatch, got %v", err)
	}
}

func TestDegradeRequiresReason(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	if err := g.Degrade("   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must fail with ErrBadReason, got %v", err)
	}
	if got := g.Quality(); got != impl.QualityHigh {
		t.Fatalf("quality after a rejected degrade = %q, want high", got)
	}
	if err := g.Degrade("thermal throttling"); err != nil {
		t.Fatalf("degrade: %v", err)
	}
	if got := g.Quality(); got != impl.QualityMedium {
		t.Fatalf("quality after one degrade = %q, want medium", got)
	}
}

func TestDegradeStopsAtLowestRung(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	for i := 0; i < 4; i++ {
		if err := g.Degrade("lost frames"); err != nil {
			t.Fatalf("degrade %d: %v", i, err)
		}
	}
	if got := g.Quality(); got != impl.QualityOff {
		t.Fatalf("quality at the bottom of the ladder = %q, want off", got)
	}
}

func TestReportLostFramesTriggersDegrade(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	if err := g.ReportFrame(false); err != nil {
		t.Fatalf("report lost frame: %v", err)
	}
	if got := g.Quality(); got != impl.QualityHigh {
		t.Fatalf("quality after one lost frame = %q, want high below the threshold", got)
	}
	if err := g.ReportFrame(false); err != nil {
		t.Fatalf("report lost frame: %v", err)
	}
	if got := g.Quality(); got != impl.QualityMedium {
		t.Fatalf("quality after two lost frames = %q, want medium", got)
	}
	consecutive, total := g.DroppedFrames()
	if total != 2 {
		t.Fatalf("total dropped frames = %d, want 2", total)
	}
	if consecutive != 0 {
		t.Fatalf("consecutive lost frames = %d, want 0 after the ladder stepped down", consecutive)
	}
}

func TestRecoverPendingAfterDegrade(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	if err := g.Degrade("gpu reset"); err != nil {
		t.Fatalf("degrade: %v", err)
	}
	if _, err := g.Recover(); !errors.Is(err, impl.ErrRecoverPending) {
		t.Fatalf("recover without good frames must fail with ErrRecoverPending, got %v", err)
	}
	if got := g.Quality(); got != impl.QualityMedium {
		t.Fatalf("quality after a pending recover = %q, want medium", got)
	}
}

func TestRecoverRejectsWhenNotDegraded(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	if _, err := g.Recover(); !errors.Is(err, impl.ErrNotDegraded) {
		t.Fatalf("recover at the top rung must fail with ErrNotDegraded, got %v", err)
	}
}

func TestLatchRejectsBadConfig(t *testing.T) {
	cases := map[string]func(*impl.ReprojectConfig){
		"negative parallax": func(c *impl.ReprojectConfig) { c.MaxParallax = -1 },
		"zero mask step":    func(c *impl.ReprojectConfig) { c.MaskStep = 0 },
		"zero lost limit":   func(c *impl.ReprojectConfig) { c.LostThreshold = 0 },
		"zero recover":      func(c *impl.ReprojectConfig) { c.RecoverFrames = 0 },
		"zero pixels":       func(c *impl.ReprojectConfig) { c.PixelPerMeter = 0 },
		"negative shift":    func(c *impl.ReprojectConfig) { c.MaxShift = -1 },
		"unknown quality":   func(c *impl.ReprojectConfig) { c.MaxQuality = "ultra" },
	}
	for name, mutate := range cases {
		cfg := baseReprojectConfig()
		mutate(&cfg)
		g := impl.NewReprojectionGuard(cfg)
		_, _ = g.BeginFrame()
		if _, err := g.Latch(originPose(), time.Now()); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("%s: latch error = %v, want ErrBadConfig", name, err)
		}
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	frame, err := g.BeginFrame()
	if err != nil {
		t.Fatalf("begin frame: %v", err)
	}
	pose := impl.Pose{Position: impl.Vec3{X: 0.5}, Yaw: 3}
	if _, err := g.Latch(pose, time.Now()); err != nil {
		t.Fatalf("latch: %v", err)
	}
	if err := g.Degrade("overheat"); err != nil {
		t.Fatalf("degrade: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewReprojectionGuard(baseReprojectConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Quality(); got != impl.QualityMedium {
		t.Fatalf("restored quality = %q, want medium", got)
	}
	got, ok := restored.LatchInfo(frame)
	if !ok || got != pose {
		t.Fatalf("restored latch = %+v ok=%v, want %+v", got, ok, pose)
	}
	next, err := restored.BeginFrame()
	if err != nil {
		t.Fatalf("begin frame after restore: %v", err)
	}
	if next <= frame {
		t.Fatalf("frame id after restore = %d, want greater than %d", next, frame)
	}
	if err := restored.Restore([]byte("late")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Quality(); got != impl.QualityMedium {
		t.Fatalf("failed restore changed the quality to %q", got)
	}
}

func TestConcurrentLatchAndReproject(t *testing.T) {
	g := impl.NewReprojectionGuard(baseReprojectConfig())
	var wg sync.WaitGroup
	failures := make(chan string, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				frame, err := g.BeginFrame()
				if err != nil {
					failures <- "begin frame: " + err.Error()
					return
				}
				pose := impl.Pose{Position: impl.Vec3{X: 0.001 * float64(i)}, Yaw: 0.1 * float64(j)}
				if _, err := g.Latch(pose, time.Now()); err != nil {
					failures <- "latch: " + err.Error()
					return
				}
				latest := impl.Pose{Position: impl.Vec3{X: 0.002 * float64(i)}, Yaw: 0.2 * float64(j)}
				if _, _, err := g.Reproject(testFrameImage(frame), latest); err != nil {
					failures <- "reproject: " + err.Error()
					return
				}
				if err := g.ReportFrame(true); err != nil {
					failures <- "report: " + err.Error()
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Fatalf("concurrent reprojection failed: %s", msg)
	}
	if got := g.Quality(); got != impl.QualityHigh {
		t.Fatalf("quality after only good frames = %q, want high", got)
	}
}
