// Package task meters inbound traffic per tenant and per client key. Client
// keys are normalised before they are matched, quotas are accounted per tenant
// and globally, and each key owns a burst allowance plus a refillable steady
// allowance.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Reason codes stored on a decision and on a rejection record.
const (
	ReasonAllowed     = "reason_allowed"
	ReasonRateLimited = "reason_rate_limited"
	ReasonTenantQuota = "reason_tenant_quota"
	ReasonGlobalQuota = "reason_global_quota"
	ReasonBucketLimit = "reason_bucket_limit"
)

// States of one reservation.
const (
	ReservationOpen   = "open"
	ReservationClosed = "closed"
)

// Errors reported by the guard.
var (
	ErrBadConfig          = errors.New("bucket sizes, quotas, caps and refill rate must be positive")
	ErrBlankKey           = errors.New("tenant and client are both required")
	ErrRateLimited        = errors.New("both the burst and the steady allowance of this key are empty")
	ErrTenantQuota        = errors.New("tenant quota is exhausted")
	ErrGlobalQuota        = errors.New("global quota is exhausted")
	ErrBucketLimit        = errors.New("bucket table is full")
	ErrUnknownReservation = errors.New("unknown reservation")
	ErrReservationClosed  = errors.New("reservation already closed")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// LimitConfig is the immutable setup of one guard instance.
type LimitConfig struct {
	BurstTokens  int
	SteadyTokens int
	RefillPerSec int
	TenantQuota  int
	GlobalQuota  int
	MaxBuckets   int
	MaxAudit     int
}

// Reservation is the hold taken by one admitted request.
type Reservation struct {
	ID     string
	Key    string
	Tenant string
	Closed bool
}

// Decision is the explainable outcome of one meter call.
type Decision struct {
	Allowed   bool
	Key       string
	Tenant    string
	Reason    string
	Remaining int
	Seq       int64
}

// QuotaState reports how much of a quota is currently held.
type QuotaState struct {
	Used  int
	Limit int
}

// BucketState is the material free view of one meter.
type BucketState struct {
	Key    string
	Tenant string
	Burst  int
	Steady int
}

// Rejection is one refused attempt.
type Rejection struct {
	Seq    int64
	Tenant string
	Key    string
	Reason string
}

// LimitStats counts the traffic seen by the guard.
type LimitStats struct {
	Requests int
	Allowed  int
	Rejected int
	Buckets  int
	Inflight int
}

// bucket is the per key meter. Burst is spent before Steady.
type bucket struct {
	key        string
	tenant     string
	burst      int
	steady     int
	lastRefill int64
}

// hold is one reservation together with the allowance it consumed.
type hold struct {
	reservation Reservation
	fromBurst   bool
}

// RateLimitBypassGuard owns the bucket table, the quota counters and the
// rejection log.
type RateLimitBypassGuard struct {
	mu        sync.Mutex
	cfg       LimitConfig
	buckets   map[string]*bucket
	holds     map[string]*hold
	tenantUse map[string]int
	globalUse int
	rejects   []Rejection
	stats     LimitStats
	seq       int64
	holdNo    int64
}

// NewRateLimitBypassGuard validates the caps and starts empty.
func NewRateLimitBypassGuard(cfg LimitConfig) (*RateLimitBypassGuard, error) {
	if cfg.BurstTokens < 0 ||
		cfg.SteadyTokens <= 0 ||
		cfg.RefillPerSec <= 0 ||
		cfg.TenantQuota <= 0 ||
		cfg.GlobalQuota <= 0 ||
		cfg.MaxBuckets <= 0 ||
		cfg.MaxAudit <= 0 {
		return nil, ErrBadConfig
	}

	return &RateLimitBypassGuard{
		cfg:       cfg,
		buckets:   make(map[string]*bucket),
		holds:     make(map[string]*hold),
		tenantUse: make(map[string]int),
		rejects:   make([]Rejection, 0, cfg.MaxAudit),
	}, nil
}

// NormalizeKey folds one tenant and client pair into the bucket key.
func (g *RateLimitBypassGuard) NormalizeKey(tenant, client string) (string, error) {
	_, _, key, err := normalizePair(tenant, client)
	return key, err
}

// Reserve takes a quota slot and one token from the client bucket.
func (g *RateLimitBypassGuard) Reserve(tenant, client string, now int64) (Reservation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	out, err := g.reserveLocked(tenant, client, now, true)
	return out.reservation, err
}

// Commit finalises a reservation once the request has been served.
func (g *RateLimitBypassGuard) Commit(res Reservation) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.closeReservationLocked(res.ID, false)
}

