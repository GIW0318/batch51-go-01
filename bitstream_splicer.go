// Package task joins independently encoded video and audio segments into one
// decodable bitstream, judging parameter set compatibility and rebasing the
// presentation timeline onto a single output timebase.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"sort"
	"strconv"
	"sync"
)

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
	mu sync.RWMutex

	limits       Limits
	segments     []Segment
	segmentIndex map[SegID]int
	paramSets    []ParamSet
	paramSetSeen map[ParamSet]struct{}

	totalFrames int
	duration    int64
	revision    int
	nextID      int

	timebase Timebase
	gopAlign bool
	stats    SpliceStats
}

// NewBitstreamSplicer builds a splicer for the given caps.
func NewBitstreamSplicer(limits Limits) *BitstreamSplicer {
	return &BitstreamSplicer{
		limits:       normalizeLimits(limits),
		segmentIndex: make(map[SegID]int),
		paramSetSeen: make(map[ParamSet]struct{}),
		timebase:     Timebase{Num: 1, Den: 1},
		gopAlign:     true,
	}
}

func normalizeLimits(l Limits) Limits {
	if l.MaxSegments <= 0 {
		l.MaxSegments = DefaultMaxSegments
	}
	if l.MaxFrames <= 0 {
		l.MaxFrames = DefaultMaxFrames
	}
	if l.MaxParamSets <= 0 {
		l.MaxParamSets = DefaultMaxParamSets
	}
	if l.MaxDuration <= 0 {
		l.MaxDuration = DefaultMaxDuration
	}
	return l
}

func validSegmentKind(kind SegKind) bool {
	return kind == KindVideo || kind == KindAudio
}

func validFrameKind(kind FrameKind) bool {
	return kind == FrameIDR || kind == FrameP || kind == FrameB
}

func validateSegmentFields(seg Segment) error {
	if !validSegmentKind(seg.Kind) {
		return ErrBadSegment
	}
	if len(seg.Frames) == 0 {
		return ErrEmptySegment
	}

	var previousPTS int64
	for i, frame := range seg.Frames {
		if !validFrameKind(frame.Kind) {
			return ErrBadFrame
		}
		if frame.PTS < 0 || (i > 0 && frame.PTS < previousPTS) {
			return ErrBadTiming
		}
		previousPTS = frame.PTS
	}

	if seg.Kind == KindVideo {
		if seg.Params.Width <= 0 || seg.Params.Height <= 0 {
			return ErrBadParamSet
		}
		hasIDR := false
		for _, frame := range seg.Frames {
			if frame.Kind == FrameIDR {
				hasIDR = true
				break
			}
		}
		if !hasIDR {
			return ErrNoIDR
		}
	}
	return nil
}

func copyFrames(frames []Frame) []Frame {
	out := make([]Frame, len(frames))
	copy(out, frames)
	return out
}

func copySegment(seg Segment) Segment {
	seg.Frames = copyFrames(seg.Frames)
	return seg
}

func segmentDuration(seg Segment) int64 {
	if len(seg.Frames) == 0 {
		return 0
	}
	return seg.Frames[len(seg.Frames)-1].PTS - seg.Frames[0].PTS
}

func (s *BitstreamSplicer) nextAutoIDLocked() (SegID, int) {
	next := s.nextID
	for {
		next++
		id := SegID("seg-" + strconv.Itoa(next))
		if _, exists := s.segmentIndex[id]; !exists {
			return id, next
		}
	}
}

// AddSegment appends one segment and returns its identifier.
func (s *BitstreamSplicer) AddSegment(seg Segment) (SegID, error) {
	if err := validateSegmentFields(seg); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := seg.ID
	nextID := s.nextID
	if id == "" {
		id, nextID = s.nextAutoIDLocked()
	} else if _, exists := s.segmentIndex[id]; exists {
		return "", ErrDuplicateSegment
	}

	if len(s.segments) >= s.limits.MaxSegments {
		return "", ErrSegmentLimit
	}

	needsParamSet := false
	if seg.Kind == KindVideo {
		if _, cached := s.paramSetSeen[seg.Params]; !cached {
			if len(s.paramSets) >= s.limits.MaxParamSets {
				return "", ErrParamSetLimit
			}
			needsParamSet = true
		}
	}

	if len(seg.Frames) > s.limits.MaxFrames-s.totalFrames {
		return "", ErrFrameBudget
	}

	duration := segmentDuration(seg)
	if duration > s.limits.MaxDuration-s.duration {
		return "", ErrDurationLimit
	}

	stored := copySegment(seg)
	stored.ID = id
	s.nextID = nextID
	s.segments = append(s.segments, stored)
	s.segmentIndex[id] = len(s.segments) - 1
	s.totalFrames += len(stored.Frames)
	s.duration += duration
	s.revision++

	if needsParamSet {
		s.paramSets = append(s.paramSets, seg.Params)
		s.paramSetSeen[seg.Params] = struct{}{}
	}

	s.stats.Segments = len(s.segments)
	s.stats.Frames = s.totalFrames
	s.stats.ParamSets = len(s.paramSets)
	s.stats.Duration = s.duration

	return id, nil
}

