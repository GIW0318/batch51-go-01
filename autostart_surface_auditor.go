// Package task inventories the autostart surface of a host: systemd units and
// timers, cron and at jobs, shell startup files, desktop autostart entries,
// udev rules and module load config. Entries merge by name across sources, keep
// every declaring source, and are diffed against a stored baseline.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Supported autostart sources.
const (
	SourceSystemdSystem = "systemd_system"
	SourceSystemdUser   = "systemd_user"
	SourceCron          = "cron"
	SourceAt            = "at"
	SourceShellProfile  = "shell_profile"
	SourceDesktop       = "desktop_autostart"
	SourceUdev          = "udev"
	SourceModules       = "modules_load"
)

// Delta kinds reported by Diff.
const (
	DeltaAdded     = "added"
	DeltaModified  = "modified"
	DeltaRemoved   = "removed"
	DeltaTimestamp = "timestamp_only"
)

// Scopes accepted by Scan, Baseline and Diff.
const (
	ScopeAll    = "all"
	ScopeSystem = "system"
	ScopeUser   = "user"
)

// Errors returned by the auditor.
var (
	ErrBadSource        = errors.New("autostart source is unknown")
	ErrBadItem          = errors.New("autostart item path is required")
	ErrUnknownScope     = errors.New("audit scope is unknown")
	ErrScanLimit        = errors.New("autostart entry limit reached")
	ErrNoBaseline       = errors.New("no baseline recorded for this scope")
	ErrUnknownEntry     = errors.New("autostart entry is unknown")
	ErrProtectedEntry   = errors.New("autostart entry is protected")
	ErrBadUntil         = errors.New("allowance expiry is required")
	ErrAllowanceLimit   = errors.New("allowance limit reached")
	ErrUnknownAllowance = errors.New("allowance is not recorded")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

const snapshotMagic = "gsb/batch51-go-safety-c-05/autostart-surface"
const snapshotVersion = 1

// AuditConfig bounds the inventory and the retained baselines.
type AuditConfig struct {
	MaxEntries    int
	MaxBaselines  int
	MaxAllowances int
	Protected     []string
}

// Item is one declaration collected from a source.
type Item struct {
	Source  string
	Path    string
	Name    string
	Content string
	MTime   time.Time
}

// Entry is one merged autostart entry.
type Entry struct {
	ID        string
	Name      string
	Path      string
	Digest    string
	Sources   []string
	Effective string
	MTime     time.Time
	Protected bool
}

// Delta is one difference between the current scan and the baseline.
type Delta struct {
	Kind  string
	ID    string
	Name  string
	Path  string
	Entry Entry
}

// Allowance is one temporary acceptance of a known entry.
type Allowance struct {
	EntryID string
	Until   time.Time
}

// AuditStats counts the inventory and the performed actions.
type AuditStats struct {
	Entries    int
	Baselines  int
	Allowances int
	Removals   int64
}

type declaration struct {
	Path    string
	Name    string
	Content string
	Digest  string
	MTime   time.Time
	Sources []string
	Seq     uint64
}

type baselineRecord struct {
	Scope   string
	Entries []Entry
}

type entryGroup struct {
	Name         string
	Declarations []*declaration
	Entry        Entry
	WinnerSeq    uint64
}

// AutostartSurfaceAuditor owns the collected items, baselines and allowances.
type AutostartSurfaceAuditor struct {
	mu sync.Mutex

	cfg       AuditConfig
	protected []string

	declarations map[string]*declaration
	baselines    []baselineRecord

	allowances     map[string]Allowance
	allowanceOrder []string

	removals int64
	nextSeq  uint64
}

// NewAutostartSurfaceAuditor builds an auditor with the given caps.
func NewAutostartSurfaceAuditor(cfg AuditConfig) *AutostartSurfaceAuditor {
	cfg.Protected = append([]string(nil), cfg.Protected...)
	protected := make([]string, 0, len(cfg.Protected))
	for _, protectedPath := range cfg.Protected {
		if normalized := normalizePath(protectedPath); normalized != "" {
			protected = append(protected, normalized)
		}
	}
	return &AutostartSurfaceAuditor{
		cfg:          cfg,
		protected:    protected,
		declarations: make(map[string]*declaration),
		allowances:   make(map[string]Allowance),
		nextSeq:      1,
	}
}

func sourceNames() []string {
	return []string{
		SourceSystemdSystem,
		SourceSystemdUser,
		SourceCron,
		SourceAt,
		SourceShellProfile,
		SourceDesktop,
		SourceUdev,
		SourceModules,
	}
}

func validSource(source string) bool {
	switch source {
	case SourceSystemdSystem, SourceSystemdUser, SourceCron, SourceAt,
		SourceShellProfile, SourceDesktop, SourceUdev, SourceModules:
		return true
	default:
		return false
	}
}

func sourceRank(source string) int {
	switch source {
	case SourceSystemdSystem:
		return 800
	case SourceSystemdUser:
		return 700
	case SourceCron:
		return 600
	case SourceAt:
		return 500
	case SourceShellProfile:
		return 400
	case SourceDesktop:
		return 300
	case SourceUdev:
		return 200
	case SourceModules:
		return 100
	default:
		return 0
	}
}

func sourceIndex(source string) int {
	for i, candidate := range sourceNames() {
		if candidate == source {
			return i
		}
	}
	return len(sourceNames())
}

func normalizePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return path.Clean(value)
}

