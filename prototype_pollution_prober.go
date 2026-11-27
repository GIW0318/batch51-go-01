// Package task builds prototype pollution payloads, injects them into a
// registered JSON merge target, verifies the inherited side effect and
// recovers the shared object afterwards.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Payload path kinds.
const (
	PathProto       = "proto"
	PathConstructor = "constructor"
	PathArray       = "array"
)

// Merge modes of a registered target.
const (
	ModeSafe           = "safe"
	ModeVulnerable     = "vulnerable"
	ModeBlacklistProto = "blacklist_proto"
)

// Verification channels.
const (
	ChannelResponse   = "response"
	ChannelSideEffect = "side_effect"
)

// Reasons attached to a finding.
const (
	ReasonInherited         = "inherited"
	ReasonNotInherited      = "not_inherited"
	ReasonSideEffectOnly    = "side_effect_only"
	ReasonBlacklistBypassed = "blacklist_bypassed"
)

// ProtoKey is the prototype key used by the proto and array paths.
const ProtoKey = "__proto__"

// ConstructorKey and PrototypeKey build the constructor prototype path.
const (
	ConstructorKey = "constructor"
	PrototypeKey   = "prototype"
)

// Errors returned by the prober.
var (
	ErrBadTarget       = errors.New("target name is required")
	ErrDuplicateTarget = errors.New("target is already registered")
	ErrUnknownTarget   = errors.New("target is not registered")
	ErrBadKey          = errors.New("payload key is required")
	ErrBadPathKind     = errors.New("payload path kind is not supported")
	ErrBadPayload      = errors.New("payload does not carry a merge path")
	ErrBadMarker       = errors.New("marker is required")
	ErrBadMode         = errors.New("merge mode is not supported")
	ErrProbeBudget     = errors.New("probe budget exhausted for target")
	ErrBadLimit        = errors.New("prober limits must be positive")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// ProberConfig bounds one prober instance.
type ProberConfig struct {
	MaxConcurrent      int
	MaxProbesPerTarget int
	MaxResidualKeys    int
}

// TargetSpec describes one JSON merge target.
type TargetSpec struct {
	Name           string
	Mode           string
	Defaults       map[string]string
	WorkerIsolated bool
}

// Payload is one generated pollution probe.
type Payload struct {
	Kind   string
	Key    string
	Value  string
	Body   string
	Path   []string
	Marker string
}

// Response is what a target returns for one injected payload.
type Response struct {
	Accepted bool
	Status   int
	Merged   []string
	Worker   bool
}

// Verification is the read back of one polluted key.
type Verification struct {
	Inherited bool
	Value     string
	Channel   string
	Marker    string
}

// Recovery reports what a cleanup removed and what stayed behind.
type Recovery struct {
	Removed  []string
	Residual []string
	Seq      int64
}

// Baseline is the shared snapshot taken before a round of probes.
type Baseline struct {
	Keys map[string]string
	Seq  int64
}

// Finding is the graded outcome of one full probe.
type Finding struct {
	Target      string
	Kind        string
	Key         string
	Confirmed   bool
	Reason      string
	Channel     string
	BaselineSeq int64
	Seq         int64
}

// ProberStats counts the traffic of one target.
type ProberStats struct {
	Probes       int64
	Polluted     int64
	Recovered    int64
	Residual     int
	PeakInFlight int
}

type targetState struct {
	mu sync.Mutex

	name           string
	mode           string
	workerIsolated bool

	defaults  map[string]string
	overrides map[string]string
	isolated  map[string]string

	// pollutionOrder keeps first-write order and never contains duplicates.
	pollutionOrder []string

	baseline      Baseline
	baselineSet   bool
	baselineDirty bool

	// residual is the failure ledger. It is bounded by MaxResidualKeys and
	// append-only until a later recovery removes the corresponding key.
	residual []string

	findings []Finding
	stats    ProberStats
	inFlight int
}

// PrototypePollutionProber owns targets, baselines, findings and counters.
type PrototypePollutionProber struct {
	cfg ProberConfig

	mu      sync.RWMutex
	targets map[string]*targetState
	order   []string

	seqMu sync.Mutex
	seq   int64

	sem chan struct{}
}

// NewPrototypePollutionProber builds a prober with the given caps.
func NewPrototypePollutionProber(cfg ProberConfig) *PrototypePollutionProber {
	limit := cfg.MaxConcurrent
	if limit < 1 {
		limit = 1
	}
	return &PrototypePollutionProber{
		cfg:     cfg,
		targets: make(map[string]*targetState),
		sem:     make(chan struct{}, limit),
	}
}

// AddTarget registers one JSON merge target with its mode and default keys.
func (p *PrototypePollutionProber) AddTarget(spec TargetSpec) error {
	if err := validateConfig(p.cfg); err != nil {
		return err
	}
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return ErrBadTarget
	}
	if !validMode(spec.Mode) {
		return ErrBadMode
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureMapsLocked()
	if _, exists := p.targets[name]; exists {
		return ErrDuplicateTarget
	}
	t := &targetState{
		name:           name,
		mode:           spec.Mode,
		workerIsolated: spec.WorkerIsolated,
		defaults:       cloneStrings(spec.Defaults),
		overrides:      make(map[string]string),
		isolated:       make(map[string]string),
	}
	p.targets[name] = t
	p.order = append(p.order, name)
	return nil
}

// Targets lists the registered target names in registration order.
func (p *PrototypePollutionProber) Targets() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.order...)
}