// Cancel drops a reservation and hands the token back.
func (g *RateLimitBypassGuard) Cancel(res Reservation) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.closeReservationLocked(res.ID, true)
}

// Allow meters one request end to end: reserve, then commit.
func (g *RateLimitBypassGuard) Allow(tenant, client string, now int64) (Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	out, err := g.reserveLocked(tenant, client, now, false)
	decision := Decision{
		Allowed:   err == nil,
		Key:       out.key,
		Tenant:    out.tenant,
		Reason:    out.reason,
		Remaining: out.remaining,
		Seq:       out.seq,
	}
	if err == nil {
		decision.Reason = ReasonAllowed
	}
	return decision, err
}

// Refill tops every bucket up to its steady allowance.
func (g *RateLimitBypassGuard) Refill(now int64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, b := range g.buckets {
		if now <= b.lastRefill {
			continue
		}

		elapsed := now - b.lastRefill
		missing := g.cfg.SteadyTokens - b.steady
		if missing > 0 {
			secondsNeeded := (int64(missing) + int64(g.cfg.RefillPerSec) - 1) / int64(g.cfg.RefillPerSec)
			if elapsed >= secondsNeeded {
				b.steady = g.cfg.SteadyTokens
			} else {
				b.steady += int(elapsed) * g.cfg.RefillPerSec
			}
		}
		b.lastRefill = now
	}
}

// Quota reports how much of one tenant quota is held.
func (g *RateLimitBypassGuard) Quota(tenant string) QuotaState {
	folded, err := normalizeComponent(tenant)
	if err != nil {
		return QuotaState{Limit: g.cfg.TenantQuota}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	return QuotaState{Used: g.tenantUse[folded], Limit: g.cfg.TenantQuota}
}

// Global reports how much of the global quota is held.
func (g *RateLimitBypassGuard) Global() QuotaState {
	g.mu.Lock()
	defer g.mu.Unlock()

	return QuotaState{Used: g.globalUse, Limit: g.cfg.GlobalQuota}
}

// Buckets lists the meters in ascending key order.
func (g *RateLimitBypassGuard) Buckets() []BucketState {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]BucketState, 0, len(g.buckets))
	for _, b := range g.buckets {
		out = append(out, BucketState{
			Key:    b.key,
			Tenant: b.tenant,
			Burst:  b.burst,
			Steady: b.steady,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Stats returns the running counters.
func (g *RateLimitBypassGuard) Stats() LimitStats {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.stats
}

// Rejections returns a copy of the refusal log, oldest first.
func (g *RateLimitBypassGuard) Rejections() []Rejection {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]Rejection, len(g.rejects))
	copy(out, g.rejects)
	return out
}

const snapshotVersion = 1

type snapshotBucket struct {
	Key        string `json:"key"`
	Tenant     string `json:"tenant"`
	Burst      int    `json:"burst"`
	Steady     int    `json:"steady"`
	LastRefill int64  `json:"last_refill"`
}

type snapshotHold struct {
	Reservation Reservation `json:"reservation"`
	FromBurst   bool        `json:"from_burst"`
}

type guardSnapshot struct {
	Version    int              `json:"version"`
	Buckets    []snapshotBucket `json:"buckets"`
	TenantUse  map[string]int   `json:"tenant_use"`
	GlobalUse  int              `json:"global_use"`
	Holds      []snapshotHold   `json:"holds"`
	Seq        int64            `json:"seq"`
	HoldNo     int64            `json:"hold_no"`
	Rejections []Rejection      `json:"rejections"`
	Stats      LimitStats       `json:"stats"`
}

// Snapshot serialises buckets, quotas, reservations, sequence, log and
// counters.
func (g *RateLimitBypassGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	buckets := make([]snapshotBucket, 0, len(g.buckets))
	for _, b := range g.buckets {
		buckets = append(buckets, snapshotBucket{
			Key:        b.key,
			Tenant:     b.tenant,
			Burst:      b.burst,
			Steady:     b.steady,
			LastRefill: b.lastRefill,
		})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Key < buckets[j].Key })

	holds := make([]snapshotHold, 0, len(g.holds))
	for _, h := range g.holds {
		holds = append(holds, snapshotHold{
			Reservation: h.reservation,
			FromBurst:   h.fromBurst,
		})
	}
	sort.Slice(holds, func(i, j int) bool {
		return holds[i].Reservation.ID < holds[j].Reservation.ID
	})

	tenantUse := make(map[string]int, len(g.tenantUse))
	for tenant, used := range g.tenantUse {
		tenantUse[tenant] = used
	}

	rejections := make([]Rejection, len(g.rejects))
	copy(rejections, g.rejects)
	payload := guardSnapshot{
		Version:    snapshotVersion,
		Buckets:    buckets,
		TenantUse:  tenantUse,
		GlobalUse:  g.globalUse,
		Holds:      holds,
		Seq:        g.seq,
		HoldNo:     g.holdNo,
		Rejections: rejections,
		Stats:      g.stats,
	}
	return json.Marshal(payload)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *RateLimitBypassGuard) Restore(data []byte) error {
	state, err := g.parseSnapshot(data)
	if err != nil {
		return err
	}

	g.mu.Lock()
	g.buckets = state.buckets
	g.holds = state.holds
	g.tenantUse = state.tenantUse
	g.globalUse = state.globalUse
	g.rejects = state.rejects
	g.stats = state.stats
	g.seq = state.seq
	g.holdNo = state.holdNo
	g.mu.Unlock()
	return nil
}

