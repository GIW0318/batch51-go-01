// Package task probes directory query entry points for filter injection.
package task

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Payload kinds produced by Build.
const (
	PayloadClosure  = "closure"
	PayloadWildcard = "wildcard"
	PayloadLogic    = "logic"
)

// Diff verdicts.
const (
	VerdictConfirmed    = "injection_confirmed"
	VerdictNotConfirmed = "not_confirmed"
	VerdictUndetermined = "undetermined"
	VerdictError        = "error"
)

// Errors reported by the prober.
var (
	ErrEmptyBase    = errors.New("base filter is empty")
	ErrNilResponder = errors.New("responder is nil")
	ErrUnknownKind  = errors.New("payload kind is not supported")
	ErrBadTarget    = errors.New("target identifier is empty")
	ErrQueueTimeout = errors.New("probe queue wait timed out")
)

const (
	probeQueueTimeout = time.Second
	defaultField      = "uid"
)

var nonceSequence atomic.Uint64

// Target is one query entry point under test.
type Target struct {
	ID     string `json:"id"`
	Base   string `json:"base"`
	Cap    int    `json:"cap"`
	Cached bool   `json:"cached"`
}

// Payload is one probe filter.
type Payload struct {
	Kind   string `json:"kind"`
	Filter string `json:"filter"`
	Nonce  string `json:"nonce"`
}

// ProbeResult is what one probe returned.
type ProbeResult struct {
	Target     string   `json:"target"`
	Payload    Payload  `json:"payload"`
	Count      int      `json:"count"`
	Attributes []string `json:"attributes"`
	DurationMs int64    `json:"duration_ms"`
	Truncated  bool     `json:"truncated"`
	Cached     bool     `json:"cached"`
	Error      string   `json:"error"`
}

// Diff compares a baseline probe with an injected one.
type Diff struct {
	Kind     string `json:"kind"`
	Baseline int    `json:"baseline"`
	Injected int    `json:"injected"`
	Verdict  string `json:"verdict"`
	Capped   bool   `json:"capped"`
	Note     string `json:"note"`
}

// Responder answers one filter against the directory.
type Responder interface {
	Query(filter string) (count int, attributes []string, err error)
}

// LdapFilterInjectionProber serialises probes per entry point.
type LdapFilterInjectionProber struct {
	maxConcurrent int
	gate          chan struct{}
	mu            sync.Mutex
	entryLocks    map[string]chan struct{}
}

// NewLdapFilterInjectionProber builds a prober with a fixed concurrency cap.
func NewLdapFilterInjectionProber(maxConcurrent int) *LdapFilterInjectionProber {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &LdapFilterInjectionProber{
		maxConcurrent: maxConcurrent,
		gate:          make(chan struct{}, maxConcurrent),
		entryLocks:    map[string]chan struct{}{},
	}
}

// Build produces the closure, wildcard and logic payloads for one entry point.
func (p *LdapFilterInjectionProber) Build(base string) ([]Payload, error) {
	if strings.TrimSpace(base) == "" {
		return nil, ErrEmptyBase
	}

	nonce := p.Nonce()
	field := inferField(base)
	marker := fmt.Sprintf("(%s=%s", field, nonce)
	return []Payload{
		{Kind: PayloadClosure, Filter: base + marker + p.WrapClosure(field), Nonce: nonce},
		{Kind: PayloadWildcard, Filter: base + marker + p.WrapWildcard(field), Nonce: nonce},
		{Kind: PayloadLogic, Filter: base + marker + p.WrapLogic(field), Nonce: nonce},
	}, nil
}

