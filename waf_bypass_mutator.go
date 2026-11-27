// Package task plans equivalent payload variants for a confirmed injection
// point and decides which of them really got past the edge device.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Mutation operators, in the fixed ranking order.
const (
	OpURL       = "enc_url"
	OpDouble    = "enc_double"
	OpUnicode   = "enc_unicode"
	OpSeparator = "sep_equiv"
	OpSplit     = "keyword_split"
	OpCase      = "case_swap"
)

var operatorOrder = [...]string{
	OpURL,
	OpDouble,
	OpUnicode,
	OpSeparator,
	OpSplit,
	OpCase,
}

// Outcome reasons.
const (
	ReasonAllowed       = "reason_allowed"
	ReasonWAFRule       = "reason_waf_rule"
	ReasonIndeterminate = "reason_indeterminate"
	ReasonClientError   = "reason_client_error"
	ReasonBypass        = "reason_bypass"
	ReasonBlocked       = "reason_blocked"
)

// Errors reported by the mutator.
var (
	ErrNilProbe         = errors.New("probe is nil")
	ErrBadPoint         = errors.New("injection point is incomplete")
	ErrEmptyPayload     = errors.New("payload is empty")
	ErrNoBaseline       = errors.New("baseline of this point was not measured")
	ErrStopped          = errors.New("mutator is stopped")
	ErrBadReason        = errors.New("stop reason is empty")
	ErrDuplicateVariant = errors.New("variant was already submitted")
	ErrThrottled        = errors.New("mutator is throttled after repeated blocks")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// InjectionPoint is one confirmed injection entry.
type InjectionPoint struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Param    string `json:"param"`
	Layer    string `json:"layer"`
	Baseline string `json:"baseline"`
}

// Payload is the original payload bound to one injection point.
type Payload struct {
	ID    string `json:"id"`
	Point string `json:"point"`
	Raw   string `json:"raw"`
}

// PayloadVariant is one encoded form of a payload.
type PayloadVariant struct {
	ID      string `json:"id"`
	Base    string `json:"base"`
	Op      string `json:"op"`
	Encoded string `json:"encoded"`
	Point   string `json:"point"`
}

// Response is what the edge or the backend answered.
type Response struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
	Effect bool   `json:"effect"`
}

// Probe sends one encoded payload to one injection point.
type Probe interface {
	Send(point InjectionPoint, encoded string) (Response, error)
}

// Blocked is the classification of the untouched payload.
type Blocked struct {
	Status  int    `json:"status"`
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason"`
}

// Outcome is the classification of one submitted variant.
type Outcome struct {
	Variant   string `json:"variant"`
	Status    int    `json:"status"`
	Passed    bool   `json:"passed"`
	Reached   bool   `json:"reached"`
	Effect    bool   `json:"effect"`
	Effective bool   `json:"effective"`
	Reason    string `json:"reason"`
	Delay     int    `json:"delay_ms"`
}

// MutatorStats is the running ledger of the mutator.
type MutatorStats struct {
	Sent              int    `json:"sent"`
	Blocked           int    `json:"blocked"`
	Effective         int    `json:"effective"`
	ConsecutiveBlocks int    `json:"consecutive_blocks"`
	Throttled         bool   `json:"throttled"`
	Stopped           bool   `json:"stopped"`
	StopReason        string `json:"stop_reason"`
}

// Limits bounds the run.
type Limits struct {
	MaxSends      int
	ThrottleAfter int
}

type baselineRecord struct {
	Point   InjectionPoint `json:"point"`
	Blocked Blocked        `json:"blocked"`
}

const (
	snapshotFormat  = "waf-bypass-mutator"
	snapshotVersion = 1
)

