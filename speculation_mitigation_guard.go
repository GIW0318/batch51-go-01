// Package task plans and enforces per process speculative execution mitigations:
// it intersects a policy with the host CPU capabilities, keeps the mitigation
// level monotonic and hands the same level to every thread of a process.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// CPU vulnerability bits reported by the host profile.
const (
	VulnSpectre1 uint32 = 1 << 0
	VulnSpectre2 uint32 = 1 << 1
	VulnMeltdown uint32 = 1 << 2
	VulnMds      uint32 = 1 << 3
	VulnL1tf     uint32 = 1 << 4
)

// Mitigation feature names understood by Plan and Relax.
const (
	FeatRetpoline = "retpoline"
	FeatIbrs      = "ibrs"
	FeatIbpb      = "ibpb"
	FeatSsbd      = "ssbd"
	FeatMdClear   = "md_clear"
	FeatL1dFlush  = "l1d_flush"
	FeatVerw      = "verw"
)

// Feature weights used to score a mitigation level.
const (
	WeightRetpoline = 2
	WeightIbrs      = 2
	WeightIbpb      = 1
	WeightSsbd      = 2
	WeightMdClear   = 3
	WeightL1dFlush  = 1
	WeightVerw      = 2
)

// Errors returned by the guard.
var (
	ErrBadPolicy          = errors.New("mitigation policy is invalid")
	ErrBadProcess         = errors.New("process id is invalid")
	ErrUnsatisfiable      = errors.New("host cannot satisfy the mitigation policy")
	ErrUnknownProcess     = errors.New("process is not tracked")
	ErrDowngradeBlocked   = errors.New("mitigation level may only be raised")
	ErrBadReason          = errors.New("relaxation reason is required")
	ErrUnknownFeature     = errors.New("feature is not part of the current level")
	ErrRelaxWindowExpired = errors.New("relaxation window is closed")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// HostProfile is the CPU picture of one machine.
type HostProfile struct {
	Model     string   `json:"model"`
	Vulns     uint32   `json:"vulns"`
	Supported []string `json:"supported"`
}

// Policy is the mitigation contract a process must meet.
type Policy struct {
	Name     string   `json:"name"`
	Required []string `json:"required"`
	MinLevel int      `json:"min_level"`
}

// MitigationPlan is the resolved, explainable feature set.
type MitigationPlan struct {
	Policy      string   `json:"policy"`
	Level       int      `json:"level"`
	Features    []string `json:"features"`
	Missing     []string `json:"missing"`
	Satisfiable bool     `json:"satisfiable"`
	Digest      string   `json:"digest"`
}

// MitigationState is the live record of one process.
type MitigationState struct {
	PID          int       `json:"pid"`
	Level        int       `json:"level"`
	BaseLevel    int       `json:"base_level"`
	Features     []string  `json:"features"`
	Generation   int64     `json:"generation"`
	Relaxed      bool      `json:"relaxed"`
	RelaxFeature string    `json:"relax_feature"`
	RelaxUntil   time.Time `json:"relax_until"`
	RelaxCount   int       `json:"relax_count"`
}

// ExpiryEvent is emitted when a temporary relaxation falls back.
type ExpiryEvent struct {
	PID     int       `json:"pid"`
	Feature string    `json:"feature"`
	Level   int       `json:"level"`
	At      time.Time `json:"at"`
}

// GuardConfig bounds the relaxation windows.
type GuardConfig struct {
	MaxRelax     time.Duration
	DefaultLevel int
}

const (
	snapshotMagic   = "gsb-speculation-mitigation-guard"
	snapshotVersion = 1
)

// relaxWindow is one temporary feature exemption.
type relaxWindow struct {
	Feature string    `json:"feature"`
	Until   time.Time `json:"until"`
}

// processRecord is the internal, authoritative state of one process.
type processRecord struct {
	PID          int
	Level        int
	BaseLevel    int
	Features     []string
	BaseFeatures []string
	Generation   int64
	RelaxCount   int
	Relaxes      []relaxWindow
}

// SpeculationMitigationGuard owns the per process mitigation records.
type SpeculationMitigationGuard struct {
	mu        sync.Mutex
	cfg       GuardConfig
	processes map[int]*processRecord
}

// NewSpeculationMitigationGuard builds a guard with the given limits.
func NewSpeculationMitigationGuard(cfg GuardConfig) *SpeculationMitigationGuard {
	return &SpeculationMitigationGuard{
		cfg:       cfg,
		processes: make(map[int]*processRecord),
	}
}

// FeatureWeight reports the level contribution of one feature name.
func FeatureWeight(name string) int {
	switch name {
	case FeatRetpoline:
		return WeightRetpoline
	case FeatIbrs:
		return WeightIbrs
	case FeatIbpb:
		return WeightIbpb
	case FeatSsbd:
		return WeightSsbd
	case FeatMdClear:
		return WeightMdClear
	case FeatL1dFlush:
		return WeightL1dFlush
	case FeatVerw:
		return WeightVerw
	default:
		return 0
	}
}

func knownFeature(name string) bool {
	return FeatureWeight(name) != 0
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func normalizeFeatures(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, name := range in {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func removeString(items []string, want string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item != want {
			out = append(out, item)
		}
	}
	return out
}

func levelForFeatures(features []string) int {
	level := 0
	for _, name := range features {
		level += FeatureWeight(name)
	}
	return level
}

func planDigest(policy string, features []string, level int) string {
	payload := struct {
		Policy   string   `json:"policy"`
		Features []string `json:"features"`
		Level    int      `json:"level"`
	}{
		Policy:   policy,
		Features: features,
		Level:    level,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Plan resolves a policy against a host and explains what is missing.
func (g *SpeculationMitigationGuard) Plan(host HostProfile, policy Policy) (MitigationPlan, error) {
	if strings.TrimSpace(host.Model) == "" {
		return MitigationPlan{}, ErrBadPolicy
	}
	if len(policy.Required) == 0 {
		return MitigationPlan{}, ErrBadPolicy
	}
	for _, name := range policy.Required {
		if !knownFeature(name) {
			return MitigationPlan{}, ErrBadPolicy
		}
	}

	requested := normalizeFeatures(policy.Required)
	supported := make(map[string]struct{}, len(host.Supported))
	for _, name := range host.Supported {
		supported[name] = struct{}{}
	}

	available := make([]string, 0, len(requested))
	missing := make([]string, 0, len(requested))
	for _, name := range requested {
		if _, ok := supported[name]; ok || implicitlySupported(host, name) {
			available = append(available, name)
		} else {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)

	level := levelForFeatures(available)
	if policy.MinLevel > level {
		level = policy.MinLevel
	}

	plan := MitigationPlan{
		Policy:      policy.Name,
		Level:       level,
		Features:    available,
		Missing:     missing,
		Satisfiable: len(missing) == 0,
		Digest:      planDigest(policy.Name, available, level),
	}
	if len(missing) != 0 {
		return plan, ErrUnsatisfiable
	}
	return plan, nil
}

// implicitlySupported covers mitigations that are not listed by an otherwise
// capable host because the corresponding vulnerability is not present. VERW is
// a generic buffer-clearing primitive that is only material on MDS-capable CPUs.
func implicitlySupported(host HostProfile, name string) bool {
	return name == FeatVerw && host.Vulns&VulnMds == 0
}

// Apply installs a plan on one process.
func (g *SpeculationMitigationGuard) Apply(pid int, plan MitigationPlan) error {
	if pid <= 0 {
		return ErrBadProcess
	}

	features := normalizeFeatures(plan.Features)
	level := plan.Level

	g.mu.Lock()
	defer g.mu.Unlock()

	rec, ok := g.processes[pid]
	if !ok {
		g.processes[pid] = &processRecord{
			PID:          pid,
			Level:        level,
			BaseLevel:    level,
			Features:     cloneStrings(features),
			BaseFeatures: cloneStrings(features),
			Generation:   1,
		}
		return nil
	}

	// Re-applying the exact policy that established the baseline is a no-op.
	if level == rec.BaseLevel && equalStrings(features, rec.BaseFeatures) {
		return nil
	}
	if level < rec.BaseLevel {
		return ErrDowngradeBlocked
	}

	rec.Level = level
	rec.BaseLevel = level
	rec.Features = cloneStrings(features)
	rec.BaseFeatures = cloneStrings(features)
	rec.Relaxes = nil
	rec.Generation++
	return nil
}

// Read returns the live state of one process.
func (g *SpeculationMitigationGuard) Read(pid int) (MitigationState, error) {
	if pid <= 0 {
		return MitigationState{}, ErrBadProcess
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rec, ok := g.processes[pid]
	if !ok {
		return MitigationState{}, ErrUnknownProcess
	}
	return stateFromRecord(rec), nil
}

func stateFromRecord(rec *processRecord) MitigationState {
	state := MitigationState{
		PID:        rec.PID,
		Level:      rec.Level,
		BaseLevel:  rec.BaseLevel,
		Features:   cloneStrings(rec.Features),
		Generation: rec.Generation,
		Relaxed:    len(rec.Relaxes) != 0,
		RelaxCount: rec.RelaxCount,
	}
	if len(rec.Relaxes) != 0 {
		state.RelaxFeature = rec.Relaxes[0].Feature
		state.RelaxUntil = rec.Relaxes[0].Until
	}
	return state
}

// Relax drops one feature for a bounded window with an operator reason.
func (g *SpeculationMitigationGuard) Relax(pid int, feature string, reason string) error {
	if pid <= 0 {
		return ErrBadProcess
	}
	if strings.TrimSpace(reason) == "" {
		return ErrBadReason
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rec, ok := g.processes[pid]
	if !ok {
		return ErrUnknownProcess
	}
	if !containsString(rec.Features, feature) || !knownFeature(feature) {
		return ErrUnknownFeature
	}
	if g.cfg.MaxRelax <= 0 {
		return ErrRelaxWindowExpired
	}

	rec.Features = removeString(rec.Features, feature)
	weight := FeatureWeight(feature)
	if weight >= rec.Level {
		rec.Level = 0
	} else {
		rec.Level -= weight
	}

	until := time.Now().Add(g.cfg.MaxRelax)
	rec.Relaxes = append(rec.Relaxes, relaxWindow{Feature: feature, Until: until})
	sort.Slice(rec.Relaxes, func(i, j int) bool {
		if rec.Relaxes[i].Until.Equal(rec.Relaxes[j].Until) {
			return rec.Relaxes[i].Feature < rec.Relaxes[j].Feature
		}
		return rec.Relaxes[i].Until.Before(rec.Relaxes[j].Until)
	})
	rec.RelaxCount++
	rec.Generation++
	return nil
}

// Inherit copies the parent policy to a forked or exec'd child.
func (g *SpeculationMitigationGuard) Inherit(parent, child int) error {
	if parent <= 0 || child <= 0 {
		return ErrBadProcess
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	parentRec, ok := g.processes[parent]
	if !ok {
		return ErrUnknownProcess
	}

	childRec, ok := g.processes[child]
	if !ok {
		g.processes[child] = &processRecord{
			PID:          child,
			Level:        parentRec.BaseLevel,
			BaseLevel:    parentRec.BaseLevel,
			Features:     cloneStrings(parentRec.BaseFeatures),
			BaseFeatures: cloneStrings(parentRec.BaseFeatures),
			Generation:   1,
		}
		return nil
	}

	// A child that already carries a higher baseline keeps its own policy.
	if parentRec.BaseLevel > childRec.BaseLevel {
		childRec.Level = parentRec.BaseLevel
		childRec.BaseLevel = parentRec.BaseLevel
		childRec.Features = cloneStrings(parentRec.BaseFeatures)
		childRec.BaseFeatures = cloneStrings(parentRec.BaseFeatures)
		childRec.Relaxes = nil
		childRec.Generation++
	}
	return nil
}

// Exec re-checks a process after exec and keeps its state.
func (g *SpeculationMitigationGuard) Exec(pid int) (MitigationState, error) {
	if pid <= 0 {
		return MitigationState{}, ErrBadProcess
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rec, ok := g.processes[pid]
	if !ok {
		return MitigationState{}, ErrUnknownProcess
	}
	rec.Generation++
	return stateFromRecord(rec), nil
}

// Exit forgets a finished process.
func (g *SpeculationMitigationGuard) Exit(pid int) error {
	if pid <= 0 {
		return ErrBadProcess
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.processes[pid]; !ok {
		return ErrUnknownProcess
	}
	delete(g.processes, pid)
	return nil
}

// Tick expires the relaxation windows that are already over.
func (g *SpeculationMitigationGuard) Tick(now time.Time) []ExpiryEvent {
	g.mu.Lock()
	defer g.mu.Unlock()

	events := make([]ExpiryEvent, 0)
	for pid, rec := range g.processes {
		if len(rec.Relaxes) == 0 {
			continue
		}

		remaining := make([]relaxWindow, 0, len(rec.Relaxes))
		expired := make([]relaxWindow, 0, len(rec.Relaxes))
		for _, window := range rec.Relaxes {
			if !now.Before(window.Until) {
				expired = append(expired, window)
			} else {
				remaining = append(remaining, window)
			}
		}
		if len(expired) == 0 {
			continue
		}

		for _, window := range expired {
			if !containsString(rec.Features, window.Feature) {
				rec.Features = append(rec.Features, window.Feature)
				sort.Strings(rec.Features)
			}
			rec.Level += FeatureWeight(window.Feature)
			events = append(events, ExpiryEvent{
				PID:     pid,
				Feature: window.Feature,
				Level:   rec.Level,
				At:      now,
			})
		}

		rec.Relaxes = remaining
		if len(remaining) == 0 {
			rec.Level = rec.BaseLevel
			rec.Features = cloneStrings(rec.BaseFeatures)
		}
		rec.Generation++
	}

	sort.Slice(events, func(i, j int) bool {
		if events[i].PID != events[j].PID {
			return events[i].PID < events[j].PID
		}
		return events[i].Feature < events[j].Feature
	})
	return events
}

// snapshotProcess is the durable form of one process record.
type snapshotProcess struct {
	PID          int           `json:"pid"`
	Level        int           `json:"level"`
	BaseLevel    int           `json:"base_level"`
	Features     []string      `json:"features"`
	BaseFeatures []string      `json:"base_features"`
	Generation   int64         `json:"generation"`
	RelaxCount   int           `json:"relax_count"`
	Relaxes      []relaxWindow `json:"relaxes,omitempty"`
}

// snapshotEnvelope is the versioned, all-or-nothing snapshot payload.
type snapshotEnvelope struct {
	Magic     string            `json:"magic"`
	Version   int               `json:"version"`
	Processes []snapshotProcess `json:"processes"`
}

// Snapshot serialises the process table and the relaxation deadlines.
func (g *SpeculationMitigationGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	processes := make([]snapshotProcess, 0, len(g.processes))
	for _, rec := range g.processes {
		item := snapshotProcess{
			PID:          rec.PID,
			Level:        rec.Level,
			BaseLevel:    rec.BaseLevel,
			Features:     cloneStrings(rec.Features),
			BaseFeatures: cloneStrings(rec.BaseFeatures),
			Generation:   rec.Generation,
			RelaxCount:   rec.RelaxCount,
		}
		if len(rec.Relaxes) != 0 {
			item.Relaxes = make([]relaxWindow, len(rec.Relaxes))
			copy(item.Relaxes, rec.Relaxes)
		}
		processes = append(processes, item)
	}
	sort.Slice(processes, func(i, j int) bool {
		return processes[i].PID < processes[j].PID
	})

	payload := snapshotEnvelope{
		Magic:     snapshotMagic,
		Version:   snapshotVersion,
		Processes: processes,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *SpeculationMitigationGuard) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var payload snapshotEnvelope
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Magic != snapshotMagic || payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	restored := make(map[int]*processRecord, len(payload.Processes))
	for _, item := range payload.Processes {
		rec, ok := recordFromSnapshot(item)
		if !ok {
			return ErrBadSnapshot
		}
		if _, exists := restored[rec.PID]; exists {
			return ErrBadSnapshot
		}
		restored[rec.PID] = rec
	}

	g.mu.Lock()
	g.processes = restored
	g.mu.Unlock()
	return nil
}

func recordFromSnapshot(item snapshotProcess) (*processRecord, bool) {
	if item.PID <= 0 || item.Generation <= 0 || item.RelaxCount < 0 {
		return nil, false
	}
	if item.Level < 0 || item.BaseLevel < 0 || item.Level > item.BaseLevel {
		return nil, false
	}

	features := normalizeFeatures(item.Features)
	baseFeatures := normalizeFeatures(item.BaseFeatures)
	for _, name := range features {
		if !knownFeature(name) {
			return nil, false
		}
	}
	for _, name := range baseFeatures {
		if !knownFeature(name) {
			return nil, false
		}
	}

	relaxes := make([]relaxWindow, 0, len(item.Relaxes))
	seen := make(map[string]struct{}, len(item.Relaxes))
	for _, window := range item.Relaxes {
		if !knownFeature(window.Feature) || window.Until.IsZero() {
			return nil, false
		}
		if _, exists := seen[window.Feature]; exists {
			return nil, false
		}
		if containsString(features, window.Feature) {
			return nil, false
		}
		seen[window.Feature] = struct{}{}
		relaxes = append(relaxes, window)
	}
	sort.Slice(relaxes, func(i, j int) bool {
		if relaxes[i].Until.Equal(relaxes[j].Until) {
			return relaxes[i].Feature < relaxes[j].Feature
		}
		return relaxes[i].Until.Before(relaxes[j].Until)
	})

	// The baseline must be exactly the live set plus the exempted features.
	expectedBase := cloneStrings(features)
	for _, window := range relaxes {
		expectedBase = append(expectedBase, window.Feature)
	}
	expectedBase = normalizeFeatures(expectedBase)
	if !equalStrings(baseFeatures, expectedBase) {
		return nil, false
	}

	exemptedWeight := 0
	for _, window := range relaxes {
		exemptedWeight += FeatureWeight(window.Feature)
	}
	if item.BaseLevel-exemptedWeight != item.Level {
		return nil, false
	}

	return &processRecord{
		PID:          item.PID,
		Level:        item.Level,
		BaseLevel:    item.BaseLevel,
		Features:     features,
		BaseFeatures: baseFeatures,
		Generation:   item.Generation,
		RelaxCount:   item.RelaxCount,
		Relaxes:      relaxes,
	}, true
}
