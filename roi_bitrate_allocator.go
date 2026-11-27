// Package task allocates per block QP offsets for region of interest encoding
// and keeps the total bitrate inside the configured target.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Contract defaults applied when Limits or the configuration leaves a field
// unset.
const (
	DefaultBlockSize  = 16
	DefaultQPMin      = 10
	DefaultQPMax      = 45
	DefaultSmooth     = 4
	DefaultMaxBlocks  = 4096
	DefaultMaxSamples = 65536
	MaxRoiWeight      = 4.0
)

// Errors reported by the allocator.
var (
	ErrBadRect       = errors.New("roi rectangle is empty or has a negative origin")
	ErrBadWeight     = errors.New("roi weight is outside the accepted range")
	ErrBadFrame      = errors.New("frame dimensions are invalid")
	ErrBlockBudget   = errors.New("frame block count exceeds the budget")
	ErrBadSaliency   = errors.New("saliency map dimensions do not match its samples")
	ErrSampleBudget  = errors.New("saliency sample count exceeds the budget")
	ErrBadQPRange    = errors.New("qp range is out of bounds or inverted")
	ErrBadWindow     = errors.New("smoothing window is outside the accepted range")
	ErrBadBits       = errors.New("target bits must be positive")
	ErrRoiOutOfFrame = errors.New("roi rectangle is not fully inside the frame")
	ErrUnknownPlan   = errors.New("no pending plan for this frame")
	ErrStaleFrame    = errors.New("frame was already committed")
	ErrBadTable      = errors.New("qp table does not match the planned grid")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Rect is a pixel aligned region of interest.
type Rect struct {
	X int
	Y int
	W int
	H int
}

// Frame describes the picture the QP table is planned for.
type Frame struct {
	ID        int
	Width     int
	Height    int
	BlockSize int
}

// SaliencyMap is a coarse per sample importance map in the 0..255 range.
type SaliencyMap struct {
	Width  int
	Height int
	Values []uint8
}

// QPTable is the per block quantiser decision for one frame.
type QPTable struct {
	FrameID   int
	BlockCols int
	BlockRows int
	Base      int
	QP        []int
	ROIBlocks int
}

// RoiStats is the running counter set of one allocator.
type RoiStats struct {
	Frames      int
	Committed   int
	Clamped     int
	ROIFrames   int
	Rejected    int
	LastFrameID int
	LastBase    int
}

// Limits bounds the per frame work of the allocator.
type Limits struct {
	MaxBlocks  int
	MaxSamples int
}

// RoiBitrateAllocator owns the region of interest, the saliency map, the QP
// bounds and the per block smoothing history.
type RoiBitrateAllocator struct {
	limits Limits
}

// NewRoiBitrateAllocator builds an allocator for the given caps.
func NewRoiBitrateAllocator(limits Limits) *RoiBitrateAllocator {
	return &RoiBitrateAllocator{limits: limits}
}

// SetRoi installs one region of interest with its weight.
func (a *RoiBitrateAllocator) SetRoi(rect Rect, weight float32) error {
	return errNotImplemented
}

// ClearRoi removes the region of interest.
func (a *RoiBitrateAllocator) ClearRoi() error {
	return errNotImplemented
}

// SetSaliencyMap installs the saliency map used to weight blocks.
func (a *RoiBitrateAllocator) SetSaliencyMap(m SaliencyMap) error {
	return errNotImplemented
}

// PlanQP plans the per block QP table for one frame.
func (a *RoiBitrateAllocator) PlanQP(frame Frame, targetBits int) (QPTable, error) {
	return QPTable{}, errNotImplemented
}

// SetQPRange replaces the inclusive QP bounds.
func (a *RoiBitrateAllocator) SetQPRange(min, max int) error {
	return errNotImplemented
}

// SetSmoothWindow replaces the number of frames the per block step limit is
// derived from.
func (a *RoiBitrateAllocator) SetSmoothWindow(frames int) error {
	return errNotImplemented
}

// MarkSceneCut drops the smoothing history at a scene boundary.
func (a *RoiBitrateAllocator) MarkSceneCut() error {
	return errNotImplemented
}

// Commit records one planned table as the new smoothing history.
func (a *RoiBitrateAllocator) Commit(qp QPTable) error {
	return errNotImplemented
}

// Stats returns a copy of the running counters.
func (a *RoiBitrateAllocator) Stats() RoiStats {
	return RoiStats{}
}

// Roi reports the installed region of interest.
func (a *RoiBitrateAllocator) Roi() (Rect, float32, bool) {
	return Rect{}, 0, false
}

// QPRange reports the inclusive QP bounds in force.
func (a *RoiBitrateAllocator) QPRange() (int, int) {
	return 0, 0
}

// SmoothWindow reports the smoothing window in force.
func (a *RoiBitrateAllocator) SmoothWindow() int {
	return 0
}

// Snapshot serialises the roi, the saliency map, the bounds, the smoothing
// history and the counters.
func (a *RoiBitrateAllocator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *RoiBitrateAllocator) Restore(data []byte) error {
	return errNotImplemented
}
