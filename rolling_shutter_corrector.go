// Package task corrects rolling shutter footage by modelling the exposure time
// of every scanline and integrating gyro samples over the readout window.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sync"
)

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

func (v Vec3) add(o Vec3) Vec3 { return Vec3{X: v.X + o.X, Y: v.Y + o.Y, Z: v.Z + o.Z} }

func (v Vec3) scale(s float64) Vec3 { return Vec3{X: v.X * s, Y: v.Y * s, Z: v.Z * s} }

func (v Vec3) mag() float64 { return math.Sqrt(v.X*v.X + v.Y*v.Y + v.Z*v.Z) }

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

func (cfg ShutterConfig) withDefaults() ShutterConfig {
	if cfg.ReadoutUS <= 0 {
		cfg.ReadoutUS = DefaultReadoutUS
	}
	if cfg.StrengthMax <= 0 {
		cfg.StrengthMax = DefaultStrengthMax
	}
	if cfg.GridW <= 0 {
		cfg.GridW = DefaultGridW
	}
	if cfg.GridH <= 0 {
		cfg.GridH = DefaultGridH
	}
	if cfg.MaxSamplesPerFrame <= 0 {
		cfg.MaxSamplesPerFrame = DefaultSampleBudget
	}
	if cfg.MaxWarpVertices <= 0 {
		cfg.MaxWarpVertices = DefaultWarpVertexLimit
	}
	return cfg
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
// counters. All mutable state is guarded by mu so the preview and export
// threads can call Calibrate, Correct and Snapshot concurrently.
type RollingShutterCorrector struct {
	mu       sync.RWMutex
	cfg      ShutterConfig
	model    ShutterModel
	hasModel bool
	grid     GridSize
	segments int
	clamped  int
	gaps     int
	history  []GyroSample
}

// NewRollingShutterCorrector builds a corrector for the given configuration.
func NewRollingShutterCorrector(cfg ShutterConfig) *RollingShutterCorrector {
	cfg = cfg.withDefaults()
	return &RollingShutterCorrector{
		cfg:  cfg,
		grid: GridSize{W: cfg.GridW, H: cfg.GridH},
	}
}

// Calibrate derives the per row exposure model from frame metadata.
func (c *RollingShutterCorrector) Calibrate(meta FrameMeta) (ShutterModel, error) {
	if meta.Width <= 0 || meta.Height <= 0 || meta.StartUS < 0 {
		return ShutterModel{}, ErrBadMeta
	}
	readout := meta.ReadoutUS
	if readout <= 0 {
		readout = c.cfg.ReadoutUS
	}
	rowExposure := readout / int64(meta.Height)
	if rowExposure < 1 {
		return ShutterModel{}, ErrBadMeta
	}
	model := ShutterModel{
		FrameID:       meta.FrameID,
		StartUS:       meta.StartUS,
		RowCount:      meta.Height,
		ReadoutUS:     readout,
		RowExposureUS: rowExposure,
		SceneID:       meta.SceneID,
		Valid:         true,
	}
	c.mu.Lock()
	c.model = model
	c.hasModel = true
	c.mu.Unlock()
	return model, nil
}

// Correct computes the per row rotation offsets for one frame.
func (c *RollingShutterCorrector) Correct(frame Frame, gyro []GyroSample) (Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.hasModel {
		return Frame{}, ErrNotCalibrated
	}
	model := c.model
	if frame.H != model.RowCount || frame.W <= 0 {
		return Frame{}, ErrBadFrame
	}
	if len(gyro) > c.cfg.MaxSamplesPerFrame {
		return Frame{}, ErrSampleBudget
	}
	if !sortedStrict(gyro) {
		c.gaps++
	}
	clean := cleanSamples(gyro)

	requested := frame.Strength
	if requested <= 0 {
		requested = 1
	}

	offsets := make([]Vec3, model.RowCount)
	maxMag := 0.0
	for row := 0; row < model.RowCount; row++ {
		toUS := model.StartUS + int64(row)*model.RowExposureUS
		off := integrate(clean, model.StartUS, toUS)
		off = off.scale(requested)
		offsets[row] = off
		if m := off.mag(); m > maxMag {
			maxMag = m
		}
	}

	strength := requested
	clamped := false
	if maxMag > c.cfg.StrengthMax && maxMag > 0 {
		decay := c.cfg.StrengthMax / maxMag
		for row := range offsets {
			offsets[row] = offsets[row].scale(decay)
		}
		strength = requested * decay
		clamped = true
		c.clamped++
	}

	c.history = append(c.history, clean...)

	out := frame
	out.RowOffsets = offsets
	out.Strength = strength
	out.Clamped = clamped
	return out, nil
}

// RowExposure reports the exposure timestamp of one scanline in microseconds.
func (c *RollingShutterCorrector) RowExposure(model ShutterModel, row int) (int64, error) {
	if row < 0 || row >= model.RowCount {
		return 0, ErrBadRow
	}
	return model.StartUS + int64(row)*model.RowExposureUS, nil
}

// Integrate integrates angular velocity over a microsecond interval.
func (c *RollingShutterCorrector) Integrate(gyro []GyroSample, fromUS, toUS int64) (Vec3, error) {
	if fromUS > toUS {
		return Vec3{}, ErrBadInterval
	}
	return integrate(gyro, fromUS, toUS), nil
}

// integrate applies the trapezoidal rule to the piecewise linear angular
// velocity signal. Samples are cleaned so out of order or duplicate
// timestamps never accumulate negative time.
func integrate(gyro []GyroSample, fromUS, toUS int64) Vec3 {
	if fromUS >= toUS || len(gyro) == 0 {
		return Vec3{}
	}
	samples := cleanSamples(gyro)
	var acc Vec3
	prevT := fromUS
	prevW := sampleAt(samples, fromUS)
	for _, s := range samples {
		if s.AtUS <= fromUS {
			continue
		}
		if s.AtUS >= toUS {
			break
		}
		dt := float64(s.AtUS - prevT)
		acc = acc.add(prevW.add(s.W).scale(0.5 * dt))
		prevT = s.AtUS
		prevW = s.W
	}
	endW := sampleAt(samples, toUS)
	dt := float64(toUS - prevT)
	acc = acc.add(prevW.add(endW).scale(0.5 * dt))
	return acc.scale(1e-6)
}

// sortedStrict reports whether the sample timestamps are strictly increasing.
func sortedStrict(gyro []GyroSample) bool {
	for i := 1; i < len(gyro); i++ {
		if gyro[i].AtUS <= gyro[i-1].AtUS {
			return false
		}
	}
	return true
}

// cleanSamples sorts samples by timestamp and collapses duplicates, keeping
// the most recent reading for any repeated timestamp.
func cleanSamples(gyro []GyroSample) []GyroSample {
	if len(gyro) == 0 {
		return nil
	}
	sorted := make([]GyroSample, len(gyro))
	copy(sorted, gyro)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].AtUS < sorted[j].AtUS })
	out := sorted[:0]
	for _, s := range sorted {
		if n := len(out); n > 0 && out[n-1].AtUS == s.AtUS {
			out[n-1] = s
			continue
		}
		out = append(out, s)
	}
	return out
}

