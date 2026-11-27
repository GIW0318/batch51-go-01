package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Session states.
const (
	SessionPending = "pending"
	SessionActive  = "active"
)

// Guard errors.
var (
	ErrInvalidLogin       = errors.New("portal, initiator and target are required")
	ErrUnknownSession     = errors.New("unknown session")
	ErrSessionLimit       = errors.New("session limit reached")
	ErrChallengeMismatch  = errors.New("chap challenge does not match the session")
	ErrChallengeReused    = errors.New("chap challenge was already consumed")
	ErrMutualAuthRequired = errors.New("target side chap answer is required")
	ErrAuthLocked         = errors.New("chap attempt window exhausted")
	ErrNotAuthenticated   = errors.New("session has not completed the login stages")
	ErrInitiatorMismatch  = errors.New("initiator does not own this session")
	ErrLunDenied          = errors.New("lun is not inside the initiator mask")
	ErrLunLimit           = errors.New("per session lun limit reached")
	ErrInvalidMask        = errors.New("lun mask is invalid")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// LoginRequest is the first stage of an iSCSI login.
type LoginRequest struct {
	Initiator  string `json:"initiator"`
	Target     string `json:"target"`
	CHAPMethod string `json:"chap_method"`
}

// ChapResponse answers the challenge issued at login.
type ChapResponse struct {
	ChallengeID string `json:"challenge_id"`
	Response    string `json:"response"`
	Mutual      bool   `json:"mutual"`
}

// Session is one negotiated iSCSI session.
type Session struct {
	ID          string `json:"id"`
	Portal      string `json:"portal"`
	Initiator   string `json:"initiator"`
	Target      string `json:"target"`
	State       string `json:"state"`
	ChallengeID string `json:"challenge_id"`
	ExpCmdSN    int    `json:"exp_cmd_sn"`
	Commands    int    `json:"commands"`
	Luns        int    `json:"luns"`
}

// LunGrant is the outcome of a successful LUN mapping.
type LunGrant struct {
	SessionID      string `json:"session_id"`
	Lun            int    `json:"lun"`
	Initiator      string `json:"initiator"`
	MaskGeneration int    `json:"mask_generation"`
}

// IscsiPolicy bounds one guard.
type IscsiPolicy struct {
	MaxSessions             int
	MaxSessionsPerInitiator int
	MaxLunsPerSession       int
	MaxMaskEntries          int
	ChapWindow              int
	RequireMutualAuth       bool
	MaskGeneration          int
}

// IscsiSessionGuard owns the session table and the per initiator LUN masks.
type IscsiSessionGuard struct {
	cfg IscsiPolicy
}

// NewIscsiSessionGuard builds a guard with the given policy.
func NewIscsiSessionGuard(cfg IscsiPolicy) *IscsiSessionGuard {
	return &IscsiSessionGuard{cfg: cfg}
}

// Login opens a session in the pending state and issues a CHAP challenge.
func (g *IscsiSessionGuard) Login(portal string, req LoginRequest) (Session, error) {
	return Session{}, errNotImplemented
}

// Auth completes the CHAP handshake, including the mutual answer when the
// policy requires it.
func (g *IscsiSessionGuard) Auth(sessionID string, chap ChapResponse) error {
	return errNotImplemented
}

// MapLun grants one LUN to the session when the initiator mask allows it.
func (g *IscsiSessionGuard) MapLun(sessionID string, lun int, initiator string) (LunGrant, error) {
	return LunGrant{}, errNotImplemented
}

// Recover advances the command window and replays a duplicate CmdSN
// idempotently.
func (g *IscsiSessionGuard) Recover(sessionID string, cmdSN int) (Session, error) {
	return Session{}, errNotImplemented
}

// Logout closes a session and releases its LUNs.
func (g *IscsiSessionGuard) Logout(sessionID string) error {
	return errNotImplemented
}

// Sessions lists the live sessions ordered by session id.
func (g *IscsiSessionGuard) Sessions() []Session {
	return nil
}

// LunMask lists the masked LUNs of one initiator in ascending order.
func (g *IscsiSessionGuard) LunMask(initiator string) []int {
	return nil
}

// SetLunMask replaces the mask of one initiator and bumps the generation.
func (g *IscsiSessionGuard) SetLunMask(initiator string, luns []int) error {
	return errNotImplemented
}

// MaskGeneration reports the current mask generation.
func (g *IscsiSessionGuard) MaskGeneration() int {
	return 0
}

// Snapshot serialises the session table, the masks and the command watermarks.
func (g *IscsiSessionGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *IscsiSessionGuard) Restore(data []byte) error {
	return errNotImplemented
}
