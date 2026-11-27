// Package task keeps the monster codex of every account: discoveries from
// several sources, regional completion with a daily gain cap and version
// driven backfill.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// SourceKind tells how an entry was learned.
type SourceKind string

// Source kinds accepted by Discover.
const (
	SourceKill    SourceKind = "kill"
	SourceObserve SourceKind = "observe"
	SourceShare   SourceKind = "share"
)

// Errors reported by the ledger.
var (
	ErrBlankAccount = errors.New("account id is required")
	ErrUnknownEntry = errors.New("codex entry is unknown at this version")
	ErrBadSource    = errors.New("unknown discovery source")
	ErrBadVersion   = errors.New("codex version must be positive")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// CodexConfig is the static codex table of one game build.
type CodexConfig struct {
	Version         int
	DailyRegionGain int
	EntryRegion     map[string]string
	EntryVersion    map[string]int
	RegionEntries   map[string][]string
}

// Source is the provenance of one discovery.
type Source struct {
	Kind        SourceKind
	FromAccount string
}

// KillRecord is one historical kill replayed by Backfill.
type KillRecord struct {
	EntryID string
	At      time.Time
	Source  SourceKind
}

// Discovery describes what one Discover call changed.
type Discovery struct {
	AccountID string
	EntryID   string
	RegionID  string
	First     bool
	Counted   bool
	Deferred  int
	Seq       int64
	At        time.Time
}

// EntryState is the per account record of one entry.
type EntryState struct {
	EntryID    string
	RegionID   string
	Sources    []string
	Introduced int
	Hidden     bool
	FirstAt    time.Time
}

// Progress reports regional completion for one account.
type Progress struct {
	RegionID   string
	Discovered int
	Total      int
	Percent    int
	Backlog    int
	Complete   bool
}

// BackfillResult reports how a historical replay was applied.
type BackfillResult struct {
	Added    []string
	Skipped  []string
	Rewarded int
}

type pendingReason uint8

const (
	pendingDaily pendingReason = iota
	pendingVersion
)

type pendingRecord struct {
	EntryID    string
	RegionID   string
	At         time.Time
	Order      int64
	Reason     pendingReason
	Source     SourceKind
	NeedReward bool
}

type entryRecord struct {
	State         EntryState
	Counted       bool
	CountedAt     time.Time
	Qualifying    bool
	RewardApplied bool
}

func (e *entryRecord) public() EntryState {
	if e == nil {
		return EntryState{}
	}
	out := e.State
	out.Sources = append([]string(nil), e.State.Sources...)
	return out
}

type accountState struct {
	Entries      map[string]*entryRecord
	Seq          int64
	Daily        map[string]map[string]int
	Backlogs     map[string][]*pendingRecord
	Pending      map[string]*pendingRecord
	PendingOrder int64
}

type ledgerState struct {
	Version  int
	Accounts map[string]*accountState
}

// CodexLedger owns the per account codex state.
type CodexLedger struct {
	mu    sync.RWMutex
	cfg   CodexConfig
	state ledgerState
}

// NewCodexLedger builds a ledger from the static codex table.
func NewCodexLedger(cfg CodexConfig) *CodexLedger {
	return &CodexLedger{
		cfg: cloneConfig(cfg),
		state: ledgerState{
			Version:  cfg.Version,
			Accounts: make(map[string]*accountState),
		},
	}
}

func cloneConfig(cfg CodexConfig) CodexConfig {
	out := cfg
	out.EntryRegion = make(map[string]string, len(cfg.EntryRegion))
	for id, region := range cfg.EntryRegion {
		out.EntryRegion[id] = region
	}
	out.EntryVersion = make(map[string]int, len(cfg.EntryVersion))
	for id, version := range cfg.EntryVersion {
		out.EntryVersion[id] = version
	}
	out.RegionEntries = make(map[string][]string, len(cfg.RegionEntries))
	for region, entries := range cfg.RegionEntries {
		out.RegionEntries[region] = append([]string(nil), entries...)
	}
	return out
}

func newAccountState() *accountState {
	return &accountState{
		Entries:  make(map[string]*entryRecord),
		Daily:    make(map[string]map[string]int),
		Backlogs: make(map[string][]*pendingRecord),
		Pending:  make(map[string]*pendingRecord),
	}
}

func validSource(kind SourceKind) bool {
	switch kind {
	case SourceKill, SourceObserve, SourceShare:
		return true
	default:
		return false
	}
}

func isSelfSource(kind SourceKind) bool {
	return kind == SourceKill || kind == SourceObserve
}

func addSource(state *EntryState, kind SourceKind) {
	value := string(kind)
	for _, source := range state.Sources {
		if source == value {
			return
		}
	}
	state.Sources = append(state.Sources, value)
	sort.Strings(state.Sources)
}

func dayKey(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func (l *CodexLedger) entryMeta(entryID string) (string, int, bool) {
	region, ok := l.cfg.EntryRegion[entryID]
	if !ok || region == "" {
		return "", 0, false
	}
	version, ok := l.cfg.EntryVersion[entryID]
	if !ok || version <= 0 {
		return "", 0, false
	}
	return region, version, true
}

func (l *CodexLedger) entryVisible(entryID string, version int) bool {
	_, introduced, ok := l.entryMeta(entryID)
	return ok && introduced <= version
}

func (l *CodexLedger) regionEntryIDs(region string, version int) []string {
	seen := make(map[string]struct{})
	add := func(entryID string) {
		entryRegion, introduced, ok := l.entryMeta(entryID)
		if !ok || entryRegion != region || introduced > version {
			return
		}
		seen[entryID] = struct{}{}
	}
	for _, entryID := range l.cfg.RegionEntries[region] {
		add(entryID)
	}
	for entryID, entryRegion := range l.cfg.EntryRegion {
		if entryRegion == region {
			add(entryID)
		}
	}
	out := make([]string, 0, len(seen))
	for entryID := range seen {
		out = append(out, entryID)
	}
	sort.Strings(out)
	return out
}

func (l *CodexLedger) regionSize(region string) int {
	seen := make(map[string]struct{})
	for _, entryID := range l.cfg.RegionEntries[region] {
		seen[entryID] = struct{}{}
	}
	for entryID, entryRegion := range l.cfg.EntryRegion {
		if entryRegion == region {
			seen[entryID] = struct{}{}
		}
	}
	return len(seen)
}

func (l *CodexLedger) regionKnown(region string) bool {
	if _, ok := l.cfg.RegionEntries[region]; ok {
		return true
	}
	for _, entryRegion := range l.cfg.EntryRegion {
		if entryRegion == region {
			return true
		}
	}
	return false
}

func (l *CodexLedger) dailyCap() int {
	if l.cfg.DailyRegionGain < 0 {
		return 0
	}
	return l.cfg.DailyRegionGain
}

func (l *CodexLedger) dailyCount(account *accountState, region, day string) int {
	if account == nil || account.Daily[region] == nil {
		return 0
	}
	return account.Daily[region][day]
}

func (l *CodexLedger) addDaily(account *accountState, region, day string) {
	if account.Daily[region] == nil {
		account.Daily[region] = make(map[string]int)
	}
	account.Daily[region][day]++
}

func (l *CodexLedger) enqueuePending(account *accountState, entryID, region string, at time.Time, source SourceKind, reason pendingReason, needReward bool) *pendingRecord {
	if pending := account.Pending[entryID]; pending != nil {
		at = at.UTC()
		if at.Before(pending.At) {
			pending.At = at
		}
		if needReward && !pending.NeedReward {
			pending.NeedReward = true
			pending.Source = source
		}
		return pending
	}
	if l.regionSize(region) == 0 {
		return nil
	}
	pending := &pendingRecord{
		EntryID:    entryID,
		RegionID:   region,
		At:         at.UTC(),
		Order:      account.PendingOrder,
		Reason:     reason,
		Source:     source,
		NeedReward: needReward,
	}
	account.PendingOrder++
	account.Pending[entryID] = pending
	account.Backlogs[region] = append(account.Backlogs[region], pending)
	return pending
}

func (l *CodexLedger) removePending(account *accountState, entryID string) {
	pending := account.Pending[entryID]
	if pending == nil {
		return
	}
	delete(account.Pending, entryID)
	queue := account.Backlogs[pending.RegionID]
	for i, item := range queue {
		if item == pending {
			queue = append(queue[:i], queue[i+1:]...)
			break
		}
	}
	if len(queue) == 0 {
		delete(account.Backlogs, pending.RegionID)
	} else {
		account.Backlogs[pending.RegionID] = queue
	}
}

func (l *CodexLedger) pendingCount(account *accountState, region string) int {
	if account == nil {
		return 0
	}
	count := 0
	for _, pending := range account.Pending {
		if pending.RegionID == region {
			count++
		}
	}
	return count
}

func (l *CodexLedger) orderedPending(account *accountState, region string) []*pendingRecord {
	queue := account.Backlogs[region]
	out := append([]*pendingRecord(nil), queue...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].Order < out[j].Order
		}
		return out[i].At.Before(out[j].At)
	})
	return out
}

