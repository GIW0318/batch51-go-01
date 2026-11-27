// Package task fronts the realm KDC and orchestrates the pre-authentication
// step of the Kerberos AS exchange, including replay accounting.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by the behaviours that are still missing.
var errNotImplemented = errors.New("not implemented")

// Reply statuses carried by AsReply.
const (
	StatusContinue = "continue"
	StatusIssued   = "issued"
)

// Reason codes attached to a reply and to a recorded failure.
const (
	ReasonOK               = "ok"
	ReasonPreauthRequired  = "preauth_required"
	ReasonSkewFuture       = "skew_future"
	ReasonSkewPast         = "skew_past"
	ReasonReplay           = "replay"
	ReasonWeakEType        = "weak_etype"
	ReasonUnsupportedEType = "unsupported_etype"
	ReasonArmorMismatch    = "armor_mismatch"
	ReasonPendingLimit     = "pending_limit"
	ReasonReplayFull       = "replay_full"
	ReasonFailureLimit     = "failure_limit"
	ReasonUnknownSession   = "unknown_session"
)

// Errors surfaced by the guard.
var (
	ErrBadRequest       = errors.New("client, realm and tenant are required")
	ErrUnknownSession   = errors.New("unknown as exchange session")
	ErrClockSkewFuture  = errors.New("timestamp is too far in the future")
	ErrClockSkewPast    = errors.New("timestamp is too far in the past")
	ErrReplay           = errors.New("preauth timestamp already recorded")
	ErrReplayCacheFull  = errors.New("replay cache is full")
	ErrWeakEType        = errors.New("etype is below the policy floor")
	ErrUnsupportedEType = errors.New("etype is not enabled for this realm")
	ErrArmorMismatch    = errors.New("fast armor binding does not match the pending request")
	ErrPendingLimit     = errors.New("too many pending sessions for this account")
	ErrTooManyFailures  = errors.New("account is locked after repeated preauth failures")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// RealmPolicy is the start up configuration of the guard for one tenant.
type RealmPolicy struct {
	Tenant        string
	Realm         string
	AllowedETypes []int
	MinEType      int
	ClockSkew     time.Duration
	MaxReplay     int
	MaxPending    int
	MaxFailures   int
	RequireFAST   bool
	Now           func() time.Time
}

// Policy is the read only tenant view returned by Policy.
type Policy struct {
	Tenant      string
	Realm       string
	MinEType    int
	ETypes      []int
	ClockSkew   time.Duration
	RequireFAST bool
}

// AsRequest is one inbound AS-REQ that has not presented pre-authentication.
type AsRequest struct {
	Client string
	Realm  string
	Tenant string
	Nonce  string
	FAST   bool
	Armor  string
}

// PreauthData is the PA-ENC-TIMESTAMP blob a client sends on the second leg.
type PreauthData struct {
	Client string
	Realm  string
	EType  int
	Time   time.Time
	FAST   bool
	Armor  string
}

// AsReply is the answer handed back to the KDC front end.
type AsReply struct {
	SessionID string
	Status    string
	Reason    string
	EType     int
	Salt      string
	ETypes    []int
	Seq       int64
}

// TicketStats is the per tenant counter view.
type TicketStats struct {
	Tenant   string
	Issued   int
	Rejected int
	Pending  int
	Failures int
}

// AsPreauthGuard owns the pending sessions, the replay cache and the per
// account failure counters.
type AsPreauthGuard struct {
	cfg RealmPolicy
}

// NewAsPreauthGuard builds a guard for one tenant policy.
func NewAsPreauthGuard(cfg RealmPolicy) *AsPreauthGuard {
	return &AsPreauthGuard{cfg: cfg}
}

// Policy returns the resolved, read only view for one tenant.
func (g *AsPreauthGuard) Policy(tenant string) Policy {
	return Policy{}
}

// Begin opens a pre-authentication session and asks the client for a timestamp.
func (g *AsPreauthGuard) Begin(req AsRequest) (AsReply, error) {
	return AsReply{}, errNotImplemented
}

// Preauth validates PA-ENC-TIMESTAMP and issues the reply on success.
func (g *AsPreauthGuard) Preauth(sessionID string, pa PreauthData) (AsReply, error) {
	return AsReply{}, errNotImplemented
}

// CheckReplay decides and records one (client, realm, timestamp, etype) tuple
// in a single atomic step.
func (g *AsPreauthGuard) CheckReplay(client, realm string, ts time.Time, etype int) error {
	return errNotImplemented
}

// Pending reads the request waiting behind a session id.
func (g *AsPreauthGuard) Pending(sessionID string) (AsRequest, bool) {
	return AsRequest{}, false
}

// Failures reads the accumulated failure count of one client.
func (g *AsPreauthGuard) Failures(client string) int {
	return 0
}

// Stats reports the counters of one tenant.
func (g *AsPreauthGuard) Stats(tenant string) TicketStats {
	return TicketStats{}
}

// ReplaySize reports how many tuples the replay cache holds.
func (g *AsPreauthGuard) ReplaySize() int {
	return 0
}

// Snapshot serialises replay entries, failures and the sequence watermark.
func (g *AsPreauthGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *AsPreauthGuard) Restore(data []byte) error {
	return errNotImplemented
}
