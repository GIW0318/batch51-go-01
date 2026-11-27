// Package task plans a crop window path that reframes a wide source into a
// target aspect ratio, following the subject track while keeping faces and
// subtitles inside the frame.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Contract defaults applied when Limits or the configuration leaves a field
// unset.
const (
	DefaultAspect    = 16.0 / 9.0
	DefaultSmoothing = 3
	DefaultInterval  = 30
	MaxSmoothing     = 32
	MaxInterval      = 600
	DefaultKeyframes = 2048
	DefaultSamples   = 4096
)

// Errors reported by the planner.
var (
	ErrNoSource       = errors.New("source frame size is not set")
	ErrBadSource      = errors.New("source width and height must be positive")
	ErrBadAspect      = errors.New("target aspect is outside the accepted range")
	ErrNoTrack        = errors.New("subject track is not set")
	ErrBadTrack       = errors.New("subject track is empty or malformed")
	ErrSampleBudget   = errors.New("subject track exceeds the sample budget")
	ErrBadSafeRect    = errors.New("safe rectangle is empty or has a negative origin")
	ErrBadWindow      = errors.New("smoothing window is outside the accepted range")
	ErrBadInterval    = errors.New("keyframe interval is outside the accepted range")
	ErrBadRange       = errors.New("planning range is empty or starts before zero")
	ErrKeyframeBudget = errors.New("keyframe count exceeds the budget")
	ErrInvalidPlan    = errors.New("reframe plan leaves the source frame")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Rect is an axis aligned rectangle in source pixels.
type Rect struct {
	X float64
	Y float64
	W float64
	H float64
}

// SubjectSample is one tracked subject position at a source timestamp.
type SubjectSample struct {
	TS         int64
	X          float64
	Y          float64
	Confidence float64
}

// Keyframe is one crop window of the plan.
type Keyframe struct {
	TS int64
	X  float64
	Y  float64
	W  float64
	H  float64
}

// ReframePlan is the crop window path for one range of source frames.
type ReframePlan struct {
	SourceW    int
	SourceH    int
	Aspect     float64
	Keyframes  []Keyframe
	Frames     int
	Holes      int
	SafeShifts int
}

// ReframeStats is the running counter set of one planner.
type ReframeStats struct {
	Plans      int
	Keyframes  int
	Holes      int
	SafeShifts int
	LastStart  int64
	LastEnd    int64
}

// Limits bounds the plan size of the planner.
type Limits struct {
	MaxKeyframes int
	MaxSamples   int
}

// AutoReframePlanner owns the source size, the subject track, the safe
// rectangles and the crop window history.
type AutoReframePlanner struct {
	limits Limits
}

// NewAutoReframePlanner builds a planner for the given caps.
func NewAutoReframePlanner(limits Limits) *AutoReframePlanner {
	return &AutoReframePlanner{limits: limits}
}

// SetSource records the source frame size.
func (p *AutoReframePlanner) SetSource(width, height int) error {
	return errNotImplemented
}

// SetTargetAspect replaces the target aspect ratio.
func (p *AutoReframePlanner) SetTargetAspect(a float64) error {
	return errNotImplemented
}

// SetSubjectTrack replaces the subject track.
func (p *AutoReframePlanner) SetSubjectTrack(track []SubjectSample) error {
	return errNotImplemented
}

// SetSafeRects replaces the rectangles that must stay visible.
func (p *AutoReframePlanner) SetSafeRects(rects []Rect) error {
	return errNotImplemented
}

// SetSmoothing replaces the number of keyframes averaged for the crop window.
func (p *AutoReframePlanner) SetSmoothing(window int) error {
	return errNotImplemented
}

// SetKeyframeInterval replaces the keyframe spacing in source frames.
func (p *AutoReframePlanner) SetKeyframeInterval(frames int) error {
	return errNotImplemented
}

// Plan builds the crop window path for the given source range.
func (p *AutoReframePlanner) Plan(start, end int64) (ReframePlan, error) {
	return ReframePlan{}, errNotImplemented
}

// Validate checks that a plan stays inside the source frame.
func (p *AutoReframePlanner) Validate(plan ReframePlan) error {
	return errNotImplemented
}

// Stats returns a copy of the running counters.
func (p *AutoReframePlanner) Stats() ReframeStats {
	return ReframeStats{}
}

// TargetAspect reports the target aspect ratio in force.
func (p *AutoReframePlanner) TargetAspect() float64 {
	return 0
}

// Smoothing reports the smoothing window in force.
func (p *AutoReframePlanner) Smoothing() int {
	return 0
}

// KeyframeInterval reports the keyframe spacing in force.
func (p *AutoReframePlanner) KeyframeInterval() int {
	return 0
}

// Snapshot serialises the source size, the track, the safe rectangles and the
// counters.
func (p *AutoReframePlanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *AutoReframePlanner) Restore(data []byte) error {
	return errNotImplemented
}