func (l *CodexLedger) createEntry(account *accountState, entryID, region string, introduced int, kind SourceKind, at time.Time) *entryRecord {
	entry := &entryRecord{
		State: EntryState{
			EntryID:    entryID,
			RegionID:   region,
			Introduced: introduced,
			FirstAt:    at,
		},
		Qualifying: isSelfSource(kind),
	}
	addSource(&entry.State, kind)
	account.Entries[entryID] = entry
	return entry
}

func (l *CodexLedger) tryCount(account *accountState, entry *entryRecord, at time.Time) bool {
	if entry == nil {
		return false
	}
	if entry.Counted {
		l.removePending(account, entry.State.EntryID)
		return false
	}
	region := entry.State.RegionID
	day := dayKey(at)
	if l.dailyCount(account, region, day) < l.dailyCap() {
		entry.Counted = true
		entry.CountedAt = at
		l.addDaily(account, region, day)
		if pending := account.Pending[entry.State.EntryID]; pending != nil && pending.NeedReward {
			entry.RewardApplied = true
		}
		l.removePending(account, entry.State.EntryID)
		return true
	}
	l.enqueuePending(account, entry.State.EntryID, region, at, SourceKill, pendingDaily, false)
	return false
}

func (l *CodexLedger) visibleHidden(entryID string, version int) bool {
	return !l.entryVisible(entryID, version)
}

