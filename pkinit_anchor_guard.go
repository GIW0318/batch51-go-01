// Package task runs the certificate part of an initial authentication: it
// builds a chain to one of the configured trust anchors, maps the certificate
// subject to an account through the mapping rules, and re-checks that account
// before a ticket is issued.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Mapping rule kinds.
const (
	MatchDNS       = "dns"
	MatchUPN       = "upn"
	MatchPrincipal = "principal"
)

// Errors returned by the anchor guard.
var (
	ErrBadChain         = errors.New("certificate chain is empty or too long")
	ErrNoTrustPath      = errors.New("chain does not reach a live trust anchor")
	ErrExpiredCert      = errors.New("certificate is past its validity window")
	ErrNotYetValid      = errors.New("certificate is not valid yet")
	ErrKeyUsage         = errors.New("leaf certificate does not allow client authentication")
	ErrBadAnchor        = errors.New("trust anchor is incomplete or duplicated")
	ErrUnknownAnchor    = errors.New("trust anchor is not registered")
	ErrBadRule          = errors.New("mapping rule is incomplete")
	ErrMappingMissing   = errors.New("no mapping rule matched the certificate")
	ErrMappingConflict  = errors.New("more than one mapping rule matched the certificate")
	ErrAccountUnknown   = errors.New("account has no registered state")
	ErrAccountDisabled  = errors.New("account is disabled")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Cert is the subset of an X.509 certificate the guard needs.
type Cert struct {
	Subject   string
	Issuer    string
	Serial    string
	NotBefore time.Time
	NotAfter  time.Time
	KeyUsage  []string
	SANs      []string
}

// TrustAnchor is one configured root. Anchors of a newer generation may
// coexist with the older ones while a rotation is running.
type TrustAnchor struct {
	ID         string
	Subject    string
	Generation int
	Priority   int
	RetiredAt  time.Time
}

// MappingRule maps a certificate subject to one account.
type MappingRule struct {
	ID string
	// MatchKind is MatchDNS, MatchUPN or MatchPrincipal.
	MatchKind string
	// Pattern is an exact value or a wildcard starting with "*." or "*@".
	Pattern    string
	Account    string
	Generation int
}

// ChainResult reports the trust path that validated a certificate chain.
type ChainResult struct {
	AnchorID   string
	Generation int
	Subject    string
	Path       []string
}

// Mapping is the account a certificate maps to.
type Mapping struct {
	Principal  string
	Account    string
	RuleID     string
	Generation int
	At         time.Time
}

// AccountState is the directory state of one account, re-checked before a
// ticket is issued.
type AccountState struct {
	Account    string
	Enabled    bool
	Generation int
}

// IssueRequest carries everything one ticket request needs.
type IssueRequest struct {
	Chain     []Cert
	Principal string
	SANs      []string
}

// Ticket is one issued authentication ticket.
type Ticket struct {
	Account           string
	RuleID            string
	AnchorID          string
	AnchorGeneration  int
	MappingGeneration int
	IssuedAt          time.Time
	Seq               uint64
}

// TraceEntry is one decision written to the guard trace.
type TraceEntry struct {
	Stage   string
	Detail  string
	Outcome string
	Seq     uint64
	At      time.Time
}

// AnchorStats counts guard activity since the process started.
type AnchorStats struct {
	Chains    int
	Rejected  int
	Mapped    int
	Conflicts int
	Issued    int
}

// AnchorConfig bounds one guard instance.
type AnchorConfig struct {
	// MaxChainLen caps the certificate chain length.
	MaxChainLen int
	// Now is the clock used when a caller does not pass an instant.
	Now func() time.Time
}

// PkinitAnchorGuard owns the anchors, the mapping rules and the account
// states.
type PkinitAnchorGuard struct {
	cfg AnchorConfig
}

// NewPkinitAnchorGuard builds a guard with the given bounds.
func NewPkinitAnchorGuard(cfg AnchorConfig) *PkinitAnchorGuard {
	return &PkinitAnchorGuard{cfg: cfg}
}

// AddAnchor registers one trust anchor.
func (g *PkinitAnchorGuard) AddAnchor(a TrustAnchor) error {
	return errNotImplemented
}

// RetireAnchor stops one anchor from validating new chains.
func (g *PkinitAnchorGuard) RetireAnchor(id string, at time.Time) error {
	return errNotImplemented
}

// Anchors lists the configured trust anchors, oldest generation first.
func (g *PkinitAnchorGuard) Anchors() []TrustAnchor {
	return nil
}

// AddRule registers one subject to account mapping rule.
func (g *PkinitAnchorGuard) AddRule(r MappingRule) error {
	return errNotImplemented
}

// Rules lists the configured mapping rules in registration order.
func (g *PkinitAnchorGuard) Rules() []MappingRule {
	return nil
}

// Verify builds and validates the chain up to one live trust anchor.
func (g *PkinitAnchorGuard) Verify(chain []Cert, at time.Time) (ChainResult, error) {
	return ChainResult{}, errNotImplemented
}

// Map resolves a certificate subject to exactly one account.
func (g *PkinitAnchorGuard) Map(principal string, sans []string, at time.Time) (Mapping, error) {
	return Mapping{}, errNotImplemented
}

// Issue re-checks the chain, the mapping and the account state, then issues a
// ticket.
func (g *PkinitAnchorGuard) Issue(req IssueRequest, at time.Time) (Ticket, error) {
	return Ticket{}, errNotImplemented
}

// SetAccount stores the directory state of one account.
func (g *PkinitAnchorGuard) SetAccount(st AccountState) error {
	return errNotImplemented
}

// Account reads the stored state of one account.
func (g *PkinitAnchorGuard) Account(account string) (AccountState, bool) {
	return AccountState{}, false
}

// Trace returns the decision trace, oldest first.
func (g *PkinitAnchorGuard) Trace() []TraceEntry {
	return nil
}

// Stats returns the guard wide counters.
func (g *PkinitAnchorGuard) Stats() AnchorStats {
	return AnchorStats{}
}

// Snapshot serialises the anchors, the rules, the account states and the
// sequence watermark.
func (g *PkinitAnchorGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole guard state with a snapshot, all or nothing.
func (g *PkinitAnchorGuard) Restore(data []byte) error {
	return errNotImplemented
}