func normalizeName(value, itemPath string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return path.Base(itemPath)
}

func digestContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func entryID(itemPath, digest string) string {
	sum := sha256.Sum256([]byte(itemPath + "\x00" + digest))
	return hex.EncodeToString(sum[:])
}

func normalizeSources(sources []string) []string {
	seen := make(map[string]struct{}, len(sources))
	unique := make([]string, 0, len(sources))
	for _, source := range sources {
		if !validSource(source) {
			continue
		}
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		unique = append(unique, source)
	}
	sort.Slice(unique, func(i, j int) bool {
		return sourceIndex(unique[i]) < sourceIndex(unique[j])
	})
	return unique
}

func sourceScope(source, itemPath string) string {
	switch source {
	case SourceSystemdUser:
		return ScopeUser
	case SourceDesktop:
		normalized := normalizePath(itemPath)
		if strings.HasPrefix(normalized, "/etc/xdg/autostart/") ||
			strings.HasPrefix(normalized, "/usr/share/autostart/") ||
			strings.HasPrefix(normalized, "/usr/local/share/autostart/") {
			return ScopeSystem
		}
		return ScopeUser
	case SourceShellProfile:
		normalized := normalizePath(itemPath)
		if strings.HasPrefix(normalized, "/home/") || strings.HasPrefix(normalized, "~/") ||
			strings.HasPrefix(normalized, "/Users/") {
			return ScopeUser
		}
		return ScopeSystem
	default:
		return ScopeSystem
	}
}

func declarationSourceInScope(source, itemPath, scope string) bool {
	return scope == ScopeAll || sourceScope(source, itemPath) == scope
}

func normalizedSourcesForScope(sources []string, itemPath, scope string) []string {
	filtered := make([]string, 0, len(sources))
	for _, source := range sources {
		if declarationSourceInScope(source, itemPath, scope) {
			filtered = append(filtered, source)
		}
	}
	return normalizeSources(filtered)
}

func sourceForScope(sources []string, itemPath, scope string) string {
	best := ""
	for _, source := range sources {
		if !declarationSourceInScope(source, itemPath, scope) {
			continue
		}
		if best == "" || sourceRank(source) > sourceRank(best) {
			best = source
		}
	}
	return best
}

func pathRank(itemPath string) int {
	switch {
	case itemPath == "/etc/systemd/system" || strings.HasPrefix(itemPath, "/etc/systemd/system/"):
		return 500
	case itemPath == "/run/systemd/system" || strings.HasPrefix(itemPath, "/run/systemd/system/"):
		return 400
	case itemPath == "/usr/local/lib/systemd/system" || strings.HasPrefix(itemPath, "/usr/local/lib/systemd/system/"):
		return 300
	case itemPath == "/usr/lib/systemd/system" || strings.HasPrefix(itemPath, "/usr/lib/systemd/system/"),
		itemPath == "/lib/systemd/system" || strings.HasPrefix(itemPath, "/lib/systemd/system/"):
		return 200
	case strings.Contains(itemPath, "/.config/systemd/user"):
		return 500
	case itemPath == "/etc/systemd/user" || strings.HasPrefix(itemPath, "/etc/systemd/user/"):
		return 400
	case strings.Contains(itemPath, "/run/user/") && strings.Contains(itemPath, "/systemd/user/"):
		return 300
	case itemPath == "/usr/local/lib/systemd/user" || strings.HasPrefix(itemPath, "/usr/local/lib/systemd/user/"):
		return 200
	case itemPath == "/usr/lib/systemd/user" || strings.HasPrefix(itemPath, "/usr/lib/systemd/user/"),
		itemPath == "/lib/systemd/user" || strings.HasPrefix(itemPath, "/lib/systemd/user/"):
		return 100
	default:
		return 0
	}
}

