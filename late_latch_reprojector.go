// Package task reprojects a late latched headset frame with rotation and
// position handled separately and steps the quality ladder down on lost
// frames and back up on consecutive good frames.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

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

// ReprojectionGuard owns the latch table and the quality ladder.
type ReprojectionGuard struct {
	cfg ReprojectConfig
}

// NewReprojectionGuard builds a guard with the given config.
func NewReprojectionGuard(cfg ReprojectConfig) *ReprojectionGuard {
	return &ReprojectionGuard{cfg: cfg}
}

// BeginFrame opens a new frame and returns its id.
func (g *ReprojectionGuard) BeginFrame() (int64, error) {
	return 0, errNotImplemented
}

// Latch locks the pose of the open frame.
func (g *ReprojectionGuard) Latch(pose Pose, at time.Time) (LatchID, error) {
	return 0, errNotImplemented
}

// LatchInfo reads the pose locked for one frame.
func (g *ReprojectionGuard) LatchInfo(frameID int64) (Pose, bool) {
	return Pose{}, false
}

// Reproject warps the latched frame towards the latest pose.
func (g *ReprojectionGuard) Reproject(image FrameImage, latest Pose) (FrameImage, ReprojectStats, error) {
	return FrameImage{}, ReprojectStats{}, errNotImplemented
}

// Degrade steps the quality ladder down one rung.
func (g *ReprojectionGuard) Degrade(reason string) error {
	return errNotImplemented
}

// Recover steps the quality ladder back up when enough good frames landed.
func (g *ReprojectionGuard) Recover() (Quality, error) {
	return "", errNotImplemented
}

// ReportFrame records one delivered or lost frame.
func (g *ReprojectionGuard) ReportFrame(ok bool) error {
	return errNotImplemented
}

// Quality returns the current rung.
func (g *ReprojectionGuard) Quality() Quality {
	return ""
}

// DroppedFrames reports the consecutive and total lost frame counters.
func (g *ReprojectionGuard) DroppedFrames() (int, int) {
	return 0, 0
}

// Snapshot serialises latches, counters and the quality rung.
func (g *ReprojectionGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *ReprojectionGuard) Restore(data []byte) error {
	return errNotImplemented
}