// Generate builds one payload for the requested path kind.
func (p *PrototypePollutionProber) Generate(kind, key, value string) (Payload, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Payload{}, ErrBadKey
	}

	var (
		body string
		path []string
	)
	switch kind {
	case PathProto:
		data, err := json.Marshal(map[string]map[string]string{
			ProtoKey: {key: value},
		})
		if err != nil {
			return Payload{}, err
		}
		body = string(data)
		path = []string{ProtoKey, key}
	case PathConstructor:
		data, err := json.Marshal(map[string]map[string]map[string]string{
			ConstructorKey: {
				PrototypeKey: {key: value},
			},
		})
		if err != nil {
			return Payload{}, err
		}
		body = string(data)
		path = []string{ConstructorKey, PrototypeKey, key}
	case PathArray:
		data, err := json.Marshal([]map[string]map[string]string{
			{ProtoKey: {key: value}},
		})
		if err != nil {
			return Payload{}, err
		}
		body = string(data)
		path = []string{"[0]", ProtoKey, key}
	default:
		return Payload{}, ErrBadPathKind
	}

	return Payload{
		Kind:   kind,
		Key:    key,
		Value:  value,
		Body:   body,
		Path:   path,
		Marker: key,
	}, nil
}

// Inject merges one payload into a target and reports the applied keys.
func (p *PrototypePollutionProber) Inject(target string, payload Payload) (Response, error) {
	p.acquire()
	defer p.release()

	p.mu.RLock()
	t, err := p.lookupLocked(target)
	if err != nil {
		p.mu.RUnlock()
		return Response{}, err
	}
	t.mu.Lock()
	p.mu.RUnlock()
	defer t.mu.Unlock()

	p.beginInFlightLocked(t)
	defer p.endInFlightLocked(t)

	payload, err = inspectPayload(payload)
	if err != nil {
		return Response{}, err
	}
	return p.injectLocked(t, payload)
}

// Verify reads back one key through a freshly created object.
func (p *PrototypePollutionProber) Verify(target, marker string) (Verification, error) {
	p.mu.RLock()
	t, err := p.lookupLocked(target)
	if err != nil {
		p.mu.RUnlock()
		return Verification{}, err
	}
	t.mu.Lock()
	p.mu.RUnlock()
	defer t.mu.Unlock()

	marker = strings.TrimSpace(marker)
	if marker == "" {
		return Verification{}, ErrBadMarker
	}
	return verifyLocked(t, marker), nil
}

