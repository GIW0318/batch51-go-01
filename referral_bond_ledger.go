// Package task tracks referral codes, invitee milestones and the reward
// ledger that pays out for them.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

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

var ledgerMilestoneOrder = [...]Milestone{
	MilestoneLevel10,
	MilestoneLevel30,
	MilestonePaid,
	MilestoneActive,
}

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

type milestoneLedger struct {
	Totals   map[Milestone]int64
	Credited map[Milestone]int64
	Unlocked map[Milestone]bool
	Claimed  map[Milestone]bool
	Revoked  map[Milestone]bool
}

type accountRecord struct {
	RegisteredAt time.Time
	Codes        []string
	Balance      int64
	Seq          int64
	Milestones   milestoneLedger
}

type codeRecord struct {
	OwnerID  string
	Bindings int
}

type bondRecord struct {
	Bond
	Fingerprint Fingerprint
}

// ReferralBondLedger owns the accounts, the codes, the bonds and the ledger.
type ReferralBondLedger struct {
	mu       sync.RWMutex
	policy   ReferralPolicy
	accounts map[string]*accountRecord
	codes    map[string]*codeRecord
	bonds    map[string]*bondRecord
	devices  map[string]string
	payments map[string]string
}

// NewReferralBondLedger builds a ledger with the given policy.
func NewReferralBondLedger(policy ReferralPolicy) *ReferralBondLedger {
	return &ReferralBondLedger{
		policy: ReferralPolicy{
			BindWindowHours:    policy.BindWindowHours,
			MaxBindingsPerCode: policy.MaxBindingsPerCode,
			MaxCodesPerAccount: policy.MaxCodesPerAccount,
			RetryAfterSeconds:  policy.RetryAfterSeconds,
			Thresholds:         cloneMilestoneInts(policy.Thresholds),
			Rewards:            cloneMilestoneInts(policy.Rewards),
		},
		accounts: make(map[string]*accountRecord),
		codes:    make(map[string]*codeRecord),
		bonds:    make(map[string]*bondRecord),
		devices:  make(map[string]string),
		payments: make(map[string]string),
	}
}

// Register records an account and the moment it signed up.
func (l *ReferralBondLedger) Register(accountID string, at time.Time) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ErrEmptyAccount
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.accounts[accountID]; exists {
		return ErrAccountExists
	}
	l.accounts[accountID] = &accountRecord{
		RegisteredAt: at,
		Codes:        make([]string, 0),
		Milestones:   newMilestoneLedger(),
	}
	return nil
}

// IssueCode mints one invite code for an account.
func (l *ReferralBondLedger) IssueCode(accountID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", ErrEmptyAccount
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	account, ok := l.accounts[accountID]
	if !ok {
		return "", ErrUnknownAccount
	}
	if len(account.Codes) >= l.policy.MaxCodesPerAccount {
		return "", ErrCodeLimit
	}

	code := fmt.Sprintf("%s#%04d", accountID, len(account.Codes)+1)
	account.Codes = append(account.Codes, code)
	l.codes[code] = &codeRecord{OwnerID: accountID}
	return code, nil
}

// Codes lists the invite codes of an account in issue order.
func (l *ReferralBondLedger) Codes(accountID string) []string {
	accountID = strings.TrimSpace(accountID)

	l.mu.RLock()
	defer l.mu.RUnlock()

	account, ok := l.accounts[accountID]
	if !ok {
		return []string{}
	}
	return append([]string{}, account.Codes...)
}

// Bind attaches an invitee to an invite code.
func (l *ReferralBondLedger) Bind(inviteeID, code string, at time.Time) (Bond, error) {
	return l.bind(inviteeID, code, Fingerprint{}, at)
}

// BindWithFingerprint binds an invitee together with its device and payment
// fingerprint.
func (l *ReferralBondLedger) BindWithFingerprint(inviteeID, code string, fp Fingerprint, at time.Time) (Bond, error) {
	return l.bind(inviteeID, code, fp, at)
}

