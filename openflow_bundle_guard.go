// Package task orders the flow table transactions a controller pushes at a
// switch: bundles commit as a unit, barriers pin the visible order, and the
// controller role decides which side may write.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Role is the OpenFlow controller role for one datapath.
type Role uint8

// Roles accepted by SetRole.
const (
	RoleEqual  Role = 0
	RoleMaster Role = 1
	RoleSlave  Role = 2
)

// Bundle lifecycle states reported by BundleState.
const (
	BundleOpen      = "open"
	BundleCommitted = "committed"
)

// Errors returned by the guard.
var (
	ErrBadDpid         = errors.New("dpid is required")
	ErrUnknownDpid     = errors.New("dpid is not registered")
	ErrUnknownBundle   = errors.New("bundle is not open")
	ErrBundleRejected  = errors.New("bundle was rejected as a whole")
	ErrBadMessage      = errors.New("message type is not supported")
	ErrEmptyBundle     = errors.New("bundle has no messages")
	ErrNotMaster       = errors.New("controller is not master for this dpid")
	ErrStaleGeneration = errors.New("generation does not move forward")
	ErrRoleDenied      = errors.New("role value is not supported")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// BundleID aliases the identifier returned by OpenBundle.
type BundleID = string

// Limits bounds the live tables of the guard.
type Limits struct {
	MaxBundles       int
	MaxMsgsPerBundle int
	MaxPending       int
}

// BundleReq opens a transaction envelope on one datapath.
type BundleReq struct {
	Xid   uint32
	Flags uint16
	Kind  string
}

// OfMsg is one OpenFlow message carried inside a bundle.
type OfMsg struct {
	Xid      uint32
	Type     string
	Group    string
	Creates  string
	Table    int
	Priority int
	Match    string
}

// CommitProps carries the ordering requirements of one commit.
type CommitProps struct {
	Barrier bool
	Atomic  bool
}

// CommitResult reports what a commit did.
type CommitResult struct {
	BundleID string
	Applied  int
	Barrier  uint32
}

// BarrierTicket is the fence handed back to the controller.
type BarrierTicket struct {
	Dpid    string
	Xid     uint32
	Pending int
	Role    Role
	Done    bool
}

// RoleState is the current role and generation of one datapath.
type RoleState struct {
	Dpid string
	Role Role
	Gen  uint64
}

// Bundle is the observable state of one transaction envelope.
type Bundle struct {
	ID    string
	Dpid  string
	State string
	Msgs  []OfMsg
}

type bundleData struct {
	id    string
	dpid  string
	state string
	req   BundleReq
	msgs  []OfMsg
}

type dpidData struct {
	mu sync.Mutex

	dpid string
	role Role
	gen  uint64

	flow     []OfMsg
	groups   []string
	groupSet map[string]struct{}

	pending map[uint32]struct{}

	barrierPresent bool
	barrier        BarrierTicket
	nextBarrierXid uint32
}

// OpenflowBundleGuard owns the per dpid tables, bundles and barrier state.
//
// The guard lock protects the registries and gives Snapshot/Restore a stable
// view. Ordinary operations take it for reading, then serialize only on the
// affected datapath.
type OpenflowBundleGuard struct {
	mu sync.RWMutex

	limits Limits

	bundles    map[string]*bundleData
	dpids      map[string]*dpidData
	nextBundle uint64
}

// NewOpenflowBundleGuard builds a guard with the given caps.
func NewOpenflowBundleGuard(limits Limits) *OpenflowBundleGuard {
	return &OpenflowBundleGuard{
		limits:  limits,
		bundles: make(map[string]*bundleData),
		dpids:   make(map[string]*dpidData),
	}
}

func normalizeDpid(dpid string) (string, error) {
	dpid = strings.TrimSpace(dpid)
	if dpid == "" {
		return "", ErrBadDpid
	}
	return dpid, nil
}

func validRole(role Role) bool {
	return role == RoleEqual || role == RoleMaster || role == RoleSlave
}

func validMessage(msg OfMsg) bool {
	if msg.Xid == 0 {
		return false
	}
	switch msg.Type {
	case "flow_mod", "group_mod", "meter_mod":
		return true
	default:
		return false
	}
}

func newDpidData(dpid string) *dpidData {
	return &dpidData{
		dpid:     dpid,
		role:     RoleMaster,
		groupSet: make(map[string]struct{}),
		pending:  make(map[uint32]struct{}),
	}
}

func (d *dpidData) nextFenceXid() uint32 {
	d.nextBarrierXid++
	if d.nextBarrierXid == 0 {
		d.nextBarrierXid = 1
	}
	return d.nextBarrierXid
}

func (d *dpidData) issueBarrier(xid uint32) BarrierTicket {
	if xid == 0 {
		xid = d.nextFenceXid()
	}
	ticket := BarrierTicket{
		Dpid:    d.dpid,
		Xid:     xid,
		Pending: len(d.pending),
		Role:    d.role,
		Done:    len(d.pending) == 0,
	}
	d.barrier = ticket
	d.barrierPresent = true
	return ticket
}

func (d *dpidData) liveBarrier() (BarrierTicket, bool) {
	if !d.barrierPresent {
		return BarrierTicket{}, false
	}
	ticket := d.barrier
	ticket.Dpid = d.dpid
	ticket.Pending = len(d.pending)
	ticket.Role = d.role
	ticket.Done = len(d.pending) == 0
	return ticket, true
}

func (g *OpenflowBundleGuard) nextBundleIDLocked() string {
	for {
		g.nextBundle++
		if g.nextBundle == 0 {
			g.nextBundle = 1
		}
		id := "bundle-" + strconv.FormatUint(g.nextBundle, 10)
		if _, exists := g.bundles[id]; !exists {
			return id
		}
	}
}

// OpenBundle starts a transaction envelope on one datapath.
func (g *OpenflowBundleGuard) OpenBundle(dpid string, req BundleReq) (BundleID, error) {
	dpid, err := normalizeDpid(dpid)
	if err != nil {
		return "", err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	dp := g.dpids[dpid]
	if dp == nil {
		dp = newDpidData(dpid)
		g.dpids[dpid] = dp
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()

	if dp.role == RoleSlave {
		return "", ErrNotMaster
	}
	if g.limits.MaxBundles > 0 && len(g.bundles) >= g.limits.MaxBundles {
		return "", ErrBundleRejected
	}

	id := g.nextBundleIDLocked()
	g.bundles[id] = &bundleData{
		id:    id,
		dpid:  dpid,
		state: BundleOpen,
		req:   req,
	}
	return id, nil
}

// Add appends one message to an open bundle.
func (g *OpenflowBundleGuard) Add(bundleID string, msg OfMsg) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	bundle := g.bundles[bundleID]
	if bundle == nil {
		return ErrUnknownBundle
	}
	dp := g.dpids[bundle.dpid]
	if dp == nil {
		return ErrUnknownDpid
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()

	if bundle.state != BundleOpen {
		return ErrUnknownBundle
	}
	if dp.role == RoleSlave {
		return ErrNotMaster
	}
	if !validMessage(msg) {
		return ErrBadMessage
	}
	if g.limits.MaxMsgsPerBundle > 0 && len(bundle.msgs) >= g.limits.MaxMsgsPerBundle {
		return ErrBundleRejected
	}

	bundle.msgs = append(bundle.msgs, msg)
	return nil
}

func splitGroupNames(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	})
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		if name := strings.TrimSpace(field); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func groupsCreatedBy(msgs []OfMsg) map[string]struct{} {
	created := make(map[string]struct{})
	for _, msg := range msgs {
		for _, name := range splitGroupNames(msg.Creates) {
			created[name] = struct{}{}
		}
		if msg.Type == "group_mod" {
			for _, name := range splitGroupNames(msg.Group) {
				created[name] = struct{}{}
			}
		}
	}
	return created
}

func referencesSatisfied(msgs []OfMsg, existing map[string]struct{}) bool {
	created := groupsCreatedBy(msgs)
	for _, msg := range msgs {
		for _, name := range splitGroupNames(msg.Group) {
			if _, ok := existing[name]; ok {
				continue
			}
			if _, ok := created[name]; !ok {
				return false
			}
		}
	}
	return true
}

func (d *dpidData) pendingAfter(msgs []OfMsg) (map[uint32]struct{}, int) {
	next := make(map[uint32]struct{}, len(d.pending))
	for xid := range d.pending {
		next[xid] = struct{}{}
	}
	for _, msg := range msgs {
		next[msg.Xid] = struct{}{}
	}
	return next, len(next)
}

// Commit applies the whole bundle or nothing at all.
func (g *OpenflowBundleGuard) Commit(bundleID string, props CommitProps) (CommitResult, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	bundle := g.bundles[bundleID]
	if bundle == nil {
		return CommitResult{}, ErrUnknownBundle
	}
	dp := g.dpids[bundle.dpid]
	if dp == nil {
		return CommitResult{}, ErrUnknownDpid
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()

	if bundle.state != BundleOpen {
		return CommitResult{}, ErrUnknownBundle
	}
	if dp.role == RoleSlave {
		return CommitResult{}, ErrNotMaster
	}
	if len(bundle.msgs) == 0 {
		return CommitResult{}, ErrEmptyBundle
	}
	if !referencesSatisfied(bundle.msgs, dp.groupSet) {
		return CommitResult{}, ErrBundleRejected
	}

	nextPending, pendingCount := dp.pendingAfter(bundle.msgs)
	if g.limits.MaxPending > 0 && pendingCount > g.limits.MaxPending {
		return CommitResult{}, ErrBundleRejected
	}

	for _, msg := range bundle.msgs {
		dp.flow = append(dp.flow, msg)
		if msg.Type == "group_mod" {
			for _, name := range splitGroupNames(msg.Creates) {
				dp.addGroupLocked(name)
			}
			if len(splitGroupNames(msg.Creates)) == 0 {
				for _, name := range splitGroupNames(msg.Group) {
					dp.addGroupLocked(name)
				}
			}
		}
	}
	dp.pending = nextPending
	bundle.state = BundleCommitted

	result := CommitResult{
		BundleID: bundle.id,
		Applied:  len(bundle.msgs),
	}
	if props.Barrier {
		ticket := dp.issueBarrier(bundle.req.Xid)
		result.Barrier = uint32(ticket.Pending)
	}
	return result, nil
}

func (d *dpidData) addGroupLocked(name string) {
	if name == "" {
		return
	}
	if _, exists := d.groupSet[name]; exists {
		return
	}
	d.groupSet[name] = struct{}{}
	d.groups = append(d.groups, name)
}

// Barrier issues a fence and reports how many replies are still outstanding.
func (g *OpenflowBundleGuard) Barrier(dpid string) (BarrierTicket, error) {
	dpid, err := normalizeDpid(dpid)
	if err != nil {
		return BarrierTicket{}, err
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	dp := g.dpids[dpid]
	if dp == nil {
		return BarrierTicket{}, ErrUnknownDpid
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()
	return dp.issueBarrier(0), nil
}

// BarrierReply consumes one asynchronous reply for a pending message xid.
func (g *OpenflowBundleGuard) BarrierReply(dpid string, xid uint32) error {
	dpid, err := normalizeDpid(dpid)
	if err != nil {
		return err
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	dp := g.dpids[dpid]
	if dp == nil {
		return ErrUnknownDpid
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()
	delete(dp.pending, xid)
	return nil
}

// BarrierState reads the last fence of a datapath.
func (g *OpenflowBundleGuard) BarrierState(dpid string) (BarrierTicket, bool) {
	dpid = strings.TrimSpace(dpid)
	if dpid == "" {
		return BarrierTicket{}, false
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	dp := g.dpids[dpid]
	if dp == nil {
		return BarrierTicket{}, false
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()
	return dp.liveBarrier()
}

// SetRole moves the controller role forward by one generation.
func (g *OpenflowBundleGuard) SetRole(dpid string, role Role, gen uint64) (RoleState, error) {
	dpid, err := normalizeDpid(dpid)
	if err != nil {
		return RoleState{}, err
	}
	if !validRole(role) {
		return RoleState{}, ErrRoleDenied
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	dp := g.dpids[dpid]
	created := false
	if dp == nil {
		if gen == 0 {
			return RoleState{}, ErrStaleGeneration
		}
		dp = newDpidData(dpid)
		g.dpids[dpid] = dp
		created = true
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()

	if gen <= dp.gen {
		if created {
			delete(g.dpids, dpid)
		}
		return RoleState{}, ErrStaleGeneration
	}

	dp.role = role
	dp.gen = gen
	return RoleState{Dpid: dpid, Role: role, Gen: gen}, nil
}

// Reconnect re-arms the fences that were not acknowledged before the switch
// dropped and reports how many were re-issued.
func (g *OpenflowBundleGuard) Reconnect(dpid string) (int, error) {
	dpid, err := normalizeDpid(dpid)
	if err != nil {
		return 0, err
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	dp := g.dpids[dpid]
	if dp == nil {
		return 0, ErrUnknownDpid
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()
	pending := len(dp.pending)
	dp.issueBarrier(0)
	return pending, nil
}

// BundleState reads one transaction envelope.
func (g *OpenflowBundleGuard) BundleState(bundleID string) (Bundle, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	bundle := g.bundles[bundleID]
	if bundle == nil {
		return Bundle{}, false
	}
	dp := g.dpids[bundle.dpid]
	if dp == nil {
		return Bundle{}, false
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()
	return Bundle{
		ID:    bundle.id,
		Dpid:  bundle.dpid,
		State: bundle.state,
		Msgs:  append([]OfMsg(nil), bundle.msgs...),
	}, true
}

// FlowTable lists the messages that are visible in the datapath today.
func (g *OpenflowBundleGuard) FlowTable(dpid string) []OfMsg {
	dpid = strings.TrimSpace(dpid)
	if dpid == "" {
		return nil
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	dp := g.dpids[dpid]
	if dp == nil {
		return nil
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()
	if len(dp.flow) == 0 {
		return nil
	}
	return append([]OfMsg(nil), dp.flow...)
}

// GroupTable lists the group ids the datapath knows today.
func (g *OpenflowBundleGuard) GroupTable(dpid string) []string {
	dpid = strings.TrimSpace(dpid)
	if dpid == "" {
		return nil
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	dp := g.dpids[dpid]
	if dp == nil {
		return nil
	}

	dp.mu.Lock()
	defer dp.mu.Unlock()
	if len(dp.groups) == 0 {
		return nil
	}
	return append([]string(nil), dp.groups...)
}

type snapshotBundle struct {
	ID    string    `json:"id"`
	Dpid  string    `json:"dpid"`
	State string    `json:"state"`
	Req   BundleReq `json:"req"`
	Msgs  []OfMsg   `json:"msgs"`
}

type snapshotBarrier struct {
	Present bool   `json:"present"`
	Xid     uint32 `json:"xid"`
}

type snapshotDpid struct {
	Dpid           string          `json:"dpid"`
	Role           Role            `json:"role"`
	Gen            uint64          `json:"gen"`
	Flow           []OfMsg         `json:"flow"`
	Groups         []string        `json:"groups"`
	Pending        []uint32        `json:"pending"`
	Barrier        snapshotBarrier `json:"barrier"`
	NextBarrierXid uint32          `json:"next_barrier_xid"`
}

type snapshotDoc struct {
	Version    int              `json:"version"`
	NextBundle uint64           `json:"next_bundle"`
	Bundles    []snapshotBundle `json:"bundles"`
	Dpids      []snapshotDpid   `json:"dpids"`
}

// Snapshot serialises bundles, roles, tables and outstanding replies.
func (g *OpenflowBundleGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	dpidNames := make([]string, 0, len(g.dpids))
	for dpid := range g.dpids {
		dpidNames = append(dpidNames, dpid)
	}
	sort.Strings(dpidNames)

	// Holding the guard for writing and all datapath locks gives one
	// consistent cut across every dpid. The lock order is always guard then
	// datapath, so ordinary operations cannot deadlock against this pass.
	for _, dpid := range dpidNames {
		g.dpids[dpid].mu.Lock()
	}
	defer func() {
		for _, dpid := range dpidNames {
			g.dpids[dpid].mu.Unlock()
		}
	}()

	doc := snapshotDoc{Version: 1, NextBundle: g.nextBundle}
	for _, dpid := range dpidNames {
		dp := g.dpids[dpid]
		pending := make([]uint32, 0, len(dp.pending))
		for xid := range dp.pending {
			pending = append(pending, xid)
		}
		sort.Slice(pending, func(i, j int) bool { return pending[i] < pending[j] })

		doc.Dpids = append(doc.Dpids, snapshotDpid{
			Dpid:    dp.dpid,
			Role:    dp.role,
			Gen:     dp.gen,
			Flow:    append([]OfMsg(nil), dp.flow...),
			Groups:  append([]string(nil), dp.groups...),
			Pending: pending,
			Barrier: snapshotBarrier{
				Present: dp.barrierPresent,
				Xid:     dp.barrier.Xid,
			},
			NextBarrierXid: dp.nextBarrierXid,
		})
	}

	bundleIDs := make([]string, 0, len(g.bundles))
	for id := range g.bundles {
		bundleIDs = append(bundleIDs, id)
	}
	sort.Strings(bundleIDs)
	for _, id := range bundleIDs {
		bundle := g.bundles[id]
		doc.Bundles = append(doc.Bundles, snapshotBundle{
			ID:    bundle.id,
			Dpid:  bundle.dpid,
			State: bundle.state,
			Req:   bundle.req,
			Msgs:  append([]OfMsg(nil), bundle.msgs...),
		})
	}

	return json.Marshal(doc)
}

func decodeSnapshot(data []byte) (*snapshotDoc, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var doc snapshotDoc
	if err := decoder.Decode(&doc); err != nil {
		return nil, ErrBadSnapshot
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrBadSnapshot
	}
	return &doc, nil
}

func (g *OpenflowBundleGuard) buildRestoredState(doc *snapshotDoc) (map[string]*bundleData, map[string]*dpidData, uint64, error) {
	if doc.Version != 1 {
		return nil, nil, 0, ErrBadSnapshot
	}
	if g.limits.MaxBundles > 0 && len(doc.Bundles) > g.limits.MaxBundles {
		return nil, nil, 0, ErrBadSnapshot
	}

	dpids := make(map[string]*dpidData, len(doc.Dpids))
	for _, saved := range doc.Dpids {
		if saved.Dpid == "" || strings.TrimSpace(saved.Dpid) != saved.Dpid {
			return nil, nil, 0, ErrBadSnapshot
		}
		if _, exists := dpids[saved.Dpid]; exists {
			return nil, nil, 0, ErrBadSnapshot
		}
		if !validRole(saved.Role) {
			return nil, nil, 0, ErrBadSnapshot
		}
		if g.limits.MaxPending > 0 && len(saved.Pending) > g.limits.MaxPending {
			return nil, nil, 0, ErrBadSnapshot
		}

		dp := newDpidData(saved.Dpid)
		dp.role = saved.Role
		dp.gen = saved.Gen
		dp.nextBarrierXid = saved.NextBarrierXid

		for _, msg := range saved.Flow {
			if !validMessage(msg) {
				return nil, nil, 0, ErrBadSnapshot
			}
			dp.flow = append(dp.flow, msg)
		}
		for _, group := range saved.Groups {
			if group == "" || strings.TrimSpace(group) != group {
				return nil, nil, 0, ErrBadSnapshot
			}
			if _, exists := dp.groupSet[group]; exists {
				return nil, nil, 0, ErrBadSnapshot
			}
			dp.addGroupLocked(group)
		}
		for _, xid := range saved.Pending {
			if xid == 0 {
				return nil, nil, 0, ErrBadSnapshot
			}
			if _, exists := dp.pending[xid]; exists {
				return nil, nil, 0, ErrBadSnapshot
			}
			dp.pending[xid] = struct{}{}
		}
		dp.barrierPresent = saved.Barrier.Present
		dp.barrier = BarrierTicket{
			Dpid:    dp.dpid,
			Xid:     saved.Barrier.Xid,
			Pending: len(dp.pending),
			Role:    dp.role,
			Done:    len(dp.pending) == 0,
		}
		dpids[dp.dpid] = dp
	}

	bundles := make(map[string]*bundleData, len(doc.Bundles))
	for _, saved := range doc.Bundles {
		if saved.ID == "" || strings.TrimSpace(saved.ID) != saved.ID {
			return nil, nil, 0, ErrBadSnapshot
		}
		if _, exists := bundles[saved.ID]; exists {
			return nil, nil, 0, ErrBadSnapshot
		}
		if saved.State != BundleOpen && saved.State != BundleCommitted {
			return nil, nil, 0, ErrBadSnapshot
		}
		if _, ok := dpids[saved.Dpid]; !ok {
			return nil, nil, 0, ErrBadSnapshot
		}
		if saved.State == BundleCommitted && len(saved.Msgs) == 0 {
			return nil, nil, 0, ErrBadSnapshot
		}
		if g.limits.MaxMsgsPerBundle > 0 && len(saved.Msgs) > g.limits.MaxMsgsPerBundle {
			return nil, nil, 0, ErrBadSnapshot
		}
		for _, msg := range saved.Msgs {
			if !validMessage(msg) {
				return nil, nil, 0, ErrBadSnapshot
			}
		}
		bundles[saved.ID] = &bundleData{
			id:    saved.ID,
			dpid:  saved.Dpid,
			state: saved.State,
			req:   saved.Req,
			msgs:  append([]OfMsg(nil), saved.Msgs...),
		}
	}

	return bundles, dpids, doc.NextBundle, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *OpenflowBundleGuard) Restore(data []byte) error {
	doc, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}
	bundles, dpids, nextBundle, err := g.buildRestoredState(doc)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.bundles = bundles
	g.dpids = dpids
	g.nextBundle = nextBundle
	return nil
}