// Probe builds, injects, verifies and grades one payload end to end.
func (p *PrototypePollutionProber) Probe(target, kind, key, value string) (Finding, error) {
	p.acquire()
	defer p.release()

	p.mu.RLock()
	t, err := p.lookupLocked(target)
	if err != nil {
		p.mu.RUnlock()
		return Finding{}, err
	}
	t.mu.Lock()
	p.mu.RUnlock()
	defer t.mu.Unlock()

	p.beginInFlightLocked(t)
	defer p.endInFlightLocked(t)

	payload, err := p.Generate(kind, key, value)
	if err != nil {
		return Finding{}, err
	}
	if p.budgetExhaustedLocked(t) {
		return Finding{}, ErrProbeBudget
	}

	// A round keeps the same pre-injection snapshot. Subsequent probes must
	// not silently move that baseline forward after every side effect.
	if !t.baselineSet {
		p.takeBaselineLocked(t)
	}
	baselineSeq := t.baseline.Seq

	if _, err := p.injectLocked(t, payload); err != nil {
		return Finding{}, err
	}

	verification := verifyLocked(t, payload.Marker)
	confirmed := !payloadBlocked(t, payload) && verification.Inherited &&
		valueChangedFromBaseline(t.baseline, payload.Marker, verification.Value)
	finding := Finding{
		Target:      t.name,
		Kind:        payload.Kind,
		Key:         payload.Key,
		Confirmed:   confirmed,
		Reason:      ReasonNotInherited,
		Channel:     ChannelResponse,
		BaselineSeq: baselineSeq,
		Seq:         p.nextSeq(),
	}
	if confirmed {
		finding.Channel = verification.Channel
		switch {
		case verification.Channel == ChannelSideEffect:
			finding.Reason = ReasonSideEffectOnly
		case t.mode == ModeBlacklistProto && payload.Kind == PathConstructor:
			finding.Reason = ReasonBlacklistBypassed
		default:
			finding.Reason = ReasonInherited
		}
	}
	t.findings = append(t.findings, finding)
	return finding, nil
}

// Baseline takes and stores the shared snapshot of one target.
func (p *PrototypePollutionProber) Baseline(target string) (Baseline, error) {
	p.mu.RLock()
	t, err := p.lookupLocked(target)
	if err != nil {
		p.mu.RUnlock()
		return Baseline{}, err
	}
	t.mu.Lock()
	p.mu.RUnlock()
	defer t.mu.Unlock()

	return p.takeBaselineLocked(t), nil
}

// Recover removes the polluted keys of one target and reports the residue.
func (p *PrototypePollutionProber) Recover(target string) (Recovery, error) {
	p.mu.RLock()
	t, err := p.lookupLocked(target)
	if err != nil {
		p.mu.RUnlock()
		return Recovery{}, err
	}
	t.mu.Lock()
	p.mu.RUnlock()
	defer t.mu.Unlock()

	seen := make(map[string]struct{})
	removed := make([]string, 0, len(t.pollutionOrder))
	keys := append([]string(nil), t.pollutionOrder...)
	for _, key := range sortedExtraKeys(t.overrides, t.pollutionOrder) {
		keys = append(keys, key)
	}
	for _, key := range sortedExtraKeys(t.isolated, t.pollutionOrder) {
		keys = append(keys, key)
	}
	for _, key := range keys {
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		_, inOverrides := t.overrides[key]
		_, inIsolated := t.isolated[key]
		if !inOverrides && !inIsolated {
			continue
		}
		seen[key] = struct{}{}
		delete(t.overrides, key)
		delete(t.isolated, key)
		removed = append(removed, key)
	}

	t.pollutionOrder = append([]string(nil), removed...)
	t.stats.Recovered += int64(len(removed))

	left := pollutionKeysLocked(t)
	if len(left) > 0 {
		// A partial cleanup must leave an audit trail. The next automatic
		// baseline excludes these keys so comparison does not adopt pollution.
		t.residual = boundedUnique(t.residual, left, p.cfg.MaxResidualKeys)
		t.baselineSet = false
		t.baselineDirty = true
	} else {
		t.residual = nil
		t.baselineSet = false
		t.baselineDirty = false
	}
	t.stats.Residual = len(left)

	return Recovery{
		Removed:  removed,
		Residual: append([]string(nil), t.residual...),
		Seq:      p.nextSeq(),
	}, nil
}

