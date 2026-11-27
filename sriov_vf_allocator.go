// Package task allocates SR-IOV virtual functions of a physical NIC and keeps
// each function's tenant, MAC address, VLAN list and trust bit consistent
// across reconfiguration and restarts.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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

type vfRecord struct {
	ID         string
	PF         string
	Index      int
	State      string
	Tenant     string
	MAC        string
	VLANs      []uint16
	Trust      bool
	SpoofChk   bool
	Generation int64
	GuestID    string
}

type pfPool struct {
	vfs []*vfRecord
}

// SriovVfAllocator owns the per PF pool, the tenant leases and the counters.
type SriovVfAllocator struct {
	mu  sync.Mutex
	cfg AllocatorConfig
	pfs map[string]*pfPool
}

const snapshotVersion = 1

type snapshotData struct {
	Version int           `json:"version"`
	PFs     *[]snapshotPF `json:"pfs"`
}

type snapshotPF struct {
	Name string       `json:"name"`
	VFs  []snapshotVF `json:"vfs"`
}

type snapshotVF struct {
	ID         string   `json:"id"`
	PF         string   `json:"pf"`
	Index      int      `json:"index"`
	State      string   `json:"state"`
	Tenant     string   `json:"tenant"`
	MAC        string   `json:"mac"`
	VLANs      []uint16 `json:"vlans"`
	Trust      bool     `json:"trust"`
	SpoofChk   bool     `json:"spoofchk"`
	Generation int64    `json:"generation"`
	GuestID    string   `json:"guest_id"`
}

// NewSriovVfAllocator builds an allocator with the given caps.
func NewSriovVfAllocator(cfg AllocatorConfig) *SriovVfAllocator {
	cfgCopy := cfg
	if cfg.PfMacs != nil {
		cfgCopy.PfMacs = make(map[string]string, len(cfg.PfMacs))
		for pf, mac := range cfg.PfMacs {
			cfgCopy.PfMacs[strings.TrimSpace(pf)] = strings.TrimSpace(mac)
		}
	}
	return &SriovVfAllocator{
		cfg: cfgCopy,
		pfs: make(map[string]*pfPool),
	}
}

