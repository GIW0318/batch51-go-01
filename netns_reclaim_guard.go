// Package task tracks the holders of every network namespace and reclaims
// the ones whose last holder went away and whose kernel objects are gone.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

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
	ErrCleanupLimit      = errors.New("cleanup attempt limit reached")
)

const snapshotVersion = 1

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

type holderRef struct {
	kind string
	key  string
}

// holderGroup represents one distinct holder. A bind-mounted file can be seen
// under several keys but still has one inode and therefore one group.
type holderGroup struct {
	id    int64
	kind  string
	inode uint64
	keys  map[string]struct{}
}

type cleanupState struct {
	veth   bool
	routes bool
	rules  bool
}

func (c cleanupState) complete() bool {
	return c.veth && c.routes && c.rules
}

type netnsRecord struct {
	id           string
	inode        uint64
	groups       map[int64]*holderGroup
	holders      map[holderRef]*holderGroup
	mountGroups  map[uint64]int64
	nextGroupID  int64
	pinned       bool
	state        string
	tombstone    bool
	generation   int64
	cleanup      cleanupState
	external     bool
	cleanupTries int
}

func (n *netnsRecord) refs() int {
	refs := len(n.groups)
	if n.external {
		refs++
	}
	return refs
}

// NetnsReclaimGuard owns the registry, the holder tables and the queue.
type NetnsReclaimGuard struct {
	mu          sync.RWMutex
	cfg         ReclaimConfig
	namespaces  map[string]*netnsRecord
	reclaimable map[string]struct{}
}

// NewNetnsReclaimGuard builds a guard around the given config.
func NewNetnsReclaimGuard(cfg ReclaimConfig) *NetnsReclaimGuard {
	return &NetnsReclaimGuard{
		cfg:         cfg,
		namespaces:  make(map[string]*netnsRecord),
		reclaimable: make(map[string]struct{}),
	}
}