// ResidualKeys lists the keys that are still inherited by new objects.
func (p *PrototypePollutionProber) ResidualKeys(target string) []string {
	p.mu.RLock()
	t, err := p.lookupLocked(target)
	if err != nil {
		p.mu.RUnlock()
		return nil
	}
	t.mu.Lock()
	p.mu.RUnlock()
	defer t.mu.Unlock()

	return t.residualKeysLocked(p.cfg.MaxResidualKeys)
}

// Stats reads the counters of one target.
func (p *PrototypePollutionProber) Stats(target string) ProberStats {
	p.mu.RLock()
	t, err := p.lookupLocked(target)
	if err != nil {
		p.mu.RUnlock()
		return ProberStats{}
	}
	t.mu.Lock()
	p.mu.RUnlock()
	defer t.mu.Unlock()

	stats := t.stats
	stats.Residual = len(t.residualKeysLocked(p.cfg.MaxResidualKeys))
	return stats
}

// Reset drops the findings of one target but keeps the merge state.
func (p *PrototypePollutionProber) Reset(target string) error {
	p.mu.RLock()
	t, err := p.lookupLocked(target)
	if err != nil {
		p.mu.RUnlock()
		return err
	}
	t.mu.Lock()
	p.mu.RUnlock()
	defer t.mu.Unlock()

	t.findings = nil
	return nil
}

const snapshotVersion = 1

type proberSnapshot struct {
	Version int              `json:"version"`
	Seq     int64            `json:"seq"`
	Targets []targetSnapshot `json:"targets"`
}

type targetSnapshot struct {
	Name           string            `json:"name"`
	Mode           string            `json:"mode"`
	WorkerIsolated bool              `json:"worker_isolated,omitempty"`
	Defaults       map[string]string `json:"defaults"`
	Shared         map[string]string `json:"shared"`
	Overrides      map[string]string `json:"overrides,omitempty"`
	Isolated       map[string]string `json:"isolated,omitempty"`
	PollutionOrder []string          `json:"pollution_order,omitempty"`
	Baseline       Baseline          `json:"baseline"`
	BaselineSet    bool              `json:"baseline_set"`
	BaselineDirty  bool              `json:"baseline_dirty,omitempty"`
	Residual       []string          `json:"residual,omitempty"`
	Findings       []Finding         `json:"findings,omitempty"`
	Stats          ProberStats       `json:"stats"`
}

