// Package task arbitrates the target side of an iSCSI session: login
// negotiation, the per connection StatSN waterline and the per session CmdSN
// window that gates how many commands may be outstanding at once.
package task

import (
	"context"
	"errors"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Status is the SCSI status carried by one command response.
type Status uint8

// SCSI status values exposed by Execute.
const (
	StatusGood           Status = 0x00
	StatusCheckCondition Status = 0x02
	StatusBusy           Status = 0x08
	StatusReject         Status = 0x10
)

// Login phases reported by SessionInfo.Phase.
const (
	PhaseLogin     = "login"
	PhaseFull      = "full-feature"
	PhaseFailed    = "failed"
	PhaseLoggedOut = "logged-out"
)

// Errors returned by the guard.
var (
	ErrBadLogin        = errors.New("initiator, target, portal and connection ids are required")
	ErrLoginRejected   = errors.New("login parameter is not accepted")
	ErrSessionLimit    = errors.New("session limit reached for this target")
	ErrUnknownSession  = errors.New("unknown session")
	ErrUnknownConn     = errors.New("unknown connection")
	ErrBadCommand      = errors.New("command is not usable")
	ErrWindowExhausted = errors.New("cmdsn window is exhausted")
	ErrCmdSNInvalid    = errors.New("cmdsn is outside the session window")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Limits bounds the live tables of the guard.
type Limits struct {
	MaxSessions        int
	MaxCmdWindow       uint32
	MaxConnsPerSession int
}

// LoginRequest is the operational parameter set offered at login.
type LoginRequest struct {
	Initiator string
	Target    string
	Portal    string
	ConnIDs   []string
	Params    map[string]string
}

// LoginResult is the outcome of one successful login.
type LoginResult struct {
	SessionID string
	Status    Status
	CmdSN     uint32
	TextKeys  []string
}

// Command is one SCSI command submitted against a session.
type Command struct {
	ITT       uint32
	ConnID    string
	Immediate bool
	Read      bool
	Payload   []byte
}

// Response is the recorded answer of one command.
type Response struct {
	ITT       uint32
	ConnID    string
	CmdSN     uint32
	StatSN    uint32
	Status    Status
	Immediate bool
}

// SessionInfo is the observable state of one session.
type SessionInfo struct {
	ID          string
	Initiator   string
	Target      string
	Phase       string
	NextCmdSN   uint32
	ExpCmdSN    uint32
	ConnIDs     []string
	Immediate   uint32
	DigestDrops uint32
}

// ConnInfo is the observable state of one connection.
type ConnInfo struct {
	ID        string
	SessionID string
	StatSN    uint32
	Lost      bool
	Reason    string
}

// TaskFn is the body run by TaskManage once its CmdSN is allocated.
type TaskFn func(assignedCmdSN uint32) error

// IscsiSessionGuardP3 owns the session table, the command window and the
// per connection response stream.
type IscsiSessionGuardP3 struct {
	limits Limits
}

// NewIscsiSessionGuardP3 builds a guard with the given caps.
func NewIscsiSessionGuardP3(limits Limits) *IscsiSessionGuardP3 {
	return &IscsiSessionGuardP3{limits: limits}
}

// Login negotiates the operational parameters and opens a session.
func (g *IscsiSessionGuardP3) Login(ctx context.Context, req LoginRequest) (LoginResult, error) {
	return LoginResult{}, errNotImplemented
}

// Execute places one command in the session window and returns its status.
func (g *IscsiSessionGuardP3) Execute(sessionID string, cmd Command) (Status, error) {
	return StatusReject, errNotImplemented
}

// TaskManage appends a task after the referenced CmdSN.
func (g *IscsiSessionGuardP3) TaskManage(sessionID string, refCmdSN uint32, fn TaskFn) error {
	return errNotImplemented
}

// Ack acknowledges every command below expCmdSN on the session.
func (g *IscsiSessionGuardP3) Ack(sessionID string, expCmdSN uint32) error {
	return errNotImplemented
}

// DigestFailed drops the PDU that failed its digest check on a connection.
func (g *IscsiSessionGuardP3) DigestFailed(connID string, cmdSN uint32) error {
	return errNotImplemented
}

// ConnectionLost marks a connection unusable and records the reason.
func (g *IscsiSessionGuardP3) ConnectionLost(connID, reason string) error {
	return errNotImplemented
}

// Reconnect brings a lost connection back on its session.
func (g *IscsiSessionGuardP3) Reconnect(sessionID, connID string) (ConnInfo, error) {
	return ConnInfo{}, errNotImplemented
}

// Session reads one session record.
func (g *IscsiSessionGuardP3) Session(id string) (SessionInfo, bool) {
	return SessionInfo{}, false
}

// Conn reads one connection record.
func (g *IscsiSessionGuardP3) Conn(id string) (ConnInfo, bool) {
	return ConnInfo{}, false
}

// Response returns the recorded answer of one ITT.
func (g *IscsiSessionGuardP3) Response(sessionID string, itt uint32) (Response, bool) {
	return Response{}, false
}

// Responses lists the response stream of one connection in StatSN order.
func (g *IscsiSessionGuardP3) Responses(connID string) []Response {
	return nil
}

// Attempts reports how many times the command body of an ITT really ran.
func (g *IscsiSessionGuardP3) Attempts(sessionID string, itt uint32) int {
	return 0
}

// Snapshot serialises sessions, connections, responses and ITT records.
func (g *IscsiSessionGuardP3) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *IscsiSessionGuardP3) Restore(data []byte) error {
	return errNotImplemented
}