// Discover records one discovery of an entry by an account.
func (l *CodexLedger) Discover(accountID, entryID string, source Source, at time.Time) (Discovery, error) {
	if strings.TrimSpace(accountID) == "" {
		return Discovery{}, ErrBlankAccount
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	region, introduced, ok := l.entryMeta(entryID)
	if !ok || introduced > l.state.Version {
		return Discovery{}, ErrUnknownEntry
	}
	if !validSource(source.Kind) {
		return Discovery{}, ErrBadSource
	}

	account := l.state.Accounts[accountID]
	if account == nil {
		account = newAccountState()
		l.state.Accounts[accountID] = account
	}
	account.Seq++
	result := Discovery{
		AccountID: accountID,
		EntryID:   entryID,
		RegionID:  region,
		Seq:       account.Seq,
		At:        at,
	}

	entry := account.Entries[entryID]
	if entry == nil {
		entry = l.createEntry(account, entryID, region, introduced, source.Kind, at)
		result.First = true
	} else {
		addSource(&entry.State, source.Kind)
		if isSelfSource(source.Kind) {
			entry.Qualifying = true
		}
	}

	if !entry.Counted && entry.Qualifying && account.Pending[entryID] == nil && isSelfSource(source.Kind) {
		result.Counted = l.tryCount(account, entry, at)
	}
	result.Deferred = l.pendingCount(account, region)
	return result, nil
}

// Progress reports how much of a region the account has completed.
func (l *CodexLedger) Progress(accountID, regionID string) (Progress, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if !l.regionKnown(regionID) {
		return Progress{}, nil
	}
	entryIDs := l.regionEntryIDs(regionID, l.state.Version)
	progress := Progress{RegionID: regionID, Total: len(entryIDs)}
	account := l.state.Accounts[accountID]
	if account != nil {
		for _, entryID := range entryIDs {
			entry := account.Entries[entryID]
			if entry != nil && entry.Counted && !entry.State.Hidden {
				progress.Discovered++
			}
		}
		progress.Backlog = l.pendingCount(account, regionID)
	}
	if progress.Total > 0 {
		progress.Percent = progress.Discovered * 100 / progress.Total
		progress.Complete = progress.Discovered >= progress.Total
	}
	return progress, nil
}

// Backfill replays historical kills for one account.
func (l *CodexLedger) Backfill(accountID string, records []KillRecord) (BackfillResult, error) {
	if strings.TrimSpace(accountID) == "" {
		return BackfillResult{}, ErrBlankAccount
	}

	sorted := append([]KillRecord(nil), records...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].At.Before(sorted[j].At)
	})

	l.mu.Lock()
	defer l.mu.Unlock()

	account := l.state.Accounts[accountID]
	if account == nil {
		account = newAccountState()
		l.state.Accounts[accountID] = account
	}
	result := BackfillResult{Added: make([]string, 0), Skipped: make([]string, 0)}
	for _, record := range sorted {
		if _, _, ok := l.entryMeta(record.EntryID); !ok {
			result.Skipped = append(result.Skipped, record.EntryID)
			continue
		}
		if _, exists := account.Entries[record.EntryID]; exists {
			result.Skipped = append(result.Skipped, record.EntryID)
			continue
		}

		kind := record.Source
		if kind == "" {
			kind = SourceKill
		}
		if !validSource(kind) {
			kind = SourceKill
		}
		region, introduced, _ := l.entryMeta(record.EntryID)
		if introduced > l.state.Version {
			l.enqueuePending(account, record.EntryID, region, record.At, kind, pendingVersion, isSelfSource(kind))
			result.Skipped = append(result.Skipped, record.EntryID)
			continue
		}

		entry := l.createEntry(account, record.EntryID, region, introduced, kind, record.At)
		result.Added = append(result.Added, record.EntryID)
		if isSelfSource(kind) {
			entry.RewardApplied = true
			result.Rewarded++
		}
		if entry.Qualifying {
			l.tryCount(account, entry, record.At)
		}
	}
	return result, nil
}

