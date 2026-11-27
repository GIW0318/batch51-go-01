// Package task compares two independent host object views, attributes the set
// difference to hiding and keeps the evidence across restarts.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Object kinds enumerated by the scanner.
const (
	KindProcesses = "processes"
	KindModules   = "modules"
	KindListeners = "listeners"
)

// Attribution verdicts.
const (
	VerdictHiddenFromDirect = "hidden_from_direct"
	VerdictOnlyInDirect     = "only_in_direct"
)

// View sides an object can be missing from.
const (
	missingFromDirect       = "direct"
	missingFromConventional = "conventional"
)

// Errors returned by the scanner.
var (
	ErrUnknownKind      = errors.New("unknown object kind")
	ErrMissingCollector = errors.New("collector is not configured")
	ErrWindowExceeded   = errors.New("collection window exceeded")
	ErrUnknownItem      = errors.New("diff item is unknown")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Object is one item reported by a collector. Key is the stable identity of
// the object and Name is only a display hint.
type Object struct {
	Kind   string
	Key    string
	Name   string
	SeenAt int64
}

// Collector enumerates one view of the host. now is the wall clock of the
// current round and must be used for SeenAt bookkeeping.
type Collector interface {
	Collect(kind string, now int64) ([]Object, error)
}

// Collectors holds the direct system call view and the conventional view.
type Collectors struct {
	Direct       Collector
	Conventional Collector
}

// Policy bounds one round of enumeration.
type Policy struct {
	TimeWindow        int64
	MaxObjectsPerKind int
	MaxRecollect      int
	Now               func() int64
}

// View is the paired result of one enumeration round.
type View struct {
	Kind         string
	Direct       []Object
	Conventional []Object
	WindowStart  int64
	WindowEnd    int64
	Truncated    bool
	Recollects   int
}

// DiffItem is one object missing from exactly one view.
type DiffItem struct {
	Kind        string
	Key         string
	Name        string
	MissingFrom string
}

// DiffReport is the ordered set difference between the two views.
type DiffReport struct {
	Items        []DiffItem
	HiddenCount  int
	PhantomCount int
}

// Attribution grades one diff item.
type Attribution struct {
	Kind     string
	Key      string
	Verdict  string
	Severity int
}

// Finding is one attributed diff item kept by the scanner.
type Finding struct {
	Kind     string
	Key      string
	Name     string
	Verdict  string
	Severity int
	Seq      int64
}

// ScanResult summarises one full scan.
type ScanResult struct {
	Kinds         []string
	Diff          DiffReport
	NewFindings   int
	Indeterminate bool
}

// Stats counts enumeration and scan work.
type Stats struct {
	Enumerations  int
	Recollects    int
	Scans         int
	Indeterminate int
}

// RootkitCrossViewScanner owns the paired views, the findings and the counters.
type RootkitCrossViewScanner struct {
	collectors Collectors
	policy     Policy

	mu       sync.Mutex
	findings []Finding
	seen     map[string]struct{}
	seq      int64
	stats    Stats
}

// knownKinds lists every enumerable kind in scan order.
var knownKinds = []string{KindProcesses, KindModules, KindListeners}

// NewRootkitCrossViewScanner builds a scanner over the two collectors.
func NewRootkitCrossViewScanner(collectors Collectors, policy Policy) *RootkitCrossViewScanner {
	return &RootkitCrossViewScanner{
		collectors: collectors,
		policy:     policy,
		seen:       map[string]struct{}{},
	}
}

func isKnownKind(kind string) bool {
	for _, k := range knownKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func (s *RootkitCrossViewScanner) now() int64 {
	if s.policy.Now != nil {
		return s.policy.Now()
	}
	return time.Now().Unix()
}

// Enumerate collects both views of one kind inside a bounded time window.
func (s *RootkitCrossViewScanner) Enumerate(kind string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enumerateLocked(kind)
}

func (s *RootkitCrossViewScanner) enumerateLocked(kind string) (View, error) {
	if !isKnownKind(kind) {
		return View{}, ErrUnknownKind
	}
	if s.collectors.Direct == nil || s.collectors.Conventional == nil {
		return View{}, ErrMissingCollector
	}

	var recollects int
	for {
		now := s.now()
		direct, err := s.collectors.Direct.Collect(kind, now)
		if err != nil {
			s.stats.Indeterminate++
			s.stats.Recollects += recollects
			return View{}, fmt.Errorf("direct view restricted: %w", err)
		}
		conventional, err := s.collectors.Conventional.Collect(kind, now)
		if err != nil {
			s.stats.Indeterminate++
			s.stats.Recollects += recollects
			return View{}, fmt.Errorf("conventional view restricted: %w", err)
		}

		start, end := collectionWindow(direct, conventional, now)
		if end-start > s.policy.TimeWindow {
			if recollects < s.policy.MaxRecollect {
				recollects++
				continue
			}
			s.stats.Indeterminate++
			s.stats.Recollects += recollects
			return View{}, ErrWindowExceeded
		}

		direct, truncatedDirect := sortAndTruncate(direct, s.policy.MaxObjectsPerKind)
		conventional, truncatedConventional := sortAndTruncate(conventional, s.policy.MaxObjectsPerKind)
		s.stats.Enumerations++
		s.stats.Recollects += recollects
		return View{
			Kind:         kind,
			Direct:       direct,
			Conventional: conventional,
			WindowStart:  start,
			WindowEnd:    end,
			Truncated:    truncatedDirect || truncatedConventional,
			Recollects:   recollects,
		}, nil
	}
}

// collectionWindow returns the min and max SeenAt across both views. When
// both views are empty the window collapses to the collection instant.
func collectionWindow(direct, conventional []Object, now int64) (int64, int64) {
	start, end := now, now
	first := true
	for _, side := range [][]Object{direct, conventional} {
		for _, obj := range side {
			if first || obj.SeenAt < start {
				start = obj.SeenAt
			}
			if first || obj.SeenAt > end {
				end = obj.SeenAt
			}
			first = false
		}
	}
	return start, end
}

// sortAndTruncate orders objects by stable key and keeps the lowest keys up
// to the per kind cap.
func sortAndTruncate(objects []Object, cap int) ([]Object, bool) {
	out := make([]Object, len(objects))
	copy(out, objects)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].SeenAt < out[j].SeenAt
	})
	if cap > 0 && len(out) > cap {
		return out[:cap], true
	}
	return out, false
}