func (l *ReferralBondLedger) bind(inviteeID, code string, fp Fingerprint, at time.Time) (Bond, error) {
	inviteeID = strings.TrimSpace(inviteeID)
	if inviteeID == "" {
		return Bond{}, ErrEmptyAccount
	}
	code = strings.TrimSpace(code)
	fp.Device = strings.TrimSpace(fp.Device)
	fp.Payment = strings.TrimSpace(fp.Payment)

	l.mu.Lock()
	defer l.mu.Unlock()

	codeInfo, ok := l.codes[code]
	if !ok {
		return Bond{}, ErrUnknownCode
	}
	if inviteeID == codeInfo.OwnerID {
		return Bond{}, ErrSelfReferral
	}
	if _, exists := l.bonds[inviteeID]; exists {
		return Bond{}, ErrAlreadyBound
	}

	account, ok := l.accounts[inviteeID]
	if !ok {
		return Bond{}, ErrUnknownAccount
	}
	deadline := account.RegisteredAt.Add(time.Duration(l.policy.BindWindowHours) * time.Hour)
	if at.After(deadline) {
		return Bond{}, ErrBindWindowClosed
	}
	if codeInfo.Bindings >= l.policy.MaxBindingsPerCode {
		return Bond{}, &CodeFullError{
			RetryAfter: at.Add(time.Duration(l.policy.RetryAfterSeconds) * time.Second),
		}
	}

	quarantined := false
	if fp.Device != "" {
		if owner, used := l.devices[fp.Device]; used && owner != inviteeID {
			quarantined = true
		}
	}
	if fp.Payment != "" {
		if owner, used := l.payments[fp.Payment]; used && owner != inviteeID {
			quarantined = true
		}
	}

	bond := Bond{
		InviteeID:   inviteeID,
		InviterID:   codeInfo.OwnerID,
		Code:        code,
		BoundAt:     at,
		Quarantined: quarantined,
	}
	l.bonds[inviteeID] = &bondRecord{Bond: bond, Fingerprint: fp}
	codeInfo.Bindings++
	if fp.Device != "" {
		if _, used := l.devices[fp.Device]; !used {
			l.devices[fp.Device] = inviteeID
		}
	}
	if fp.Payment != "" {
		if _, used := l.payments[fp.Payment]; !used {
			l.payments[fp.Payment] = inviteeID
		}
	}
	return bond, nil
}

// Accrue books milestone progress for an invitee.
func (l *ReferralBondLedger) Accrue(inviteeID string, milestone Milestone, delta int64) error {
	inviteeID = strings.TrimSpace(inviteeID)

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, bound := l.bonds[inviteeID]; !bound {
		return ErrNotBound
	}
	account, ok := l.accounts[inviteeID]
	if !ok {
		return ErrNotBound
	}
	if !isKnownMilestone(milestone) {
		return ErrUnknownMilestone
	}
	if delta <= 0 {
		return ErrBadDelta
	}

	account.Milestones.Totals[milestone] += delta
	account.Milestones.Credited[milestone] += delta
	if !account.Milestones.Revoked[milestone] &&
		account.Milestones.Credited[milestone] >= l.threshold(milestone) {
		account.Milestones.Unlocked[milestone] = true
	}
	return nil
}

// Progress reads the milestone ledger of one invitee.
func (l *ReferralBondLedger) Progress(inviteeID string) (Progress, error) {
	inviteeID = strings.TrimSpace(inviteeID)

	l.mu.RLock()
	defer l.mu.RUnlock()

	account, ok := l.accounts[inviteeID]
	if !ok {
		return Progress{}, ErrUnknownAccount
	}
	if _, bound := l.bonds[inviteeID]; !bound {
		return Progress{}, ErrNotBound
	}

	progress := Progress{
		InviteeID: inviteeID,
		Totals:    make(map[Milestone]int64, len(ledgerMilestoneOrder)),
		Credited:  make(map[Milestone]int64, len(ledgerMilestoneOrder)),
		Unlocked:  make([]Milestone, 0),
		Claimed:   make([]Milestone, 0),
	}
	for _, milestone := range ledgerMilestoneOrder {
		progress.Totals[milestone] = account.Milestones.Totals[milestone]
		progress.Credited[milestone] = account.Milestones.Credited[milestone]
		if account.Milestones.Unlocked[milestone] {
			progress.Unlocked = append(progress.Unlocked, milestone)
		}
		if account.Milestones.Claimed[milestone] {
			progress.Claimed = append(progress.Claimed, milestone)
		}
	}
	return progress, nil
}

