package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch51-go-game-c-03"
)

func calibrated(t *testing.T, c *impl.RollingShutterCorrector, rows int, readoutUS int64) impl.ShutterModel {
	t.Helper()
	model, err := c.Calibrate(impl.FrameMeta{
		FrameID:   "f-cal",
		Width:     1920,
		Height:    rows,
		StartUS:   0,
		ReadoutUS: readoutUS,
		SceneID:   "scene-1",
	})
	if err != nil {
		t.Fatalf("calibration must succeed, got %v", err)
	}
	return model
}

func constantGyro(z float64) []impl.GyroSample {
	return []impl.GyroSample{
		{AtUS: 0, W: impl.Vec3{Z: z}},
		{AtUS: 100000, W: impl.Vec3{Z: z}},
	}
}

func TestNewCorrectorDefaults(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	if c.StrengthMax() != impl.DefaultStrengthMax {
		t.Fatalf("default strength cap = %v, want %v", c.StrengthMax(), impl.DefaultStrengthMax)
	}
	grid := c.Grid()
	if grid.W != impl.DefaultGridW || grid.H != impl.DefaultGridH {
		t.Fatalf("default grid = %dx%d, want %dx%d", grid.W, grid.H, impl.DefaultGridW, impl.DefaultGridH)
	}
	if c.SampleBudget() != impl.DefaultSampleBudget {
		t.Fatalf("default sample budget = %d, want %d", c.SampleBudget(), impl.DefaultSampleBudget)
	}
	if c.Segments() != 0 || c.Clamped() != 0 {
		t.Fatalf("fresh counters segments=%d clamped=%d, want 0", c.Segments(), c.Clamped())
	}
	if _, ok := c.Model(); ok {
		t.Fatalf("a fresh corrector must not report a calibrated model")
	}
}

func TestCalibrateDerivesRowExposure(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{ReadoutUS: 20000})
	model, err := c.Calibrate(impl.FrameMeta{FrameID: "f1", Width: 1920, Height: 100, StartUS: 1000, ReadoutUS: 10000, SceneID: "s1"})
	if err != nil {
		t.Fatalf("calibration must succeed, got %v", err)
	}
	if !model.Valid {
		t.Fatalf("calibrated model reported invalid")
	}
	if model.RowCount != 100 {
		t.Fatalf("row count = %d, want 100", model.RowCount)
	}
	if model.ReadoutUS != 10000 || model.RowExposureUS != 100 {
		t.Fatalf("readout=%d row exposure=%d, want 10000 and 100", model.ReadoutUS, model.RowExposureUS)
	}
	if model.StartUS != 1000 || model.SceneID != "s1" {
		t.Fatalf("model start=%d scene=%q, want 1000 and s1", model.StartUS, model.SceneID)
	}
	fallback, err := c.Calibrate(impl.FrameMeta{FrameID: "f2", Width: 1920, Height: 200, StartUS: 0})
	if err != nil {
		t.Fatalf("calibration without a per frame readout must succeed, got %v", err)
	}
	if fallback.ReadoutUS != 20000 || fallback.RowExposureUS != 100 {
		t.Fatalf("fallback readout=%d row exposure=%d, want 20000 and 100", fallback.ReadoutUS, fallback.RowExposureUS)
	}
}

func TestCalibrateRejectsBadMeta(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	cases := []impl.FrameMeta{
		{FrameID: "f", Width: 0, Height: 100, ReadoutUS: 10000},
		{FrameID: "f", Width: 1920, Height: 0, ReadoutUS: 10000},
		{FrameID: "f", Width: 1920, Height: 100, ReadoutUS: 10},
		{FrameID: "f", Width: 1920, Height: 100, StartUS: -5, ReadoutUS: 10000},
	}
	for i, meta := range cases {
		if _, err := c.Calibrate(meta); !errors.Is(err, impl.ErrBadMeta) {
			t.Fatalf("case %d: err = %v, want ErrBadMeta", i, err)
		}
	}
}

func TestRowExposureBounds(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	model := calibrated(t, c, 100, 10000)
	first, err := c.RowExposure(model, 0)
	if err != nil {
		t.Fatalf("row 0 must be readable, got %v", err)
	}
	if first != model.StartUS {
		t.Fatalf("row 0 exposure = %d, want %d", first, model.StartUS)
	}
	last, err := c.RowExposure(model, 99)
	if err != nil {
		t.Fatalf("last row must be readable, got %v", err)
	}
	if want := model.StartUS + 99*model.RowExposureUS; last != want {
		t.Fatalf("last row exposure = %d, want %d", last, want)
	}
	if _, err := c.RowExposure(model, -1); !errors.Is(err, impl.ErrBadRow) {
		t.Fatalf("negative row err = %v, want ErrBadRow", err)
	}
	if _, err := c.RowExposure(model, 100); !errors.Is(err, impl.ErrBadRow) {
		t.Fatalf("row past the model err = %v, want ErrBadRow", err)
	}
}