type reserveOutcome struct {
	reservation Reservation
	key         string
	tenant      string
	reason      string
	remaining   int
	seq         int64
}

// reserveLocked is the single critical section for quota checks, token
// consumption and optional reservation registration. Callers must hold mu.
func (g *RateLimitBypassGuard) reserveLocked(tenantRaw, clientRaw string, now int64, register bool) (reserveOutcome, error) {
	tenant, _, key, err := normalizePair(tenantRaw, clientRaw)
	if err != nil {
		return reserveOutcome{}, err
	}

	out := reserveOutcome{key: key, tenant: tenant}
	g.seq++
	out.seq = g.seq
	g.stats.Requests++

	if g.tenantUse[tenant] >= g.cfg.TenantQuota {
		out.reason = ReasonTenantQuota
		out.remaining = g.remainingLocked(key)
		g.rejectLocked(out.seq, tenant, key, out.reason)
		return out, ErrTenantQuota
	}
	if g.globalUse >= g.cfg.GlobalQuota {
		out.reason = ReasonGlobalQuota
		out.remaining = g.remainingLocked(key)
		g.rejectLocked(out.seq, tenant, key, out.reason)
		return out, ErrGlobalQuota
	}

	b := g.buckets[key]
	if b == nil {
		if len(g.buckets) >= g.cfg.MaxBuckets {
			out.reason = ReasonBucketLimit
			g.rejectLocked(out.seq, tenant, key, out.reason)
			return out, ErrBucketLimit
		}
		b = &bucket{
			key:        key,
			tenant:     tenant,
			burst:      g.cfg.BurstTokens,
			steady:     g.cfg.SteadyTokens,
			lastRefill: now,
		}
		g.buckets[key] = b
		g.stats.Buckets++
	}

	fromBurst := false
	switch {
	case b.burst > 0:
		b.burst--
		fromBurst = true
	case b.steady > 0:
		b.steady--
	default:
		out.reason = ReasonRateLimited
		out.remaining = 0
		g.rejectLocked(out.seq, tenant, key, out.reason)
		return out, ErrRateLimited
	}

	out.remaining = b.burst + b.steady
	g.stats.Allowed++

	if !register {
		out.reservation = Reservation{Key: key, Tenant: tenant}
		return out, nil
	}

	g.tenantUse[tenant]++
	g.globalUse++
	g.holdNo++
	id := fmt.Sprintf("hold-%d", g.holdNo)
	reservation := Reservation{ID: id, Key: key, Tenant: tenant}
	g.holds[id] = &hold{reservation: reservation, fromBurst: fromBurst}
	g.stats.Inflight++
	out.reservation = reservation
	return out, nil
}