// SetVersion switches the ledger to another codex version.
func (l *CodexLedger) SetVersion(v int) error {
	if v <= 0 {
		return ErrBadVersion
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.state.Version = v
	for _, account := range l.state.Accounts {
		for entryID, entry := range account.Entries {
			entry.State.Hidden = l.visibleHidden(entryID, v)
		}
		l.replayVersionPending(account, v)
	}
	return nil
}

func (l *CodexLedger) replayVersionPending(account *accountState, version int) {
	var pending []*pendingRecord
	for _, item := range account.Pending {
		if item.Reason == pendingVersion && l.entryVisible(item.EntryID, version) {
			pending = append(pending, item)
		}
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].At.Equal(pending[j].At) {
			return pending[i].Order < pending[j].Order
		}
		return pending[i].At.Before(pending[j].At)
	})

	for _, item := range pending {
		current := account.Pending[item.EntryID]
		if current != item || !l.entryVisible(item.EntryID, version) {
			continue
		}
		region, introduced, ok := l.entryMeta(item.EntryID)
		if !ok {
			continue
		}
		entry := account.Entries[item.EntryID]
		if entry == nil {
			entry = l.createEntry(account, item.EntryID, region, introduced, item.Source, item.At)
		} else {
			addSource(&entry.State, item.Source)
			if isSelfSource(item.Source) {
				entry.Qualifying = true
			}
		}
		if item.NeedReward {
			entry.RewardApplied = true
		}
		if entry.Counted {
			l.removePending(account, item.EntryID)
			continue
		}
		if entry.Qualifying {
			if !l.tryCount(account, entry, item.At) {
				item.Reason = pendingDaily
			}
		} else {
			item.Reason = pendingDaily
		}
	}
}

// Version returns the codex version currently in force.
func (l *CodexLedger) Version() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state.Version
}

// Entry reads the account record of one entry.
func (l *CodexLedger) Entry(accountID, entryID string) (EntryState, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	account := l.state.Accounts[accountID]
	if account == nil {
		return EntryState{}, false
	}
	entry, ok := account.Entries[entryID]
	if !ok {
		return EntryState{}, false
	}
	return entry.public(), true
}

// Pending lists the discoveries waiting for the next day, oldest first.
func (l *CodexLedger) Pending(accountID, regionID string) ([]string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	account := l.state.Accounts[accountID]
	if account == nil {
		return []string{}, nil
	}
	queue := l.orderedPending(account, regionID)
	out := make([]string, 0, len(queue))
	for _, item := range queue {
		out = append(out, item.EntryID)
	}
	return out, nil
}

