// Package task assigns render frames and viewports to several GPUs, keeps a
// cross adapter shared resource ledger and falls back to a single adapter when
// one of them is taken offline.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Adapter selection policies.
const (
	ModeAlternate = "alternate"
	ModeSplit     = "split"
)

// Errors reported by the affinity scheduler.
var (
	ErrBadConfig        = errors.New("multi gpu configuration is invalid")
	ErrBadAdapter       = errors.New("adapter description is invalid")
	ErrDuplicateAdapter = errors.New("adapter is already registered")
	ErrAdapterLimit     = errors.New("adapter limit reached")
	ErrUnknownAdapter   = errors.New("adapter is not registered")
	ErrNoAdapter        = errors.New("no enabled adapter can take work")
	ErrBadFrame         = errors.New("frame request is invalid")
	ErrUnknownFrame     = errors.New("frame is not assigned or already finished")
	ErrBadShared        = errors.New("shared resource request is invalid")
	ErrSharedMismatch   = errors.New("shared resource size mismatch")
	ErrSharedLimit      = errors.New("shared resource byte budget exceeded")
	ErrUnknownShared    = errors.New("shared resource is not registered")
	ErrFenceOrder       = errors.New("cross adapter fence order is violated")
	ErrCopyBudget       = errors.New("shared copy bandwidth budget exceeded")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

const (
	snapshotVersion = 1
	maxInt64Value   = int64(1<<63 - 1)
)

// Size is a two dimensional extent.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// AdapterInfo describes one graphics adapter.
type AdapterInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	QueueDepth int    `json:"queue_depth"`
	MemoryMB   int    `json:"memory_mb"`
}

// FrameReq is one frame submitted by the renderer.
type FrameReq struct {
	Number    int64 `json:"number"`
	Passes    int   `json:"passes"`
	Viewports int   `json:"viewports"`
}

// Assignment is the decision taken for one frame or one viewport.
type Assignment struct {
	Frame    int64  `json:"frame"`
	Viewport string `json:"viewport"`
	Adapter  string `json:"adapter"`
	Passes   int    `json:"passes"`
	Deferred bool   `json:"deferred"`
	Reason   string `json:"reason"`
}

