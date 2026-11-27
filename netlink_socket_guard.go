// Package task governs netlink sockets: which protocol families a caller may
// open, how port identifiers and multicast groups are handed out, in which
// order messages are delivered and how a dump continues across a restart.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Socket states.
const (
	SocketOpened = "opened"
	SocketBound  = "bound"
	SocketClosed = "closed"
)

// Message kinds accepted by Send.
const (
	KindAck     = "ack"
	KindData    = "data"
	KindControl = "control"
)

// Policy verdicts recorded on the audit trail of Open.
const (
	PolicyAllow = "allow"
	PolicyDeny  = "deny"
)

// Errors returned by the guard.
var (
	ErrBadFamily         = errors.New("netlink family is invalid")
	ErrUnsupportedFamily = errors.New("netlink family is not supported")
	ErrPermission        = errors.New("credential cannot open this netlink family")
	ErrSocketLimit       = errors.New("socket table is full")
	ErrUnknownSocket     = errors.New("unknown netlink socket")
	ErrAlreadyBound      = errors.New("socket is already bound")
	ErrNotBound          = errors.New("socket is not bound")
	ErrPortInUse         = errors.New("port id is already bound")
	ErrBadPort           = errors.New("port id is out of range")
	ErrDuplicateGroup    = errors.New("multicast group is listed twice")
	ErrBadGroup          = errors.New("multicast group is out of range")
	ErrUnknownGroup      = errors.New("multicast group is not subscribed")
	ErrBadMessage        = errors.New("message kind is invalid")
	ErrQueueFull         = errors.New("socket queue is full")
	ErrBadMax            = errors.New("receive limit must be positive")
	ErrFamilyMismatch    = errors.New("dump request family does not match the socket")
	ErrUnknownCursor     = errors.New("unknown dump cursor")
	ErrBadObject         = errors.New("object record is invalid")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// GuardConfig bounds the socket table, the port range and the queues.
type GuardConfig struct {
	MaxSockets      int
	PortBase        uint32
	PortCount       uint32
	MaxQueue        int
	DumpPageSize    int
	AllowedFamilies []int
}

// Cred is the caller identity used for family admission.
type Cred struct {
	UID         int
	CapNetAdmin bool
	NS          string
}

// Socket is the record handed back by Open.
type Socket struct {
	ID     string   `json:"id"`
	Family int      `json:"family"`
	PortID uint32   `json:"port_id"`
	Gen    uint64   `json:"gen"`
	Groups []uint32 `json:"groups"`
	State  string   `json:"state"`
	NS     string   `json:"ns"`
	Queued int      `json:"queued"`
}

// Message is one queued netlink message.
type Message struct {
	Seq     uint64 `json:"seq"`
	Kind    string `json:"kind"`
	Group   uint32 `json:"group"`
	Payload string `json:"payload"`
	PortID  uint32 `json:"port_id"`
}

// BindResult reports the port and the groups a socket ended up with.
type BindResult struct {
	PortID uint32   `json:"port_id"`
	Groups []uint32 `json:"groups"`
	Gen    uint64   `json:"gen"`
}

// DumpRequest starts a dump on one socket.
type DumpRequest struct {
	Family   int
	Filter   string
	PageSize int
}

// DumpCursor is the resume token of one dump.
type DumpCursor struct {
	ID       string `json:"id"`
	SockID   string `json:"sock_id"`
	Gen      uint64 `json:"gen"`
	NS       string `json:"ns"`
	Next     int    `json:"next"`
	Total    int    `json:"total"`
	PageSize int    `json:"page_size"`
	Done     bool   `json:"done"`
}

// DumpPage is one page of a dump plus the advanced cursor.
type DumpPage struct {
	Items  []string   `json:"items"`
	Cursor DumpCursor `json:"cursor"`
}

// socketRecord is the mutable socket state.
type socketRecord struct {
	View  Socket
	Seq   uint64
	Queue []Message
	Gen   uint64
}

// objectRecord is one dumpable kernel object.
type objectRecord struct {
	Family int    `json:"family"`
	NS     string `json:"ns"`
	Name   string `json:"name"`
}

// NetlinkSocketGuard owns the socket table, the port table, the object table
// and the unfinished dumps.
type NetlinkSocketGuard struct {
	mu      sync.Mutex
	cfg     GuardConfig
	nextID  int
	sockets map[string]*socketRecord
	ports   map[uint32]string
	objects []objectRecord
	cursors map[string]*DumpCursor
	gen     uint64
}

// NewNetlinkSocketGuard builds a guard with the given bounds.
func NewNetlinkSocketGuard(cfg GuardConfig) *NetlinkSocketGuard {
	return &NetlinkSocketGuard{cfg: cfg}
}

// Open creates a socket for one protocol family.
func (g *NetlinkSocketGuard) Open(family int, cred Cred) (Socket, error) {
	return Socket{}, errNotImplemented
}

// Bind assigns a port identifier and the multicast groups of a socket.
func (g *NetlinkSocketGuard) Bind(sockID string, groups []uint32) (BindResult, error) {
	return BindResult{}, errNotImplemented
}

// BindPort binds a socket to an explicit port identifier.
func (g *NetlinkSocketGuard) BindPort(sockID string, portID uint32, groups []uint32) (BindResult, error) {
	return BindResult{}, errNotImplemented
}

// UnbindGroup drops one multicast group from a bound socket.
func (g *NetlinkSocketGuard) UnbindGroup(sockID string, group uint32) error {
	return errNotImplemented
}

// Send queues one message on a bound socket.
func (g *NetlinkSocketGuard) Send(sockID string, msg Message) error {
	return errNotImplemented
}

// Recv dequeues up to max messages in send order.
func (g *NetlinkSocketGuard) Recv(sockID string, max int) ([]Message, error) {
	return nil, errNotImplemented
}

// Dump starts a dump and returns its cursor.
func (g *NetlinkSocketGuard) Dump(sockID string, req DumpRequest) (DumpCursor, error) {
	return DumpCursor{}, errNotImplemented
}

// DumpPage returns the next page of a started dump.
func (g *NetlinkSocketGuard) DumpPage(cursorID string) (DumpPage, error) {
	return DumpPage{}, errNotImplemented
}

// RegisterObject adds one object that dumps can list.
func (g *NetlinkSocketGuard) RegisterObject(family int, ns, name string) error {
	return errNotImplemented
}

// Socket reads one socket view.
func (g *NetlinkSocketGuard) Socket(sockID string) (Socket, bool) {
	return Socket{}, false
}

// Close releases the socket, its port identifier and its queue.
func (g *NetlinkSocketGuard) Close(sockID string) error {
	return errNotImplemented
}

// Snapshot serialises the socket table, the bound ports and the cursors.
func (g *NetlinkSocketGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *NetlinkSocketGuard) Restore(data []byte) error {
	return errNotImplemented
}
