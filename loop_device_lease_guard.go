// Package task governs loop device leases: which backing file a device is
// bound to, when a device may be shared read only, how reference counts keep a
// detached device alive, and how leases survive a restart.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

const snapshotVersion = 1

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
	ownRefs    int
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
	Number     int      `json:"number"`
	Inode      uint64   `json:"inode"`
	Backing    string   `json:"backing"`
	Generation uint64   `json:"generation"`
	ReadOnly   bool     `json:"read_only"`
	AutoClear  bool     `json:"auto_clear"`
	Refs       int      `json:"refs"`
	State      string   `json:"state"`
	Leases     []string `json:"leases"`
}

// LoopDeviceLeaseGuard owns the device pool, the lease table and the backing
// file identity map.
type LoopDeviceLeaseGuard struct {
	mu        sync.Mutex
	cfg       GuardConfig
	nextID    int
	nextInode uint64
	leases    map[string]*Lease
	devices   map[int]*deviceRecord
	backings  map[string]*BackingInfo
	alive     map[string]bool
	events    []LeaseEvent
}

// NewLoopDeviceLeaseGuard builds a guard with the given pool bounds.
func NewLoopDeviceLeaseGuard(cfg GuardConfig) *LoopDeviceLeaseGuard {
	cfg.BlockSizes = append([]int(nil), cfg.BlockSizes...)
	g := &LoopDeviceLeaseGuard{cfg: cfg}
	g.initLocked()
	return g
}

func (g *LoopDeviceLeaseGuard) initLocked() {
	if g.nextID <= 0 {
		g.nextID = 1
	}
	if g.nextInode == 0 {
		g.nextInode = 1
	}
	if g.leases == nil {
		g.leases = make(map[string]*Lease)
	}
	if g.devices == nil {
		g.devices = make(map[int]*deviceRecord)
	}
	if g.backings == nil {
		g.backings = make(map[string]*BackingInfo)
	}
	if g.alive == nil {
		g.alive = make(map[string]bool)
	}
}

func blank(s string) bool {
	return strings.TrimSpace(s) == ""
}

// Attach binds a backing file to a loop device and returns the lease.
func (g *LoopDeviceLeaseGuard) Attach(backing string, req AttachRequest) (Lease, error) {
	if blank(backing) {
		return Lease{}, ErrBlankBacking
	}
	if blank(req.Holder) {
		return Lease{}, ErrBlankHolder
	}
	if req.Refs < 0 {
		return Lease{}, ErrBadRefCount
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	info, known := g.lookupBackingLocked(backing)
	if !known {
		info = BackingInfo{Path: backing, Inode: g.nextInode, Generation: 1}
	}

	device := g.deviceForBackingLocked(info)
	if device != nil {
		if !req.ReadOnly || !device.ReadOnly || device.State != DeviceInUse {
			return Lease{}, ErrBackingBusy
		}
		if g.atLeaseCapLocked() {
			return Lease{}, ErrNoFreeDevice
		}
	} else if g.atLeaseCapLocked() || !g.canAllocateDeviceLocked() {
		return Lease{}, ErrNoFreeDevice
	}

	number := 0
	if device == nil {
		var ok bool
		number, ok = g.nextFreeDeviceLocked()
		if !ok {
			return Lease{}, ErrNoFreeDevice
		}
	} else {
		number = device.Number
	}

	id := leaseID(g.nextID)
	at := req.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if !known {
		backingCopy := info
		g.backings[backing] = &backingCopy
		g.nextInode++
	}

	if device == nil {
		device = &deviceRecord{
			Number:     number,
			Inode:      info.Inode,
			Backing:    backing,
			Generation: info.Generation,
			ReadOnly:   req.ReadOnly,
			AutoClear:  req.AutoClear,
			Refs:       req.Refs,
			State:      DeviceInUse,
			Leases:     []string{id},
		}
		g.devices[number] = device
	} else {
		device.Refs += req.Refs
		device.Leases = append(device.Leases, id)
		device.AutoClear = device.AutoClear || req.AutoClear
		g.refreshDeviceStateLocked(device)
	}

	lease := &Lease{
		ID:         id,
		Device:     number,
		Backing:    backing,
		Inode:      info.Inode,
		ReadOnly:   req.ReadOnly,
		AutoClear:  req.AutoClear,
		Refs:       device.Refs,
		State:      LeaseActive,
		Holder:     req.Holder,
		Generation: info.Generation,
		AttachedAt: at,
		ownRefs:    req.Refs,
	}
	g.leases[id] = lease
	g.nextID++
	g.syncDeviceLeaseRefsLocked(device)
	return *lease, nil
}

// Detach drops one lease and releases the device when no reference is left.
func (g *LoopDeviceLeaseGuard) Detach(leaseID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	lease, ok := g.leases[leaseID]
	if !ok {
		return ErrUnknownLease
	}
	if lease.State != LeaseActive {
		return ErrLeaseReleased
	}

	if lease.ownRefs == 0 {
		g.releaseLeaseLocked(lease, false)
		return nil
	}

	lease.State = LeasePending
	device := g.devices[lease.Device]
	if device != nil {
		g.syncDeviceLeaseRefsLocked(device)
		g.refreshDeviceStateLocked(device)
	}
	return nil
}

// Reconfigure rewrites the mutable options of a live lease.
func (g *LoopDeviceLeaseGuard) Reconfigure(leaseID string, opts LoopOptions) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	lease, ok := g.leases[leaseID]
	if !ok {
		return ErrUnknownLease
	}
	if lease.State != LeaseActive {
		return ErrLeaseReleased
	}
	if opts.BlockSize != 0 && !g.blockSizeAllowedLocked(opts.BlockSize) {
		return ErrBadOptions
	}

	device := g.devices[lease.Device]
	if !opts.ReadOnly && lease.ReadOnly && device != nil {
		liveOnDevice := 0
		for _, id := range device.Leases {
			other := g.leases[id]
			if other != nil && other.State != LeaseReleased {
				liveOnDevice++
			}
		}
		if liveOnDevice > 1 {
			return ErrBackingBusy
		}
	}

	lease.ReadOnly = opts.ReadOnly
	lease.AutoClear = opts.AutoClear
	if opts.BlockSize != 0 {
		lease.BlockSize = opts.BlockSize
	}
	if device != nil {
		g.refreshDeviceFlagsLocked(device)
		g.refreshDeviceStateLocked(device)
	}
	return nil
}