// Claim pays out every unlocked milestone that has not been paid yet.
func (l *ReferralBondLedger) Claim(accountID string) (Grant, error) {
	accountID = strings.TrimSpace(accountID)

	l.mu.Lock()
	defer l.mu.Unlock()

	account, ok := l.accounts[accountID]
	if !ok {
		return Grant{}, ErrUnknownAccount
	}
	if _, bound := l.bonds[accountID]; !bound {
		return Grant{}, ErrNotBound
	}

	grant := Grant{
		AccountID:  accountID,
		Milestones: make([]Milestone, 0),
		Balance:    account.Balance,
		Seq:        account.Seq,
	}
	for _, milestone := range ledgerMilestoneOrder {
		if !account.Milestones.Unlocked[milestone] ||
			account.Milestones.Claimed[milestone] ||
			account.Milestones.Revoked[milestone] {
			continue
		}
		grant.Amount += l.reward(milestone)
		grant.Milestones = append(grant.Milestones, milestone)
		account.Milestones.Claimed[milestone] = true
	}
	if len(grant.Milestones) == 0 {
		return grant, nil
	}

	account.Balance += grant.Amount
	account.Seq++
	grant.Balance = account.Balance
	grant.Seq = account.Seq
	return grant, nil
}

// Reverse reclaims unlocked rewards and records a clawback.
func (l *ReferralBondLedger) Reverse(accountID, reason string) (Clawback, error) {
	accountID = strings.TrimSpace(accountID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Clawback{}, ErrBadReason
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	account, ok := l.accounts[accountID]
	if !ok {
		return Clawback{}, ErrUnknownAccount
	}
	if _, bound := l.bonds[accountID]; !bound {
		return Clawback{}, ErrNotBound
	}

	clawback := Clawback{
		AccountID: accountID,
		Reason:    reason,
		Balance:   account.Balance,
		Seq:       account.Seq,
	}
	for _, milestone := range ledgerMilestoneOrder {
		if account.Milestones.Revoked[milestone] {
			continue
		}
		if !account.Milestones.Unlocked[milestone] && !account.Milestones.Claimed[milestone] {
			continue
		}

		reward := l.reward(milestone)
		clawback.Reversed += reward
		if account.Milestones.Claimed[milestone] {
			clawback.Recovered += reward
		}
		account.Milestones.Revoked[milestone] = true
		delete(account.Milestones.Unlocked, milestone)
	}

	// Paid rewards are taken back even when that takes the spendable balance
	// negative. The debt is then naturally offset by the next claim.
	account.Balance -= clawback.Recovered
	account.Seq++
	clawback.Balance = account.Balance
	clawback.Seq = account.Seq
	return clawback, nil
}

// Balance reads the reward balance of one account.
func (l *ReferralBondLedger) Balance(accountID string) (int64, error) {
	accountID = strings.TrimSpace(accountID)

	l.mu.RLock()
	defer l.mu.RUnlock()

	account, ok := l.accounts[accountID]
	if !ok {
		return 0, ErrUnknownAccount
	}
	return account.Balance, nil
}

// BondOf reads the bond of one invitee.
func (l *ReferralBondLedger) BondOf(inviteeID string) (Bond, bool) {
	inviteeID = strings.TrimSpace(inviteeID)

	l.mu.RLock()
	defer l.mu.RUnlock()

	record, ok := l.bonds[inviteeID]
	if !ok {
		return Bond{}, false
	}
	return record.Bond, true
}

// Quarantined lists the quarantined invitees in lexical order.
func (l *ReferralBondLedger) Quarantined() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make([]string, 0)
	for inviteeID, record := range l.bonds {
		if record.Quarantined {
			result = append(result, inviteeID)
		}
	}
	sort.Strings(result)
	return result
}

type snapshotFile struct {
	Version  int               `json:"version"`
	Accounts []snapshotAccount `json:"accounts"`
	Bonds    []snapshotBond    `json:"bonds"`
}

type snapshotAccount struct {
	ID           string           `json:"id"`
	RegisteredAt time.Time        `json:"registered_at"`
	Codes        []string         `json:"codes"`
	Balance      int64            `json:"balance"`
	Seq          int64            `json:"seq"`
	Totals       map[string]int64 `json:"totals"`
	Credited     map[string]int64 `json:"credited"`
	Unlocked     []string         `json:"unlocked"`
	Claimed      []string         `json:"claimed"`
	Revoked      []string         `json:"revoked"`
}

type snapshotBond struct {
	InviteeID   string    `json:"invitee_id"`
	InviterID   string    `json:"inviter_id"`
	Code        string    `json:"code"`
	BoundAt     time.Time `json:"bound_at"`
	Quarantined bool      `json:"quarantined"`
	Device      string    `json:"device"`
	Payment     string    `json:"payment"`
}