// CheckCompatibility judges whether two segments can share one decoder
// configuration.
func (s *BitstreamSplicer) CheckCompatibility(a, b SegID) (CompatReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	indexA, ok := s.segmentIndex[a]
	if !ok {
		return CompatReport{}, ErrUnknownSegment
	}
	indexB, ok := s.segmentIndex[b]
	if !ok {
		return CompatReport{}, ErrUnknownSegment
	}

	segA := s.segments[indexA]
	segB := s.segments[indexB]
	report := CompatReport{A: a, B: b}

	if segA.Kind != segB.Kind {
		report.Reasons = []string{"kind"}
		report.NeedParamSet = true
		report.NeedReset = true
		return report, nil
	}

	if segA.Kind == KindAudio {
		report.Compatible = true
		return report, nil
	}

	reasons := make([]string, 0, 6)
	if segA.Params.Chroma != segB.Params.Chroma {
		reasons = append(reasons, "chroma")
	}
	if segA.Params.Height != segB.Params.Height {
		reasons = append(reasons, "height")
	}
	if segA.Params.Level != segB.Params.Level {
		reasons = append(reasons, "level")
	}
	if segA.Params.Profile != segB.Params.Profile {
		reasons = append(reasons, "profile")
	}
	if segA.Params.Width != segB.Params.Width {
		reasons = append(reasons, "width")
	}
	sort.Strings(reasons)

	if len(reasons) == 0 {
		report.Compatible = true
		return report, nil
	}

	report.Reasons = reasons
	report.NeedParamSet = true
	report.NeedReset = true
	return report, nil
}

func frameAt(segments []Segment, index int) (segmentIndex, frameIndex int, ok bool) {
	if index < 0 {
		return 0, 0, false
	}
	seen := 0
	for si := range segments {
		next := seen + len(segments[si].Frames)
		if index < next {
			return si, index - seen, true
		}
		seen = next
	}
	return 0, 0, false
}

func (s *BitstreamSplicer) needResetAtCutLocked(cut, total int) bool {
	if cut <= 0 || cut >= total {
		return false
	}
	leftSeg, _, okLeft := frameAt(s.segments, cut-1)
	rightSeg, _, okRight := frameAt(s.segments, cut)
	if !okLeft || !okRight {
		return false
	}
	if s.segments[leftSeg].Kind != KindVideo || s.segments[rightSeg].Kind != KindVideo {
		return false
	}
	return s.segments[leftSeg].Params != s.segments[rightSeg].Params
}

func (s *BitstreamSplicer) planSegmentsLocked(requested, cut int) []SegID {
	out := make([]SegID, 0, len(s.segments))
	seen := make(map[SegID]struct{}, len(s.segments))
	global := 0
	for _, seg := range s.segments {
		included := false
		for i := range seg.Frames {
			index := global + i
			if index < requested || index >= cut {
				included = true
				break
			}
		}
		if included {
			if _, exists := seen[seg.ID]; !exists {
				seen[seg.ID] = struct{}{}
				out = append(out, seg.ID)
			}
		}
		global += len(seg.Frames)
	}
	return out
}