func (a *AutostartSurfaceAuditor) maxEntries() int {
	if a.cfg.MaxEntries <= 0 {
		return 0
	}
	return a.cfg.MaxEntries
}

func (a *AutostartSurfaceAuditor) maxBaselines() int {
	if a.cfg.MaxBaselines <= 0 {
		return 0
	}
	return a.cfg.MaxBaselines
}

func (a *AutostartSurfaceAuditor) maxAllowances() int {
	if a.cfg.MaxAllowances <= 0 {
		return 0
	}
	return a.cfg.MaxAllowances
}

func validScope(scope string) bool {
	return scope == ScopeAll || scope == ScopeSystem || scope == ScopeUser
}

// Register feeds one batch of declarations collected from the sources.
func (a *AutostartSurfaceAuditor) Register(items []Item) error {
	type batchItem struct {
		Path    string
		Name    string
		Content string
		MTime   time.Time
		Sources map[string]struct{}
	}

	batch := make(map[string]*batchItem, len(items))
	order := make([]string, 0, len(items))
	for _, raw := range items {
		if !validSource(raw.Source) {
			return ErrBadSource
		}
		itemPath := normalizePath(raw.Path)
		if itemPath == "" {
			return ErrBadItem
		}
		item, ok := batch[itemPath]
		if !ok {
			item = &batchItem{
				Path:    itemPath,
				Sources: make(map[string]struct{}),
			}
			batch[itemPath] = item
			order = append(order, itemPath)
		}
		item.Name = normalizeName(raw.Name, itemPath)
		item.Content = raw.Content
		item.MTime = raw.MTime
		item.Sources[raw.Source] = struct{}{}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, itemPath := range order {
		item := batch[itemPath]
		sources := make([]string, 0, len(item.Sources))
		for source := range item.Sources {
			sources = append(sources, source)
		}
		sources = normalizeSources(sources)
		digest := digestContent(item.Content)
		seq := a.nextSeq
		a.nextSeq++

		existing, ok := a.declarations[itemPath]
		if ok && existing.Digest == digest {
			sources = normalizeSources(append(append([]string(nil), existing.Sources...), sources...))
		}
		a.declarations[itemPath] = &declaration{
			Path:    itemPath,
			Name:    item.Name,
			Content: item.Content,
			Digest:  digest,
			MTime:   item.MTime,
			Sources: sources,
			Seq:     seq,
		}
	}
	return nil
}

func (a *AutostartSurfaceAuditor) isProtectedPath(itemPath string) bool {
	for _, protectedPath := range a.protected {
		if itemPath == protectedPath || strings.HasPrefix(itemPath, protectedPath+"/") {
			return true
		}
	}
	return false
}

func (a *AutostartSurfaceAuditor) buildGroupsLocked(scope string) []entryGroup {
	groups := make(map[string]*entryGroup)
	for _, item := range a.declarations {
		sources := normalizedSourcesForScope(item.Sources, item.Path, scope)
		if len(sources) == 0 {
			continue
		}
		name := normalizeName(item.Name, item.Path)
		group, ok := groups[name]
		if !ok {
			group = &entryGroup{Name: name}
			groups[name] = group
		}
		group.Declarations = append(group.Declarations, item)
		source := sourceForScope(sources, item.Path, scope)
		candidate := Entry{
			ID:        entryID(item.Path, item.Digest),
			Name:      name,
			Path:      item.Path,
			Digest:    item.Digest,
			Effective: source,
			MTime:     item.MTime,
		}
		if group.Entry.ID == "" || betterCandidate(candidate, item.Seq, group) {
			group.Entry = candidate
			group.WinnerSeq = item.Seq
		}
	}

	result := make([]entryGroup, 0, len(groups))
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		group := groups[name]
		allSources := make([]string, 0)
		for _, item := range group.Declarations {
			allSources = append(allSources, normalizedSourcesForScope(item.Sources, item.Path, scope)...)
		}
		group.Entry.Sources = normalizeSources(allSources)
		for _, item := range group.Declarations {
			if a.isProtectedPath(item.Path) {
				group.Entry.Protected = true
				break
			}
		}
		result = append(result, *group)
	}
	return result
}