// Snapshot serialises the accounts, the codes, the bonds and the ledger.
func (l *ReferralBondLedger) Snapshot() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	accountIDs := make([]string, 0, len(l.accounts))
	for accountID := range l.accounts {
		accountIDs = append(accountIDs, accountID)
	}
	sort.Strings(accountIDs)

	file := snapshotFile{
		Version:  1,
		Accounts: make([]snapshotAccount, 0, len(accountIDs)),
		Bonds:    make([]snapshotBond, 0, len(l.bonds)),
	}
	for _, accountID := range accountIDs {
		account := l.accounts[accountID]
		entry := snapshotAccount{
			ID:           accountID,
			RegisteredAt: account.RegisteredAt,
			Codes:        append([]string{}, account.Codes...),
			Balance:      account.Balance,
			Seq:          account.Seq,
			Totals:       make(map[string]int64, len(ledgerMilestoneOrder)),
			Credited:     make(map[string]int64, len(ledgerMilestoneOrder)),
			Unlocked:     make([]string, 0),
			Claimed:      make([]string, 0),
			Revoked:      make([]string, 0),
		}
		for _, milestone := range ledgerMilestoneOrder {
			entry.Totals[string(milestone)] = account.Milestones.Totals[milestone]
			entry.Credited[string(milestone)] = account.Milestones.Credited[milestone]
			if account.Milestones.Unlocked[milestone] {
				entry.Unlocked = append(entry.Unlocked, string(milestone))
			}
			if account.Milestones.Claimed[milestone] {
				entry.Claimed = append(entry.Claimed, string(milestone))
			}
			if account.Milestones.Revoked[milestone] {
				entry.Revoked = append(entry.Revoked, string(milestone))
			}
		}
		file.Accounts = append(file.Accounts, entry)
	}

	bondIDs := make([]string, 0, len(l.bonds))
	for inviteeID := range l.bonds {
		bondIDs = append(bondIDs, inviteeID)
	}
	sort.Strings(bondIDs)
	for _, inviteeID := range bondIDs {
		record := l.bonds[inviteeID]
		file.Bonds = append(file.Bonds, snapshotBond{
			InviteeID:   record.InviteeID,
			InviterID:   record.InviterID,
			Code:        record.Code,
			BoundAt:     record.BoundAt,
			Quarantined: record.Quarantined,
			Device:      record.Fingerprint.Device,
			Payment:     record.Fingerprint.Payment,
		})
	}

	return json.Marshal(file)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *ReferralBondLedger) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file *snapshotFile
	if err := decoder.Decode(&file); err != nil || file == nil {
		return ErrBadSnapshot
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrBadSnapshot
	}
	if file.Version != 1 || file.Accounts == nil || file.Bonds == nil {
		return ErrBadSnapshot
	}

	accounts, codes, bonds, devices, payments, err := l.buildRestoredState(file)
	if err != nil {
		return ErrBadSnapshot
	}

	l.mu.Lock()
	l.accounts = accounts
	l.codes = codes
	l.bonds = bonds
	l.devices = devices
	l.payments = payments
	l.mu.Unlock()
	return nil
}

