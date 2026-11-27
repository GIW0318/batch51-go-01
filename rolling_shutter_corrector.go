// Package task corrects rolling shutter footage by modelling the exposure time
// of every scanline and integrating gyro samples over the readout window.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Contract defaults used when the configuration leaves a field unset.
const (
	DefaultReadoutUS       int64 = 20000
	DefaultStrengthMax           = 0.8
	DefaultGridW                 = 16
	DefaultGridH                 = 9
	DefaultSampleBudget          = 1024
	DefaultWarpVertexLimit       = 4096
)

// Errors reported by the corrector.
var (
	ErrBadMeta       = errors.New("frame metadata is not usable for calibration")
	ErrBadRow        = errors.New("row index is outside the calibrated model")
	ErrBadInterval   = errors.New("integration interval is reversed")
	ErrBadFrame      = errors.New("frame does not match the calibrated model")
	ErrNotCalibrated = errors.New("no shutter model has been calibrated")
	ErrSampleBudget  = errors.New("gyro sample count exceeds the per frame budget")
	ErrBadGrid       = errors.New("warp grid is invalid or exceeds the vertex budget")
	ErrBadReason     = errors.New("reset reason is required")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Vec3 is a rotation offset in degrees.
type Vec3 struct {
	X float64
	Y float64
	Z float64
}

// GridSize is the warp mesh resolution.
type GridSize struct {
	W int
	H int
}

// ShutterConfig configures one corrector.
type ShutterConfig struct {
	ReadoutUS          int64
	StrengthMax        float64
	GridW              int
	GridH              int
	MaxSamplesPerFrame int
	MaxWarpVertices    int
}

// FrameMeta describes one captured frame.
type FrameMeta struct {
	FrameID   string
	Width     int
	Height    int
	StartUS   int64
	ReadoutUS int64
	FPS       float64
	SceneID   string
}

// ShutterModel is the calibrated per row exposure model of one frame.
type ShutterModel struct {
	FrameID       string
	StartUS       int64
	RowCount      int
	ReadoutUS     int64
	RowExposureUS int64
	SceneID       string
	Valid         bool
}

// GyroSample is one angular velocity measurement in degrees per second.
type GyroSample struct {
	AtUS int64
	W    Vec3
}

// Frame is one frame to correct. RowOffsets carries the applied per row
// rotation offset in degrees.
type Frame struct {
	ID         string
	W          int
	H          int
	AtUS       int64
	SceneID    string
	RowOffsets []Vec3
	Strength   float64
	Clamped    bool
}

// RollingShutterCorrector owns the active model, the warp grid and the segment
// counters.
type RollingShutterCorrector struct {
	cfg ShutterConfig
}

// NewRollingShutterCorrector builds a corrector for the given configuration.
func NewRollingShutterCorrector(cfg ShutterConfig) *RollingShutterCorrector {
	return &RollingShutterCorrector{cfg: cfg}
}

// Calibrate derives the per row exposure model from frame metadata.
func (c *RollingShutterCorrector) Calibrate(meta FrameMeta) (ShutterModel, error) {
	return ShutterModel{}, errNotImplemented
}

// Correct computes the per row rotation offsets for one frame.
func (c *RollingShutterCorrector) Correct(frame Frame, gyro []GyroSample) (Frame, error) {
	return Frame{}, errNotImplemented
}

// RowExposure reports the exposure timestamp of one scanline in microseconds.
func (c *RollingShutterCorrector) RowExposure(model ShutterModel, row int) (int64, error) {
	return 0, errNotImplemented
}

// Integrate integrates angular velocity over a microsecond interval.
func (c *RollingShutterCorrector) Integrate(gyro []GyroSample, fromUS, toUS int64) (Vec3, error) {
	return Vec3{}, errNotImplemented
}

// SetGrid changes the warp mesh resolution.
func (c *RollingShutterCorrector) SetGrid(res GridSize) error {
	return errNotImplemented
}

// Reset clears the calibrated model and starts a new segment.
func (c *RollingShutterCorrector) Reset(reason string) error {
	return errNotImplemented
}

// Snapshot serialises the corrector state.
func (c *RollingShutterCorrector) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the corrector state with a snapshot, all or nothing.
func (c *RollingShutterCorrector) Restore(data []byte) error {
	return errNotImplemented
}

// Grid reports the active warp mesh resolution.
func (c *RollingShutterCorrector) Grid() GridSize {
	return GridSize{}
}

// StrengthMax reports the cap applied to the correction strength.
func (c *RollingShutterCorrector) StrengthMax() float64 {
	return 0
}

// Segments reports how many segments have been started.
func (c *RollingShutterCorrector) Segments() int {
	return 0
}

// Clamped reports how many frames hit the strength cap.
func (c *RollingShutterCorrector) Clamped() int {
	return 0
}

// Model returns the active shutter model.
func (c *RollingShutterCorrector) Model() (ShutterModel, bool) {
	return ShutterModel{}, false
}

// SampleBudget reports the per frame gyro sample budget.
func (c *RollingShutterCorrector) SampleBudget() int {
	return 0
}
