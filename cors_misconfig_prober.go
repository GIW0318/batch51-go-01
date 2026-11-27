// Package task builds cross origin probe requests, submits the paired
// preflight and actual requests to a registered endpoint and grades the
// origin handling of its policy.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Origin variants used when building a request.
const (
	OriginExact     = "exact"
	OriginSuffix    = "suffix"
	OriginNull      = "null"
	OriginArbitrary = "arbitrary"
)

// Policy classes reported by Judge.
const (
	PolicyExact      = "exact_allowlist"
	PolicySuffix     = "suffix_match"
	PolicyReflect    = "reflect_all"
	PolicyWildcard   = "wildcard"
	PolicyNullAllowed = "null_allowed"
	PolicyDenyAll    = "deny_all"
)

// Reasons attached to one probe.
const (
	ReasonOriginAllowed = "origin_allowed"
	ReasonOriginDenied  = "origin_denied"
)

// NullOrigin is the literal origin sent by sandboxed documents.
const NullOrigin = "null"

// Errors returned by the prober.
var (
	ErrBadEndpoint       = errors.New("endpoint name is required")
	ErrDuplicateEndpoint = errors.New("endpoint is already registered")
	ErrUnknownEndpoint   = errors.New("endpoint is not registered")
	ErrBadOrigin         = errors.New("origin is required")
	ErrBadKind           = errors.New("origin kind is not supported")
	ErrBadPolicy         = errors.New("policy does not define any origin source")
	ErrProbeBudget       = errors.New("probe budget exhausted for endpoint")
	ErrBadLimit          = errors.New("prober limits must be positive")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// ProberConfig bounds one prober instance.
type ProberConfig struct {
	MaxConcurrent        int
	MaxProbesPerEndpoint int
}

// CorsPolicy is the origin handling profile of one endpoint.
type CorsPolicy struct {
	Name             string
	AllowedOrigins   []string
	Suffix           string
	ReflectAll       bool
	AllowNull        bool
	AllowCredentials bool
	PreflightAllowed bool
	ActualAllowed    bool
	ErrorBranchOnly  bool
}

// Request is one cross origin request.
type Request struct {
	Origin          string
	Kind            string
	Method          string
	Preflight       bool
	WithCredentials bool
	Marker          string
	Headers         map[string]string
}

// Response is what an endpoint answers for one request.
type Response struct {
	Status           int
	AllowOrigin      string
	AllowCredentials bool
	AllowMethods     string
	VaryOrigin       bool
	Marker           string
}

// Verdict is the graded policy class of one endpoint.
type Verdict struct {
	Endpoint    string
	Class       string
	Reflected   bool
	Credentials bool
	Mismatch    int
	Seq         int64
}

// ProbeRecord is one submitted request kept for replay.
type ProbeRecord struct {
	Seq         int64
	Endpoint    string
	Kind        string
	Preflight   bool
	Origin      string
	StatusCode  int
	AllowOrigin string
	Marker      string
}

// ProberStats counts the traffic of one endpoint.
type ProberStats struct {
	Probes       int64
	Allowed      int64
	Denied       int64
	PeakInFlight int
}

// CorsMisconfigProber owns policies, records and counters.
type CorsMisconfigProber struct {
	cfg ProberConfig
}

// NewCorsMisconfigProber builds a prober with the given caps.
func NewCorsMisconfigProber(cfg ProberConfig) *CorsMisconfigProber {
	return &CorsMisconfigProber{cfg: cfg}
}

// AddEndpoint registers one policy profile.
func (p *CorsMisconfigProber) AddEndpoint(policy CorsPolicy) error {
	return errNotImplemented
}

// Endpoints lists the registered endpoint names in registration order.
func (p *CorsMisconfigProber) Endpoints() []string {
	return nil
}

// Build assembles one request for the requested origin variant.
func (p *CorsMisconfigProber) Build(kind, origin string, withCredentials, preflight bool) (Request, error) {
	return Request{}, errNotImplemented
}

// Probe submits one request and records the origin decision.
func (p *CorsMisconfigProber) Probe(endpoint string, req Request) (Response, error) {
	return Response{}, errNotImplemented
}

// Judge runs the fixed origin matrix and grades the policy class.
func (p *CorsMisconfigProber) Judge(endpoint string) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Records lists the submitted requests of one endpoint, oldest first.
func (p *CorsMisconfigProber) Records(endpoint string) []ProbeRecord {
	return nil
}

// Stats reads the counters of one endpoint.
func (p *CorsMisconfigProber) Stats(endpoint string) ProberStats {
	return ProberStats{}
}

// GlobalStats reads the counters shared by every endpoint.
func (p *CorsMisconfigProber) GlobalStats() ProberStats {
	return ProberStats{}
}

// RecycleMarkers releases the probe markers of one endpoint and counts them.
func (p *CorsMisconfigProber) RecycleMarkers(endpoint string) int {
	return 0
}

// Reset drops the records of one endpoint but keeps the policy.
func (p *CorsMisconfigProber) Reset(endpoint string) error {
	return errNotImplemented
}

// Dump serialises policies, records and counters.
func (p *CorsMisconfigProber) Dump() ([]byte, error) {
	return nil, errNotImplemented
}

// Load replaces the whole state with a snapshot, all or nothing.
func (p *CorsMisconfigProber) Load(data []byte) error {
	return errNotImplemented
}
