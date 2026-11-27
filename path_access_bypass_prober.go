// Package task probes protected paths for normalisation mismatches between
// the front gateway and the backend router.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Variant dimensions produced by Variants, in fixed order.
const (
	DimSingleEncode  = "single_encode"
	DimDoubleEncode  = "double_encode"
	DimDotSegment    = "dot_segment"
	DimSemicolon     = "semicolon_param"
	DimCase          = "case_flip"
	DimTrailingSlash = "trailing_slash"
)

// Errors reported by the prober.
var (
	ErrBadPath         = errors.New("path is empty")
	ErrNoTransport     = errors.New("transport is not installed")
	ErrUnknownVariant  = errors.New("variant was not produced for this path")
	ErrVariantProbed   = errors.New("variant has already been probed")
	ErrBudgetExhausted = errors.New("request budget exhausted")
	ErrNoBaseline      = errors.New("baseline has not been captured")
	ErrNoObservations  = errors.New("no observation to judge")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Response is what the transport reports for one path.
type Response struct {
	Status  int               `json:"status"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}

// Transport fetches one path from the target.
type Transport interface {
	Fetch(path string) (Response, error)
}

// Clock supplies the probe timestamps.
type Clock interface {
	Now() int64
}

// Baseline is the fingerprint of the protected path fetched as is.
type Baseline struct {
	Path        string   `json:"path"`
	Status      int      `json:"status"`
	Length      int      `json:"length"`
	Markers     []string `json:"markers"`
	Fingerprint string   `json:"fingerprint"`
}

// PathVariant is one normalisation variant of the protected path.
type PathVariant struct {
	ID        string `json:"id"`
	Dimension string `json:"dimension"`
	Path      string `json:"path"`
}

// Observation is the outcome of probing one variant once.
type Observation struct {
	VariantID   string   `json:"variant_id"`
	Dimension   string   `json:"dimension"`
	Path        string   `json:"path"`
	Status      int      `json:"status"`
	Length      int      `json:"length"`
	Markers     []string `json:"markers"`
	Fingerprint string   `json:"fingerprint"`
	At          int64    `json:"at"`
}

// BypassVerdict is the judgement over a set of observations.
type BypassVerdict struct {
	Bypassed []string `json:"bypassed"`
	Rejected []string `json:"rejected"`
	Reason   string   `json:"reason"`
}

// BudgetState reports how many requests were spent.
type BudgetState struct {
	Used      int `json:"used"`
	Limit     int `json:"limit"`
	Remaining int `json:"remaining"`
}

// ProberConfig bounds the prober and names the protected markers.
type ProberConfig struct {
	MaxRequests      int
	ProtectedMarkers []string
}

// PathAccessBypassProber owns the baseline, the probe history and the budget.
type PathAccessBypassProber struct {
	cfg   ProberConfig
	clock Clock
	mu    sync.Mutex
}

// NewPathAccessBypassProber builds a prober with the given limits.
func NewPathAccessBypassProber(cfg ProberConfig) *PathAccessBypassProber {
	return &PathAccessBypassProber{cfg: cfg}
}

// SetTransport installs the transport used for every fetch.
func (p *PathAccessBypassProber) SetTransport(t Transport) error {
	return errNotImplemented
}

// SetClock installs the clock used for probe timestamps.
func (p *PathAccessBypassProber) SetClock(c Clock) error {
	return errNotImplemented
}

// Baseline captures the fingerprint of the protected path itself.
func (p *PathAccessBypassProber) Baseline(path string) (Baseline, error) {
	return Baseline{}, errNotImplemented
}

// Variants derives the normalisation variants for one path.
func (p *PathAccessBypassProber) Variants(path string) ([]PathVariant, error) {
	return nil, errNotImplemented
}

// Probe fetches one variant exactly once and records the outcome.
func (p *PathAccessBypassProber) Probe(variant PathVariant) (Observation, error) {
	return Observation{}, errNotImplemented
}

// Judge folds the observations into one bypass verdict.
func (p *PathAccessBypassProber) Judge(obs []Observation) (BypassVerdict, error) {
	return BypassVerdict{}, errNotImplemented
}

// Budget reports how much of the request budget is still available.
func (p *PathAccessBypassProber) Budget() (BudgetState, error) {
	return BudgetState{}, errNotImplemented
}

// Observations returns a copy of every recorded probe outcome.
func (p *PathAccessBypassProber) Observations() []Observation {
	return nil
}

// Snapshot serialises the baseline, the probe history and the budget.
func (p *PathAccessBypassProber) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole prober state, all or nothing.
func (p *PathAccessBypassProber) Restore(data []byte) error {
	return errNotImplemented
}