func betterCandidate(candidate Entry, candidateSeq uint64, current *entryGroup) bool {
	candidateRank := sourceRank(candidate.Effective)
	currentRank := sourceRank(current.Entry.Effective)
	if candidateRank != currentRank {
		return candidateRank > currentRank
	}
	candidatePathRank := pathRank(candidate.Path)
	currentPathRank := pathRank(current.Entry.Path)
	if candidatePathRank != currentPathRank {
		return candidatePathRank > currentPathRank
	}
	if candidateSeq != current.WinnerSeq {
		return candidateSeq > current.WinnerSeq
	}
	return candidate.Path < current.Entry.Path
}

func (a *AutostartSurfaceAuditor) entriesForScopeLocked(scope string) []Entry {
	groups := a.buildGroupsLocked(scope)
	entries := make([]Entry, 0, len(groups))
	for _, group := range groups {
		entries = append(entries, group.Entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		if entries[i].Path != entries[j].Path {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].ID < entries[j].ID
	})
	return entries
}

func (a *AutostartSurfaceAuditor) scanLocked(scope string) ([]Entry, error) {
	entries := a.entriesForScopeLocked(scope)
	limit := a.maxEntries()
	if limit > 0 && len(entries) > limit {
		return nil, ErrScanLimit
	}
	return entries, nil
}

