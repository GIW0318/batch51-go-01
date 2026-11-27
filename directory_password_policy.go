// Package task decides directory password policy at bind time and keeps the
// password change ledger: expiry grace, forced change after a temporary
// password and reuse of historical passwords.
package task

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

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
	ErrMissingField    = errors.New("account, tenant and password are required")
	ErrUnknownTenant   = errors.New("tenant has no directory policy")
	ErrAccountExists   = errors.New("account already provisioned")
	ErrAccountNotFound = errors.New("account not found")
	ErrBadPassword     = errors.New("password does not match")
	ErrAccountLocked   = errors.New("account is locked")
	ErrPasswordExpired = errors.New("password expired and the grace budget is spent")
	ErrPasswordPolicy  = errors.New("password violates the tenant policy")
	ErrPasswordReused  = errors.New("password was used recently")
	ErrSessionInvalid  = errors.New("session is not valid for this account")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

const (
	maxAccountsPerDirectory = 4096
	maxSessionsPerAccount   = 64
	snapshotMagic           = "gsb/directory-password-policy"
	snapshotVersion         = 1
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

type sessionRecord struct {
	ID         string    `json:"id"`
	ChangeOnly bool      `json:"change_only"`
	IssuedAt   time.Time `json:"issued_at"`
}

type accountRecord struct {
	Account       string          `json:"account"`
	Tenant        string          `json:"tenant"`
	PasswordHash  string          `json:"password_hash"`
	History       []string        `json:"history"`
	Failures      int             `json:"failures"`
	LockedAt      time.Time       `json:"locked_at"`
	GraceUsed     int             `json:"grace_used"`
	MustChange    bool            `json:"must_change"`
	Temporary     bool            `json:"temporary"`
	LastChangedAt time.Time       `json:"last_changed_at"`
	Sessions      []sessionRecord `json:"sessions"`
}

// DirectoryPasswordPolicy owns the account table, the digest history rings and
// the session index.
type DirectoryPasswordPolicy struct {
	mu          sync.Mutex
	cfg         PolicyConfig
	accounts    map[string]*accountRecord
	order       []string
	nextSession uint64
}

type snapshotDocument struct {
	Magic         string            `json:"magic"`
	Version       int               `json:"version"`
	Checksum      string            `json:"checksum"`
	DefaultTenant string            `json:"default_tenant"`
	Policies      map[string]Policy `json:"policies"`
	Accounts      []accountRecord   `json:"accounts"`
	NextSession   uint64            `json:"next_session"`
}

// NewDirectoryPasswordPolicy builds an engine with the given policies.
func NewDirectoryPasswordPolicy(cfg PolicyConfig) *DirectoryPasswordPolicy {
	cfg.Policies = clonePolicies(cfg.Policies)
	return &DirectoryPasswordPolicy{
		cfg:         cfg,
		accounts:    make(map[string]*accountRecord),
		order:       make([]string, 0),
		nextSession: 1,
	}
}

// Provision creates one account with an initial password.
func (d *DirectoryPasswordPolicy) Provision(account, tenant, initialPW string) error {
	if account == "" || tenant == "" || initialPW == "" {
		return ErrMissingField
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureStateLocked()

	if _, ok := d.accounts[account]; ok {
		return ErrAccountExists
	}
	p, err := d.policyLocked(tenant)
	if err != nil {
		return err
	}
	if err := validatePassword(initialPW, p); err != nil {
		return err
	}

	now := d.clockLocked()
	d.accounts[account] = &accountRecord{
		Account:       account,
		Tenant:        tenant,
		PasswordHash:  digestPassword(initialPW),
		History:       make([]string, 0),
		LastChangedAt: now,
		Sessions:      make([]sessionRecord, 0),
	}
	d.order = append(d.order, account)
	d.evictOldestAccountsLocked()
	return nil
}

// Bind grades one bind attempt against the account state and the policy.
func (d *DirectoryPasswordPolicy) Bind(account, pw string, now time.Time) (BindResult, error) {
	if account == "" || pw == "" {
		return BindResult{}, ErrMissingField
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureStateLocked()

	a, ok := d.accounts[account]
	if !ok {
		return BindResult{}, ErrAccountNotFound
	}
	p, err := d.policyLocked(a.Tenant)
	if err != nil {
		return BindResult{Tenant: a.Tenant}, err
	}

	now = normalizeTime(now)
	if now.IsZero() {
		now = d.clockLocked()
	}
	result := BindResult{
		Tenant:          a.Tenant,
		MustChange:      a.MustChange,
		GraceRemaining:  graceRemaining(a, p),
		AttemptsLeft:    attemptsRemaining(a, p),
		PasswordAgeDays: passwordAgeDays(a, now),
	}

	d.clearExpiredLockLocked(a, p, now)
	d.applyFailureThresholdLocked(a, p, now)
	if accountLocked(a, p, now) {
		result.State = StateLocked
		result.AttemptsLeft = 0
		return result, ErrAccountLocked
	}

	if !passwordHashMatches(a.PasswordHash, pw) {
		a.Failures++
		if a.Failures >= p.MaxFailures {
			a.LockedAt = now
			result.State = StateLocked
			result.AttemptsLeft = 0
			result.PasswordAgeDays = passwordAgeDays(a, now)
			return result, ErrAccountLocked
		}
		result.State = accountState(a, p, now)
		result.AttemptsLeft = attemptsRemaining(a, p)
		result.PasswordAgeDays = passwordAgeDays(a, now)
		return result, ErrBadPassword
	}

	a.Failures = 0
	a.LockedAt = time.Time{}
	result.AttemptsLeft = p.MaxFailures
	result.PasswordAgeDays = passwordAgeDays(a, now)

	if !passwordExpired(a, p, now) {
		result.Allowed = true
		result.State = accountState(a, p, now)
		result.MustChange = a.MustChange
		result.GraceRemaining = graceRemaining(a, p)
		result.SessionID = d.addSessionLocked(a, a.MustChange || a.Temporary, now)
		return result, nil
	}

	if a.GraceUsed < p.GraceLogins {
		a.GraceUsed++
		a.MustChange = true
		result.Allowed = true
		result.MustChange = true
		result.State = StateExpiredGrace
		result.GraceRemaining = graceRemaining(a, p)
		result.SessionID = d.addSessionLocked(a, true, now)
		return result, nil
	}

	result.State = StateExpired
	result.GraceRemaining = 0
	result.MustChange = a.MustChange
	return result, ErrPasswordExpired
}

// Change replaces the password of one account and revokes its sessions.
func (d *DirectoryPasswordPolicy) Change(account, oldPW, newPW string) (ChangeResult, error) {
	if account == "" || oldPW == "" || newPW == "" {
		return ChangeResult{}, ErrMissingField
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureStateLocked()

	a, ok := d.accounts[account]
	if !ok {
		return ChangeResult{}, ErrAccountNotFound
	}
	p, err := d.policyLocked(a.Tenant)
	if err != nil {
		return ChangeResult{}, err
	}
	if !passwordHashMatches(a.PasswordHash, oldPW) {
		return ChangeResult{}, ErrBadPassword
	}
	if err := validatePassword(newPW, p); err != nil {
		return ChangeResult{}, err
	}
	newHash := digestPassword(newPW)
	if hashUsed(a, p, newHash) {
		return ChangeResult{}, ErrPasswordReused
	}

	a.History = appendHistory(a.History, a.PasswordHash, p.HistoryDepth)
	a.PasswordHash = newHash
	a.LastChangedAt = d.clockLocked()
	a.Failures = 0
	a.LockedAt = time.Time{}
	a.GraceUsed = 0
	a.MustChange = false
	a.Temporary = false

	revoked := len(a.Sessions)
	a.Sessions = make([]sessionRecord, 0)
	return ChangeResult{
		Changed:         true,
		Tenant:          a.Tenant,
		SessionsRevoked: revoked,
		HistoryRetained: len(a.History),
	}, nil
}

// Reset installs a temporary password and drops every live session.
func (d *DirectoryPasswordPolicy) Reset(account, tempPW string, mustChange bool) error {
	if account == "" || tempPW == "" {
		return ErrMissingField
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureStateLocked()

	a, ok := d.accounts[account]
	if !ok {
		return ErrAccountNotFound
	}
	p, err := d.policyLocked(a.Tenant)
	if err != nil {
		return err
	}
	if err := validatePassword(tempPW, p); err != nil {
		return err
	}

	a.History = appendHistory(a.History, a.PasswordHash, p.HistoryDepth)
	a.PasswordHash = digestPassword(tempPW)
	a.LastChangedAt = d.clockLocked()
	a.Failures = 0
	a.LockedAt = time.Time{}
	a.GraceUsed = 0
	a.MustChange = mustChange
	a.Temporary = true
	a.Sessions = make([]sessionRecord, 0)
	return nil
}

// Status reads one account record.
func (d *DirectoryPasswordPolicy) Status(account string) (AccountStatus, error) {
	if account == "" {
		return AccountStatus{}, ErrMissingField
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureStateLocked()

	a, ok := d.accounts[account]
	if !ok {
		return AccountStatus{}, ErrAccountNotFound
	}
	p, err := d.policyLocked(a.Tenant)
	if err != nil {
		return AccountStatus{}, err
	}
	now := d.clockLocked()
	d.clearExpiredLockLocked(a, p, now)
	d.applyFailureThresholdLocked(a, p, now)
	return AccountStatus{
		Account:         a.Account,
		Tenant:          a.Tenant,
		State:           accountState(a, p, now),
		Failures:        a.Failures,
		GraceUsed:       a.GraceUsed,
		MustChange:      a.MustChange,
		PasswordAgeDays: passwordAgeDays(a, now),
		Sessions:        len(a.Sessions),
		HistoryDepth:    len(a.History),
		PolicyVersion:   p.Version,
		LastChangedAt:   a.LastChangedAt,
	}, nil
}

// Policy returns the policy in force for one tenant.
func (d *DirectoryPasswordPolicy) Policy(tenant string) Policy {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.cfg.Policies[tenant]
	if !ok {
		return Policy{Tenant: tenant}
	}
	return p
}

// SetPolicy installs a new policy version for one tenant.
func (d *DirectoryPasswordPolicy) SetPolicy(tenant string, p Policy) error {
	if tenant == "" {
		return ErrMissingField
	}
	if err := validatePolicy(tenant, p); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureStateLocked()

	if old, ok := d.cfg.Policies[tenant]; ok {
		p.Version = old.Version + 1
		if p.Version <= 0 {
			p.Version = 1
		}
	} else if p.Version <= 0 {
		p.Version = 1
	}
	d.cfg.Policies[tenant] = p

	for _, a := range d.accounts {
		if a.Tenant != tenant {
			continue
		}
		if len(a.History) > p.HistoryDepth {
			a.History = append([]string(nil), a.History[len(a.History)-p.HistoryDepth:]...)
		}
		if a.GraceUsed > p.GraceLogins {
			a.GraceUsed = p.GraceLogins
		}
	}
	return nil
}

// Sessions lists the live sessions of one account.
func (d *DirectoryPasswordPolicy) Sessions(account string) []string {
	if account == "" {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.accounts[account]
	if !ok || len(a.Sessions) == 0 {
		return nil
	}
	ids := make([]string, len(a.Sessions))
	for i, session := range a.Sessions {
		ids[i] = session.ID
	}
	return ids
}

// Authorize checks that a session may be used for something other than a
// password change.
func (d *DirectoryPasswordPolicy) Authorize(account, sessionID string) error {
	if account == "" {
		return ErrMissingField
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.accounts[account]
	if !ok {
		return ErrAccountNotFound
	}
	p, err := d.policyLocked(a.Tenant)
	if err != nil {
		return ErrSessionInvalid
	}
	now := d.clockLocked()
	for _, session := range a.Sessions {
		if session.ID != sessionID {
			continue
		}
		if session.ChangeOnly || a.MustChange || a.Temporary || passwordExpired(a, p, now) {
			return ErrSessionInvalid
		}
		return nil
	}
	return ErrSessionInvalid
}

// HistoryDepth reports how many historical digests are retained.
func (d *DirectoryPasswordPolicy) HistoryDepth(account string) int {
	if account == "" {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.accounts[account]
	if !ok {
		return 0
	}
	return len(a.History)
}

// GraceUsed reports how many grace logins were already spent.
func (d *DirectoryPasswordPolicy) GraceUsed(account string) int {
	if account == "" {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.accounts[account]
	if !ok {
		return 0
	}
	return a.GraceUsed
}

// Snapshot serialises accounts, history rings and sessions.
func (d *DirectoryPasswordPolicy) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureStateLocked()

	doc := snapshotDocument{
		Magic:         snapshotMagic,
		Version:       snapshotVersion,
		DefaultTenant: d.cfg.DefaultTenant,
		Policies:      clonePolicies(d.cfg.Policies),
		Accounts:      make([]accountRecord, 0, len(d.order)),
		NextSession:   d.nextSession,
	}
	for _, name := range d.order {
		a, ok := d.accounts[name]
		if !ok {
			continue
		}
		copyRecord := *a
		copyRecord.History = append([]string{}, a.History...)
		copyRecord.Sessions = append([]sessionRecord{}, a.Sessions...)
		doc.Accounts = append(doc.Accounts, copyRecord)
	}
	doc.Checksum = snapshotChecksum(&doc)
	data, err := json.Marshal(&doc)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (d *DirectoryPasswordPolicy) Restore(data []byte) error {
	doc, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	if len(doc.Accounts) > maxAccountsPerDirectory {
		return ErrBadSnapshot
	}
	accounts := make(map[string]*accountRecord, len(doc.Accounts))
	order := make([]string, 0, len(doc.Accounts))
	sessionIDs := make(map[string]struct{})
	for i := range doc.Accounts {
		source := doc.Accounts[i]
		if source.Account == "" || source.Tenant == "" {
			return ErrBadSnapshot
		}
		if _, exists := accounts[source.Account]; exists {
			return ErrBadSnapshot
		}
		p, ok := doc.Policies[source.Tenant]
		if !ok || validatePolicy(source.Tenant, p) != nil {
			return ErrBadSnapshot
		}
		hash, err := normalizeDigest(source.PasswordHash)
		if err != nil {
			return ErrBadSnapshot
		}
		if source.Failures < 0 || source.GraceUsed < 0 {
			return ErrBadSnapshot
		}
		if len(source.Sessions) > maxSessionsPerAccount {
			return ErrBadSnapshot
		}

		history := make([]string, 0, len(source.History))
		start := 0
		if len(source.History) > p.HistoryDepth {
			start = len(source.History) - p.HistoryDepth
		}
		for _, item := range source.History[start:] {
			normalized, err := normalizeDigest(item)
			if err != nil {
				return ErrBadSnapshot
			}
			history = append(history, normalized)
		}

		sessions := make([]sessionRecord, 0, len(source.Sessions))
		for _, session := range source.Sessions {
			if session.ID == "" {
				return ErrBadSnapshot
			}
			if _, exists := sessionIDs[session.ID]; exists {
				return ErrBadSnapshot
			}
			sessionIDs[session.ID] = struct{}{}
			session.IssuedAt = normalizeTime(session.IssuedAt)
			sessions = append(sessions, session)
		}

		copyRecord := source
		copyRecord.Account = source.Account
		copyRecord.Tenant = source.Tenant
		copyRecord.PasswordHash = hash
		copyRecord.History = history
		copyRecord.LockedAt = normalizeTime(source.LockedAt)
		copyRecord.LastChangedAt = normalizeTime(source.LastChangedAt)
		copyRecord.Sessions = sessions
		accounts[source.Account] = &copyRecord
		order = append(order, source.Account)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg.DefaultTenant = doc.DefaultTenant
	d.cfg.Policies = clonePolicies(doc.Policies)
	d.accounts = accounts
	d.order = order
	d.nextSession = doc.NextSession
	if d.nextSession == 0 {
		d.nextSession = 1
	}
	return nil
}

func (d *DirectoryPasswordPolicy) ensureStateLocked() {
	if d.accounts == nil {
		d.accounts = make(map[string]*accountRecord)
	}
	if d.order == nil {
		d.order = make([]string, 0)
	}
	if d.cfg.Policies == nil {
		d.cfg.Policies = make(map[string]Policy)
	}
	if d.nextSession == 0 {
		d.nextSession = 1
	}
}

func (d *DirectoryPasswordPolicy) clockLocked() time.Time {
	if d.cfg.Now == nil {
		return time.Time{}
	}
	return normalizeTime(d.cfg.Now())
}

func (d *DirectoryPasswordPolicy) policyLocked(tenant string) (Policy, error) {
	p, ok := d.cfg.Policies[tenant]
	if !ok || tenant == "" {
		return Policy{}, ErrUnknownTenant
	}
	if err := validatePolicy(tenant, p); err != nil {
		return Policy{}, err
	}
	return p, nil
}

func (d *DirectoryPasswordPolicy) evictOldestAccountsLocked() {
	for len(d.order) > maxAccountsPerDirectory {
		oldest := d.order[0]
		delete(d.accounts, oldest)
		d.order = append([]string(nil), d.order[1:]...)
	}
}

func (d *DirectoryPasswordPolicy) addSessionLocked(a *accountRecord, changeOnly bool, now time.Time) string {
	session := sessionRecord{
		ID:         d.newSessionIDLocked(a.Account, now),
		ChangeOnly: changeOnly,
		IssuedAt:   normalizeTime(now),
	}
	a.Sessions = append(a.Sessions, session)
	if len(a.Sessions) > maxSessionsPerAccount {
		a.Sessions = append([]sessionRecord(nil), a.Sessions[len(a.Sessions)-maxSessionsPerAccount:]...)
	}
	return session.ID
}

func (d *DirectoryPasswordPolicy) newSessionIDLocked(account string, now time.Time) string {
	d.nextSession++
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	sum := sha256.Sum256([]byte(account + "|" + strconv.FormatUint(d.nextSession, 10) + "|" + strconv.FormatInt(now.UnixNano(), 10)))
	return hex.EncodeToString(sum[:])
}

func (d *DirectoryPasswordPolicy) clearExpiredLockLocked(a *accountRecord, p Policy, now time.Time) {
	if a.LockedAt.IsZero() || accountLocked(a, p, now) {
		return
	}
	a.LockedAt = time.Time{}
	a.Failures = 0
}

func (d *DirectoryPasswordPolicy) applyFailureThresholdLocked(a *accountRecord, p Policy, now time.Time) {
	if a.LockedAt.IsZero() && a.Failures >= p.MaxFailures {
		a.LockedAt = now
	}
}

func accountLocked(a *accountRecord, p Policy, now time.Time) bool {
	if a.LockedAt.IsZero() || p.LockoutMinutes <= 0 {
		return false
	}
	if a.Failures < p.MaxFailures {
		return false
	}
	return now.Before(a.LockedAt.Add(minutesDuration(p.LockoutMinutes)))
}

func accountState(a *accountRecord, p Policy, now time.Time) string {
	if accountLocked(a, p, now) {
		return StateLocked
	}
	if passwordExpired(a, p, now) {
		if a.GraceUsed < p.GraceLogins {
			return StateExpiredGrace
		}
		return StateExpired
	}
	if a.MustChange {
		return StateMustChange
	}
	return StateActive
}

func passwordExpired(a *accountRecord, p Policy, now time.Time) bool {
	if p.MaxAgeDays <= 0 || a.LastChangedAt.IsZero() {
		return false
	}
	return now.Sub(a.LastChangedAt) >= daysDuration(p.MaxAgeDays)
}

func passwordAgeDays(a *accountRecord, now time.Time) int {
	if a.LastChangedAt.IsZero() {
		return 0
	}
	age := now.Sub(a.LastChangedAt)
	if age <= 0 {
		return 0
	}
	return int(age / (24 * time.Hour))
}

func graceRemaining(a *accountRecord, p Policy) int {
	remaining := p.GraceLogins - a.GraceUsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

func attemptsRemaining(a *accountRecord, p Policy) int {
	remaining := p.MaxFailures - a.Failures
	if remaining < 0 {
		return 0
	}
	return remaining
}

func appendHistory(history []string, oldHash string, depth int) []string {
	if depth <= 0 {
		return make([]string, 0)
	}
	history = append(history, oldHash)
	if len(history) > depth {
		history = append([]string(nil), history[len(history)-depth:]...)
	}
	return history
}

func hashUsed(a *accountRecord, p Policy, candidate string) bool {
	if digestEqual(a.PasswordHash, candidate) {
		return true
	}
	limit := len(a.History)
	if limit > p.HistoryDepth {
		limit = p.HistoryDepth
	}
	if limit < 0 {
		limit = 0
	}
	for _, old := range a.History[len(a.History)-limit:] {
		if digestEqual(old, candidate) {
			return true
		}
	}
	return false
}

func digestPassword(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func normalizeDigest(value string) (string, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != sha256.Size {
		return "", ErrBadSnapshot
	}
	return hex.EncodeToString(raw), nil
}

func passwordHashMatches(want, password string) bool {
	raw, err := hex.DecodeString(want)
	if err != nil || len(raw) != sha256.Size {
		return false
	}
	sum := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(raw, sum[:]) == 1
}

func digestEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func validatePassword(password string, p Policy) error {
	length := utf8.RuneCountInString(password)
	if length < p.MinLength || length > p.MaxLength {
		return ErrPasswordPolicy
	}
	if passwordClassCount(password) < p.MinClasses {
		return ErrPasswordPolicy
	}
	return nil
}

func passwordClassCount(password string) int {
	var lower, upper, digit, symbol bool
	for _, r := range password {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			symbol = true
		}
	}
	count := 0
	for _, present := range []bool{lower, upper, digit, symbol} {
		if present {
			count++
		}
	}
	return count
}

func validatePolicy(tenant string, p Policy) error {
	if tenant == "" || p.Tenant != tenant {
		return ErrPasswordPolicy
	}
	if p.MinLength <= 0 || p.MaxLength < p.MinLength {
		return ErrPasswordPolicy
	}
	if p.MinClasses <= 0 || p.MinClasses > 4 {
		return ErrPasswordPolicy
	}
	if p.HistoryDepth < 0 || p.MaxAgeDays < 0 || p.GraceLogins < 0 {
		return ErrPasswordPolicy
	}
	if p.MaxFailures <= 0 || p.LockoutMinutes <= 0 {
		return ErrPasswordPolicy
	}
	return nil
}

func clonePolicies(source map[string]Policy) map[string]Policy {
	copied := make(map[string]Policy, len(source))
	for tenant, policy := range source {
		copied[tenant] = policy
	}
	return copied
}

func normalizeTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.Round(0).UTC()
}

func daysDuration(days int) time.Duration {
	if days <= 0 {
		return 0
	}
	const maxDays = int64((1<<63 - 1) / int64(24*time.Hour))
	if int64(days) >= maxDays {
		return time.Duration(1<<63 - 1)
	}
	return time.Duration(days) * 24 * time.Hour
}

func minutesDuration(minutes int) time.Duration {
	if minutes <= 0 {
		return 0
	}
	const maxMinutes = int64((1<<63 - 1) / int64(time.Minute))
	if int64(minutes) >= maxMinutes {
		return time.Duration(1<<63 - 1)
	}
	return time.Duration(minutes) * time.Minute
}

func decodeSnapshot(data []byte) (*snapshotDocument, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, ErrBadSnapshot
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var doc snapshotDocument
	if err := decoder.Decode(&doc); err != nil {
		return nil, ErrBadSnapshot
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, ErrBadSnapshot
	}
	if doc.Magic != snapshotMagic || doc.Version != snapshotVersion {
		return nil, ErrBadSnapshot
	}
	if !digestEqual(doc.Checksum, snapshotChecksum(&doc)) {
		return nil, ErrBadSnapshot
	}
	for tenant, p := range doc.Policies {
		if validatePolicy(tenant, p) != nil {
			return nil, ErrBadSnapshot
		}
	}
	return &doc, nil
}

func snapshotChecksum(doc *snapshotDocument) string {
	copyDoc := *doc
	copyDoc.Checksum = ""
	data, err := json.Marshal(&copyDoc)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
