// Package task tracks referral codes, invitee milestones and the reward
// ledger that pays out for them.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Milestone is one progress node of an invited account.
type Milestone string

// Known milestones, in ledger order.
const (
	MilestoneLevel10 Milestone = "level_10"
	MilestoneLevel30 Milestone = "level_30"
	MilestonePaid    Milestone = "first_paid"
	MilestoneActive  Milestone = "active_7d"
)

// MilestoneOrder lists the milestones in the order the ledger reports them.
var MilestoneOrder = []Milestone{MilestoneLevel10, MilestoneLevel30, MilestonePaid, MilestoneActive}

// RetryAfter is attached to a full invite code so the caller knows when the
// code may accept another invitee.
type CodeFullError struct {
	RetryAfter time.Time
}

// Error implements the error interface.
func (e *CodeFullError) Error() string { return "invite code is full" }

// Unwrap exposes the sentinel error behind the retry hint.
func (e *CodeFullError) Unwrap() error { return ErrCodeFull }

// Sentinel errors returned by the ledger.
var (
	ErrEmptyAccount     = errors.New("account id is required")
	ErrAccountExists    = errors.New("account is already registered")
	ErrUnknownAccount   = errors.New("account is not registered")
	ErrUnknownCode      = errors.New("invite code is unknown")
	ErrSelfReferral     = errors.New("account cannot bind its own invite code")
	ErrAlreadyBound     = errors.New("invitee is already bound")
	ErrBindWindowClosed = errors.New("binding window has closed")
	ErrCodeLimit        = errors.New("account reached its invite code limit")
	ErrCodeFull         = errors.New("invite code reached its binding limit")
	ErrNotBound         = errors.New("invitee is not bound to any code")
	ErrBadDelta         = errors.New("milestone delta must be positive")
	ErrUnknownMilestone = errors.New("milestone is not supported")
	ErrBadReason        = errors.New("reverse reason is required")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// ReferralPolicy carries the binding window, the code caps and the reward
// table of the programme.
type ReferralPolicy struct {
	BindWindowHours    int
	MaxBindingsPerCode int
	MaxCodesPerAccount int
	RetryAfterSeconds  int
	Thresholds         map[Milestone]int64
	Rewards            map[Milestone]int64
}

// Fingerprint identifies the device and the payment instrument of an account.
type Fingerprint struct {
	Device  string
	Payment string
}

// Bond is the relation between an invitee and the invite code it used.
type Bond struct {
	InviteeID   string
	InviterID   string
	Code        string
	BoundAt     time.Time
	Quarantined bool
}

// Progress is the milestone ledger of one invitee.
type Progress struct {
	InviteeID string
	Totals    map[Milestone]int64
	Credited  map[Milestone]int64
	Unlocked  []Milestone
	Claimed   []Milestone
}

// Grant is the outcome of one claim.
type Grant struct {
	AccountID  string
	Amount     int64
	Milestones []Milestone
	Balance    int64
	Seq        int64
}

// Clawback is the outcome of one reversal.
type Clawback struct {
	AccountID string
	Reason    string
	Reversed  int64
	Recovered int64
	Balance   int64
	Seq       int64
}

// ReferralBondLedger owns the accounts, the codes, the bonds and the ledger.
type ReferralBondLedger struct {
	policy ReferralPolicy
}

// NewReferralBondLedger builds a ledger with the given policy.
func NewReferralBondLedger(policy ReferralPolicy) *ReferralBondLedger {
	return &ReferralBondLedger{policy: policy}
}

// Register records an account and the moment it signed up.
func (l *ReferralBondLedger) Register(accountID string, at time.Time) error {
	return errNotImplemented
}

// IssueCode mints one invite code for an account.
func (l *ReferralBondLedger) IssueCode(accountID string) (string, error) {
	return "", errNotImplemented
}

// Codes lists the invite codes of an account in issue order.
func (l *ReferralBondLedger) Codes(accountID string) []string {
	return nil
}

// Bind attaches an invitee to an invite code.
func (l *ReferralBondLedger) Bind(inviteeID, code string, at time.Time) (Bond, error) {
	return Bond{}, errNotImplemented
}

// BindWithFingerprint binds an invitee together with its device and payment
// fingerprint.
func (l *ReferralBondLedger) BindWithFingerprint(inviteeID, code string, fp Fingerprint, at time.Time) (Bond, error) {
	return Bond{}, errNotImplemented
}

// Accrue books milestone progress for an invitee.
func (l *ReferralBondLedger) Accrue(inviteeID string, milestone Milestone, delta int64) error {
	return errNotImplemented
}

// Progress reads the milestone ledger of one invitee.
func (l *ReferralBondLedger) Progress(inviteeID string) (Progress, error) {
	return Progress{}, errNotImplemented
}

// Claim pays out every unlocked milestone that has not been paid yet.
func (l *ReferralBondLedger) Claim(accountID string) (Grant, error) {
	return Grant{}, errNotImplemented
}

// Reverse reclaims unlocked rewards and records a clawback.
func (l *ReferralBondLedger) Reverse(accountID, reason string) (Clawback, error) {
	return Clawback{}, errNotImplemented
}

// Balance reads the reward balance of one account.
func (l *ReferralBondLedger) Balance(accountID string) (int64, error) {
	return 0, errNotImplemented
}

// BondOf reads the bond of one invitee.
func (l *ReferralBondLedger) BondOf(inviteeID string) (Bond, bool) {
	return Bond{}, false
}

// Quarantined lists the quarantined invitees in lexical order.
func (l *ReferralBondLedger) Quarantined() []string {
	return nil
}

// Snapshot serialises the accounts, the codes, the bonds and the ledger.
func (l *ReferralBondLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *ReferralBondLedger) Restore(data []byte) error {
	return errNotImplemented
}
