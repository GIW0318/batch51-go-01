// Package task builds host header probes for the three request sources,
// submits them to a registered entrypoint and grades which layer took the
// injected value.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Request sources that can carry the host value.
const (
	SourceHost        = "host"
	SourceForwarded   = "x_forwarded_host"
	SourceRequestLine = "request_line"
)

// Layers that can read the host value.
const (
	LayerFrontend = "frontend"
	LayerBackend  = "backend"
	LayerCache    = "cache"
	LayerTemplate = "template"
)

// Reasons attached to a verdict.
const (
	ReasonHostUsed     = "host_used"
	ReasonHostIgnored  = "host_ignored"
	ReasonLayeredSplit = "layered_split"
)

// DefaultHost is what a layer falls back to when it does not read the source.
const DefaultHost = "app.internal"

// MarkerPrefix prefixes every recyclable probe marker.
const MarkerPrefix = "h-"

// Errors returned by the prober.
var (
	ErrBadEndpoint       = errors.New("entrypoint name is required")
	ErrDuplicateEndpoint = errors.New("entrypoint is already registered")
	ErrUnknownEndpoint   = errors.New("entrypoint is not registered")
	ErrBadHost           = errors.New("host value is required")
	ErrBadSource         = errors.New("request source is not supported")
	ErrBadPolicy         = errors.New("policy does not define any layer source")
	ErrProbeBudget       = errors.New("probe budget exhausted for entrypoint")
	ErrBadLimit          = errors.New("prober limits must be positive")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// ProberConfig bounds one prober instance.
type ProberConfig struct {
	MaxConcurrent        int
	MaxProbesPerEndpoint int
}

// HostPolicy describes which source each layer of one entrypoint reads.
type HostPolicy struct {
	Name           string
	FrontSource    string
	BackSource     string
	CacheSource    string
	TemplateSource string
	RedirectPath   string
	LinkPath       string
	EscapeLinks    bool
}

// Request is one probe request with the injected source populated.
type Request struct {
	Source      string
	Host        string
	Marker      string
	HostHeader  string
	Forwarded   string
	RequestLine string
}

// Observation is what one entrypoint produced for one request.
type Observation struct {
	Status     int
	Location   string
	Link       string
	CacheKey   string
	UsedSource string
	Layer      string
	Accepted   bool
	Marker     string
}

// Verdict is the graded value chain of one entrypoint.
type Verdict struct {
	Endpoint        string
	EffectiveSource string
	Layer           string
	Poisoned        bool
	Layered         bool
	Seq             int64
}

// SourceResult is the per source outcome of one judge round.
type SourceResult struct {
	Source   string
	Accepted bool
	Layer    string
	Value    string
}

// ProbeRecord is one submitted request kept for replay.
type ProbeRecord struct {
	Seq      int64
	Endpoint string
	Source   string
	Layer    string
	Value    string
	Marker   string
}

// ProberStats counts the traffic of one entrypoint.
type ProberStats struct {
	Probes       int64
	Accepted     int64
	Rejected     int64
	PeakInFlight int
}

// HostHeaderPoisonProber owns policies, records and counters.
type HostHeaderPoisonProber struct {
	cfg ProberConfig
}

// NewHostHeaderPoisonProber builds a prober with the given caps.
func NewHostHeaderPoisonProber(cfg ProberConfig) *HostHeaderPoisonProber {
	return &HostHeaderPoisonProber{cfg: cfg}
}

// AddEntrypoint registers one policy profile.
func (p *HostHeaderPoisonProber) AddEntrypoint(policy HostPolicy) error {
	return errNotImplemented
}

// Entrypoints lists the registered entrypoint names in registration order.
func (p *HostHeaderPoisonProber) Entrypoints() []string {
	return nil
}

// Build assembles one request for the requested source.
func (p *HostHeaderPoisonProber) Build(source, host string) (Request, error) {
	return Request{}, errNotImplemented
}

// Probe submits one request and records the produced links and cache key.
func (p *HostHeaderPoisonProber) Probe(endpoint string, req Request) (Observation, error) {
	return Observation{}, errNotImplemented
}

// Judge runs the three sources in a fixed order and grades the value chain.
func (p *HostHeaderPoisonProber) Judge(endpoint string) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Sources returns the per source results of the last judge round.
func (p *HostHeaderPoisonProber) Sources(endpoint string) []SourceResult {
	return nil
}

// Records lists the submitted requests of one entrypoint, oldest first.
func (p *HostHeaderPoisonProber) Records(endpoint string) []ProbeRecord {
	return nil
}

// Stats reads the counters of one entrypoint.
func (p *HostHeaderPoisonProber) Stats(endpoint string) ProberStats {
	return ProberStats{}
}

// RecycleMarkers releases the probe markers of one entrypoint and counts them.
func (p *HostHeaderPoisonProber) RecycleMarkers(endpoint string) int {
	return 0
}

// Reset drops the records of one entrypoint but keeps the policy.
func (p *HostHeaderPoisonProber) Reset(endpoint string) error {
	return errNotImplemented
}

// MailPreview renders the notification template with the injected host.
func (p *HostHeaderPoisonProber) MailPreview(endpoint, host string) (Observation, error) {
	return Observation{}, errNotImplemented
}

// Save serialises policies, records and counters.
func (p *HostHeaderPoisonProber) Save() ([]byte, error) {
	return nil, errNotImplemented
}

// Reload replaces the whole state with a snapshot, all or nothing.
func (p *HostHeaderPoisonProber) Reload(data []byte) error {
	return errNotImplemented
}
