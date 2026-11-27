// Package task inspects the type graph of an untrusted payload before any
// value is materialised. Only fully qualified type names carried by the
// allowlist may appear anywhere in the graph, and the walk is bounded in depth
// and size so a hostile payload cannot buy unbounded work.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Reason codes stored on a decision and on an audit record.
const (
	ReasonTypesAllowed  = "reason_types_allowed"
	ReasonRootDenied    = "reason_root_denied"
	ReasonFieldDenied   = "reason_field_denied"
	ReasonDepthExceeded = "reason_depth_exceeded"
	ReasonCycleDetected = "reason_cycle_detected"
	ReasonNodeBudget    = "reason_node_budget"
)

// Errors reported by the guard.
var (
	ErrBadConfig     = errors.New("depth cap, node cap and audit cap must be positive")
	ErrBadTypeName   = errors.New("type name must be fully qualified")
	ErrBadPayload    = errors.New("payload is not a readable type description")
	ErrBadRoot       = errors.New("payload must name a root type")
	ErrDuplicateType = errors.New("payload declares the same type twice")
	ErrMissingRoot   = errors.New("root type is missing from the payload")
	ErrUnknownType   = errors.New("field points at a type the payload does not declare")
	ErrGraphTooLarge = errors.New("type graph exceeds the node cap")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// AllowlistConfig bounds the walk and the audit trail.
type AllowlistConfig struct {
	MaxDepth int
	MaxNodes int
	MaxAudit int
}

// TypeNode is one declared type. Field values are type references and may use
// the pointer and slice decorations of the wire format, for example
// "[]pkg.Item" or "*pkg.Item".
type TypeNode struct {
	Name   string            `json:"name"`
	Fields map[string]string `json:"fields"`
}

// Payload is the raw type description shipped alongside a value.
type Payload struct {
	Root  string     `json:"root"`
	Nodes []TypeNode `json:"nodes"`
}

// TypeGraph is the resolved form of a payload: every reference points at a
// declared node and the declaration order is remembered.
type TypeGraph struct {
	Root  string
	Nodes map[string]TypeNode
	Order []string
}

// Decision is the explainable outcome of one adjudication.
type Decision struct {
	Allowed bool
	Reason  string
	Type    string
	Field   string
	Path    []string
	Depth   int
	Visited int
	Seq     int64
}

// CheckRecord is one line of the adjudication trail.
type CheckRecord struct {
	Seq     int64
	Root    string
	Allowed bool
	Reason  string
	Type    string
	Nodes   int
}

// GuardStats counts the traffic seen by the guard.
type GuardStats struct {
	Checks       int
	Allowed      int
	Denied       int
	AllowedTypes int
}

// walkEntry is the per node state used while walking the graph.
type walkEntry struct {
	depth int
	path  []string
}

// DeserializationAllowlistGuard owns the allowlist, the audit log and the
// counters of one deployment.
type DeserializationAllowlistGuard struct {
	cfg     AllowlistConfig
	allowed map[string]bool
	audit   []CheckRecord
	stats   GuardStats
	seq     int64
}

// NewDeserializationAllowlistGuard validates the caps and starts empty.
func NewDeserializationAllowlistGuard(cfg AllowlistConfig) (*DeserializationAllowlistGuard, error) {
	return &DeserializationAllowlistGuard{cfg: cfg}, nil
}

// Allow admits one fully qualified type name; repeats are harmless.
func (g *DeserializationAllowlistGuard) Allow(name string) error {
	return errNotImplemented
}

// Drop removes one name from the allowlist.
func (g *DeserializationAllowlistGuard) Drop(name string) error {
	return errNotImplemented
}

// Parse reads the type description out of an untrusted payload.
func (g *DeserializationAllowlistGuard) Parse(raw []byte) (Payload, error) {
	return Payload{}, errNotImplemented
}

// BuildGraph resolves every type reference of a payload.
func (g *DeserializationAllowlistGuard) BuildGraph(p Payload) (TypeGraph, error) {
	return TypeGraph{}, errNotImplemented
}

// Adjudicate walks a resolved graph against the allowlist.
func (g *DeserializationAllowlistGuard) Adjudicate(graph TypeGraph) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Check runs parse, build and adjudicate in one step and records the outcome.
func (g *DeserializationAllowlistGuard) Check(raw []byte) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Policy returns the allowlist in ascending order.
func (g *DeserializationAllowlistGuard) Policy() []string {
	return nil
}

// Audit returns a copy of the adjudication trail, oldest first.
func (g *DeserializationAllowlistGuard) Audit() []CheckRecord {
	return nil
}

// Stats returns the running counters.
func (g *DeserializationAllowlistGuard) Stats() GuardStats {
	return GuardStats{}
}

// MaxDepth reports the configured depth cap.
func (g *DeserializationAllowlistGuard) MaxDepth() int {
	return 0
}

// Snapshot serialises the allowlist, the audit log, the sequence and the
// counters.
func (g *DeserializationAllowlistGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *DeserializationAllowlistGuard) Restore(data []byte) error {
	return errNotImplemented
}