// List returns the live leases ordered by device number then lease id.
func (g *LoopDeviceLeaseGuard) List() []LeaseView {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	views := make([]LeaseView, 0, len(g.leases))
	for _, lease := range g.leases {
		if lease.State == LeaseReleased {
			continue
		}
		views = append(views, g.viewLocked(lease))
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Device != views[j].Device {
			return views[i].Device < views[j].Device
		}
		return leaseIDLess(views[i].ID, views[j].ID)
	})
	return views
}

// Lease reads one lease view.
func (g *LoopDeviceLeaseGuard) Lease(leaseID string) (LeaseView, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	lease, ok := g.leases[leaseID]
	if !ok {
		return LeaseView{}, false
	}
	return g.viewLocked(lease), true
}

// Reconcile reclaims leases whose holder is gone and that outlived StaleAfter.
func (g *LoopDeviceLeaseGuard) Reconcile(now time.Time) ([]LeaseEvent, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	cutoff := now.Add(-g.cfg.StaleAfter)
	ids := make([]string, 0, len(g.leases))
	for id, lease := range g.leases {
		if lease.State == LeaseReleased {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return leaseIDLess(ids[i], ids[j]) })

	events := make([]LeaseEvent, 0)
	for _, id := range ids {
		lease := g.leases[id]
		if lease == nil || lease.State == LeaseReleased {
			continue
		}
		alive, known := g.alive[lease.Holder]
		if !known || alive {
			continue
		}
		if !lease.AttachedAt.Before(cutoff) {
			continue
		}

		events = append(events, LeaseEvent{
			Kind:    EventReclaimed,
			LeaseID: lease.ID,
			Device:  lease.Device,
			Holder:  lease.Holder,
			Reason:  ReasonHolderGone,
		})
		g.releaseLeaseLocked(lease, true)
	}
	g.events = append(g.events, events...)
	return append([]LeaseEvent(nil), events...), nil
}

