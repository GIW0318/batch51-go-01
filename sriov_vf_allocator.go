// Package task allocates SR-IOV virtual functions of a physical NIC and keeps
// each function's tenant, MAC address, VLAN list and trust bit consistent
// across reconfiguration and restarts.
package task

import "errors"

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Virtual function lifecycle states.
const (
	VFIdle           = "idle"
	VFLeased         = "leased"
	VFConfigured     = "configured"
	VFPendingReclaim = "pending_reclaim"
)

// Errors returned by the allocator.
var (
	ErrBadRequest    = errors.New("vf request is invalid")
	ErrBadCount      = errors.New("vf count out of range")
	ErrUnknownPF     = errors.New("physical function is not enabled")
	ErrNoFreeVF      = errors.New("no free vf on this pf")
	ErrVFNotFound    = errors.New("vf not found")
	ErrVFNotLeased   = errors.New("vf is not leased")
	ErrTenantQuota   = errors.New("tenant vf quota exceeded")
	ErrTrustRequired = errors.New("trust mode is required for this change")
	ErrMacConflict   = errors.New("mac address collides with the pf address")
	ErrVlanLimit     = errors.New("vlan list exceeds the per vf limit")
	ErrShrinkBlocked = errors.New("cannot shrink while vfs are leased")
	ErrGuestBound    = errors.New("vf is bound to a guest driver")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// AllocatorConfig bounds the pool. PfMacs maps a physical function name to its
// own hardware address, which no virtual function may take.
type AllocatorConfig struct {
	MaxVfsPerPF     int
	MaxVfsPerTenant int
	MaxVlansPerVf   int
	PfMacs          map[string]string
}

// VF is one virtual function slot of a physical function.
type VF struct {
	ID         string `json:"id"`
	PF         string `json:"pf"`
	Index      int    `json:"index"`
	State      string `json:"state"`
	Generation int64  `json:"generation"`
}

// VFRequest is what a tenant asks for when it borrows a virtual function.
type VFRequest struct {
	Tenant string   `json:"tenant"`
	MAC    string   `json:"mac"`
	VLANs  []uint16 `json:"vlans"`
	Trust  bool     `json:"trust"`
}

// VFLease is the tenant visible view of a borrowed function.
type VFLease struct {
	VFID       string   `json:"vf_id"`
	PF         string   `json:"pf"`
	Tenant     string   `json:"tenant"`
	MAC        string   `json:"mac"`
	VLANs      []uint16 `json:"vlans"`
	Trust      bool     `json:"trust"`
	Generation int64    `json:"generation"`
}

// VFConfig is one reconfiguration of an already leased function.
type VFConfig struct {
	MAC      string   `json:"mac"`
	VLANs    []uint16 `json:"vlans"`
	SpoofChk bool     `json:"spoofchk"`
}

// VFState is the full operator view of one function.
type VFState struct {
	VFID       string   `json:"vf_id"`
	PF         string   `json:"pf"`
	Tenant     string   `json:"tenant"`
	State      string   `json:"state"`
	MAC        string   `json:"mac"`
	VLANs      []uint16 `json:"vlans"`
	Trust      bool     `json:"trust"`
	SpoofChk   bool     `json:"spoofchk"`
	Generation int64    `json:"generation"`
	GuestID    string   `json:"guest_id"`
}

// SriovVfAllocator owns the per PF pool, the tenant leases and the counters.
type SriovVfAllocator struct {
	cfg AllocatorConfig
}

// NewSriovVfAllocator builds an allocator with the given caps.
func NewSriovVfAllocator(cfg AllocatorConfig) *SriovVfAllocator {
	return &SriovVfAllocator{cfg: cfg}
}

// Enable creates or grows the virtual function pool of one physical function.
func (a *SriovVfAllocator) Enable(pf string, count int) ([]VF, error) {
	return nil, errNotImplemented
}

// Allocate leases the lowest free function of a physical function.
func (a *SriovVfAllocator) Allocate(pf string, req VFRequest) (VFLease, error) {
	return VFLease{}, errNotImplemented
}

// Configure rewrites the MAC, VLAN list and spoof check of a leased function.
func (a *SriovVfAllocator) Configure(vfID string, cfg VFConfig) (VFState, error) {
	return VFState{}, errNotImplemented
}

// SetTrust flips the trust bit of a leased function.
func (a *SriovVfAllocator) SetTrust(vfID string, trust bool) error {
	return errNotImplemented
}

// AttachGuest records that a guest driver bound the function.
func (a *SriovVfAllocator) AttachGuest(vfID, guestID string) (VFState, error) {
	return VFState{}, errNotImplemented
}

// DetachGuest records that the guest driver released the function.
func (a *SriovVfAllocator) DetachGuest(vfID string) error {
	return errNotImplemented
}

// CompleteReclaim resets a function whose guest driver is gone and returns it
// to the free pool.
func (a *SriovVfAllocator) CompleteReclaim(vfID string) (VFState, error) {
	return VFState{}, errNotImplemented
}

// Release gives a function back to the pool.
func (a *SriovVfAllocator) Release(vfID string) error {
	return errNotImplemented
}

// LeaseOf reads the lease of one function.
func (a *SriovVfAllocator) LeaseOf(vfID string) (VFLease, bool) {
	return VFLease{}, false
}

// Vfs lists the functions of one physical function ordered by index.
func (a *SriovVfAllocator) Vfs(pf string) []VF {
	return nil
}

// FreeCount reports how many functions of one physical function are idle.
func (a *SriovVfAllocator) FreeCount(pf string) int {
	return 0
}

// Snapshot serialises the pool, the leases and the generation counters.
func (a *SriovVfAllocator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *SriovVfAllocator) Restore(data []byte) error {
	return errNotImplemented
}
