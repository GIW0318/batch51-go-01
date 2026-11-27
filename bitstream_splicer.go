// Package task joins independently encoded video and audio segments into one
// decodable bitstream, judging parameter set compatibility and rebasing the
// presentation timeline onto a single output timebase.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// SegID identifies one added segment.
type SegID string

// SegKind tells a segment apart from the audio side of the mux.
type SegKind string

// Supported segment kinds.
const (
	KindVideo SegKind = "video"
	KindAudio SegKind = "audio"
)

// FrameKind is the slice type of one coded frame.
type FrameKind string

// Supported frame kinds.
const (
	FrameIDR FrameKind = "idr"
	FrameP   FrameKind = "p"
	FrameB   FrameKind = "b"
)

// Contract defaults applied when Limits leaves a field unset.
const (
	DefaultMaxSegments  = 16
	DefaultMaxFrames    = 8192
	DefaultMaxParamSets = 8
	DefaultMaxDuration  = 900000
)

// Timebase is the tick rate of the output timeline, expressed as Num/Den
// seconds per tick.
type Timebase struct {
	Num int64
	Den int64
}

// ParamSet is the decoder configuration a video segment was encoded against.
type ParamSet struct {
	SPSID   int
	PPSID   int
	Profile int
	Level   int
	Chroma  int
	Width   int
	Height  int
}

// Frame is one coded frame described by its own segment local timestamps.
type Frame struct {
	PTS  int64
	DTS  int64
	Kind FrameKind
	Ref  bool
}

// Segment is one independently encoded chunk handed to the splicer.
type Segment struct {
	ID       SegID
	Kind     SegKind
	Params   ParamSet
	Timebase Timebase
	Frames   []Frame
}

// SegmentInfo is the read only view returned by Segments.
type SegmentInfo struct {
	ID       SegID
	Kind     SegKind
	Params   ParamSet
	Frames   int
	FirstPTS int64
	LastPTS  int64
}

// Limits bounds the segment table, the frame store and the parameter set
// cache.
type Limits struct {
	MaxSegments  int
	MaxFrames    int
	MaxParamSets int
	MaxDuration  int64
}

// CompatReport is the explainable answer of CheckCompatibility.
type CompatReport struct {
	A            SegID
	B            SegID
	Compatible   bool
	Reasons      []string
	NeedParamSet bool
	NeedReset    bool
}

// SplicePlan is the outcome of aligning a requested cut to a decodable frame.
type SplicePlan struct {
	Revision  int
	Requested int
	Cut       int
	Wait      int
	Dropped   int
	NeedReset bool
	Segments  []SegID
}

// OutFrame is one frame of the produced bitstream.
type OutFrame struct {
	PTS  int64
	DTS  int64
	Kind FrameKind
	Ref  bool
	Seg  SegID
}

// Bitstream is the splice result together with the parameter sets and decoder
// reset points it needs.
type Bitstream struct {
	Frames    []OutFrame
	ParamSets []ParamSet
	ResetPTS  []int64
	Revision  int
}

// SpliceStats is the running counter set of one splicer.
type SpliceStats struct {
	Segments  int
	Frames    int
	ParamSets int
	Splices   int
	Resets    int
	Dropped   int
	Duration  int64
}

// BitstreamSplicer owns the segment table, the parameter set cache and the
// rebase ledger.
type BitstreamSplicer struct {
	limits Limits
}

// NewBitstreamSplicer builds a splicer for the given caps.
func NewBitstreamSplicer(limits Limits) *BitstreamSplicer {
	return &BitstreamSplicer{limits: limits}
}

// AddSegment appends one segment and returns its identifier.
func (s *BitstreamSplicer) AddSegment(seg Segment) (SegID, error) {
	return "", errNotImplemented
}

// CheckCompatibility judges whether two segments can share one decoder
// configuration.
func (s *BitstreamSplicer) CheckCompatibility(a, b SegID) (CompatReport, error) {
	return CompatReport{}, errNotImplemented
}

// PlanSplice aligns a requested frame index to the next decodable cut point.
func (s *BitstreamSplicer) PlanSplice(at int) (SplicePlan, error) {
	return SplicePlan{}, errNotImplemented
}

// Splice materialises the bitstream described by one plan.
func (s *BitstreamSplicer) Splice(plan SplicePlan) (Bitstream, error) {
	return Bitstream{}, errNotImplemented
}

// SetTimebase replaces the output timebase used by later splices.
func (s *BitstreamSplicer) SetTimebase(tb Timebase) error {
	return errNotImplemented
}

// Timebase reports the output timebase currently in force.
func (s *BitstreamSplicer) Timebase() Timebase {
	return Timebase{}
}

// SetGopAlign turns IDR boundary alignment on or off.
func (s *BitstreamSplicer) SetGopAlign(on bool) error {
	return errNotImplemented
}

// GopAlign reports whether IDR boundary alignment is in force.
func (s *BitstreamSplicer) GopAlign() bool {
	return false
}

// Stats returns a copy of the running counters.
func (s *BitstreamSplicer) Stats() SpliceStats {
	return SpliceStats{}
}

// Segments lists the added segments in insertion order.
func (s *BitstreamSplicer) Segments() []SegmentInfo {
	return nil
}

// ParamSets lists the distinct video parameter sets in first seen order.
func (s *BitstreamSplicer) ParamSets() []ParamSet {
	return nil
}

// Reset drops every segment and zeroes the counters.
func (s *BitstreamSplicer) Reset() error {
	return errNotImplemented
}

// Snapshot serialises the segment index, the rebase ledger and the counters.
func (s *BitstreamSplicer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *BitstreamSplicer) Restore(data []byte) error {
	return errNotImplemented
}

// Errors reported by the splicer.
var (
	ErrBadSegment       = errors.New("segment fields are invalid")
	ErrEmptySegment     = errors.New("segment carries no frames")
	ErrBadFrame         = errors.New("frame kind is not idr, p or b")
	ErrBadTiming        = errors.New("frame timestamps must be non-negative and non-decreasing")
	ErrBadParamSet      = errors.New("video parameter set is incomplete")
	ErrDuplicateSegment = errors.New("segment id already exists")
	ErrSegmentLimit     = errors.New("segment table is full")
	ErrFrameBudget      = errors.New("frame store budget exceeded")
	ErrParamSetLimit    = errors.New("parameter set cache is full")
	ErrDurationLimit    = errors.New("total segment duration exceeds the cap")
	ErrNoIDR            = errors.New("video segment has no idr frame")
	ErrUnknownSegment   = errors.New("unknown segment")
	ErrBadSplicePoint   = errors.New("splice point is outside the frame store")
	ErrBadPlan          = errors.New("splice plan does not fit the current state")
	ErrPlanStale        = errors.New("splice plan was built against an older segment table")
	ErrBadTimebase      = errors.New("timebase numerator and denominator must be positive")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)