type mutatorSnapshot struct {
	Format            string                    `json:"format"`
	Version           int                       `json:"version"`
	Baselines         map[string]baselineRecord `json:"baselines"`
	Tried             []string                  `json:"tried"`
	EffectiveIDs      []string                  `json:"effective_ids"`
	EffectiveCounts   map[string]int            `json:"effective_counts"`
	EffectiveTotal    int                       `json:"effective_total"`
	Sent              int                       `json:"sent"`
	Blocked           int                       `json:"blocked"`
	ConsecutiveBlocks int                       `json:"consecutive_blocks"`
	Throttled         bool                      `json:"throttled"`
	Stopped           bool                      `json:"stopped"`
	StopReason        string                    `json:"stop_reason"`
}

// WafBypassMutator owns the baseline table, the tried set and the throttle.
type WafBypassMutator struct {
	mu       sync.Mutex
	probe    Probe
	limits   Limits
	baseline map[string]baselineRecord

	tried           map[string]struct{}
	effective       map[string]struct{}
	effectiveCounts map[string]int

	sent              int
	blocked           int
	effectiveTotal    int
	consecutiveBlocks int
	throttled         bool
	stopped           bool
	stopReason        string
}

// NewWafBypassMutator builds a mutator over the given probe.
func NewWafBypassMutator(probe Probe, limits Limits) *WafBypassMutator {
	return &WafBypassMutator{
		probe:           probe,
		limits:          limits,
		baseline:        make(map[string]baselineRecord),
		tried:           make(map[string]struct{}),
		effective:       make(map[string]struct{}),
		effectiveCounts: make(map[string]int),
	}
}

// Baseline measures whether the untouched payload is blocked.
func (m *WafBypassMutator) Baseline(point InjectionPoint) (Blocked, error) {
	if !validPoint(point) {
		return Blocked{}, ErrBadPoint
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureMaps()

	if m.probe == nil {
		return Blocked{}, ErrNilProbe
	}
	if m.stopped {
		return Blocked{}, ErrStopped
	}
	if m.throttled {
		return Blocked{}, ErrThrottled
	}

	response, err := m.probe.Send(point, point.Baseline)
	if err != nil {
		return Blocked{}, err
	}

	classification := classifyBaseline(response.Status)
	m.baseline[point.ID] = baselineRecord{Point: point, Blocked: classification}
	return classification, nil
}

// Variants expands one payload into the full operator set.
func (m *WafBypassMutator) Variants(payload Payload) ([]PayloadVariant, error) {
	if payload.Raw == "" {
		return nil, ErrEmptyPayload
	}
	if strings.TrimSpace(payload.Point) == "" {
		return nil, ErrBadPoint
	}
	if m.probe == nil {
		return nil, ErrNilProbe
	}

	urlEncoded := urlEncode(payload.Raw)
	ops := []struct {
		name    string
		encoded string
	}{
		{OpURL, urlEncoded},
		{OpDouble, urlEncode(urlEncoded)},
		{OpUnicode, unicodeEscape(payload.Raw)},
		{OpSeparator, strings.ReplaceAll(payload.Raw, " ", "/**/")},
		{OpSplit, keywordSplit(payload.Raw)},
		{OpCase, swapCase(payload.Raw)},
	}

	variants := make([]PayloadVariant, 0, len(ops))
	for _, op := range ops {
		variants = append(variants, PayloadVariant{
			ID:      payload.ID + "|" + op.name,
			Base:    payload.ID,
			Op:      op.name,
			Encoded: op.encoded,
			Point:   payload.Point,
		})
	}
	return variants, nil
}

// Submit sends one variant and classifies the answer.
func (m *WafBypassMutator) Submit(variant PayloadVariant) (Outcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureMaps()

	if m.probe == nil {
		return Outcome{}, ErrNilProbe
	}
	if m.stopped {
		return Outcome{}, ErrStopped
	}
	if m.throttled {
		return Outcome{}, ErrThrottled
	}

	pointRecord, ok := m.baseline[variant.Point]
	if !ok {
		return Outcome{}, ErrNoBaseline
	}
	if _, ok := m.tried[variant.ID]; ok {
		return Outcome{}, ErrDuplicateVariant
	}

	m.tried[variant.ID] = struct{}{}
	m.sent++

	response, err := m.probe.Send(pointRecord.Point, variant.Encoded)
	if err != nil {
		return Outcome{}, err
	}

	passed := !isBlockedStatus(response.Status)
	outcome := Outcome{
		Variant: variant.ID,
		Status:  response.Status,
		Passed:  passed,
		Reached: passed,
		Effect:  response.Effect,
		Reason:  ReasonBlocked,
	}
	if passed && response.Effect {
		outcome.Effective = true
		outcome.Reason = ReasonBypass
		m.effective[variant.ID] = struct{}{}
		m.effectiveCounts[variant.ID]++
		m.effectiveTotal++
	}

	if !passed {
		m.blocked++
		m.consecutiveBlocks++
		if m.limits.ThrottleAfter > 0 && m.consecutiveBlocks >= m.limits.ThrottleAfter {
			m.throttled = true
		}
	} else {
		m.consecutiveBlocks = 0
	}

	return outcome, nil
}

// Rank orders variants so the most promising ones go first.
func (m *WafBypassMutator) Rank(variants []PayloadVariant) ([]PayloadVariant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ranked := make([]PayloadVariant, len(variants))
	copy(ranked, variants)

	sort.SliceStable(ranked, func(i, j int) bool {
		left, right := ranked[i], ranked[j]

		leftCount := m.effectiveCounts[left.ID]
		rightCount := m.effectiveCounts[right.ID]
		if leftCount != rightCount {
			return leftCount > rightCount
		}

		_, leftTried := m.tried[left.ID]
		_, rightTried := m.tried[right.ID]
		if leftTried != rightTried {
			return leftTried
		}

		leftOpRank := operatorRank(left.Op)
		rightOpRank := operatorRank(right.Op)
		if leftOpRank != rightOpRank {
			return leftOpRank < rightOpRank
		}
		if left.Op != right.Op {
			return left.Op < right.Op
		}
		return left.ID < right.ID
	})

	return ranked, nil
}

// Stop freezes the run with an operator supplied reason.
func (m *WafBypassMutator) Stop(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrBadReason
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return nil
	}
	m.stopped = true
	m.stopReason = reason
	return nil
}