// PlanSplice aligns a requested frame index to the next decodable cut point.
func (s *BitstreamSplicer) PlanSplice(at int) (SplicePlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := s.totalFrames
	if at < 0 || at >= total {
		return SplicePlan{}, ErrBadSplicePoint
	}

	cut := at
	wait := 0
	dropped := 0
	if s.gopAlign {
		segAt, frameAtInSeg, ok := frameAt(s.segments, at)
		atIsVideoIDR := ok && s.segments[segAt].Kind == KindVideo &&
			s.segments[segAt].Frames[frameAtInSeg].Kind == FrameIDR
		hasVideo := false
		for _, seg := range s.segments {
			if seg.Kind == KindVideo {
				hasVideo = true
				break
			}
		}
		if hasVideo && !atIsVideoIDR {
			found := false
			for index := at; index < total; index++ {
				si, fi, ok := frameAt(s.segments, index)
				if ok && s.segments[si].Kind == KindVideo && s.segments[si].Frames[fi].Kind == FrameIDR {
					cut = index
					found = true
					break
				}
			}
			if !found {
				return SplicePlan{}, ErrNoIDR
			}
			wait = cut - at
			for index := at; index < cut; index++ {
				si, fi, ok := frameAt(s.segments, index)
				if ok && !s.segments[si].Frames[fi].Ref {
					dropped++
				}
			}
		}
	}

	return SplicePlan{
		Revision:  s.revision,
		Requested: at,
		Cut:       cut,
		Wait:      wait,
		Dropped:   dropped,
		NeedReset: s.needResetAtCutLocked(cut, total),
		Segments:  s.planSegmentsLocked(at, cut),
	}, nil
}

func mulDivFloor(value, numerator, denominator int64) int64 {
	if value <= 0 {
		return 0
	}
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	if value <= math.MaxInt64/numerator {
		return value * numerator / denominator
	}

	var result, a, b, c big.Int
	a.SetInt64(value)
	b.SetInt64(numerator)
	c.SetInt64(denominator)
	result.Mul(&a, &b)
	result.Quo(&result, &c)
	if !result.IsInt64() {
		return math.MaxInt64
	}
	return result.Int64()
}

func addClamp(a, b int64) int64 {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func (s *BitstreamSplicer) scaleSegmentOffset(seg Segment, offset int64) int64 {
	if seg.Kind == KindAudio {
		if seg.Timebase.Num > 0 && seg.Timebase.Den > 0 {
			return mulDivFloor(offset, seg.Timebase.Num, seg.Timebase.Den)
		}
		// Audio has an independent clock. A video rebase must not move it.
		return offset
	}
	timebase := seg.Timebase
	if timebase.Num <= 0 || timebase.Den <= 0 {
		timebase = s.timebase
	}
	return mulDivFloor(offset, timebase.Num, timebase.Den)
}

func (s *BitstreamSplicer) droppedBetweenLocked(requested, cut int) int {
	dropped := 0
	for index := requested; index < cut; index++ {
		si, fi, ok := frameAt(s.segments, index)
		if ok && !s.segments[si].Frames[fi].Ref {
			dropped++
		}
	}
	return dropped
}

// Splice materialises the bitstream described by one plan.
func (s *BitstreamSplicer) Splice(plan SplicePlan) (Bitstream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if plan.Revision != s.revision {
		return Bitstream{}, ErrPlanStale
	}

	total := s.totalFrames
	if plan.Requested < 0 || plan.Cut < plan.Requested || plan.Requested >= total || plan.Cut > total {
		return Bitstream{}, ErrBadPlan
	}
	if plan.Wait != plan.Cut-plan.Requested {
		return Bitstream{}, ErrBadPlan
	}
	dropped := s.droppedBetweenLocked(plan.Requested, plan.Cut)
	if plan.Dropped != dropped {
		return Bitstream{}, ErrBadPlan
	}

	result := Bitstream{
		Frames:    make([]OutFrame, 0, total-dropped),
		ParamSets: make([]ParamSet, 0, len(s.paramSets)),
		ResetPTS:  make([]int64, 0),
		Revision:  s.revision,
	}

	seenParams := make(map[ParamSet]struct{}, len(s.paramSets))
	var lastVideoParams ParamSet
	haveLastVideo := false
	var previousOutputPTS int64
	havePreviousOutput := false

	global := 0
	for _, seg := range s.segments {
		segBase := int64(0)
		origin := int64(0)
		haveIncludedFrame := false
		segLastPTS := int64(0)

		for i, frame := range seg.Frames {
			index := global + i
			if index >= plan.Requested && index < plan.Cut {
				continue
			}

			if !haveIncludedFrame {
				if havePreviousOutput {
					segBase = addClamp(previousOutputPTS, 1)
				}
				origin = frame.PTS
				haveIncludedFrame = true

				if seg.Kind == KindVideo {
					if haveLastVideo && lastVideoParams != seg.Params {
						result.ResetPTS = append(result.ResetPTS, segBase)
					}
					if _, exists := seenParams[seg.Params]; !exists {
						seenParams[seg.Params] = struct{}{}
						result.ParamSets = append(result.ParamSets, seg.Params)
					}
					lastVideoParams = seg.Params
					haveLastVideo = true
				}
			}

			pts := addClamp(segBase, s.scaleSegmentOffset(seg, frame.PTS-origin))
			result.Frames = append(result.Frames, OutFrame{
				PTS:  pts,
				DTS:  pts,
				Kind: frame.Kind,
				Ref:  frame.Ref,
				Seg:  seg.ID,
			})
			segLastPTS = pts
		}

		if haveIncludedFrame {
			previousOutputPTS = segLastPTS
			havePreviousOutput = true
		}
		global += len(seg.Frames)
	}

	s.stats.Splices++
	s.stats.Dropped += dropped
	s.stats.Resets += len(result.ResetPTS)
	return result, nil
}

// SetTimebase replaces the output timebase used by later splices.
func (s *BitstreamSplicer) SetTimebase(tb Timebase) error {
	if tb.Num <= 0 || tb.Den <= 0 {
		return ErrBadTimebase
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timebase = tb
	return nil
}

// Timebase reports the output timebase currently in force.
func (s *BitstreamSplicer) Timebase() Timebase {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.timebase
}

// SetGopAlign turns IDR boundary alignment on or off.
func (s *BitstreamSplicer) SetGopAlign(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gopAlign = on
	return nil
}

// GopAlign reports whether IDR boundary alignment is in force.
func (s *BitstreamSplicer) GopAlign() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gopAlign
}

// Stats returns a copy of the running counters.
func (s *BitstreamSplicer) Stats() SpliceStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

// Segments lists the added segments in insertion order.
func (s *BitstreamSplicer) Segments() []SegmentInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	infos := make([]SegmentInfo, len(s.segments))
	for i, seg := range s.segments {
		info := SegmentInfo{
			ID:     seg.ID,
			Kind:   seg.Kind,
			Params: seg.Params,
			Frames: len(seg.Frames),
		}
		if len(seg.Frames) > 0 {
			info.FirstPTS = seg.Frames[0].PTS
			info.LastPTS = seg.Frames[len(seg.Frames)-1].PTS
		}
		infos[i] = info
	}
	return infos
}