// Register adds one network namespace to the registry.
func (g *NetnsReclaimGuard) Register(id string, inode uint64) (NetnsInfo, error) {
	if strings.TrimSpace(id) == "" {
		return NetnsInfo{}, ErrInvalidID
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	if _, ok := g.namespaces[id]; ok {
		return NetnsInfo{}, ErrAlreadyRegistered
	}
	if len(g.namespaces) >= g.cfg.MaxNamespaces {
		return NetnsInfo{}, ErrNamespaceLimit
	}

	n := &netnsRecord{
		id:          id,
		inode:       inode,
		groups:      make(map[int64]*holderGroup),
		holders:     make(map[holderRef]*holderGroup),
		mountGroups: make(map[uint64]int64),
		state:       NetnsLive,
		generation:  1,
	}
	g.namespaces[id] = n
	return n.info(), nil
}

// Acquire records one holder of the namespace.
func (g *NetnsReclaimGuard) Acquire(id, kind, key string, inode uint64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	n, ok := g.namespaces[id]
	if !ok {
		return ErrUnknownNamespace
	}
	if !validHolderKind(kind) || strings.TrimSpace(key) == "" {
		return ErrInvalidHolder
	}

	ref := holderRef{kind: kind, key: key}
	if _, ok := n.holders[ref]; ok {
		return nil
	}

	if kind == HolderMount && inode != 0 {
		if groupID, ok := n.mountGroups[inode]; ok {
			group := n.groups[groupID]
			group.keys[key] = struct{}{}
			n.holders[ref] = group
			n.generation++
			g.makeLiveLocked(n)
			return nil
		}
	}

	if n.refs() >= g.cfg.MaxHoldersPerNetns {
		return ErrHolderLimit
	}

	n.nextGroupID++
	group := &holderGroup{
		id:    n.nextGroupID,
		kind:  kind,
		inode: inode,
		keys:  map[string]struct{}{key: {}},
	}
	n.groups[group.id] = group
	n.holders[ref] = group
	if kind == HolderMount && inode != 0 {
		n.mountGroups[inode] = group.id
	}
	n.generation++
	g.makeLiveLocked(n)
	return nil
}

// Release drops one holder of the namespace.
func (g *NetnsReclaimGuard) Release(id, kind, key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	n, ok := g.namespaces[id]
	if !ok {
		return ErrUnknownNamespace
	}

	ref := holderRef{kind: kind, key: key}
	group, ok := n.holders[ref]
	if !ok {
		return ErrUnknownHolder
	}

	delete(n.holders, ref)
	delete(group.keys, key)
	if len(group.keys) != 0 {
		// Another key still refers to the same bind-mounted inode.
		n.generation++
		return nil
	}

	delete(n.groups, group.id)
	if group.kind == HolderMount && group.inode != 0 && n.mountGroups[group.inode] == group.id {
		delete(n.mountGroups, group.inode)
	}

	n.generation++
	if n.refs() == 0 && !n.pinned {
		n.state = NetnsPending
		g.reclaimable[n.id] = struct{}{}
	} else {
		n.state = NetnsLive
		delete(g.reclaimable, n.id)
	}
	return nil
}

// Pin keeps a namespace out of the reclaim queue.
func (g *NetnsReclaimGuard) Pin(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	n, ok := g.namespaces[id]
	if !ok {
		return ErrUnknownNamespace
	}
	if n.pinned {
		return nil
	}
	n.pinned = true
	n.generation++
	g.makeLiveLocked(n)
	return nil
}

// Unpin allows a namespace with no holders to be reclaimed again.
func (g *NetnsReclaimGuard) Unpin(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	n, ok := g.namespaces[id]
	if !ok {
		return ErrUnknownNamespace
	}
	if !n.pinned {
		return ErrNotPinned
	}
	n.pinned = false
	n.generation++
	if n.refs() == 0 {
		n.state = NetnsPending
		g.reclaimable[n.id] = struct{}{}
	} else {
		n.state = NetnsLive
		delete(g.reclaimable, n.id)
	}
	return nil
}

// Sweep walks the reclaim queue and reclaims the namespaces that are clean.
func (g *NetnsReclaimGuard) Sweep() (SweepReport, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	report := SweepReport{
		Reclaimed: make([]string, 0),
		Deferred:  make([]string, 0),
		Failed:    make([]string, 0),
	}
	queue := sortedKeys(g.reclaimable)
	var limitErr error

	for _, id := range queue {
		n, ok := g.namespaces[id]
		if !ok {
			delete(g.reclaimable, id)
			continue
		}

		// A queued entry that became live again must never be reclaimed.
		if n.pinned || n.refs() != 0 || n.state != NetnsPending {
			delete(g.reclaimable, id)
			if n.state == NetnsPending {
				n.state = NetnsLive
				n.generation++
			}
			report.Deferred = append(report.Deferred, id)
			continue
		}

		if !n.cleanup.complete() {
			n.tombstone = true
			if n.cleanupTries < g.cfg.CleanupAttempts {
				n.cleanupTries++
				n.generation++
			} else {
				limitErr = ErrCleanupLimit
			}
			report.Failed = append(report.Failed, id)
			continue
		}

		delete(g.namespaces, id)
		delete(g.reclaimable, id)
		report.Reclaimed = append(report.Reclaimed, id)
	}

	return report, limitErr
}

// Info reads one registered namespace.
func (g *NetnsReclaimGuard) Info(id string) (NetnsInfo, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	n, ok := g.namespaces[id]
	if !ok {
		return NetnsInfo{}, false
	}
	return n.info(), true
}

// Pending lists the namespaces waiting for reclamation, ordered by id.
func (g *NetnsReclaimGuard) Pending() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return sortedKeys(g.reclaimable)
}

// SetCleanup records whether the veth pair, the routes and the rules are gone.
func (g *NetnsReclaimGuard) SetCleanup(id string, veth, routes, rules bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	n, ok := g.namespaces[id]
	if !ok {
		return ErrUnknownNamespace
	}
	next := cleanupState{veth: veth, routes: routes, rules: rules}
	if n.cleanup != next {
		n.cleanup = next
		n.generation++
	}
	return nil
}

// MarkExternalMount records that an outside process still has the file mounted.
func (g *NetnsReclaimGuard) MarkExternalMount(id string, present bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	n, ok := g.namespaces[id]
	if !ok {
		return ErrUnknownNamespace
	}
	if n.external == present {
		return nil
	}

	if present {
		if n.refs() >= g.cfg.MaxHoldersPerNetns {
			return ErrHolderLimit
		}
		n.external = true
		n.generation++
		g.makeLiveLocked(n)
		return nil
	}

	n.external = false
	n.generation++
	if n.refs() == 0 && !n.pinned {
		n.state = NetnsPending
		g.reclaimable[n.id] = struct{}{}
	} else {
		n.state = NetnsLive
		delete(g.reclaimable, n.id)
	}
	return nil
}

type snapshotCleanup struct {
	Veth   bool `json:"veth"`
	Routes bool `json:"routes"`
	Rules  bool `json:"rules"`
}

