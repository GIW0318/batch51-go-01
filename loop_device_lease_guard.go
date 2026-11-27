// Package task governs loop device leases: which backing file a device is
// bound to, when a device may be shared read only, how reference counts keep a
// detached device alive, and how leases survive a restart.
package task

import (
	"errors"
	"sync"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Lease states reported by List and Lease.
const (
	LeaseActive   = "active"
	LeasePending  = "pending_release"
	LeaseReleased = "released"
)

// Device states kept in the allocation table.
const (
	DeviceInUse   = "in_use"
	DevicePending = "pending_release"
)

// Event kinds returned by Reconcile.
const (
	EventReclaimed = "reclaimed"
	EventKept      = "kept"
)

// Reason codes attached to lease events.
const (
	ReasonHolderGone = "holder_gone"
	ReasonHolderLive = "holder_alive"
)

// DefaultLeaseRefs is used when a request does not carry a reference count.
const DefaultLeaseRefs = 1

// Errors returned by the guard.
var (
	ErrBlankHolder   = errors.New("lease holder is required")
	ErrBlankBacking  = errors.New("backing file path is required")
	ErrUnknownLease  = errors.New("unknown lease")
	ErrLeaseReleased = errors.New("lease is already released")
	ErrBackingBusy   = errors.New("backing file is already leased")
	ErrNoFreeDevice  = errors.New("no free loop device in the configured range")
	ErrBadOptions    = errors.New("loop options are invalid")
	ErrBadRefCount   = errors.New("reference count cannot go below zero")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// GuardConfig bounds the device pool and the reclaim policy.
type GuardConfig struct {
	DeviceBase  int
	DeviceCount int
	MaxLeases   int
	StaleAfter  time.Duration
	BlockSizes  []int
}

// AttachRequest is one attach call: who holds the device, how it may be used
// and how many mount references come with it.
type AttachRequest struct {
	Holder    string
	ReadOnly  bool
	AutoClear bool
	Refs      int
	At        time.Time
}

// LoopOptions are the mutable flags of a live lease.
type LoopOptions struct {
	ReadOnly  bool
	AutoClear bool
	BlockSize int
}

// BackingInfo is the file handle identity of a backing file. The inode stays
// stable for one generation of the file and changes when the file is replaced.
type BackingInfo struct {
	Path       string `json:"path"`
	Inode      uint64 `json:"inode"`
	Generation uint64 `json:"generation"`
	SizeBytes  int64  `json:"size_bytes"`
}

// Lease is the record handed back by Attach.
type Lease struct {
	ID         string    `json:"id"`
	Device     int       `json:"device"`
	Backing    string    `json:"backing"`
	Inode      uint64    `json:"inode"`
	ReadOnly   bool      `json:"read_only"`
	AutoClear  bool      `json:"auto_clear"`
	BlockSize  int       `json:"block_size"`
	Refs       int       `json:"refs"`
	State      string    `json:"state"`
	Holder     string    `json:"holder"`
	Generation uint64    `json:"generation"`
	AttachedAt time.Time `json:"attached_at"`
}

// LeaseView is the read only projection returned by List and Lease.
type LeaseView struct {
	ID         string    `json:"id"`
	Device     int       `json:"device"`
	Backing    string    `json:"backing"`
	Inode      uint64    `json:"inode"`
	ReadOnly   bool      `json:"read_only"`
	AutoClear  bool      `json:"auto_clear"`
	BlockSize  int       `json:"block_size"`
	Refs       int       `json:"refs"`
	State      string    `json:"state"`
	Holder     string    `json:"holder"`
	Generation uint64    `json:"generation"`
	AttachedAt time.Time `json:"attached_at"`
}

// LeaseEvent describes one reclaim decision.
type LeaseEvent struct {
	Kind    string `json:"kind"`
	LeaseID string `json:"lease_id"`
	Device  int    `json:"device"`
	Holder  string `json:"holder"`
	Reason  string `json:"reason"`
}

// deviceRecord is one allocated loop device.
type deviceRecord struct {
	Number    int      `json:"number"`
	Inode     uint64   `json:"inode"`
	Backing   string   `json:"backing"`
	ReadOnly  bool     `json:"read_only"`
	AutoClear bool     `json:"auto_clear"`
	Refs      int      `json:"refs"`
	State     string   `json:"state"`
	Leases    []string `json:"leases"`
}

// LoopDeviceLeaseGuard owns the device pool, the lease table and the backing
// file identity map.
type LoopDeviceLeaseGuard struct {
	mu       sync.Mutex
	cfg      GuardConfig
	nextID   int
	leases   map[string]*Lease
	devices  map[int]*deviceRecord
	backings map[string]*BackingInfo
	alive    map[string]bool
	events   []LeaseEvent
}

// NewLoopDeviceLeaseGuard builds a guard with the given pool bounds.
func NewLoopDeviceLeaseGuard(cfg GuardConfig) *LoopDeviceLeaseGuard {
	return &LoopDeviceLeaseGuard{cfg: cfg}
}

// Attach binds a backing file to a loop device and returns the lease.
func (g *LoopDeviceLeaseGuard) Attach(backing string, req AttachRequest) (Lease, error) {
	return Lease{}, errNotImplemented
}

// Detach drops one lease and releases the device when no reference is left.
func (g *LoopDeviceLeaseGuard) Detach(leaseID string) error {
	return errNotImplemented
}

// Reconfigure rewrites the mutable options of a live lease.
func (g *LoopDeviceLeaseGuard) Reconfigure(leaseID string, opts LoopOptions) error {
	return errNotImplemented
}

// List returns the live leases ordered by device number then lease id.
func (g *LoopDeviceLeaseGuard) List() []LeaseView {
	return nil
}

// Lease reads one lease view.
func (g *LoopDeviceLeaseGuard) Lease(leaseID string) (LeaseView, bool) {
	return LeaseView{}, false
}

// Reconcile reclaims leases whose holder is gone and that outlived StaleAfter.
func (g *LoopDeviceLeaseGuard) Reconcile(now time.Time) ([]LeaseEvent, error) {
	return nil, errNotImplemented
}

// Backing resolves the current file handle of a backing path.
func (g *LoopDeviceLeaseGuard) Backing(path string) (BackingInfo, error) {
	return BackingInfo{}, errNotImplemented
}

// ReplaceBacking swaps the file behind a path and hands back the new handle.
func (g *LoopDeviceLeaseGuard) ReplaceBacking(path string) (BackingInfo, error) {
	return BackingInfo{}, errNotImplemented
}

// OpenRef takes one more mount reference on a live lease.
func (g *LoopDeviceLeaseGuard) OpenRef(leaseID string) (int, error) {
	return 0, errNotImplemented
}

// CloseRef drops one mount reference and returns the remaining count.
func (g *LoopDeviceLeaseGuard) CloseRef(leaseID string) (int, error) {
	return 0, errNotImplemented
}

// SetHolderAlive records whether a holder process is still running.
func (g *LoopDeviceLeaseGuard) SetHolderAlive(holder string, alive bool) {
}

// Events returns the reclaim history, oldest first.
func (g *LoopDeviceLeaseGuard) Events() []LeaseEvent {
	return nil
}

// Snapshot serialises the lease table, the device pool and the backing map.
func (g *LoopDeviceLeaseGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *LoopDeviceLeaseGuard) Restore(data []byte) error {
	return errNotImplemented
}