// rejectLocked records one refused request. Callers must hold mu.
func (g *RateLimitBypassGuard) rejectLocked(seq int64, tenant, key, reason string) {
	g.stats.Rejected++
	g.rejects = append(g.rejects, Rejection{
		Seq:    seq,
		Tenant: tenant,
		Key:    key,
		Reason: reason,
	})
	if len(g.rejects) > g.cfg.MaxAudit {
		copy(g.rejects, g.rejects[len(g.rejects)-g.cfg.MaxAudit:])
		g.rejects = g.rejects[:g.cfg.MaxAudit]
	}
}

// closeReservationLocked closes a hold and optionally returns its token.
// Callers must hold mu.
func (g *RateLimitBypassGuard) closeReservationLocked(id string, returnToken bool) error {
	if id == "" {
		return ErrUnknownReservation
	}
	h, ok := g.holds[id]
	if !ok {
		return ErrUnknownReservation
	}
	if h.reservation.Closed {
		return ErrReservationClosed
	}

	h.reservation.Closed = true
	g.releaseQuotaLocked(h.reservation.Tenant)
	if returnToken {
		b := g.buckets[h.reservation.Key]
		if b != nil {
			if h.fromBurst {
				if b.burst < g.cfg.BurstTokens {
					b.burst++
				}
			} else if b.steady < g.cfg.SteadyTokens {
				b.steady++
			}
		}
	}
	return nil
}

// releaseQuotaLocked releases one open reservation's quota and inflight slot.
// Callers must hold mu.
func (g *RateLimitBypassGuard) releaseQuotaLocked(tenant string) {
	if used := g.tenantUse[tenant]; used > 1 {
		g.tenantUse[tenant] = used - 1
	} else {
		delete(g.tenantUse, tenant)
	}
	if g.globalUse > 0 {
		g.globalUse--
	}
	if g.stats.Inflight > 0 {
		g.stats.Inflight--
	}
}

func (g *RateLimitBypassGuard) remainingLocked(key string) int {
	if b := g.buckets[key]; b != nil {
		return b.burst + b.steady
	}
	return 0
}

func normalizePair(tenantRaw, clientRaw string) (string, string, string, error) {
	tenant, err := normalizeComponent(tenantRaw)
	if err != nil {
		return "", "", "", err
	}
	client, err := normalizeComponent(clientRaw)
	if err != nil {
		return "", "", "", err
	}
	return tenant, client, tenant + "/" + client, nil
}

func normalizeComponent(raw string) (string, error) {
	folded := strings.ToLower(strings.TrimSpace(raw))
	if folded == "" {
		return "", ErrBlankKey
	}
	decoded, err := url.PathUnescape(folded)
	if err != nil {
		return "", fmt.Errorf("percent-decode key: %w", err)
	}
	if strings.TrimSpace(decoded) == "" {
		return "", ErrBlankKey
	}
	return decoded, nil
}

type restoredState struct {
	buckets   map[string]*bucket
	holds     map[string]*hold
	tenantUse map[string]int
	globalUse int
	rejects   []Rejection
	stats     LimitStats
	seq       int64
	holdNo    int64
}