type snapshotHolder struct {
	Kind  string   `json:"kind"`
	Keys  []string `json:"keys"`
	Inode uint64   `json:"inode,omitempty"`
}

type snapshotNamespace struct {
	ID              string           `json:"id"`
	Inode           uint64           `json:"inode,omitempty"`
	Refs            int              `json:"refs"`
	Holders         []snapshotHolder `json:"holders"`
	Pinned          bool             `json:"pinned,omitempty"`
	State           string           `json:"state"`
	Tombstone       bool             `json:"tombstone,omitempty"`
	Generation      int64            `json:"generation"`
	Cleanup         snapshotCleanup  `json:"cleanup"`
	ExternalMount   bool             `json:"external_mount,omitempty"`
	CleanupAttempts int              `json:"cleanup_attempts,omitempty"`
}

type snapshotPayload struct {
	Version    int                 `json:"version"`
	Namespaces []snapshotNamespace `json:"namespaces"`
	Queue      []string            `json:"queue"`
}

// Snapshot serialises the registry, the holders and the reclaim queue.
func (g *NetnsReclaimGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	payload := snapshotPayload{
		Version:    snapshotVersion,
		Namespaces: make([]snapshotNamespace, 0, len(g.namespaces)),
		Queue:      sortedKeys(g.reclaimable),
	}

	ids := make([]string, 0, len(g.namespaces))
	for id := range g.namespaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := g.namespaces[id]
		snap := snapshotNamespace{
			ID:              n.id,
			Inode:           n.inode,
			Refs:            n.refs(),
			Pinned:          n.pinned,
			State:           n.state,
			Tombstone:       n.tombstone,
			Generation:      n.generation,
			Cleanup:         snapshotCleanup{Veth: n.cleanup.veth, Routes: n.cleanup.routes, Rules: n.cleanup.rules},
			ExternalMount:   n.external,
			CleanupAttempts: n.cleanupTries,
			Holders:         make([]snapshotHolder, 0, len(n.groups)),
		}
		groupIDs := make([]int64, 0, len(n.groups))
		for groupID := range n.groups {
			groupIDs = append(groupIDs, groupID)
		}
		sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })
		for _, groupID := range groupIDs {
			group := n.groups[groupID]
			keys := make([]string, 0, len(group.keys))
			for key := range group.keys {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			snap.Holders = append(snap.Holders, snapshotHolder{
				Kind:  group.kind,
				Keys:  keys,
				Inode: group.inode,
			})
		}
		payload.Namespaces = append(payload.Namespaces, snap)
	}
	g.mu.RUnlock()

	return json.Marshal(payload)
}

// Restore replaces the whole registry with a snapshot, all or nothing.
func (g *NetnsReclaimGuard) Restore(data []byte) error {
	namespaces, reclaimable, err := g.parseSnapshot(data)
	if err != nil {
		return err
	}

	g.mu.Lock()
	g.namespaces = namespaces
	g.reclaimable = reclaimable
	g.mu.Unlock()
	return nil
}