// Throttled reports whether the run is slowed down by repeated blocks.
func (m *WafBypassMutator) Throttled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.throttled
}

// Tried lists submitted variant identifiers, sorted.
func (m *WafBypassMutator) Tried() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortedSet(m.tried)
}

// Effective lists the variants that produced an observable effect, sorted.
func (m *WafBypassMutator) Effective() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortedSet(m.effective)
}

// Stats returns the running ledger.
func (m *WafBypassMutator) Stats() (MutatorStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MutatorStats{
		Sent:              m.sent,
		Blocked:           m.blocked,
		Effective:         m.effectiveTotal,
		ConsecutiveBlocks: m.consecutiveBlocks,
		Throttled:         m.throttled,
		Stopped:           m.stopped,
		StopReason:        m.stopReason,
	}, nil
}

// Snapshot serialises baselines, the tried set and the throttle state.
func (m *WafBypassMutator) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot := mutatorSnapshot{
		Format:            snapshotFormat,
		Version:           snapshotVersion,
		Baselines:         make(map[string]baselineRecord, len(m.baseline)),
		Tried:             sortedSet(m.tried),
		EffectiveIDs:      sortedSet(m.effective),
		EffectiveCounts:   make(map[string]int, len(m.effectiveCounts)),
		EffectiveTotal:    m.effectiveTotal,
		Sent:              m.sent,
		Blocked:           m.blocked,
		ConsecutiveBlocks: m.consecutiveBlocks,
		Throttled:         m.throttled,
		Stopped:           m.stopped,
		StopReason:        m.stopReason,
	}
	for id, record := range m.baseline {
		snapshot.Baselines[id] = record
	}
	for id, count := range m.effectiveCounts {
		snapshot.EffectiveCounts[id] = count
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *WafBypassMutator) Restore(data []byte) error {
	snapshot, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.baseline = snapshot.baseline
	m.tried = snapshot.tried
	m.effective = snapshot.effective
	m.effectiveCounts = snapshot.effectiveCounts
	m.effectiveTotal = snapshot.effectiveTotal
	m.sent = snapshot.sent
	m.blocked = snapshot.blocked
	m.consecutiveBlocks = snapshot.consecutiveBlocks
	m.throttled = snapshot.throttled
	m.stopped = snapshot.stopped
	m.stopReason = snapshot.stopReason
	if !m.throttled && m.limits.ThrottleAfter > 0 &&
		m.consecutiveBlocks >= m.limits.ThrottleAfter {
		m.throttled = true
	}
	return nil
}