// Backing resolves the current file handle of a backing path.
func (g *LoopDeviceLeaseGuard) Backing(path string) (BackingInfo, error) {
	if blank(path) {
		return BackingInfo{}, ErrBlankBacking
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	if info, ok := g.lookupBackingLocked(path); ok {
		return info, nil
	}
	info := BackingInfo{Path: path, Inode: g.nextInode, Generation: 1}
	infoCopy := info
	g.backings[path] = &infoCopy
	g.nextInode++
	return info, nil
}

// ReplaceBacking swaps the file behind a path and hands back the new handle.
func (g *LoopDeviceLeaseGuard) ReplaceBacking(path string) (BackingInfo, error) {
	if blank(path) {
		return BackingInfo{}, ErrBlankBacking
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	generation := uint64(1)
	if old, ok := g.lookupBackingLocked(path); ok {
		generation = old.Generation + 1
	}
	info := BackingInfo{
		Path:       path,
		Inode:      g.nextInode,
		Generation: generation,
	}
	infoCopy := info
	g.backings[path] = &infoCopy
	g.nextInode++
	return info, nil
}

// OpenRef takes one more mount reference on a live lease.
func (g *LoopDeviceLeaseGuard) OpenRef(leaseID string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	lease, ok := g.leases[leaseID]
	if !ok {
		return 0, ErrUnknownLease
	}
	if lease.State == LeaseReleased {
		return 0, ErrLeaseReleased
	}
	device := g.devices[lease.Device]
	if device == nil {
		return 0, ErrLeaseReleased
	}

	lease.ownRefs++
	device.Refs++
	g.syncDeviceLeaseRefsLocked(device)
	g.refreshDeviceStateLocked(device)
	return device.Refs, nil
}

// CloseRef drops one mount reference and returns the remaining count.
func (g *LoopDeviceLeaseGuard) CloseRef(leaseID string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	lease, ok := g.leases[leaseID]
	if !ok {
		return 0, ErrUnknownLease
	}
	if lease.ownRefs <= 0 {
		return 0, ErrBadRefCount
	}
	device := g.devices[lease.Device]
	if device == nil || device.Refs <= 0 {
		return 0, ErrBadRefCount
	}

	lease.ownRefs--
	device.Refs--

	if lease.State == LeasePending && lease.ownRefs == 0 {
		remaining := device.Refs
		g.releaseLeaseLocked(lease, false)
		return remaining, nil
	}

	g.syncDeviceLeaseRefsLocked(device)
	g.refreshDeviceStateLocked(device)

	if device.Refs == 0 && device.AutoClear {
		g.releaseDeviceLeasesLocked(device)
		return 0, nil
	}
	return device.Refs, nil
}

// SetHolderAlive records whether a holder process is still running.
func (g *LoopDeviceLeaseGuard) SetHolderAlive(holder string, alive bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	g.alive[holder] = alive
}

// Events returns the reclaim history, oldest first.
func (g *LoopDeviceLeaseGuard) Events() []LeaseEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	return append([]LeaseEvent(nil), g.events...)
}

type snapshotLease struct {
	Lease
	OwnRefs int `json:"own_refs"`
}

type snapshotState struct {
	Config    GuardConfig     `json:"config"`
	NextID    int             `json:"next_lease_id"`
	NextInode uint64          `json:"next_inode"`
	Leases    []snapshotLease `json:"leases"`
	Devices   []deviceRecord  `json:"devices"`
	Backings  []BackingInfo   `json:"backings"`
	Alive     map[string]bool `json:"alive,omitempty"`
	Events    []LeaseEvent    `json:"events,omitempty"`
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

// Snapshot serialises the lease table, the device pool and the backing map.
func (g *LoopDeviceLeaseGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	state := snapshotState{
		Config:    g.cfg,
		NextID:    g.nextID,
		NextInode: g.nextInode,
	}
	state.Config.BlockSizes = append([]int(nil), g.cfg.BlockSizes...)

	ids := make([]string, 0, len(g.leases))
	for id := range g.leases {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return leaseIDLess(ids[i], ids[j]) })
	for _, id := range ids {
		lease := g.leases[id]
		state.Leases = append(state.Leases, snapshotLease{
			Lease:   *lease,
			OwnRefs: lease.ownRefs,
		})
	}

	numbers := make([]int, 0, len(g.devices))
	for number := range g.devices {
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	for _, number := range numbers {
		device := g.devices[number]
		copyDevice := *device
		copyDevice.Leases = append([]string(nil), device.Leases...)
		state.Devices = append(state.Devices, copyDevice)
	}

	paths := make([]string, 0, len(g.backings))
	for path := range g.backings {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		state.Backings = append(state.Backings, *g.backings[path])
	}

	if len(g.alive) > 0 {
		state.Alive = make(map[string]bool, len(g.alive))
		for holder, alive := range g.alive {
			state.Alive[holder] = alive
		}
	}
	state.Events = append([]LeaseEvent(nil), g.events...)

	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		State:    raw,
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *LoopDeviceLeaseGuard) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || len(envelope.State) == 0 {
		return ErrBadSnapshot
	}
	checksum, err := hex.DecodeString(envelope.Checksum)
	if err != nil || len(checksum) != sha256.Size {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.State)
	if !equalBytes(checksum, sum[:]) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.State, &state); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(&state); err != nil {
		return ErrBadSnapshot
	}

	cfg := state.Config
	cfg.BlockSizes = append([]int(nil), state.Config.BlockSizes...)
	leases := make(map[string]*Lease, len(state.Leases))
	for _, snapLease := range state.Leases {
		lease := snapLease.Lease
		lease.ownRefs = snapLease.OwnRefs
		leaseCopy := lease
		leases[lease.ID] = &leaseCopy
	}
	devices := make(map[int]*deviceRecord, len(state.Devices))
	for _, snapDevice := range state.Devices {
		device := snapDevice
		device.Leases = append([]string(nil), snapDevice.Leases...)
		deviceCopy := device
		devices[device.Number] = &deviceCopy
	}
	backings := make(map[string]*BackingInfo, len(state.Backings))
	for _, snapBacking := range state.Backings {
		backing := snapBacking
		backingCopy := backing
		backings[backing.Path] = &backingCopy
	}
	alive := make(map[string]bool, len(state.Alive))
	for holder, isAlive := range state.Alive {
		alive[holder] = isAlive
	}
	events := append([]LeaseEvent(nil), state.Events...)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	nextID := state.NextID
	if g.nextID > nextID {
		nextID = g.nextID
	}
	for _, lease := range state.Leases {
		if number, ok := leaseNumber(lease.ID); ok && number >= nextID {
			nextID = number + 1
		}
	}
	nextInode := state.NextInode
	if g.nextInode > nextInode {
		nextInode = g.nextInode
	}

	g.cfg = cfg
	g.nextID = nextID
	g.nextInode = nextInode
	g.leases = leases
	g.devices = devices
	g.backings = backings
	g.alive = alive
	g.events = events
	return nil
}

