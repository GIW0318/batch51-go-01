// Package task plans equivalent payload variants for a confirmed injection
// point and decides which of them really got past the edge device.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Mutation operators, in the fixed ranking order.
const (
	OpURL       = "enc_url"
	OpDouble    = "enc_double"
	OpUnicode   = "enc_unicode"
	OpSeparator = "sep_equiv"
	OpSplit     = "keyword_split"
	OpCase      = "case_swap"
)

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

// WafBypassMutator owns the baseline table, the tried set and the throttle.
type WafBypassMutator struct {
	mu sync.Mutex
}

// NewWafBypassMutator builds a mutator over the given probe.
func NewWafBypassMutator(probe Probe, limits Limits) *WafBypassMutator {
	_ = probe
	_ = limits
	return &WafBypassMutator{}
}

// Baseline measures whether the untouched payload is blocked.
func (m *WafBypassMutator) Baseline(point InjectionPoint) (Blocked, error) {
	return Blocked{}, errNotImplemented
}

// Variants expands one payload into the full operator set.
func (m *WafBypassMutator) Variants(payload Payload) ([]PayloadVariant, error) {
	return nil, errNotImplemented
}

// Submit sends one variant and classifies the answer.
func (m *WafBypassMutator) Submit(variant PayloadVariant) (Outcome, error) {
	return Outcome{}, errNotImplemented
}

// Rank orders variants so the most promising ones go first.
func (m *WafBypassMutator) Rank(variants []PayloadVariant) ([]PayloadVariant, error) {
	return nil, errNotImplemented
}

// Stop freezes the run with an operator supplied reason.
func (m *WafBypassMutator) Stop(reason string) error {
	return errNotImplemented
}

// Throttled reports whether the run is slowed down by repeated blocks.
func (m *WafBypassMutator) Throttled() bool {
	return false
}

// Tried lists submitted variant identifiers, sorted.
func (m *WafBypassMutator) Tried() []string {
	return nil
}

// Effective lists the variants that produced an observable effect, sorted.
func (m *WafBypassMutator) Effective() []string {
	return nil
}

// Stats returns the running ledger.
func (m *WafBypassMutator) Stats() (MutatorStats, error) {
	return MutatorStats{}, errNotImplemented
}

// Snapshot serialises baselines, the tried set and the throttle state.
func (m *WafBypassMutator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *WafBypassMutator) Restore(data []byte) error {
	return errNotImplemented
}