// Diff computes the set difference of a view.
func (s *RootkitCrossViewScanner) Diff(view View) (DiffReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diffLocked(view)
}

// objectID identifies one object independent of its display name.
type objectID struct {
	kind string
	key  string
}

func (s *RootkitCrossViewScanner) diffLocked(view View) (DiffReport, error) {
	directSet := make(map[objectID]struct{}, len(view.Direct))
	for _, obj := range view.Direct {
		directSet[objectID{kind: obj.Kind, key: obj.Key}] = struct{}{}
	}
	conventionalSet := make(map[objectID]struct{}, len(view.Conventional))
	for _, obj := range view.Conventional {
		conventionalSet[objectID{kind: obj.Kind, key: obj.Key}] = struct{}{}
	}

	directMin, directOK := minSeenAt(view.Direct)
	windowWidth := view.WindowEnd - view.WindowStart

	report := DiffReport{}
	for _, obj := range view.Conventional {
		if _, ok := directSet[objectID{kind: obj.Kind, key: obj.Key}]; ok {
			continue
		}
		if isShortLivedChurn(obj, directMin, directOK, windowWidth) {
			continue
		}
		report.Items = append(report.Items, DiffItem{
			Kind:        obj.Kind,
			Key:         obj.Key,
			Name:        obj.Name,
			MissingFrom: missingFromDirect,
		})
		report.HiddenCount++
	}
	for _, obj := range view.Direct {
		if _, ok := conventionalSet[objectID{kind: obj.Kind, key: obj.Key}]; ok {
			continue
		}
		report.Items = append(report.Items, DiffItem{
			Kind:        obj.Kind,
			Key:         obj.Key,
			Name:        obj.Name,
			MissingFrom: missingFromConventional,
		})
		report.PhantomCount++
	}
	sortDiffItems(report.Items)
	return report, nil
}

func minSeenAt(objects []Object) (int64, bool) {
	if len(objects) == 0 {
		return 0, false
	}
	min := objects[0].SeenAt
	for _, obj := range objects[1:] {
		if obj.SeenAt < min {
			min = obj.SeenAt
		}
	}
	return min, true
}

// isShortLivedChurn reports whether a conventional-only process simply exited
// between the two collections instead of being hidden: it was last seen
// before the direct collection started and its lifetime, derived from the
// start time encoded in the stable key, fits inside the collection window.
func isShortLivedChurn(obj Object, directMin int64, directOK bool, windowWidth int64) bool {
	if obj.Kind != KindProcesses || !directOK {
		return false
	}
	if obj.SeenAt >= directMin {
		return false
	}
	start, ok := processStartTime(obj.Key)
	if !ok {
		return false
	}
	lifetime := obj.SeenAt - start
	return lifetime >= 0 && lifetime <= windowWidth
}