func TestIntegrateConstantVelocity(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	got, err := c.Integrate(constantGyro(10), 0, 100000)
	if err != nil {
		t.Fatalf("integration must succeed, got %v", err)
	}
	if math.Abs(got.Z-1.0) > 1e-9 || got.X != 0 || got.Y != 0 {
		t.Fatalf("integral over one tenth of a second = %+v, want Z 1", got)
	}
	half, err := c.Integrate(constantGyro(10), 25000, 75000)
	if err != nil {
		t.Fatalf("half interval integration must succeed, got %v", err)
	}
	if math.Abs(half.Z-0.5) > 1e-9 {
		t.Fatalf("integral over half the interval = %v, want 0.5", half.Z)
	}
	ramp := []impl.GyroSample{{AtUS: 0, W: impl.Vec3{Z: 0}}, {AtUS: 100000, W: impl.Vec3{Z: 20}}}
	got, err = c.Integrate(ramp, 0, 100000)
	if err != nil {
		t.Fatalf("ramp integration must succeed, got %v", err)
	}
	if math.Abs(got.Z-1.0) > 1e-9 {
		t.Fatalf("ramp integral = %v, want 1", got.Z)
	}
}

func TestIntegrateEmptyGyroIsZero(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	got, err := c.Integrate(nil, 0, 5000)
	if err != nil {
		t.Fatalf("integration without samples must succeed, got %v", err)
	}
	if got.X != 0 || got.Y != 0 || got.Z != 0 {
		t.Fatalf("integral without samples = %+v, want the zero vector", got)
	}
}

func TestIntegrateRejectsReversedInterval(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	if _, err := c.Integrate(constantGyro(10), 200, 100); !errors.Is(err, impl.ErrBadInterval) {
		t.Fatalf("reversed interval err = %v, want ErrBadInterval", err)
	}
}

func TestCorrectAppliesRowOffsets(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	calibrated(t, c, 4, 4000)
	out, err := c.Correct(impl.Frame{ID: "f1", W: 1920, H: 4, SceneID: "scene-1"}, constantGyro(10))
	if err != nil {
		t.Fatalf("correction must succeed, got %v", err)
	}
	if len(out.RowOffsets) != 4 {
		t.Fatalf("row offset count = %d, want 4", len(out.RowOffsets))
	}
	want := []float64{0, 0.01, 0.02, 0.03}
	for row, w := range want {
		got := out.RowOffsets[row].Z
		if math.Abs(got-w) > 1e-9 {
			t.Fatalf("row %d offset = %v, want %v", row, got, w)
		}
		if out.RowOffsets[row].X != 0 || out.RowOffsets[row].Y != 0 {
			t.Fatalf("row %d lateral offset = %+v, want zero", row, out.RowOffsets[row])
		}
	}
	if out.Clamped {
		t.Fatalf("a three hundredths of a degree correction must not be clamped")
	}
	if math.Abs(out.Strength-1) > 1e-9 {
		t.Fatalf("applied strength = %v, want 1", out.Strength)
	}
	if out.ID != "f1" || out.H != 4 {
		t.Fatalf("corrected frame identity = %q/%d, want f1/4", out.ID, out.H)
	}
}

func TestCorrectZeroGyroKeepsOffsetsZero(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	calibrated(t, c, 3, 3000)
	out, err := c.Correct(impl.Frame{ID: "f2", W: 640, H: 3, SceneID: "scene-1"}, nil)
	if err != nil {
		t.Fatalf("correction without gyro samples must succeed, got %v", err)
	}
	if len(out.RowOffsets) != 3 {
		t.Fatalf("row offset count = %d, want 3", len(out.RowOffsets))
	}
	for row, off := range out.RowOffsets {
		if off.X != 0 || off.Y != 0 || off.Z != 0 {
			t.Fatalf("row %d offset without gyro = %+v, want zero", row, off)
		}
	}
	if out.Clamped {
		t.Fatalf("a frame without gyro samples must not be clamped")
	}
}

func TestCorrectRejectsFrameSizeMismatch(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	calibrated(t, c, 4, 4000)
	if _, err := c.Correct(impl.Frame{ID: "f3", W: 1920, H: 5, SceneID: "scene-1"}, constantGyro(10)); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("row count mismatch err = %v, want ErrBadFrame", err)
	}
	if _, err := c.Correct(impl.Frame{ID: "f4", W: 0, H: 4, SceneID: "scene-1"}, constantGyro(10)); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("zero width err = %v, want ErrBadFrame", err)
	}
}

