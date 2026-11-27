// Package task keeps the collection codex of every account: it books discovery
// events, unlocks variants, awards finished sets once, converts surplus copies
// and remaps the entries when the catalog version changes.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors reported by the codex.
var (
	ErrBadEntry       = errors.New("catalog entry is invalid")
	ErrDuplicateEntry = errors.New("catalog entry already exists")
	ErrUnknownEntry   = errors.New("unknown catalog entry")
	ErrBadSet         = errors.New("set definition is invalid")
	ErrDuplicateSet   = errors.New("set already exists")
	ErrUnknownSet     = errors.New("unknown set")
	ErrBadEvent       = errors.New("discovery event is invalid")
	ErrBadAccount     = errors.New("account id is required")
	ErrBadQty         = errors.New("quantity is outside the allowed range")
	ErrNoDuplicate    = errors.New("entry has no surplus copy to convert")
	ErrDailyLimit     = errors.New("daily conversion budget reached")
	ErrSetIncomplete  = errors.New("set is not complete yet")
	ErrAlreadyClaimed = errors.New("set reward was already claimed")
	ErrUnknownVersion = errors.New("unknown catalog version")
	ErrBadVersion     = errors.New("catalog version order is invalid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// CatalogEntry is one collectible of the catalog.
type CatalogEntry struct {
	ID      string
	Name    string
	SetID   string
	Hidden  bool
	Removed bool
}

// EntrySet is one set of the catalog and its one shot reward.
type EntrySet struct {
	ID           string
	EntryIDs     []string
	RewardID     string
	RewardAmount int
}

// VersionSpec is one catalog version and the entry ids it renames or removes.
type VersionSpec struct {
	ID      string
	Order   int
	Renames map[string]string
	Removed []string
}

// DiscoverEvent is one reported discovery.
type DiscoverEvent struct {
	EventID string
	EntryID string
	Variant string
	Qty     int
}

// DiscoverResult is the booked outcome of one discovery.
type DiscoverResult struct {
	EntryID         string
	Count           int
	Duplicate       bool
	VariantUnlocked bool
	CompletedSets   []string
	Replayed        bool
}

// SetReward is the answer of one claim.
type SetReward struct {
	SetID          string
	RewardID       string
	Amount         int
	AlreadyClaimed bool
	Missing        []string
}

// ConvertResult is the answer of one duplicate conversion.
type ConvertResult struct {
	EntryID    string
	Consumed   int
	Produced   int
	DailyUsed  int
	DailyLimit int
}

// Reason explains one entry of the codex.
type Reason struct {
	Kind   string
	Detail string
}

// CodexStats summarises one ledger.
type CodexStats struct {
	Entries       int
	Sets          int
	Accounts      int
	Discovered    int
	Variants      int
	SetsCompleted int
	SetsClaimed   int
	Converted     int
	Legacy        int
}

// CodexConfig bounds one ledger.
type CodexConfig struct {
	MaxEntriesPerAccount int
	MaxEvents            int
	MaxDailyConvert      int
	ConvertRate          int
}

type codexEntryState struct {
	Count    int
	Revealed bool
	Legacy   bool
	Variants map[string]struct{}
}

type accountCodex struct {
	Version   string
	Entries   map[string]*codexEntryState
	Events    map[string]DiscoverResult
	DailyDay  string
	DailyUsed int
	Converted int
	Completed map[string]struct{}
	Claimed   map[string]struct{}
}

// CollectionCodexLedger owns the catalog, the account codexes and the version
// table.
type CollectionCodexLedger struct {
	cfg CodexConfig

	mu       sync.RWMutex
	entries  map[string]CatalogEntry
	sets     map[string]EntrySet
	versions map[string]VersionSpec
	accounts map[string]*accountCodex
}

// NewCollectionCodexLedger builds a ledger with the given bounds.
func NewCollectionCodexLedger(cfg CodexConfig) *CollectionCodexLedger {
	return &CollectionCodexLedger{
		cfg:      cfg,
		entries:  make(map[string]CatalogEntry),
		sets:     make(map[string]EntrySet),
		versions: make(map[string]VersionSpec),
		accounts: make(map[string]*accountCodex),
	}
}

func blank(s string) bool {
	return strings.TrimSpace(s) == ""
}

func cloneStrings(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	return append([]string(nil), src...)
}

func cloneSet(s EntrySet) EntrySet {
	s.EntryIDs = cloneStrings(s.EntryIDs)
	return s
}

func cloneVersion(v VersionSpec) VersionSpec {
	copyV := v
	copyV.Renames = make(map[string]string, len(v.Renames))
	for from, to := range v.Renames {
		copyV.Renames[from] = to
	}
	copyV.Removed = cloneStrings(v.Removed)
	return copyV
}

func cloneResult(r DiscoverResult) DiscoverResult {
	r.CompletedSets = cloneStrings(r.CompletedSets)
	return r
}

func newAccountCodex() *accountCodex {
	return &accountCodex{
		Entries:   make(map[string]*codexEntryState),
		Events:    make(map[string]DiscoverResult),
		Completed: make(map[string]struct{}),
		Claimed:   make(map[string]struct{}),
	}
}

func newCodexEntryState() *codexEntryState {
	return &codexEntryState{Variants: make(map[string]struct{})}
}

func (l *CollectionCodexLedger) entryCapReached(a *accountCodex, entryID string) bool {
	if a == nil || l.cfg.MaxEntriesPerAccount <= 0 {
		return false
	}
	if _, exists := a.Entries[entryID]; exists {
		return false
	}
	return len(a.Entries) >= l.cfg.MaxEntriesPerAccount
}

func (l *CollectionCodexLedger) eventCapReached(a *accountCodex) bool {
	return a != nil && l.cfg.MaxEvents > 0 && len(a.Events) >= l.cfg.MaxEvents
}

func (l *CollectionCodexLedger) setCompleteLocked(a *accountCodex, set EntrySet) bool {
	if a == nil || len(set.EntryIDs) == 0 {
		return false
	}
	for _, entryID := range set.EntryIDs {
		st := a.Entries[entryID]
		if st == nil || st.Count <= 0 || st.Legacy {
			return false
		}
		if entry, ok := l.entries[entryID]; ok && entry.Hidden && !st.Revealed {
			return false
		}
	}
	return true
}

func (l *CollectionCodexLedger) completedSetsForEntryLocked(a *accountCodex, entryID string) []string {
	if a == nil {
		return nil
	}
	var completed []string
	for setID, set := range l.sets {
		if _, done := a.Completed[setID]; done {
			continue
		}
		contains := false
		for _, id := range set.EntryIDs {
			if id == entryID {
				contains = true
				break
			}
		}
		if contains && l.setCompleteLocked(a, set) {
			a.Completed[setID] = struct{}{}
			completed = append(completed, setID)
		}
	}
	sort.Strings(completed)
	return completed
}

func (l *CollectionCodexLedger) completeSetForAllAccountsLocked(set EntrySet) {
	for _, a := range l.accounts {
		if _, done := a.Completed[set.ID]; done {
			continue
		}
		if l.setCompleteLocked(a, set) {
			a.Completed[set.ID] = struct{}{}
		}
	}
}

func (l *CollectionCodexLedger) missingEntriesLocked(a *accountCodex, set EntrySet) []string {
	missing := make([]string, 0)
	for _, entryID := range set.EntryIDs {
		if a == nil {
			missing = append(missing, entryID)
			continue
		}
		st := a.Entries[entryID]
		if st == nil || st.Count <= 0 || st.Legacy {
			missing = append(missing, entryID)
			continue
		}
		if entry, ok := l.entries[entryID]; ok && entry.Hidden && !st.Revealed {
			missing = append(missing, entryID)
		}
	}
	return missing
}

// RegisterEntry adds one catalog entry.
func (l *CollectionCodexLedger) RegisterEntry(e CatalogEntry) error {
	if blank(e.ID) || blank(e.Name) {
		return ErrBadEntry
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.entries[e.ID]; exists {
		return ErrDuplicateEntry
	}
	l.entries[e.ID] = e
	return nil
}

// RegisterSet adds one set and its reward.
func (l *CollectionCodexLedger) RegisterSet(s EntrySet) error {
	if blank(s.ID) || len(s.EntryIDs) == 0 {
		return ErrBadSet
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.sets[s.ID]; exists {
		return ErrDuplicateSet
	}

	seen := make(map[string]struct{}, len(s.EntryIDs))
	for _, entryID := range s.EntryIDs {
		if _, duplicate := seen[entryID]; duplicate {
			return ErrBadSet
		}
		seen[entryID] = struct{}{}
		if _, exists := l.entries[entryID]; !exists {
			return ErrUnknownEntry
		}
	}

	stored := cloneSet(s)
	l.sets[stored.ID] = stored
	l.completeSetForAllAccountsLocked(stored)
	return nil
}

// RegisterVersion adds one catalog version.
func (l *CollectionCodexLedger) RegisterVersion(v VersionSpec) error {
	if blank(v.ID) {
		return ErrBadVersion
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.versions[v.ID]; exists {
		return ErrBadVersion
	}
	l.versions[v.ID] = cloneVersion(v)
	return nil
}

// Discover books one discovery event.
func (l *CollectionCodexLedger) Discover(acct string, ev DiscoverEvent) (DiscoverResult, error) {
	if blank(ev.EventID) {
		return DiscoverResult{}, ErrBadEvent
	}
	if blank(acct) {
		return DiscoverResult{}, ErrBadAccount
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	a := l.accounts[acct]
	if a != nil {
		if first, exists := a.Events[ev.EventID]; exists {
			replay := cloneResult(first)
			replay.Replayed = true
			return replay, nil
		}
	}

	if _, exists := l.entries[ev.EntryID]; !exists {
		return DiscoverResult{}, ErrUnknownEntry
	}
	if ev.Qty <= 0 {
		return DiscoverResult{}, ErrBadQty
	}
	maxInt := int(^uint(0) >> 1)
	if a != nil {
		if st := a.Entries[ev.EntryID]; st != nil && ev.Qty > maxInt-st.Count {
			return DiscoverResult{}, ErrBadQty
		}
	}
	if l.entryCapReached(a, ev.EntryID) {
		return DiscoverResult{}, ErrBadEntry
	}
	if l.eventCapReached(a) {
		return DiscoverResult{}, ErrBadEvent
	}

	if a == nil {
		a = newAccountCodex()
		l.accounts[acct] = a
	}
	st := a.Entries[ev.EntryID]
	if st == nil {
		st = newCodexEntryState()
		a.Entries[ev.EntryID] = st
	}

	wasDuplicate := st.Count > 0
	st.Count += ev.Qty

	variantUnlocked := false
	if ev.Variant != "" {
		if _, exists := st.Variants[ev.Variant]; !exists {
			st.Variants[ev.Variant] = struct{}{}
			variantUnlocked = true
		}
	}

	newlyCompleted := l.completedSetsForEntryLocked(a, ev.EntryID)
	result := DiscoverResult{
		EntryID:         ev.EntryID,
		Count:           st.Count,
		Duplicate:       wasDuplicate,
		VariantUnlocked: variantUnlocked,
		CompletedSets:   newlyCompleted,
	}
	a.Events[ev.EventID] = cloneResult(result)
	return cloneResult(result), nil
}

// Reveal lifts the hidden flag of one entry for one account.
func (l *CollectionCodexLedger) Reveal(acct string, entryID string) error {
	if blank(acct) {
		return ErrBadAccount
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.entries[entryID]; !exists {
		return ErrUnknownEntry
	}

	a := l.accounts[acct]
	if l.entryCapReached(a, entryID) {
		return ErrBadEntry
	}
	if a == nil {
		a = newAccountCodex()
		l.accounts[acct] = a
	}
	st := a.Entries[entryID]
	if st == nil {
		st = newCodexEntryState()
		a.Entries[entryID] = st
	}
	st.Revealed = true
	_ = l.completedSetsForEntryLocked(a, entryID)
	return nil
}

// UnlockVariant unlocks one variant of one entry.
func (l *CollectionCodexLedger) UnlockVariant(acct string, entryID string, variant string) error {
	if blank(acct) {
		return ErrBadAccount
	}
	if blank(variant) {
		return ErrBadEvent
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.entries[entryID]; !exists {
		return ErrUnknownEntry
	}

	a := l.accounts[acct]
	if l.entryCapReached(a, entryID) {
		return ErrBadEntry
	}
	if a == nil {
		a = newAccountCodex()
		l.accounts[acct] = a
	}
	st := a.Entries[entryID]
	if st == nil {
		st = newCodexEntryState()
		a.Entries[entryID] = st
	}
	st.Variants[variant] = struct{}{}
	return nil
}

// ClaimSet pays the reward of one finished set.
func (l *CollectionCodexLedger) ClaimSet(acct string, setID string) (SetReward, error) {
	if blank(acct) {
		return SetReward{}, ErrBadAccount
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	set, exists := l.sets[setID]
	if !exists {
		return SetReward{}, ErrUnknownSet
	}
	reward := SetReward{
		SetID:    set.ID,
		RewardID: set.RewardID,
		Amount:   set.RewardAmount,
	}

	a := l.accounts[acct]
	if a != nil {
		if _, claimed := a.Claimed[setID]; claimed {
			reward.AlreadyClaimed = true
			return reward, ErrAlreadyClaimed
		}
	}

	if a == nil {
		reward.Missing = cloneStrings(set.EntryIDs)
		return reward, ErrSetIncomplete
	}
	if _, complete := a.Completed[setID]; !complete {
		reward.Missing = l.missingEntriesLocked(a, set)
		return reward, ErrSetIncomplete
	}

	a.Claimed[setID] = struct{}{}
	return reward, nil
}

// ConvertDuplicate turns surplus copies into shards.
func (l *CollectionCodexLedger) ConvertDuplicate(acct string, entryID string, qty int) (ConvertResult, error) {
	if blank(acct) {
		return ConvertResult{}, ErrBadAccount
	}
	if qty <= 0 {
		return ConvertResult{}, ErrBadQty
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.entries[entryID]; !exists {
		return ConvertResult{}, ErrUnknownEntry
	}

	a := l.accounts[acct]
	var st *codexEntryState
	if a != nil {
		st = a.Entries[entryID]
	}
	if st == nil || st.Count < 2 {
		return ConvertResult{}, ErrNoDuplicate
	}
	surplus := st.Count - 1
	if qty > surplus {
		return ConvertResult{}, ErrBadQty
	}

	today := time.Now().UTC().Format("2006-01-02")
	if a.DailyDay != today {
		a.DailyDay = today
		a.DailyUsed = 0
	}
	if l.cfg.MaxDailyConvert > 0 && a.DailyUsed >= l.cfg.MaxDailyConvert {
		return ConvertResult{}, ErrDailyLimit
	}
	if l.cfg.ConvertRate > 0 && qty > int(^uint(0)>>1)/l.cfg.ConvertRate {
		return ConvertResult{}, ErrBadQty
	}

	st.Count -= qty
	a.Converted += qty
	a.DailyUsed++

	return ConvertResult{
		EntryID:    entryID,
		Consumed:   qty,
		Produced:   qty * l.cfg.ConvertRate,
		DailyUsed:  a.DailyUsed,
		DailyLimit: l.cfg.MaxDailyConvert,
	}, nil
}

func mergeEntryState(dst, src *codexEntryState) *codexEntryState {
	if dst == nil {
		return src
	}
	dst.Count += src.Count
	dst.Revealed = dst.Revealed || src.Revealed
	dst.Legacy = dst.Legacy || src.Legacy
	for variant := range src.Variants {
		dst.Variants[variant] = struct{}{}
	}
	return dst
}

func (l *CollectionCodexLedger) applyVersionLocked(a *accountCodex, v VersionSpec) {
	removed := make(map[string]struct{}, len(v.Removed))
	for _, oldID := range v.Removed {
		removed[oldID] = struct{}{}
		if st := a.Entries[oldID]; st != nil {
			st.Legacy = true
		}
	}

	next := make(map[string]*codexEntryState, len(a.Entries))
	eventTargets := make(map[string]string, len(v.Renames))
	for oldID, st := range a.Entries {
		if _, isRemoved := removed[oldID]; isRemoved {
			next[oldID] = st
			continue
		}
		newID, rename := v.Renames[oldID]
		if !rename || blank(newID) || st.Legacy {
			newID = oldID
		}
		next[newID] = mergeEntryState(next[newID], st)
		if newID != oldID {
			eventTargets[oldID] = newID
		}
	}
	a.Entries = next
	for eventID, eventResult := range a.Events {
		if newID, rename := eventTargets[eventResult.EntryID]; rename {
			eventResult.EntryID = newID
			a.Events[eventID] = eventResult
		}
	}
}

// Migrate remaps the codex of one account to a newer catalog version.
func (l *CollectionCodexLedger) Migrate(acct string, fromVer, toVer string) error {
	if blank(acct) {
		return ErrBadAccount
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	from, exists := l.versions[fromVer]
	if !exists {
		return ErrUnknownVersion
	}
	to, exists := l.versions[toVer]
	if !exists {
		return ErrUnknownVersion
	}
	if to.Order <= from.Order {
		return ErrBadVersion
	}

	specs := make([]VersionSpec, 0)
	batchSize := 0
	for _, v := range l.versions {
		if v.Order > from.Order && v.Order <= to.Order {
			specs = append(specs, cloneVersion(v))
			batchSize += len(v.Renames) + len(v.Removed)
		}
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Order != specs[j].Order {
			return specs[i].Order < specs[j].Order
		}
		return specs[i].ID < specs[j].ID
	})
	a := l.accounts[acct]
	if a == nil {
		return nil
	}
	if l.cfg.MaxEvents > 0 && batchSize > l.cfg.MaxEvents {
		return ErrBadVersion
	}
	if a.Version == toVer {
		return nil
	}
	for _, v := range specs {
		l.applyVersionLocked(a, v)
	}
	a.Version = toVer
	return nil
}

// Explain lists the reasons behind one entry of the codex.
func (l *CollectionCodexLedger) Explain(acct string, entryID string) []Reason {
	l.mu.RLock()
	defer l.mu.RUnlock()

	a := l.accounts[acct]
	if a == nil {
		return nil
	}
	st := a.Entries[entryID]
	if st == nil {
		return nil
	}

	reasons := make([]Reason, 0, 2+len(st.Variants))
	if st.Count > 0 {
		reasons = append(reasons, Reason{Kind: "discovered", Detail: fmt.Sprintf("count=%d", st.Count)})
	}
	if entry, exists := l.entries[entryID]; exists && entry.Hidden {
		if st.Revealed {
			reasons = append(reasons, Reason{Kind: "revealed", Detail: entryID})
		} else {
			reasons = append(reasons, Reason{Kind: "hidden", Detail: entryID})
		}
	}
	if st.Legacy {
		reasons = append(reasons, Reason{Kind: "legacy", Detail: entryID})
	}
	variants := make([]string, 0, len(st.Variants))
	for variant := range st.Variants {
		variants = append(variants, variant)
	}
	sort.Strings(variants)
	for _, variant := range variants {
		reasons = append(reasons, Reason{Kind: "variant", Detail: variant})
	}
	return reasons
}

// Stats reads the counters of the ledger.
func (l *CollectionCodexLedger) Stats() CodexStats {
	l.mu.RLock()
	defer l.mu.RUnlock()

	stats := CodexStats{
		Entries:  len(l.entries),
		Sets:     len(l.sets),
		Accounts: len(l.accounts),
	}
	for _, a := range l.accounts {
		for _, st := range a.Entries {
			if st.Count > 0 {
				stats.Discovered++
			}
			stats.Variants += len(st.Variants)
			if st.Legacy {
				stats.Legacy++
			}
		}
		stats.SetsCompleted += len(a.Completed)
		stats.SetsClaimed += len(a.Claimed)
		stats.Converted += a.Converted
	}
	return stats
}

const snapshotFormat = "collection-codex-ledger-v1"

type snapshotEntry struct {
	Count    int      `json:"count"`
	Revealed bool     `json:"revealed,omitempty"`
	Legacy   bool     `json:"legacy,omitempty"`
	Variants []string `json:"variants,omitempty"`
}

type snapshotAccount struct {
	Version   string                    `json:"version,omitempty"`
	Entries   map[string]snapshotEntry  `json:"entries"`
	Events    map[string]DiscoverResult `json:"events"`
	DailyDay  string                    `json:"daily_day,omitempty"`
	DailyUsed int                       `json:"daily_used,omitempty"`
	Converted int                       `json:"converted,omitempty"`
	Completed []string                  `json:"completed,omitempty"`
	Claimed   []string                  `json:"claimed,omitempty"`
}

type snapshotPayload struct {
	Format   string                     `json:"format"`
	Entries  []CatalogEntry             `json:"entries"`
	Sets     []EntrySet                 `json:"sets"`
	Versions []VersionSpec              `json:"versions"`
	Accounts map[string]snapshotAccount `json:"accounts"`
}

// Snapshot serialises the catalog, the version table and the account codexes.
func (l *CollectionCodexLedger) Snapshot() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	payload := snapshotPayload{
		Format:   snapshotFormat,
		Entries:  make([]CatalogEntry, 0, len(l.entries)),
		Sets:     make([]EntrySet, 0, len(l.sets)),
		Versions: make([]VersionSpec, 0, len(l.versions)),
		Accounts: make(map[string]snapshotAccount, len(l.accounts)),
	}
	for _, entry := range l.entries {
		payload.Entries = append(payload.Entries, entry)
	}
	sort.Slice(payload.Entries, func(i, j int) bool { return payload.Entries[i].ID < payload.Entries[j].ID })
	for _, set := range l.sets {
		payload.Sets = append(payload.Sets, cloneSet(set))
	}
	sort.Slice(payload.Sets, func(i, j int) bool { return payload.Sets[i].ID < payload.Sets[j].ID })
	for _, version := range l.versions {
		payload.Versions = append(payload.Versions, cloneVersion(version))
	}
	sort.Slice(payload.Versions, func(i, j int) bool {
		if payload.Versions[i].Order != payload.Versions[j].Order {
			return payload.Versions[i].Order < payload.Versions[j].Order
		}
		return payload.Versions[i].ID < payload.Versions[j].ID
	})

	for acct, a := range l.accounts {
		sa := snapshotAccount{
			Version:   a.Version,
			Entries:   make(map[string]snapshotEntry, len(a.Entries)),
			Events:    make(map[string]DiscoverResult, len(a.Events)),
			DailyDay:  a.DailyDay,
			DailyUsed: a.DailyUsed,
			Converted: a.Converted,
			Completed: make([]string, 0, len(a.Completed)),
			Claimed:   make([]string, 0, len(a.Claimed)),
		}
		for entryID, st := range a.Entries {
			variants := make([]string, 0, len(st.Variants))
			for variant := range st.Variants {
				variants = append(variants, variant)
			}
			sort.Strings(variants)
			sa.Entries[entryID] = snapshotEntry{
				Count:    st.Count,
				Revealed: st.Revealed,
				Legacy:   st.Legacy,
				Variants: variants,
			}
		}
		for eventID, result := range a.Events {
			sa.Events[eventID] = cloneResult(result)
		}
		for setID := range a.Completed {
			sa.Completed = append(sa.Completed, setID)
		}
		sort.Strings(sa.Completed)
		for setID := range a.Claimed {
			sa.Claimed = append(sa.Claimed, setID)
		}
		sort.Strings(sa.Claimed)
		payload.Accounts[acct] = sa
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *CollectionCodexLedger) Restore(data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil || payload.Format != snapshotFormat {
		return ErrBadSnapshot
	}

	entries := make(map[string]CatalogEntry, len(payload.Entries))
	for _, entry := range payload.Entries {
		if blank(entry.ID) || blank(entry.Name) {
			return ErrBadSnapshot
		}
		if _, duplicate := entries[entry.ID]; duplicate {
			return ErrBadSnapshot
		}
		entries[entry.ID] = entry
	}

	sets := make(map[string]EntrySet, len(payload.Sets))
	for _, set := range payload.Sets {
		if blank(set.ID) || len(set.EntryIDs) == 0 {
			return ErrBadSnapshot
		}
		if _, duplicate := sets[set.ID]; duplicate {
			return ErrBadSnapshot
		}
		seen := make(map[string]struct{}, len(set.EntryIDs))
		for _, entryID := range set.EntryIDs {
			if blank(entryID) {
				return ErrBadSnapshot
			}
			if _, duplicate := seen[entryID]; duplicate {
				return ErrBadSnapshot
			}
			seen[entryID] = struct{}{}
			if _, exists := entries[entryID]; !exists {
				return ErrBadSnapshot
			}
		}
		sets[set.ID] = cloneSet(set)
	}

	versions := make(map[string]VersionSpec, len(payload.Versions))
	for _, version := range payload.Versions {
		if blank(version.ID) {
			return ErrBadSnapshot
		}
		if _, duplicate := versions[version.ID]; duplicate {
			return ErrBadSnapshot
		}
		versions[version.ID] = cloneVersion(version)
	}

	accounts := make(map[string]*accountCodex, len(payload.Accounts))
	for acct, sa := range payload.Accounts {
		if blank(acct) {
			return ErrBadSnapshot
		}
		if l.cfg.MaxEvents > 0 && len(sa.Events) > l.cfg.MaxEvents {
			return ErrBadSnapshot
		}
		if l.cfg.MaxEntriesPerAccount > 0 && len(sa.Entries) > l.cfg.MaxEntriesPerAccount {
			return ErrBadSnapshot
		}
		if sa.DailyUsed < 0 || sa.Converted < 0 {
			return ErrBadSnapshot
		}
		if sa.Version != "" {
			if _, exists := versions[sa.Version]; !exists {
				return ErrBadSnapshot
			}
		}

		a := newAccountCodex()
		a.Version = sa.Version
		a.DailyDay = sa.DailyDay
		a.DailyUsed = sa.DailyUsed
		a.Converted = sa.Converted
		for entryID, se := range sa.Entries {
			if blank(entryID) || se.Count < 0 {
				return ErrBadSnapshot
			}
			st := newCodexEntryState()
			st.Count = se.Count
			st.Revealed = se.Revealed
			st.Legacy = se.Legacy
			for _, variant := range se.Variants {
				if blank(variant) {
					return ErrBadSnapshot
				}
				st.Variants[variant] = struct{}{}
			}
			a.Entries[entryID] = st
		}
		for eventID, result := range sa.Events {
			if blank(eventID) {
				return ErrBadSnapshot
			}
			a.Events[eventID] = cloneResult(result)
		}
		seenCompleted := make(map[string]struct{}, len(sa.Completed))
		for _, setID := range sa.Completed {
			if _, exists := sets[setID]; !exists {
				return ErrBadSnapshot
			}
			if _, duplicate := seenCompleted[setID]; duplicate {
				return ErrBadSnapshot
			}
			seenCompleted[setID] = struct{}{}
			a.Completed[setID] = struct{}{}
		}
		seenClaimed := make(map[string]struct{}, len(sa.Claimed))
		for _, setID := range sa.Claimed {
			if _, exists := sets[setID]; !exists {
				return ErrBadSnapshot
			}
			if _, duplicate := seenClaimed[setID]; duplicate {
				return ErrBadSnapshot
			}
			if _, complete := a.Completed[setID]; !complete {
				return ErrBadSnapshot
			}
			seenClaimed[setID] = struct{}{}
			a.Claimed[setID] = struct{}{}
		}
		accounts[acct] = a
	}

	l.entries = entries
	l.sets = sets
	l.versions = versions
	l.accounts = accounts
	return nil
}