// sampleAt evaluates the piecewise linear signal, clamping to the nearest
// valid sample outside the measured range.
func sampleAt(samples []GyroSample, atUS int64) Vec3 {
	n := len(samples)
	if n == 0 {
		return Vec3{}
	}
	if atUS <= samples[0].AtUS {
		return samples[0].W
	}
	if atUS >= samples[n-1].AtUS {
		return samples[n-1].W
	}
	i := sort.Search(n, func(i int) bool { return samples[i].AtUS >= atUS })
	if samples[i].AtUS == atUS {
		return samples[i].W
	}
	lo := samples[i-1]
	hi := samples[i]
	span := hi.AtUS - lo.AtUS
	if span <= 0 {
		return lo.W
	}
	t := float64(atUS-lo.AtUS) / float64(span)
	return lo.W.add(hi.W.sub(lo.W).scale(t))
}

func (v Vec3) sub(o Vec3) Vec3 { return Vec3{X: v.X - o.X, Y: v.Y - o.Y, Z: v.Z - o.Z} }

// SetGrid changes the warp mesh resolution. A rejected grid leaves the active
// grid untouched.
func (c *RollingShutterCorrector) SetGrid(res GridSize) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if res.W < 1 || res.H < 1 || res.W*res.H > c.cfg.MaxWarpVertices {
		return ErrBadGrid
	}
	c.grid = res
	return nil
}

