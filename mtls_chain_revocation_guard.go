// Package task admits inbound mutual TLS peers by rebuilding the client
// certificate chain from the leaf up to a configured trust anchor, checking
// the validity window and key usage of every certificate on the path, and
// cross checking the exact leaf serial against a revocation table before the
// pinned fingerprint of the leaf is compared with the one presented on the
// wire. Every verdict, allowed or refused, is written to a bounded audit trail
// together with the sequence number it was assigned.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Verdict codes carried by a Decision.
const (
	VerdictAllow = "mtc_allow"
	VerdictDeny  = "mtc_deny"
)

// Reason codes explain a Decision and are attached to the audit trail.
const (
	ReasonTrusted            = "mtc_trusted"
	ReasonBrokenLink         = "mtc_broken_link"
	ReasonUnknownAnchor      = "mtc_unknown_anchor"
	ReasonCertificateExpired = "mtc_certificate_expired"
	ReasonIssuerNotCA        = "mtc_issuer_not_ca"
	ReasonLeafKeyUsage       = "mtc_leaf_key_usage"
	ReasonRevoked            = "mtc_revoked"
	ReasonRevocationDown     = "mtc_revocation_unavailable"
	ReasonPinMismatch        = "mtc_pin_mismatch"
	ReasonChainDepth         = "mtc_chain_depth"
	ReasonMalformed          = "mtc_malformed"
)

// Errors returned by the guard.
var (
	ErrBadCertificate        = errors.New("certificate record is incomplete")
	ErrBadRevocation         = errors.New("revocation record is incomplete")
	ErrChainIncomplete       = errors.New("chain is missing an intermediate certificate")
	ErrChainOrder            = errors.New("chain is not ordered leaf first")
	ErrChainDepth            = errors.New("chain exceeds the configured depth")
	ErrCertificateExpired    = errors.New("certificate is outside its validity window")
	ErrNotCA                 = errors.New("issuer certificate is not a CA")
	ErrKeyUsage              = errors.New("leaf certificate lacks client auth usage")
	ErrUnknownAnchor         = errors.New("chain does not reach a trust anchor")
	ErrRevocationUnavailable = errors.New("revocation source is unavailable")
	ErrPinMismatch           = errors.New("presented fingerprint does not match the pinned leaf")
	ErrPinLimit              = errors.New("pinned fingerprint table is full")
	ErrBadSnapshot           = errors.New("snapshot payload is invalid")
)

// CertRecord is one parsed certificate. SignedBy carries the fingerprint of
// the issuing certificate, so a chain is linked when the SignedBy value of a
// certificate equals the Fingerprint of the next one.
type CertRecord struct {
	Serial      string   `json:"serial"`
	Subject     string   `json:"subject"`
	Issuer      string   `json:"issuer"`
	Fingerprint string   `json:"fingerprint"`
	SignedBy    string   `json:"signed_by"`
	IsCA        bool     `json:"is_ca"`
	NotBefore   int64    `json:"not_before"`
	NotAfter    int64    `json:"not_after"`
	KeyUsage    []string `json:"key_usage"`
}

// RevocationRecord is one entry of the revocation table. Entries are keyed by
// the pair (issuer, serial); the subject name is only carried for reporting.
type RevocationRecord struct {
	Serial    string `json:"serial"`
	Issuer    string `json:"issuer"`
	Subject   string `json:"subject"`
	RevokedAt int64  `json:"revoked_at"`
	Reason    string `json:"reason"`
}

// PinEntry keeps one leaf fingerprint with the window it stays on record.
type PinEntry struct {
	Fingerprint string `json:"fingerprint"`
	NotBefore   int64  `json:"not_before"`
	NotAfter    int64  `json:"not_after"`
}

// Decision is the admission verdict for one presented chain.
type Decision struct {
	Allowed bool     `json:"allowed"`
	Code    string   `json:"code"`
	Reason  string   `json:"reason"`
	Leaf    string   `json:"leaf"`
	Depth   int      `json:"depth"`
	Chain   []string `json:"chain"`
	Seq     int64    `json:"seq"`
}

// AuditRecord is one line of the admission trail.
type AuditRecord struct {
	Seq    int64  `json:"seq"`
	Event  string `json:"event"`
	Serial string `json:"serial"`
	Issuer string `json:"issuer"`
	Depth  int    `json:"depth"`
	Code   string `json:"code"`
	At     int64  `json:"at"`
}

// GuardConfig bounds the guard tables.
type GuardConfig struct {
	MaxChainDepth int
	MaxAudit      int
	MaxPins       int
}

// MtlsChainRevocationGuard owns the trust anchors, the revocation table, the
// pinned leaf fingerprints and the admission audit trail.
type MtlsChainRevocationGuard struct {
	cfg GuardConfig
}

// NewMtlsChainRevocationGuard builds a guard with the given bounds.
func NewMtlsChainRevocationGuard(cfg GuardConfig) *MtlsChainRevocationGuard {
	return &MtlsChainRevocationGuard{cfg: cfg}
}

// Trust registers one trust anchor. Registering the same fingerprint twice is
// idempotent and keeps the existing entry.
func (g *MtlsChainRevocationGuard) Trust(anchor CertRecord) error {
	return errNotImplemented
}

// TrustedAnchors lists the registered anchor fingerprints in ascending order.
func (g *MtlsChainRevocationGuard) TrustedAnchors() []string {
	return nil
}

// AddRevocation appends one entry to the revocation table.
func (g *MtlsChainRevocationGuard) AddRevocation(rec RevocationRecord) error {
	return errNotImplemented
}

// SetRevocationSource marks the revocation table as reachable or not. While it
// is unreachable every admission is refused.
func (g *MtlsChainRevocationGuard) SetRevocationSource(available bool) {
}

// IsRevoked reports whether the exact (issuer, serial) pair is on the
// revocation table. It fails with ErrRevocationUnavailable while the source is
// unreachable.
func (g *MtlsChainRevocationGuard) IsRevoked(serial, issuer string) (bool, error) {
	return false, errNotImplemented
}

// CheckChain rebuilds and validates the chain, returning it leaf first.
func (g *MtlsChainRevocationGuard) CheckChain(chain []CertRecord, now int64) ([]CertRecord, error) {
	return nil, errNotImplemented
}

// Decide produces the admission verdict for a presented chain.
func (g *MtlsChainRevocationGuard) Decide(chain []CertRecord, now int64, peerFingerprint string) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Pin records the leaf fingerprint that may be presented between startAt and
// endAt.
func (g *MtlsChainRevocationGuard) Pin(cert CertRecord, startAt, endAt int64) error {
	return errNotImplemented
}

// Pins returns a copy of the pin table ordered by fingerprint.
func (g *MtlsChainRevocationGuard) Pins() []PinEntry {
	return nil
}

// Audit returns a copy of the admission trail, oldest first.
func (g *MtlsChainRevocationGuard) Audit() []AuditRecord {
	return nil
}

// Snapshot serialises anchors, revocations, pins, the audit trail and the
// verdict counter.
func (g *MtlsChainRevocationGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole guard state from a snapshot, all or nothing.
func (g *MtlsChainRevocationGuard) Restore(data []byte) error {
	return errNotImplemented
}