func (g *LoopDeviceLeaseGuard) lookupBackingLocked(path string) (BackingInfo, bool) {
	info, ok := g.backings[path]
	if !ok {
		return BackingInfo{}, false
	}
	return *info, true
}

func (g *LoopDeviceLeaseGuard) deviceForBackingLocked(info BackingInfo) *deviceRecord {
	for _, device := range g.devices {
		if device.Inode == info.Inode && device.Generation == info.Generation {
			return device
		}
	}
	return nil
}

func (g *LoopDeviceLeaseGuard) atLeaseCapLocked() bool {
	if g.cfg.MaxLeases <= 0 {
		return false
	}
	live := 0
	for _, lease := range g.leases {
		if lease.State != LeaseReleased {
			live++
		}
	}
	return live >= g.cfg.MaxLeases
}

func (g *LoopDeviceLeaseGuard) canAllocateDeviceLocked() bool {
	_, ok := g.nextFreeDeviceLocked()
	return ok
}

func (g *LoopDeviceLeaseGuard) nextFreeDeviceLocked() (int, bool) {
	if g.cfg.DeviceCount <= 0 {
		return 0, false
	}
	for offset := 0; offset < g.cfg.DeviceCount; offset++ {
		number := g.cfg.DeviceBase + offset
		if _, used := g.devices[number]; !used {
			return number, true
		}
	}
	return 0, false
}

func (g *LoopDeviceLeaseGuard) blockSizeAllowedLocked(size int) bool {
	for _, allowed := range g.cfg.BlockSizes {
		if allowed == size {
			return true
		}
	}
	return false
}