// Viewport is one split screen target.
type Viewport struct {
	ID     string `json:"id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// SharedRes is one cross adapter resource.
type SharedRes struct {
	Key    string `json:"key"`
	Size   Size   `json:"size"`
	Bytes  int64  `json:"bytes"`
	Writes int    `json:"writes"`
	Reads  int    `json:"reads"`
}

// FenceToken is one ordered access stamp on a shared resource.
type FenceToken struct {
	Key     string `json:"key"`
	Adapter string `json:"adapter"`
	Seq     int64  `json:"seq"`
	Write   bool   `json:"write"`
}

// FallbackInfo reports the single adapter fallback state.
type FallbackInfo struct {
	Active   bool     `json:"active"`
	Disabled []string `json:"disabled"`
	Pending  int      `json:"pending"`
	Reason   string   `json:"reason"`
}

// MGPUStats is the live accounting of the scheduler.
type MGPUStats struct {
	Adapters    int   `json:"adapters"`
	Enabled     int   `json:"enabled"`
	Pending     int   `json:"pending"`
	InFlight    int   `json:"in_flight"`
	Deferred    int   `json:"deferred"`
	Migrated    int   `json:"migrated"`
	SharedBytes int64 `json:"shared_bytes"`
}

// MGPUConfig bounds the scheduler.
type MGPUConfig struct {
	MaxAdapters    int    `json:"max_adapters"`
	QueueDepth     int    `json:"queue_depth"`
	MaxSharedBytes int64  `json:"max_shared_bytes"`
	MaxCopyBytes   int64  `json:"max_copy_bytes"`
	Mode           string `json:"mode"`
}

type adapterState struct {
	Info       AdapterInfo
	QueueDepth int
	Disabled   bool
	Used       int
}

type frameState struct {
	Number   int64
	Adapter  string
	Passes   int
	InFlight bool
}

type sharedState struct {
	Key     string
	Size    Size
	Bytes   int64
	Writes  int
	Reads   int
	LastSeq int64
}

// MultiGpuAffinity owns the adapter table, the in flight frames, the shared
// resource ledger and the fallback state. A single lock guards the complete
// state so scheduling either observes the old table or the new table, never a
// partially migrated one.
type MultiGpuAffinity struct {
	mu             sync.RWMutex
	cfg            MGPUConfig
	adapters       []adapterState
	adapterIndex   map[string]int
	frames         map[int64]frameState
	shared         map[string]sharedState
	sharedBytes    int64
	deferred       int
	migrated       int
	fallbackReason string
}

// NewMultiGpuAffinity validates the configuration and creates an empty
// scheduler.
func NewMultiGpuAffinity(cfg MGPUConfig) (*MultiGpuAffinity, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return &MultiGpuAffinity{
		cfg:          cfg,
		adapterIndex: make(map[string]int),
		frames:       make(map[int64]frameState),
		shared:       make(map[string]sharedState),
	}, nil
}

func validateConfig(cfg MGPUConfig) error {
	if cfg.MaxAdapters <= 0 || cfg.QueueDepth <= 0 || cfg.MaxSharedBytes <= 0 || cfg.MaxCopyBytes <= 0 {
		return ErrBadConfig
	}
	if cfg.Mode != ModeAlternate && cfg.Mode != ModeSplit {
		return ErrBadConfig
	}
	return nil
}

func effectiveQueueDepth(cfg MGPUConfig, a AdapterInfo) int {
	if a.QueueDepth > 0 {
		return a.QueueDepth
	}
	return cfg.QueueDepth
}

// RegisterAdapter adds one adapter to the pool.
func (m *MultiGpuAffinity) RegisterAdapter(a AdapterInfo) error {
	if strings.TrimSpace(a.ID) == "" || a.QueueDepth < 0 {
		return ErrBadAdapter
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.adapterIndex[a.ID]; ok {
		return ErrDuplicateAdapter
	}
	if len(m.adapters) >= m.cfg.MaxAdapters {
		return ErrAdapterLimit
	}

	depth := effectiveQueueDepth(m.cfg, a)
	m.adapterIndex[a.ID] = len(m.adapters)
	m.adapters = append(m.adapters, adapterState{
		Info:       a,
		QueueDepth: depth,
	})
	return nil
}

// AssignFrame picks the adapter of one frame or defers it to the next frame.
func (m *MultiGpuAffinity) AssignFrame(frame FrameReq) (Assignment, error) {
	if frame.Number <= 0 || frame.Passes <= 0 {
		return Assignment{}, ErrBadFrame
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.frames[frame.Number]; exists {
		return Assignment{}, ErrBadFrame
	}

	idx, ok := m.pickFrameAdapterLocked(frame.Number)
	if !ok {
		return Assignment{}, ErrNoAdapter
	}

	a := &m.adapters[idx]
	assignment := Assignment{
		Frame:   frame.Number,
		Adapter: a.Info.ID,
		Passes:  frame.Passes,
	}
	if a.Used >= a.QueueDepth {
		m.deferred++
		assignment.Deferred = true
		assignment.Reason = fmt.Sprintf("adapter %s queue depth %d reached", a.Info.ID, a.QueueDepth)
		return assignment, nil
	}

	m.frames[frame.Number] = frameState{
		Number:  frame.Number,
		Adapter: a.Info.ID,
		Passes:  frame.Passes,
	}
	a.Used++
	return assignment, nil
}

func (m *MultiGpuAffinity) pickFrameAdapterLocked(number int64) (int, bool) {
	if m.cfg.Mode == ModeAlternate {
		enabled := m.enabledIndicesLocked()
		if len(enabled) == 0 {
			return 0, false
		}
		pos := int((number - 1) % int64(len(enabled)))
		return enabled[pos], true
	}

	best := -1
	for i := range m.adapters {
		a := &m.adapters[i]
		if a.Disabled {
			continue
		}
		if best < 0 || a.Used < m.adapters[best].Used {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

func (m *MultiGpuAffinity) enabledIndicesLocked() []int {
	enabled := make([]int, 0, len(m.adapters))
	for i := range m.adapters {
		if !m.adapters[i].Disabled {
			enabled = append(enabled, i)
		}
	}
	return enabled
}

// AssignViewports spreads the split screen viewports over the enabled
// adapters.
func (m *MultiGpuAffinity) AssignViewports(vps []Viewport) ([]Assignment, error) {
	assignments := make([]Assignment, 0, len(vps))
	if len(vps) == 0 {
		return assignments, nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	enabled := m.enabledIndicesLocked()
	if len(enabled) == 0 {
		return nil, ErrNoAdapter
	}

	for i, vp := range vps {
		adapter := m.adapters[enabled[i%len(enabled)]]
		assignments = append(assignments, Assignment{
			Viewport: vp.ID,
			Adapter:  adapter.Info.ID,
		})
	}
	return assignments, nil
}

// Submit marks a pending frame as in flight on its adapter.
func (m *MultiGpuAffinity) Submit(frame int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, ok := m.frames[frame]
	if !ok || f.InFlight {
		return ErrUnknownFrame
	}
	f.InFlight = true
	m.frames[frame] = f
	return nil
}

// Complete releases one frame and its queue slot.
func (m *MultiGpuAffinity) Complete(frame int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, ok := m.frames[frame]
	if !ok {
		return ErrUnknownFrame
	}
	delete(m.frames, frame)
	if idx, ok := m.adapterIndex[f.Adapter]; ok && m.adapters[idx].Used > 0 {
		m.adapters[idx].Used--
	}

	// A disabled adapter may have retained pending work when every remaining
	// queue was full. Completion creates an opportunity to finish moving it.
	m.migratePendingFromDisabledLocked()
	return nil
}

func (m *MultiGpuAffinity) migratePendingFromDisabledLocked() {
	if len(m.frames) == 0 {
		return
	}

	frames := make([]int64, 0)
	for number, f := range m.frames {
		if f.InFlight {
			continue
		}
		if idx, ok := m.adapterIndex[f.Adapter]; ok && m.adapters[idx].Disabled {
			frames = append(frames, number)
		}
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i] < frames[j] })

	for _, number := range frames {
		f := m.frames[number]
		source, ok := m.adapterIndex[f.Adapter]
		if !ok {
			continue
		}
		target, ok := m.migrationTargetLocked(source)
		if !ok {
			continue
		}
		if m.adapters[source].Used > 0 {
			m.adapters[source].Used--
		}
		f.Adapter = m.adapters[target].Info.ID
		m.frames[number] = f
		m.adapters[target].Used++
		m.migrated++
	}
}

func (m *MultiGpuAffinity) migrationTargetLocked(source int) (int, bool) {
	// Prefer the primary (first registered enabled) adapter, as required by
	// single-card fallback. If its hard queue cap is full, another enabled
	// adapter is preferred over dropping or splitting the frame.
	for i := range m.adapters {
		if i == source || m.adapters[i].Disabled {
			continue
		}
		if m.adapters[i].Used < m.adapters[i].QueueDepth {
			return i, true
		}
		break
	}
	for i := range m.adapters {
		if i == source || m.adapters[i].Disabled {
			continue
		}
		if m.adapters[i].Used < m.adapters[i].QueueDepth {
			return i, true
		}
	}
	return 0, false
}

// AcquireShared creates or reads one cross adapter resource.
func (m *MultiGpuAffinity) AcquireShared(key string, size Size) (SharedRes, error) {
	if strings.TrimSpace(key) == "" || size.Width <= 0 || size.Height <= 0 {
		return SharedRes{}, ErrBadShared
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.shared[key]; ok {
		if existing.Size != size {
			return SharedRes{}, ErrSharedMismatch
		}
		return sharedResult(existing), nil
	}

	w := int64(size.Width)
	h := int64(size.Height)
	if w > maxInt64Value/h {
		return SharedRes{}, ErrSharedLimit
	}
	bytes := w * h
	if bytes > m.cfg.MaxSharedBytes-m.sharedBytes {
		return SharedRes{}, ErrSharedLimit
	}

	res := sharedState{Key: key, Size: size, Bytes: bytes}
	m.shared[key] = res
	m.sharedBytes += bytes
	return sharedResult(res), nil
}

func sharedResult(res sharedState) SharedRes {
	return SharedRes{
		Key:    res.Key,
		Size:   res.Size,
		Bytes:  res.Bytes,
		Writes: res.Writes,
		Reads:  res.Reads,
	}
}

// SyncCrossAdapter orders and charges the cross adapter accesses of one frame.
func (m *MultiGpuAffinity) SyncCrossAdapter(tokens []FenceToken) error {
	if len(tokens) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	seenSeq := make(map[string]int64)
	pendingWrite := make(map[string]bool)
	var copyBytes int64

	for _, token := range tokens {
		res, ok := m.shared[token.Key]
		if !ok {
			return ErrUnknownShared
		}
		if token.Seq <= 0 || token.Seq <= res.LastSeq {
			return ErrFenceOrder
		}
		if last, seen := seenSeq[token.Key]; seen && token.Seq <= last {
			return ErrFenceOrder
		}
		if !token.Write && pendingWrite[token.Key] {
			// A read in the same batch cannot observe a write whose copy has
			// not completed yet. The whole batch is rejected without waiting.
			return ErrFenceOrder
		}

		if res.Bytes > m.cfg.MaxCopyBytes || copyBytes > m.cfg.MaxCopyBytes-res.Bytes {
			return ErrCopyBudget
		}
		copyBytes += res.Bytes
		seenSeq[token.Key] = token.Seq
		if token.Write {
			pendingWrite[token.Key] = true
		}
	}

	for _, token := range tokens {
		res := m.shared[token.Key]
		res.LastSeq = token.Seq
		if token.Write {
			res.Writes++
		} else {
			res.Reads++
		}
		m.shared[token.Key] = res
	}
	return nil
}

// DisableAdapter takes one adapter offline and rebuilds the fallback plan.
func (m *MultiGpuAffinity) DisableAdapter(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx, ok := m.adapterIndex[id]
	if !ok {
		return ErrUnknownAdapter
	}
	if m.adapters[idx].Disabled {
		m.migratePendingFromDisabledLocked()
		return nil
	}

	enabled := 0
	for i := range m.adapters {
		if !m.adapters[i].Disabled {
			enabled++
		}
	}
	if enabled <= 1 {
		return ErrNoAdapter
	}

	m.adapters[idx].Disabled = true
	m.fallbackReason = fmt.Sprintf("adapter %s disabled; work is routed to the remaining adapters", id)
	m.migratePendingFromDisabledLocked()
	return nil
}

// FallbackState reports the current single adapter fallback.
func (m *MultiGpuAffinity) FallbackState() FallbackInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fallbackStateLocked()
}

func (m *MultiGpuAffinity) fallbackStateLocked() FallbackInfo {
	disabled := make([]string, 0)
	pending := 0
	for i := range m.adapters {
		if m.adapters[i].Disabled {
			disabled = append(disabled, m.adapters[i].Info.ID)
		}
	}
	for _, f := range m.frames {
		if !f.InFlight {
			pending++
		}
	}
	sort.Strings(disabled)

	info := FallbackInfo{
		Active:   len(disabled) > 0,
		Disabled: disabled,
		Pending:  pending,
	}
	if info.Active {
		info.Reason = m.fallbackReason
	}
	return info
}

// Stats returns the live accounting.
func (m *MultiGpuAffinity) Stats() MGPUStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := MGPUStats{
		Adapters:    len(m.adapters),
		Deferred:    m.deferred,
		Migrated:    m.migrated,
		SharedBytes: m.sharedBytes,
	}
	for i := range m.adapters {
		if !m.adapters[i].Disabled {
			stats.Enabled++
		}
	}
	for _, f := range m.frames {
		if f.InFlight {
			stats.InFlight++
		} else {
			stats.Pending++
		}
	}
	return stats
}

type snapshotAdapter struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	QueueDepth int    `json:"queue_depth"`
	MemoryMB   int    `json:"memory_mb"`
	Disabled   bool   `json:"disabled"`
}

type snapshotFrame struct {
	Number   int64  `json:"number"`
	Adapter  string `json:"adapter"`
	Passes   int    `json:"passes"`
	InFlight bool   `json:"in_flight"`
}

type snapshotShared struct {
	Key     string `json:"key"`
	Size    Size   `json:"size"`
	Bytes   int64  `json:"bytes"`
	Writes  int    `json:"writes"`
	Reads   int    `json:"reads"`
	LastSeq int64  `json:"last_seq"`
}

type snapshotData struct {
	Version  int               `json:"version"`
	Config   MGPUConfig        `json:"config"`
	Adapters []snapshotAdapter `json:"adapters"`
	Frames   []snapshotFrame   `json:"frames"`
	Shared   []snapshotShared  `json:"shared"`
	Deferred int               `json:"deferred"`
	Migrated int               `json:"migrated"`
	Fallback FallbackInfo      `json:"fallback"`
}

// Snapshot serialises adapters, assignments, the shared ledger and the
// fallback state.
func (m *MultiGpuAffinity) Snapshot() ([]byte, error) {
	m.mu.RLock()
	snapshot := m.snapshotLocked()
	m.mu.RUnlock()
	return json.Marshal(snapshot)
}

func (m *MultiGpuAffinity) snapshotLocked() snapshotData {
	snapshot := snapshotData{
		Version:  snapshotVersion,
		Config:   m.cfg,
		Adapters: make([]snapshotAdapter, 0, len(m.adapters)),
		Frames:   make([]snapshotFrame, 0, len(m.frames)),
		Shared:   make([]snapshotShared, 0, len(m.shared)),
		Deferred: m.deferred,
		Migrated: m.migrated,
		Fallback: m.fallbackStateLocked(),
	}

	for i := range m.adapters {
		a := &m.adapters[i]
		snapshot.Adapters = append(snapshot.Adapters, snapshotAdapter{
			ID:         a.Info.ID,
			Name:       a.Info.Name,
			QueueDepth: a.QueueDepth,
			MemoryMB:   a.Info.MemoryMB,
			Disabled:   a.Disabled,
		})
	}

	numbers := make([]int64, 0, len(m.frames))
	for number := range m.frames {
		numbers = append(numbers, number)
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
	for _, number := range numbers {
		f := m.frames[number]
		snapshot.Frames = append(snapshot.Frames, snapshotFrame{
			Number:   f.Number,
			Adapter:  f.Adapter,
			Passes:   f.Passes,
			InFlight: f.InFlight,
		})
	}

	keys := make([]string, 0, len(m.shared))
	for key := range m.shared {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		res := m.shared[key]
		snapshot.Shared = append(snapshot.Shared, snapshotShared{
			Key:     res.Key,
			Size:    res.Size,
			Bytes:   res.Bytes,
			Writes:  res.Writes,
			Reads:   res.Reads,
			LastSeq: res.LastSeq,
		})
	}
	return snapshot
}

// Restore replaces the whole scheduler with a snapshot, all or nothing.
func (m *MultiGpuAffinity) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	next, err := restoreSnapshot(snapshot)
	if err != nil {
		return ErrBadSnapshot
	}

	m.mu.Lock()
	m.cfg = next.cfg
	m.adapters = next.adapters
	m.adapterIndex = next.adapterIndex
	m.frames = next.frames
	m.shared = next.shared
	m.sharedBytes = next.sharedBytes
	m.deferred = next.deferred
	m.migrated = next.migrated
	m.fallbackReason = next.fallbackReason
	m.mu.Unlock()
	return nil
}

func restoreSnapshot(snapshot snapshotData) (*MultiGpuAffinity, error) {
	if snapshot.Version != snapshotVersion {
		return nil, ErrBadSnapshot
	}
	if err := validateConfig(snapshot.Config); err != nil {
		return nil, ErrBadSnapshot
	}
	if len(snapshot.Adapters) > snapshot.Config.MaxAdapters || snapshot.Deferred < 0 || snapshot.Migrated < 0 {
		return nil, ErrBadSnapshot
	}

	next := &MultiGpuAffinity{
		cfg:          snapshot.Config,
		adapters:     make([]adapterState, 0, len(snapshot.Adapters)),
		adapterIndex: make(map[string]int, len(snapshot.Adapters)),
		frames:       make(map[int64]frameState, len(snapshot.Frames)),
		shared:       make(map[string]sharedState, len(snapshot.Shared)),
		deferred:     snapshot.Deferred,
		migrated:     snapshot.Migrated,
	}
	enabled := 0
	for _, adapter := range snapshot.Adapters {
		if strings.TrimSpace(adapter.ID) == "" || adapter.QueueDepth <= 0 {
			return nil, ErrBadSnapshot
		}
		if _, exists := next.adapterIndex[adapter.ID]; exists {
			return nil, ErrBadSnapshot
		}
		if !adapter.Disabled {
			enabled++
		}
		next.adapterIndex[adapter.ID] = len(next.adapters)
		next.adapters = append(next.adapters, adapterState{
			Info: AdapterInfo{
				ID:         adapter.ID,
				Name:       adapter.Name,
				QueueDepth: adapter.QueueDepth,
				MemoryMB:   adapter.MemoryMB,
			},
			QueueDepth: adapter.QueueDepth,
			Disabled:   adapter.Disabled,
		})
	}
	if len(next.adapters) > 0 && enabled == 0 {
		return nil, ErrBadSnapshot
	}

	for _, frame := range snapshot.Frames {
		if frame.Number <= 0 || frame.Passes <= 0 {
			return nil, ErrBadSnapshot
		}
		if _, exists := next.frames[frame.Number]; exists {
			return nil, ErrBadSnapshot
		}
		idx, exists := next.adapterIndex[frame.Adapter]
		if !exists {
			return nil, ErrBadSnapshot
		}
		next.adapters[idx].Used++
		if next.adapters[idx].Used > next.adapters[idx].QueueDepth {
			return nil, ErrBadSnapshot
		}
		next.frames[frame.Number] = frameState{
			Number:   frame.Number,
			Adapter:  frame.Adapter,
			Passes:   frame.Passes,
			InFlight: frame.InFlight,
		}
	}

	for _, shared := range snapshot.Shared {
		if strings.TrimSpace(shared.Key) == "" || shared.Size.Width <= 0 || shared.Size.Height <= 0 {
			return nil, ErrBadSnapshot
		}
		if _, exists := next.shared[shared.Key]; exists {
			return nil, ErrBadSnapshot
		}
		w := int64(shared.Size.Width)
		h := int64(shared.Size.Height)
		if w > maxInt64Value/h || shared.Bytes != w*h || shared.Bytes <= 0 {
			return nil, ErrBadSnapshot
		}
		if shared.Writes < 0 || shared.Reads < 0 || shared.LastSeq < 0 {
			return nil, ErrBadSnapshot
		}
		if shared.Bytes > next.cfg.MaxSharedBytes-next.sharedBytes {
			return nil, ErrBadSnapshot
		}
		res := sharedState{
			Key:     shared.Key,
			Size:    shared.Size,
			Bytes:   shared.Bytes,
			Writes:  shared.Writes,
			Reads:   shared.Reads,
			LastSeq: shared.LastSeq,
		}
		next.shared[shared.Key] = res
		next.sharedBytes += shared.Bytes
	}

	if snapshot.Fallback.Active != (enabled < len(next.adapters)) {
		return nil, ErrBadSnapshot
	}
	if snapshot.Fallback.Pending != next.pendingCount() {
		return nil, ErrBadSnapshot
	}
	expectedDisabled := next.disabledIDs()
	if len(snapshot.Fallback.Disabled) != len(expectedDisabled) {
		return nil, ErrBadSnapshot
	}
	for i := range expectedDisabled {
		if snapshot.Fallback.Disabled[i] != expectedDisabled[i] {
			return nil, ErrBadSnapshot
		}
	}

	next.fallbackReason = snapshot.Fallback.Reason
	if next.fallbackReason == "" && len(expectedDisabled) > 0 {
		next.fallbackReason = "one or more adapters are disabled"
	}
	return next, nil
}

func (m *MultiGpuAffinity) pendingCount() int {
	pending := 0
	for _, f := range m.frames {
		if !f.InFlight {
			pending++
		}
	}
	return pending
}

func (m *MultiGpuAffinity) disabledIDs() []string {
	disabled := make([]string, 0)
	for i := range m.adapters {
		if m.adapters[i].Disabled {
			disabled = append(disabled, m.adapters[i].Info.ID)
		}
	}
	sort.Strings(disabled)
	return disabled
}