// Enable creates or grows the virtual function pool of one physical function.
func (a *SriovVfAllocator) Enable(pf string, count int) ([]VF, error) {
	name := strings.TrimSpace(pf)
	if name == "" {
		return nil, ErrBadRequest
	}
	if count < 1 || count > a.cfg.MaxVfsPerPF {
		return nil, ErrBadCount
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	pool, ok := a.pfs[name]
	if !ok {
		pool = &pfPool{}
		a.pfs[name] = pool
	}

	current := len(pool.vfs)
	switch {
	case count < current:
		for i := count; i < current; i++ {
			if pool.vfs[i].State != VFIdle {
				return nil, ErrShrinkBlocked
			}
		}
		pool.vfs = pool.vfs[:count]
	case count > current:
		for i := current; i < count; i++ {
			pool.vfs = append(pool.vfs, &vfRecord{
				ID:    fmt.Sprintf("%s:vf%d", name, i),
				PF:    name,
				Index: i,
				State: VFIdle,
			})
		}
	}

	return vfList(pool), nil
}

// Allocate leases the lowest free function of a physical function.
func (a *SriovVfAllocator) Allocate(pf string, req VFRequest) (VFLease, error) {
	name := strings.TrimSpace(pf)
	tenant := strings.TrimSpace(req.Tenant)
	mac := strings.TrimSpace(req.MAC)

	a.mu.Lock()
	defer a.mu.Unlock()

	pool, ok := a.pfs[name]
	if !ok {
		return VFLease{}, ErrUnknownPF
	}
	if tenant == "" {
		return VFLease{}, ErrBadRequest
	}
	if err := validateVLANs(req.VLANs, a.cfg.MaxVlansPerVf); err != nil {
		return VFLease{}, err
	}
	if mac != "" && a.macCollidesWithPF(name, mac) {
		return VFLease{}, ErrMacConflict
	}

	var free *vfRecord
	for _, vf := range pool.vfs {
		if vf.State == VFIdle {
			free = vf
			break
		}
	}
	if free == nil {
		return VFLease{}, ErrNoFreeVF
	}
	if a.tenantLeaseCount(tenant) >= a.cfg.MaxVfsPerTenant {
		return VFLease{}, ErrTenantQuota
	}

	free.State = VFLeased
	free.Tenant = tenant
	free.MAC = mac
	free.VLANs = cloneVLANs(req.VLANs)
	free.Trust = req.Trust
	free.SpoofChk = false
	free.GuestID = ""
	free.Generation++

	return leaseOf(free), nil
}

// Configure rewrites the MAC, VLAN list and spoof check of a leased function.
func (a *SriovVfAllocator) Configure(vfID string, cfg VFConfig) (VFState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	vf, err := a.findVF(vfID)
	if err != nil {
		return VFState{}, err
	}
	if vf.State == VFIdle {
		return VFState{}, ErrVFNotLeased
	}

	vlansChanged := cfg.VLANs != nil
	if vlansChanged {
		if err := validateVLANs(cfg.VLANs, a.cfg.MaxVlansPerVf); err != nil {
			return VFState{}, err
		}
	}

	nextMAC := strings.TrimSpace(cfg.MAC)
	if nextMAC != "" && a.macCollidesWithPF(vf.PF, nextMAC) {
		return VFState{}, ErrMacConflict
	}
	if !vf.Trust {
		if nextMAC != "" && !sameMAC(nextMAC, vf.MAC) {
			return VFState{}, ErrTrustRequired
		}
		if vlansChanged && !sameVLANs(cfg.VLANs, vf.VLANs) {
			return VFState{}, ErrTrustRequired
		}
	}

	if nextMAC != "" {
		vf.MAC = nextMAC
	}
	if vlansChanged {
		vf.VLANs = cloneVLANs(cfg.VLANs)
	}
	vf.SpoofChk = cfg.SpoofChk
	vf.State = VFConfigured
	vf.Generation++

	return stateOf(vf), nil
}

// SetTrust flips the trust bit of a leased function.
func (a *SriovVfAllocator) SetTrust(vfID string, trust bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	vf, err := a.findVF(vfID)
	if err != nil {
		return err
	}
	if vf.State == VFIdle {
		return ErrVFNotLeased
	}

	vf.Trust = trust
	vf.Generation++
	return nil
}

// AttachGuest records that a guest driver bound the function.
func (a *SriovVfAllocator) AttachGuest(vfID, guestID string) (VFState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	vf, err := a.findVF(vfID)
	if err != nil {
		return VFState{}, err
	}
	if vf.State != VFLeased && vf.State != VFConfigured {
		return VFState{}, ErrVFNotLeased
	}

	guest := strings.TrimSpace(guestID)
	if guest == "" {
		return VFState{}, ErrBadRequest
	}
	if vf.GuestID != "" {
		return VFState{}, ErrGuestBound
	}

	vf.GuestID = guest
	vf.Generation++
	return stateOf(vf), nil
}

// DetachGuest records that the guest driver released the function.
func (a *SriovVfAllocator) DetachGuest(vfID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	vf, err := a.findVF(vfID)
	if err != nil {
		return err
	}
	if vf.State == VFIdle {
		return ErrVFNotLeased
	}
	if vf.GuestID == "" {
		return nil
	}

	vf.GuestID = ""
	vf.Generation++
	return nil
}

// CompleteReclaim resets a function whose guest driver is gone and returns it
// to the free pool.
func (a *SriovVfAllocator) CompleteReclaim(vfID string) (VFState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	vf, err := a.findVF(vfID)
	if err != nil {
		return VFState{}, err
	}
	if vf.State != VFPendingReclaim {
		return VFState{}, ErrVFNotLeased
	}
	if vf.GuestID != "" {
		return VFState{}, ErrGuestBound
	}

	resetIdle(vf)
	return stateOf(vf), nil
}

// Release gives a function back to the pool.
func (a *SriovVfAllocator) Release(vfID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	vf, err := a.findVF(vfID)
	if err != nil {
		return err
	}
	if vf.State == VFIdle {
		return ErrVFNotLeased
	}
	if vf.State == VFPendingReclaim {
		return nil
	}
	if vf.GuestID != "" {
		vf.State = VFPendingReclaim
		vf.Generation++
		return nil
	}

	resetIdle(vf)
	return nil
}

// LeaseOf reads the lease of one function.
func (a *SriovVfAllocator) LeaseOf(vfID string) (VFLease, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	vf, err := a.findVF(vfID)
	if err != nil || vf.State == VFIdle {
		return VFLease{}, false
	}
	return leaseOf(vf), true
}

// Vfs lists the functions of one physical function ordered by index.
func (a *SriovVfAllocator) Vfs(pf string) []VF {
	a.mu.Lock()
	defer a.mu.Unlock()

	pool, ok := a.pfs[strings.TrimSpace(pf)]
	if !ok {
		return []VF{}
	}
	return vfList(pool)
}

// FreeCount reports how many functions of one physical function are idle.
func (a *SriovVfAllocator) FreeCount(pf string) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	pool, ok := a.pfs[strings.TrimSpace(pf)]
	if !ok {
		return 0
	}
	free := 0
	for _, vf := range pool.vfs {
		if vf.State == VFIdle {
			free++
		}
	}
	return free
}

