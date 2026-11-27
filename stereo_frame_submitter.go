// Package task submits stereo VR frames: one shared visibility set, per eye
// culling against a latched pose and a fixed left then right submit order.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Eye selects one eye of the headset.
type Eye int

// Eye identifiers.
const (
	EyeLeft  Eye = 0
	EyeRight Eye = 1
	EyeCount     = 2
)

// Defaults applied for budget fields left at zero.
const (
	DefaultMaxItemsPerEye   = 4096
	DefaultMaxVertsPerEye   = 1 << 20
	DefaultMaxReprojections = 2
)

// Vec3 is a point in tracking space.
type Vec3 struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// Pose is the head pose of one eye as reported by the runtime.
type Pose struct {
	Position Vec3    `json:"position"`
	Yaw      float32 `json:"yaw"`
	Pitch    float32 `json:"pitch"`
	IPD      float32 `json:"ipd"`
}

// HmdState is what the runtime reports when a frame begins.
type HmdState struct {
	Frame     int   `json:"frame"`
	StartTs   int64 `json:"start_ts"`
	RefreshHz int   `json:"refresh_hz"`
}

// SharedItem is one entry of the scene visibility set shared by both eyes.
type SharedItem struct {
	ID            uint32  `json:"id"`
	InLeft        bool    `json:"in_left"`
	InRight       bool    `json:"in_right"`
	Verts         int     `json:"verts"`
	Reprojections int     `json:"reprojections"`
	WarpX         float32 `json:"warp_x"`
	WarpY         float32 `json:"warp_y"`
}

// VisibilitySet is the read only visibility list produced by the scene pass.
type VisibilitySet struct {
	Frame int          `json:"frame"`
	Items []SharedItem `json:"items"`
}

// DrawItem is one draw call handed to one eye.
type DrawItem struct {
	ID    uint32 `json:"id"`
	Verts int    `json:"verts"`
	Eye   Eye    `json:"eye"`
}

// StereoBudget bounds one eye of one frame.
type StereoBudget struct {
	MaxItemsPerEye   int `json:"max_items_per_eye"`
	MaxVertsPerEye   int `json:"max_verts_per_eye"`
	MaxReprojections int `json:"max_reprojections"`
}

// FrameReport is what EndFrame hands back to the compositor.
type FrameReport struct {
	Frame      int  `json:"frame"`
	LeftItems  int  `json:"left_items"`
	RightItems int  `json:"right_items"`
	LeftPose   Pose `json:"left_pose"`
	RightPose  Pose `json:"right_pose"`
}

// StereoStats is the per submitter counter block.
type StereoStats struct {
	Frames  int `json:"frames"`
	Latches int `json:"latches"`
	Submits int `json:"submits"`
	Culled  int `json:"culled"`
	Budget  int `json:"budget"`
}

// Errors returned by the submitter.
var (
	ErrBadHmd         = errors.New("hmd state is not usable")
	ErrFrameOpen      = errors.New("a frame is already open")
	ErrFrameClosed    = errors.New("no frame is open")
	ErrBadEye         = errors.New("eye is not left or right")
	ErrBadPose        = errors.New("pose is not usable")
	ErrAlreadyLatched = errors.New("pose is already latched for this frame")
	ErrNoPose         = errors.New("no pose has been reported for this eye")
	ErrStalePose      = errors.New("pose timestamp is older than the frame start")
	ErrBadVisibility  = errors.New("visibility set is not usable")
	ErrNoVisibility   = errors.New("no visibility set was shared for this frame")
	ErrEyeBudget      = errors.New("eye exceeds its per frame budget")
	ErrSubmitOrder    = errors.New("eyes must be submitted once each, left before right")
	ErrBadBudget      = errors.New("stereo budget is not usable")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// StereoFrameSubmitter owns the per frame poses, the shared visibility set and
// the submit bookkeeping.
type StereoFrameSubmitter struct {
	budget StereoBudget
}

// NewStereoFrameSubmitter builds a submitter with the given budget.
func NewStereoFrameSubmitter(budget StereoBudget) *StereoFrameSubmitter {
	return &StereoFrameSubmitter{budget: budget}
}

// SetBudget replaces the per eye caps.
func (s *StereoFrameSubmitter) SetBudget(budget StereoBudget) error {
	return errNotImplemented
}

// BeginFrame opens one frame for the given headset state.
func (s *StereoFrameSubmitter) BeginFrame(hmd HmdState) error {
	return errNotImplemented
}

// SetEyePose records a pose reported for one eye.
func (s *StereoFrameSubmitter) SetEyePose(eye Eye, pose Pose, ts int64) error {
	return errNotImplemented
}

// LatchPose freezes the pose an eye will render with this frame.
func (s *StereoFrameSubmitter) LatchPose(eye Eye, at int64) (Pose, error) {
	return Pose{}, errNotImplemented
}

// ShareVisibility installs the read only visibility set of the frame.
func (s *StereoFrameSubmitter) ShareVisibility(vis VisibilitySet) error {
	return errNotImplemented
}

// SharedIDs reports the identifiers of the shared visibility set, ascending.
func (s *StereoFrameSubmitter) SharedIDs() []uint32 {
	return nil
}

// PerEyeCull filters the shared visibility set for one eye.
func (s *StereoFrameSubmitter) PerEyeCull(eye Eye) ([]DrawItem, error) {
	return nil, errNotImplemented
}

// Submit records the draw items of one eye.
func (s *StereoFrameSubmitter) Submit(eye Eye, items []DrawItem) error {
	return errNotImplemented
}

// EndFrame closes the frame and reports what was submitted.
func (s *StereoFrameSubmitter) EndFrame() (FrameReport, error) {
	return FrameReport{}, errNotImplemented
}

// Stats reports the counters of the submitter.
func (s *StereoFrameSubmitter) Stats() StereoStats {
	return StereoStats{}
}

// Snapshot serialises the frame number, the latched poses and the budget.
func (s *StereoFrameSubmitter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *StereoFrameSubmitter) Restore(data []byte) error {
	return errNotImplemented
}
