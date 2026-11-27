// Package task mines undocumented query and form parameters on a known
// endpoint by differential probing.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Candidate sources, in priority order.
const (
	SourceSeed     = "seed"
	SourceExisting = "existing"
	SourcePage     = "page"
)

// Evidence kinds attached to a finding.
const (
	EvidenceLength    = "length"
	EvidenceFields    = "fields"
	EvidenceReflected = "reflected"
	EvidenceNone      = "none"
)

// Errors reported by the miner.
var (
	ErrBadEndpoint      = errors.New("endpoint name and path are required")
	ErrNoCandidates     = errors.New("no candidate parameter to probe")
	ErrEmptyBatch       = errors.New("probe batch is empty")
	ErrBudgetExhausted  = errors.New("request budget exhausted")
	ErrNoBaseline       = errors.New("baseline has not been captured")
	ErrNoObservations   = errors.New("no observation to judge")
	ErrUnstableEndpoint = errors.New("endpoint response is not stable")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Endpoint is one documented API entry point.
type Endpoint struct {
	Name        string            `json:"name"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	ContentType string            `json:"content_type"`
	Params      []string          `json:"params"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
}

// Param is one injected query or form parameter.
type Param struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Request is one probe request handed to the transport.
type Request struct {
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	ContentType string            `json:"content_type"`
	Params      []Param           `json:"params"`
	Headers     map[string]string `json:"headers"`
}

// Response is what the transport reports back.
type Response struct {
	Status int               `json:"status"`
	Body   string            `json:"body"`
	Header map[string]string `json:"header"`
}

// Transport performs one request against the endpoint.
type Transport interface {
	Send(req Request) (Response, error)
}

// Baseline is the stable shape of the bare response.
type Baseline struct {
	Endpoint    string   `json:"endpoint"`
	Status      int      `json:"status"`
	Length      int      `json:"length"`
	Fields      []string `json:"fields"`
	Fingerprint string   `json:"fingerprint"`
	Samples     int      `json:"samples"`
}

// ParamCandidate is one parameter name under test.
type ParamCandidate struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// ParamObservation is what one candidate did to the response.
type ParamObservation struct {
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Status    int      `json:"status"`
	Length    int      `json:"length"`
	Fields    []string `json:"fields"`
	Reflected bool     `json:"reflected"`
	Location  string   `json:"location"`
	Diff      bool     `json:"diff"`
	Evidence  string   `json:"evidence"`
	Sentinel  string   `json:"sentinel"`
}

// ParamFinding is one accepted hidden parameter.
type ParamFinding struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Evidence string `json:"evidence"`
	Delta    int    `json:"delta"`
}

// BudgetState reports how many requests were spent.
type BudgetState struct {
	Used      int `json:"used"`
	Limit     int `json:"limit"`
	Remaining int `json:"remaining"`
}

// ProberConfig bounds the miner.
type ProberConfig struct {
	MaxRequests     int
	BaselineSamples int
	MaxSplits       int
}

// HiddenParamMiner owns the baseline, the observations and the budget.
type HiddenParamMiner struct {
	cfg ProberConfig
	mu  sync.Mutex
}

// NewHiddenParamMiner builds a miner with the given limits.
func NewHiddenParamMiner(cfg ProberConfig) *HiddenParamMiner {
	return &HiddenParamMiner{cfg: cfg}
}

// SetTransport installs the transport used for every request.
func (m *HiddenParamMiner) SetTransport(t Transport) error {
	return errNotImplemented
}

// Baseline captures the shape of the bare response and checks it is stable.
func (m *HiddenParamMiner) Baseline(endpoint Endpoint) (Baseline, error) {
	return Baseline{}, errNotImplemented
}

// Candidates builds the ordered candidate list for one endpoint.
func (m *HiddenParamMiner) Candidates(endpoint Endpoint, seed []string) ([]ParamCandidate, error) {
	return nil, errNotImplemented
}

// Probe injects one batch and attributes every change to single candidates.
func (m *HiddenParamMiner) Probe(endpoint Endpoint, batch []ParamCandidate) ([]ParamObservation, error) {
	return nil, errNotImplemented
}

// Judge turns the observations into accepted findings.
func (m *HiddenParamMiner) Judge(obs []ParamObservation) ([]ParamFinding, error) {
	return nil, errNotImplemented
}

// Confirmed returns a copy of every accepted finding.
func (m *HiddenParamMiner) Confirmed() []ParamFinding {
	return nil
}

// Observations returns a copy of every recorded observation.
func (m *HiddenParamMiner) Observations() []ParamObservation {
	return nil
}

// Budget reports how much of the request budget is still available.
func (m *HiddenParamMiner) Budget() (BudgetState, error) {
	return BudgetState{}, errNotImplemented
}

// Snapshot serialises the baseline, the findings and the budget.
func (m *HiddenParamMiner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole miner state, all or nothing.
func (m *HiddenParamMiner) Restore(data []byte) error {
	return errNotImplemented
}