func (g *LoopDeviceLeaseGuard) viewLocked(lease *Lease) LeaseView {
	refs := lease.Refs
	if device := g.devices[lease.Device]; device != nil {
		refs = device.Refs
	}
	return LeaseView{
		ID:         lease.ID,
		Device:     lease.Device,
		Backing:    lease.Backing,
		Inode:      lease.Inode,
		ReadOnly:   lease.ReadOnly,
		AutoClear:  lease.AutoClear,
		BlockSize:  lease.BlockSize,
		Refs:       refs,
		State:      lease.State,
		Holder:     lease.Holder,
		Generation: lease.Generation,
		AttachedAt: lease.AttachedAt,
	}
}

func (g *LoopDeviceLeaseGuard) syncDeviceLeaseRefsLocked(device *deviceRecord) {
	if device == nil {
		return
	}
	for _, id := range device.Leases {
		lease := g.leases[id]
		if lease != nil && lease.State != LeaseReleased {
			lease.Refs = device.Refs
		}
	}
}

func (g *LoopDeviceLeaseGuard) releaseLeaseLocked(lease *Lease, dropOwnRefs bool) {
	if lease == nil || lease.State == LeaseReleased {
		return
	}
	device := g.devices[lease.Device]
	if dropOwnRefs && device != nil && lease.ownRefs > 0 {
		device.Refs -= lease.ownRefs
		if device.Refs < 0 {
			device.Refs = 0
		}
	}
	lease.ownRefs = 0
	lease.State = LeaseReleased
	if device != nil {
		device.Leases = removeLeaseID(device.Leases, lease.ID)
		if device.Refs == 0 && len(device.Leases) == 0 {
			delete(g.devices, device.Number)
			lease.Refs = 0
			return
		}
		lease.Refs = device.Refs
		g.syncDeviceLeaseRefsLocked(device)
		g.refreshDeviceFlagsLocked(device)
		g.refreshDeviceStateLocked(device)
		return
	}
	lease.Refs = 0
}

func (g *LoopDeviceLeaseGuard) releaseDeviceLeasesLocked(device *deviceRecord) {
	if device == nil {
		return
	}
	ids := append([]string(nil), device.Leases...)
	device.Leases = nil
	device.Refs = 0
	for _, id := range ids {
		lease := g.leases[id]
		if lease == nil || lease.State == LeaseReleased {
			continue
		}
		lease.ownRefs = 0
		lease.Refs = 0
		lease.State = LeaseReleased
	}
	delete(g.devices, device.Number)
}

func (g *LoopDeviceLeaseGuard) refreshDeviceStateLocked(device *deviceRecord) {
	if device == nil {
		return
	}
	active := false
	live := 0
	for _, id := range device.Leases {
		lease := g.leases[id]
		if lease == nil || lease.State == LeaseReleased {
			continue
		}
		live++
		if lease.State == LeaseActive {
			active = true
		}
	}
	if live == 0 && device.Refs == 0 {
		delete(g.devices, device.Number)
		return
	}
	if active {
		device.State = DeviceInUse
	} else {
		device.State = DevicePending
	}
}

func (g *LoopDeviceLeaseGuard) refreshDeviceFlagsLocked(device *deviceRecord) {
	if device == nil {
		return
	}
	first := true
	readOnly := true
	autoClear := false
	for _, id := range device.Leases {
		lease := g.leases[id]
		if lease == nil || lease.State == LeaseReleased {
			continue
		}
		if first {
			readOnly = lease.ReadOnly
			first = false
		} else {
			readOnly = readOnly && lease.ReadOnly
		}
		autoClear = autoClear || lease.AutoClear
	}
	if !first {
		device.ReadOnly = readOnly
		device.AutoClear = autoClear
	}
}

func removeLeaseID(ids []string, target string) []string {
	for i, id := range ids {
		if id == target {
			copy(ids[i:], ids[i+1:])
			return ids[:len(ids)-1]
		}
	}
	return ids
}

func leaseID(number int) string {
	raw := strconv.Itoa(number)
	if len(raw) < 10 {
		raw = strings.Repeat("0", 10-len(raw)) + raw
	}
	return "lease-" + raw
}

func leaseNumber(id string) (int, bool) {
	index := strings.LastIndexByte(id, '-')
	if index < 0 || index == len(id)-1 {
		return 0, false
	}
	number, err := strconv.Atoi(id[index+1:])
	if err != nil || number < 0 {
		return 0, false
	}
	return number, true
}

