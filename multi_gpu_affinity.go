// Package task assigns render frames and viewports to several GPUs, keeps a
// cross adapter shared resource ledger and falls back to a single adapter when
// one of them is taken offline.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

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

// MultiGpuAffinity owns the adapter table, the in flight frames, the shared
// resource ledger and the fallback state.
type MultiGpuAffinity struct {
}

// NewMultiGpuAffinity validates the configuration and creates an empty
// scheduler.
func NewMultiGpuAffinity(cfg MGPUConfig) (*MultiGpuAffinity, error) {
	return nil, errNotImplemented
}

// RegisterAdapter adds one adapter to the pool.
func (m *MultiGpuAffinity) RegisterAdapter(a AdapterInfo) error {
	return errNotImplemented
}

// AssignFrame picks the adapter of one frame or defers it to the next frame.
func (m *MultiGpuAffinity) AssignFrame(frame FrameReq) (Assignment, error) {
	return Assignment{}, errNotImplemented
}

// AssignViewports spreads the split screen viewports over the enabled
// adapters.
func (m *MultiGpuAffinity) AssignViewports(vps []Viewport) ([]Assignment, error) {
	return nil, errNotImplemented
}

// Submit marks a pending frame as in flight on its adapter.
func (m *MultiGpuAffinity) Submit(frame int64) error {
	return errNotImplemented
}

// Complete releases one frame and its queue slot.
func (m *MultiGpuAffinity) Complete(frame int64) error {
	return errNotImplemented
}

// AcquireShared creates or reads one cross adapter resource.
func (m *MultiGpuAffinity) AcquireShared(key string, size Size) (SharedRes, error) {
	return SharedRes{}, errNotImplemented
}

// SyncCrossAdapter orders and charges the cross adapter accesses of one frame.
func (m *MultiGpuAffinity) SyncCrossAdapter(tokens []FenceToken) error {
	return errNotImplemented
}

// DisableAdapter takes one adapter offline and rebuilds the fallback plan.
func (m *MultiGpuAffinity) DisableAdapter(id string) error {
	return errNotImplemented
}

// FallbackState reports the current single adapter fallback.
func (m *MultiGpuAffinity) FallbackState() FallbackInfo {
	return FallbackInfo{}
}

// Stats returns the live accounting.
func (m *MultiGpuAffinity) Stats() MGPUStats {
	return MGPUStats{}
}

// Snapshot serialises adapters, assignments, the shared ledger and the
// fallback state.
func (m *MultiGpuAffinity) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole scheduler with a snapshot, all or nothing.
func (m *MultiGpuAffinity) Restore(data []byte) error {
	return errNotImplemented
}
