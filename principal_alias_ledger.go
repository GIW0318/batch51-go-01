// Package task keeps the alias ledger of renamed principals so that tokens and
// audits issued under an old identifier still land on the right subject.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by the behaviours that are still missing.
var errNotImplemented = errors.New("not implemented")

// Errors surfaced by the ledger.
var (
	ErrBlankName        = errors.New("principal and name are required")
	ErrUnknownPrincipal = errors.New("principal is not registered")
	ErrPrincipalExists  = errors.New("principal is already registered")
	ErrNameTaken        = errors.New("name is live, aliased or reserved")
	ErrSameName         = errors.New("new name equals the current name")
	ErrAliasCycle       = errors.New("new name already points back into this alias chain")
	ErrChainDepth       = errors.New("alias chain depth limit reached")
	ErrHistoryLimit     = errors.New("alias history limit reached")
	ErrReserveLimit     = errors.New("reservation budget is exhausted")
	ErrNotReserved      = errors.New("name is not reserved")
	ErrNameNotFound     = errors.New("name is not live, aliased or reserved")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// AliasConfig holds the retention and ledger caps of one tenant.
type AliasConfig struct {
	Tenant        string
	Retention     time.Duration
	MaxChainDepth int
	MaxHistory    int
	MaxReserved   int
	Now           func() time.Time
}

// Alias is one retired name that still points at a live principal.
type Alias struct {
	Name      string
	Principal string
	From      time.Time
	Until     time.Time
	Seq       int64
}

// RenameResult reports one completed rename.
type RenameResult struct {
	Principal string
	OldName   string
	NewName   string
	Seq       int64
	Chain     []string
}

// Resolution is the answer to a name lookup.
type Resolution struct {
	Name      string
	Principal string
	Canonical string
	Found     bool
	Alias     bool
	Reserved  bool
	Until     time.Time
}

// Reservation is one held name that nobody may register yet.
type Reservation struct {
	Name  string
	Owner string
	Until time.Time
}

// PrincipalAliasLedger owns the live names, the alias chains and the
// reservation table.
type PrincipalAliasLedger struct {
	cfg AliasConfig
}

// NewPrincipalAliasLedger builds a ledger for one tenant.
func NewPrincipalAliasLedger(cfg AliasConfig) *PrincipalAliasLedger {
	return &PrincipalAliasLedger{cfg: cfg}
}

// Register claims the first name of a new principal.
func (l *PrincipalAliasLedger) Register(principal, name string, at time.Time) error {
	return errNotImplemented
}

// Rename retires the current name and makes the new one live.
func (l *PrincipalAliasLedger) Rename(principal, newName string, at time.Time) (RenameResult, error) {
	return RenameResult{}, errNotImplemented
}

// Resolve maps any live, aliased or reserved name to its owner.
func (l *PrincipalAliasLedger) Resolve(name string) (Resolution, error) {
	return Resolution{}, errNotImplemented
}

// Reserve holds a name for one owner until an instant.
func (l *PrincipalAliasLedger) Reserve(name string, until time.Time, owner string) error {
	return errNotImplemented
}

// Release drops a reservation before it expires.
func (l *PrincipalAliasLedger) Release(name string) error {
	return errNotImplemented
}

// History returns the retired names of one principal, oldest first.
func (l *PrincipalAliasLedger) History(principal string) ([]Alias, error) {
	return nil, errNotImplemented
}

// Chain returns the live name and every retired name of one principal.
func (l *PrincipalAliasLedger) Chain(principal string) ([]string, error) {
	return nil, errNotImplemented
}

// Reservations lists the reservations still in force at an instant.
func (l *PrincipalAliasLedger) Reservations(now time.Time) []Reservation {
	return nil
}

// Prune drops expired aliases and reservations, returning how many went away.
func (l *PrincipalAliasLedger) Prune(now time.Time) (int, error) {
	return 0, errNotImplemented
}

// Snapshot serialises the ledger and its sequence watermark.
func (l *PrincipalAliasLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole ledger with a snapshot, all or nothing.
func (l *PrincipalAliasLedger) Restore(data []byte) error {
	return errNotImplemented
}