// Rollover applies the backlog of one day under that day's cap.
func (l *CodexLedger) Rollover(accountID string, day time.Time) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	account := l.state.Accounts[accountID]
	if account == nil {
		return 0, nil
	}

	dayName := dayKey(day)
	applied := 0
	for region := range account.Backlogs {
		remaining := l.dailyCap() - l.dailyCount(account, region, dayName)
		if remaining <= 0 {
			continue
		}
		for _, item := range l.orderedPending(account, region) {
			if remaining <= 0 {
				break
			}
			if account.Pending[item.EntryID] != item {
				continue
			}
			if !l.entryVisible(item.EntryID, l.state.Version) {
				continue
			}
			_, introduced, ok := l.entryMeta(item.EntryID)
			if !ok {
				continue
			}
			entry := account.Entries[item.EntryID]
			if entry == nil {
				entry = l.createEntry(account, item.EntryID, region, introduced, item.Source, item.At)
			}
			if item.NeedReward {
				entry.RewardApplied = true
			}
			if entry.Counted {
				l.removePending(account, item.EntryID)
				continue
			}
			entry.Counted = true
			entry.CountedAt = day
			l.addDaily(account, region, dayName)
			l.removePending(account, item.EntryID)
			applied++
			remaining--
		}
	}
	return applied, nil
}

// Hidden lists the entries moved to the hidden zone by a version rollback.
func (l *CodexLedger) Hidden(accountID string) ([]string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	account := l.state.Accounts[accountID]
	if account == nil {
		return []string{}, nil
	}
	out := make([]string, 0)
	for entryID, entry := range account.Entries {
		if entry.State.Hidden {
			out = append(out, entryID)
		}
	}
	sort.Strings(out)
	return out, nil
}