// Snapshot serialises the pool, the leases and the generation counters.
func (a *SriovVfAllocator) Snapshot() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	names := make([]string, 0, len(a.pfs))
	for name := range a.pfs {
		names = append(names, name)
	}
	sort.Strings(names)

	pfs := make([]snapshotPF, 0, len(names))
	for _, name := range names {
		pool := a.pfs[name]
		out := snapshotPF{
			Name: name,
			VFs:  make([]snapshotVF, 0, len(pool.vfs)),
		}
		for _, vf := range pool.vfs {
			out.VFs = append(out.VFs, snapshotVF{
				ID:         vf.ID,
				PF:         vf.PF,
				Index:      vf.Index,
				State:      vf.State,
				Tenant:     vf.Tenant,
				MAC:        vf.MAC,
				VLANs:      cloneVLANs(vf.VLANs),
				Trust:      vf.Trust,
				SpoofChk:   vf.SpoofChk,
				Generation: vf.Generation,
				GuestID:    vf.GuestID,
			})
		}
		pfs = append(pfs, out)
	}

	data := snapshotData{Version: snapshotVersion, PFs: &pfs}
	return json.Marshal(data)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *SriovVfAllocator) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var snap snapshotData
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion || snap.PFs == nil {
		return ErrBadSnapshot
	}

	next := make(map[string]*pfPool, len(*snap.PFs))
	seenIDs := make(map[string]struct{})
	for _, savedPF := range *snap.PFs {
		name := strings.TrimSpace(savedPF.Name)
		if name == "" || name != savedPF.Name {
			return ErrBadSnapshot
		}
		if _, exists := next[name]; exists {
			return ErrBadSnapshot
		}
		if len(savedPF.VFs) < 1 || len(savedPF.VFs) > a.cfg.MaxVfsPerPF {
			return ErrBadSnapshot
		}

		pool := &pfPool{vfs: make([]*vfRecord, 0, len(savedPF.VFs))}
		for i, savedVF := range savedPF.VFs {
			if savedVF.Index != i ||
				savedVF.PF != name ||
				savedVF.ID != fmt.Sprintf("%s:vf%d", name, i) ||
				savedVF.Generation < 0 {
				return ErrBadSnapshot
			}
			if _, exists := seenIDs[savedVF.ID]; exists {
				return ErrBadSnapshot
			}
			if err := validateVLANs(savedVF.VLANs, a.cfg.MaxVlansPerVf); err != nil {
				return ErrBadSnapshot
			}
			if err := a.validateSnapshotVF(name, savedVF); err != nil {
				return ErrBadSnapshot
			}
			seenIDs[savedVF.ID] = struct{}{}
			pool.vfs = append(pool.vfs, &vfRecord{
				ID:         savedVF.ID,
				PF:         savedVF.PF,
				Index:      savedVF.Index,
				State:      savedVF.State,
				Tenant:     savedVF.Tenant,
				MAC:        savedVF.MAC,
				VLANs:      cloneVLANs(savedVF.VLANs),
				Trust:      savedVF.Trust,
				SpoofChk:   savedVF.SpoofChk,
				Generation: savedVF.Generation,
				GuestID:    savedVF.GuestID,
			})
		}
		next[name] = pool
	}

	a.mu.Lock()
	a.pfs = next
	a.mu.Unlock()
	return nil
}

