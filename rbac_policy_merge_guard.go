// Package task merges several RBAC policy documents of one tenant into a
// single role graph, expands role inheritance, and answers access requests
// with a deny precedence rule. Every decision carries the sources that matched
// and is appended to a bounded audit log with a monotonic sequence number.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Effects a rule may carry.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// Decision codes.
const (
	CodeAllow          = "rbm_allow"
	CodeDenyRule       = "rbm_deny_rule"
	CodeNoMatch        = "rbm_no_match"
	CodeUnknownTenant  = "rbm_unknown_tenant"
	CodeUnknownRole    = "rbm_unknown_role"
	CodePolicyReplaced = "rbm_policy_replaced"
)

// Errors returned by the guard.
var (
	ErrBadPolicy        = errors.New("policy document is incomplete")
	ErrDuplicatePolicy  = errors.New("policy id is already bound to another tenant")
	ErrUnknownRole      = errors.New("unknown role referenced by inheritance")
	ErrInheritanceCycle = errors.New("role inheritance contains a cycle")
	ErrTenantBoundary   = errors.New("role inheritance crosses a tenant boundary")
	ErrDepthExceeded    = errors.New("role inheritance exceeds the configured depth")
	ErrStaleVersion     = errors.New("policy version is not newer than the loaded one")
	ErrPolicyLimit      = errors.New("policy table is full")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Rule is one permission statement inside a role.
type Rule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

// RoleDef is one role of a policy document. Parents may name a role of the
// same tenant directly, or another tenant with the "tenant:role" form.
type RoleDef struct {
	Parents []string `json:"parents"`
	Rules   []Rule   `json:"rules"`
}

// Policy is one versioned RBAC document loaded from a tenant repository.
type Policy struct {
	ID      string             `json:"id"`
	Tenant  string             `json:"tenant"`
	Version int64              `json:"version"`
	Roles   map[string]RoleDef `json:"roles"`
}

// Request is one access question.
type Request struct {
	Tenant   string `json:"tenant"`
	Role     string `json:"role"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// Match names one rule that took part in a decision and where it came from.
type Match struct {
	Policy   string `json:"policy"`
	Role     string `json:"role"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

// Decision is the answer to one Request.
type Decision struct {
	Allowed bool    `json:"allowed"`
	Code    string  `json:"code"`
	Reason  string  `json:"reason"`
	Matched []Match `json:"matched"`
	Seq     int64   `json:"seq"`
}

// AuditEvent is one line of the decision log.
type AuditEvent struct {
	Seq      int64  `json:"seq"`
	Tenant   string `json:"tenant"`
	Role     string `json:"role"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Code     string `json:"code"`
	Allowed  bool   `json:"allowed"`
	Sources  int    `json:"sources"`
}

// MergeConfig bounds the policy and audit tables and the inheritance depth.
type MergeConfig struct {
	MaxPolicies int
	MaxDepth    int
	MaxAudit    int
}

// RbacPolicyMergeGuard owns the policy table, the derived role graph and the
// audit log.
type RbacPolicyMergeGuard struct {
	cfg MergeConfig
}

// NewRbacPolicyMergeGuard builds a guard with the given bounds.
func NewRbacPolicyMergeGuard(cfg MergeConfig) *RbacPolicyMergeGuard {
	return &RbacPolicyMergeGuard{cfg: cfg}
}

// Load validates one policy document and installs it. A document with the same
// id is only accepted when its version is strictly newer.
func (g *RbacPolicyMergeGuard) Load(p Policy) error {
	return errNotImplemented
}

// Merge installs a batch of policy documents as one unit: if any document is
// rejected the table is left untouched.
func (g *RbacPolicyMergeGuard) Merge(policies []Policy) error {
	return errNotImplemented
}

// Expand returns the role itself plus every inherited role, deduplicated and
// sorted by name.
func (g *RbacPolicyMergeGuard) Expand(tenant, role string) ([]string, error) {
	return nil, errNotImplemented
}

// Evaluate answers one access request.
func (g *RbacPolicyMergeGuard) Evaluate(req Request) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Roles lists the role names known for a tenant in ascending order.
func (g *RbacPolicyMergeGuard) Roles(tenant string) []string {
	return nil
}

// Policies lists the policy ids loaded for a tenant in ascending order.
func (g *RbacPolicyMergeGuard) Policies(tenant string) []string {
	return nil
}

// Version reports the loaded version of one policy id.
func (g *RbacPolicyMergeGuard) Version(policyID string) (int64, bool) {
	return 0, false
}

// Audit returns a copy of the decision log, oldest first.
func (g *RbacPolicyMergeGuard) Audit() []AuditEvent {
	return nil
}

// Snapshot serialises the policy table and the audit sequence watermark.
func (g *RbacPolicyMergeGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole policy table from a snapshot, all or nothing.
func (g *RbacPolicyMergeGuard) Restore(data []byte) error {
	return errNotImplemented
}