func (l *ReferralBondLedger) buildRestoredState(file *snapshotFile) (
	map[string]*accountRecord,
	map[string]*codeRecord,
	map[string]*bondRecord,
	map[string]string,
	map[string]string,
	error,
) {
	accounts := make(map[string]*accountRecord, len(file.Accounts))
	codes := make(map[string]*codeRecord)
	for _, entry := range file.Accounts {
		if entry.ID == "" || entry.ID != strings.TrimSpace(entry.ID) || entry.Codes == nil {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := accounts[entry.ID]; exists {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}

		milestones, err := restoreMilestones(entry)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		account := &accountRecord{
			RegisteredAt: entry.RegisteredAt,
			Codes:        make([]string, len(entry.Codes)),
			Balance:      entry.Balance,
			Seq:          entry.Seq,
			Milestones:   milestones,
		}
		if account.Seq < 0 {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		for index, code := range entry.Codes {
			if code != fmt.Sprintf("%s#%04d", entry.ID, index+1) {
				return nil, nil, nil, nil, nil, ErrBadSnapshot
			}
			if _, exists := codes[code]; exists {
				return nil, nil, nil, nil, nil, ErrBadSnapshot
			}
			account.Codes[index] = code
			codes[code] = &codeRecord{OwnerID: entry.ID}
		}
		accounts[entry.ID] = account
	}

	bonds := make(map[string]*bondRecord, len(file.Bonds))
	devices := make(map[string]string)
	payments := make(map[string]string)
	for _, entry := range file.Bonds {
		if entry.InviteeID == "" || entry.InviteeID != strings.TrimSpace(entry.InviteeID) ||
			entry.Device != strings.TrimSpace(entry.Device) ||
			entry.Payment != strings.TrimSpace(entry.Payment) {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := bonds[entry.InviteeID]; exists {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := accounts[entry.InviteeID]; !exists {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		code, exists := codes[entry.Code]
		if !exists || code.OwnerID != entry.InviterID || entry.InviteeID == entry.InviterID {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		code.Bindings++
		record := &bondRecord{
			Bond: Bond{
				InviteeID:   entry.InviteeID,
				InviterID:   entry.InviterID,
				Code:        entry.Code,
				BoundAt:     entry.BoundAt,
				Quarantined: entry.Quarantined,
			},
			Fingerprint: Fingerprint{Device: entry.Device, Payment: entry.Payment},
		}
		bonds[entry.InviteeID] = record
		if entry.Device != "" {
			if _, used := devices[entry.Device]; !used {
				devices[entry.Device] = entry.InviteeID
			}
		}
		if entry.Payment != "" {
			if _, used := payments[entry.Payment]; !used {
				payments[entry.Payment] = entry.InviteeID
			}
		}
	}

	return accounts, codes, bonds, devices, payments, nil
}

func restoreMilestones(entry snapshotAccount) (milestoneLedger, error) {
	if entry.Totals == nil || entry.Credited == nil || entry.Unlocked == nil ||
		entry.Claimed == nil || entry.Revoked == nil ||
		len(entry.Totals) != len(ledgerMilestoneOrder) ||
		len(entry.Credited) != len(ledgerMilestoneOrder) {
		return milestoneLedger{}, ErrBadSnapshot
	}

	milestones := newMilestoneLedger()
	for _, milestone := range ledgerMilestoneOrder {
		key := string(milestone)
		total, totalOK := entry.Totals[key]
		credited, creditedOK := entry.Credited[key]
		if !totalOK || !creditedOK || total < 0 || credited < 0 || credited > total {
			return milestoneLedger{}, ErrBadSnapshot
		}
		milestones.Totals[milestone] = total
		milestones.Credited[milestone] = credited
	}

	unlocked, err := milestoneSet(entry.Unlocked)
	if err != nil {
		return milestoneLedger{}, err
	}
	claimed, err := milestoneSet(entry.Claimed)
	if err != nil {
		return milestoneLedger{}, err
	}
	revoked, err := milestoneSet(entry.Revoked)
	if err != nil {
		return milestoneLedger{}, err
	}
	for _, milestone := range ledgerMilestoneOrder {
		if unlocked[milestone] && revoked[milestone] {
			return milestoneLedger{}, ErrBadSnapshot
		}
	}
	milestones.Unlocked = unlocked
	milestones.Claimed = claimed
	milestones.Revoked = revoked
	return milestones, nil
}

func milestoneSet(values []string) (map[Milestone]bool, error) {
	set := make(map[Milestone]bool, len(values))
	for _, value := range values {
		milestone := Milestone(value)
		if !isKnownMilestone(milestone) || set[milestone] {
			return nil, ErrBadSnapshot
		}
		set[milestone] = true
	}
	return set, nil
}

func newMilestoneLedger() milestoneLedger {
	return milestoneLedger{
		Totals:   make(map[Milestone]int64, len(ledgerMilestoneOrder)),
		Credited: make(map[Milestone]int64, len(ledgerMilestoneOrder)),
		Unlocked: make(map[Milestone]bool, len(ledgerMilestoneOrder)),
		Claimed:  make(map[Milestone]bool, len(ledgerMilestoneOrder)),
		Revoked:  make(map[Milestone]bool, len(ledgerMilestoneOrder)),
	}
}

func cloneMilestoneInts(source map[Milestone]int64) map[Milestone]int64 {
	result := make(map[Milestone]int64, len(source))
	for milestone, value := range source {
		result[milestone] = value
	}
	return result
}

func isKnownMilestone(milestone Milestone) bool {
	switch milestone {
	case MilestoneLevel10, MilestoneLevel30, MilestonePaid, MilestoneActive:
		return true
	default:
		return false
	}
}

func (l *ReferralBondLedger) threshold(milestone Milestone) int64 {
	return l.policy.Thresholds[milestone]
}

func (l *ReferralBondLedger) reward(milestone Milestone) int64 {
	return l.policy.Rewards[milestone]
}
