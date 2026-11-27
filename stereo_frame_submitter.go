// Package task submits stereo VR frames: one shared visibility set, per eye
// culling against a latched pose and a fixed left then right submit order.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
)

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

// snapshotMagic identifies payloads produced by Snapshot.
const snapshotMagic = "stereo-frame-submitter/v1"

// poseSample is one reported pose with its capture timestamp.
type poseSample struct {
	ts   int64
	pose Pose
}

// snapshotPayload is the serialised frame boundary state.
type snapshotPayload struct {
	Magic     string       `json:"magic"`
	Frame     int          `json:"frame"`
	Budget    StereoBudget `json:"budget"`
	LeftPose  Pose         `json:"left_pose"`
	RightPose Pose         `json:"right_pose"`
	Stats     StereoStats  `json:"stats"`
}

// StereoFrameSubmitter owns the per frame poses, the shared visibility set and
// the submit bookkeeping. All methods are safe for concurrent use; frame
// state only becomes visible as a whole.
type StereoFrameSubmitter struct {
	mu          sync.Mutex
	budget      StereoBudget
	open        bool
	frame       int
	startTs     int64
	poses       [EyeCount][]poseSample
	latched     [EyeCount]bool
	latchPoses  [EyeCount]Pose
	lastLatched [EyeCount]Pose
	vis         []SharedItem
	hasVis      bool
	submitted   [EyeCount]bool
	counts      [EyeCount]int
	stats       StereoStats
}

// normalizeBudget fills zero fields with defaults and rejects negative caps.
func normalizeBudget(b StereoBudget) (StereoBudget, error) {
	if b.MaxItemsPerEye < 0 || b.MaxVertsPerEye < 0 || b.MaxReprojections < 0 {
		return StereoBudget{}, ErrBadBudget
	}
	if b.MaxItemsPerEye == 0 {
		b.MaxItemsPerEye = DefaultMaxItemsPerEye
	}
	if b.MaxVertsPerEye == 0 {
		b.MaxVertsPerEye = DefaultMaxVertsPerEye
	}
	if b.MaxReprojections == 0 {
		b.MaxReprojections = DefaultMaxReprojections
	}
	return b, nil
}

// validEye reports whether eye is one of the two physical eyes.
func validEye(eye Eye) bool {
	return eye == EyeLeft || eye == EyeRight
}

// NewStereoFrameSubmitter builds a submitter with the given budget.
func NewStereoFrameSubmitter(budget StereoBudget) *StereoFrameSubmitter {
	normalized, err := normalizeBudget(budget)
	if err != nil {
		normalized, _ = normalizeBudget(StereoBudget{})
	}
	return &StereoFrameSubmitter{budget: normalized}
}

// SetBudget replaces the per eye caps.
func (s *StereoFrameSubmitter) SetBudget(budget StereoBudget) error {
	normalized, err := normalizeBudget(budget)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.budget = normalized
	s.mu.Unlock()
	return nil
}

// BeginFrame opens one frame for the given headset state.
func (s *StereoFrameSubmitter) BeginFrame(hmd HmdState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open {
		return ErrFrameOpen
	}
	if hmd.StartTs <= 0 || hmd.RefreshHz <= 0 || hmd.Frame <= s.frame {
		return ErrBadHmd
	}
	s.open = true
	s.frame = hmd.Frame
	s.startTs = hmd.StartTs
	s.latched = [EyeCount]bool{}
	s.submitted = [EyeCount]bool{}
	s.counts = [EyeCount]int{}
	s.vis = nil
	s.hasVis = false
	return nil
}

// SetEyePose records a pose reported for one eye.
func (s *StereoFrameSubmitter) SetEyePose(eye Eye, pose Pose, ts int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return ErrFrameClosed
	}
	if !validEye(eye) {
		return ErrBadEye
	}
	if pose == (Pose{}) {
		return ErrBadPose
	}
	hist := s.poses[eye]
	if n := len(hist); n > 0 && ts < hist[n-1].ts {
		return ErrStalePose
	}
	s.poses[eye] = append(hist, poseSample{ts: ts, pose: pose})
	return nil
}

// LatchPose freezes the pose an eye will render with this frame.
func (s *StereoFrameSubmitter) LatchPose(eye Eye, at int64) (Pose, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return Pose{}, ErrFrameClosed
	}
	if !validEye(eye) {
		return Pose{}, ErrBadEye
	}
	if s.latched[eye] {
		return Pose{}, ErrAlreadyLatched
	}
	hist := s.poses[eye]
	best := -1
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].ts <= at {
			best = i
			break
		}
	}
	if best < 0 {
		return Pose{}, ErrNoPose
	}
	pose := hist[best].pose
	s.latched[eye] = true
	s.latchPoses[eye] = pose
	s.lastLatched[eye] = pose
	s.stats.Latches++
	return pose, nil
}