// Export serialises targets, baselines, findings and counters.
func (p *PrototypePollutionProber) Export() ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	snapshot := proberSnapshot{
		Version: snapshotVersion,
		Seq:     p.currentSeq(),
		Targets: make([]targetSnapshot, 0, len(p.order)),
	}
	for _, name := range p.order {
		t := p.targets[name]
		if t == nil {
			continue
		}
		t.mu.Lock()
		snapshot.Targets = append(snapshot.Targets, targetSnapshot{
			Name:           t.name,
			Mode:           t.mode,
			WorkerIsolated: t.workerIsolated,
			Defaults:       cloneStrings(t.defaults),
			Shared:         effectiveLocked(t),
			Overrides:      cloneStrings(t.overrides),
			Isolated:       cloneStrings(t.isolated),
			PollutionOrder: append([]string(nil), t.pollutionOrder...),
			Baseline: Baseline{
				Keys: cloneStrings(t.baseline.Keys),
				Seq:  t.baseline.Seq,
			},
			BaselineSet:   t.baselineSet,
			BaselineDirty: t.baselineDirty,
			Residual:      append([]string(nil), t.residual...),
			Findings:      append([]Finding(nil), t.findings...),
			Stats:         t.stats,
		})
		t.mu.Unlock()
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Import replaces the whole state with a snapshot, all or nothing.
func (p *PrototypePollutionProber) Import(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}
	var snapshot proberSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	if snapshot.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	targets := make(map[string]*targetState, len(snapshot.Targets))
	order := make([]string, 0, len(snapshot.Targets))
	maxSeq := snapshot.Seq
	for _, saved := range snapshot.Targets {
		name := strings.TrimSpace(saved.Name)
		if name == "" || !validMode(saved.Mode) {
			return ErrBadSnapshot
		}
		if _, duplicate := targets[name]; duplicate {
			return ErrBadSnapshot
		}

		defaults := cloneStrings(saved.Defaults)
		overrides := cloneStrings(saved.Overrides)
		isolated := cloneStrings(saved.Isolated)
		if saved.Shared != nil && !equalStrings(saved.Shared, mergeStrings(defaults, overrides)) {
			return ErrBadSnapshot
		}
		if saved.Stats.Probes < 0 || saved.Stats.Polluted < 0 ||
			saved.Stats.Recovered < 0 || saved.Stats.Residual < 0 ||
			saved.Stats.PeakInFlight < 0 {
			return ErrBadSnapshot
		}
		if saved.BaselineSet && saved.Baseline.Seq <= 0 {
			return ErrBadSnapshot
		}

		pollutionOrder := sanitizeOrder(saved.PollutionOrder, overrides, isolated)
		residual := boundedUnique(nil, saved.Residual, p.cfg.MaxResidualKeys)
		t := &targetState{
			name:           name,
			mode:           saved.Mode,
			workerIsolated: saved.WorkerIsolated,
			defaults:       defaults,
			overrides:      overrides,
			isolated:       isolated,
			pollutionOrder: pollutionOrder,
			baseline: Baseline{
				Keys: cloneStrings(saved.Baseline.Keys),
				Seq:  saved.Baseline.Seq,
			},
			baselineSet:   saved.BaselineSet,
			baselineDirty: saved.BaselineDirty,
			residual:      residual,
			findings:      append([]Finding(nil), saved.Findings...),
			stats:         saved.Stats,
		}
		if t.baseline.Seq > maxSeq {
			maxSeq = t.baseline.Seq
		}
		for _, finding := range t.findings {
			if finding.Seq > maxSeq {
				maxSeq = finding.Seq
			}
			if finding.BaselineSeq > maxSeq {
				maxSeq = finding.BaselineSeq
			}
		}
		targets[name] = t
		order = append(order, name)
	}
	if maxSeq < 0 {
		return ErrBadSnapshot
	}

	p.mu.Lock()
	p.seqMu.Lock()
	p.targets = targets
	p.order = order
	p.seq = maxSeq
	p.seqMu.Unlock()
	p.mu.Unlock()
	return nil
}

func validateConfig(cfg ProberConfig) error {
	if cfg.MaxConcurrent <= 0 || cfg.MaxProbesPerTarget <= 0 || cfg.MaxResidualKeys <= 0 {
		return ErrBadLimit
	}
	return nil
}

func validMode(mode string) bool {
	switch mode {
	case ModeSafe, ModeVulnerable, ModeBlacklistProto:
		return true
	default:
		return false
	}
}

func (p *PrototypePollutionProber) ensureMapsLocked() {
	if p.targets == nil {
		p.targets = make(map[string]*targetState)
	}
	if p.sem == nil {
		limit := p.cfg.MaxConcurrent
		if limit < 1 {
			limit = 1
		}
		p.sem = make(chan struct{}, limit)
	}
}

func (p *PrototypePollutionProber) lookupLocked(name string) (*targetState, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrUnknownTarget
	}
	t := p.targets[name]
	if t == nil {
		return nil, ErrUnknownTarget
	}
	return t, nil
}

func (p *PrototypePollutionProber) acquire() {
	if p.sem != nil {
		p.sem <- struct{}{}
	}
}

func (p *PrototypePollutionProber) release() {
	if p.sem != nil {
		<-p.sem
	}
}

func (p *PrototypePollutionProber) nextSeq() int64 {
	p.seqMu.Lock()
	p.seq++
	seq := p.seq
	p.seqMu.Unlock()
	return seq
}

func (p *PrototypePollutionProber) currentSeq() int64 {
	p.seqMu.Lock()
	seq := p.seq
	p.seqMu.Unlock()
	return seq
}

func (p *PrototypePollutionProber) beginInFlightLocked(t *targetState) {
	t.inFlight++
	if t.inFlight > t.stats.PeakInFlight {
		t.stats.PeakInFlight = t.inFlight
	}
}

func (p *PrototypePollutionProber) endInFlightLocked(t *targetState) {
	t.inFlight--
}

func (p *PrototypePollutionProber) budgetExhaustedLocked(t *targetState) bool {
	return t.stats.Probes >= int64(p.cfg.MaxProbesPerTarget)
}

