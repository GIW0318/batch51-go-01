// Package task audits the supply of game currencies: it keeps one balance per
// account and currency, books the ledger entries by business time, replays
// duplicate entries, and reports the accounts that overdrew the window.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Errors reported by the auditor.
var (
	ErrBadAccount        = errors.New("account id and currency are required")
	ErrDuplicateAccount  = errors.New("account is already registered")
	ErrAccountLimit      = errors.New("account budget reached")
	ErrUnknownAccount    = errors.New("unknown account")
	ErrAccountClosed     = errors.New("account is closed")
	ErrBadEntry          = errors.New("ledger entry is invalid")
	ErrCurrencyMismatch  = errors.New("entry currency does not match the account")
	ErrUnknownCurrency   = errors.New("unknown currency")
	ErrDuplicateEntry    = errors.New("entry id was already booked with another amount")
	ErrBadTolerance      = errors.New("tolerance must not be negative")
	ErrBadWindow         = errors.New("audit window is inverted")
	ErrWindowLimit       = errors.New("audit window holds too many entries")
	ErrBadReason         = errors.New("flag reason is required")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// LedgerEntry is one movement of one currency.
type LedgerEntry struct {
	EntryID  string
	Account  string
	Currency string
	Amount   int64
	BizTime  int64
	Kind     string
}

// Totals is the aggregate view of one currency.
type Totals struct {
	Currency string
	Issued   int64
	Recycled int64
	Net      int64
	Entries  int
	Accounts int
}

// Tolerances bounds the overdraft an audit may ignore.
type Tolerances struct {
	Absolute int64
	Relative float64
}

// Anomaly is one account that went below zero inside the window.
type Anomaly struct {
	Account string
	EntryID string
	Amount  int64
	Reason  string
}

// AuditReport is the answer of one window audit.
type AuditReport struct {
	Currency        string
	From            int64
	To              int64
	Opening         int64
	Issued          int64
	Recycled        int64
	Closing         int64
	Delta           int64
	WithinTolerance bool
	Anomalies       []Anomaly
}

// ReconcileResult compares the account balances with the booked entries.
type ReconcileResult struct {
	Currency   string
	Expected   int64
	Actual     int64
	Difference int64
	Flagged    []string
	Entries    int
}

// AuditStats summarises one auditor.
type AuditStats struct {
	Currencies int
	Accounts   int
	Closed     int
	Entries    int
	Duplicates int
	Conflicts  int
	Flagged    int
}

// AuditLimits caps one auditor.
type AuditLimits struct {
	MaxAccounts      int
	MaxCurrencies    int
	MaxWindowEntries int
}

// CurrencySupplyAuditor owns the accounts, the ledger and the audit state.
type CurrencySupplyAuditor struct {
	limits AuditLimits
}

// NewCurrencySupplyAuditor builds an auditor with the given caps.
func NewCurrencySupplyAuditor(limits AuditLimits) *CurrencySupplyAuditor {
	return &CurrencySupplyAuditor{limits: limits}
}

// RegisterAccount binds one account to one currency.
func (a *CurrencySupplyAuditor) RegisterAccount(acct string, currency string) error {
	return errNotImplemented
}

// CloseAccount stops new entries without dropping the history.
func (a *CurrencySupplyAuditor) CloseAccount(acct string) error {
	return errNotImplemented
}

// Record books one ledger entry.
func (a *CurrencySupplyAuditor) Record(entry LedgerEntry) error {
	return errNotImplemented
}

// SnapshotTotals reads the aggregate of one currency.
func (a *CurrencySupplyAuditor) SnapshotTotals(currency string) (Totals, error) {
	return Totals{}, errNotImplemented
}

// Audit walks one window of one currency.
func (a *CurrencySupplyAuditor) Audit(currency string, from, to int64) (AuditReport, error) {
	return AuditReport{}, errNotImplemented
}

// SetTolerance installs the overdraft tolerance.
func (a *CurrencySupplyAuditor) SetTolerance(t Tolerances) error {
	return errNotImplemented
}

// FlagAccount marks one account for the next reconciliation.
func (a *CurrencySupplyAuditor) FlagAccount(acct string, reason string) error {
	return errNotImplemented
}

// Reconcile compares the balances with the booked entries.
func (a *CurrencySupplyAuditor) Reconcile(currency string) (ReconcileResult, error) {
	return ReconcileResult{}, errNotImplemented
}

// Stats reads the counters of the auditor.
func (a *CurrencySupplyAuditor) Stats() AuditStats {
	return AuditStats{}
}

// Snapshot serialises accounts, entries and the audit watermarks.
func (a *CurrencySupplyAuditor) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *CurrencySupplyAuditor) Restore(data []byte) error {
	return errNotImplemented
}