// ShareVisibility installs the read only visibility set of the frame.
func (s *StereoFrameSubmitter) ShareVisibility(vis VisibilitySet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return ErrFrameClosed
	}
	seen := make(map[uint32]struct{}, len(vis.Items))
	for _, item := range vis.Items {
		if _, dup := seen[item.ID]; dup {
			return ErrBadVisibility
		}
		seen[item.ID] = struct{}{}
		if !item.InLeft && !item.InRight {
			return ErrBadVisibility
		}
	}
	items := make([]SharedItem, len(vis.Items))
	copy(items, vis.Items)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	s.vis = items
	s.hasVis = true
	return nil
}

// SharedIDs reports the identifiers of the shared visibility set, ascending.
func (s *StereoFrameSubmitter) SharedIDs() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasVis {
		return nil
	}
	ids := make([]uint32, len(s.vis))
	for i, item := range s.vis {
		ids[i] = item.ID
	}
	return ids
}

// outOfClip reports whether a reprojected item still lies outside clip space.
func outOfClip(item SharedItem) bool {
	return item.WarpX < -1 || item.WarpX > 1 || item.WarpY < -1 || item.WarpY > 1
}

// PerEyeCull filters the shared visibility set for one eye.
func (s *StereoFrameSubmitter) PerEyeCull(eye Eye) ([]DrawItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return nil, ErrFrameClosed
	}
	if !validEye(eye) {
		return nil, ErrBadEye
	}
	if !s.hasVis {
		return nil, ErrNoVisibility
	}
	var items []DrawItem
	verts := 0
	for _, item := range s.vis {
		visible := item.InLeft
		if eye == EyeRight {
			visible = item.InRight
		}
		if !visible {
			continue
		}
		if outOfClip(item) && item.Reprojections >= s.budget.MaxReprojections {
			s.stats.Culled++
			continue
		}
		items = append(items, DrawItem{ID: item.ID, Verts: item.Verts, Eye: eye})
		verts += item.Verts
	}
	if len(items) > s.budget.MaxItemsPerEye || verts > s.budget.MaxVertsPerEye {
		s.stats.Budget++
		return nil, ErrEyeBudget
	}
	return items, nil
}

// Submit records the draw items of one eye.
func (s *StereoFrameSubmitter) Submit(eye Eye, items []DrawItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return ErrFrameClosed
	}
	if !validEye(eye) {
		return ErrBadEye
	}
	if s.submitted[eye] || (eye == EyeRight && !s.submitted[EyeLeft]) {
		return ErrSubmitOrder
	}
	s.submitted[eye] = true
	s.counts[eye] = len(items)
	s.stats.Submits++
	return nil
}

// EndFrame closes the frame and reports what was submitted.
func (s *StereoFrameSubmitter) EndFrame() (FrameReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return FrameReport{}, ErrFrameClosed
	}
	if !s.submitted[EyeLeft] || !s.submitted[EyeRight] {
		return FrameReport{}, ErrSubmitOrder
	}
	report := FrameReport{
		Frame:      s.frame,
		LeftItems:  s.counts[EyeLeft],
		RightItems: s.counts[EyeRight],
		LeftPose:   s.latchPoses[EyeLeft],
		RightPose:  s.latchPoses[EyeRight],
	}
	s.open = false
	s.stats.Frames++
	return report, nil
}

// Stats reports the counters of the submitter.
func (s *StereoFrameSubmitter) Stats() StereoStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Snapshot serialises the frame number, the latched poses and the budget.
// Snapshots are only taken on a frame boundary.
func (s *StereoFrameSubmitter) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open {
		return nil, ErrFrameOpen
	}
	return json.Marshal(snapshotPayload{
		Magic:     snapshotMagic,
		Frame:     s.frame,
		Budget:    s.budget,
		LeftPose:  s.lastLatched[EyeLeft],
		RightPose: s.lastLatched[EyeRight],
		Stats:     s.stats,
	})
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *StereoFrameSubmitter) Restore(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Magic != snapshotMagic {
		return ErrBadSnapshot
	}
	if _, err := normalizeBudget(payload.Budget); err != nil {
		return ErrBadSnapshot
	}
	s.frame = payload.Frame
	s.budget = payload.Budget
	s.lastLatched[EyeLeft] = payload.LeftPose
	s.lastLatched[EyeRight] = payload.RightPose
	s.stats = payload.Stats
	s.open = false
	s.startTs = 0
	s.poses = [EyeCount][]poseSample{}
	s.latched = [EyeCount]bool{}
	s.latchPoses = [EyeCount]Pose{payload.LeftPose, payload.RightPose}
	s.submitted = [EyeCount]bool{}
	s.counts = [EyeCount]int{}
	s.vis = nil
	s.hasVis = false
	return nil
}
