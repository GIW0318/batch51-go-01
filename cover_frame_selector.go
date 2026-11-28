// Package task scores video frame candidates for a cover image, removes near
// duplicate frames inside one scene and truncates the pool to a stable quota.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"math/bits"
	"sort"
	"strings"
	"sync"
)

// Contract defaults applied when Limits or the configuration leaves a field
// unset.
const (
	DefaultQuota         = 4
	MaxQuota             = 50
	DefaultMaxCandidates = 4096
	DefaultHashBudget    = 4096
	DefaultWeightSharp   = 0.5
	DefaultWeightSubject = 0.3
	DefaultWeightColor   = 0.2
	DarkFrameRatio       = 0.9
	BrightFrameRatio     = 0.9
	DuplicateDistance    = 4
)

// Errors reported by the selector.
var (
	ErrBadFrame         = errors.New("frame fields are out of range")
	ErrBadTS            = errors.New("candidate timestamp must not be negative")
	ErrDuplicateTS      = errors.New("candidate timestamp already exists")
	ErrCandidateLimit   = errors.New("candidate pool is full")
	ErrBadWeights       = errors.New("score weights must be non negative and not all zero")
	ErrBadQuota         = errors.New("quota is outside the accepted range")
	ErrBadCuts          = errors.New("scene cuts must be non negative and strictly increasing")
	ErrBadReason        = errors.New("rejection reason is required")
	ErrUnknownCandidate = errors.New("unknown candidate timestamp")
	ErrNoCandidates     = errors.New("candidate pool is empty")
	ErrHashBudget       = errors.New("hash budget for one selection is exceeded")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Frame is one decoded frame offered as a cover candidate.
type Frame struct {
	ID           string
	Width        int
	Height       int
	Sharpness    float64
	SubjectRatio float64
	Colorfulness float64
	Hash         uint64
	Dark         float64
	Bright       float64
}

// ScoreWeights are the three weights of the cover score.
type ScoreWeights struct {
	Sharpness float64
	Subject   float64
	Color     float64
}

// ScoreBreakdown explains one candidate score.
type ScoreBreakdown struct {
	TS        int64
	Total     float64
	Sharpness float64
	Subject   float64
	Color     float64
	Reason    string
}

// CoverCandidate is one selected cover frame.
type CoverCandidate struct {
	TS      int64
	FrameID string
	Score   float64
}

// CoverStats is the running counter set of one selector.
type CoverStats struct {
	Candidates int
	Selected   int
	Rejected   int
	Deduped    int
	HashCalls  int
	DarkFrames int
}

// Limits bounds the candidate pool and the hashing work of one selection.
type Limits struct {
	MaxCandidates int
	MaxHashes     int
}

// candidate is one pooled frame together with its timestamp.
type candidate struct {
	frame Frame
	ts    int64
}

// CoverFrameSelector owns the candidate pool, the rejection ledger, the scene
// cuts and the scoring configuration.
type CoverFrameSelector struct {
	mu         sync.RWMutex
	limits     Limits
	pool       []candidate
	byTS       map[int64]int
	rejections map[int64]string
	cuts       []int64
	weights    ScoreWeights
	quota      int
	selected   int
	deduped    int
	hashCalls  int
	darkFrames int
}

// NewCoverFrameSelector builds a selector for the given caps.
func NewCoverFrameSelector(limits Limits) *CoverFrameSelector {
	return &CoverFrameSelector{
		limits:     limits,
		byTS:       make(map[int64]int),
		rejections: make(map[int64]string),
		weights: ScoreWeights{
			Sharpness: DefaultWeightSharp,
			Subject:   DefaultWeightSubject,
			Color:     DefaultWeightColor,
		},
		quota: DefaultQuota,
	}
}

func (s *CoverFrameSelector) maxCandidates() int {
	if s.limits.MaxCandidates > 0 {
		return s.limits.MaxCandidates
	}
	return DefaultMaxCandidates
}

func (s *CoverFrameSelector) maxHashes() int {
	if s.limits.MaxHashes > 0 {
		return s.limits.MaxHashes
	}
	return DefaultHashBudget
}

func validUnit(v float64) bool {
	return !math.IsNaN(v) && v >= 0 && v <= 1
}

func validateFrame(f Frame) error {
	if f.ID == "" || f.Width <= 0 || f.Height <= 0 {
		return ErrBadFrame
	}
	if !validUnit(f.Sharpness) || !validUnit(f.SubjectRatio) || !validUnit(f.Colorfulness) {
		return ErrBadFrame
	}
	return nil
}

func validateWeights(w ScoreWeights) error {
	if math.IsNaN(w.Sharpness) || math.IsNaN(w.Subject) || math.IsNaN(w.Color) {
		return ErrBadWeights
	}
	if w.Sharpness < 0 || w.Subject < 0 || w.Color < 0 {
		return ErrBadWeights
	}
	if w.Sharpness == 0 && w.Subject == 0 && w.Color == 0 {
		return ErrBadWeights
	}
	return nil
}

func validateCuts(cuts []int64) error {
	for i, c := range cuts {
		if c < 0 {
			return ErrBadCuts
		}
		if i > 0 && c <= cuts[i-1] {
			return ErrBadCuts
		}
	}
	return nil
}

func isDarkFrame(f Frame) bool {
	return f.Dark >= DarkFrameRatio || f.Bright >= BrightFrameRatio
}

func (s *CoverFrameSelector) score(f Frame) float64 {
	return s.weights.Sharpness*f.Sharpness +
		s.weights.Subject*f.SubjectRatio +
		s.weights.Color*f.Colorfulness
}

// segmentOf returns the scene segment index of a timestamp for the cuts.
func segmentOf(ts int64, cuts []int64) int {
	return sort.Search(len(cuts), func(i int) bool { return cuts[i] > ts })
}

// AddCandidate adds one frame to the candidate pool.
func (s *CoverFrameSelector) AddCandidate(frame Frame, ts int64) error {
	if err := validateFrame(frame); err != nil {
		return err
	}
	if ts < 0 {
		return ErrBadTS
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byTS[ts]; ok {
		return ErrDuplicateTS
	}
	active := 0
	for _, c := range s.pool {
		if _, rejected := s.rejections[c.ts]; !rejected {
			active++
		}
	}
	if active >= s.maxCandidates() {
		return ErrCandidateLimit
	}
	s.byTS[ts] = len(s.pool)
	s.pool = append(s.pool, candidate{frame: frame, ts: ts})
	if isDarkFrame(frame) {
		s.darkFrames++
	}
	return nil
}

// SetWeights replaces the score weights.
func (s *CoverFrameSelector) SetWeights(w ScoreWeights) error {
	if err := validateWeights(w); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.weights = w
	return nil
}

// Weights reports the score weights in force.
func (s *CoverFrameSelector) Weights() ScoreWeights {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.weights
}

// SetSceneCuts replaces the scene cut timestamps.
func (s *CoverFrameSelector) SetSceneCuts(cuts []int64) error {
	if err := validateCuts(cuts); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cuts = append([]int64(nil), cuts...)
	return nil
}

// SetQuota replaces the number of covers kept by Select.
func (s *CoverFrameSelector) SetQuota(n int) error {
	if n < 1 || n > MaxQuota {
		return ErrBadQuota
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quota = n
	return nil
}

// Quota reports the number of covers kept by Select.
func (s *CoverFrameSelector) Quota() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.quota
}

// Select scores, dedupes and truncates the candidate pool.
func (s *CoverFrameSelector) Select() ([]CoverCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type scored struct {
		cand  candidate
		score float64
	}
	var participants []scored
	for _, c := range s.pool {
		if _, rejected := s.rejections[c.ts]; rejected {
			continue
		}
		if isDarkFrame(c.frame) {
			continue
		}
		participants = append(participants, scored{cand: c, score: s.score(c.frame)})
	}
	if len(participants) == 0 {
		return nil, ErrNoCandidates
	}
	if len(participants) > s.maxHashes() {
		return nil, ErrHashBudget
	}
	sort.SliceStable(participants, func(i, j int) bool {
		return participants[i].cand.ts < participants[j].cand.ts
	})
	kept := make([]scored, 0, len(participants))
	deduped := 0
	pending := -1
	pendingSeg := -1
	for _, p := range participants {
		seg := segmentOf(p.cand.ts, s.cuts)
		if pending >= 0 && seg == pendingSeg &&
			bits.OnesCount64(kept[pending].cand.frame.Hash^p.cand.frame.Hash) <= DuplicateDistance {
			if p.score > kept[pending].score {
				kept[pending] = p
			}
			deduped++
			pending = -1
			continue
		}
		kept = append(kept, p)
		pending = len(kept) - 1
		pendingSeg = seg
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].score != kept[j].score {
			return kept[i].score > kept[j].score
		}
		return kept[i].cand.ts < kept[j].cand.ts
	})
	if len(kept) > s.quota {
		kept = kept[:s.quota]
	}
	out := make([]CoverCandidate, len(kept))
	for i, k := range kept {
		out[i] = CoverCandidate{
			TS:      k.cand.ts,
			FrameID: k.cand.frame.ID,
			Score:   k.score,
		}
	}
	s.selected = len(out)
	s.deduped += deduped
	s.hashCalls += len(participants)
	return out, nil
}