// processStartTime extracts the start time from a "pid:<n>/start:<t>" key.
func processStartTime(key string) (int64, bool) {
	idx := strings.LastIndex(key, "/start:")
	if idx < 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(key[idx+len("/start:"):], 10, 64)
	if err != nil {
		return 0, false
	}
	return start, true
}

func sortDiffItems(items []DiffItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Key < items[j].Key
	})
}

// Attribute explains which side is missing one object.
func (s *RootkitCrossViewScanner) Attribute(item DiffItem) (Attribution, error) {
	if !isKnownKind(item.Kind) {
		return Attribution{}, ErrUnknownItem
	}
	attr := Attribution{Kind: item.Kind, Key: item.Key}
	switch item.MissingFrom {
	case missingFromDirect:
		attr.Verdict = VerdictHiddenFromDirect
		attr.Severity = 80
	case missingFromConventional:
		attr.Verdict = VerdictOnlyInDirect
		attr.Severity = 30
	default:
		return Attribution{}, ErrUnknownItem
	}
	return attr, nil
}

// Scan enumerates every kind, diffs the views and appends new findings.
func (s *RootkitCrossViewScanner) Scan() (ScanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := ScanResult{Kinds: append([]string(nil), knownKinds...)}
	for _, kind := range knownKinds {
		view, err := s.enumerateLocked(kind)
		if err != nil {
			result.Indeterminate = true
			continue
		}
		report, err := s.diffLocked(view)
		if err != nil {
			result.Indeterminate = true
			continue
		}
		result.Diff.Items = append(result.Diff.Items, report.Items...)
		result.Diff.HiddenCount += report.HiddenCount
		result.Diff.PhantomCount += report.PhantomCount
	}
	sortDiffItems(result.Diff.Items)

	for _, item := range result.Diff.Items {
		attr, err := s.Attribute(item)
		if err != nil {
			continue
		}
		id := attr.Verdict + "|" + attr.Kind + "|" + attr.Key
		if _, ok := s.seen[id]; ok {
			continue
		}
		s.seen[id] = struct{}{}
		s.seq++
		s.findings = append(s.findings, Finding{
			Kind:     attr.Kind,
			Key:      attr.Key,
			Name:     item.Name,
			Verdict:  attr.Verdict,
			Severity: attr.Severity,
			Seq:      s.seq,
		})
		result.NewFindings++
	}
	s.stats.Scans++
	return result, nil
}

// Findings returns a copy of the findings, oldest first.
func (s *RootkitCrossViewScanner) Findings() []Finding {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Finding, len(s.findings))
	copy(out, s.findings)
	return out
}

// snapshotState is the serialised form of the scanner state.
type snapshotState struct {
	Seq      int64     `json:"seq"`
	Findings []Finding `json:"findings"`
	Stats    Stats     `json:"stats"`
}

// Snapshot serialises findings, sequence counter and statistics.
func (s *RootkitCrossViewScanner) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := snapshotState{
		Seq:      s.seq,
		Findings: append([]Finding(nil), s.findings...),
		Stats:    s.stats,
	}
	return json.Marshal(state)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *RootkitCrossViewScanner) Restore(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return ErrBadSnapshot
	}
	if state.Seq < 0 {
		return ErrBadSnapshot
	}
	seen := make(map[string]struct{}, len(state.Findings))
	var maxSeq int64
	for _, f := range state.Findings {
		if !isKnownKind(f.Kind) || f.Seq <= 0 {
			return ErrBadSnapshot
		}
		switch f.Verdict {
		case VerdictHiddenFromDirect:
			if f.Severity != 80 {
				return ErrBadSnapshot
			}
		case VerdictOnlyInDirect:
			if f.Severity != 30 {
				return ErrBadSnapshot
			}
		default:
			return ErrBadSnapshot
		}
		id := f.Verdict + "|" + f.Kind + "|" + f.Key
		if _, ok := seen[id]; ok {
			return ErrBadSnapshot
		}
		seen[id] = struct{}{}
		if f.Seq > maxSeq {
			maxSeq = f.Seq
		}
	}
	if state.Seq < maxSeq {
		return ErrBadSnapshot
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.findings = append([]Finding(nil), state.Findings...)
	s.seen = seen
	s.seq = state.Seq
	s.stats = state.Stats
	return nil
}

// Stats returns a copy of the counters.
func (s *RootkitCrossViewScanner) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Reset drops every finding and counter.
func (s *RootkitCrossViewScanner) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.findings = nil
	s.seen = map[string]struct{}{}
	s.seq = 0
	s.stats = Stats{}
}
