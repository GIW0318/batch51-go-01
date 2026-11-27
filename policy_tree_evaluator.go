// Package task evaluates certificate policy trees for a validated path and
// reports which policies a relying party may accept.
package task

import "errors"

// errNotImplemented is returned by the behaviours that are still missing.
var errNotImplemented = errors.New("not implemented")

// AnyPolicyOID is the special anyPolicy identifier.
const AnyPolicyOID = "2.5.29.32.0"

// Reasons carried by PolicyResult and Trace.
const (
	ReasonOK            = "ok"
	ReasonNoValidPolicy = "no_valid_policy"
)

// Errors surfaced by the evaluator.
var (
	ErrEmptyChain      = errors.New("certificate chain is empty")
	ErrChainBroken     = errors.New("certificate chain is not linked")
	ErrTooManyPolicies = errors.New("too many policies on one certificate")
	ErrMappingDepth    = errors.New("policy mapping depth exceeded")
	ErrNodeBudget      = errors.New("policy tree node budget exceeded")
	ErrUnknownPolicy   = errors.New("policy was not part of the last evaluation")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// EvalConfig bounds the work one evaluation may perform.
type EvalConfig struct {
	MaxNodes           int
	MaxMappingDepth    int
	MaxPoliciesPerCert int
	CacheSize          int
}

// PolicyOID is one certificatePolicies entry.
type PolicyOID struct {
	OID       string
	Qualifier string
}

// PolicyMap is one issuerDomainPolicy to subjectDomainPolicy pair.
type PolicyMap struct {
	Issuer  string
	Subject string
}

// CertView is the policy relevant projection of one certificate. A negative
// skip counter means the corresponding extension is absent.
type CertView struct {
	Subject          string
	Issuer           string
	Fingerprint      string
	Policies         []PolicyOID
	Mappings         []PolicyMap
	RequireExplicit  int
	InhibitMapping   int
	InhibitAnyPolicy int
	SelfIssued       bool
}

// PolicyRequest describes what the relying party asks for.
type PolicyRequest struct {
	InitialPolicies []string
	RequireExplicit bool
	TrustAnchor     string
}

// PolicyResult is the accepted policy set of one path.
type PolicyResult struct {
	Valid    bool
	Policies []string
	Reason   string
	Steps    int
	ChainFP  string
}

// TraceStep is one certificate visit inside an explanation.
type TraceStep struct {
	Index     int
	Subject   string
	Before    []string
	After     []string
	SkipCerts int
	Note      string
}

// Trace explains how one policy travelled along the last evaluated path.
type Trace struct {
	OID      string
	ChainFP  string
	Steps    []TraceStep
	Accepted bool
	Reason   string
}

// PolicyTreeEvaluator owns the bounded result cache keyed by path fingerprint
// and initial policy set.
type PolicyTreeEvaluator struct {
	cfg EvalConfig
}

// NewPolicyTreeEvaluator builds an evaluator with the given caps.
func NewPolicyTreeEvaluator(cfg EvalConfig) *PolicyTreeEvaluator {
	return &PolicyTreeEvaluator{cfg: cfg}
}

// Evaluate walks the chain from the trust anchor to the leaf certificate.
func (e *PolicyTreeEvaluator) Evaluate(chain []CertView, req PolicyRequest) (PolicyResult, error) {
	return PolicyResult{}, errNotImplemented
}

// ValidPolicy reports whether one policy survived the last evaluated path.
func (e *PolicyTreeEvaluator) ValidPolicy(chain []CertView, oid string) (bool, error) {
	return false, errNotImplemented
}

// Explain returns the per certificate reasoning of the last evaluation.
func (e *PolicyTreeEvaluator) Explain(oid string) (Trace, error) {
	return Trace{}, errNotImplemented
}

// Cached reports whether the pair is already in the result cache.
func (e *PolicyTreeEvaluator) Cached(chain []CertView, req PolicyRequest) bool {
	return false
}

// Invalidate drops every cached result.
func (e *PolicyTreeEvaluator) Invalidate() error {
	return errNotImplemented
}

// Normalize validates linkage and per certificate policy counts.
func (e *PolicyTreeEvaluator) Normalize(chain []CertView) ([]CertView, error) {
	return nil, errNotImplemented
}

// Fingerprint derives the stable path fingerprint used as the cache key.
func (e *PolicyTreeEvaluator) Fingerprint(chain []CertView) (string, error) {
	return "", errNotImplemented
}

// PolicySet returns only the accepted OIDs of one path.
func (e *PolicyTreeEvaluator) PolicySet(chain []CertView, req PolicyRequest) ([]string, error) {
	return nil, errNotImplemented
}

// Snapshot serialises the cached evaluations and the node counter.
func (e *PolicyTreeEvaluator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole cache with a snapshot, all or nothing.
func (e *PolicyTreeEvaluator) Restore(data []byte) error {
	return errNotImplemented
}