// Reject records a manual rejection for one candidate.
func (s *CoverFrameSelector) Reject(ts int64, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrBadReason
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byTS[ts]; !ok {
		return ErrUnknownCandidate
	}
	s.rejections[ts] = reason
	return nil
}

// ClearReject removes the manual rejection record of one candidate.
func (s *CoverFrameSelector) ClearReject(ts int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byTS[ts]; !ok {
		return ErrUnknownCandidate
	}
	delete(s.rejections, ts)
	return nil
}

// Explain returns the score breakdown of one candidate.
func (s *CoverFrameSelector) Explain(ts int64) ScoreBreakdown {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byTS[ts]
	if !ok {
		return ScoreBreakdown{TS: ts, Reason: "unknown"}
	}
	f := s.pool[idx].frame
	return ScoreBreakdown{
		TS:        ts,
		Total:     s.score(f),
		Sharpness: f.Sharpness,
		Subject:   f.SubjectRatio,
		Color:     f.Colorfulness,
		Reason:    "scored",
	}
}

// Stats returns a copy of the running counters.
func (s *CoverFrameSelector) Stats() CoverStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return CoverStats{
		Candidates: len(s.pool),
		Selected:   s.selected,
		Rejected:   len(s.rejections),
		Deduped:    s.deduped,
		HashCalls:  s.hashCalls,
		DarkFrames: s.darkFrames,
	}
}

