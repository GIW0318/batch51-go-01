// Package task keeps the alias ledger of renamed principals so that tokens and
// audits issued under an old identifier still land on the right subject.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

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

const aliasSnapshotVersion = 1

type aliasLedgerSnapshot struct {
	Version      int               `json:"version"`
	Tenant       string            `json:"tenant,omitempty"`
	Seq          int64             `json:"seq"`
	Live         map[string]string `json:"live"`
	Aliases      []Alias           `json:"aliases"`
	Reservations []Reservation     `json:"reservations"`
}

// PrincipalAliasLedger owns the live names, the alias chains and the
// reservation table.
type PrincipalAliasLedger struct {
	mu sync.RWMutex

	cfg AliasConfig

	seq          int64
	live         map[string]string
	principals   map[string]string
	aliases      map[string]Alias
	reservations map[string]Reservation
}

// NewPrincipalAliasLedger builds a ledger for one tenant.
func NewPrincipalAliasLedger(cfg AliasConfig) *PrincipalAliasLedger {
	return &PrincipalAliasLedger{
		cfg:          cfg,
		live:         make(map[string]string),
		principals:   make(map[string]string),
		aliases:      make(map[string]Alias),
		reservations: make(map[string]Reservation),
	}
}

func (l *PrincipalAliasLedger) now() time.Time {
	if l.cfg.Now != nil {
		return l.cfg.Now()
	}
	return time.Now()
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func (l *PrincipalAliasLedger) nextSeq() int64 {
	l.seq++
	return l.seq
}

// Register claims the first name of a new principal.
func (l *PrincipalAliasLedger) Register(principal, name string, at time.Time) error {
	if blank(principal) || blank(name) {
		return ErrBlankName
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.principals[principal]; ok {
		return ErrPrincipalExists
	}
	if _, ok := l.live[name]; ok {
		return ErrNameTaken
	}

	removeAlias, removeReservation, err := l.nameAvailability(name, at, "")
	if err != nil {
		return err
	}
	if removeAlias {
		delete(l.aliases, name)
	}
	if removeReservation {
		delete(l.reservations, name)
	}

	l.live[name] = principal
	l.principals[principal] = name
	return nil
}

// Rename retires the current name and makes the new one live.
func (l *PrincipalAliasLedger) Rename(principal, newName string, at time.Time) (RenameResult, error) {
	if blank(principal) || blank(newName) {
		return RenameResult{}, ErrBlankName
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	oldName, ok := l.principals[principal]
	if !ok {
		return RenameResult{}, ErrUnknownPrincipal
	}
	if newName == oldName {
		return RenameResult{}, ErrSameName
	}

	removeAlias, removeReservation, err := l.nameAvailability(newName, at, principal)
	if err != nil {
		return RenameResult{}, err
	}

	historyCount := l.historyCount(principal)
	if removeAlias {
		if alias, exists := l.aliases[newName]; exists && alias.Principal == principal {
			historyCount--
		}
	}
	if historyCount+1 > l.cfg.MaxHistory {
		return RenameResult{}, ErrHistoryLimit
	}
	if historyCount+1 > l.cfg.MaxChainDepth {
		return RenameResult{}, ErrChainDepth
	}

	if removeAlias {
		delete(l.aliases, newName)
	}
	if removeReservation {
		delete(l.reservations, newName)
	}

	seq := l.nextSeq()
	alias := Alias{
		Name:      oldName,
		Principal: principal,
		From:      at,
		Until:     at.Add(l.cfg.Retention),
		Seq:       seq,
	}

	delete(l.live, oldName)
	l.aliases[oldName] = alias
	l.live[newName] = principal
	l.principals[principal] = newName

	return RenameResult{
		Principal: principal,
		OldName:   oldName,
		NewName:   newName,
		Seq:       seq,
		Chain:     l.chainLocked(principal),
	}, nil
}

// nameAvailability checks a target name without changing the ledger. The bool
// results tell the caller which expired entries may be removed on commit.
func (l *PrincipalAliasLedger) nameAvailability(name string, at time.Time, samePrincipal string) (bool, bool, error) {
	if owner, ok := l.live[name]; ok {
		if owner != samePrincipal {
			return false, false, ErrNameTaken
		}
	}

	removeAlias := false
	if alias, ok := l.aliases[name]; ok {
		if alias.Until.After(at) {
			if alias.Principal == samePrincipal {
				return false, false, ErrAliasCycle
			}
			return false, false, ErrNameTaken
		}
		removeAlias = true
	}

	removeReservation := false
	if reservation, ok := l.reservations[name]; ok {
		if reservation.Until.After(at) {
			return false, false, ErrNameTaken
		}
		removeReservation = true
	}

	return removeAlias, removeReservation, nil
}

// Resolve maps any live, aliased or reserved name to its owner.
func (l *PrincipalAliasLedger) Resolve(name string) (Resolution, error) {
	if blank(name) {
		return Resolution{}, ErrBlankName
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	now := l.now()
	if principal, ok := l.live[name]; ok {
		return Resolution{
			Name:      name,
			Principal: principal,
			Canonical: name,
			Found:     true,
		}, nil
	}

	if alias, ok := l.aliases[name]; ok && alias.Until.After(now) {
		return Resolution{
			Name:      name,
			Principal: alias.Principal,
			Canonical: l.principals[alias.Principal],
			Found:     true,
			Alias:     true,
			Until:     alias.Until,
		}, nil
	}

	if reservation, ok := l.reservations[name]; ok && reservation.Until.After(now) {
		return Resolution{
			Name:      name,
			Principal: reservation.Owner,
			Reserved:  true,
			Until:     reservation.Until,
		}, nil
	}

	return Resolution{}, ErrNameNotFound
}

// Reserve holds a name for one owner until an instant.
func (l *PrincipalAliasLedger) Reserve(name string, until time.Time, owner string) error {
	if blank(name) || blank(owner) {
		return ErrBlankName
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if _, ok := l.live[name]; ok {
		return ErrNameTaken
	}

	removeAlias := false
	if alias, ok := l.aliases[name]; ok {
		if alias.Until.After(now) {
			return ErrNameTaken
		}
		removeAlias = true
	}

	removeReservation := false
	if reservation, ok := l.reservations[name]; ok {
		if reservation.Until.After(now) {
			return ErrNameTaken
		}
		removeReservation = true
	}

	activeReservations := 0
	for _, reservation := range l.reservations {
		if reservation.Until.After(now) {
			activeReservations++
		}
	}
	if removeReservation {
		activeReservations--
	}
	if activeReservations+1 > l.cfg.MaxReserved {
		return ErrReserveLimit
	}

	if removeAlias {
		delete(l.aliases, name)
	}
	if removeReservation {
		delete(l.reservations, name)
	}
	l.reservations[name] = Reservation{Name: name, Owner: owner, Until: until}
	l.nextSeq()
	return nil
}

// Release drops a reservation before it expires.
func (l *PrincipalAliasLedger) Release(name string) error {
	if blank(name) {
		return ErrBlankName
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	reservation, ok := l.reservations[name]
	if !ok {
		return ErrNotReserved
	}
	if !reservation.Until.After(l.now()) {
		delete(l.reservations, name)
		return ErrNotReserved
	}
	delete(l.reservations, name)
	return nil
}

// History returns the retired names of one principal, oldest first.
func (l *PrincipalAliasLedger) History(principal string) ([]Alias, error) {
	if blank(principal) {
		return nil, ErrBlankName
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	if _, ok := l.principals[principal]; !ok {
		return nil, ErrUnknownPrincipal
	}

	history := make([]Alias, 0)
	for _, alias := range l.aliases {
		if alias.Principal == principal {
			history = append(history, alias)
		}
	}
	sortAliases(history)
	return history, nil
}

// Chain returns the live name and every retired name of one principal.
func (l *PrincipalAliasLedger) Chain(principal string) ([]string, error) {
	if blank(principal) {
		return nil, ErrBlankName
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	if _, ok := l.principals[principal]; !ok {
		return nil, ErrUnknownPrincipal
	}
	return l.chainLocked(principal), nil
}

func (l *PrincipalAliasLedger) historyCount(principal string) int {
	count := 0
	for _, alias := range l.aliases {
		if alias.Principal == principal {
			count++
		}
	}
	return count
}

func (l *PrincipalAliasLedger) chainLocked(principal string) []string {
	history := make([]Alias, 0)
	for _, alias := range l.aliases {
		if alias.Principal == principal {
			history = append(history, alias)
		}
	}
	sortAliases(history)

	chain := make([]string, 0, len(history)+1)
	for _, alias := range history {
		chain = append(chain, alias.Name)
	}
	chain = append(chain, l.principals[principal])
	return chain
}

func sortAliases(aliases []Alias) {
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].Seq != aliases[j].Seq {
			return aliases[i].Seq < aliases[j].Seq
		}
		if !aliases[i].From.Equal(aliases[j].From) {
			return aliases[i].From.Before(aliases[j].From)
		}
		return aliases[i].Name < aliases[j].Name
	})
}

// Reservations lists the reservations still in force at an instant.
func (l *PrincipalAliasLedger) Reservations(now time.Time) []Reservation {
	l.mu.RLock()
	defer l.mu.RUnlock()

	reservations := make([]Reservation, 0)
	for _, reservation := range l.reservations {
		if reservation.Until.After(now) {
			reservations = append(reservations, reservation)
		}
	}
	sort.Slice(reservations, func(i, j int) bool {
		if reservations[i].Name != reservations[j].Name {
			return reservations[i].Name < reservations[j].Name
		}
		return reservations[i].Until.Before(reservations[j].Until)
	})
	return reservations
}

// Prune drops expired aliases and reservations, returning how many went away.
func (l *PrincipalAliasLedger) Prune(now time.Time) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	removed := 0
	for name, alias := range l.aliases {
		if !alias.Until.After(now) {
			delete(l.aliases, name)
			removed++
		}
	}
	for name, reservation := range l.reservations {
		if !reservation.Until.After(now) {
			delete(l.reservations, name)
			removed++
		}
	}
	return removed, nil
}

// Snapshot serialises the ledger and its sequence watermark.
func (l *PrincipalAliasLedger) Snapshot() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	live := make(map[string]string, len(l.live))
	for name, principal := range l.live {
		live[name] = principal
	}

	aliases := make([]Alias, 0, len(l.aliases))
	for _, alias := range l.aliases {
		aliases = append(aliases, alias)
	}
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].Principal != aliases[j].Principal {
			return aliases[i].Principal < aliases[j].Principal
		}
		if aliases[i].Seq != aliases[j].Seq {
			return aliases[i].Seq < aliases[j].Seq
		}
		return aliases[i].Name < aliases[j].Name
	})

	reservations := make([]Reservation, 0, len(l.reservations))
	for _, reservation := range l.reservations {
		reservations = append(reservations, reservation)
	}
	sort.Slice(reservations, func(i, j int) bool {
		return reservations[i].Name < reservations[j].Name
	})

	payload := aliasLedgerSnapshot{
		Version:      aliasSnapshotVersion,
		Tenant:       l.cfg.Tenant,
		Seq:          l.seq,
		Live:         live,
		Aliases:      aliases,
		Reservations: reservations,
	}
	return json.Marshal(payload)
}

