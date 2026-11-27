// Package task plans, generates, verifies and resumes proxy media for source
// assets, keeping the source time base and timecode offset intact.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Proxy tiers.
const (
	TierHigh = "1080p"
	TierMid  = "720p"
	TierLow  = "480p"
)

// Proxy status values.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusComplete  = "complete"
	StatusSuspended = "suspended"
	StatusUnknown   = "unknown"
)

// Contract defaults used when the limits leave a field unset.
const (
	DefaultMaxConcurrent       = 4
	DefaultDiskBudget    int64 = 8 << 30
	DefaultMaxShards           = 4096
	DefaultShardMS       int64 = 10000
)

// Errors reported by the generator.
var (
	ErrBadAsset     = errors.New("asset fields are out of range")
	ErrShardLimit   = errors.New("shard count exceeds the limit")
	ErrBadPlan      = errors.New("proxy plan is not usable")
	ErrBadBudget    = errors.New("generation budget or disk figure is invalid")
	ErrUnknownProxy = errors.New("proxy is unknown")
	ErrTenantQuota  = errors.New("tenant concurrency quota reached")
	ErrNotAcquired  = errors.New("tenant has no acquired slot")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// ProxyLimits bounds the generator.
type ProxyLimits struct {
	MaxConcurrent   int
	DiskBudgetBytes int64
	MaxShards       int
	ShardMS         int64
}

// Asset is one source clip.
type Asset struct {
	ID              string
	Tenant          string
	Width           int
	Height          int
	FPSNum          int
	FPSDen          int
	TimebaseNum     int
	TimebaseDen     int
	StartTimecodeMS int64
	DurationMS      int64
	ContentHash     string
	MetadataHash    string
	Bytes           int64
}

// ProxyPlan is the resolved generation plan for one asset.
type ProxyPlan struct {
	ProxyID          string
	AssetID          string
	Tenant           string
	Tier             string
	Width            int
	Height           int
	Shards           int
	ShardMS          int64
	TimebaseNum      int
	TimebaseDen      int
	TimecodeOffsetMS int64
	Params           string
	ContentHash      string
}

// ProxyRef is a proxy as recorded on disk.
type ProxyRef struct {
	ID               string
	AssetID          string
	ShardsTotal      int
	ShardsDone       []int
	ContentHash      string
	TimecodeOffsetMS int64
	DiskBytes        int64
}

// Progress is the generation state of one proxy.
type Progress struct {
	ProxyID   string
	Done      int
	Total     int
	Skipped   int
	DiskBytes int64
	Suspended bool
	Reason    string
}

// VerifyResult is the outcome of verifying one proxy.
type VerifyResult struct {
	OK            bool
	ContentMatch  bool
	TimecodeOK    bool
	MissingShards []int
}

// ProxyGenerator owns the plans, the progress table and the tenant slots.
type ProxyGenerator struct {
	limits ProxyLimits
}

// NewProxyGenerator builds a generator for the given limits.
func NewProxyGenerator(limits ProxyLimits) *ProxyGenerator {
	return &ProxyGenerator{limits: limits}
}

// Plan resolves the proxy plan of one asset.
func (g *ProxyGenerator) Plan(asset Asset) (ProxyPlan, error) {
	return ProxyPlan{}, errNotImplemented
}

// Shards reports how many shards one asset needs.
func (g *ProxyGenerator) Shards(asset Asset) (int, error) {
	return 0, errNotImplemented
}

// Generate advances generation by at most budget shards.
func (g *ProxyGenerator) Generate(plan ProxyPlan, budget int) (Progress, error) {
	return Progress{}, errNotImplemented
}

// Verify checks a proxy against its stored plan.
func (g *ProxyGenerator) Verify(proxy ProxyRef) (VerifyResult, error) {
	return VerifyResult{}, errNotImplemented
}

// Resume continues a proxy from the shards already on disk.
func (g *ProxyGenerator) Resume(proxy ProxyRef) (Progress, error) {
	return Progress{}, errNotImplemented
}

// ProgressOf reads the progress of one proxy.
func (g *ProxyGenerator) ProgressOf(proxyID string) (Progress, bool) {
	return Progress{}, false
}

// Status reports the status string of one proxy.
func (g *ProxyGenerator) Status(proxyID string) string {
	return ""
}

// Acquire takes one concurrency slot for a tenant.
func (g *ProxyGenerator) Acquire(tenant string) error {
	return errNotImplemented
}

// Release returns one concurrency slot for a tenant.
func (g *ProxyGenerator) Release(tenant string) error {
	return errNotImplemented
}

// SetDiskFree records the free disk space in bytes.
func (g *ProxyGenerator) SetDiskFree(bytes int64) error {
	return errNotImplemented
}

// Snapshot serialises the generator state.
func (g *ProxyGenerator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the generator state with a snapshot, all or nothing.
func (g *ProxyGenerator) Restore(data []byte) error {
	return errNotImplemented
}

// Limits reports the effective limits.
func (g *ProxyGenerator) Limits() ProxyLimits {
	return ProxyLimits{}
}