const snapshotVersion = 1

type snapshotCandidate struct {
	Frame Frame `json:"frame"`
	TS    int64 `json:"ts"`
}

type snapshotRejection struct {
	TS     int64  `json:"ts"`
	Reason string `json:"reason"`
}

type snapshot struct {
	Version    int                 `json:"version"`
	Candidates []snapshotCandidate `json:"candidates"`
	Rejections []snapshotRejection `json:"rejections"`
	Cuts       []int64             `json:"cuts"`
	Weights    ScoreWeights        `json:"weights"`
	Quota      int                 `json:"quota"`
	Selected   int                 `json:"selected"`
	Deduped    int                 `json:"deduped"`
	HashCalls  int                 `json:"hashCalls"`
	DarkFrames int                 `json:"darkFrames"`
}

// Snapshot serialises the pool, the rejection ledger, the cuts and the
// configuration.
func (s *CoverFrameSelector) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := snapshot{
		Version:    snapshotVersion,
		Candidates: make([]snapshotCandidate, 0, len(s.pool)),
		Rejections: make([]snapshotRejection, 0, len(s.rejections)),
		Cuts:       append([]int64(nil), s.cuts...),
		Weights:    s.weights,
		Quota:      s.quota,
		Selected:   s.selected,
		Deduped:    s.deduped,
		HashCalls:  s.hashCalls,
		DarkFrames: s.darkFrames,
	}
	for _, c := range s.pool {
		snap.Candidates = append(snap.Candidates, snapshotCandidate{Frame: c.frame, TS: c.ts})
	}
	rejectedTS := make([]int64, 0, len(s.rejections))
	for ts := range s.rejections {
		rejectedTS = append(rejectedTS, ts)
	}
	sort.Slice(rejectedTS, func(i, j int) bool { return rejectedTS[i] < rejectedTS[j] })
	for _, ts := range rejectedTS {
		snap.Rejections = append(snap.Rejections, snapshotRejection{TS: ts, Reason: s.rejections[ts]})
	}
	return json.Marshal(snap)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *CoverFrameSelector) Restore(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if err := validateWeights(snap.Weights); err != nil {
		return ErrBadSnapshot
	}
	if snap.Quota < 1 || snap.Quota > MaxQuota {
		return ErrBadSnapshot
	}
	if err := validateCuts(snap.Cuts); err != nil {
		return ErrBadSnapshot
	}
	if snap.Selected < 0 || snap.Deduped < 0 || snap.HashCalls < 0 || snap.DarkFrames < 0 {
		return ErrBadSnapshot
	}
	pool := make([]candidate, 0, len(snap.Candidates))
	byTS := make(map[int64]int, len(snap.Candidates))
	for _, sc := range snap.Candidates {
		if err := validateFrame(sc.Frame); err != nil {
			return ErrBadSnapshot
		}
		if sc.TS < 0 {
			return ErrBadSnapshot
		}
		if _, dup := byTS[sc.TS]; dup {
			return ErrBadSnapshot
		}
		byTS[sc.TS] = len(pool)
		pool = append(pool, candidate{frame: sc.Frame, ts: sc.TS})
	}
	rejections := make(map[int64]string, len(snap.Rejections))
	for _, sr := range snap.Rejections {
		if strings.TrimSpace(sr.Reason) == "" {
			return ErrBadSnapshot
		}
		if _, ok := byTS[sr.TS]; !ok {
			return ErrBadSnapshot
		}
		if _, dup := rejections[sr.TS]; dup {
			return ErrBadSnapshot
		}
		rejections[sr.TS] = sr.Reason
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pool = pool
	s.byTS = byTS
	s.rejections = rejections
	s.cuts = append([]int64(nil), snap.Cuts...)
	s.weights = snap.Weights
	s.quota = snap.Quota
	s.selected = snap.Selected
	s.deduped = snap.Deduped
	s.hashCalls = snap.HashCalls
	s.darkFrames = snap.DarkFrames
	return nil
}
