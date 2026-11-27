// Package task decides directory password policy at bind time and keeps the
// password change ledger: expiry grace, forced change after a temporary
// password and reuse of historical passwords.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Account states reported by Status and Bind.
const (
	StateActive       = "active"
	StateMustChange   = "must_change"
	StateExpiredGrace = "expired_grace"
	StateExpired      = "expired"
	StateLocked       = "locked"
)

// Errors returned by the policy engine.
var (
	ErrMissingField      = errors.New("account, tenant and password are required")
	ErrUnknownTenant     = errors.New("tenant has no directory policy")
	ErrAccountExists     = errors.New("account already provisioned")
	ErrAccountNotFound   = errors.New("account not found")
	ErrBadPassword       = errors.New("password does not match")
	ErrAccountLocked     = errors.New("account is locked")
	ErrPasswordExpired   = errors.New("password expired and the grace budget is spent")
	ErrPasswordPolicy    = errors.New("password violates the tenant policy")
	ErrPasswordReused    = errors.New("password was used recently")
	ErrSessionInvalid    = errors.New("session is not valid for this account")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Policy is the per tenant password policy in force.
type Policy struct {
	Tenant         string `json:"tenant"`
	MinLength      int    `json:"min_length"`
	MaxLength      int    `json:"max_length"`
	MinClasses     int    `json:"min_classes"`
	HistoryDepth   int    `json:"history_depth"`
	MaxAgeDays     int    `json:"max_age_days"`
	GraceLogins    int    `json:"grace_logins"`
	MaxFailures    int    `json:"max_failures"`
	LockoutMinutes int    `json:"lockout_minutes"`
	Version        int    `json:"version"`
}

// PolicyConfig wires the tenant policies and the injected clock.
type PolicyConfig struct {
	DefaultTenant string
	Policies      map[string]Policy
	Now           func() time.Time
}

// BindResult is the answer of one bind attempt.
type BindResult struct {
	Allowed         bool   `json:"allowed"`
	MustChange      bool   `json:"must_change"`
	State           string `json:"state"`
	Tenant          string `json:"tenant"`
	SessionID       string `json:"session_id"`
	GraceRemaining  int    `json:"grace_remaining"`
	AttemptsLeft    int    `json:"attempts_left"`
	PasswordAgeDays int    `json:"password_age_days"`
}

// ChangeResult is the answer of one password change.
type ChangeResult struct {
	Changed         bool   `json:"changed"`
	Tenant          string `json:"tenant"`
	SessionsRevoked int    `json:"sessions_revoked"`
	HistoryRetained int    `json:"history_retained"`
}

// AccountStatus is the operator view of one account.
type AccountStatus struct {
	Account         string    `json:"account"`
	Tenant          string    `json:"tenant"`
	State           string    `json:"state"`
	Failures        int       `json:"failures"`
	GraceUsed       int       `json:"grace_used"`
	MustChange      bool      `json:"must_change"`
	PasswordAgeDays int       `json:"password_age_days"`
	Sessions        int       `json:"sessions"`
	HistoryDepth    int       `json:"history_depth"`
	PolicyVersion   int       `json:"policy_version"`
	LastChangedAt   time.Time `json:"last_changed_at"`
}

// DirectoryPasswordPolicy owns the account table, the digest history rings and
// the session index.
type DirectoryPasswordPolicy struct {
	cfg PolicyConfig
}

// NewDirectoryPasswordPolicy builds an engine with the given policies.
func NewDirectoryPasswordPolicy(cfg PolicyConfig) *DirectoryPasswordPolicy {
	return &DirectoryPasswordPolicy{cfg: cfg}
}

// Provision creates one account with an initial password.
func (d *DirectoryPasswordPolicy) Provision(account, tenant, initialPW string) error {
	return errNotImplemented
}

// Bind grades one bind attempt against the account state and the policy.
func (d *DirectoryPasswordPolicy) Bind(account, pw string, now time.Time) (BindResult, error) {
	return BindResult{}, errNotImplemented
}

// Change replaces the password of one account and revokes its sessions.
func (d *DirectoryPasswordPolicy) Change(account, oldPW, newPW string) (ChangeResult, error) {
	return ChangeResult{}, errNotImplemented
}

// Reset installs a temporary password and drops every live session.
func (d *DirectoryPasswordPolicy) Reset(account, tempPW string, mustChange bool) error {
	return errNotImplemented
}

// Status reads one account record.
func (d *DirectoryPasswordPolicy) Status(account string) (AccountStatus, error) {
	return AccountStatus{}, errNotImplemented
}

// Policy returns the policy in force for one tenant.
func (d *DirectoryPasswordPolicy) Policy(tenant string) Policy {
	return Policy{}
}

// SetPolicy installs a new policy version for one tenant.
func (d *DirectoryPasswordPolicy) SetPolicy(tenant string, p Policy) error {
	return errNotImplemented
}

// Sessions lists the live sessions of one account.
func (d *DirectoryPasswordPolicy) Sessions(account string) []string {
	return nil
}

// Authorize checks that a session may be used for something other than a
// password change.
func (d *DirectoryPasswordPolicy) Authorize(account, sessionID string) error {
	return errNotImplemented
}

// HistoryDepth reports how many historical digests are retained.
func (d *DirectoryPasswordPolicy) HistoryDepth(account string) int {
	return 0
}

// GraceUsed reports how many grace logins were already spent.
func (d *DirectoryPasswordPolicy) GraceUsed(account string) int {
	return 0
}

// Snapshot serialises accounts, history rings and sessions.
func (d *DirectoryPasswordPolicy) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (d *DirectoryPasswordPolicy) Restore(data []byte) error {
	return errNotImplemented
}