// ParamSets lists the distinct video parameter sets in first seen order.
func (s *BitstreamSplicer) ParamSets() []ParamSet {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]ParamSet, len(s.paramSets))
	copy(out, s.paramSets)
	return out
}

// Reset drops every segment and zeroes the counters.
func (s *BitstreamSplicer) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.segments = nil
	s.segmentIndex = make(map[SegID]int)
	s.paramSets = nil
	s.paramSetSeen = make(map[ParamSet]struct{})
	s.totalFrames = 0
	s.duration = 0
	s.nextID = 0
	s.revision++
	s.stats = SpliceStats{}
	return nil
}

const (
	snapshotMagic   = "gsb.batch51-go-game-c-11.bitstream-splicer"
	snapshotVersion = 1
)

type splicerSnapshot struct {
	Limits    Limits      `json:"limits"`
	Segments  []Segment   `json:"segments"`
	ParamSets []ParamSet  `json:"param_sets"`
	Timebase  Timebase    `json:"timebase"`
	GopAlign  bool        `json:"gop_align"`
	Revision  int         `json:"revision"`
	NextID    int         `json:"next_id"`
	Stats     SpliceStats `json:"stats"`
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

// Snapshot serialises the segment index, the rebase ledger and the counters.
func (s *BitstreamSplicer) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state := splicerSnapshot{
		Limits:    s.limits,
		Segments:  make([]Segment, len(s.segments)),
		ParamSets: append([]ParamSet(nil), s.paramSets...),
		Timebase:  s.timebase,
		GopAlign:  s.gopAlign,
		Revision:  s.revision,
		NextID:    s.nextID,
		Stats:     s.stats,
	}
	for i, seg := range s.segments {
		state.Segments[i] = copySegment(seg)
	}

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	}
	return json.Marshal(envelope)
}

