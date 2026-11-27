// Package task attests the integrity of a game client before login and before
// a match: it issues one shot challenges, compares the module hashes against
// the pinned manifest and grades the session.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Attestation verdict actions.
const (
	ActionAllow   = "allow"
	ActionLimited = "limited"
	ActionDeny    = "deny"
)

// Verdict reason codes.
const (
	ReasonVerified       = "verified"
	ReasonModuleMismatch = "module_mismatch"
	ReasonModuleMissing  = "module_missing"
	ReasonModuleDirty    = "module_dirty"
	ReasonUnsigned       = "unsigned"
	ReasonRevoked        = "session_revoked"
)

// Errors reported by the attestation service.
var (
	ErrBadSession        = errors.New("session id is required")
	ErrSessionRevoked    = errors.New("session is revoked")
	ErrChallengeNotFound = errors.New("challenge not found")
	ErrChallengeUsed     = errors.New("challenge already consumed")
	ErrChallengeExpired  = errors.New("challenge expired")
	ErrChallengeLimit    = errors.New("challenge cap reached")
	ErrBadProof          = errors.New("proof is invalid")
	ErrBadManifest       = errors.New("manifest is invalid")
	ErrManifestLimit     = errors.New("manifest exceeds the module cap")
	ErrBadPolicy         = errors.New("attestation policy is invalid")
	ErrBadModule         = errors.New("module name is required")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// ModuleEntry is one pinned module of a manifest version.
type ModuleEntry struct {
	Name     string
	Hash     string
	Size     int
	Critical bool
}

// Manifest is one immutable manifest version.
type Manifest struct {
	Entries []ModuleEntry
}

// ModuleProof is the client side reading of one module.
type ModuleProof struct {
	Name   string
	Hash   string
	Size   int
	ReadOK bool
}

// Proof is the client answer to one challenge.
type Proof struct {
	ChallengeID string
	Session     string
	Modules     []ModuleProof
	Signature   string
}

// Challenge is one one shot attestation ticket.
type Challenge struct {
	ID          string
	Session     string
	Nonce       string
	ManifestVer string
	IssuedAt    time.Time
	ExpiresAt   time.Time
}

// AttestPolicy is the attestation policy in force.
type AttestPolicy struct {
	ChallengeTTL     time.Duration
	RequireSignature bool
	OnMismatch       string
	OnReadFailure    string
}

// Verdict is the graded outcome of one session.
type Verdict struct {
	Session     string
	Action      string
	Reason      string
	ManifestVer string
}

// Reason is one explainable entry of a session verdict.
type Reason struct {
	Code   string
	Module string
	Detail string
}

// AttestStats aggregates the attestation counters.
type AttestStats struct {
	ChallengesIssued int
	ChallengesUsed   int
	Verified         int
	Allowed          int
	Limited          int
	Denied           int
}

// AttestLimits bounds the attestation service.
type AttestLimits struct {
	MaxModules    int
	MaxModuleSize int
	MaxChallenges int
}

// ClientIntegrityAttestation owns the challenge table, the manifests and the
// session verdicts.
type ClientIntegrityAttestation struct {
	limits AttestLimits
	clock  func() time.Time
}

// NewClientIntegrityAttestation builds a service with the given caps and clock.
func NewClientIntegrityAttestation(limits AttestLimits, clock func() time.Time) *ClientIntegrityAttestation {
	return &ClientIntegrityAttestation{limits: limits, clock: clock}
}

// IssueChallenge hands out one fresh challenge for a session.
func (a *ClientIntegrityAttestation) IssueChallenge(session string) (Challenge, error) {
	return Challenge{}, errNotImplemented
}

// Verify grades one proof against the challenged manifest version.
func (a *ClientIntegrityAttestation) Verify(session string, proof Proof) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// SetManifest installs a new manifest version.
func (a *ClientIntegrityAttestation) SetManifest(ver string, manifest Manifest) error {
	return errNotImplemented
}

// SetPolicy installs a new attestation policy.
func (a *ClientIntegrityAttestation) SetPolicy(p AttestPolicy) error {
	return errNotImplemented
}

// MarkModuleDirty flags one module as tampered with.
func (a *ClientIntegrityAttestation) MarkModuleDirty(module string) error {
	return errNotImplemented
}

// Revoke fences a session off for good.
func (a *ClientIntegrityAttestation) Revoke(session string) error {
	return errNotImplemented
}

// Explain lists the reasons attached to the last verdict of a session.
func (a *ClientIntegrityAttestation) Explain(session string) []Reason {
	return nil
}

// Stats returns a copy of the attestation counters.
func (a *ClientIntegrityAttestation) Stats() AttestStats {
	return AttestStats{}
}

// Snapshot serialises the challenge table, the manifests and the verdicts.
func (a *ClientIntegrityAttestation) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the service state with a snapshot, all or nothing.
func (a *ClientIntegrityAttestation) Restore(data []byte) error {
	return errNotImplemented
}