func (p *PrototypePollutionProber) injectLocked(t *targetState, payload Payload) (Response, error) {
	if p.budgetExhaustedLocked(t) {
		return Response{}, ErrProbeBudget
	}

	response := Response{Accepted: true, Status: 200, Worker: t.workerIsolated}
	if payloadBlocked(t, payload) {
		t.stats.Probes++
		return response, nil
	}

	if t.workerIsolated {
		t.isolated[payload.Key] = payload.Value
		t.recordPollutionLocked(payload.Key)
		// A worker-local mutation is not visible in this response. Verify and
		// the side-effect interfaces can still observe it.
	} else {
		t.overrides[payload.Key] = payload.Value
		t.recordPollutionLocked(payload.Key)
		response.Merged = []string{payload.Key}
	}
	t.stats.Probes++
	t.stats.Polluted++
	return response, nil
}

func payloadBlocked(t *targetState, payload Payload) bool {
	return t.mode == ModeSafe ||
		(t.mode == ModeBlacklistProto && (payload.Kind == PathProto || payload.Kind == PathArray))
}

func (t *targetState) recordPollutionLocked(key string) {
	for _, existing := range t.pollutionOrder {
		if existing == key {
			return
		}
	}
	t.pollutionOrder = append(t.pollutionOrder, key)
}

func verifyLocked(t *targetState, marker string) Verification {
	if value, ok := t.overrides[marker]; ok {
		return Verification{
			Inherited: true,
			Value:     value,
			Channel:   ChannelResponse,
			Marker:    marker,
		}
	}
	if value, ok := t.defaults[marker]; ok {
		return Verification{
			Inherited: true,
			Value:     value,
			Channel:   ChannelResponse,
			Marker:    marker,
		}
	}
	if value, ok := t.isolated[marker]; ok {
		return Verification{
			Inherited: true,
			Value:     value,
			Channel:   ChannelSideEffect,
			Marker:    marker,
		}
	}
	return Verification{Channel: ChannelResponse, Marker: marker}
}

func valueChangedFromBaseline(baseline Baseline, key, value string) bool {
	previous, existed := baseline.Keys[key]
	return !existed || previous != value
}

func (p *PrototypePollutionProber) takeBaselineLocked(t *targetState) Baseline {
	keys := effectiveLocked(t)
	if t.baselineDirty {
		for _, key := range t.residual {
			delete(keys, key)
		}
	}
	t.baseline = Baseline{Keys: keys, Seq: p.nextSeq()}
	t.baselineSet = true
	t.baselineDirty = false
	return Baseline{Keys: cloneStrings(keys), Seq: t.baseline.Seq}
}

func effectiveLocked(t *targetState) map[string]string {
	return mergeStrings(t.defaults, t.overrides)
}

func mergeStrings(base, override map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(override))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}