// Scan inventories one scope and returns the merged entries by name.
func (a *AutostartSurfaceAuditor) Scan(scope string) ([]Entry, error) {
	if !validScope(scope) {
		return nil, ErrUnknownScope
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.scanLocked(scope)
}

// Baseline stores the current inventory of one scope for later diffing.
func (a *AutostartSurfaceAuditor) Baseline(scope string) error {
	if !validScope(scope) {
		return ErrUnknownScope
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	entries, err := a.scanLocked(scope)
	if err != nil {
		return err
	}
	a.baselines = append(a.baselines, baselineRecord{
		Scope:   scope,
		Entries: cloneEntries(entries),
	})
	a.evictBaselinesLocked()
	return nil
}

func (a *AutostartSurfaceAuditor) evictBaselinesLocked() {
	limit := a.maxBaselines()
	if limit <= 0 || len(a.baselines) <= limit {
		return
	}
	keep := len(a.baselines) - limit
	remaining := make([]baselineRecord, len(a.baselines)-keep)
	copy(remaining, a.baselines[keep:])
	a.baselines = remaining
}

func (a *AutostartSurfaceAuditor) latestBaselineLocked(scope string) (baselineRecord, bool) {
	for i := len(a.baselines) - 1; i >= 0; i-- {
		if a.baselines[i].Scope == scope {
			return a.baselines[i], true
		}
	}
	return baselineRecord{}, false
}

// Diff compares the current inventory with the newest baseline of one scope.
func (a *AutostartSurfaceAuditor) Diff(scope string) ([]Delta, error) {
	if !validScope(scope) {
		return nil, ErrUnknownScope
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	baseline, ok := a.latestBaselineLocked(scope)
	if !ok {
		return nil, ErrNoBaseline
	}
	current, err := a.scanLocked(scope)
	if err != nil {
		return nil, err
	}
	return diffEntries(baseline.Entries, current), nil
}

func diffEntries(baseline, current []Entry) []Delta {
	baselineByID := make(map[string]Entry, len(baseline))
	baselineByPath := make(map[string]Entry, len(baseline))
	for _, entry := range baseline {
		baselineByID[entry.ID] = entry
		if _, exists := baselineByPath[entry.Path]; !exists {
			baselineByPath[entry.Path] = entry
		}
	}
	usedBaseline := make(map[string]bool, len(baseline))
	deltas := make([]Delta, 0)

	for _, entry := range current {
		if old, ok := baselineByID[entry.ID]; ok {
			usedBaseline[old.ID] = true
			if sameEntryExceptTime(old, entry) {
				if !old.MTime.Equal(entry.MTime) {
					deltas = append(deltas, Delta{
						Kind:  DeltaTimestamp,
						ID:    entry.ID,
						Name:  entry.Name,
						Path:  entry.Path,
						Entry: entry,
					})
				}
				continue
			}
			deltas = append(deltas, Delta{
				Kind:  DeltaModified,
				ID:    entry.ID,
				Name:  entry.Name,
				Path:  entry.Path,
				Entry: entry,
			})
			continue
		}
		if old, ok := baselineByPath[entry.Path]; ok && !usedBaseline[old.ID] {
			usedBaseline[old.ID] = true
			deltas = append(deltas, Delta{
				Kind:  DeltaModified,
				ID:    entry.ID,
				Name:  entry.Name,
				Path:  entry.Path,
				Entry: entry,
			})
			continue
		}
		deltas = append(deltas, Delta{
			Kind:  DeltaAdded,
			ID:    entry.ID,
			Name:  entry.Name,
			Path:  entry.Path,
			Entry: entry,
		})
	}

	for _, old := range baseline {
		if usedBaseline[old.ID] {
			continue
		}
		deltas = append(deltas, Delta{
			Kind:  DeltaRemoved,
			ID:    old.ID,
			Name:  old.Name,
			Path:  old.Path,
			Entry: old,
		})
	}
	sort.Slice(deltas, func(i, j int) bool {
		if deltas[i].Path != deltas[j].Path {
			return deltas[i].Path < deltas[j].Path
		}
		if deltas[i].Kind != deltas[j].Kind {
			return deltas[i].Kind < deltas[j].Kind
		}
		return deltas[i].ID < deltas[j].ID
	})
	return deltas
}

func sameEntryExceptTime(left, right Entry) bool {
	return left.ID == right.ID &&
		left.Name == right.Name &&
		left.Path == right.Path &&
		left.Digest == right.Digest &&
		left.Effective == right.Effective &&
		sameStrings(left.Sources, right.Sources)
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func cloneEntries(entries []Entry) []Entry {
	cloned := make([]Entry, len(entries))
	for i, entry := range entries {
		cloned[i] = cloneEntry(entry)
	}
	return cloned
}

func cloneEntry(entry Entry) Entry {
	entry.Sources = append([]string(nil), entry.Sources...)
	return entry
}

func (a *AutostartSurfaceAuditor) findGroupByIDLocked(id string) (entryGroup, bool) {
	for _, scope := range []string{ScopeAll, ScopeSystem, ScopeUser} {
		groups := a.buildGroupsLocked(scope)
		for _, group := range groups {
			if group.Entry.ID == id {
				return a.groupForNameLocked(group.Name, scope)
			}
		}
	}
	return entryGroup{}, false
}

func (a *AutostartSurfaceAuditor) groupForNameLocked(name, scope string) (entryGroup, bool) {
	for _, group := range a.buildGroupsLocked(scope) {
		if group.Name == name {
			return group, true
		}
	}
	return entryGroup{}, false
}

func (a *AutostartSurfaceAuditor) declarationsForGroupLocked(name string) []*declaration {
	declarations := make([]*declaration, 0)
	for _, item := range a.declarations {
		if normalizeName(item.Name, item.Path) == name {
			declarations = append(declarations, item)
		}
	}
	sort.Slice(declarations, func(i, j int) bool {
		return declarations[i].Path < declarations[j].Path
	})
	return declarations
}

func (a *AutostartSurfaceAuditor) groupProtectedLocked(name string) bool {
	for _, item := range a.declarationsForGroupLocked(name) {
		if a.isProtectedPath(item.Path) {
			return true
		}
	}
	return false
}

// Allow accepts one known entry until the given time.
func (a *AutostartSurfaceAuditor) Allow(entryID string, until time.Time) error {
	if until.IsZero() {
		return ErrBadUntil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	group, ok := a.findGroupByIDLocked(entryID)
	if !ok {
		return ErrUnknownEntry
	}
	if a.groupProtectedLocked(group.Name) {
		return ErrProtectedEntry
	}
	_, exists := a.allowances[entryID]
	if !exists {
		limit := a.maxAllowances()
		if limit > 0 && len(a.allowances) >= limit {
			return ErrAllowanceLimit
		}
		a.allowanceOrder = append(a.allowanceOrder, entryID)
	}
	a.allowances[entryID] = Allowance{EntryID: entryID, Until: until}
	return nil
}

// RevokeAllow drops one recorded allowance.
func (a *AutostartSurfaceAuditor) RevokeAllow(entryID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.allowances[entryID]; !ok {
		return ErrUnknownAllowance
	}
	delete(a.allowances, entryID)
	for i, recorded := range a.allowanceOrder {
		if recorded == entryID {
			a.allowanceOrder = append(a.allowanceOrder[:i], a.allowanceOrder[i+1:]...)
			break
		}
	}
	return nil
}

// AllowList lists the recorded allowances, oldest first.
func (a *AutostartSurfaceAuditor) AllowList() []Allowance {
	a.mu.Lock()
	defer a.mu.Unlock()

	list := make([]Allowance, 0, len(a.allowances))
	for _, entryID := range a.allowanceOrder {
		if allowance, ok := a.allowances[entryID]; ok {
			list = append(list, allowance)
		}
	}
	return list
}

// Remove takes one entry out of the inventory through its source.
func (a *AutostartSurfaceAuditor) Remove(entryID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	group, ok := a.findGroupByIDLocked(entryID)
	if !ok {
		return ErrUnknownEntry
	}
	if a.groupProtectedLocked(group.Name) {
		return ErrProtectedEntry
	}
	declarations := a.declarationsForGroupLocked(group.Name)
	if err := a.removeDeclarationsLocked(declarations); err != nil {
		return err
	}
	a.removals++
	return nil
}

func (a *AutostartSurfaceAuditor) removeDeclarationsLocked(declarations []*declaration) error {
	removed := make(map[string]*declaration, len(declarations))
	rollback := func() {
		for itemPath, original := range removed {
			a.declarations[itemPath] = original
		}
	}
	for _, item := range declarations {
		current, ok := a.declarations[item.Path]
		if !ok || current != item {
			rollback()
			return ErrUnknownEntry
		}
		removed[item.Path] = current
		delete(a.declarations, item.Path)
	}
	return nil
}

// Entries lists the current merged entries of one scope.
func (a *AutostartSurfaceAuditor) Entries(scope string) []Entry {
	if !validScope(scope) {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.entriesForScopeLocked(scope)
}

// Entry reads one merged entry by id.
func (a *AutostartSurfaceAuditor) Entry(id string) (Entry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if group, ok := a.findGroupByIDLocked(id); ok {
		return cloneEntry(group.Entry), true
	}
	return Entry{}, false
}

// SourceNames lists the supported autostart sources.
func (a *AutostartSurfaceAuditor) SourceNames() []string {
	return sourceNames()
}

// Stats reads the auditor counters.
func (a *AutostartSurfaceAuditor) Stats() AuditStats {
	a.mu.Lock()
	defer a.mu.Unlock()

	return AuditStats{
		Entries:    len(a.entriesForScopeLocked(ScopeAll)),
		Baselines:  len(a.baselines),
		Allowances: len(a.allowances),
		Removals:   a.removals,
	}
}

type snapshotDeclaration struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Content string    `json:"content"`
	Digest  string    `json:"digest"`
	MTime   time.Time `json:"mtime"`
	Sources []string  `json:"sources"`
	Seq     uint64    `json:"seq"`
}

type snapshotBaseline struct {
	Scope   string  `json:"scope"`
	Entries []Entry `json:"entries"`
}

type snapshotPayload struct {
	Declarations []snapshotDeclaration `json:"declarations"`
	Baselines    []snapshotBaseline    `json:"baselines"`
	Allowances   []Allowance           `json:"allowances"`
	Removals     int64                 `json:"removals"`
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

// Snapshot serialises items, baselines, removals and allowances.
func (a *AutostartSurfaceAuditor) Snapshot() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	declarations := make([]snapshotDeclaration, 0, len(a.declarations))
	for _, item := range a.declarations {
		declarations = append(declarations, snapshotDeclaration{
			Path:    item.Path,
			Name:    item.Name,
			Content: item.Content,
			Digest:  item.Digest,
			MTime:   item.MTime,
			Sources: append([]string(nil), item.Sources...),
			Seq:     item.Seq,
		})
	}
	sort.Slice(declarations, func(i, j int) bool {
		if declarations[i].Seq != declarations[j].Seq {
			return declarations[i].Seq < declarations[j].Seq
		}
		return declarations[i].Path < declarations[j].Path
	})

	baselines := make([]snapshotBaseline, 0, len(a.baselines))
	for _, baseline := range a.baselines {
		baselines = append(baselines, snapshotBaseline{
			Scope:   baseline.Scope,
			Entries: cloneEntries(baseline.Entries),
		})
	}

	allowances := make([]Allowance, 0, len(a.allowances))
	for _, entryID := range a.allowanceOrder {
		if allowance, ok := a.allowances[entryID]; ok {
			allowances = append(allowances, allowance)
		}
	}

	payload := snapshotPayload{
		Declarations: declarations,
		Baselines:    baselines,
		Allowances:   allowances,
		Removals:     a.removals,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payloadBytes)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  payloadBytes,
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *AutostartSurfaceAuditor) Restore(data []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}
	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return ErrBadSnapshot
	}

	declarations := make(map[string]*declaration, len(payload.Declarations))
	maxSeq := uint64(0)
	for _, snapshotItem := range payload.Declarations {
		itemPath := normalizePath(snapshotItem.Path)
		if itemPath == "" || itemPath != snapshotItem.Path {
			return ErrBadSnapshot
		}
		if _, exists := declarations[itemPath]; exists {
			return ErrBadSnapshot
		}
		sources := normalizeSources(snapshotItem.Sources)
		if len(sources) == 0 {
			return ErrBadSnapshot
		}
		digest := digestContent(snapshotItem.Content)
		if snapshotItem.Digest != "" && snapshotItem.Digest != digest {
			return ErrBadSnapshot
		}
		name := normalizeName(snapshotItem.Name, itemPath)
		if name == "" {
			return ErrBadSnapshot
		}
		declarations[itemPath] = &declaration{
			Path:    itemPath,
			Name:    name,
			Content: snapshotItem.Content,
			Digest:  digest,
			MTime:   snapshotItem.MTime,
			Sources: sources,
			Seq:     snapshotItem.Seq,
		}
		if snapshotItem.Seq > maxSeq {
			maxSeq = snapshotItem.Seq
		}
	}

	if limit := a.maxBaselines(); limit > 0 && len(payload.Baselines) > limit {
		return ErrBadSnapshot
	}
	baselines := make([]baselineRecord, 0, len(payload.Baselines))
	for _, snapshotBaseline := range payload.Baselines {
		if !validScope(snapshotBaseline.Scope) {
			return ErrBadSnapshot
		}
		if limit := a.maxEntries(); limit > 0 && len(snapshotBaseline.Entries) > limit {
			return ErrBadSnapshot
		}
		for _, entry := range snapshotBaseline.Entries {
			if entry.ID == "" || entry.Path == "" || entry.Digest == "" {
				return ErrBadSnapshot
			}
			if len(entry.Sources) > 0 && len(normalizeSources(entry.Sources)) != len(entry.Sources) {
				return ErrBadSnapshot
			}
		}
		baselines = append(baselines, baselineRecord{
			Scope:   snapshotBaseline.Scope,
			Entries: cloneEntries(snapshotBaseline.Entries),
		})
	}

	if limit := a.maxAllowances(); limit > 0 && len(payload.Allowances) > limit {
		return ErrBadSnapshot
	}
	allowances := make(map[string]Allowance, len(payload.Allowances))
	allowanceOrder := make([]string, 0, len(payload.Allowances))
	for _, allowance := range payload.Allowances {
		if allowance.EntryID == "" || allowance.Until.IsZero() {
			return ErrBadSnapshot
		}
		if _, exists := allowances[allowance.EntryID]; exists {
			return ErrBadSnapshot
		}
		allowances[allowance.EntryID] = allowance
		allowanceOrder = append(allowanceOrder, allowance.EntryID)
	}
	if payload.Removals < 0 {
		return ErrBadSnapshot
	}

	a.declarations = declarations
	a.baselines = baselines
	a.allowances = allowances
	a.allowanceOrder = allowanceOrder
	a.removals = payload.Removals
	a.nextSeq = maxSeq + 1
	return nil
}