func parseSnapshot(data []byte) ([]byte, bool) {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, false
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion || len(envelope.Payload) == 0 {
		return nil, false
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return nil, false
	}
	return envelope.Payload, true
}

func validateRestoredState(state splicerSnapshot) (*BitstreamSplicer, bool) {
	restored := &BitstreamSplicer{}
	restored.limits = normalizeLimits(state.Limits)
	if state.Timebase.Num <= 0 || state.Timebase.Den <= 0 {
		return nil, false
	}
	if state.Revision < 0 || state.NextID < 0 {
		return nil, false
	}
	if state.Stats.Segments < 0 || state.Stats.Frames < 0 || state.Stats.ParamSets < 0 ||
		state.Stats.Splices < 0 || state.Stats.Resets < 0 || state.Stats.Dropped < 0 ||
		state.Stats.Duration < 0 {
		return nil, false
	}
	if len(state.Segments) > restored.limits.MaxSegments {
		return nil, false
	}

	restored.segments = make([]Segment, 0, len(state.Segments))
	restored.segmentIndex = make(map[SegID]int, len(state.Segments))
	restored.paramSets = make([]ParamSet, 0, len(state.ParamSets))
	restored.paramSetSeen = make(map[ParamSet]struct{}, len(state.ParamSets))

	var totalFrames int
	var duration int64
	rebuiltParamSets := make([]ParamSet, 0, len(state.ParamSets))
	rebuiltSeen := make(map[ParamSet]struct{}, len(state.ParamSets))

	for _, raw := range state.Segments {
		seg := copySegment(raw)
		if seg.ID == "" {
			return nil, false
		}
		if _, exists := restored.segmentIndex[seg.ID]; exists {
			return nil, false
		}
		if err := validateSegmentFields(seg); err != nil {
			return nil, false
		}
		if len(seg.Frames) > restored.limits.MaxFrames-totalFrames {
			return nil, false
		}
		segDur := segmentDuration(seg)
		if segDur > restored.limits.MaxDuration-duration {
			return nil, false
		}

		restored.segmentIndex[seg.ID] = len(restored.segments)
		restored.segments = append(restored.segments, seg)
		totalFrames += len(seg.Frames)
		duration += segDur

		if seg.Kind == KindVideo {
			if _, exists := rebuiltSeen[seg.Params]; !exists {
				rebuiltSeen[seg.Params] = struct{}{}
				rebuiltParamSets = append(rebuiltParamSets, seg.Params)
			}
		}
	}

	if len(rebuiltParamSets) > restored.limits.MaxParamSets {
		return nil, false
	}
	if len(rebuiltParamSets) != len(state.ParamSets) {
		return nil, false
	}
	for i := range rebuiltParamSets {
		if rebuiltParamSets[i] != state.ParamSets[i] {
			return nil, false
		}
	}
	restored.paramSets = rebuiltParamSets
	restored.paramSetSeen = rebuiltSeen

	if state.Stats.Segments != len(restored.segments) ||
		state.Stats.Frames != totalFrames ||
		state.Stats.ParamSets != len(restored.paramSets) ||
		state.Stats.Duration != duration {
		return nil, false
	}

	restored.totalFrames = totalFrames
	restored.duration = duration
	restored.revision = state.Revision
	restored.nextID = state.NextID
	restored.timebase = state.Timebase
	restored.gopAlign = state.GopAlign
	restored.stats = state.Stats
	return restored, true
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *BitstreamSplicer) Restore(data []byte) error {
	payload, ok := parseSnapshot(data)
	if !ok {
		return ErrBadSnapshot
	}

	var state splicerSnapshot
	if err := json.Unmarshal(payload, &state); err != nil {
		return ErrBadSnapshot
	}
	restored, ok := validateRestoredState(state)
	if !ok {
		return ErrBadSnapshot
	}

	s.mu.Lock()
	s.limits = restored.limits
	s.segments = restored.segments
	s.segmentIndex = restored.segmentIndex
	s.paramSets = restored.paramSets
	s.paramSetSeen = restored.paramSetSeen
	s.totalFrames = restored.totalFrames
	s.duration = restored.duration
	s.revision = restored.revision
	s.nextID = restored.nextID
	s.timebase = restored.timebase
	s.gopAlign = restored.gopAlign
	s.stats = restored.stats
	s.mu.Unlock()
	return nil
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
