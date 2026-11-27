// Package task issues, renews and cancels WS-Trust style security tokens for
// the federated login endpoint and keeps one token family per principal and
// audience pair.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Token states reported by Explain.
const (
	StateActive     = "active"
	StateExpired    = "expired"
	StateRevoked    = "revoked"
	StateSuperseded = "superseded"
)

// Reasons written on a tombstone.
const (
	ReasonCancelled = "cancelled"
	ReasonRenewed   = "renewed"
)

// Errors returned by the issuer.
var (
	ErrMissingField      = errors.New("principal and applies to are required")
	ErrLifetimeTooLong   = errors.New("requested lifetime exceeds the policy cap")
	ErrPrincipalQuota    = errors.New("too many live tokens for this principal")
	ErrAudienceQuota     = errors.New("too many live tokens for this audience")
	ErrUnknownToken      = errors.New("token not found")
	ErrTokenExpired      = errors.New("token expired")
	ErrTokenRevoked      = errors.New("token was revoked")
	ErrTokenSuperseded   = errors.New("token was superseded by a renewal")
	ErrAudienceMismatch  = errors.New("token was issued for another audience")
	ErrAudienceChange    = errors.New("renewal may not change the audience")
	ErrRenewWindowClosed = errors.New("renewal arrived outside the renewal window")
	ErrProofMismatch     = errors.New("proof of possession key does not match")
	ErrEmptyEntropy      = errors.New("server entropy must not be empty")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// IssuerPolicy bounds the token family tables.
type IssuerPolicy struct {
	Realm                  string
	MaxLifetimeSeconds     int
	DefaultLifetimeSeconds int
	RenewWindowSeconds     int
	MaxTokensPerPrincipal  int
	MaxTokensPerAudience   int
	MaxTombstones          int
	Now                    func() time.Time
}

// RequestSecurityToken is the RST body accepted by Issue and Renew.
type RequestSecurityToken struct {
	Principal       string
	AppliesTo       string
	LifetimeSeconds int
	ClientEntropy   []byte
	RequestID       string
	TokenID         string
}

// ProofOfPossession carries the key the client must present for Cancel.
type ProofOfPossession struct {
	Key string
}

// Response is the RSTR produced by Issue and Renew.
type Response struct {
	TokenID         string    `json:"token_id"`
	Family          string    `json:"family"`
	Principal       string    `json:"principal"`
	AppliesTo       string    `json:"applies_to"`
	LifetimeSeconds int       `json:"lifetime_seconds"`
	ProofKey        string    `json:"proof_key"`
	ExpiresAt       time.Time `json:"expires_at"`
	Renewed         bool      `json:"renewed"`
	Idempotent      bool      `json:"idempotent"`
}

// Claims is the assertion returned by Validate.
type Claims struct {
	Principal string    `json:"principal"`
	AppliesTo string    `json:"applies_to"`
	Family    string    `json:"family"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Trace is the operator view of one token.
type Trace struct {
	TokenID       string    `json:"token_id"`
	Family        string    `json:"family"`
	State         string    `json:"state"`
	Principal     string    `json:"principal"`
	AppliesTo     string    `json:"applies_to"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Renewals      int       `json:"renewals"`
	RevokedReason string    `json:"revoked_reason"`
}

// IssuerStats aggregates the counters of the issuer.
type IssuerStats struct {
	Issued     int `json:"issued"`
	Renewed    int `json:"renewed"`
	Cancelled  int `json:"cancelled"`
	Rejected   int `json:"rejected"`
	Tombstoned int `json:"tombstoned"`
}

// WsTrustIssuer owns the token table, the families and the revocation
// tombstones.
type WsTrustIssuer struct {
	cfg IssuerPolicy
}

// NewWsTrustIssuer builds an issuer bound to one policy.
func NewWsTrustIssuer(cfg IssuerPolicy) *WsTrustIssuer {
	return &WsTrustIssuer{cfg: cfg}
}

// Issue answers an issue request, idempotently for a repeated request id.
func (w *WsTrustIssuer) Issue(req RequestSecurityToken) (Response, error) {
	return Response{}, errNotImplemented
}

// Renew extends one live token inside the renewal window.
func (w *WsTrustIssuer) Renew(req RequestSecurityToken) (Response, error) {
	return Response{}, errNotImplemented
}

// Cancel revokes the whole token family of one token.
func (w *WsTrustIssuer) Cancel(tokenID string, proof ProofOfPossession) error {
	return errNotImplemented
}

// Validate turns one live token into claims for the given audience.
func (w *WsTrustIssuer) Validate(tokenID, appliesTo string) (Claims, error) {
	return Claims{}, errNotImplemented
}

// Explain returns the operator view of one token.
func (w *WsTrustIssuer) Explain(tokenID string) (Trace, error) {
	return Trace{}, errNotImplemented
}

// TokensFor counts the live tokens of one principal.
func (w *WsTrustIssuer) TokensFor(principal string) int {
	return 0
}

// FamilyOf reports the family name of one token.
func (w *WsTrustIssuer) FamilyOf(tokenID string) (string, bool) {
	return "", false
}

// IsRevoked reports whether one token id is covered by a tombstone.
func (w *WsTrustIssuer) IsRevoked(tokenID string) bool {
	return false
}

// KeyMaterial returns the hex proof key bound to one token.
func (w *WsTrustIssuer) KeyMaterial(tokenID string) (string, bool) {
	return "", false
}

// DeriveKey combines the server and client entropy into one proof key.
func (w *WsTrustIssuer) DeriveKey(serverEntropy, clientEntropy []byte) ([]byte, error) {
	return nil, errNotImplemented
}

// Stats returns a snapshot of the counters.
func (w *WsTrustIssuer) Stats() IssuerStats {
	return IssuerStats{}
}

// Snapshot serialises tokens, families, tombstones and counters.
func (w *WsTrustIssuer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (w *WsTrustIssuer) Restore(data []byte) error {
	return errNotImplemented
}
