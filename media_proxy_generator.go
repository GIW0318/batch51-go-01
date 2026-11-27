// Package task plans, generates, verifies and resumes proxy media for source
// assets, keeping the source time base and timecode offset intact.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

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

// ProxyPlan is the resolved generation plan of one asset.
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

// proxyState tracks the per-shard progress of one proxy.
type proxyState struct {
	total     int
	done      map[int]bool
	skipped   int
	diskBytes int64
	suspended bool
	reason    string
}

func (s *proxyState) doneCount() int { return len(s.done) }

// ProxyGenerator owns the plans, the progress table and the tenant slots.
type ProxyGenerator struct {
	mu       sync.Mutex
	limits   ProxyLimits
	plans    map[string]ProxyPlan
	states   map[string]*proxyState
	tenants  map[string]int
	diskFree int64
	diskUsed int64
}

func normalizeLimits(limits ProxyLimits) ProxyLimits {
	if limits.MaxConcurrent <= 0 {
		limits.MaxConcurrent = DefaultMaxConcurrent
	}
	if limits.DiskBudgetBytes <= 0 {
		limits.DiskBudgetBytes = DefaultDiskBudget
	}
	if limits.MaxShards <= 0 {
		limits.MaxShards = DefaultMaxShards
	}
	if limits.ShardMS <= 0 {
		limits.ShardMS = DefaultShardMS
	}
	return limits
}

// NewProxyGenerator builds a generator for the given limits.
func NewProxyGenerator(limits ProxyLimits) *ProxyGenerator {
	limits = normalizeLimits(limits)
	return &ProxyGenerator{
		limits:   limits,
		plans:    map[string]ProxyPlan{},
		states:   map[string]*proxyState{},
		tenants:  map[string]int{},
		diskFree: limits.DiskBudgetBytes,
	}
}

func validateAsset(asset Asset) error {
	if asset.ID == "" || asset.Tenant == "" || asset.ContentHash == "" {
		return ErrBadAsset
	}
	if asset.Width <= 0 || asset.Height <= 0 || asset.DurationMS <= 0 {
		return ErrBadAsset
	}
	if asset.FPSNum <= 0 || asset.FPSDen <= 0 {
		return ErrBadAsset
	}
	if asset.TimebaseNum <= 0 || asset.TimebaseDen <= 0 {
		return ErrBadAsset
	}
	return nil
}

func tierForHeight(height int) (string, int) {
	switch {
	case height >= 1080:
		return TierHigh, 1080
	case height >= 720:
		return TierMid, 720
	default:
		return TierLow, 480
	}
}

func (g *ProxyGenerator) shardCount(asset Asset) (int, error) {
	if err := validateAsset(asset); err != nil {
		return 0, err
	}
	n := int((asset.DurationMS + g.limits.ShardMS - 1) / g.limits.ShardMS)
	if n > g.limits.MaxShards {
		return 0, ErrShardLimit
	}
	return n, nil
}

// Plan resolves the proxy plan of one asset.
func (g *ProxyGenerator) Plan(asset Asset) (ProxyPlan, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	shards, err := g.shardCount(asset)
	if err != nil {
		return ProxyPlan{}, err
	}
	tier, tierHeight := tierForHeight(asset.Height)
	width := asset.Width * tierHeight / asset.Height
	if width%2 != 0 {
		width++
	}
	plan := ProxyPlan{
		ProxyID:          fmt.Sprintf("%s-%s", asset.ID, tier),
		AssetID:          asset.ID,
		Tenant:           asset.Tenant,
		Tier:             tier,
		Width:            width,
		Height:           tierHeight,
		Shards:           shards,
		ShardMS:          g.limits.ShardMS,
		TimebaseNum:      asset.TimebaseNum,
		TimebaseDen:      asset.TimebaseDen,
		TimecodeOffsetMS: asset.StartTimecodeMS,
		Params: fmt.Sprintf(
			"tier=%s;height=%d;fps=%d/%d;timebase=%d/%d;offset_ms=%d;vfr=preserve",
			tier, tierHeight, asset.FPSNum, asset.FPSDen,
			asset.TimebaseNum, asset.TimebaseDen, asset.StartTimecodeMS,
		),
		ContentHash: asset.ContentHash,
	}
	g.plans[plan.ProxyID] = plan
	if _, ok := g.states[plan.ProxyID]; !ok {
		g.states[plan.ProxyID] = &proxyState{total: shards, done: map[int]bool{}}
	}
	return plan, nil
}