func (g *RateLimitBypassGuard) parseSnapshot(data []byte) (*restoredState, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, ErrBadSnapshot
	}

	var payload guardSnapshot
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if payload.Version != snapshotVersion ||
		payload.Buckets == nil ||
		payload.TenantUse == nil ||
		payload.Holds == nil ||
		payload.Rejections == nil {
		return nil, ErrBadSnapshot
	}
	if payload.GlobalUse < 0 || payload.GlobalUse > g.cfg.GlobalQuota ||
		payload.Seq < 0 || payload.HoldNo < 0 ||
		payload.Stats.Requests < 0 ||
		payload.Stats.Allowed < 0 ||
		payload.Stats.Rejected < 0 ||
		payload.Stats.Buckets < 0 ||
		payload.Stats.Inflight < 0 ||
		payload.Stats.Requests != payload.Stats.Allowed+payload.Stats.Rejected ||
		payload.Stats.Rejected < len(payload.Rejections) ||
		len(payload.Buckets) > g.cfg.MaxBuckets ||
		len(payload.Rejections) > g.cfg.MaxAudit ||
		payload.Stats.Buckets != len(payload.Buckets) ||
		payload.Stats.Allowed < payload.Stats.Inflight {
		return nil, ErrBadSnapshot
	}

	rejections := make([]Rejection, len(payload.Rejections))
	copy(rejections, payload.Rejections)
	state := &restoredState{
		buckets:   make(map[string]*bucket, len(payload.Buckets)),
		holds:     make(map[string]*hold, len(payload.Holds)),
		tenantUse: make(map[string]int, len(payload.TenantUse)),
		globalUse: payload.GlobalUse,
		rejects:   rejections,
		stats:     payload.Stats,
		seq:       payload.Seq,
		holdNo:    payload.HoldNo,
	}

	for _, saved := range payload.Buckets {
		if saved.Key == "" || saved.Tenant == "" ||
			saved.Burst < 0 || saved.Burst > g.cfg.BurstTokens ||
			saved.Steady < 0 || saved.Steady > g.cfg.SteadyTokens ||
			!strings.HasPrefix(saved.Key, saved.Tenant+"/") ||
			len(saved.Key) <= len(saved.Tenant)+1 {
			return nil, ErrBadSnapshot
		}
		if _, exists := state.buckets[saved.Key]; exists {
			return nil, ErrBadSnapshot
		}
		state.buckets[saved.Key] = &bucket{
			key:        saved.Key,
			tenant:     saved.Tenant,
			burst:      saved.Burst,
			steady:     saved.Steady,
			lastRefill: saved.LastRefill,
		}
	}

	for tenant, used := range payload.TenantUse {
		if tenant == "" || used <= 0 || used > g.cfg.TenantQuota {
			return nil, ErrBadSnapshot
		}
		state.tenantUse[tenant] = used
	}

	openByTenant := make(map[string]int)
	openCount := 0
	lastRejectSeq := int64(0)
	for i, record := range state.rejects {
		if record.Seq <= lastRejectSeq || record.Seq > state.seq ||
			record.Tenant == "" || record.Key == "" || record.Reason == "" {
			return nil, ErrBadSnapshot
		}
		if i > 0 && record.Seq <= state.rejects[i-1].Seq {
			return nil, ErrBadSnapshot
		}
		lastRejectSeq = record.Seq
	}

	for _, saved := range payload.Holds {
		reservation := saved.Reservation
		if reservation.ID == "" || reservation.Key == "" || reservation.Tenant == "" {
			return nil, ErrBadSnapshot
		}
		if _, exists := state.holds[reservation.ID]; exists {
			return nil, ErrBadSnapshot
		}
		b := state.buckets[reservation.Key]
		if b == nil || b.tenant != reservation.Tenant {
			return nil, ErrBadSnapshot
		}
		state.holds[reservation.ID] = &hold{
			reservation: reservation,
			fromBurst:   saved.FromBurst,
		}
		if !reservation.Closed {
			openCount++
			openByTenant[reservation.Tenant]++
		}
	}

	if state.globalUse != openCount || payload.Stats.Inflight != openCount {
		return nil, ErrBadSnapshot
	}
	for tenant, used := range state.tenantUse {
		if openByTenant[tenant] != used {
			return nil, ErrBadSnapshot
		}
	}
	for tenant, count := range openByTenant {
		if state.tenantUse[tenant] != count {
			return nil, ErrBadSnapshot
		}
	}

	if payload.Seq < int64(payload.Stats.Requests) {
		return nil, ErrBadSnapshot
	}
	return state, nil
}