// Submit runs one payload against one entry point.
func (p *LdapFilterInjectionProber) Submit(t Target, r Responder, payload Payload) (ProbeResult, error) {
	if strings.TrimSpace(t.ID) == "" {
		return ProbeResult{}, ErrBadTarget
	}
	if isNilResponder(r) {
		return ProbeResult{}, ErrNilResponder
	}
	if !knownKind(payload.Kind) {
		return ProbeResult{}, ErrUnknownKind
	}

	if t.Cached {
		payload = p.cacheSafePayload(t, payload)
	}

	entryKey := strings.TrimSpace(t.ID)
	deadline := time.Now().Add(probeQueueTimeout)
	entry := p.entryGate(entryKey)
	if !acquire(entry, deadline) {
		return failedSubmit(t, payload, queueTimeoutError(t.ID)), queueTimeoutError(t.ID)
	}
	defer release(entry)

	if !acquire(p.gate, deadline) {
		err := queueTimeoutError(t.ID)
		return failedSubmit(t, payload, err), err
	}
	defer release(p.gate)

	started := time.Now()
	count, attributes, err := r.Query(payload.Filter)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		result := failedSubmit(t, payload, err)
		result.DurationMs = duration
		return result, fmt.Errorf("query target %q with %s payload: %w", t.ID, payload.Kind, err)
	}

	result := ProbeResult{
		Target:     t.ID,
		Payload:    payload,
		Count:      count,
		Attributes: p.SortEntries(attributes),
		DurationMs: duration,
		Truncated:  t.Cap > 0 && count >= t.Cap,
		Cached:     t.Cached,
	}
	return result, nil
}

// Diff grades a baseline result against an injected one.
func (p *LdapFilterInjectionProber) Diff(baseline, injected ProbeResult) Diff {
	kind := injected.Payload.Kind
	if kind == "" {
		kind = baseline.Payload.Kind
	}
	diff := Diff{
		Kind:     kind,
		Baseline: baseline.Count,
		Injected: injected.Count,
		Capped:   baseline.Truncated || injected.Truncated,
	}

	if baseline.Error != "" || injected.Error != "" {
		diff.Verdict = VerdictError
		diff.Note = probeErrorNote(baseline.Error, injected.Error)
		return diff
	}

	baselineAttrs := p.SortEntries(baseline.Attributes)
	injectedAttrs := p.SortEntries(injected.Attributes)
	attributesDiffer := !equalStrings(baselineAttrs, injectedAttrs)

	switch {
	case baseline.Count != injected.Count:
		diff.Verdict = VerdictConfirmed
		if diff.Capped {
			diff.Note = "count difference is bounded by the server result cap and is not a complete differential"
		}
	case attributesDiffer:
		diff.Verdict = VerdictConfirmed
		if diff.Capped {
			diff.Note = "attribute difference was observed, but result pages are capped"
		}
	case baseline.Cached || injected.Cached:
		diff.Verdict = VerdictUndetermined
		diff.Note = "identical observations came from a cached entry point and cannot support a not-confirmed verdict"
	case baseline.Payload.Nonce != "" && baseline.Payload.Nonce == injected.Payload.Nonce:
		diff.Verdict = VerdictUndetermined
		diff.Note = "identical observations share the same probe prefix, so a cached result cannot support a not-confirmed verdict"
	case diff.Capped:
		diff.Verdict = VerdictUndetermined
		diff.Note = "both observations hit the server result cap, so identical pages do not rule out injection"
	default:
		diff.Verdict = VerdictNotConfirmed
	}
	return diff
}

// Nonce returns a fresh probe prefix.
func (p *LdapFilterInjectionProber) Nonce() string {
	sequence := nonceSequence.Add(1)
	return fmt.Sprintf("probe%x%x", time.Now().UnixNano(), sequence)
}

// WrapClosure builds the bracket closing fragment.
func (p *LdapFilterInjectionProber) WrapClosure(field string) string {
	field = normalizeField(field)
	return fmt.Sprintf("))(%s=*", field)
}

// WrapWildcard builds the wildcard fragment.
func (p *LdapFilterInjectionProber) WrapWildcard(field string) string {
	field = normalizeField(field)
	return fmt.Sprintf(")(%s=*)", field)
}