// Restore replaces the whole ledger with a snapshot, all or nothing.
func (l *PrincipalAliasLedger) Restore(data []byte) error {
	var payload aliasLedgerSnapshot
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(payload); err != nil {
		return ErrBadSnapshot
	}

	live := make(map[string]string, len(payload.Live))
	principals := make(map[string]string, len(payload.Live))
	for name, principal := range payload.Live {
		live[name] = principal
		principals[principal] = name
	}
	aliases := make(map[string]Alias, len(payload.Aliases))
	for _, alias := range payload.Aliases {
		aliases[alias.Name] = alias
	}
	reservations := make(map[string]Reservation, len(payload.Reservations))
	for _, reservation := range payload.Reservations {
		reservations[reservation.Name] = reservation
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq = payload.Seq
	l.live = live
	l.principals = principals
	l.aliases = aliases
	l.reservations = reservations
	return nil
}

func validateSnapshot(payload aliasLedgerSnapshot) error {
	if payload.Version != aliasSnapshotVersion || payload.Seq < 0 {
		return ErrBadSnapshot
	}
	if payload.Live == nil || payload.Aliases == nil || payload.Reservations == nil {
		return ErrBadSnapshot
	}

	seenNames := make(map[string]struct{}, len(payload.Live)+len(payload.Aliases)+len(payload.Reservations))
	principals := make(map[string]string, len(payload.Live))
	for name, principal := range payload.Live {
		if blank(name) || blank(principal) {
			return ErrBadSnapshot
		}
		if _, exists := seenNames[name]; exists {
			return ErrBadSnapshot
		}
		if _, exists := principals[principal]; exists {
			return ErrBadSnapshot
		}
		seenNames[name] = struct{}{}
		principals[principal] = name
	}

	seenAliasSeqs := make(map[int64]struct{}, len(payload.Aliases))
	for _, alias := range payload.Aliases {
		if blank(alias.Name) || blank(alias.Principal) || alias.Seq <= 0 || alias.Seq > payload.Seq {
			return ErrBadSnapshot
		}
		if _, exists := principals[alias.Principal]; !exists {
			return ErrBadSnapshot
		}
		if _, exists := seenNames[alias.Name]; exists {
			return ErrBadSnapshot
		}
		if _, exists := seenAliasSeqs[alias.Seq]; exists {
			return ErrBadSnapshot
		}
		seenNames[alias.Name] = struct{}{}
		seenAliasSeqs[alias.Seq] = struct{}{}
	}

	for _, reservation := range payload.Reservations {
		if blank(reservation.Name) || blank(reservation.Owner) {
			return ErrBadSnapshot
		}
		if _, exists := seenNames[reservation.Name]; exists {
			return ErrBadSnapshot
		}
		seenNames[reservation.Name] = struct{}{}
	}
	return nil
}