type restoredState struct {
	baseline          map[string]baselineRecord
	tried             map[string]struct{}
	effective         map[string]struct{}
	effectiveCounts   map[string]int
	effectiveTotal    int
	sent              int
	blocked           int
	consecutiveBlocks int
	throttled         bool
	stopped           bool
	stopReason        string
}

func decodeSnapshot(data []byte) (restoredState, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var raw mutatorSnapshot
	if err := decoder.Decode(&raw); err != nil {
		return restoredState{}, badSnapshot(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("trailing JSON value")
		}
		return restoredState{}, badSnapshot(err)
	}

	if raw.Format != snapshotFormat || raw.Version != snapshotVersion {
		return restoredState{}, badSnapshot(errors.New("unsupported snapshot format or version"))
	}
	if raw.Sent < 0 || raw.Blocked < 0 || raw.EffectiveTotal < 0 || raw.ConsecutiveBlocks < 0 {
		return restoredState{}, badSnapshot(errors.New("negative counter"))
	}
	if raw.EffectiveTotal != len(raw.EffectiveIDs) {
		return restoredState{}, badSnapshot(errors.New("effective total does not match the effective set"))
	}
	if raw.Sent < len(raw.Tried) || raw.Sent < raw.EffectiveTotal || raw.Blocked > raw.Sent {
		return restoredState{}, badSnapshot(errors.New("inconsistent counters"))
	}
	if raw.ConsecutiveBlocks > raw.Blocked {
		return restoredState{}, badSnapshot(errors.New("consecutive blocks exceed blocked count"))
	}
	if raw.Stopped && strings.TrimSpace(raw.StopReason) == "" {
		return restoredState{}, badSnapshot(errors.New("a stopped snapshot has no reason"))
	}

	baseline := make(map[string]baselineRecord, len(raw.Baselines))
	for id, record := range raw.Baselines {
		if id == "" || record.Point.ID != id || !validPoint(record.Point) {
			return restoredState{}, badSnapshot(errors.New("invalid baseline record"))
		}
		baseline[id] = record
	}

	tried := make(map[string]struct{}, len(raw.Tried))
	for _, id := range raw.Tried {
		if id == "" {
			return restoredState{}, badSnapshot(errors.New("empty tried variant id"))
		}
		if _, exists := tried[id]; exists {
			return restoredState{}, badSnapshot(errors.New("duplicate tried variant id"))
		}
		tried[id] = struct{}{}
	}

	effective := make(map[string]struct{}, len(raw.EffectiveIDs))
	for _, id := range raw.EffectiveIDs {
		if id == "" {
			return restoredState{}, badSnapshot(errors.New("empty effective variant id"))
		}
		if _, exists := tried[id]; !exists {
			return restoredState{}, badSnapshot(errors.New("effective variant was not tried"))
		}
		if _, exists := effective[id]; exists {
			return restoredState{}, badSnapshot(errors.New("duplicate effective variant id"))
		}
		count, exists := raw.EffectiveCounts[id]
		if !exists || count < 1 {
			return restoredState{}, badSnapshot(errors.New("effective variant has no history"))
		}
		effective[id] = struct{}{}
	}
	if len(raw.EffectiveCounts) != len(effective) {
		return restoredState{}, badSnapshot(errors.New("effective history has unknown variants"))
	}

	effectiveCounts := make(map[string]int, len(raw.EffectiveCounts))
	historyTotal := int64(0)
	for id, count := range raw.EffectiveCounts {
		if count < 1 || count > raw.Sent {
			return restoredState{}, badSnapshot(errors.New("effective history exceeds sent count"))
		}
		historyTotal += int64(count)
		effectiveCounts[id] = count
	}
	if historyTotal > int64(raw.Sent) {
		return restoredState{}, badSnapshot(errors.New("effective history exceeds sent count"))
	}

	return restoredState{
		baseline:          baseline,
		tried:             tried,
		effective:         effective,
		effectiveCounts:   effectiveCounts,
		effectiveTotal:    raw.EffectiveTotal,
		sent:              raw.Sent,
		blocked:           raw.Blocked,
		consecutiveBlocks: raw.ConsecutiveBlocks,
		throttled:         raw.Throttled,
		stopped:           raw.Stopped,
		stopReason:        raw.StopReason,
	}, nil
}

