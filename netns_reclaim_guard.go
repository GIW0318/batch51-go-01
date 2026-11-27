// Package task tracks the holders of every network namespace and reclaims
// the ones whose last holder went away and whose kernel objects are gone.
package task

import "errors"

// errNotImplemented is returned by behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Kinds of holder a network namespace can have.
const (
	HolderProcess = "process"
	HolderFD      = "fd"
	HolderMount   = "mount"
)

// States of a registered network namespace.
const (
	NetnsLive      = "live"
	NetnsPending   = "pending"
	NetnsReclaimed = "reclaimed"
)

// Errors reported by the guard.
var (
	ErrInvalidID         = errors.New("namespace id is not usable")
	ErrAlreadyRegistered = errors.New("namespace already registered")
	ErrNamespaceLimit    = errors.New("namespace limit reached")
	ErrUnknownNamespace  = errors.New("unknown namespace")
	ErrInvalidHolder     = errors.New("holder is not usable")
	ErrHolderLimit       = errors.New("holder limit reached")
	ErrUnknownHolder     = errors.New("unknown holder")
	ErrNotPinned         = errors.New("namespace is not pinned")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Holder is one process, descriptor or mount that keeps a namespace alive.
type Holder struct {
	Kind  string
	Key   string
	Inode uint64
}

// NetnsInfo is the registry view of one network namespace.
type NetnsInfo struct {
	ID         string
	Inode      uint64
	Refs       int
	Pinned     bool
	State      string
	Tombstone  bool
	Generation int64
}

// SweepReport is the outcome of one pass over the reclaim queue.
type SweepReport struct {
	Reclaimed []string
	Deferred  []string
	Failed    []string
}

// ReclaimConfig carries the hard bounds of the registry.
type ReclaimConfig struct {
	MaxNamespaces      int
	MaxHoldersPerNetns int
	CleanupAttempts    int
}

// NetnsReclaimGuard owns the registry, the holder tables and the queue.
type NetnsReclaimGuard struct {
	cfg ReclaimConfig
}

// NewNetnsReclaimGuard builds a guard around the given config.
func NewNetnsReclaimGuard(cfg ReclaimConfig) *NetnsReclaimGuard {
	return &NetnsReclaimGuard{cfg: cfg}
}

// Register adds one network namespace to the registry.
func (g *NetnsReclaimGuard) Register(id string, inode uint64) (NetnsInfo, error) {
	return NetnsInfo{}, errNotImplemented
}

// Acquire records one holder of the namespace.
func (g *NetnsReclaimGuard) Acquire(id, kind, key string, inode uint64) error {
	return errNotImplemented
}

// Release drops one holder of the namespace.
func (g *NetnsReclaimGuard) Release(id, kind, key string) error {
	return errNotImplemented
}

// Pin keeps a namespace out of the reclaim queue.
func (g *NetnsReclaimGuard) Pin(id string) error {
	return errNotImplemented
}

// Unpin allows a namespace with no holders to be reclaimed again.
func (g *NetnsReclaimGuard) Unpin(id string) error {
	return errNotImplemented
}

// Sweep walks the reclaim queue and reclaims the namespaces that are clean.
func (g *NetnsReclaimGuard) Sweep() (SweepReport, error) {
	return SweepReport{}, errNotImplemented
}

// Info reads one registered namespace.
func (g *NetnsReclaimGuard) Info(id string) (NetnsInfo, bool) {
	return NetnsInfo{}, false
}

// Pending lists the namespaces waiting for reclamation, ordered by id.
func (g *NetnsReclaimGuard) Pending() []string {
	return nil
}

// SetCleanup records whether the veth pair, the routes and the rules are gone.
func (g *NetnsReclaimGuard) SetCleanup(id string, veth, routes, rules bool) error {
	return errNotImplemented
}

// MarkExternalMount records that an outside process still has the file mounted.
func (g *NetnsReclaimGuard) MarkExternalMount(id string, present bool) error {
	return errNotImplemented
}

// Snapshot serialises the registry, the holders and the reclaim queue.
func (g *NetnsReclaimGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole registry with a snapshot, all or nothing.
func (g *NetnsReclaimGuard) Restore(data []byte) error {
	return errNotImplemented
}