// Shards reports how many shards one asset needs.
func (g *ProxyGenerator) Shards(asset Asset) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shardCount(asset)
}

// shardCost estimates the bytes one generated shard writes to disk.
func shardCost(plan ProxyPlan) int64 {
	var bitrate int64
	switch plan.Tier {
	case TierHigh:
		bitrate = 8_000_000
	case TierMid:
		bitrate = 4_000_000
	default:
		bitrate = 1_500_000
	}
	cost := bitrate / 8 * plan.ShardMS / 1000
	if cost <= 0 {
		cost = 1
	}
	return cost
}

func (g *ProxyGenerator) progressOfLocked(proxyID string, st *proxyState) Progress {
	return Progress{
		ProxyID:   proxyID,
		Done:      st.doneCount(),
		Total:     st.total,
		Skipped:   st.skipped,
		DiskBytes: st.diskBytes,
		Suspended: st.suspended,
		Reason:    st.reason,
	}
}

// Generate advances generation by at most budget shards.
func (g *ProxyGenerator) Generate(plan ProxyPlan, budget int) (Progress, error) {
	if budget <= 0 {
		return Progress{}, ErrBadBudget
	}
	if plan.ProxyID == "" || plan.Shards <= 0 {
		return Progress{}, ErrBadPlan
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	st, ok := g.states[plan.ProxyID]
	if !ok {
		st = &proxyState{total: plan.Shards, done: map[int]bool{}}
		g.states[plan.ProxyID] = st
	}
	if _, ok := g.plans[plan.ProxyID]; !ok {
		g.plans[plan.ProxyID] = plan
	}

	st.suspended = false
	st.reason = ""
	cost := shardCost(plan)
	advanced := 0
	for advanced < budget && st.doneCount() < st.total {
		if g.diskFree < cost {
			st.suspended = true
			st.reason = "disk budget exhausted"
			break
		}
		next := -1
		for i := 0; i < st.total; i++ {
			if !st.done[i] {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		st.done[next] = true
		st.diskBytes += cost
		g.diskUsed += cost
		g.diskFree -= cost
		advanced++
	}
	return g.progressOfLocked(plan.ProxyID, st), nil
}

// Verify checks a proxy against its stored plan.
func (g *ProxyGenerator) Verify(proxy ProxyRef) (VerifyResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	plan, ok := g.plans[proxy.ID]
	if !ok {
		return VerifyResult{}, ErrUnknownProxy
	}
	done := make(map[int]bool, len(proxy.ShardsDone))
	for _, idx := range proxy.ShardsDone {
		done[idx] = true
	}
	missing := []int{}
	for i := 0; i < plan.Shards; i++ {
		if !done[i] {
			missing = append(missing, i)
		}
	}
	res := VerifyResult{
		ContentMatch:  proxy.ContentHash == plan.ContentHash,
		TimecodeOK:    proxy.TimecodeOffsetMS == plan.TimecodeOffsetMS,
		MissingShards: missing,
	}
	res.OK = res.ContentMatch && res.TimecodeOK && len(missing) == 0
	return res, nil
}

// Resume continues a proxy from the shards already on disk.
func (g *ProxyGenerator) Resume(proxy ProxyRef) (Progress, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	st, ok := g.states[proxy.ID]
	if !ok {
		return Progress{}, ErrUnknownProxy
	}
	for _, idx := range proxy.ShardsDone {
		if idx < 0 || idx >= st.total || st.done[idx] {
			continue
		}
		st.done[idx] = true
		st.skipped++
	}
	st.suspended = false
	st.reason = ""
	return g.progressOfLocked(proxy.ID, st), nil
}

// ProgressOf reads the progress of one proxy.
func (g *ProxyGenerator) ProgressOf(proxyID string) (Progress, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.states[proxyID]
	if !ok {
		return Progress{}, false
	}
	return g.progressOfLocked(proxyID, st), true
}

// Status reports the status string of one proxy.
func (g *ProxyGenerator) Status(proxyID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.states[proxyID]
	if !ok {
		return StatusUnknown
	}
	switch {
	case st.doneCount() >= st.total:
		return StatusComplete
	case st.suspended:
		return StatusSuspended
	case st.doneCount() == 0:
		return StatusPending
	default:
		return StatusRunning
	}
}

// Acquire takes one concurrency slot for a tenant.
func (g *ProxyGenerator) Acquire(tenant string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tenants[tenant] >= g.limits.MaxConcurrent {
		return ErrTenantQuota
	}
	g.tenants[tenant]++
	return nil
}

// Release returns one concurrency slot for a tenant.
func (g *ProxyGenerator) Release(tenant string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tenants[tenant] <= 0 {
		return ErrNotAcquired
	}
	g.tenants[tenant]--
	return nil
}

// SetDiskFree records the free disk space in bytes.
func (g *ProxyGenerator) SetDiskFree(bytes int64) error {
	if bytes < 0 {
		return ErrBadBudget
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.diskFree = bytes
	return nil
}

const snapshotVersion = 1

type snapshotState struct {
	Total     int    `json:"total"`
	Done      []int  `json:"done"`
	Skipped   int    `json:"skipped"`
	DiskBytes int64  `json:"disk_bytes"`
	Suspended bool   `json:"suspended"`
	Reason    string `json:"reason"`
}

type snapshot struct {
	Version  int                      `json:"version"`
	Limits   ProxyLimits              `json:"limits"`
	Plans    map[string]ProxyPlan     `json:"plans"`
	Progress map[string]snapshotState `json:"progress"`
	Tenants  map[string]int           `json:"tenants"`
	DiskFree int64                    `json:"disk_free"`
	DiskUsed int64                    `json:"disk_used"`
}

// Snapshot serialises the generator state.
func (g *ProxyGenerator) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	snap := snapshot{
		Version:  snapshotVersion,
		Limits:   g.limits,
		Plans:    make(map[string]ProxyPlan, len(g.plans)),
		Progress: make(map[string]snapshotState, len(g.states)),
		Tenants:  make(map[string]int, len(g.tenants)),
		DiskFree: g.diskFree,
		DiskUsed: g.diskUsed,
	}
	for id, plan := range g.plans {
		snap.Plans[id] = plan
	}
	for id, st := range g.states {
		done := make([]int, 0, len(st.done))
		for idx := range st.done {
			done = append(done, idx)
		}
		sort.Ints(done)
		snap.Progress[id] = snapshotState{
			Total:     st.total,
			Done:      done,
			Skipped:   st.skipped,
			DiskBytes: st.diskBytes,
			Suspended: st.suspended,
			Reason:    st.reason,
		}
	}
	for tenant, n := range g.tenants {
		snap.Tenants[tenant] = n
	}
	return json.Marshal(snap)
}

// Restore replaces the generator state with a snapshot, all or nothing.
func (g *ProxyGenerator) Restore(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	limits := snap.Limits
	if limits.MaxConcurrent <= 0 || limits.DiskBudgetBytes <= 0 ||
		limits.MaxShards <= 0 || limits.ShardMS <= 0 {
		return ErrBadSnapshot
	}
	if snap.DiskFree < 0 || snap.DiskUsed < 0 {
		return ErrBadSnapshot
	}
	if snap.Plans == nil {
		snap.Plans = map[string]ProxyPlan{}
	}
	if snap.Progress == nil {
		snap.Progress = map[string]snapshotState{}
	}
	if snap.Tenants == nil {
		snap.Tenants = map[string]int{}
	}
	states := make(map[string]*proxyState, len(snap.Progress))
	for id, ps := range snap.Progress {
		plan, ok := snap.Plans[id]
		if !ok || ps.Total <= 0 || plan.Shards != ps.Total {
			return ErrBadSnapshot
		}
		st := &proxyState{
			total:     ps.Total,
			done:      make(map[int]bool, len(ps.Done)),
			skipped:   ps.Skipped,
			diskBytes: ps.DiskBytes,
			suspended: ps.Suspended,
			reason:    ps.Reason,
		}
		if st.skipped < 0 || st.diskBytes < 0 {
			return ErrBadSnapshot
		}
		for _, idx := range ps.Done {
			if idx < 0 || idx >= ps.Total || st.done[idx] {
				return ErrBadSnapshot
			}
			st.done[idx] = true
		}
		states[id] = st
	}
	tenants := make(map[string]int, len(snap.Tenants))
	for tenant, n := range snap.Tenants {
		if n < 0 || n > limits.MaxConcurrent {
			return ErrBadSnapshot
		}
		tenants[tenant] = n
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.limits = limits
	g.plans = snap.Plans
	g.states = states
	g.tenants = tenants
	g.diskFree = snap.DiskFree
	g.diskUsed = snap.DiskUsed
	return nil
}

// Limits reports the effective limits.
func (g *ProxyGenerator) Limits() ProxyLimits {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limits
}