// WrapLogic builds the boolean combination fragment.
func (p *LdapFilterInjectionProber) WrapLogic(field string) string {
	field = normalizeField(field)
	return fmt.Sprintf(")(|(%s=*)(&(%s=*)(%s=*)))", field, field, field)
}

// SortEntries orders attribute lines and drops duplicates.
func (p *LdapFilterInjectionProber) SortEntries(entries []string) []string {
	if len(entries) == 0 {
		return []string{}
	}

	seen := make(map[string]struct{}, len(entries))
	unique := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		unique = append(unique, entry)
	}
	sort.Strings(unique)
	return unique
}

// ClassifyCounts grades a count delta and reports whether the cap was hit.
func (p *LdapFilterInjectionProber) ClassifyCounts(baseline, injected, limit int) (string, bool) {
	capped := limit > 0 && (baseline >= limit || injected >= limit)
	if baseline != injected {
		return VerdictConfirmed, capped
	}
	if capped {
		return VerdictUndetermined, true
	}
	return VerdictNotConfirmed, false
}

func (p *LdapFilterInjectionProber) entryGate(id string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	gate, ok := p.entryLocks[id]
	if !ok {
		gate = make(chan struct{}, 1)
		p.entryLocks[id] = gate
	}
	return gate
}

func (p *LdapFilterInjectionProber) cacheSafePayload(t Target, payload Payload) Payload {
	nonce := p.Nonce()
	field := inferField(t.Base)
	if payload.Nonce != "" && strings.Contains(payload.Filter, payload.Nonce) {
		payload.Filter = strings.ReplaceAll(payload.Filter, payload.Nonce, nonce)
	} else if strings.TrimSpace(payload.Filter) == "" {
		payload.Filter = fmt.Sprintf("(%s=%s*)", field, nonce)
	} else {
		filter := strings.TrimSpace(payload.Filter)
		if !strings.HasPrefix(filter, "(") {
			filter = "(" + filter + ")"
		}
		payload.Filter = fmt.Sprintf("(&%s(|(%s=%s*)(!(%s=%s*))))", filter, field, nonce, field, nonce)
	}
	payload.Nonce = nonce
	return payload
}

func acquire(gate chan struct{}, deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case gate <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

func release(gate chan struct{}) {
	<-gate
}

func failedSubmit(t Target, payload Payload, err error) ProbeResult {
	return ProbeResult{
		Target:  t.ID,
		Payload: payload,
		Error:   err.Error(),
	}
}

func queueTimeoutError(target string) error {
	return fmt.Errorf("%w for target %q: %w", ErrQueueTimeout, target, context.DeadlineExceeded)
}

func knownKind(kind string) bool {
	switch kind {
	case PayloadClosure, PayloadWildcard, PayloadLogic:
		return true
	default:
		return false
	}
}

func isNilResponder(r Responder) bool {
	if r == nil {
		return true
	}
	value := reflect.ValueOf(r)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func normalizeField(field string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		return defaultField
	}
	return field
}

func inferField(base string) string {
	index := strings.Index(base, "{input}")
	if index < 0 {
		return defaultField
	}
	prefix := base[:index]
	equals := strings.LastIndex(prefix, "=")
	if equals < 0 {
		return defaultField
	}
	start := equals - 1
	for start >= 0 && !strings.ContainsRune("(|&! ", rune(prefix[start])) {
		start--
	}
	field := strings.TrimSpace(prefix[start+1 : equals])
	if field == "" {
		return defaultField
	}
	return field
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func probeErrorNote(baselineErr, injectedErr string) string {
	switch {
	case baselineErr != "" && injectedErr != "":
		return fmt.Sprintf("baseline probe failed: %s; injected probe failed: %s", baselineErr, injectedErr)
	case baselineErr != "":
		return "baseline probe failed: " + baselineErr
	default:
		return "injected probe failed: " + injectedErr
	}
}
