// Package task builds XML entity probes, submits them to a registered XML
// endpoint and correlates echo, error and out of band evidence by probe id.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Entity payload kinds.
const (
	KindInternal  = "internal"
	KindExternal  = "external"
	KindParameter = "parameter"
)

// Probe status values.
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusUncertain = "uncertain"
	StatusRefused   = "refused"
)

// Evidence channels.
const (
	ChannelEcho  = "echo"
	ChannelError = "error"
	ChannelOOB   = "oob"
)

// Reasons attached to a finding.
const (
	ReasonEntityExpanded     = "entity_expanded"
	ReasonEntityNotExpanded  = "entity_not_expanded"
	ReasonCallbackCorrelated = "callback_correlated"
	ReasonCallbackMissing    = "callback_missing"
	ReasonBodyTruncated      = "body_truncated"
	ReasonParseRejected      = "parse_rejected"
)

// EchoMarker prefixes the inline expansion written into a response body.
const EchoMarker = "GSB-"

// Errors returned by the prober.
var (
	ErrBadEndpoint       = errors.New("endpoint name is required")
	ErrDuplicateEndpoint = errors.New("endpoint is already registered")
	ErrUnknownEndpoint   = errors.New("endpoint is not registered")
	ErrBadProbeID        = errors.New("probe id is required")
	ErrBadPayloadKind    = errors.New("payload kind is not supported")
	ErrBadPayload        = errors.New("payload does not carry a doctype declaration")
	ErrBadCallback       = errors.New("callback does not carry a probe id")
	ErrUnknownProbe      = errors.New("callback does not match any probe")
	ErrProbeBudget       = errors.New("probe budget exhausted for endpoint")
	ErrBadLimit          = errors.New("prober limits must be positive")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// ProberConfig bounds one prober instance.
type ProberConfig struct {
	MaxConcurrent        int
	MaxProbesPerEndpoint int
	CallbackWindow       int64
	MaxCallbacks         int
}

// Endpoint is the parser profile of one candidate XML entry.
type Endpoint struct {
	Name             string
	ExpandsGeneral   bool
	ExpandsParameter bool
	ResolvesExternal bool
	RejectsDoctype   bool
	MaxBodyBytes     int
	SourceAddr       string
}

// Payload is one generated entity probe.
type Payload struct {
	ProbeID string
	Kind    string
	Body    string
	Marker  string
}

// Response is what an endpoint returns for one submitted payload.
type Response struct {
	Status    int
	Body      string
	Truncated bool
	Rejected  bool
}

// Callback is one out of band hit reported by the shared listener.
type Callback struct {
	ProbeID    string
	SourceAddr string
	At         int64
	Payload    string
}

// Correlation joins one probe with the evidence collected for it.
type Correlation struct {
	ProbeID   string
	Endpoint  string
	Kind      string
	Status    string
	Channel   string
	Confirmed bool
	Late      bool
	Callbacks int
}

// ProbeRecord is one submitted payload kept for replay.
type ProbeRecord struct {
	Seq        int64
	ProbeID    string
	Endpoint   string
	Kind       string
	Submitted  int64
	Deadline   int64
	Status     string
	Channel    string
	Callbacks  int
	Echoed     bool
}

// Finding is the graded outcome of one full probe.
type Finding struct {
	Endpoint  string
	Kind      string
	ProbeID   string
	Status    string
	Channel   string
	Confirmed bool
	Reason    string
	Callbacks int
}

// ProberStats counts the traffic of one endpoint.
type ProberStats struct {
	Probes       int64
	Callbacks    int64
	Correlated   int64
	Orphans      int64
	PeakInFlight int
}

// XxeEntityProber owns endpoints, probe records, callbacks and counters.
type XxeEntityProber struct {
	cfg ProberConfig
}

// NewXxeEntityProber builds a prober with the given caps.
func NewXxeEntityProber(cfg ProberConfig) *XxeEntityProber {
	return &XxeEntityProber{cfg: cfg}
}

// RegisterEndpoint adds one parser profile.
func (p *XxeEntityProber) RegisterEndpoint(ep Endpoint) error {
	return errNotImplemented
}

// Endpoints lists the registered endpoint names in registration order.
func (p *XxeEntityProber) Endpoints() []string {
	return nil
}

// BuildPayload builds one entity payload that embeds the probe id.
func (p *XxeEntityProber) BuildPayload(kind, probeID string) (Payload, error) {
	return Payload{}, errNotImplemented
}

// Submit posts one payload and records the evidence observed inline.
func (p *XxeEntityProber) Submit(endpoint string, payload Payload) (Response, error) {
	return Response{}, errNotImplemented
}

// DeliverCallback feeds one out of band hit into the shared listener table.
func (p *XxeEntityProber) DeliverCallback(cb Callback) error {
	return errNotImplemented
}

// Callbacks lists the callbacks received for one endpoint, oldest first.
func (p *XxeEntityProber) Callbacks(endpoint string) []Callback {
	return nil
}

// Correlate joins probes and callbacks for one endpoint.
func (p *XxeEntityProber) Correlate(endpoint string) []Correlation {
	return nil
}

// Probes lists the probe records of one endpoint, oldest first.
func (p *XxeEntityProber) Probes(endpoint string) []ProbeRecord {
	return nil
}

// Probe builds, submits and grades one entity probe end to end.
func (p *XxeEntityProber) Probe(endpoint, kind string) (Finding, error) {
	return Finding{}, errNotImplemented
}

// Advance moves the logical clock forward and returns the new time.
func (p *XxeEntityProber) Advance(ticks int64) int64 {
	return 0
}

// Expire marks every overdue probe of one endpoint as uncertain.
func (p *XxeEntityProber) Expire(endpoint string) int {
	return 0
}

// Stats reads the counters of one endpoint.
func (p *XxeEntityProber) Stats(endpoint string) ProberStats {
	return ProberStats{}
}

// Snapshot serialises endpoints, records, callbacks and the clock.
func (p *XxeEntityProber) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Load replaces the whole state with a snapshot, all or nothing.
func (p *XxeEntityProber) Load(data []byte) error {
	return errNotImplemented
}
