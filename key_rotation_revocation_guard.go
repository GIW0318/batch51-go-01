// Package task rotates and revokes tenant signing keys while requests that
// were already signed keep a reference to the version they were issued under.
// Key material never leaves the guard; the public view only carries labels,
// states and reference counts.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Lifecycle states of one key version.
const (
	VersionActive   = "active"
	VersionRotating = "rotating"
	VersionRetired  = "retired"
	VersionRevoked  = "revoked"
)

// Actions written into the rotation event log.
const (
	ActionRotated  = "rotated"
	ActionAcquired = "acquired"
	ActionReleased = "released"
	ActionRetired  = "retired"
	ActionRevoked  = "revoked"
)

// Errors reported by the guard.
var (
	ErrBadTenant       = errors.New("tenant id is required")
	ErrBadConfig       = errors.New("version cap, audit cap and in-flight cap must be positive")
	ErrBadVersion      = errors.New("key version label is required")
	ErrBadMaterial     = errors.New("key material is required")
	ErrBadReason       = errors.New("revocation reason is required")
	ErrVersionConflict = errors.New("version label already bound to different material")
	ErrUnknownVersion  = errors.New("unknown key version")
	ErrVersionRevoked  = errors.New("key version is revoked")
	ErrVersionRetired  = errors.New("key version is retired")
	ErrVersionLimit    = errors.New("version table is full of referenced keys")
	ErrCurrentVersion  = errors.New("the current version cannot be retired")
	ErrUnknownLease    = errors.New("unknown lease")
	ErrLeaseClosed     = errors.New("lease already released")
	ErrInflightLimit   = errors.New("in-flight cap reached")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// RotationConfig bounds the permanent and the transient tables.
type RotationConfig struct {
	MaxVersions int
	MaxAudit    int
	MaxInflight int
}

// KeyVersion is the material free view of one version handed to callers.
type KeyVersion struct {
	Version   string
	State     string
	Refs      int
	RotatedAt int64
}

// Lease proves that one in-flight request holds a version.
type Lease struct {
	ID      string
	Version string
	Closed  bool
}

// RotationEvent is one line of the rotation audit log.
type RotationEvent struct {
	Seq     int64
	Action  string
	Version string
	Reason  string
	Refs    int
}

// RotationStats counts the traffic handled by the guard.
type RotationStats struct {
	Rotations int
	Revoked   int
	Retired   int
	Inflight  int
	Versions  int
}

// keyRecord keeps the material and the reference count of one version.
type keyRecord struct {
	version   string
	material  []byte
	state     string
	refs      int
	rotatedAt int64
}

// KeyRotationRevocationGuard owns the version table, the lease table and the
// rotation event log of one tenant.
type KeyRotationRevocationGuard struct {
	tenant  string
	cfg     RotationConfig
	keys    map[string]*keyRecord
	order   []string
	current string
	leases  map[string]*Lease
	events  []RotationEvent
	stats   RotationStats
	seq     int64
	leaseNo int64
}

// NewKeyRotationRevocationGuard validates the tenant and the caps.
func NewKeyRotationRevocationGuard(tenant string, cfg RotationConfig) (*KeyRotationRevocationGuard, error) {
	return &KeyRotationRevocationGuard{tenant: tenant, cfg: cfg}, nil
}

// Rotate publishes a version label and makes it the one new requests use.
func (g *KeyRotationRevocationGuard) Rotate(version string, material []byte, now int64) (KeyVersion, error) {
	return KeyVersion{}, errNotImplemented
}

// Resolve reports the state of a version without handing out its material.
func (g *KeyRotationRevocationGuard) Resolve(version string) (KeyVersion, error) {
	return KeyVersion{}, errNotImplemented
}

// Revoke kills a version for good and records why.
func (g *KeyRotationRevocationGuard) Revoke(version, reason string, now int64) error {
	return errNotImplemented
}

// Acquire registers one in-flight request against a version.
func (g *KeyRotationRevocationGuard) Acquire(version string) (Lease, error) {
	return Lease{}, errNotImplemented
}

// Release drops one in-flight reference.
func (g *KeyRotationRevocationGuard) Release(lease Lease) error {
	return errNotImplemented
}

// Retire takes a non current version out of service.
func (g *KeyRotationRevocationGuard) Retire(version string) error {
	return errNotImplemented
}

// Current reports the version new requests must use, empty when none is live.
func (g *KeyRotationRevocationGuard) Current() string {
	return ""
}

// Versions lists every retained version in rotation order.
func (g *KeyRotationRevocationGuard) Versions() []KeyVersion {
	return nil
}

// RefCount reports how many in-flight requests hold one version.
func (g *KeyRotationRevocationGuard) RefCount(version string) int {
	return 0
}

// Events returns a copy of the rotation audit log, oldest first.
func (g *KeyRotationRevocationGuard) Events() []RotationEvent {
	return nil
}

// Stats returns the running counters.
func (g *KeyRotationRevocationGuard) Stats() RotationStats {
	return RotationStats{}
}

// Snapshot serialises versions, leases, sequence, events and counters.
func (g *KeyRotationRevocationGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *KeyRotationRevocationGuard) Restore(data []byte) error {
	return errNotImplemented
}
