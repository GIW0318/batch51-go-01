// Package task probes an API endpoint for rate limit key assumptions and
// for dimension switches that escape the bucket.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Variant dimensions produced by Variants, in fixed order.
const (
	DimSourceHeader   = "source_header"
	DimIdentity       = "identity"
	DimPathCase       = "path_case"
	DimTrailingSlash  = "trailing_slash"
	DimMethodOverride = "method_override"
	DimQueryOrder     = "query_order"
)

// Key structures inferred by Judge.
const (
	StructureShared      = "shared"
	StructureIndependent = "independent"
	StructureGlobal      = "global"
)

// Errors reported by the prober.
var (
	ErrBadEndpoint     = errors.New("endpoint name and path are required")
	ErrBadBurst        = errors.New("burst size must be positive")
	ErrNoTransport     = errors.New("transport is not installed")
	ErrNoLimitObserved = errors.New("no throttling observed during the burst")
	ErrUnknownVariant  = errors.New("variant was not produced for this profile")
	ErrVariantProbed   = errors.New("variant has already been probed")
	ErrBudgetExhausted = errors.New("request budget exhausted")
	ErrBackoffActive   = errors.New("retry-after backoff is still active")
	ErrNoObservations  = errors.New("no observation to judge")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Endpoint is one API entry point under test.
type Endpoint struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Request is one probe request handed to the transport.
type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

// Response is what the transport reports back.
type Response struct {
	Status  int               `json:"status"`
	Length  int               `json:"length"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Transport performs one request against the target.
type Transport interface {
	Do(req Request) (Response, error)
}

// Clock supplies the probe timestamps.
type Clock interface {
	Now() int64
}

// LimitProfile is what Baseline learned about the endpoint.
type LimitProfile struct {
	Endpoint string `json:"endpoint"`
	Path     string `json:"path"`
	Limit    int    `json:"limit"`
	Probes   int    `json:"probes"`
	Blocked  bool   `json:"blocked"`
}

// BurstResult summarises one burst of requests.
type BurstResult struct {
	Requests   int `json:"requests"`
	Allowed    int `json:"allowed"`
	Blocked    int `json:"blocked"`
	LastStatus int `json:"last_status"`
}

// BypassVariant is one mutated request that may escape the bucket.
type BypassVariant struct {
	ID        string            `json:"id"`
	Dimension string            `json:"dimension"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers"`
}

// Observation is the outcome of probing one variant once.
type Observation struct {
	VariantID string `json:"variant_id"`
	Dimension string `json:"dimension"`
	Status    int    `json:"status"`
	Allowed   bool   `json:"allowed"`
	At        int64  `json:"at"`
}

// BypassVerdict is the judgement over a set of observations.
type BypassVerdict struct {
	Structure string   `json:"structure"`
	Bypassed  []string `json:"bypassed"`
	Blocked   []string `json:"blocked"`
	Reason    string   `json:"reason"`
}

// BudgetState reports how many probe requests were spent.
type BudgetState struct {
	Used      int `json:"used"`
	Limit     int `json:"limit"`
	Remaining int `json:"remaining"`
}

// ProberConfig bounds the prober.
type ProberConfig struct {
	BaselineBurst int
	MaxRequests   int
	BackoffMs     int64
}

// ApiRateLimitBypassProber owns the transport, the budget and the probes.
type ApiRateLimitBypassProber struct {
	cfg   ProberConfig
	clock Clock
	mu    sync.Mutex
}

// NewApiRateLimitBypassProber builds a prober with the given limits.
func NewApiRateLimitBypassProber(cfg ProberConfig) *ApiRateLimitBypassProber {
	return &ApiRateLimitBypassProber{cfg: cfg}
}

// SetTransport installs the transport used for every request.
func (p *ApiRateLimitBypassProber) SetTransport(t Transport) error {
	return errNotImplemented
}

// SetClock installs the clock used for probe timestamps.
func (p *ApiRateLimitBypassProber) SetClock(c Clock) error {
	return errNotImplemented
}

// Baseline measures how many requests pass before the first 429.
func (p *ApiRateLimitBypassProber) Baseline(endpoint Endpoint) (LimitProfile, error) {
	return LimitProfile{}, errNotImplemented
}

// Burst sends up to n requests and stops at the first rejection.
func (p *ApiRateLimitBypassProber) Burst(endpoint Endpoint, n int) (BurstResult, error) {
	return BurstResult{}, errNotImplemented
}

// Variants derives the bypass variants for one profile.
func (p *ApiRateLimitBypassProber) Variants(profile LimitProfile) ([]BypassVariant, error) {
	return nil, errNotImplemented
}

// Probe sends one variant exactly once and records the outcome.
func (p *ApiRateLimitBypassProber) Probe(variant BypassVariant) (Observation, error) {
	return Observation{}, errNotImplemented
}

// Judge folds the observations into one key structure verdict.
func (p *ApiRateLimitBypassProber) Judge(obs []Observation) (BypassVerdict, error) {
	return BypassVerdict{}, errNotImplemented
}

// Budget reports how much of the request budget is still available.
func (p *ApiRateLimitBypassProber) Budget() (BudgetState, error) {
	return BudgetState{}, errNotImplemented
}

// Observations returns a copy of every recorded probe outcome.
func (p *ApiRateLimitBypassProber) Observations() []Observation {
	return nil
}

// Snapshot serialises the profile, the probed variants and the budget.
func (p *ApiRateLimitBypassProber) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole prober state, all or nothing.
func (p *ApiRateLimitBypassProber) Restore(data []byte) error {
	return errNotImplemented
}