// Reset clears the calibrated model and the integration history, then starts
// a new segment.
func (c *RollingShutterCorrector) Reset(reason string) error {
	if reason == "" {
		return ErrBadReason
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = ShutterModel{}
	c.hasModel = false
	c.history = nil
	c.segments++
	return nil
}

// snapshotPayload is the serialised corrector state.
type snapshotPayload struct {
	Magic    string       `json:"magic"`
	Version  int          `json:"version"`
	Model    ShutterModel `json:"model"`
	HasModel bool         `json:"hasModel"`
	Grid     GridSize     `json:"grid"`
	Segments int          `json:"segments"`
	Clamped  int          `json:"clamped"`
	Gaps     int          `json:"gaps"`
}

const (
	snapshotMagic   = "rolling-shutter-corrector"
	snapshotVersion = 1
)

// Snapshot serialises the corrector state. The lock guarantees the model,
// grid and counters are a consistent generation, never a mix of old and new.
func (c *RollingShutterCorrector) Snapshot() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	payload := snapshotPayload{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Model:    c.model,
		HasModel: c.hasModel,
		Grid:     c.grid,
		Segments: c.segments,
		Clamped:  c.clamped,
		Gaps:     c.gaps,
	}
	return json.Marshal(payload)
}

// Restore replaces the corrector state with a snapshot, all or nothing. A
// corrupt payload leaves the live state untouched.
func (c *RollingShutterCorrector) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Magic != snapshotMagic || payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if payload.Grid.W < 1 || payload.Grid.H < 1 ||
		payload.Grid.W*payload.Grid.H > c.cfg.MaxWarpVertices {
		return ErrBadSnapshot
	}
	if payload.Segments < 0 || payload.Clamped < 0 || payload.Gaps < 0 {
		return ErrBadSnapshot
	}
	if payload.HasModel && (!payload.Model.Valid || payload.Model.RowCount <= 0) {
		return ErrBadSnapshot
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = payload.Model
	c.hasModel = payload.HasModel
	c.grid = payload.Grid
	c.segments = payload.Segments
	c.clamped = payload.Clamped
	c.gaps = payload.Gaps
	c.history = nil
	return nil
}

// Grid reports the active warp mesh resolution.
func (c *RollingShutterCorrector) Grid() GridSize {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.grid
}

// StrengthMax reports the cap applied to the correction strength.
func (c *RollingShutterCorrector) StrengthMax() float64 {
	return c.cfg.StrengthMax
}

// Segments reports how many segments boundaries have been started.
func (c *RollingShutterCorrector) Segments() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.segments
}

// Clamped reports how many frames hit the strength cap.
func (c *RollingShutterCorrector) Clamped() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clamped
}

// Model returns the active shutter model.
func (c *RollingShutterCorrector) Model() (ShutterModel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model, c.hasModel
}

// SampleBudget reports the per frame gyro sample budget.
func (c *RollingShutterCorrector) SampleBudget() int {
	return c.cfg.MaxSamplesPerFrame
}
