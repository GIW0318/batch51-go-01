// Package task grants, inherits and evaluates WebDAV resource locks: depth
// handling, shared versus exclusive conflicts, If header evaluation and the
// atomicity of lock table changes around resource moves.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Lock depths accepted by Lock.
const (
	DepthZero     = "0"
	DepthInfinity = "infinity"
)

// Lock scopes accepted by Lock.
const (
	ScopeExclusive = "exclusive"
	ScopeShared    = "shared"
)

// HTTP status codes reported by CheckIf.
const (
	StatusPreconditionFailed = 412
	StatusLocked             = 423
)

// Reasons attached to a failed If evaluation.
const (
	ReasonTokenMismatch = "token_mismatch"
	ReasonETagMismatch  = "etag_mismatch"
	ReasonLocked        = "locked"
)

// Errors returned by the guard.
var (
	ErrBadPath           = errors.New("path must be an absolute clean dav path")
	ErrBadDepth          = errors.New("lock depth must be 0 or infinity")
	ErrBadScope          = errors.New("lock scope must be exclusive or shared")
	ErrBadTimeout        = errors.New("lock timeout must be positive")
	ErrLockConflict      = errors.New("lock conflicts with an existing lock")
	ErrLockNotFound      = errors.New("lock token not found")
	ErrLockExpired       = errors.New("lock token already expired")
	ErrTokenPathMismatch = errors.New("lock token does not belong to this path")
	ErrLimitReached      = errors.New("lock table is full")
	ErrLockRequired      = errors.New("resource is locked and the request carries no matching token")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// LockRequest is the body of a LOCK request.
type LockRequest struct {
	Owner   string        `json:"owner"`
	Depth   string        `json:"depth"`
	Scope   string        `json:"scope"`
	Timeout time.Duration `json:"timeout"`
}

// LockToken is one granted lock.
type LockToken struct {
	Token   string    `json:"token"`
	Path    string    `json:"path"`
	Owner   string    `json:"owner"`
	Depth   string    `json:"depth"`
	Scope   string    `json:"scope"`
	Expires time.Time `json:"expires"`
	Rev     uint64    `json:"rev"`
}

// IfCondition is one condition of an If header list.
type IfCondition struct {
	Token string `json:"token"`
	ETag  string `json:"etag"`
	Not   bool   `json:"not"`
}

// IfResult is the outcome of one conditional request evaluation.
type IfResult struct {
	OK      bool   `json:"ok"`
	Status  int    `json:"status"`
	Reason  string `json:"reason"`
	Matched string `json:"matched"`
}

// LockPolicy holds the server side limits.
type LockPolicy struct {
	MaxTimeout time.Duration `json:"max_timeout"`
	MaxLocks   int           `json:"max_locks"`
}

// WebdavLockGuard owns the lock table, the entity tags and the clock.
type WebdavLockGuard struct {
	cfg LockPolicy
}

// NewWebdavLockGuard builds a guard with the given limits.
func NewWebdavLockGuard(cfg LockPolicy) *WebdavLockGuard {
	return &WebdavLockGuard{cfg: cfg}
}

// SetClock sets the instant used to stamp and evaluate lock expiries.
func (g *WebdavLockGuard) SetClock(now time.Time) {
}

// Lock grants one lock unless an existing lock covers the same resource.
func (g *WebdavLockGuard) Lock(path string, req LockRequest) (LockToken, error) {
	return LockToken{}, errNotImplemented
}

// Refresh extends one live lock.
func (g *WebdavLockGuard) Refresh(token string, timeout time.Duration) (LockToken, error) {
	return LockToken{}, errNotImplemented
}

// Unlock drops one lock; the path must match the path the token was granted on.
func (g *WebdavLockGuard) Unlock(path, token string) error {
	return errNotImplemented
}

// CheckIf evaluates one If header list against the lock table.
func (g *WebdavLockGuard) CheckIf(path string, conditions []IfCondition) (IfResult, error) {
	return IfResult{}, errNotImplemented
}

// Move moves the resource together with the locks granted on it.
func (g *WebdavLockGuard) Move(src, dst, token string) error {
	return errNotImplemented
}

// LocksOn lists the live locks that protect the path, oldest first.
func (g *WebdavLockGuard) LocksOn(path string) []LockToken {
	return nil
}

// LockOf reads one lock token.
func (g *WebdavLockGuard) LockOf(token string) (LockToken, bool) {
	return LockToken{}, false
}

// Tag stores the entity tag currently attached to the path.
func (g *WebdavLockGuard) Tag(path, etag string) error {
	return errNotImplemented
}

// Expire advances the clock and reclaims every lock that already timed out.
func (g *WebdavLockGuard) Expire(now time.Time) int {
	return 0
}

// Snapshot serialises the lock table, the tags and the clock.
func (g *WebdavLockGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *WebdavLockGuard) Restore(data []byte) error {
	return errNotImplemented
}