func pollutionKeysLocked(t *targetState) []string {
	seen := make(map[string]struct{}, len(t.overrides)+len(t.isolated))
	keys := make([]string, 0, len(t.overrides)+len(t.isolated))
	for _, key := range t.pollutionOrder {
		_, inOverrides := t.overrides[key]
		_, inIsolated := t.isolated[key]
		if !inOverrides && !inIsolated {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	extras := make([]string, 0)
	for key := range t.overrides {
		extras = append(extras, key)
	}
	for key := range t.isolated {
		extras = append(extras, key)
	}
	sort.Strings(extras)
	for _, key := range extras {
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func (t *targetState) residualKeysLocked(limit int) []string {
	keys := pollutionKeysLocked(t)
	for _, key := range t.residual {
		if !containsString(keys, key) {
			keys = append(keys, key)
		}
	}
	return boundedUnique(nil, keys, limit)
}

func sortedExtraKeys(values map[string]string, ordered []string) []string {
	known := make(map[string]struct{}, len(ordered))
	for _, key := range ordered {
		known[key] = struct{}{}
	}
	extras := make([]string, 0)
	for key := range values {
		if _, ok := known[key]; !ok {
			extras = append(extras, key)
		}
	}
	sort.Strings(extras)
	return extras
}

func sanitizeOrder(order []string, maps ...map[string]string) []string {
	seen := make(map[string]struct{}, len(order))
	result := make([]string, 0, len(order))
	for _, key := range order {
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		present := false
		for _, values := range maps {
			if _, ok := values[key]; ok {
				present = true
				break
			}
		}
		if !present {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	extras := make([]string, 0)
	for _, values := range maps {
		for key := range values {
			if _, ok := seen[key]; !ok {
				extras = append(extras, key)
			}
		}
	}
	sort.Strings(extras)
	for _, key := range extras {
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func boundedUnique(dst, src []string, limit int) []string {
	if limit < 0 {
		limit = 0
	}
	seen := make(map[string]struct{}, len(dst)+len(src))
	for _, key := range dst {
		seen[key] = struct{}{}
	}
	for _, key := range src {
		if key == "" {
			continue
		}
		if len(dst) >= limit {
			break
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		dst = append(dst, key)
	}
	return dst
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneStrings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func equalStrings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func inspectPayload(payload Payload) (Payload, error) {
	if strings.TrimSpace(payload.Body) == "" || len(payload.Path) == 0 {
		return Payload{}, ErrBadPayload
	}

	kind := payload.Kind
	if kind != "" && kind != PathProto && kind != PathConstructor && kind != PathArray {
		return Payload{}, ErrBadPathKind
	}
	key := strings.TrimSpace(payload.Key)
	pathKey := strings.TrimSpace(payload.Path[len(payload.Path)-1])
	if key == "" {
		key = pathKey
	}
	if key == "" || pathKey == "" {
		return Payload{}, ErrBadKey
	}
	if pathKey != key {
		return Payload{}, ErrBadPayload
	}
	if kind == "" {
		kind = kindFromPath(payload.Path)
	}
	if kind == "" || !pathMatchesKind(kind, payload.Path) {
		return Payload{}, ErrBadPayload
	}

	var decoded any
	if err := json.Unmarshal([]byte(payload.Body), &decoded); err != nil {
		return Payload{}, ErrBadPayload
	}
	bodyValue, ok := bodyValueAtPath(decoded, kind, key)
	if !ok {
		return Payload{}, ErrBadPayload
	}
	if payload.Value == "" {
		payload.Value = bodyValue
	} else if bodyValue != payload.Value {
		return Payload{}, ErrBadPayload
	}

	payload.Kind = kind
	payload.Key = key
	payload.Path = append([]string(nil), payload.Path...)
	if strings.TrimSpace(payload.Marker) == "" {
		payload.Marker = key
	}
	return payload, nil
}

func kindFromPath(path []string) string {
	switch {
	case len(path) == 2 && path[0] == ProtoKey:
		return PathProto
	case len(path) == 3 && path[0] == ConstructorKey && path[1] == PrototypeKey:
		return PathConstructor
	case len(path) == 3 && path[0] == "[0]" && path[1] == ProtoKey:
		return PathArray
	default:
		return ""
	}
}

func pathMatchesKind(kind string, path []string) bool {
	return kindFromPath(path) == kind
}

func bodyValueAtPath(decoded any, kind, key string) (string, bool) {
	switch kind {
	case PathProto:
		root, ok := decoded.(map[string]any)
		if !ok {
			return "", false
		}
		proto, ok := root[ProtoKey].(map[string]any)
		if !ok {
			return "", false
		}
		value, ok := proto[key].(string)
		return value, ok
	case PathConstructor:
		root, ok := decoded.(map[string]any)
		if !ok {
			return "", false
		}
		constructor, ok := root[ConstructorKey].(map[string]any)
		if !ok {
			return "", false
		}
		prototype, ok := constructor[PrototypeKey].(map[string]any)
		if !ok {
			return "", false
		}
		value, ok := prototype[key].(string)
		return value, ok
	case PathArray:
		root, ok := decoded.([]any)
		if !ok || len(root) == 0 {
			return "", false
		}
		first, ok := root[0].(map[string]any)
		if !ok {
			return "", false
		}
		proto, ok := first[ProtoKey].(map[string]any)
		if !ok {
			return "", false
		}
		value, ok := proto[key].(string)
		return value, ok
	default:
		return "", false
	}
}