func (g *NetnsReclaimGuard) parseSnapshot(data []byte) (map[string]*netnsRecord, map[string]struct{}, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var payload snapshotPayload
	if err := dec.Decode(&payload); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, nil, fmt.Errorf("%w: trailing data", ErrBadSnapshot)
	}
	if payload.Version != snapshotVersion {
		return nil, nil, fmt.Errorf("%w: unsupported version", ErrBadSnapshot)
	}
	if len(payload.Namespaces) > g.cfg.MaxNamespaces {
		return nil, nil, fmt.Errorf("%w: namespace limit exceeded", ErrBadSnapshot)
	}

	namespaces := make(map[string]*netnsRecord, len(payload.Namespaces))
	for _, snap := range payload.Namespaces {
		if strings.TrimSpace(snap.ID) == "" {
			return nil, nil, fmt.Errorf("%w: blank namespace id", ErrBadSnapshot)
		}
		if _, exists := namespaces[snap.ID]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate namespace id", ErrBadSnapshot)
		}
		if snap.State != NetnsLive && snap.State != NetnsPending {
			return nil, nil, fmt.Errorf("%w: invalid namespace state", ErrBadSnapshot)
		}
		if snap.Generation < 0 {
			return nil, nil, fmt.Errorf("%w: invalid generation", ErrBadSnapshot)
		}
		if snap.CleanupAttempts < 0 || snap.CleanupAttempts > g.cfg.CleanupAttempts {
			return nil, nil, fmt.Errorf("%w: cleanup attempt limit exceeded", ErrBadSnapshot)
		}

		n := &netnsRecord{
			id:           snap.ID,
			inode:        snap.Inode,
			groups:       make(map[int64]*holderGroup, len(snap.Holders)),
			holders:      make(map[holderRef]*holderGroup),
			mountGroups:  make(map[uint64]int64),
			pinned:       snap.Pinned,
			state:        snap.State,
			tombstone:    snap.Tombstone,
			generation:   snap.Generation,
			cleanup:      cleanupState{veth: snap.Cleanup.Veth, routes: snap.Cleanup.Routes, rules: snap.Cleanup.Rules},
			external:     snap.ExternalMount,
			cleanupTries: snap.CleanupAttempts,
		}

		for holderIndex, holder := range snap.Holders {
			if !validHolderKind(holder.Kind) || len(holder.Keys) == 0 {
				return nil, nil, fmt.Errorf("%w: invalid holder", ErrBadSnapshot)
			}
			groupID := int64(holderIndex + 1)
			group := &holderGroup{
				id:    groupID,
				kind:  holder.Kind,
				inode: holder.Inode,
				keys:  make(map[string]struct{}, len(holder.Keys)),
			}
			for _, key := range holder.Keys {
				if strings.TrimSpace(key) == "" {
					return nil, nil, fmt.Errorf("%w: blank holder key", ErrBadSnapshot)
				}
				ref := holderRef{kind: holder.Kind, key: key}
				if _, exists := n.holders[ref]; exists {
					return nil, nil, fmt.Errorf("%w: duplicate holder key", ErrBadSnapshot)
				}
				group.keys[key] = struct{}{}
				n.holders[ref] = group
			}
			if holder.Kind == HolderMount && holder.Inode != 0 {
				if _, exists := n.mountGroups[holder.Inode]; exists {
					return nil, nil, fmt.Errorf("%w: duplicate mount inode", ErrBadSnapshot)
				}
				n.mountGroups[holder.Inode] = groupID
			}
			n.groups[groupID] = group
		}
		n.nextGroupID = int64(len(n.groups))

		if n.refs() != snap.Refs {
			return nil, nil, fmt.Errorf("%w: reference count mismatch", ErrBadSnapshot)
		}
		if n.pinned && n.state == NetnsPending {
			return nil, nil, fmt.Errorf("%w: pinned namespace is pending", ErrBadSnapshot)
		}
		if n.state == NetnsPending && (n.pinned || n.refs() != 0) {
			return nil, nil, fmt.Errorf("%w: pending namespace still has owners", ErrBadSnapshot)
		}
		if n.refs() > g.cfg.MaxHoldersPerNetns {
			return nil, nil, fmt.Errorf("%w: holder limit exceeded", ErrBadSnapshot)
		}
		namespaces[n.id] = n
	}

	reclaimable := make(map[string]struct{}, len(payload.Queue))
	for _, id := range payload.Queue {
		if _, exists := reclaimable[id]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate queue entry", ErrBadSnapshot)
		}
		n, ok := namespaces[id]
		if !ok || n.state != NetnsPending {
			return nil, nil, fmt.Errorf("%w: queue entry is not pending", ErrBadSnapshot)
		}
		reclaimable[id] = struct{}{}
	}
	for id, n := range namespaces {
		if n.state == NetnsPending {
			if _, ok := reclaimable[id]; !ok {
				return nil, nil, fmt.Errorf("%w: pending namespace is not queued", ErrBadSnapshot)
			}
		}
	}

	return namespaces, reclaimable, nil
}

func (g *NetnsReclaimGuard) initLocked() {
	if g.namespaces == nil {
		g.namespaces = make(map[string]*netnsRecord)
	}
	if g.reclaimable == nil {
		g.reclaimable = make(map[string]struct{})
	}
}

func (g *NetnsReclaimGuard) makeLiveLocked(n *netnsRecord) {
	if n.state == NetnsPending {
		n.state = NetnsLive
	}
	delete(g.reclaimable, n.id)
}

func (n *netnsRecord) info() NetnsInfo {
	return NetnsInfo{
		ID:         n.id,
		Inode:      n.inode,
		Refs:       n.refs(),
		Pinned:     n.pinned,
		State:      n.state,
		Tombstone:  n.tombstone,
		Generation: n.generation,
	}
}

func validHolderKind(kind string) bool {
	return kind == HolderProcess || kind == HolderFD || kind == HolderMount
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