func TestCorrectRequiresCalibration(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	if _, err := c.Correct(impl.Frame{ID: "f5", W: 640, H: 2, SceneID: "s"}, constantGyro(5)); !errors.Is(err, impl.ErrNotCalibrated) {
		t.Fatalf("correction before calibration err = %v, want ErrNotCalibrated", err)
	}
}

func TestCorrectRejectsSampleBudget(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{MaxSamplesPerFrame: 32})
	calibrated(t, c, 4, 4000)
	gyro := make([]impl.GyroSample, 64)
	for i := range gyro {
		gyro[i] = impl.GyroSample{AtUS: int64(i) * 100, W: impl.Vec3{Z: 2}}
	}
	if _, err := c.Correct(impl.Frame{ID: "f6", W: 640, H: 4, SceneID: "scene-1"}, gyro); !errors.Is(err, impl.ErrSampleBudget) {
		t.Fatalf("oversized gyro batch err = %v, want ErrSampleBudget", err)
	}
}

func TestSetGridValidatesAndStores(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	if err := c.SetGrid(impl.GridSize{W: 8, H: 8}); err != nil {
		t.Fatalf("setting an in budget grid must succeed, got %v", err)
	}
	grid := c.Grid()
	if grid.W != 8 || grid.H != 8 {
		t.Fatalf("grid after set = %dx%d, want 8x8", grid.W, grid.H)
	}
	if err := c.SetGrid(impl.GridSize{W: 0, H: 4}); !errors.Is(err, impl.ErrBadGrid) {
		t.Fatalf("zero width grid err = %v, want ErrBadGrid", err)
	}
	if err := c.SetGrid(impl.GridSize{W: 65, H: 65}); !errors.Is(err, impl.ErrBadGrid) {
		t.Fatalf("grid past the vertex budget err = %v, want ErrBadGrid", err)
	}
	grid = c.Grid()
	if grid.W != 8 || grid.H != 8 {
		t.Fatalf("grid changed by a rejected value = %dx%d, want 8x8", grid.W, grid.H)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	if err := c.SetGrid(impl.GridSize{W: 8, H: 6}); err != nil {
		t.Fatalf("setting the grid must succeed, got %v", err)
	}
	calibrated(t, c, 4, 4000)
	if _, err := c.Correct(impl.Frame{ID: "f7", W: 640, H: 4, SceneID: "scene-1"}, constantGyro(10)); err != nil {
		t.Fatalf("correction must succeed, got %v", err)
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewRollingShutterCorrector(impl.ShutterConfig{ReadoutUS: 9999})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	model, ok := restored.Model()
	if !ok {
		t.Fatalf("restored corrector reports no model")
	}
	if model.RowCount != 4 || model.RowExposureUS != 1000 {
		t.Fatalf("restored model rows=%d row exposure=%d, want 4 and 1000", model.RowCount, model.RowExposureUS)
	}
	grid := restored.Grid()
	if grid.W != 8 || grid.H != 6 {
		t.Fatalf("restored grid = %dx%d, want 8x6", grid.W, grid.H)
	}
	if _, err := restored.Correct(impl.Frame{ID: "f8", W: 640, H: 4, SceneID: "scene-1"}, constantGyro(10)); err != nil {
		t.Fatalf("correction after restore must succeed, got %v", err)
	}
	if err := restored.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot err = %v, want ErrBadSnapshot", err)
	}
	if _, ok := restored.Model(); !ok {
		t.Fatalf("model cleared by a rejected snapshot")
	}
}

func TestResetIncrementsSegments(t *testing.T) {
	c := impl.NewRollingShutterCorrector(impl.ShutterConfig{})
	calibrated(t, c, 4, 4000)
	if err := c.Reset("scene_cut"); err != nil {
		t.Fatalf("reset must succeed, got %v", err)
	}
	if c.Segments() != 1 {
		t.Fatalf("segments after reset = %d, want 1", c.Segments())
	}
	if _, ok := c.Model(); ok {
		t.Fatalf("model still reported after reset")
	}
	if err := c.Reset(""); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("empty reason err = %v, want ErrBadReason", err)
	}
	if c.Segments() != 1 {
		t.Fatalf("segments after a rejected reset = %d, want 1", c.Segments())
	}
	if err := c.Reset("gyro_gap"); err != nil {
		t.Fatalf("second reset must succeed, got %v", err)
	}
	if c.Segments() != 2 {
		t.Fatalf("segments after two resets = %d, want 2", c.Segments())
	}
}
