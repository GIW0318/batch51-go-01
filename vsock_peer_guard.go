// Package task admits vsock peers between the hypervisor and its guests: it
// binds listening ports to a tenant and a CID allow list, gates the handshake
// on peer credentials and keeps a per direction credit window.
package task

import "errors"

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Reserved context ids of the vsock address space.
const (
	CidHypervisor uint32 = 0
	CidLocal      uint32 = 1
	CidHost       uint32 = 2
	CidAny        uint32 = 0xFFFFFFFF
)

// Connection sides.
const (
	SideHost  = "host"
	SideGuest = "guest"
)

// Connection states.
const (
	StateHandshake   = "handshaking"
	StateEstablished = "established"
	StateHalfClosed  = "half_closed"
	StateClosed      = "closed"
)

// Shutdown directions.
const (
	ShutdownRead  = "read"
	ShutdownWrite = "write"
	ShutdownBoth  = "both"
)

// Errors returned by the guard.
var (
	ErrBadCID            = errors.New("listening context id is invalid")
	ErrBadPort           = errors.New("port is invalid")
	ErrBadPolicy         = errors.New("port policy is invalid")
	ErrPortInUse         = errors.New("port is already bound")
	ErrUnknownListener   = errors.New("listener not found")
	ErrBadSide           = errors.New("peer side is invalid")
	ErrCidMismatch       = errors.New("peer context id belongs to the other side")
	ErrCidDenied         = errors.New("peer context id is not allowed on this port")
	ErrNoListener        = errors.New("no listener on this port")
	ErrConnLimit         = errors.New("connection limit reached")
	ErrConnNotFound      = errors.New("connection not found")
	ErrBadCredential     = errors.New("peer credential is invalid")
	ErrTenantMismatch    = errors.New("peer tenant differs from the port tenant")
	ErrAlreadyAuthorized = errors.New("connection is already authorized")
	ErrNotEstablished    = errors.New("connection is not established")
	ErrCreditExhausted   = errors.New("peer credit is exhausted")
	ErrBadCredit         = errors.New("credit amount is invalid")
	ErrShutdown          = errors.New("connection direction is shut down")
	ErrBadShutdown       = errors.New("shutdown mode is invalid")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// PortPolicy is what one listening port accepts.
type PortPolicy struct {
	Tenant      string   `json:"tenant"`
	AllowedCIDs []uint32 `json:"allowed_cids"`
	RequireAuth bool     `json:"require_auth"`
	MaxCredit   uint32   `json:"max_credit"`
}

// PeerAddr is the remote end of one connection attempt.
type PeerAddr struct {
	Side string `json:"side"`
	CID  uint32 `json:"cid"`
	Port uint32 `json:"port"`
}

// ConnectRequest targets a local listening port.
type ConnectRequest struct {
	Port  uint32 `json:"port"`
	Nonce string `json:"nonce"`
}

// PeerCred is what the peer presents during the handshake.
type PeerCred struct {
	Tenant string `json:"tenant"`
	UID    uint32 `json:"uid"`
	Token  string `json:"token"`
}

// Decision is the explainable outcome of the handshake.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
	Tenant  string `json:"tenant"`
}

// Window is the credit picture of one connection.
type Window struct {
	Send       uint32 `json:"send"`
	Recv       uint32 `json:"recv"`
	Granted    uint32 `json:"granted"`
	Clamped    bool   `json:"clamped"`
	Generation uint64 `json:"generation"`
}

// Listener is one bound port of the host.
type Listener struct {
	ID          string   `json:"id"`
	CID         uint32   `json:"cid"`
	Port        uint32   `json:"port"`
	Tenant      string   `json:"tenant"`
	AllowedCIDs []uint32 `json:"allowed_cids"`
	MaxCredit   uint32   `json:"max_credit"`
}

// Conn is one live connection.
type Conn struct {
	ID            string   `json:"id"`
	ListenerID    string   `json:"listener_id"`
	Tenant        string   `json:"tenant"`
	Peer          PeerAddr `json:"peer"`
	State         string   `json:"state"`
	Generation    uint64   `json:"generation"`
	SendCredit    uint32   `json:"send_credit"`
	RecvCredit    uint32   `json:"recv_credit"`
	Authenticated bool     `json:"authenticated"`
	LocalShutdown string   `json:"local_shutdown"`
}

// VsockConfig bounds the tables and the credit windows.
type VsockConfig struct {
	MaxConnsPerEndpoint int
	MaxConns            int
	BaseCredit          uint32
	MaxCredit           uint32
}

// VsockPeerGuard owns the listener table, the connections and the credits.
type VsockPeerGuard struct {
	cfg VsockConfig
}

// NewVsockPeerGuard builds a guard with the given caps.
func NewVsockPeerGuard(cfg VsockConfig) *VsockPeerGuard {
	return &VsockPeerGuard{cfg: cfg}
}

// Listen binds one host port to a tenant and a CID allow list.
func (g *VsockPeerGuard) Listen(cid, port uint32, policy PortPolicy) (Listener, error) {
	return Listener{}, errNotImplemented
}

// CloseListener removes one bound port.
func (g *VsockPeerGuard) CloseListener(listenerID string) error {
	return errNotImplemented
}

// Connect opens a handshaking connection towards a host port.
func (g *VsockPeerGuard) Connect(peer PeerAddr, req ConnectRequest) (Conn, error) {
	return Conn{}, errNotImplemented
}

// Authorize checks the peer credential and opens the credit windows.
func (g *VsockPeerGuard) Authorize(connID string, cred PeerCred) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Credit books bytes against the send window of one connection.
func (g *VsockPeerGuard) Credit(connID string, bytes uint32) (Window, error) {
	return Window{}, errNotImplemented
}

// Grant advertises receive credit on one connection.
func (g *VsockPeerGuard) Grant(connID string, bytes uint32) (Window, error) {
	return Window{}, errNotImplemented
}

// Shutdown closes one direction of a connection.
func (g *VsockPeerGuard) Shutdown(connID string, how string) error {
	return errNotImplemented
}

// ConnState reads one live connection.
func (g *VsockPeerGuard) ConnState(connID string) (Conn, bool) {
	return Conn{}, false
}

// Listeners lists the bound ports ordered by port number.
func (g *VsockPeerGuard) Listeners() []Listener {
	return nil
}

// SetAllowedCIDs rewrites the allow list of one listener for new connections.
func (g *VsockPeerGuard) SetAllowedCIDs(listenerID string, cids []uint32) error {
	return errNotImplemented
}

// Snapshot serialises the listeners, the connections and the credits.
func (g *VsockPeerGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *VsockPeerGuard) Restore(data []byte) error {
	return errNotImplemented
}
