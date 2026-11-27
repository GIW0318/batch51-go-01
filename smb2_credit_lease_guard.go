// Package task runs the SMB2 server side credit window, the lease state
// machine and the durable handle table used after a reconnect.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Lease states are a bitmask: a lease may carry any combination of them.
type LeaseState uint32

// Individual lease state bits.
const (
	LeaseNone   LeaseState = 0
	LeaseRead   LeaseState = 1
	LeaseWrite  LeaseState = 2
	LeaseHandle LeaseState = 4

	leaseStateMask  = LeaseRead | LeaseWrite | LeaseHandle
	leaseAccessMask = LeaseRead | LeaseWrite
	snapshotVersion = 1
)

// Errors returned by the guard.
var (
	ErrBlankConn       = errors.New("connection id is required")
	ErrConnExists      = errors.New("connection already negotiated")
	ErrConnUnknown     = errors.New("connection not negotiated")
	ErrBlankClient     = errors.New("client guid is required")
	ErrBadDialect      = errors.New("unsupported smb2 dialect")
	ErrBadCost         = errors.New("credit cost must be positive")
	ErrCreditExhausted = errors.New("credit window exhausted")
	ErrBadPath         = errors.New("file path must be absolute")
	ErrBadLeaseState   = errors.New("lease state transition is invalid")
	ErrLeaseNotFound   = errors.New("lease not found")
	ErrBreakPending    = errors.New("lease break already pending")
	ErrNoBreakPending  = errors.New("no lease break pending")
	ErrBreakMismatch   = errors.New("acknowledged state does not match the pending break")
	ErrHandleNotFound  = errors.New("durable handle not found")
	ErrHandleOwner     = errors.New("durable handle belongs to another client")
	ErrLeaseLimit      = errors.New("lease table is full")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// ServerConfig is the negotiated capability of the server.
type ServerConfig struct {
	Dialect        uint16 `json:"dialect"`
	InitialCredits uint16 `json:"initial_credits"`
	MaxCredits     uint16 `json:"max_credits"`
	MaxLeases      int    `json:"max_leases"`
}

// NegotiateReq opens one transport connection.
type NegotiateReq struct {
	ConnID           string `json:"conn_id"`
	ClientGUID       string `json:"client_guid"`
	Dialect          uint16 `json:"dialect"`
	CreditsRequested uint16 `json:"credits_requested"`
}

// NegotiateResp reports the granted dialect and credit window.
type NegotiateResp struct {
	ConnID     string `json:"conn_id"`
	Dialect    uint16 `json:"dialect"`
	Credits    uint16 `json:"credits"`
	ClientGUID string `json:"client_guid"`
}

// CreateReq is one CREATE carrying an optional lease key.
type CreateReq struct {
	Path     string     `json:"path"`
	LeaseKey string     `json:"lease_key"`
	Desired  LeaseState `json:"desired"`
}

// FileID is the durable identity of one open file.
type FileID struct {
	Persistent uint64 `json:"persistent"`
	Volatile   uint64 `json:"volatile"`
}

// Handle is one recoverable open handle.
type Handle struct {
	FileID FileID `json:"file_id"`
	Path   string `json:"path"`
	ConnID string `json:"conn_id"`
}

// LeaseEntry is one lease record keyed by the client supplied lease key.
type LeaseEntry struct {
	LeaseKey string     `json:"lease_key"`
	FileID   FileID     `json:"file_id"`
	Owner    string     `json:"owner"`
	ConnID   string     `json:"conn_id"`
	State    LeaseState `json:"state"`
	Breaking bool       `json:"breaking"`
	Target   LeaseState `json:"target"`
	Rev      uint64     `json:"rev"`
}

// BreakResult is the outcome of one server initiated downgrade.
type BreakResult struct {
	LeaseKey    string     `json:"lease_key"`
	From        LeaseState `json:"from"`
	To          LeaseState `json:"to"`
	AckRequired bool       `json:"ack_required"`
	Rev         uint64     `json:"rev"`
}

// CreditStats reports the live credit accounting.
type CreditStats struct {
	Connections int    `json:"connections"`
	Outstanding uint32 `json:"outstanding"`
	Charges     uint64 `json:"charges"`
	Breaks      uint64 `json:"breaks"`
}

type connState struct {
	connID     string
	clientGUID string
	dialect    uint16
	window     uint16
	available  uint16
}

type handleState struct {
	fileID FileID
	path   string
	owner  string
	connID string
}

// Smb2CreditLeaseGuard owns the connection table, the credit windows, the
// lease table and the durable handle table.
type Smb2CreditLeaseGuard struct {
	mu         sync.Mutex
	cfg        ServerConfig
	conns      map[string]*connState
	leases     map[string]*LeaseEntry
	handles    map[FileID]*handleState
	nextFileID uint64
	charges    uint64
	breaks     uint64
}

// NewSmb2CreditLeaseGuard builds a guard with the given capabilities.
func NewSmb2CreditLeaseGuard(cfg ServerConfig) *Smb2CreditLeaseGuard {
	return &Smb2CreditLeaseGuard{
		cfg:     cfg,
		conns:   make(map[string]*connState),
		leases:  make(map[string]*LeaseEntry),
		handles: make(map[FileID]*handleState),
	}
}

func normalizeConnID(connID string) string {
	return strings.TrimSpace(connID)
}

func validLeaseState(state LeaseState) bool {
	return state&^leaseStateMask == 0
}

// Negotiate opens one connection and grants its credit window.
func (g *Smb2CreditLeaseGuard) Negotiate(req NegotiateReq) (NegotiateResp, error) {
	connID := normalizeConnID(req.ConnID)
	if connID == "" {
		return NegotiateResp{}, ErrBlankConn
	}
	client := strings.TrimSpace(req.ClientGUID)
	if client == "" {
		return NegotiateResp{}, ErrBlankClient
	}
	if req.Dialect != g.cfg.Dialect {
		return NegotiateResp{}, ErrBadDialect
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.conns[connID]; ok {
		return NegotiateResp{}, ErrConnExists
	}

	credits := req.CreditsRequested
	if credits == 0 {
		credits = g.cfg.InitialCredits
	}
	if credits > g.cfg.InitialCredits {
		credits = g.cfg.InitialCredits
	}
	if credits > g.cfg.MaxCredits {
		credits = g.cfg.MaxCredits
	}

	g.conns[connID] = &connState{
		connID:     connID,
		clientGUID: client,
		dialect:    req.Dialect,
		window:     credits,
		available:  credits,
	}
	return NegotiateResp{
		ConnID:     connID,
		Dialect:    req.Dialect,
		Credits:    credits,
		ClientGUID: client,
	}, nil
}

// Charge consumes credits for one request and reports what is left.
func (g *Smb2CreditLeaseGuard) Charge(connID string, cost uint16) (uint16, error) {
	if cost == 0 {
		return 0, ErrBadCost
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[normalizeConnID(connID)]
	if !ok {
		return 0, ErrConnUnknown
	}
	if cost > conn.available {
		return conn.available, ErrCreditExhausted
	}
	conn.available -= cost
	g.charges++
	return conn.available, nil
}

// Refund returns unused credits to the window, clamped to its size.
func (g *Smb2CreditLeaseGuard) Refund(connID string, credits uint16) (uint16, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[normalizeConnID(connID)]
	if !ok {
		return 0, ErrConnUnknown
	}
	if credits >= conn.window-conn.available {
		conn.available = conn.window
	} else {
		conn.available += credits
	}
	return conn.available, nil
}

// Open creates or reopens a file handle, optionally under a lease key.
func (g *Smb2CreditLeaseGuard) Open(connID string, req CreateReq) (FileID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[normalizeConnID(connID)]
	if !ok {
		return FileID{}, ErrConnUnknown
	}
	if req.Path == "" || !strings.HasPrefix(req.Path, "/") {
		return FileID{}, ErrBadPath
	}
	if !validLeaseState(req.Desired) {
		return FileID{}, ErrBadLeaseState
	}

	if req.LeaseKey != "" {
		if existing, ok := g.leases[req.LeaseKey]; ok {
			if existing.Breaking && req.Desired&^existing.Target != 0 {
				return FileID{}, ErrBadLeaseState
			}
			return existing.FileID, nil
		}
		if len(g.leases) >= g.cfg.MaxLeases {
			return FileID{}, ErrLeaseLimit
		}
		if g.wouldConflict(req.Path, req.Desired) {
			return FileID{}, ErrBadLeaseState
		}
	}

	id := g.allocateFileID()
	h := &handleState{
		fileID: id,
		path:   req.Path,
		owner:  conn.clientGUID,
		connID: conn.connID,
	}
	g.handles[id] = h

	if req.LeaseKey != "" {
		g.leases[req.LeaseKey] = &LeaseEntry{
			LeaseKey: req.LeaseKey,
			FileID:   id,
			Owner:    conn.clientGUID,
			ConnID:   conn.connID,
			State:    req.Desired,
		}
	}
	return id, nil
}

func (g *Smb2CreditLeaseGuard) allocateFileID() FileID {
	g.nextFileID++
	if g.nextFileID == 0 {
		g.nextFileID = 1
	}
	id := FileID{Persistent: g.nextFileID, Volatile: g.nextFileID}
	for {
		if _, exists := g.handles[id]; !exists {
			return id
		}
		g.nextFileID++
		if g.nextFileID == 0 {
			g.nextFileID = 1
		}
		id = FileID{Persistent: g.nextFileID, Volatile: g.nextFileID}
	}
}

// effectiveLeaseState is the state other opens must contend with. A pending
// break is authoritative until it is acknowledged.
func (g *Smb2CreditLeaseGuard) effectiveLeaseState(lease *LeaseEntry) LeaseState {
	if lease.Breaking {
		return lease.Target
	}
	return lease.State
}

func (g *Smb2CreditLeaseGuard) wouldConflict(path string, desired LeaseState) bool {
	want := desired & leaseAccessMask
	if want == 0 {
		return false
	}
	for _, lease := range g.leases {
		h, ok := g.handles[lease.FileID]
		if !ok || h.path != path {
			continue
		}
		have := g.effectiveLeaseState(lease) & leaseAccessMask
		if have == 0 {
			continue
		}
		// Read leases may be shared, but any write access is exclusive with
		// either kind of access lease on the same file.
		if have&LeaseWrite != 0 || want&LeaseWrite != 0 {
			return true
		}
	}
	return false
}

// Break starts a server initiated lease downgrade.
func (g *Smb2CreditLeaseGuard) Break(leaseKey string, to LeaseState) (BreakResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	lease, ok := g.leases[leaseKey]
	if !ok {
		return BreakResult{}, ErrLeaseNotFound
	}
	if to == lease.State || to&^lease.State != 0 {
		return BreakResult{}, ErrBadLeaseState
	}
	if lease.Breaking {
		return BreakResult{}, ErrBreakPending
	}

	from := lease.State
	lease.Breaking = true
	lease.Target = to
	lease.Rev++
	g.breaks++
	return BreakResult{
		LeaseKey:    leaseKey,
		From:        from,
		To:          to,
		AckRequired: true,
		Rev:         lease.Rev,
	}, nil
}

// AckBreak settles the pending downgrade.
func (g *Smb2CreditLeaseGuard) AckBreak(leaseKey string, state LeaseState) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	lease, ok := g.leases[leaseKey]
	if !ok {
		return ErrLeaseNotFound
	}
	if !lease.Breaking {
		return ErrNoBreakPending
	}
	if state != lease.Target {
		return ErrBreakMismatch
	}
	lease.State = lease.Target
	lease.Breaking = false
	lease.Target = LeaseNone
	return nil
}

// Reconnect rebinds a durable handle to a new connection of the same client.
func (g *Smb2CreditLeaseGuard) Reconnect(connID string, fileID FileID) (Handle, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	connID = normalizeConnID(connID)
	conn, ok := g.conns[connID]
	if !ok {
		return Handle{}, ErrConnUnknown
	}
	h, ok := g.handles[fileID]
	if !ok {
		return Handle{}, ErrHandleNotFound
	}
	if h.owner != conn.clientGUID {
		return Handle{}, ErrHandleOwner
	}

	h.connID = connID
	for _, lease := range g.leases {
		if lease.FileID == fileID {
			lease.ConnID = connID
		}
	}
	return Handle{FileID: h.fileID, Path: h.path, ConnID: connID}, nil
}

// Disconnect drops one transport connection and its unacknowledged credits.
func (g *Smb2CreditLeaseGuard) Disconnect(connID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	connID = normalizeConnID(connID)
	if _, ok := g.conns[connID]; !ok {
		return ErrConnUnknown
	}
	delete(g.conns, connID)
	return nil
}

// LeaseOf reads one lease record.
func (g *Smb2CreditLeaseGuard) LeaseOf(leaseKey string) (LeaseEntry, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	lease, ok := g.leases[leaseKey]
	if !ok {
		return LeaseEntry{}, false
	}
	return *lease, true
}

// Stats reports the live credit accounting.
func (g *Smb2CreditLeaseGuard) Stats() CreditStats {
	g.mu.Lock()
	defer g.mu.Unlock()

	var outstanding uint64
	for _, conn := range g.conns {
		outstanding += uint64(conn.available)
	}
	return CreditStats{
		Connections: len(g.conns),
		Outstanding: uint32(outstanding),
		Charges:     g.charges,
		Breaks:      g.breaks,
	}
}

type snapshotConn struct {
	ConnID     string `json:"conn_id"`
	ClientGUID string `json:"client_guid"`
	Dialect    uint16 `json:"dialect"`
	Window     uint16 `json:"window"`
	Available  uint16 `json:"available"`
}

type snapshotHandle struct {
	FileID FileID `json:"file_id"`
	Path   string `json:"path"`
	Owner  string `json:"owner"`
	ConnID string `json:"conn_id"`
}

type snapshotData struct {
	Version     int              `json:"version"`
	NextFileID  uint64           `json:"next_file_id"`
	Charges     uint64           `json:"charges"`
	Breaks      uint64           `json:"breaks"`
	Connections []snapshotConn   `json:"connections"`
	Leases      []LeaseEntry     `json:"leases"`
	Handles     []snapshotHandle `json:"handles"`
}

// Snapshot serialises connections, leases and durable handles.
func (g *Smb2CreditLeaseGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	snap := snapshotData{
		Version:     snapshotVersion,
		NextFileID:  g.nextFileID,
		Charges:     g.charges,
		Breaks:      g.breaks,
		Connections: make([]snapshotConn, 0, len(g.conns)),
		Leases:      make([]LeaseEntry, 0, len(g.leases)),
		Handles:     make([]snapshotHandle, 0, len(g.handles)),
	}
	for _, conn := range g.conns {
		snap.Connections = append(snap.Connections, snapshotConn{
			ConnID:     conn.connID,
			ClientGUID: conn.clientGUID,
			Dialect:    conn.dialect,
			Window:     conn.window,
			Available:  conn.available,
		})
	}
	for _, lease := range g.leases {
		snap.Leases = append(snap.Leases, *lease)
	}
	for _, h := range g.handles {
		snap.Handles = append(snap.Handles, snapshotHandle{
			FileID: h.fileID,
			Path:   h.path,
			Owner:  h.owner,
			ConnID: h.connID,
		})
	}
	sort.Slice(snap.Connections, func(i, j int) bool {
		return snap.Connections[i].ConnID < snap.Connections[j].ConnID
	})
	sort.Slice(snap.Leases, func(i, j int) bool {
		return snap.Leases[i].LeaseKey < snap.Leases[j].LeaseKey
	})
	sort.Slice(snap.Handles, func(i, j int) bool {
		return snap.Handles[i].FileID.Persistent < snap.Handles[j].FileID.Persistent
	})
	return json.Marshal(snap)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *Smb2CreditLeaseGuard) Restore(data []byte) error {
	var snap snapshotData
	if err := json.Unmarshal(data, &snap); err != nil || snap.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	conns := make(map[string]*connState, len(snap.Connections))
	for _, c := range snap.Connections {
		if c.ConnID == "" || c.ClientGUID == "" || c.Available > c.Window {
			return ErrBadSnapshot
		}
		if _, exists := conns[c.ConnID]; exists {
			return ErrBadSnapshot
		}
		conns[c.ConnID] = &connState{
			connID:     c.ConnID,
			clientGUID: c.ClientGUID,
			dialect:    c.Dialect,
			window:     c.Window,
			available:  c.Available,
		}
	}

	handles := make(map[FileID]*handleState, len(snap.Handles))
	for _, h := range snap.Handles {
		if h.FileID.Persistent == 0 || h.Path == "" || !strings.HasPrefix(h.Path, "/") || h.Owner == "" {
			return ErrBadSnapshot
		}
		if _, exists := handles[h.FileID]; exists {
			return ErrBadSnapshot
		}
		handles[h.FileID] = &handleState{
			fileID: h.FileID,
			path:   h.Path,
			owner:  h.Owner,
			connID: h.ConnID,
		}
	}

	if len(snap.Leases) > g.cfg.MaxLeases {
		return ErrBadSnapshot
	}
	leases := make(map[string]*LeaseEntry, len(snap.Leases))
	for _, l := range snap.Leases {
		if l.LeaseKey == "" || l.Owner == "" || !validLeaseState(l.State) || !validLeaseState(l.Target) {
			return ErrBadSnapshot
		}
		if _, exists := handles[l.FileID]; !exists {
			return ErrBadSnapshot
		}
		if _, exists := leases[l.LeaseKey]; exists {
			return ErrBadSnapshot
		}
		if l.Breaking && (l.Target == l.State || l.Target&^l.State != 0) {
			return ErrBadSnapshot
		}
		copy := l
		leases[l.LeaseKey] = &copy
	}

	var maxFileID uint64
	for id := range handles {
		if id.Persistent > maxFileID {
			maxFileID = id.Persistent
		}
	}
	if snap.NextFileID < maxFileID {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	g.conns = conns
	g.leases = leases
	g.handles = handles
	g.nextFileID = snap.NextFileID
	g.charges = snap.Charges
	g.breaks = snap.Breaks
	g.mu.Unlock()
	return nil
}
