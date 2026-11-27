// Package task discovers API version surfaces and grades whether an older
// version of a resource is weaker than the version the service advertises.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Version carriers.
const (
	CarrierPath   = "path"
	CarrierHeader = "header"
	CarrierQuery  = "query"
)

// Response buckets written to Observation.Class.
const (
	StatusOK       = "ok"
	StatusAuthGate = "auth_gate"
	StatusNotFound = "not_found"
	StatusServer   = "server_error"
)

// Verdict kinds returned by Judge.
const (
	VerdictDowngraded   = "downgraded"
	VerdictConsistent   = "consistent"
	VerdictUndetermined = "undetermined"
)

// Reason codes attached to a downgrade verdict.
const (
	ReasonAuthGap          = "reason_auth_gap"
	ReasonAuthParity       = "reason_auth_parity"
	ReasonFieldCut         = "reason_field_cut"
	ReasonRouteUnconfirmed = "reason_route_unconfirmed"
	ReasonServerError      = "reason_server_error"
	ReasonStable           = "reason_stable"
)

// Errors reported by the prober.
var (
	ErrEmptyBase      = errors.New("base address is empty")
	ErrNoResources    = errors.New("schema declares no resources")
	ErrNoVersions     = errors.New("schema declares no version tokens")
	ErrBadSchema      = errors.New("schema default version is not declared")
	ErrNilTransport   = errors.New("transport is nil")
	ErrBadSurface     = errors.New("version surface is incomplete")
	ErrUnknownCarrier = errors.New("version carrier is not supported")
	ErrProbeBudget    = errors.New("probe budget is exhausted")
	ErrCircuitOpen    = errors.New("circuit breaker is open")
	ErrRateLimited    = errors.New("prober is rate limited")
	ErrNoObservations = errors.New("no observations to align")
	ErrNoBaseline     = errors.New("baseline observation is missing")
	ErrBadDiff        = errors.New("version diff is incomplete")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Schema is the declared version layout of one service.
type Schema struct {
	Versions       []string `json:"versions"`
	Resources      []string `json:"resources"`
	DefaultVersion string   `json:"default_version"`
	HeaderName     string   `json:"header_name"`
	QueryName      string   `json:"query_name"`
}

// VersionSurface is one addressable entry point of one version of a resource.
type VersionSurface struct {
	ID          string `json:"id"`
	Resource    string `json:"resource"`
	Version     string `json:"version"`
	Carrier     string `json:"carrier"`
	Path        string `json:"path"`
	HeaderName  string `json:"header_name"`
	HeaderValue string `json:"header_value"`
	QueryName   string `json:"query_name"`
	QueryValue  string `json:"query_value"`
	Default     bool   `json:"default"`
}

// Request is the caller shaped request that is replayed against a surface.
type Request struct {
	Method string `json:"method"`
	Auth   bool   `json:"auth"`
	Body   string `json:"body"`
}

// Response is the raw answer of one exchange.
type Response struct {
	Status    int      `json:"status"`
	Fields    []string `json:"fields"`
	ErrorCode string   `json:"error_code"`
}

// Transport performs one exchange against one version surface.
type Transport interface {
	Do(surface VersionSurface, req Request) (Response, error)
}

// Observation is the bucketed record of one effective probe.
type Observation struct {
	SurfaceID string   `json:"surface_id"`
	Resource  string   `json:"resource"`
	Version   string   `json:"version"`
	Carrier   string   `json:"carrier"`
	Status    int      `json:"status"`
	Class     string   `json:"class"`
	Fields    []string `json:"fields"`
	ErrorCode string   `json:"error_code"`
	Cached    bool     `json:"cached"`
	Default   bool     `json:"default"`
}

// VersionDiff is the field level difference between a baseline and a candidate.
type VersionDiff struct {
	Resource        string   `json:"resource"`
	Baseline        string   `json:"baseline"`
	Candidate       string   `json:"candidate"`
	MissingFields   []string `json:"missing_fields"`
	AddedFields     []string `json:"added_fields"`
	PatternMismatch bool     `json:"pattern_mismatch"`
	BaselineClass   string   `json:"baseline_class"`
	CandidateClass  string   `json:"candidate_class"`
}

// DowngradeVerdict is the explainable result of one difference.
type DowngradeVerdict struct {
	Resource   string `json:"resource"`
	Candidate  string `json:"candidate"`
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason"`
	Conclusive bool   `json:"conclusive"`
	AuthGap    bool   `json:"auth_gap"`
}

// BudgetState reports how much probing room is left.
type BudgetState struct {
	Probes      int  `json:"probes"`
	MaxProbes   int  `json:"max_probes"`
	Remaining   int  `json:"remaining"`
	RateLimited bool `json:"rate_limited"`
	CircuitOpen bool `json:"circuit_open"`
}

// Limits bounds probing.
type Limits struct {
	MaxProbes                  int
	MaxConsecutiveServerErrors int
}

// ApiVersionDowngradeProber owns the surface table, the probe ledger and the
// breaker state.
type ApiVersionDowngradeProber struct {
	mu sync.Mutex
}

// NewApiVersionDowngradeProber builds a prober on the given transport.
func NewApiVersionDowngradeProber(tr Transport, limits Limits) *ApiVersionDowngradeProber {
	_ = tr
	_ = limits
	return &ApiVersionDowngradeProber{}
}

// Discover expands the schema into resource/version/carrier surfaces.
func (p *ApiVersionDowngradeProber) Discover(base string, spec Schema) ([]VersionSurface, error) {
	return nil, errNotImplemented
}

// Probe performs at most one effective exchange per version surface.
func (p *ApiVersionDowngradeProber) Probe(surface VersionSurface, req Request) (Observation, error) {
	return Observation{}, errNotImplemented
}

// Align groups observations by resource and diffs each candidate with the baseline.
func (p *ApiVersionDowngradeProber) Align(obs []Observation) ([]VersionDiff, error) {
	return nil, errNotImplemented
}

// Judge grades one difference as a downgrade, consistent or undetermined.
func (p *ApiVersionDowngradeProber) Judge(diff VersionDiff) (DowngradeVerdict, error) {
	return DowngradeVerdict{}, errNotImplemented
}

// Budget reports the probe budget and the breaker state.
func (p *ApiVersionDowngradeProber) Budget() (BudgetState, error) {
	return BudgetState{}, errNotImplemented
}

// SetRateLimited turns the caller driven rate limit on or off.
func (p *ApiVersionDowngradeProber) SetRateLimited(limited bool) {
}

// Surfaces returns the discovered surfaces ordered by identifier.
func (p *ApiVersionDowngradeProber) Surfaces() []VersionSurface {
	return nil
}

// Observation reads one ledger entry.
func (p *ApiVersionDowngradeProber) Observation(surfaceID, resource string) (Observation, bool) {
	return Observation{}, false
}

// Snapshot serialises surfaces, the probe ledger, budget and breaker state.
func (p *ApiVersionDowngradeProber) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *ApiVersionDowngradeProber) Restore(data []byte) error {
	return errNotImplemented
}
