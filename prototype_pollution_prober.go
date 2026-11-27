// Package task builds prototype pollution payloads, injects them into a
// registered JSON merge target, verifies the inherited side effect and
// recovers the shared object afterwards.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Payload path kinds.
const (
	PathProto       = "proto"
	PathConstructor = "constructor"
	PathArray       = "array"
)

// Merge modes of a registered target.
const (
	ModeSafe           = "safe"
	ModeVulnerable     = "vulnerable"
	ModeBlacklistProto = "blacklist_proto"
)

// Verification channels.
const (
	ChannelResponse   = "response"
	ChannelSideEffect = "side_effect"
)

// Reasons attached to a finding.
const (
	ReasonInherited        = "inherited"
	ReasonNotInherited     = "not_inherited"
	ReasonSideEffectOnly   = "side_effect_only"
	ReasonBlacklistBypassed = "blacklist_bypassed"
)

// ProtoKey is the prototype key used by the proto and array paths.
const ProtoKey = "__proto__"

// ConstructorKey and PrototypeKey build the constructor prototype path.
const (
	ConstructorKey = "constructor"
	PrototypeKey   = "prototype"
)

// Errors returned by the prober.
var (
	ErrBadTarget       = errors.New("target name is required")
	ErrDuplicateTarget = errors.New("target is already registered")
	ErrUnknownTarget   = errors.New("target is not registered")
	ErrBadKey          = errors.New("payload key is required")
	ErrBadPathKind     = errors.New("payload path kind is not supported")
	ErrBadPayload      = errors.New("payload does not carry a merge path")
	ErrBadMarker       = errors.New("marker is required")
	ErrBadMode         = errors.New("merge mode is not supported")
	ErrProbeBudget     = errors.New("probe budget exhausted for target")
	ErrBadLimit        = errors.New("prober limits must be positive")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// ProberConfig bounds one prober instance.
type ProberConfig struct {
	MaxConcurrent      int
	MaxProbesPerTarget int
	MaxResidualKeys    int
}

// TargetSpec describes one JSON merge target.
type TargetSpec struct {
	Name           string
	Mode           string
	Defaults       map[string]string
	WorkerIsolated bool
}

// Payload is one generated pollution probe.
type Payload struct {
	Kind   string
	Key    string
	Value  string
	Body   string
	Path   []string
	Marker string
}

// Response is what a target returns for one injected payload.
type Response struct {
	Accepted bool
	Status   int
	Merged   []string
	Worker   bool
}

// Verification is the read back of one polluted key.
type Verification struct {
	Inherited bool
	Value     string
	Channel   string
	Marker    string
}

// Recovery reports what a cleanup removed and what stayed behind.
type Recovery struct {
	Removed  []string
	Residual []string
	Seq      int64
}

// Baseline is the shared snapshot taken before a round of probes.
type Baseline struct {
	Keys map[string]string
	Seq  int64
}

// Finding is the graded outcome of one full probe.
type Finding struct {
	Target      string
	Kind        string
	Key         string
	Confirmed   bool
	Reason      string
	Channel     string
	BaselineSeq int64
	Seq         int64
}

// ProberStats counts the traffic of one target.
type ProberStats struct {
	Probes       int64
	Polluted     int64
	Recovered    int64
	Residual     int
	PeakInFlight int
}

// PrototypePollutionProber owns targets, baselines, findings and counters.
type PrototypePollutionProber struct {
	cfg ProberConfig
}

// NewPrototypePollutionProber builds a prober with the given caps.
func NewPrototypePollutionProber(cfg ProberConfig) *PrototypePollutionProber {
	return &PrototypePollutionProber{cfg: cfg}
}

// AddTarget registers one JSON merge target with its mode and default keys.
func (p *PrototypePollutionProber) AddTarget(spec TargetSpec) error {
	return errNotImplemented
}

// Targets lists the registered target names in registration order.
func (p *PrototypePollutionProber) Targets() []string {
	return nil
}

// Generate builds one payload for the requested path kind.
func (p *PrototypePollutionProber) Generate(kind, key, value string) (Payload, error) {
	return Payload{}, errNotImplemented
}

// Inject merges one payload into a target and reports the applied keys.
func (p *PrototypePollutionProber) Inject(target string, payload Payload) (Response, error) {
	return Response{}, errNotImplemented
}

// Verify reads back one key through a freshly created object.
func (p *PrototypePollutionProber) Verify(target, marker string) (Verification, error) {
	return Verification{}, errNotImplemented
}

// Probe builds, injects, verifies and grades one payload end to end.
func (p *PrototypePollutionProber) Probe(target, kind, key, value string) (Finding, error) {
	return Finding{}, errNotImplemented
}

// Baseline takes and stores the shared snapshot of one target.
func (p *PrototypePollutionProber) Baseline(target string) (Baseline, error) {
	return Baseline{}, errNotImplemented
}

// Recover removes the polluted keys of one target and reports the residue.
func (p *PrototypePollutionProber) Recover(target string) (Recovery, error) {
	return Recovery{}, errNotImplemented
}

// ResidualKeys lists the keys that are still inherited by new objects.
func (p *PrototypePollutionProber) ResidualKeys(target string) []string {
	return nil
}

// Stats reads the counters of one target.
func (p *PrototypePollutionProber) Stats(target string) ProberStats {
	return ProberStats{}
}

// Reset drops the findings of one target but keeps the merge state.
func (p *PrototypePollutionProber) Reset(target string) error {
	return errNotImplemented
}

// Export serialises targets, baselines, findings and counters.
func (p *PrototypePollutionProber) Export() ([]byte, error) {
	return nil, errNotImplemented
}

// Import replaces the whole state with a snapshot, all or nothing.
func (p *PrototypePollutionProber) Import(data []byte) error {
	return errNotImplemented
}