const (
	snapshotMagic  = "gsb-codex-ledger"
	snapshotFormat = 1
)

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Format   int             `json:"format"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

type snapshotState struct {
	Version  int                        `json:"version"`
	Accounts map[string]snapshotAccount `json:"accounts"`
}

type snapshotAccount struct {
	Entries      map[string]snapshotEntry     `json:"entries"`
	Seq          int64                        `json:"seq"`
	Daily        map[string]map[string]int    `json:"daily,omitempty"`
	Backlogs     map[string][]snapshotPending `json:"backlogs,omitempty"`
	PendingOrder int64                        `json:"pending_order,omitempty"`
}

type snapshotEntry struct {
	EntryID       string    `json:"entry_id"`
	RegionID      string    `json:"region_id"`
	Sources       []string  `json:"sources,omitempty"`
	Introduced    int       `json:"introduced"`
	Hidden        bool      `json:"hidden,omitempty"`
	FirstAt       time.Time `json:"first_at"`
	Counted       bool      `json:"counted,omitempty"`
	CountedAt     time.Time `json:"counted_at,omitempty"`
	Qualifying    bool      `json:"qualifying,omitempty"`
	RewardApplied bool      `json:"reward_applied,omitempty"`
}

type snapshotPending struct {
	EntryID    string        `json:"entry_id"`
	RegionID   string        `json:"region_id"`
	At         time.Time     `json:"at"`
	Order      int64         `json:"order"`
	Reason     pendingReason `json:"reason"`
	Source     SourceKind    `json:"source"`
	NeedReward bool          `json:"need_reward,omitempty"`
}

func (l *CodexLedger) snapshotStateLocked() snapshotState {
	out := snapshotState{
		Version:  l.state.Version,
		Accounts: make(map[string]snapshotAccount, len(l.state.Accounts)),
	}
	for accountID, account := range l.state.Accounts {
		accountSnapshot := snapshotAccount{
			Entries:      make(map[string]snapshotEntry, len(account.Entries)),
			Seq:          account.Seq,
			Daily:        make(map[string]map[string]int, len(account.Daily)),
			Backlogs:     make(map[string][]snapshotPending, len(account.Backlogs)),
			PendingOrder: account.PendingOrder,
		}
		for region, days := range account.Daily {
			accountSnapshot.Daily[region] = make(map[string]int, len(days))
			for day, count := range days {
				accountSnapshot.Daily[region][day] = count
			}
		}
		for entryID, entry := range account.Entries {
			accountSnapshot.Entries[entryID] = snapshotEntry{
				EntryID:       entry.State.EntryID,
				RegionID:      entry.State.RegionID,
				Sources:       append([]string(nil), entry.State.Sources...),
				Introduced:    entry.State.Introduced,
				Hidden:        entry.State.Hidden,
				FirstAt:       entry.State.FirstAt,
				Counted:       entry.Counted,
				CountedAt:     entry.CountedAt,
				Qualifying:    entry.Qualifying,
				RewardApplied: entry.RewardApplied,
			}
		}
		for region, queue := range account.Backlogs {
			items := make([]snapshotPending, 0, len(queue))
			for _, item := range queue {
				items = append(items, snapshotPending{
					EntryID:    item.EntryID,
					RegionID:   item.RegionID,
					At:         item.At,
					Order:      item.Order,
					Reason:     item.Reason,
					Source:     item.Source,
					NeedReward: item.NeedReward,
				})
			}
			accountSnapshot.Backlogs[region] = items
		}
		out.Accounts[accountID] = accountSnapshot
	}
	return out
}

// Snapshot serialises every account record and the backlog.
func (l *CodexLedger) Snapshot() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	stateBytes, err := json.Marshal(l.snapshotStateLocked())
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(stateBytes)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Format:   snapshotFormat,
		Checksum: hex.EncodeToString(sum[:]),
		State:    stateBytes,
	}
	return json.Marshal(envelope)
}

// Restore replaces the ledger state with a snapshot, all or nothing.
func (l *CodexLedger) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || envelope.Format != snapshotFormat || len(envelope.State) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.State)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var snapshot snapshotState
	if err := json.Unmarshal(envelope.State, &snapshot); err != nil || snapshot.Version < 0 {
		return ErrBadSnapshot
	}
	restored, err := restoreState(snapshot)
	if err != nil {
		return ErrBadSnapshot
	}

	l.mu.Lock()
	l.state = restored
	l.mu.Unlock()
	return nil
}

func restoreState(snapshot snapshotState) (ledgerState, error) {
	state := ledgerState{
		Version:  snapshot.Version,
		Accounts: make(map[string]*accountState, len(snapshot.Accounts)),
	}
	for accountID, saved := range snapshot.Accounts {
		if strings.TrimSpace(accountID) == "" {
			return ledgerState{}, ErrBadSnapshot
		}
		account := newAccountState()
		account.Seq = saved.Seq
		account.PendingOrder = saved.PendingOrder
		for entryID, savedEntry := range saved.Entries {
			if entryID == "" || savedEntry.EntryID != entryID || savedEntry.RegionID == "" {
				return ledgerState{}, ErrBadSnapshot
			}
			entry := &entryRecord{
				State: EntryState{
					EntryID:    savedEntry.EntryID,
					RegionID:   savedEntry.RegionID,
					Sources:    append([]string(nil), savedEntry.Sources...),
					Introduced: savedEntry.Introduced,
					Hidden:     savedEntry.Hidden,
					FirstAt:    savedEntry.FirstAt,
				},
				Counted:       savedEntry.Counted,
				CountedAt:     savedEntry.CountedAt,
				Qualifying:    savedEntry.Qualifying,
				RewardApplied: savedEntry.RewardApplied,
			}
			for _, source := range entry.State.Sources {
				if !validSource(SourceKind(source)) {
					return ledgerState{}, ErrBadSnapshot
				}
			}
			sort.Strings(entry.State.Sources)
			account.Entries[entryID] = entry
		}
		for region, days := range saved.Daily {
			if region == "" || account.Daily[region] != nil {
				return ledgerState{}, ErrBadSnapshot
			}
			account.Daily[region] = make(map[string]int, len(days))
			for day, count := range days {
				if day == "" || count < 0 {
					return ledgerState{}, ErrBadSnapshot
				}
				account.Daily[region][day] = count
			}
		}
		for region, items := range saved.Backlogs {
			if region == "" {
				return ledgerState{}, ErrBadSnapshot
			}
			for _, savedPending := range items {
				if savedPending.EntryID == "" || savedPending.RegionID != region || account.Pending[savedPending.EntryID] != nil {
					return ledgerState{}, ErrBadSnapshot
				}
				if savedPending.Reason != pendingDaily && savedPending.Reason != pendingVersion {
					return ledgerState{}, ErrBadSnapshot
				}
				if !validSource(savedPending.Source) {
					return ledgerState{}, ErrBadSnapshot
				}
				pending := &pendingRecord{
					EntryID:    savedPending.EntryID,
					RegionID:   savedPending.RegionID,
					At:         savedPending.At,
					Order:      savedPending.Order,
					Reason:     savedPending.Reason,
					Source:     savedPending.Source,
					NeedReward: savedPending.NeedReward,
				}
				account.Pending[pending.EntryID] = pending
				account.Backlogs[region] = append(account.Backlogs[region], pending)
				if pending.Order >= account.PendingOrder {
					account.PendingOrder = pending.Order + 1
				}
			}
		}
		state.Accounts[accountID] = account
	}
	return state, nil
}
