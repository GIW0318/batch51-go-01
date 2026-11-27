// Package task keeps the speaker diarization ledger: it assigns segments to
// speaker slots, merges and splits labelled spans and finalises transcripts.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// Contract defaults used when the limits leave a field unset.
const (
	DefaultMaxSpeakers         = 8
	DefaultGapMS         int64 = 300
	DefaultMinSegmentMS        = 200
	DefaultMatchDistance       = 0.25
	DefaultGrayBand            = 0.1
	DefaultMinConfidence       = 0.6
)

// Errors reported by the ledger.
var (
	ErrBadSegment     = errors.New("segment fields are out of range")
	ErrBadFingerprint = errors.New("voiceprint dimensions do not match")
	ErrSlotLimit      = errors.New("speaker slot limit reached")
	ErrUnknownSegment = errors.New("segment id is unknown")
	ErrMergeMismatch  = errors.New("segments belong to different speakers")
	ErrGapTooLarge    = errors.New("silence gap is too large to merge")
	ErrBadSplit       = errors.New("split point is outside the segment")
	ErrBadThreshold   = errors.New("confidence threshold must be within zero and one")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// DiarLimits bounds the ledger.
type DiarLimits struct {
	MaxSpeakers   int
	GapMS         int64
	MinSegmentMS  int64
	MatchDistance float64
	GrayBand      float64
	MinConfidence float64
}

// Segment is one diarized span of speech.
type Segment struct {
	ID          string
	FileID      string
	StartMS     int64
	EndMS       int64
	Confidence  float64
	Fingerprint []float64
	Overlap     bool
}

// Assignment is the outcome of one Assign call.
type Assignment struct {
	SegmentID string
	Slot      int
	Created   bool
	Suspected bool
	Pending   bool
	Merged    bool
}

// Entry is one labelled span in a finalised transcript.
type Entry struct {
	SegmentID string
	StartMS   int64
	EndMS     int64
	Slots     []int
	Suspected bool
}

// Transcript is the labelled timeline of one file.
type Transcript struct {
	FileID     string
	Entries    []Entry
	DurationMS int64
	Speakers   int
}

type speakerSlot struct {
	id        int
	centroid  []float64
	samples   int
	files     map[string]struct{}
	suspected bool
}

type segmentRecord struct {
	segment   Segment
	slot      int
	pending   bool
	suspected bool
	result    Assignment
}

// DiarizationLedger owns the speaker slots, the stored segments and the review
// queue.
type DiarizationLedger struct {
	mu          sync.RWMutex
	limits      DiarLimits
	threshold   float64
	slots       []*speakerSlot
	segments    map[string]*segmentRecord
	pending     int
	nextDerived uint64
	durations   map[string]int64
}

// NewDiarizationLedger builds a ledger for the given limits.
func NewDiarizationLedger(limits DiarLimits) *DiarizationLedger {
	limits = normalizeLimits(limits)
	return &DiarizationLedger{
		limits:    limits,
		threshold: limits.MinConfidence,
		segments:  make(map[string]*segmentRecord),
		durations: make(map[string]int64),
	}
}

func normalizeLimits(limits DiarLimits) DiarLimits {
	if limits.MaxSpeakers <= 0 {
		limits.MaxSpeakers = DefaultMaxSpeakers
	}
	if limits.GapMS <= 0 {
		limits.GapMS = DefaultGapMS
	}
	if limits.MinSegmentMS <= 0 {
		limits.MinSegmentMS = DefaultMinSegmentMS
	}
	if limits.MatchDistance <= 0 || math.IsNaN(limits.MatchDistance) || math.IsInf(limits.MatchDistance, 0) {
		limits.MatchDistance = DefaultMatchDistance
	}
	if limits.GrayBand <= 0 || math.IsNaN(limits.GrayBand) || math.IsInf(limits.GrayBand, 0) {
		limits.GrayBand = DefaultGrayBand
	}
	if limits.MinConfidence <= 0 || limits.MinConfidence > 1 || math.IsNaN(limits.MinConfidence) || math.IsInf(limits.MinConfidence, 0) {
		limits.MinConfidence = DefaultMinConfidence
	}
	return limits
}

func validSegment(segment Segment) bool {
	return segment.ID != "" &&
		segment.FileID != "" &&
		segment.StartMS >= 0 &&
		segment.EndMS > segment.StartMS &&
		segment.Confidence >= 0 &&
		segment.Confidence <= 1 &&
		!math.IsNaN(segment.Confidence) &&
		!math.IsInf(segment.Confidence, 0)
}

func validFingerprint(fingerprint []float64) bool {
	for _, value := range fingerprint {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func cloneFingerprint(fingerprint []float64) []float64 {
	if fingerprint == nil {
		return nil
	}
	return append([]float64(nil), fingerprint...)
}

func cloneSegment(segment Segment) Segment {
	segment.Fingerprint = cloneFingerprint(segment.Fingerprint)
	return segment
}

func fingerprintDistance(left, right []float64) float64 {
	var sum float64
	for i := range left {
		delta := left[i] - right[i]
		sum += delta * delta
	}
	return math.Sqrt(sum)
}

func (l *DiarizationLedger) hasSlotFile(slot *speakerSlot, fileID string) bool {
	_, ok := slot.files[fileID]
	return ok
}

func (l *DiarizationLedger) updateDuration(fileID string, endMS int64) {
	if endMS > l.durations[fileID] {
		l.durations[fileID] = endMS
	}
}

func (l *DiarizationLedger) checkFingerprint(fingerprint []float64) error {
	if !validFingerprint(fingerprint) {
		return ErrBadFingerprint
	}
	for _, slot := range l.slots {
		if len(fingerprint) != len(slot.centroid) {
			return ErrBadFingerprint
		}
	}
	return nil
}

func (l *DiarizationLedger) newSlot(fingerprint []float64, fileID string, suspected bool) *speakerSlot {
	slot := &speakerSlot{
		id:        len(l.slots) + 1,
		centroid:  cloneFingerprint(fingerprint),
		samples:   1,
		files:     map[string]struct{}{fileID: {}},
		suspected: suspected,
	}
	l.slots = append(l.slots, slot)
	return slot
}

func (l *DiarizationLedger) updateSlotCentroid(slot *speakerSlot, fingerprint []float64, fileID string) {
	nextSamples := slot.samples + 1
	for i := range slot.centroid {
		slot.centroid[i] = (slot.centroid[i]*float64(slot.samples) + fingerprint[i]) / float64(nextSamples)
	}
	slot.samples = nextSamples
	slot.files[fileID] = struct{}{}
}

// Assign places one segment on a speaker slot.
func (l *DiarizationLedger) Assign(segment Segment) (Assignment, error) {
	if !validSegment(segment) {
		return Assignment{}, ErrBadSegment
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if record, ok := l.segments[segment.ID]; ok {
		return record.result, nil
	}
	if err := l.checkFingerprint(segment.Fingerprint); err != nil {
		return Assignment{}, err
	}

	if segment.Confidence < l.threshold {
		if l.pending >= l.limits.MaxSpeakers {
			return Assignment{}, ErrSlotLimit
		}
		result := Assignment{
			SegmentID: segment.ID,
			Pending:   true,
		}
		l.segments[segment.ID] = &segmentRecord{
			segment: cloneSegment(segment),
			pending: true,
			result:  result,
		}
		l.pending++
		l.updateDuration(segment.FileID, segment.EndMS)
		return result, nil
	}

	nearestIndex := -1
	nearestDistance := math.Inf(1)
	for i, slot := range l.slots {
		distance := fingerprintDistance(segment.Fingerprint, slot.centroid)
		if distance < nearestDistance {
			nearestDistance = distance
			nearestIndex = i
		}
	}

	suspected := false
	created := false
	var slot *speakerSlot
	if nearestIndex >= 0 && nearestDistance <= l.limits.MatchDistance {
		slot = l.slots[nearestIndex]
		l.updateSlotCentroid(slot, segment.Fingerprint, segment.FileID)
		suspected = slot.suspected
	} else {
		grayMatch := nearestIndex >= 0 &&
			nearestDistance > l.limits.MatchDistance &&
			nearestDistance <= l.limits.MatchDistance+l.limits.GrayBand
		if grayMatch && !l.hasSlotFile(l.slots[nearestIndex], segment.FileID) {
			suspected = true
		}
		if len(l.slots) >= l.limits.MaxSpeakers {
			return Assignment{}, ErrSlotLimit
		}
		slot = l.newSlot(segment.Fingerprint, segment.FileID, suspected)
		created = true
	}

	result := Assignment{
		SegmentID: segment.ID,
		Slot:      slot.id,
		Created:   created,
		Suspected: suspected,
	}
	l.segments[segment.ID] = &segmentRecord{
		segment:   cloneSegment(segment),
		slot:      slot.id,
		suspected: suspected,
		result:    result,
	}
	l.updateDuration(segment.FileID, segment.EndMS)
	return result, nil
}

// Merge joins two or more adjacent segments of the same speaker.
func (l *DiarizationLedger) Merge(segmentIDs []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(segmentIDs) < 2 {
		return ErrBadSegment
	}
	records := make([]*segmentRecord, 0, len(segmentIDs))
	seen := make(map[string]struct{}, len(segmentIDs))
	for _, id := range segmentIDs {
		if _, duplicate := seen[id]; duplicate {
			return ErrBadSegment
		}
		seen[id] = struct{}{}
		record, ok := l.segments[id]
		if !ok {
			return ErrUnknownSegment
		}
		records = append(records, record)
	}

	first := records[0]
	if first.pending || first.slot == 0 {
		return ErrMergeMismatch
	}
	for _, record := range records[1:] {
		if record.pending || record.slot != first.slot || record.segment.FileID != first.segment.FileID {
			return ErrMergeMismatch
		}
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].segment.StartMS != records[j].segment.StartMS {
			return records[i].segment.StartMS < records[j].segment.StartMS
		}
		if records[i].segment.EndMS != records[j].segment.EndMS {
			return records[i].segment.EndMS < records[j].segment.EndMS
		}
		return records[i].segment.ID < records[j].segment.ID
	})

	maxEnd := records[0].segment.EndMS
	for _, record := range records[1:] {
		if gap := record.segment.StartMS - maxEnd; gap > l.limits.GapMS {
			return ErrGapTooLarge
		}
		if record.segment.EndMS > maxEnd {
			maxEnd = record.segment.EndMS
		}
	}

	kept := records[0]
	merged := cloneSegment(kept.segment)
	merged.StartMS = records[0].segment.StartMS
	merged.EndMS = maxEnd
	for _, record := range records[1:] {
		merged.Overlap = merged.Overlap || record.segment.Overlap
	}
	kept.segment = merged
	for _, record := range records[1:] {
		delete(l.segments, record.segment.ID)
	}
	return nil
}

// Split cuts one segment at an absolute timeline offset.
func (l *DiarizationLedger) Split(segmentID string, at time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	record, ok := l.segments[segmentID]
	if !ok {
		return ErrUnknownSegment
	}
	if at < 0 || at < time.Duration(record.segment.StartMS)*time.Millisecond || at >= time.Duration(record.segment.EndMS)*time.Millisecond {
		return ErrBadSplit
	}
	atMS := int64(at / time.Millisecond)
	if atMS <= record.segment.StartMS || atMS >= record.segment.EndMS {
		return ErrBadSplit
	}
	if record.pending && l.pending >= l.limits.MaxSpeakers {
		return ErrSlotLimit
	}

	var rightID string
	for {
		l.nextDerived++
		rightID = fmt.Sprintf("%s#%d", record.segment.ID, l.nextDerived)
		if _, exists := l.segments[rightID]; !exists {
			break
		}
	}

	left := cloneSegment(record.segment)
	left.EndMS = atMS
	right := cloneSegment(record.segment)
	right.ID = rightID
	right.StartMS = atMS
	rightResult := record.result
	rightResult.SegmentID = rightID
	rightResult.Created = false
	rightResult.Merged = false

	record.segment = left
	l.segments[rightID] = &segmentRecord{
		segment:   right,
		slot:      record.slot,
		pending:   record.pending,
		suspected: record.suspected,
		result:    rightResult,
	}
	if record.pending {
		l.pending++
	}
	return nil
}

// Finalize emits the labelled timeline of one file.
func (l *DiarizationLedger) Finalize(fileID string) (Transcript, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	transcript := Transcript{FileID: fileID, DurationMS: l.durations[fileID]}
	for _, record := range l.segments {
		if record.segment.FileID != fileID {
			continue
		}
		if record.segment.EndMS > transcript.DurationMS {
			transcript.DurationMS = record.segment.EndMS
		}
		if record.pending {
			continue
		}
		short := record.segment.EndMS-record.segment.StartMS < l.limits.MinSegmentMS
		entry := Entry{
			SegmentID: record.segment.ID,
			StartMS:   record.segment.StartMS,
			EndMS:     record.segment.EndMS,
			Slots:     []int{record.slot},
			Suspected: record.suspected,
		}
		if short {
			entry.Slots = nil
			entry.Suspected = false
		}
		transcript.Entries = append(transcript.Entries, entry)
	}

	sort.Slice(transcript.Entries, func(i, j int) bool {
		left, right := transcript.Entries[i], transcript.Entries[j]
		if left.StartMS != right.StartMS {
			return left.StartMS < right.StartMS
		}
		if left.EndMS != right.EndMS {
			return left.EndMS < right.EndMS
		}
		return left.SegmentID < right.SegmentID
	})

	primarySlots := make([][]int, len(transcript.Entries))
	for i := range transcript.Entries {
		primarySlots[i] = append([]int(nil), transcript.Entries[i].Slots...)
	}
	speakers := make(map[int]struct{})
	for i := range transcript.Entries {
		if len(primarySlots[i]) == 0 {
			transcript.Entries[i].Slots = nil
			continue
		}
		slots := make(map[int]struct{})
		for _, slot := range primarySlots[i] {
			slots[slot] = struct{}{}
		}
		for j := range transcript.Entries {
			if i == j {
				continue
			}
			if transcript.Entries[i].StartMS < transcript.Entries[j].EndMS &&
				transcript.Entries[j].StartMS < transcript.Entries[i].EndMS {
				for _, slot := range primarySlots[j] {
					slots[slot] = struct{}{}
				}
			}
		}
		transcript.Entries[i].Slots = transcript.Entries[i].Slots[:0]
		for slot := range slots {
			transcript.Entries[i].Slots = append(transcript.Entries[i].Slots, slot)
		}
		sort.Ints(transcript.Entries[i].Slots)
		for _, slot := range transcript.Entries[i].Slots {
			speakers[slot] = struct{}{}
		}
	}
	transcript.Speakers = len(speakers)
	return transcript, nil
}

// Remove drops one stored segment.
func (l *DiarizationLedger) Remove(segmentID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	record, ok := l.segments[segmentID]
	if !ok {
		return ErrUnknownSegment
	}
	delete(l.segments, segmentID)
	if record.pending {
		l.pending--
	}
	return nil
}

// SetThreshold changes the confidence threshold used by Assign.
func (l *DiarizationLedger) SetThreshold(t float64) error {
	if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 || t > 1 {
		return ErrBadThreshold
	}
	l.mu.Lock()
	l.threshold = t
	l.mu.Unlock()
	return nil
}

type snapshotSlot struct {
	ID        int       `json:"id"`
	Centroid  []float64 `json:"centroid"`
	Samples   int       `json:"samples"`
	Files     []string  `json:"files"`
	Suspected bool      `json:"suspected"`
}

type snapshotSegment struct {
	Segment   Segment    `json:"segment"`
	Slot      int        `json:"slot"`
	Pending   bool       `json:"pending"`
	Suspected bool       `json:"suspected"`
	Result    Assignment `json:"result"`
}

type snapshotState struct {
	Limits      DiarLimits        `json:"limits"`
	Threshold   float64           `json:"threshold"`
	Slots       []snapshotSlot    `json:"slots"`
	Segments    []snapshotSegment `json:"segments"`
	Pending     int               `json:"pending"`
	NextDerived uint64            `json:"next_derived"`
	Durations   map[string]int64  `json:"durations"`
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

const snapshotVersion = 1

// Snapshot serialises the ledger state.
func (l *DiarizationLedger) Snapshot() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	state := snapshotState{
		Limits:      l.limits,
		Threshold:   l.threshold,
		Pending:     l.pending,
		NextDerived: l.nextDerived,
		Durations:   make(map[string]int64, len(l.durations)),
		Slots:       make([]snapshotSlot, 0, len(l.slots)),
		Segments:    make([]snapshotSegment, 0, len(l.segments)),
	}
	for fileID, duration := range l.durations {
		state.Durations[fileID] = duration
	}
	for _, slot := range l.slots {
		files := make([]string, 0, len(slot.files))
		for fileID := range slot.files {
			files = append(files, fileID)
		}
		sort.Strings(files)
		state.Slots = append(state.Slots, snapshotSlot{
			ID:        slot.id,
			Centroid:  cloneFingerprint(slot.centroid),
			Samples:   slot.samples,
			Files:     files,
			Suspected: slot.suspected,
		})
	}
	for _, record := range l.segments {
		state.Segments = append(state.Segments, snapshotSegment{
			Segment:   cloneSegment(record.segment),
			Slot:      record.slot,
			Pending:   record.pending,
			Suspected: record.suspected,
			Result:    record.result,
		})
	}
	sort.Slice(state.Segments, func(i, j int) bool {
		return state.Segments[i].Segment.ID < state.Segments[j].Segment.ID
	})

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  payload,
	}
	return json.Marshal(envelope)
}

func decodeSnapshot(data []byte) (*DiarizationLedger, error) {
	var envelope *snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return nil, ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || len(envelope.Payload) == 0 {
		return nil, ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return nil, ErrBadSnapshot
	}

	var state *snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil || state == nil {
		return nil, ErrBadSnapshot
	}
	if state.Threshold < 0 || state.Threshold > 1 || math.IsNaN(state.Threshold) || math.IsInf(state.Threshold, 0) {
		return nil, ErrBadSnapshot
	}
	limits := normalizeLimits(state.Limits)
	if limits != state.Limits {
		return nil, ErrBadSnapshot
	}

	ledger := &DiarizationLedger{
		limits:      limits,
		threshold:   state.Threshold,
		segments:    make(map[string]*segmentRecord, len(state.Segments)),
		nextDerived: state.NextDerived,
		durations:   make(map[string]int64, len(state.Durations)),
	}
	if len(state.Slots) > limits.MaxSpeakers {
		return nil, ErrBadSnapshot
	}
	dimension := -1
	for i, saved := range state.Slots {
		if saved.ID != i+1 || saved.Samples <= 0 || !validFingerprint(saved.Centroid) || len(saved.Files) == 0 {
			return nil, ErrBadSnapshot
		}
		if dimension < 0 {
			dimension = len(saved.Centroid)
		} else if len(saved.Centroid) != dimension {
			return nil, ErrBadSnapshot
		}
		files := make(map[string]struct{}, len(saved.Files))
		for _, fileID := range saved.Files {
			if fileID == "" {
				return nil, ErrBadSnapshot
			}
			files[fileID] = struct{}{}
		}
		ledger.slots = append(ledger.slots, &speakerSlot{
			id:        saved.ID,
			centroid:  cloneFingerprint(saved.Centroid),
			samples:   saved.Samples,
			files:     files,
			suspected: saved.Suspected,
		})
	}

	pendingCount := 0
	maxEndByFile := make(map[string]int64)
	for _, saved := range state.Segments {
		segment := saved.Segment
		if !validSegment(segment) || !validFingerprint(segment.Fingerprint) {
			return nil, ErrBadSnapshot
		}
		if _, duplicate := ledger.segments[segment.ID]; duplicate {
			return nil, ErrBadSnapshot
		}
		if saved.Pending {
			if saved.Slot != 0 {
				return nil, ErrBadSnapshot
			}
			pendingCount++
		} else {
			if saved.Slot < 1 || saved.Slot > len(ledger.slots) {
				return nil, ErrBadSnapshot
			}
			if len(segment.Fingerprint) != len(ledger.slots[saved.Slot-1].centroid) {
				return nil, ErrBadSnapshot
			}
		}
		if saved.Result.SegmentID != segment.ID ||
			saved.Result.Slot != saved.Slot ||
			saved.Result.Pending != saved.Pending ||
			saved.Result.Suspected != saved.Suspected {
			return nil, ErrBadSnapshot
		}
		ledger.segments[segment.ID] = &segmentRecord{
			segment:   cloneSegment(segment),
			slot:      saved.Slot,
			pending:   saved.Pending,
			suspected: saved.Suspected,
			result:    saved.Result,
		}
		if segment.EndMS > maxEndByFile[segment.FileID] {
			maxEndByFile[segment.FileID] = segment.EndMS
		}
	}
	if pendingCount != state.Pending || pendingCount > limits.MaxSpeakers {
		return nil, ErrBadSnapshot
	}
	ledger.pending = pendingCount
	for fileID, duration := range state.Durations {
		if fileID == "" || duration < maxEndByFile[fileID] {
			return nil, ErrBadSnapshot
		}
		ledger.durations[fileID] = duration
	}
	for fileID, endMS := range maxEndByFile {
		if _, ok := ledger.durations[fileID]; !ok {
			return nil, ErrBadSnapshot
		}
		if ledger.durations[fileID] < endMS {
			return nil, ErrBadSnapshot
		}
	}
	return ledger, nil
}

// Restore replaces the ledger state with a snapshot, all or nothing.
func (l *DiarizationLedger) Restore(data []byte) error {
	restored, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}
	l.mu.Lock()
	l.limits = restored.limits
	l.threshold = restored.threshold
	l.slots = restored.slots
	l.segments = restored.segments
	l.pending = restored.pending
	l.nextDerived = restored.nextDerived
	l.durations = restored.durations
	l.mu.Unlock()
	return nil
}

// Slots reports how many speaker slots exist.
func (l *DiarizationLedger) Slots() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.slots)
}

// Pending reports how many segments wait in the review queue.
func (l *DiarizationLedger) Pending() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.pending
}

// Segments returns the stored segments of one file, ordered by start time.
func (l *DiarizationLedger) Segments(fileID string) []Segment {
	l.mu.RLock()
	defer l.mu.RUnlock()

	segments := make([]Segment, 0)
	for _, record := range l.segments {
		if record.segment.FileID == fileID {
			segments = append(segments, cloneSegment(record.segment))
		}
	}
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].StartMS != segments[j].StartMS {
			return segments[i].StartMS < segments[j].StartMS
		}
		if segments[i].EndMS != segments[j].EndMS {
			return segments[i].EndMS < segments[j].EndMS
		}
		return segments[i].ID < segments[j].ID
	})
	return segments
}

// Limits reports the effective limits.
func (l *DiarizationLedger) Limits() DiarLimits {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.limits
}

// Threshold reports the effective confidence threshold.
func (l *DiarizationLedger) Threshold() float64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.threshold
}
