// Package task manages the keytab of a service principal: several key
// generations of different encryption types coexist, a new key is staged
// before it becomes active, an old key may only be retired after its grace
// period, and the whole table is replaced atomically on disk.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Enctype is the encryption type of one keytab entry.
type Enctype int

// Encryption types the guard understands.
const (
	EnctypeAES128 Enctype = 17
	EnctypeAES256 Enctype = 18
	EnctypeRC4    Enctype = 23
)

// Errors returned by the keytab guard.
var (
	ErrBadEntry        = errors.New("keytab entry is incomplete")
	ErrVersionRegression = errors.New("key version is lower than the newest stored one")
	ErrUnknownVersion  = errors.New("key version is not in the keytab")
	ErrNoActiveKey     = errors.New("principal has no active key")
	ErrStalePromotion  = errors.New("only a version newer than the active one can be promoted")
	ErrActiveKey       = errors.New("the active key cannot be retired")
	ErrGraceNotElapsed = errors.New("retirement grace period has not elapsed")
	ErrRetired         = errors.New("key version is retired")
	ErrStagedKey       = errors.New("key version is staged but not active")
	ErrNoMatchingKey   = errors.New("no active key matches the ticket checksum")
	ErrVersionBudget   = errors.New("key version budget for this principal is exhausted")
	ErrBadKeytab       = errors.New("keytab file is malformed")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// KeyEntry is one key of one principal.
type KeyEntry struct {
	Principal   string
	Version     int
	Enctype     Enctype
	Material    string
	Staged      bool
	Retired     bool
	InstalledAt time.Time
	ActivatedAt time.Time
	RetiredAt   time.Time
}

// Ticket is a service ticket handed in for verification.
type Ticket struct {
	Principal string
	// KVNO zero means the verifier picks the key itself, newest first.
	KVNO      int
	Enctype   Enctype
	Payload   string
	Checksum  string
}

// KeyUse reports which key verified a ticket.
type KeyUse struct {
	Principal string
	Version   int
	Enctype   Enctype
}

// InstallResult reports one accepted keytab write.
type InstallResult struct {
	Entry      KeyEntry
	Generation uint64
}

// KeytabConfig bounds one principal keytab.
type KeytabConfig struct {
	// Grace is how long a superseded key stays usable after activation of its
	// successor.
	Grace time.Duration
	// MaxVersions caps the version count kept per principal.
	MaxVersions int
	// Now is the clock used when a caller does not pass an instant.
	Now func() time.Time
}

// KeytabStats counts keytab activity since the process started.
type KeytabStats struct {
	Installs      int
	Promotions    int
	Retires       int
	Verifications int
	Misses        int
}

// KeytabRotationGuard owns the per principal key generations and the on disk
// keytab image.
type KeytabRotationGuard struct {
	cfg KeytabConfig
}

// NewKeytabRotationGuard builds a guard with the given grace period.
func NewKeytabRotationGuard(cfg KeytabConfig) *KeytabRotationGuard {
	return &KeytabRotationGuard{cfg: cfg}
}

// Install adds a new generation to the keytab of one principal. A fresh key is
// staged and only usable after Promote.
func (g *KeytabRotationGuard) Install(entry KeyEntry, at time.Time) (InstallResult, error) {
	return InstallResult{}, errNotImplemented
}

// Promote makes one staged version the active key.
func (g *KeytabRotationGuard) Promote(principal string, version int, at time.Time) error {
	return errNotImplemented
}

// Active returns the active key of one principal.
func (g *KeytabRotationGuard) Active(principal string) (KeyEntry, error) {
	return KeyEntry{}, errNotImplemented
}

// Retire marks one superseded version as retired once its grace period has
// elapsed.
func (g *KeytabRotationGuard) Retire(principal string, version int, at time.Time) error {
	return errNotImplemented
}

// Verify checks a ticket against the keytab, newest usable key first.
func (g *KeytabRotationGuard) Verify(t Ticket) (KeyUse, error) {
	return KeyUse{}, errNotImplemented
}

// Entries lists the keytab of one principal, newest version first and then by
// encryption type in ascending order.
func (g *KeytabRotationGuard) Entries(principal string) []KeyEntry {
	return nil
}

// Stats returns the guard wide counters.
func (g *KeytabRotationGuard) Stats() KeytabStats {
	return KeytabStats{}
}

// Generation is the monotonic counter of keytab mutations.
func (g *KeytabRotationGuard) Generation() uint64 {
	return 0
}

// WriteFile replaces the on disk keytab with the current table atomically.
func (g *KeytabRotationGuard) WriteFile(path string) error {
	return errNotImplemented
}

// LoadFile replaces the in memory table with the keytab stored on disk.
func (g *KeytabRotationGuard) LoadFile(path string) error {
	return errNotImplemented
}

// Snapshot serialises the keytab, the counters and the generation watermark.
func (g *KeytabRotationGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole keytab with a snapshot, all or nothing.
func (g *KeytabRotationGuard) Restore(data []byte) error {
	return errNotImplemented
}

// Seal derives the checksum a ticket carries for one payload under one key.
func Seal(material, payload string) string {
	return ""
}
