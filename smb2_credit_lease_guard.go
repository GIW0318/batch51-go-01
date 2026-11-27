// Package task runs the SMB2 server side credit window, the lease state
// machine and the durable handle table used after a reconnect.
package task

import "errors"

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

// Smb2CreditLeaseGuard owns the connection table, the credit windows, the
// lease table and the durable handle table.
type Smb2CreditLeaseGuard struct {
	cfg ServerConfig
}

// NewSmb2CreditLeaseGuard builds a guard with the given capabilities.
func NewSmb2CreditLeaseGuard(cfg ServerConfig) *Smb2CreditLeaseGuard {
	return &Smb2CreditLeaseGuard{cfg: cfg}
}

// Negotiate opens one connection and grants its credit window.
func (g *Smb2CreditLeaseGuard) Negotiate(req NegotiateReq) (NegotiateResp, error) {
	return NegotiateResp{}, errNotImplemented
}

// Charge consumes credits for one request and reports what is left.
func (g *Smb2CreditLeaseGuard) Charge(connID string, cost uint16) (uint16, error) {
	return 0, errNotImplemented
}

// Refund returns unused credits to the window, clamped to its size.
func (g *Smb2CreditLeaseGuard) Refund(connID string, credits uint16) (uint16, error) {
	return 0, errNotImplemented
}

// Open creates or reopens a file handle, optionally under a lease key.
func (g *Smb2CreditLeaseGuard) Open(connID string, req CreateReq) (FileID, error) {
	return FileID{}, errNotImplemented
}

// Break starts a server initiated lease downgrade.
func (g *Smb2CreditLeaseGuard) Break(leaseKey string, to LeaseState) (BreakResult, error) {
	return BreakResult{}, errNotImplemented
}

// AckBreak settles the pending downgrade.
func (g *Smb2CreditLeaseGuard) AckBreak(leaseKey string, state LeaseState) error {
	return errNotImplemented
}

// Reconnect rebinds a durable handle to a new connection of the same client.
func (g *Smb2CreditLeaseGuard) Reconnect(connID string, fileID FileID) (Handle, error) {
	return Handle{}, errNotImplemented
}

// Disconnect drops one transport connection and its unacknowledged credits.
func (g *Smb2CreditLeaseGuard) Disconnect(connID string) error {
	return errNotImplemented
}

// LeaseOf reads one lease record.
func (g *Smb2CreditLeaseGuard) LeaseOf(leaseKey string) (LeaseEntry, bool) {
	return LeaseEntry{}, false
}

// Stats reports the live credit accounting.
func (g *Smb2CreditLeaseGuard) Stats() CreditStats {
	return CreditStats{}
}

// Snapshot serialises connections, leases and durable handles.
func (g *Smb2CreditLeaseGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *Smb2CreditLeaseGuard) Restore(data []byte) error {
	return errNotImplemented
}