func leaseIDLess(left, right string) bool {
	leftNumber, leftOK := leaseNumber(left)
	rightNumber, rightOK := leaseNumber(right)
	if leftOK && rightOK {
		if leftNumber != rightNumber {
			return leftNumber < rightNumber
		}
		return left < right
	}
	return left < right
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for i := range left {
		diff |= left[i] ^ right[i]
	}
	return diff == 0
}

func validateSnapshot(state *snapshotState) error {
	if state.NextID < 1 || state.NextInode < 1 {
		return ErrBadSnapshot
	}
	leases := make(map[string]*snapshotLease, len(state.Leases))
	maxLeaseNumber := 0
	maxInode := uint64(0)
	for i := range state.Leases {
		lease := &state.Leases[i]
		if lease.ID == "" || lease.Holder == "" || lease.Backing == "" || lease.Inode == 0 {
			return ErrBadSnapshot
		}
		if _, duplicate := leases[lease.ID]; duplicate {
			return ErrBadSnapshot
		}
		switch lease.State {
		case LeaseActive, LeasePending, LeaseReleased:
		default:
			return ErrBadSnapshot
		}
		if lease.Refs < 0 || lease.OwnRefs < 0 {
			return ErrBadSnapshot
		}
		if lease.State == LeaseReleased && lease.OwnRefs != 0 {
			return ErrBadSnapshot
		}
		if lease.Inode > maxInode {
			maxInode = lease.Inode
		}
		if number, ok := leaseNumber(lease.ID); ok && number > maxLeaseNumber {
			maxLeaseNumber = number
		}
		leases[lease.ID] = lease
	}
	if state.NextID <= maxLeaseNumber {
		return ErrBadSnapshot
	}

	devices := make(map[int]*deviceRecord, len(state.Devices))
	for i := range state.Devices {
		device := &state.Devices[i]
		if _, duplicate := devices[device.Number]; duplicate {
			return ErrBadSnapshot
		}
		if device.Number < state.Config.DeviceBase ||
			device.Number >= state.Config.DeviceBase+state.Config.DeviceCount {
			return ErrBadSnapshot
		}
		if device.Inode == 0 || device.Backing == "" || device.Refs < 0 {
			return ErrBadSnapshot
		}
		switch device.State {
		case DeviceInUse, DevicePending:
		default:
			return ErrBadSnapshot
		}
		if device.Inode > maxInode {
			maxInode = device.Inode
		}
		seen := make(map[string]bool, len(device.Leases))
		ownRefs := 0
		active := false
		for _, id := range device.Leases {
			if seen[id] {
				return ErrBadSnapshot
			}
			seen[id] = true
			lease := leases[id]
			if lease == nil || lease.State == LeaseReleased {
				return ErrBadSnapshot
			}
			if lease.Device != device.Number ||
				lease.Inode != device.Inode ||
				lease.Generation != device.Generation ||
				lease.Backing != device.Backing {
				return ErrBadSnapshot
			}
			if lease.Refs != device.Refs {
				return ErrBadSnapshot
			}
			if lease.State == LeaseActive {
				active = true
			}
			ownRefs += lease.OwnRefs
		}
		if len(device.Leases) == 0 || ownRefs != device.Refs {
			return ErrBadSnapshot
		}
		expectedState := DevicePending
		if active {
			expectedState = DeviceInUse
		}
		if device.State != expectedState {
			return ErrBadSnapshot
		}
		devices[device.Number] = device
	}
	for _, lease := range leases {
		if lease.State == LeaseReleased {
			continue
		}
		device := devices[lease.Device]
		if device == nil {
			return ErrBadSnapshot
		}
		found := false
		for _, id := range device.Leases {
			if id == lease.ID {
				found = true
				break
			}
		}
		if !found {
			return ErrBadSnapshot
		}
	}

	paths := make(map[string]bool, len(state.Backings))
	inodes := make(map[uint64]bool, len(state.Backings))
	for _, backing := range state.Backings {
		if backing.Path == "" || backing.Inode == 0 || backing.Generation == 0 {
			return ErrBadSnapshot
		}
		if paths[backing.Path] || inodes[backing.Inode] {
			return ErrBadSnapshot
		}
		paths[backing.Path] = true
		inodes[backing.Inode] = true
		if backing.Inode > maxInode {
			maxInode = backing.Inode
		}
	}
	if state.NextInode <= maxInode {
		return ErrBadSnapshot
	}
	return nil
}
