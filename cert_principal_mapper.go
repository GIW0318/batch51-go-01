// Package task maps a client certificate identity onto an internal account for
// the mTLS ingress gateway: ordered matching rules per tenant, conflict
// reporting and a per fingerprint result cache.
package task

import "errors"

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Matchers supported by a mapping rule.
const (
	MatcherDN       = "dn"
	MatcherCN       = "cn"
	MatcherSANDNS   = "san_dns"
	MatcherSANEmail = "san_email"
	MatcherSANURI   = "san_uri"
	MatcherIssuerFP = "issuer_fp"
)

// Principal kinds produced by a rule.
const (
	KindUser    = "user"
	KindService = "service"
)

// SAN types carried by a certificate view.
const (
	SANDNS   = "dns"
	SANEmail = "email"
	SANURI   = "uri"
)

// Trace reasons reported by Explain.
const (
	ReasonMatched  = "matched"
	ReasonNoMatch  = "no_match"
	ReasonConflict = "conflict"
	ReasonRevoked  = "revoked"
)

// Errors returned by the mapper.
var (
	ErrMissingField   = errors.New("rule id, pattern, account and certificate fingerprint are required")
	ErrBadMatcher     = errors.New("matcher is not supported")
	ErrBadPriority    = errors.New("rule priority must not be negative")
	ErrRuleExists     = errors.New("rule already exists for this tenant")
	ErrRuleQuota      = errors.New("too many rules for this tenant")
	ErrRuleNotFound   = errors.New("rule not found")
	ErrNoMapping      = errors.New("no rule maps this certificate")
	ErrMappingConflict = errors.New("two rules at the same priority map this certificate")
	ErrCertRevoked    = errors.New("certificate fingerprint is revoked")
	ErrSANQuota       = errors.New("certificate carries too many subject alternative names")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// MapperConfig bounds the rule and cache tables.
type MapperConfig struct {
	DefaultTenant     string
	MaxRulesPerTenant int
	MaxCacheEntries   int
	MaxSANs           int
}

// MappingRule is one ordered matcher of one tenant.
type MappingRule struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
	Matcher  string `json:"matcher"`
	Pattern  string `json:"pattern"`
	Account  string `json:"account"`
	Kind     string `json:"kind"`
}

// SAN is one subject alternative name of the presented certificate.
type SAN struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// CertView is the certificate as seen by the gateway.
type CertView struct {
	Fingerprint string `json:"fingerprint"`
	SubjectDN   string `json:"subject_dn"`
	CN          string `json:"cn"`
	SANs        []SAN  `json:"sans"`
	IssuerFP    string `json:"issuer_fp"`
	Revoked     bool   `json:"revoked"`
}

// Principal is the account resolved for one certificate.
type Principal struct {
	Account     string `json:"account"`
	Kind        string `json:"kind"`
	Tenant      string `json:"tenant"`
	RuleID      string `json:"rule_id"`
	Matched     string `json:"matched"`
	Fingerprint string `json:"fingerprint"`
}

// Trace is the operator view of one resolve attempt.
type Trace struct {
	Tenant      string   `json:"tenant"`
	Fingerprint string   `json:"fingerprint"`
	Evaluated   []string `json:"evaluated"`
	Winner      string   `json:"winner"`
	Conflict    bool     `json:"conflict"`
	Reason      string   `json:"reason"`
}

// MapperStats aggregates the counters of the mapper.
type MapperStats struct {
	Resolved  int `json:"resolved"`
	CacheHits int `json:"cache_hits"`
	Rejected  int `json:"rejected"`
	Evicted   int `json:"evicted"`
}

// CertPrincipalMapper owns the per tenant rule sets, the revoked fingerprints
// and the resolution cache.
type CertPrincipalMapper struct {
	cfg MapperConfig
}

// NewCertPrincipalMapper builds a mapper with the given caps.
func NewCertPrincipalMapper(cfg MapperConfig) *CertPrincipalMapper {
	return &CertPrincipalMapper{cfg: cfg}
}

// AddRule appends one rule to a tenant rule set.
func (m *CertPrincipalMapper) AddRule(tenant string, rule MappingRule) error {
	return errNotImplemented
}

// RemoveRule drops one rule from a tenant rule set.
func (m *CertPrincipalMapper) RemoveRule(tenant, ruleID string) error {
	return errNotImplemented
}

// Rules lists the rules of one tenant, highest priority first.
func (m *CertPrincipalMapper) Rules(tenant string) []MappingRule {
	return nil
}

// Resolve maps one certificate onto one internal account.
func (m *CertPrincipalMapper) Resolve(tenant string, cert CertView) (Principal, error) {
	return Principal{}, errNotImplemented
}

// Explain reports how one certificate was judged.
func (m *CertPrincipalMapper) Explain(tenant string, cert CertView) (Trace, error) {
	return Trace{}, errNotImplemented
}

// ResolveMany maps a batch of certificates of one tenant.
func (m *CertPrincipalMapper) ResolveMany(tenant string, certs []CertView) ([]Principal, error) {
	return nil, errNotImplemented
}

// Invalidate revokes one certificate fingerprint and drops its cache entry.
func (m *CertPrincipalMapper) Invalidate(certFP string) error {
	return errNotImplemented
}

// Revoked reports whether one fingerprint was invalidated.
func (m *CertPrincipalMapper) Revoked(certFP string) bool {
	return false
}

// CacheSize reports how many fingerprint results are cached.
func (m *CertPrincipalMapper) CacheSize() int {
	return 0
}

// Stats returns a snapshot of the counters.
func (m *CertPrincipalMapper) Stats() MapperStats {
	return MapperStats{}
}

// Snapshot serialises rules, revocations and the cache.
func (m *CertPrincipalMapper) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *CertPrincipalMapper) Restore(data []byte) error {
	return errNotImplemented
}
