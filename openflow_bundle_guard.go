// Package task orders the flow table transactions a controller pushes at a
// switch: bundles commit as a unit, barriers pin the visible order, and the
// controller role decides which side may write.
package task

import "errors"

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

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

// OpenflowBundleGuard owns the per dpid tables, bundles and barrier state.
type OpenflowBundleGuard struct {
	limits Limits
}

// NewOpenflowBundleGuard builds a guard with the given caps.
func NewOpenflowBundleGuard(limits Limits) *OpenflowBundleGuard {
	return &OpenflowBundleGuard{limits: limits}
}

// OpenBundle starts a transaction envelope on one datapath.
func (g *OpenflowBundleGuard) OpenBundle(dpid string, req BundleReq) (BundleID, error) {
	return "", errNotImplemented
}

// Add appends one message to an open bundle.
func (g *OpenflowBundleGuard) Add(bundleID string, msg OfMsg) error {
	return errNotImplemented
}

// Commit applies the whole bundle or nothing at all.
func (g *OpenflowBundleGuard) Commit(bundleID string, props CommitProps) (CommitResult, error) {
	return CommitResult{}, errNotImplemented
}

// Barrier issues a fence and reports how many replies are still outstanding.
func (g *OpenflowBundleGuard) Barrier(dpid string) (BarrierTicket, error) {
	return BarrierTicket{}, errNotImplemented
}

// BarrierReply consumes one asynchronous reply for a pending message xid.
func (g *OpenflowBundleGuard) BarrierReply(dpid string, xid uint32) error {
	return errNotImplemented
}

// BarrierState reads the last fence of a datapath.
func (g *OpenflowBundleGuard) BarrierState(dpid string) (BarrierTicket, bool) {
	return BarrierTicket{}, false
}

// SetRole moves the controller role forward by one generation.
func (g *OpenflowBundleGuard) SetRole(dpid string, role Role, gen uint64) (RoleState, error) {
	return RoleState{}, errNotImplemented
}

// Reconnect re-arms the fences that were not acknowledged before the switch
// dropped and reports how many were re-issued.
func (g *OpenflowBundleGuard) Reconnect(dpid string) (int, error) {
	return 0, errNotImplemented
}

// BundleState reads one transaction envelope.
func (g *OpenflowBundleGuard) BundleState(bundleID string) (Bundle, bool) {
	return Bundle{}, false
}

// FlowTable lists the messages that are visible in the datapath today.
func (g *OpenflowBundleGuard) FlowTable(dpid string) []OfMsg {
	return nil
}

// GroupTable lists the group ids the datapath knows today.
func (g *OpenflowBundleGuard) GroupTable(dpid string) []string {
	return nil
}

// Snapshot serialises bundles, roles, tables and outstanding replies.
func (g *OpenflowBundleGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *OpenflowBundleGuard) Restore(data []byte) error {
	return errNotImplemented
}
