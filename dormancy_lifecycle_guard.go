// Package task grades tenant accounts by how long they have been dormant and
// drives the idle/warning/suspended/reclaimable lifecycle.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by the behaviours that are still missing.
var errNotImplemented = errors.New("not implemented")

// Dormancy levels of one account.
const (
	LevelActive      = "active"
	LevelIdle        = "idle"
	LevelWarning     = "warning"
	LevelSuspended   = "suspended"
	LevelReclaimable = "reclaimable"
)

// Reactivation proof kinds, ordered from weakest to strongest.
const (
	ProofSelfService = "self_service"
	ProofManager     = "manager"
	ProofInPerson    = "in_person"
)

// Errors surfaced by the guard.
var (
	ErrBlankAccount       = errors.New("account is required")
	ErrBadReason          = errors.New("exemption reason is required")
	ErrBadSource          = errors.New("activity source is required")
	ErrUnknownAccount     = errors.New("account is not tracked")
	ErrAccountExists      = errors.New("account is already tracked")
	ErrLoginBlocked       = errors.New("account may not sign in while dormant")
	ErrExemptionLimit     = errors.New("tenant exemption budget is exhausted")
	ErrNoticeBudget       = errors.New("tenant notice budget is exhausted")
	ErrProofRejected      = errors.New("reactivation proof is not sufficient")
	ErrIdentifierReserved = errors.New("login identifier is inside its retention window")
	ErrNoticeNotFound     = errors.New("notice is not outstanding")
	ErrBadPolicy          = errors.New("dormancy policy is invalid")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// DormancyPolicy holds the tenant thresholds and scan budgets.
type DormancyPolicy struct {
	Tenant            string
	IdleAfter         time.Duration
	WarningAfter      time.Duration
	SuspendAfter      time.Duration
	ReclaimAfter      time.Duration
	HoldAfter         time.Duration
	ShardSize         int
	BatchLimit        int
	MaxPendingNotices int
	MaxExempt         int
	Now               func() time.Time
}

// DormancyState is the readable view of one tracked account.
type DormancyState struct {
	Account      string
	Tenant       string
	Level        string
	LastActive   time.Time
	Since        time.Time
	WarningCount int
	Reclaimed    bool
	HoldUntil    time.Time
}

// Transition is one level change produced by a sweep.
type Transition struct {
	Account string
	From    string
	To      string
	At      time.Time
	Seq     int64
	Batch   int
	Note    string
}

// Notice is a cancelable warning raised when an account turns dormant.
type Notice struct {
	Account    string
	Tenant     string
	Seq        int64
	Level      string
	Cancelable bool
	Cancelled  bool
}

// Proof is the evidence supplied when an account is reactivated.
type Proof struct {
	Kind     string
	Verified bool
	Ticket   string
}

// DormancyLifecycleGuard owns the account table, the transition history and
// the outstanding notices of one tenant.
type DormancyLifecycleGuard struct {
	cfg DormancyPolicy
}

// NewDormancyLifecycleGuard builds a guard for one tenant policy.
func NewDormancyLifecycleGuard(cfg DormancyPolicy) *DormancyLifecycleGuard {
	return &DormancyLifecycleGuard{cfg: cfg}
}

// Register starts tracking an account that has just been created.
func (g *DormancyLifecycleGuard) Register(account string, at time.Time) error {
	return errNotImplemented
}

// Touch records a successful sign in and moves the activity watermark.
func (g *DormancyLifecycleGuard) Touch(account string, at time.Time, source string) error {
	return errNotImplemented
}

// Sweep advances eligible accounts by at most one level and returns the
// transitions made by this pass.
func (g *DormancyLifecycleGuard) Sweep(now time.Time) ([]Transition, error) {
	return nil, errNotImplemented
}

// State reads the current dormancy state of one account.
func (g *DormancyLifecycleGuard) State(account string) (DormancyState, error) {
	return DormancyState{}, errNotImplemented
}

// Exempt keeps an account out of the lifecycle until the given instant.
func (g *DormancyLifecycleGuard) Exempt(account string, reason string, until time.Time) error {
	return errNotImplemented
}

// Reactivate brings a dormant account back with the proof its depth requires.
func (g *DormancyLifecycleGuard) Reactivate(account string, proof Proof) error {
	return errNotImplemented
}

// ReloadPolicy swaps the tenant thresholds without touching account state.
func (g *DormancyLifecycleGuard) ReloadPolicy(cfg DormancyPolicy) error {
	return errNotImplemented
}

// History returns the transitions of one account, oldest first.
func (g *DormancyLifecycleGuard) History(account string) []Transition {
	return nil
}

// Notices returns the outstanding and cancelled notices of one tenant.
func (g *DormancyLifecycleGuard) Notices(tenant string) []Notice {
	return nil
}

// PendingNotices counts the notices that are still outstanding.
func (g *DormancyLifecycleGuard) PendingNotices(tenant string) int {
	return 0
}

// CancelNotice withdraws the outstanding warning of one account.
func (g *DormancyLifecycleGuard) CancelNotice(account string) error {
	return errNotImplemented
}

// Snapshot serialises accounts, history, notices and the scan cursor.
func (g *DormancyLifecycleGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *DormancyLifecycleGuard) Restore(data []byte) error {
	return errNotImplemented
}