func (a *SriovVfAllocator) findVF(vfID string) (*vfRecord, error) {
	id := strings.TrimSpace(vfID)
	for _, pool := range a.pfs {
		for _, vf := range pool.vfs {
			if vf.ID == id {
				return vf, nil
			}
		}
	}
	return nil, ErrVFNotFound
}

func (a *SriovVfAllocator) tenantLeaseCount(tenant string) int {
	count := 0
	for _, pool := range a.pfs {
		for _, vf := range pool.vfs {
			if vf.State != VFIdle && vf.Tenant == tenant {
				count++
			}
		}
	}
	return count
}

func (a *SriovVfAllocator) macCollidesWithPF(pf, mac string) bool {
	pfMAC, ok := a.cfg.PfMacs[pf]
	return ok && mac != "" && sameMAC(mac, pfMAC)
}

func (a *SriovVfAllocator) validateSnapshotVF(pf string, vf snapshotVF) error {
	switch vf.State {
	case VFIdle:
		if vf.Tenant != "" || vf.MAC != "" || len(vf.VLANs) != 0 ||
			vf.Trust || vf.SpoofChk || vf.GuestID != "" {
			return ErrBadSnapshot
		}
	case VFLeased, VFConfigured, VFPendingReclaim:
		if strings.TrimSpace(vf.Tenant) == "" || strings.TrimSpace(vf.Tenant) != vf.Tenant {
			return ErrBadSnapshot
		}
	default:
		return ErrBadSnapshot
	}
	if strings.TrimSpace(vf.MAC) != vf.MAC || strings.TrimSpace(vf.GuestID) != vf.GuestID {
		return ErrBadSnapshot
	}
	if a.macCollidesWithPF(pf, vf.MAC) {
		return ErrBadSnapshot
	}
	return nil
}

func validateVLANs(vlans []uint16, max int) error {
	if len(vlans) > max {
		return ErrVlanLimit
	}
	seen := make(map[uint16]struct{}, len(vlans))
	for _, vlan := range vlans {
		if _, ok := seen[vlan]; ok {
			return ErrBadRequest
		}
		seen[vlan] = struct{}{}
	}
	return nil
}

func sameMAC(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func sameVLANs(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func cloneVLANs(vlans []uint16) []uint16 {
	if vlans == nil {
		return nil
	}
	out := make([]uint16, len(vlans))
	copy(out, vlans)
	return out
}

func resetIdle(vf *vfRecord) {
	vf.State = VFIdle
	vf.Tenant = ""
	vf.MAC = ""
	vf.VLANs = nil
	vf.Trust = false
	vf.SpoofChk = false
	vf.GuestID = ""
	vf.Generation++
}

func vfList(pool *pfPool) []VF {
	out := make([]VF, 0, len(pool.vfs))
	for _, vf := range pool.vfs {
		out = append(out, VF{
			ID:         vf.ID,
			PF:         vf.PF,
			Index:      vf.Index,
			State:      vf.State,
			Generation: vf.Generation,
		})
	}
	return out
}

func leaseOf(vf *vfRecord) VFLease {
	return VFLease{
		VFID:       vf.ID,
		PF:         vf.PF,
		Tenant:     vf.Tenant,
		MAC:        vf.MAC,
		VLANs:      cloneVLANs(vf.VLANs),
		Trust:      vf.Trust,
		Generation: vf.Generation,
	}
}

func stateOf(vf *vfRecord) VFState {
	return VFState{
		VFID:       vf.ID,
		PF:         vf.PF,
		Tenant:     vf.Tenant,
		State:      vf.State,
		MAC:        vf.MAC,
		VLANs:      cloneVLANs(vf.VLANs),
		Trust:      vf.Trust,
		SpoofChk:   vf.SpoofChk,
		Generation: vf.Generation,
		GuestID:    vf.GuestID,
	}
}
