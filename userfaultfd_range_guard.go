// Package task registers the userfault handling ranges of sandboxed
// processes, routes page faults to the single owning handler and fails closed
// once that handler is gone.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultPageSize is used when Config leaves PageSize at zero.
const DefaultPageSize uint64 = 4096

const (
	defaultMaxRangesPerProcess = 64
	defaultHeartbeatTTL        = 5 * time.Second
	defaultMaxHandlerMisses    = 3
	snapshotVersion            = 1
)

// Errors reported by the guard.
var (
	ErrInvalidHandler = errors.New("handler id is not usable")
	ErrHandlerExists  = errors.New("handler already registered")
	ErrUnknownHandler = errors.New("handler is not registered")
	ErrHandlerGone    = errors.New("handler is no longer alive")
	ErrInvalidRange   = errors.New("range is not usable")
	ErrUnalignedRange = errors.New("range bounds are not page aligned")
	ErrRangeOverlap   = errors.New("range overlaps another range of the same process")
	ErrRangeLimit     = errors.New("per process range limit reached")
	ErrUnknownRange   = errors.New("range is not registered")
	ErrNoHandler      = errors.New("no handler covers the faulting address")
	ErrHandlerMisses  = errors.New("handler exhausted the page retry budget")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Config bounds the guard.
type Config struct {
	PageSize            uint64
	MaxRangesPerProcess int
	HeartbeatTTL        time.Duration
	MaxHandlerMisses    int
	Now                 func() time.Time
}

// Registration asks for one handling range of a process.
type Registration struct {
	PID       int
	Start     uint64
	End       uint64
	HandlerID string
	Backup    string
}

// RangeInfo is the stored form of one registered range.
type RangeInfo struct {
	ID         uint64 `json:"id"`
	PID        int    `json:"pid"`
	Start      uint64 `json:"start"`
	End        uint64 `json:"end"`
	HandlerID  string `json:"handler_id"`
	Backup     string `json:"backup"`
	Generation uint64 `json:"generation"`
}

// Fault is one page fault reported by the kernel.
type Fault struct {
	PID  int
	Addr uint64
}

// Resolution is the handler chosen for one page fault.
type Resolution struct {
	RangeID    uint64
	PID        int
	Page       uint64
	HandlerID  string
	Backup     string
	Generation uint64
	Token      uint64
}

// ProcessStat is the accounting of one process.
type ProcessStat struct {
	Ranges      int
	Resolutions uint64
}

type handlerState struct {
	lastSeen time.Time
}

type processState struct {
	ranges      map[uint64]RangeInfo
	resolutions uint64
}

type missKey struct {
	handlerID string
	page      uint64
}

type snapshotHandler struct {
	ID       string    `json:"id"`
	LastSeen time.Time `json:"last_seen"`
}

type snapshotProcess struct {
	PID         int    `json:"pid"`
	Resolutions uint64 `json:"resolutions"`
}

type snapshotMiss struct {
	HandlerID string `json:"handler_id"`
	Page      uint64 `json:"page"`
	Count     int    `json:"count"`
}

type snapshotData struct {
	Version        int               `json:"version"`
	NextRangeID    uint64            `json:"next_range_id"`
	NextGeneration uint64            `json:"next_generation"`
	NextToken      uint64            `json:"next_token"`
	Handlers       []snapshotHandler `json:"handlers"`
	Ranges         []RangeInfo       `json:"ranges"`
	Processes      []snapshotProcess `json:"processes"`
	Misses         []snapshotMiss    `json:"misses,omitempty"`
}

// UserfaultRangeGuard owns the handler table and the per process ranges.
type UserfaultRangeGuard struct {
	cfg Config

	mu             sync.Mutex
	handlers       map[string]*handlerState
	processes      map[int]*processState
	rangePID       map[uint64]int
	misses         map[missKey]int
	nextRangeID    uint64
	nextGeneration uint64
	nextToken      uint64
}

// NewUserfaultRangeGuard builds an empty guard.
func NewUserfaultRangeGuard(cfg Config) *UserfaultRangeGuard {
	if cfg.PageSize == 0 {
		cfg.PageSize = DefaultPageSize
	}
	if cfg.MaxRangesPerProcess <= 0 {
		cfg.MaxRangesPerProcess = defaultMaxRangesPerProcess
	}
	if cfg.HeartbeatTTL <= 0 {
		cfg.HeartbeatTTL = defaultHeartbeatTTL
	}
	if cfg.MaxHandlerMisses <= 0 {
		cfg.MaxHandlerMisses = defaultMaxHandlerMisses
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &UserfaultRangeGuard{
		cfg:            cfg,
		handlers:       make(map[string]*handlerState),
		processes:      make(map[int]*processState),
		rangePID:       make(map[uint64]int),
		misses:         make(map[missKey]int),
		nextRangeID:    1,
		nextGeneration: 1,
		nextToken:      1,
	}
}

// RegisterHandler adds one live page fault handler.
func (g *UserfaultRangeGuard) RegisterHandler(id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrInvalidHandler
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.handlers[id]; ok {
		return ErrHandlerExists
	}
	g.handlers[id] = &handlerState{lastSeen: g.cfg.Now()}
	return nil
}

// Heartbeat refreshes the liveness stamp of a handler.
func (g *UserfaultRangeGuard) Heartbeat(handlerID string, now time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	h, ok := g.handlers[handlerID]
	if !ok {
		return ErrUnknownHandler
	}
	if !g.handlerAliveLocked(h, now) {
		return ErrHandlerGone
	}
	h.lastSeen = now
	return nil
}

// HandlerAlive reports whether a handler may still serve faults.
func (g *UserfaultRangeGuard) HandlerAlive(handlerID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	h, ok := g.handlers[handlerID]
	if !ok {
		return false
	}
	return g.handlerAliveLocked(h, g.cfg.Now())
}

// Register stores one page aligned, non overlapping range of a process.
func (g *UserfaultRangeGuard) Register(reg Registration) (uint64, error) {
	if reg.PID <= 0 || reg.End <= reg.Start {
		return 0, ErrInvalidRange
	}
	pageSize := g.cfg.PageSize
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	if reg.Start%pageSize != 0 || reg.End%pageSize != 0 {
		return 0, ErrUnalignedRange
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	h, ok := g.handlers[reg.HandlerID]
	if !ok {
		return 0, ErrUnknownHandler
	}
	if !g.handlerAliveLocked(h, g.cfg.Now()) {
		return 0, ErrHandlerGone
	}

	ps := g.processes[reg.PID]
	if ps == nil {
		ps = &processState{ranges: make(map[uint64]RangeInfo)}
		g.processes[reg.PID] = ps
	}
	for _, existing := range ps.ranges {
		if rangesOverlap(existing.Start, existing.End, reg.Start, reg.End) {
			return 0, ErrRangeOverlap
		}
	}
	if len(ps.ranges) >= g.cfg.MaxRangesPerProcess {
		return 0, ErrRangeLimit
	}

	id := g.nextRangeID
	generation := g.nextGeneration
	if id == 0 || generation == 0 {
		return 0, ErrRangeLimit
	}
	g.nextRangeID++
	g.nextGeneration++

	ps.ranges[id] = RangeInfo{
		ID:         id,
		PID:        reg.PID,
		Start:      reg.Start,
		End:        reg.End,
		HandlerID:  reg.HandlerID,
		Backup:     reg.Backup,
		Generation: generation,
	}
	g.rangePID[id] = reg.PID
	return id, nil
}

// Unregister drops one whole range.
func (g *UserfaultRangeGuard) Unregister(id uint64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	pid, ok := g.rangePID[id]
	if !ok {
		return ErrUnknownRange
	}
	ps := g.processes[pid]
	if ps == nil {
		delete(g.rangePID, id)
		return ErrUnknownRange
	}
	info, ok := ps.ranges[id]
	if !ok {
		delete(g.rangePID, id)
		return ErrUnknownRange
	}
	delete(ps.ranges, id)
	delete(g.rangePID, id)
	g.removeUncoveredMissesLocked(info.HandlerID, info.Start, info.End)
	return nil
}

// UnregisterRange drops the pages of a process inside a range.
func (g *UserfaultRangeGuard) UnregisterRange(pid int, start, end uint64) error {
	if pid <= 0 || end <= start {
		return ErrInvalidRange
	}
	pageSize := g.cfg.PageSize
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	removeStart := start - start%pageSize
	removeEnd := end
	if rem := removeEnd % pageSize; rem != 0 {
		add := pageSize - rem
		if removeEnd > math.MaxUint64-add {
			return ErrInvalidRange
		}
		removeEnd += add
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	ps := g.processes[pid]
	if ps == nil || len(ps.ranges) == 0 {
		return ErrUnknownRange
	}

	type rangePlan struct {
		oldID   uint64
		replace []RangeInfo
	}
	plans := make([]rangePlan, 0)
	finalCount := len(ps.ranges)
	foundOverlap := false
	nextID := g.nextRangeID

	for _, info := range ps.ranges {
		if !rangesOverlap(info.Start, info.End, removeStart, removeEnd) {
			continue
		}
		foundOverlap = true
		plan := rangePlan{oldID: info.ID}

		if info.Start < removeStart {
			left := info
			left.End = removeStart
			plan.replace = append(plan.replace, left)
		}
		if info.End > removeEnd {
			right := info
			right.Start = removeEnd
			if len(plan.replace) == 0 {
				// A single remaining fragment keeps the original identity.
				plan.replace = append(plan.replace, right)
			} else {
				// Splitting one range into two needs a fresh identity for the
				// newly created right fragment.
				if nextID == 0 {
					return ErrRangeLimit
				}
				right.ID = nextID
				nextID++
				plan.replace = append(plan.replace, right)
			}
		}
		finalCount += len(plan.replace) - 1
		plans = append(plans, plan)
	}
	if !foundOverlap {
		return ErrUnknownRange
	}
	if finalCount > g.cfg.MaxRangesPerProcess {
		return ErrRangeLimit
	}

	for _, plan := range plans {
		oldInfo := ps.ranges[plan.oldID]
		delete(ps.ranges, plan.oldID)
		delete(g.rangePID, plan.oldID)
		for _, replacement := range plan.replace {
			ps.ranges[replacement.ID] = replacement
			g.rangePID[replacement.ID] = pid
		}
		g.removeUncoveredMissesLocked(oldInfo.HandlerID, removeStart, removeEnd)
	}
	g.nextRangeID = nextID
	return nil
}

// Handle routes one fault to the single owning handler.
func (g *UserfaultRangeGuard) Handle(fault Fault) (Resolution, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ps := g.processes[fault.PID]
	if ps == nil {
		return Resolution{}, ErrNoHandler
	}
	var owner RangeInfo
	found := false
	for _, info := range ps.ranges {
		if fault.Addr >= info.Start && fault.Addr < info.End {
			owner = info
			found = true
			break
		}
	}
	if !found {
		return Resolution{}, ErrNoHandler
	}

	h, ok := g.handlers[owner.HandlerID]
	if !ok {
		return Resolution{}, ErrHandlerGone
	}
	if !g.handlerAliveLocked(h, g.cfg.Now()) {
		return Resolution{}, ErrHandlerGone
	}

	pageSize := g.cfg.PageSize
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	page := fault.Addr - fault.Addr%pageSize
	if g.misses[missKey{handlerID: owner.HandlerID, page: page}] >= g.cfg.MaxHandlerMisses {
		return Resolution{}, ErrHandlerMisses
	}
	if g.nextToken == 0 {
		return Resolution{}, ErrHandlerMisses
	}
	token := g.nextToken
	g.nextToken++
	ps.resolutions++

	return Resolution{
		RangeID:    owner.ID,
		PID:        owner.PID,
		Page:       page,
		HandlerID:  owner.HandlerID,
		Backup:     owner.Backup,
		Generation: owner.Generation,
		Token:      token,
	}, nil
}

// ReportTimeout records one page level handler timeout.
func (g *UserfaultRangeGuard) ReportTimeout(handlerID string, page uint64) error {
	pageSize := g.cfg.PageSize
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	page -= page % pageSize

	g.mu.Lock()
	defer g.mu.Unlock()
	h, ok := g.handlers[handlerID]
	if !ok {
		return ErrUnknownHandler
	}
	if !g.handlerAliveLocked(h, g.cfg.Now()) {
		return ErrHandlerGone
	}
	key := missKey{handlerID: handlerID, page: page}
	count := g.misses[key]
	if count >= g.cfg.MaxHandlerMisses {
		return ErrHandlerMisses
	}
	g.misses[key] = count + 1
	return nil
}

// Fork creates the range table of a child process.
func (g *UserfaultRangeGuard) Fork(parentPID, childPID int) error {
	if parentPID <= 0 || childPID <= 0 || parentPID == childPID {
		return ErrInvalidRange
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	parent := g.processes[parentPID]
	if parent == nil || len(parent.ranges) == 0 {
		return ErrNoHandler
	}
	if child := g.processes[childPID]; child != nil && len(child.ranges) != 0 {
		return ErrInvalidRange
	}
	if len(parent.ranges) > g.cfg.MaxRangesPerProcess {
		return ErrRangeLimit
	}

	now := g.cfg.Now()
	sources := make([]RangeInfo, 0, len(parent.ranges))
	for _, info := range parent.ranges {
		h, ok := g.handlers[info.HandlerID]
		if !ok || !g.handlerAliveLocked(h, now) {
			return ErrHandlerGone
		}
		sources = append(sources, info)
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Start != sources[j].Start {
			return sources[i].Start < sources[j].Start
		}
		return sources[i].ID < sources[j].ID
	})

	nextID := g.nextRangeID
	nextGeneration := g.nextGeneration
	if nextID == 0 || nextGeneration == 0 ||
		uint64(len(sources)) > math.MaxUint64-nextID ||
		uint64(len(sources)) > math.MaxUint64-nextGeneration {
		return ErrRangeLimit
	}
	copied := make(map[uint64]RangeInfo, len(sources))
	for _, source := range sources {
		childRange := source
		childRange.ID = nextID
		childRange.PID = childPID
		childRange.Generation = nextGeneration
		nextID++
		nextGeneration++
		copied[childRange.ID] = childRange
	}

	g.processes[childPID] = &processState{
		ranges:      copied,
		resolutions: 0,
	}
	for id := range copied {
		g.rangePID[id] = childPID
	}
	g.nextRangeID = nextID
	g.nextGeneration = nextGeneration
	return nil
}

// Ranges lists the ranges of one process ordered by start address.
func (g *UserfaultRangeGuard) Ranges(pid int) []RangeInfo {
	g.mu.Lock()
	defer g.mu.Unlock()
	ps := g.processes[pid]
	if ps == nil || len(ps.ranges) == 0 {
		return nil
	}
	out := make([]RangeInfo, 0, len(ps.ranges))
	for _, info := range ps.ranges {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Sweep removes handlers whose heartbeat expired and their ranges.
func (g *UserfaultRangeGuard) Sweep(now time.Time) []uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	expired := make(map[string]struct{})
	for id, h := range g.handlers {
		if !g.handlerAliveLocked(h, now) {
			expired[id] = struct{}{}
			delete(g.handlers, id)
		}
	}
	if len(expired) == 0 {
		return nil
	}

	reclaimed := make([]uint64, 0)
	for _, ps := range g.processes {
		for id, info := range ps.ranges {
			if _, ok := expired[info.HandlerID]; ok {
				delete(ps.ranges, id)
				delete(g.rangePID, id)
				reclaimed = append(reclaimed, id)
			}
		}
	}
	for key := range g.misses {
		if _, ok := expired[key.handlerID]; ok {
			delete(g.misses, key)
		}
	}
	sort.Slice(reclaimed, func(i, j int) bool { return reclaimed[i] < reclaimed[j] })
	return reclaimed
}

// Stat reads the accounting of one process.
func (g *UserfaultRangeGuard) Stat(pid int) ProcessStat {
	g.mu.Lock()
	defer g.mu.Unlock()
	ps := g.processes[pid]
	if ps == nil {
		return ProcessStat{}
	}
	return ProcessStat{Ranges: len(ps.ranges), Resolutions: ps.resolutions}
}

// Snapshot serialises handlers, ranges, the generation and the token water
// marks.
func (g *UserfaultRangeGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	snap := snapshotData{
		Version:        snapshotVersion,
		NextRangeID:    g.nextRangeID,
		NextGeneration: g.nextGeneration,
		NextToken:      g.nextToken,
		Handlers:       make([]snapshotHandler, 0, len(g.handlers)),
		Ranges:         make([]RangeInfo, 0, len(g.rangePID)),
		Processes:      make([]snapshotProcess, 0, len(g.processes)),
		Misses:         make([]snapshotMiss, 0, len(g.misses)),
	}
	for id, h := range g.handlers {
		snap.Handlers = append(snap.Handlers, snapshotHandler{ID: id, LastSeen: h.lastSeen})
	}
	sort.Slice(snap.Handlers, func(i, j int) bool { return snap.Handlers[i].ID < snap.Handlers[j].ID })
	for _, info := range g.processes {
		for _, r := range info.ranges {
			snap.Ranges = append(snap.Ranges, r)
		}
	}
	sort.Slice(snap.Ranges, func(i, j int) bool { return snap.Ranges[i].ID < snap.Ranges[j].ID })
	for pid, ps := range g.processes {
		snap.Processes = append(snap.Processes, snapshotProcess{PID: pid, Resolutions: ps.resolutions})
	}
	sort.Slice(snap.Processes, func(i, j int) bool { return snap.Processes[i].PID < snap.Processes[j].PID })
	for key, count := range g.misses {
		snap.Misses = append(snap.Misses, snapshotMiss{
			HandlerID: key.handlerID,
			Page:      key.page,
			Count:     count,
		})
	}
	sort.Slice(snap.Misses, func(i, j int) bool {
		if snap.Misses[i].HandlerID != snap.Misses[j].HandlerID {
			return snap.Misses[i].HandlerID < snap.Misses[j].HandlerID
		}
		return snap.Misses[i].Page < snap.Misses[j].Page
	})

	data, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *UserfaultRangeGuard) Restore(data []byte) error {
	var snap snapshotData
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if snap.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, snap.Version)
	}

	pageSize := g.cfg.PageSize
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	handlers := make(map[string]*handlerState, len(snap.Handlers))
	for _, h := range snap.Handlers {
		if strings.TrimSpace(h.ID) == "" {
			return fmt.Errorf("%w: empty handler id", ErrBadSnapshot)
		}
		if _, ok := handlers[h.ID]; ok {
			return fmt.Errorf("%w: duplicate handler %q", ErrBadSnapshot, h.ID)
		}
		handlers[h.ID] = &handlerState{lastSeen: h.LastSeen}
	}

	processes := make(map[int]*processState)
	for _, p := range snap.Processes {
		if p.PID <= 0 {
			return fmt.Errorf("%w: invalid process id %d", ErrBadSnapshot, p.PID)
		}
		if _, ok := processes[p.PID]; ok {
			return fmt.Errorf("%w: duplicate process %d", ErrBadSnapshot, p.PID)
		}
		processes[p.PID] = &processState{
			ranges:      make(map[uint64]RangeInfo),
			resolutions: p.Resolutions,
		}
	}

	rangePID := make(map[uint64]int, len(snap.Ranges))
	var maxRangeID uint64
	var maxGeneration uint64
	for _, info := range snap.Ranges {
		if info.ID == 0 {
			return fmt.Errorf("%w: zero range id", ErrBadSnapshot)
		}
		if info.PID <= 0 || info.End <= info.Start ||
			info.Start%pageSize != 0 || info.End%pageSize != 0 {
			return fmt.Errorf("%w: invalid range %d", ErrBadSnapshot, info.ID)
		}
		if _, ok := rangePID[info.ID]; ok {
			return fmt.Errorf("%w: duplicate range id %d", ErrBadSnapshot, info.ID)
		}
		if _, ok := handlers[info.HandlerID]; !ok {
			return fmt.Errorf("%w: range %d references unknown handler", ErrBadSnapshot, info.ID)
		}
		ps := processes[info.PID]
		if ps == nil {
			ps = &processState{ranges: make(map[uint64]RangeInfo)}
			processes[info.PID] = ps
		}
		for _, existing := range ps.ranges {
			if rangesOverlap(existing.Start, existing.End, info.Start, info.End) {
				return fmt.Errorf("%w: overlapping ranges in process %d", ErrBadSnapshot, info.PID)
			}
		}
		if len(ps.ranges) >= g.cfg.MaxRangesPerProcess {
			return fmt.Errorf("%w: range limit exceeded for process %d", ErrBadSnapshot, info.PID)
		}
		ps.ranges[info.ID] = info
		rangePID[info.ID] = info.PID
		if info.ID > maxRangeID {
			maxRangeID = info.ID
		}
		if info.Generation > maxGeneration {
			maxGeneration = info.Generation
		}
	}
	if snap.NextRangeID == 0 || snap.NextRangeID <= maxRangeID {
		return fmt.Errorf("%w: invalid next range id", ErrBadSnapshot)
	}
	if snap.NextGeneration == 0 || snap.NextGeneration <= maxGeneration {
		return fmt.Errorf("%w: invalid next generation", ErrBadSnapshot)
	}
	if snap.NextToken == 0 {
		return fmt.Errorf("%w: invalid next token", ErrBadSnapshot)
	}

	misses := make(map[missKey]int, len(snap.Misses))
	for _, miss := range snap.Misses {
		if strings.TrimSpace(miss.HandlerID) == "" {
			return fmt.Errorf("%w: invalid timeout record", ErrBadSnapshot)
		}
		if _, ok := handlers[miss.HandlerID]; !ok {
			return fmt.Errorf("%w: timeout references unknown handler", ErrBadSnapshot)
		}
		if miss.Count <= 0 || miss.Page%pageSize != 0 {
			return fmt.Errorf("%w: invalid timeout record", ErrBadSnapshot)
		}
		key := missKey{handlerID: miss.HandlerID, page: miss.Page}
		if _, ok := misses[key]; ok {
			return fmt.Errorf("%w: duplicate timeout record", ErrBadSnapshot)
		}
		misses[key] = miss.Count
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.handlers = handlers
	g.processes = processes
	g.rangePID = rangePID
	g.misses = misses
	g.nextRangeID = snap.NextRangeID
	g.nextGeneration = snap.NextGeneration
	g.nextToken = snap.NextToken
	return nil
}

func (g *UserfaultRangeGuard) handlerAliveLocked(h *handlerState, now time.Time) bool {
	return now.Sub(h.lastSeen) <= g.cfg.HeartbeatTTL
}

func rangesOverlap(aStart, aEnd, bStart, bEnd uint64) bool {
	return aStart < bEnd && bStart < aEnd
}

func (g *UserfaultRangeGuard) removeUncoveredMissesLocked(handlerID string, start, end uint64) {
	for key := range g.misses {
		if key.handlerID != handlerID || key.page < start || key.page >= end {
			continue
		}
		covered := false
		for _, ps := range g.processes {
			for _, info := range ps.ranges {
				if info.HandlerID == handlerID && key.page >= info.Start && key.page < info.End {
					covered = true
					break
				}
			}
			if covered {
				break
			}
		}
		if !covered {
			delete(g.misses, key)
		}
	}
}