func badSnapshot(_ error) error {
	return ErrBadSnapshot
}

func (m *WafBypassMutator) ensureMaps() {
	if m.baseline == nil {
		m.baseline = make(map[string]baselineRecord)
	}
	if m.tried == nil {
		m.tried = make(map[string]struct{})
	}
	if m.effective == nil {
		m.effective = make(map[string]struct{})
	}
	if m.effectiveCounts == nil {
		m.effectiveCounts = make(map[string]int)
	}
}

func validPoint(point InjectionPoint) bool {
	return strings.TrimSpace(point.ID) != "" && strings.TrimSpace(point.Path) != ""
}

func classifyBaseline(status int) Blocked {
	switch {
	case status >= 200 && status < 300:
		return Blocked{Status: status, Blocked: false, Reason: ReasonAllowed}
	case isBlockedStatus(status):
		return Blocked{Status: status, Blocked: true, Reason: ReasonWAFRule}
	case status >= 500 && status < 600:
		return Blocked{Status: status, Blocked: true, Reason: ReasonIndeterminate}
	case status >= 400 && status < 500:
		return Blocked{Status: status, Blocked: false, Reason: ReasonClientError}
	default:
		return Blocked{Status: status, Blocked: true, Reason: ReasonIndeterminate}
	}
}

func isBlockedStatus(status int) bool {
	return status == 403 || status == 406 || status == 429
}

func urlEncode(payload string) string {
	const upperHex = "0123456789ABCDEF"
	var builder strings.Builder
	for i := 0; i < len(payload); i++ {
		b := payload[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') {
			builder.WriteByte(b)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(upperHex[b>>4])
		builder.WriteByte(upperHex[b&0x0f])
	}
	return builder.String()
}

func unicodeEscape(payload string) string {
	var builder strings.Builder
	for _, r := range payload {
		fmt.Fprintf(&builder, "\\u%04x", r)
	}
	return builder.String()
}

func keywordSplit(payload string) string {
	var builder strings.Builder
	runes := []rune(payload)
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			builder.WriteRune(runes[i])
			i++
			continue
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			i++
		}
		word := runes[start:i]
		if len(word) > 2 {
			builder.WriteRune(word[0])
			builder.WriteString("/**/")
			builder.WriteString(string(word[1:]))
		} else {
			builder.WriteString(string(word))
		}
	}
	return builder.String()
}

func swapCase(payload string) string {
	var builder strings.Builder
	letterIndex := 0
	for _, r := range payload {
		if !unicode.IsLetter(r) {
			builder.WriteRune(r)
			continue
		}
		if letterIndex%2 == 0 {
			builder.WriteRune(unicode.ToUpper(r))
		} else {
			builder.WriteRune(unicode.ToLower(r))
		}
		letterIndex++
	}
	return builder.String()
}

func operatorRank(op string) int {
	for i, candidate := range operatorOrder {
		if candidate == op {
			return i
		}
	}
	return len(operatorOrder)
}

func sortedSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
