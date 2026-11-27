// Package task builds document query operator probes, submits them to a
// registered document target and grades the response pairs returned by the
// two parsing paths of the target.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Query operators carried inside a probe payload.
const (
	OpEqual    = "$eq"
	OpNotEqual = "$ne"
	OpGreater  = "$gt"
	OpLess     = "$lt"
	OpRegex    = "$regex"
	OpIn       = "$in"
)

// ProbeSentinel is the value used to build the true and false pair.
const ProbeSentinel = "__gsb_probe__"

// Parse modes of a registered document target.
const (
	ModeObject = "object"
	ModeString = "string"
)

// Pair labels recorded for every submitted probe.
const (
	PairSingle = "single"
	PairTrue   = "true"
	PairFalse  = "false"
)

// Channels used to confirm a probe pair.
const (
	ChannelPair  = "pair"
	ChannelCount = "count"
	ChannelDelay = "delay"
)

// Reasons attached to a Verdict.
const (
	ReasonOperatorEvaluated = "operator_evaluated"
	ReasonStringLiteral     = "string_literal"
	ReasonPairEqual         = "pair_equal"
	ReasonNoCandidates      = "no_candidates"
	ReasonEmptyBaseline     = "empty_baseline"
	ReasonBaselineOnly      = "baseline_only"
)

// Errors returned by the prober.
var (
	ErrBadTarget       = errors.New("target name is required")
	ErrDuplicateTarget = errors.New("target is already registered")
	ErrUnknownTarget   = errors.New("target is not registered")
	ErrBadField        = errors.New("field name is required")
	ErrBadOperator     = errors.New("operator is not supported")
	ErrBadPayload      = errors.New("payload does not carry an operator")
	ErrBadMode         = errors.New("parse mode is not supported")
	ErrProbeBudget     = errors.New("probe budget exhausted for target")
	ErrBadLimit        = errors.New("prober limits must be positive")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// ProberConfig bounds one prober instance.
type ProberConfig struct {
	MaxConcurrent      int
	MaxProbesPerTarget int
	MaxRecords         int
}

// Document is one record held by a registered target.
type Document struct {
	ID     string
	Fields map[string]string
}

// Payload is one generated operator probe.
type Payload struct {
	Field     string
	Op        string
	Value     string
	Body      string
	Pair      string
	ArrayForm bool
}

// Response is what a target returns for one submitted payload.
type Response struct {
	Status  int
	Matched int
	DocIDs  []string
	Body    string
	Errored bool
}

// Baseline is the shared snapshot taken before a true and a false pair.
type Baseline struct {
	Field   string
	Matched int
	Documents int
	Seq     int64
}

// Verdict is the graded outcome of one true and false pair.
type Verdict struct {
	Field      string
	Injectable bool
	Reason     string
	Channel    string
	TrueHits   int
	FalseHits  int
	Baseline   int
	Seq        int64
}

// ProbeRecord is one submitted payload kept for replay.
type ProbeRecord struct {
	Seq         int64
	Field       string
	Op          string
	Pair        string
	Matched     int
	Status      int
	BaselineSeq int64
}

// ProberStats counts the traffic of one target.
type ProberStats struct {
	Probes       int64
	Rejected     int64
	PeakInFlight int
	Records      int
}

// NoSqlOperatorProber owns the targets, baselines, records and counters.
type NoSqlOperatorProber struct {
	cfg ProberConfig
}

// NewNoSqlOperatorProber builds a prober with the given caps.
func NewNoSqlOperatorProber(cfg ProberConfig) (*NoSqlOperatorProber, error) {
	return &NoSqlOperatorProber{cfg: cfg}, nil
}

// RegisterTarget adds one document collection with its parsing mode.
func (p *NoSqlOperatorProber) RegisterTarget(name string, docs []Document, mode string) error {
	return errNotImplemented
}

// Targets lists the registered target names in registration order.
func (p *NoSqlOperatorProber) Targets() []string {
	return nil
}

// Generate builds a single operator payload for one field.
func (p *NoSqlOperatorProber) Generate(field, value, op string) (Payload, error) {
	return Payload{}, errNotImplemented
}

// Inject submits one payload to a target and returns the matched documents.
func (p *NoSqlOperatorProber) Inject(target string, payload Payload) (Response, error) {
	return Response{}, errNotImplemented
}

// Baseline takes and stores the shared snapshot of one target.
func (p *NoSqlOperatorProber) Baseline(target, field string) (Baseline, error) {
	return Baseline{}, errNotImplemented
}

// Verdict runs the true and false pair and grades the difference.
func (p *NoSqlOperatorProber) Verdict(target, field string) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// ProbeLog returns the records of one target, oldest first.
func (p *NoSqlOperatorProber) ProbeLog(target string) []ProbeRecord {
	return nil
}

// Stats reads the counters of one target.
func (p *NoSqlOperatorProber) Stats(target string) ProberStats {
	return ProberStats{}
}

// GlobalStats reads the counters shared by every target.
func (p *NoSqlOperatorProber) GlobalStats() ProberStats {
	return ProberStats{}
}

// Reset drops the records of one target but keeps documents and baseline.
func (p *NoSqlOperatorProber) Reset(target string) error {
	return errNotImplemented
}

// Snapshot serialises targets, baselines, records and counters.
func (p *NoSqlOperatorProber) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *NoSqlOperatorProber) Restore(data []byte) error {
	return errNotImplemented
}
