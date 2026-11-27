// Package task reprojects a late latched headset frame with rotation and
// position handled separately and steps the quality ladder down on lost
// frames and back up on consecutive good frames.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reprojection modes reported for one frame.
const (
	ModeRotationParallax = "rotation_parallax"
	ModeRotationOnly     = "rotation_only"
)

// Quality is one rung of the degradation ladder.
type Quality string

// Quality ladder rungs, ordered from best to worst.
const (
	QualityHigh   Quality = "high"
	QualityMedium Quality = "medium"
	QualityLow    Quality = "low"
	QualityOff    Quality = "off"
)

// Errors returned by the guard.
var (
	ErrNoFrame        = errors.New("no frame is open")
	ErrNoLatch        = errors.New("frame has no latch")
	ErrBadImage       = errors.New("frame image is invalid")
	ErrBadReason      = errors.New("degrade reason is required")
	ErrBadConfig      = errors.New("reproject config is invalid")
	ErrRecoverPending = errors.New("recovery needs more consecutive good frames")
	ErrNotDegraded    = errors.New("quality is already at the top rung")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// ladder orders the rungs from best to worst so quality only moves one
// rung at a time and never jumps over a level.
var ladder = []Quality{QualityHigh, QualityMedium, QualityLow, QualityOff}

// validQualities is the set of rungs accepted in ReprojectConfig.
var validQualities = map[Quality]bool{
	QualityHigh:   true,
	QualityMedium: true,
	QualityLow:    true,
	QualityOff:    true,
}

// Vec3 is a point or a rotation triple in degrees.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Pose is a headset pose in world space.
type Pose struct {
	Position Vec3    `json:"position"`
	Yaw      float64 `json:"yaw"`
	Pitch    float64 `json:"pitch"`
	Roll     float64 `json:"roll"`
}

// LatchID identifies one locked pose.
type LatchID int64

// FrameImage is one rendered frame handed to the reprojector.
type FrameImage struct {
	FrameID  int64   `json:"frame_id"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Pixels   []uint8 `json:"pixels"`
	Rotation Vec3    `json:"rotation"`
	ShiftX   float64 `json:"shift_x"`
	ShiftY   float64 `json:"shift_y"`
	EdgeMask int     `json:"edge_mask"`
}

// ReprojectConfig tunes the reprojection limits.
type ReprojectConfig struct {
	MaxParallax   float64 `json:"max_parallax"`
	MaskStep      int     `json:"mask_step"`
	MaxQuality    Quality `json:"max_quality"`
	LostThreshold int     `json:"lost_threshold"`
	RecoverFrames int     `json:"recover_frames"`
	PixelPerMeter float64 `json:"pixel_per_meter"`
	MaxShift      float64 `json:"max_shift"`
}

// ReprojectStats explains one reprojection.
type ReprojectStats struct {
	FrameID  int64   `json:"frame_id"`
	Mode     string  `json:"mode"`
	RotDelta float64 `json:"rot_delta"`
	Parallax float64 `json:"parallax"`
	EdgeMask int     `json:"edge_mask"`
	Quality  Quality `json:"quality"`
}

// latchEntry is one entry of the latch table.
type latchEntry struct {
	Pose Pose      `json:"pose"`
	At   time.Time `json:"at"`
	ID   LatchID   `json:"id"`
}

// guardSnapshot is the serialised form of the whole guard state.
type guardSnapshot struct {
	Latches         map[int64]latchEntry `json:"latches"`
	FrameSeq        int64                `json:"frame_seq"`
	LatchSeq        int64                `json:"latch_seq"`
	LostConsecutive int                  `json:"lost_consecutive"`
	LostTotal       int                  `json:"lost_total"`
	GoodStreak      int                  `json:"good_streak"`
	Quality         Quality              `json:"quality"`
}

// goroutineID identifies the calling goroutine. The public Latch API has
// no frame handle, so each render thread keeps its own open frame keyed
// by the id parsed from the runtime stack prefix ("goroutine 123 ...").
func goroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	line := strings.TrimPrefix(string(buf[:n]), "goroutine ")
	end := strings.IndexByte(line, ' ')
	if end < 0 {
		return 0
	}
	id, err := strconv.ParseInt(line[:end], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// ReprojectionGuard owns the latch table and the quality ladder.
// mu guards every field below; latch table and counters are only visible
// after a fully completed update, and snapshots are mutually exclusive
// with latching.
type ReprojectionGuard struct {
	mu      sync.RWMutex
	cfg     ReprojectConfig
	latches map[int64]latchEntry

	frameSeq   int64
	latchSeq   int64
	openFrames map[int64]int64

	lostConsecutive int
	lostTotal       int
	goodStreak      int
	quality         Quality
}

// NewReprojectionGuard builds a guard with the given config. An empty
// MaxQuality defaults to the highest rung.
func NewReprojectionGuard(cfg ReprojectConfig) *ReprojectionGuard {
	if cfg.MaxQuality == "" {
		cfg.MaxQuality = QualityHigh
	}
	return &ReprojectionGuard{
		cfg:        cfg,
		latches:    make(map[int64]latchEntry),
		openFrames: make(map[int64]int64),
		quality:    cfg.MaxQuality,
	}
}

// validateConfig verifies the tuning values once per latch.
func (g *ReprojectionGuard) validateConfig() error {
	c := g.cfg
	if c.MaxParallax < 0 || c.MaxShift < 0 {
		return ErrBadConfig
	}
	if c.MaskStep <= 0 || c.LostThreshold <= 0 || c.RecoverFrames <= 0 || c.PixelPerMeter <= 0 {
		return ErrBadConfig
	}
	if !validQualities[c.MaxQuality] {
		return ErrBadConfig
	}
	return nil
}

// BeginFrame opens a new frame and returns its id, starting at one.
func (g *ReprojectionGuard) BeginFrame() (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.frameSeq++
	g.openFrames[goroutineID()] = g.frameSeq
	return g.frameSeq, nil
}

// Latch locks the pose of the open frame. Repeated latching within the
// same frame replaces the previous pose without accumulating it and keeps
// returning the same LatchID.
func (g *ReprojectionGuard) Latch(pose Pose, at time.Time) (LatchID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.validateConfig(); err != nil {
		return 0, err
	}
	frame, ok := g.openFrames[goroutineID()]
	if !ok {
		return 0, ErrNoFrame
	}
	if entry, ok := g.latches[frame]; ok {
		entry.Pose = pose
		entry.At = at
		g.latches[frame] = entry
		return entry.ID, nil
	}
	g.latchSeq++
	id := LatchID(g.latchSeq)
	g.latches[frame] = latchEntry{Pose: pose, At: at, ID: id}
	return id, nil
}

// LatchInfo reads the pose locked for one frame.
func (g *ReprojectionGuard) LatchInfo(frameID int64) (Pose, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	entry, ok := g.latches[frameID]
	if !ok {
		return Pose{}, false
	}
	return entry.Pose, true
}

// Reproject warps the latched frame towards the latest pose.
func (g *ReprojectionGuard) Reproject(image FrameImage, latest Pose) (FrameImage, ReprojectStats, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.latches[image.FrameID]
	if !ok {
		return FrameImage{}, ReprojectStats{}, ErrNoLatch
	}
	if image.Width <= 0 || image.Height <= 0 || len(image.Pixels) != image.Width*image.Height {
		return FrameImage{}, ReprojectStats{}, ErrBadImage
	}

	latched := entry.Pose
	rotDelta := maxAbs(latest.Yaw-latched.Yaw, latest.Pitch-latched.Pitch, latest.Roll-latched.Roll)

	dx := latest.Position.X - latched.Position.X
	dy := latest.Position.Y - latched.Position.Y
	dz := latest.Position.Z - latched.Position.Z
	parallax := math.Sqrt(dx*dx + dy*dy + dz*dz)

	result := image
	result.Rotation = Vec3{X: rotDelta, Y: rotDelta, Z: rotDelta}

	mode := ModeRotationParallax
	if parallax > g.cfg.MaxParallax {
		mode = ModeRotationOnly
		result.ShiftX = 0
		result.ShiftY = 0
		result.EdgeMask = image.EdgeMask + g.cfg.MaskStep
	} else {
		shiftX := dx * g.cfg.PixelPerMeter
		shiftY := dy * g.cfg.PixelPerMeter
		length := math.Sqrt(shiftX*shiftX + shiftY*shiftY)
		if g.cfg.MaxShift > 0 && length > g.cfg.MaxShift && length > 0 {
			scale := g.cfg.MaxShift / length
			shiftX *= scale
			shiftY *= scale
		}
		result.ShiftX = shiftX
		result.ShiftY = shiftY
	}

	stats := ReprojectStats{
		FrameID:  image.FrameID,
		Mode:     mode,
		RotDelta: rotDelta,
		Parallax: parallax,
		EdgeMask: result.EdgeMask,
		Quality:  g.quality,
	}
	return result, stats, nil
}

// maxAbs returns the largest absolute value of the given deltas.
func maxAbs(values ...float64) float64 {
	best := 0.0
	for _, value := range values {
		if magnitude := math.Abs(value); magnitude > best {
			best = magnitude
		}
	}
	return best
}

// stepDown moves the quality one rung towards the bottom of the ladder.
func (g *ReprojectionGuard) stepDown() {
	for index, rung := range ladder {
		if rung == g.quality && index < len(ladder)-1 {
			g.quality = ladder[index+1]
			return
		}
	}
}

// Degrade steps the quality ladder down one rung.
func (g *ReprojectionGuard) Degrade(reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if strings.TrimSpace(reason) == "" {
		return ErrBadReason
	}
	g.stepDown()
	return nil
}

// Recover steps the quality ladder back up when enough good frames landed.
func (g *ReprojectionGuard) Recover() (Quality, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.quality == g.cfg.MaxQuality {
		return g.quality, ErrNotDegraded
	}
	if g.goodStreak < g.cfg.RecoverFrames {
		return g.quality, ErrRecoverPending
	}
	for index := len(ladder) - 1; index > 0; index-- {
		if ladder[index] == g.quality {
			g.quality = ladder[index-1]
			break
		}
	}
	if rungIndex(g.quality) < rungIndex(g.cfg.MaxQuality) {
		g.quality = g.cfg.MaxQuality
	}
	g.goodStreak = 0
	return g.quality, nil
}

// rungIndex returns the position of a rung on the ladder.
func rungIndex(q Quality) int {
	for index, rung := range ladder {
		if rung == q {
			return index
		}
	}
	return len(ladder)
}

// ReportFrame records one delivered or lost frame.
func (g *ReprojectionGuard) ReportFrame(ok bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ok {
		g.lostConsecutive = 0
		g.goodStreak++
		return nil
	}
	g.lostConsecutive++
	g.lostTotal++
	g.goodStreak = 0
	if g.lostConsecutive >= g.cfg.LostThreshold {
		g.stepDown()
		g.lostConsecutive = 0
	}
	return nil
}

// Quality returns the current rung.
func (g *ReprojectionGuard) Quality() Quality {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.quality
}

// DroppedFrames reports the consecutive and total lost frame counters.
func (g *ReprojectionGuard) DroppedFrames() (int, int) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.lostConsecutive, g.lostTotal
}

// Snapshot serialises latches, counters and the quality rung.
func (g *ReprojectionGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	latches := make(map[int64]latchEntry, len(g.latches))
	for frame, entry := range g.latches {
		latches[frame] = entry
	}
	snap := guardSnapshot{
		Latches:         latches,
		FrameSeq:        g.frameSeq,
		LatchSeq:        g.latchSeq,
		LostConsecutive: g.lostConsecutive,
		LostTotal:       g.lostTotal,
		GoodStreak:      g.goodStreak,
		Quality:         g.quality,
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *ReprojectionGuard) Restore(data []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var snap guardSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Quality == "" || !validQualities[snap.Quality] {
		return ErrBadSnapshot
	}
	if snap.FrameSeq < 0 || snap.LatchSeq < 0 ||
		snap.LostConsecutive < 0 || snap.LostTotal < 0 || snap.GoodStreak < 0 {
		return ErrBadSnapshot
	}
	if snap.Latches == nil {
		snap.Latches = make(map[int64]latchEntry)
	}
	for frame, entry := range snap.Latches {
		if frame <= 0 || entry.ID <= 0 {
			return ErrBadSnapshot
		}
	}

	latches := make(map[int64]latchEntry, len(snap.Latches))
	for frame, entry := range snap.Latches {
		latches[frame] = entry
	}
	g.latches = latches
	g.frameSeq = snap.FrameSeq
	g.latchSeq = snap.LatchSeq
	g.lostConsecutive = snap.LostConsecutive
	g.lostTotal = snap.LostTotal
	g.goodStreak = snap.GoodStreak
	g.quality = snap.Quality
	g.openFrames = make(map[int64]int64)
	return nil
}
