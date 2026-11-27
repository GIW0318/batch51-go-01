// Package task decides whether a reused HTTP/2 connection may also serve a
// second origin, keeps the advertised origin set of every connection and
// answers 421 for requests the connection is not allowed to carry.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Actions reported by Check.
const (
	ActionServe     = "serve"
	ActionMisdirect = "misdirect"
	ActionRefuse    = "refuse"
)

// Reason codes attached to a verdict.
const (
	ReasonServed         = "reason_served"
	ReasonCertNotCovered = "reason_cert_not_covered"
	ReasonNotDeclared    = "reason_not_declared"
	ReasonPortMismatch   = "reason_port_mismatch"
	ReasonSchemeRejected = "reason_scheme_rejected"
	ReasonBadAuthority   = "reason_bad_authority"
	ReasonPeerMismatch   = "reason_peer_mismatch"
)

// HTTP status codes carried by a verdict.
const (
	StatusServe     = 0
	StatusMisdirect = 421
	StatusRefuse    = 400
)

// Errors returned by the guard.
var (
	ErrBadAuthority   = errors.New("authority is malformed")
	ErrBadConn        = errors.New("connection id and certificate are required")
	ErrBadCertificate = errors.New("certificate has no usable subject alternative name")
	ErrUnknownConn    = errors.New("unknown connection")
	ErrNoOrigin       = errors.New("no usable origin in the candidate list")
	ErrOriginLimit    = errors.New("advertised origin limit exceeded")
	ErrEmptyFrame     = errors.New("origin frame is empty")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Certificate is the leaf certificate presented on a connection.
type Certificate struct {
	SANs []string `json:"sans"`
}

// ConnInfo is the connection state the guard keeps per connection id.
type ConnInfo struct {
	ID       string      `json:"id"`
	Protocol string      `json:"protocol"`
	Local    string      `json:"local"`
	Remote   string      `json:"remote"`
	Port     int         `json:"port"`
	Cert     Certificate `json:"cert"`
	Gen      int         `json:"gen"`
}

// Request is one request arriving on a connection.
type Request struct {
	Authority string `json:"authority"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	TLS       bool   `json:"tls"`
}

// Origin is a canonical origin: scheme, host and effective port.
type Origin struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
}

// OriginFrame is the ORIGIN frame content the guard is willing to advertise.
type OriginFrame struct {
	Origins   []string `json:"origins"`
	Count     int      `json:"count"`
	Truncated bool     `json:"truncated"`
}

// Verdict is the outcome of one Check call.
type Verdict struct {
	Action string `json:"action"`
	Status int    `json:"status"`
	Reason string `json:"reason"`
	ConnID string `json:"conn"`
	Origin string `json:"origin"`
}

// OriginPolicy bounds the guard tables and fixes the accepted schemes.
type OriginPolicy struct {
	AllowedSchemes    []string
	MaxOriginsPerConn int
	MaxAdvertised     int
	MaxCacheEntries   int
	Resolve           func(host string) []string
}

// Stats is a copy of the guard counters.
type Stats struct {
	Conns       int `json:"conns"`
	Checks      int `json:"checks"`
	Served      int `json:"served"`
	Misdirected int `json:"misdirected"`
	Refused     int `json:"refused"`
}

// CoalescingOriginGuard owns the connection registry, the per connection
// advertised sets and the counters.
type CoalescingOriginGuard struct {
	policy OriginPolicy
}

// NewCoalescingOriginGuard builds a guard with the given policy.
func NewCoalescingOriginGuard(cfg OriginPolicy) *CoalescingOriginGuard {
	return &CoalescingOriginGuard{policy: cfg}
}

// RegisterConn records a connection and its certificate.
func (g *CoalescingOriginGuard) RegisterConn(conn ConnInfo) error {
	return errNotImplemented
}

// CanCoalesce reports whether the registered connection may serve authority.
func (g *CoalescingOriginGuard) CanCoalesce(conn ConnInfo, authority string) (bool, error) {
	return false, errNotImplemented
}

// Advertise turns a candidate list into the frame that may be sent.
func (g *CoalescingOriginGuard) Advertise(origins []string) (OriginFrame, error) {
	return OriginFrame{}, errNotImplemented
}

// Install stores the advertised frame of one registered connection.
func (g *CoalescingOriginGuard) Install(connID string, frame OriginFrame) error {
	return errNotImplemented
}

// Check grades one request against the registered connection.
func (g *CoalescingOriginGuard) Check(conn ConnInfo, req Request) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// RotateCert replaces the certificate of a registered connection.
func (g *CoalescingOriginGuard) RotateCert(connID string, cert Certificate) error {
	return errNotImplemented
}

// OriginsOf returns the advertised origins of one connection.
func (g *CoalescingOriginGuard) OriginsOf(connID string) []string {
	return nil
}

// Stats returns the live counters.
func (g *CoalescingOriginGuard) Stats() Stats {
	return Stats{}
}

// Snapshot serialises connections, advertised sets and counters.
func (g *CoalescingOriginGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *CoalescingOriginGuard) Restore(data []byte) error {
	return errNotImplemented
}
