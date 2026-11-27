// Package task keeps the swap accounting of every tenant and workload and
// reclaims pages when a tenant walks towards its soft limit.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reservation states.
const (
	ReservationPending   = "pending"
	ReservationCommitted = "committed"
	ReservationReleased  = "released"
)

// Errors reported by the governor.
var (
	ErrInvalidTenant      = errors.New("tenant name is not usable")
	ErrInvalidWorkload    = errors.New("workload name is not usable")
	ErrInvalidPages       = errors.New("page count is not usable")
	ErrInvalidQuota       = errors.New("quota is not usable")
	ErrUnknownTenant      = errors.New("unknown tenant")
	ErrTenantLimit        = errors.New("tenant limit reached")
	ErrQuotaExceeded      = errors.New("swap hard limit exceeded")
	ErrPendingLimit       = errors.New("pending reservation limit reached")
	ErrUnknownReservation = errors.New("unknown reservation")
	ErrReservationState   = errors.New("reservation is not in a usable state")
	ErrReclaimFailed      = errors.New("reclaim did not reach the soft limit")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// TenantQuota is the per tenant ceiling. Both limits are counted in pages.
type TenantQuota struct {
	HardPages int64
	SoftPages int64
	Priority  int
}

// Reservation is one request for swap pages that has not been released yet.
type Reservation struct {
	ID         string
	Tenant     string
	Workload   string
	Pages      int64
	State      string
	Generation int64
	Reclaimed  int64
}

// WorkloadUsage is the committed bill of one workload.
type WorkloadUsage struct {
	Workload string
	Pages    int64
	Failures int
}

// UsageSnapshot is the bill of one tenant at a single moment.
type UsageSnapshot struct {
	Tenant       string
	Committed    int64
	Pending      int64
	HardPages    int64
	SoftPages    int64
	SoftExceeded bool
	Generation   int64
	Workloads    []WorkloadUsage
}

// ReclaimReport is the outcome of one reclaim pass.
type ReclaimReport struct {
	Tenant    string
	Reclaimed int64
	Scanned   []string
	Failed    []string
}

// QuotaConfig carries the global bounds of the governor.
type QuotaConfig struct {
	MaxTenants        int
	MaxPending        int
	ReclaimBatchPages int64
	ThrottleWindow    int
}

type workloadState struct {
	pages    int64
	failures int
}

type tenantState struct {
	quota          TenantQuota
	generation     int64
	committed      int64
	pending        int64
	workloads      map[string]*workloadState
	reservationIDs map[string]struct{}
	throttleUntil  time.Time
}

// SwapQuotaGovernor owns the per tenant bills and the reservation table.
type SwapQuotaGovernor struct {
	mu  sync.RWMutex
	cfg QuotaConfig

	tenants        map[string]*tenantState
	reservations   map[string]*Reservation
	nextID         uint64
	nextGeneration int64
	pendingCount   int
}

// NewSwapQuotaGovernor builds a governor around the given config.
func NewSwapQuotaGovernor(cfg QuotaConfig) *SwapQuotaGovernor {
	if cfg.MaxTenants < 0 {
		cfg.MaxTenants = 0
	}
	if cfg.MaxPending < 0 {
		cfg.MaxPending = 0
	}
	if cfg.ReclaimBatchPages < 0 {
		cfg.ReclaimBatchPages = 0
	}
	if cfg.ThrottleWindow < 0 {
		cfg.ThrottleWindow = 0
	}
	return &SwapQuotaGovernor{
		cfg:            cfg,
		tenants:        make(map[string]*tenantState),
		reservations:   make(map[string]*Reservation),
		nextID:         1,
		nextGeneration: 1,
	}
}

func blankName(name string) bool {
	return strings.TrimSpace(name) == ""
}

func validQuota(q TenantQuota) bool {
	return q.HardPages > 0 && q.SoftPages > 0 && q.SoftPages <= q.HardPages
}

// SetQuota creates or replaces the quota of one tenant.
func (g *SwapQuotaGovernor) SetQuota(tenant string, q TenantQuota) error {
	if blankName(tenant) {
		return ErrInvalidTenant
	}
	if !validQuota(q) {
		return ErrInvalidQuota
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if t, ok := g.tenants[tenant]; ok {
		t.quota = q
		return nil
	}
	if len(g.tenants) >= g.cfg.MaxTenants {
		return ErrTenantLimit
	}

	g.tenants[tenant] = &tenantState{
		quota:          q,
		generation:     g.nextGeneration,
		workloads:      make(map[string]*workloadState),
		reservationIDs: make(map[string]struct{}),
	}
	g.nextGeneration++
	return nil
}

// Reserve books pages for a workload without counting them as committed.
func (g *SwapQuotaGovernor) Reserve(tenant, workload string, pages int64) (Reservation, error) {
	if blankName(tenant) {
		return Reservation{}, ErrInvalidTenant
	}
	if blankName(workload) {
		return Reservation{}, ErrInvalidWorkload
	}
	if pages <= 0 {
		return Reservation{}, ErrInvalidPages
	}

	for {
		g.mu.Lock()
		t, ok := g.tenants[tenant]
		if !ok {
			g.mu.Unlock()
			return Reservation{}, ErrUnknownTenant
		}
		if g.pendingCount >= g.cfg.MaxPending {
			g.mu.Unlock()
			return Reservation{}, ErrPendingLimit
		}

		total := t.committed + t.pending
		if total > t.quota.HardPages || pages > t.quota.HardPages-total {
			g.mu.Unlock()
			return Reservation{}, ErrQuotaExceeded
		}

		projected := total + pages
		if projected > t.quota.SoftPages && g.cfg.ThrottleWindow > 0 && !t.throttleUntil.IsZero() {
			wait := time.Until(t.throttleUntil)
			if wait > 0 {
				g.mu.Unlock()
				timer := time.NewTimer(wait)
				<-timer.C
				continue
			}
		}

		reclaimed := int64(0)
		if projected > t.quota.SoftPages {
			affected := g.positiveWorkloadNamesLocked(t)
			reclaimed = g.reclaimLocked(t, projected-t.quota.SoftPages)
			if g.cfg.ThrottleWindow > 0 && reclaimed > 0 {
				t.throttleUntil = time.Now().Add(time.Duration(g.cfg.ThrottleWindow) * time.Millisecond)
			}
			if t.committed+t.pending+pages > t.quota.SoftPages {
				g.incrementFailuresLocked(t, affected)
			}
		}

		id := g.allocateReservationIDLocked()
		reservation := Reservation{
			ID:         id,
			Tenant:     tenant,
			Workload:   workload,
			Pages:      pages,
			State:      ReservationPending,
			Generation: t.generation,
			Reclaimed:  reclaimed,
		}
		g.reservations[id] = &reservation
		t.reservationIDs[id] = struct{}{}
		t.pending += pages
		g.pendingCount++
		g.mu.Unlock()
		return reservation, nil
	}
}

// Commit turns a pending reservation into committed swap usage.
func (g *SwapQuotaGovernor) Commit(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	r, ok := g.reservations[id]
	if !ok {
		return ErrUnknownReservation
	}
	if r.State != ReservationPending {
		return ErrReservationState
	}
	t, ok := g.tenants[r.Tenant]
	if !ok || t.generation != r.Generation {
		return ErrUnknownReservation
	}

	w := t.workloads[r.Workload]
	if w == nil {
		w = &workloadState{}
		t.workloads[r.Workload] = w
	}
	w.pages += r.Pages
	t.pending -= r.Pages
	t.committed += r.Pages
	g.pendingCount--
	r.State = ReservationCommitted
	return nil
}

// Release gives booked pages back, whether they were committed or not.
func (g *SwapQuotaGovernor) Release(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	r, ok := g.reservations[id]
	if !ok {
		return ErrUnknownReservation
	}
	if r.State == ReservationReleased {
		return ErrReservationState
	}

	t, ok := g.tenants[r.Tenant]
	if !ok || t.generation != r.Generation {
		return ErrUnknownReservation
	}

	switch r.State {
	case ReservationPending:
		t.pending -= r.Pages
		g.pendingCount--
	case ReservationCommitted:
		w := t.workloads[r.Workload]
		if w != nil {
			deduct := r.Pages
			if deduct > w.pages {
				deduct = w.pages
			}
			w.pages -= deduct
			t.committed -= deduct
		}
	default:
		return ErrReservationState
	}
	r.State = ReservationReleased
	return nil
}

// Reclaim frees committed pages of one tenant up to the configured batch.
func (g *SwapQuotaGovernor) Reclaim(tenant string) (ReclaimReport, error) {
	if blankName(tenant) {
		return ReclaimReport{}, ErrInvalidTenant
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	t, ok := g.tenants[tenant]
	if !ok {
		return ReclaimReport{}, ErrUnknownTenant
	}

	report := ReclaimReport{Tenant: tenant, Scanned: g.workloadNamesLocked(t)}
	target := t.committed + t.pending - t.quota.SoftPages
	if target <= 0 {
		return report, nil
	}

	affected := g.positiveWorkloadNamesLocked(t)
	report.Reclaimed = g.reclaimLocked(t, target)
	if g.cfg.ThrottleWindow > 0 && report.Reclaimed > 0 {
		t.throttleUntil = time.Now().Add(time.Duration(g.cfg.ThrottleWindow) * time.Millisecond)
	}
	if t.committed+t.pending > t.quota.SoftPages {
		report.Failed = g.incrementFailuresLocked(t, affected)
		return report, ErrReclaimFailed
	}
	return report, nil
}

// Usage reads the bill of one tenant as a single moment snapshot.
func (g *SwapQuotaGovernor) Usage(tenant string) (UsageSnapshot, error) {
	if blankName(tenant) {
		return UsageSnapshot{}, ErrInvalidTenant
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	t, ok := g.tenants[tenant]
	if !ok {
		return UsageSnapshot{}, ErrUnknownTenant
	}

	names := g.workloadNamesLocked(t)
	workloads := make([]WorkloadUsage, 0, len(names))
	for _, name := range names {
		w := t.workloads[name]
		workloads = append(workloads, WorkloadUsage{
			Workload: name,
			Pages:    w.pages,
			Failures: w.failures,
		})
	}
	return UsageSnapshot{
		Tenant:       tenant,
		Committed:    t.committed,
		Pending:      t.pending,
		HardPages:    t.quota.HardPages,
		SoftPages:    t.quota.SoftPages,
		SoftExceeded: t.committed+t.pending > t.quota.SoftPages,
		Generation:   t.generation,
		Workloads:    workloads,
	}, nil
}

// RemoveTenant drops a tenant together with its bills and reservations.
func (g *SwapQuotaGovernor) RemoveTenant(tenant string) error {
	if blankName(tenant) {
		return ErrInvalidTenant
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	t, ok := g.tenants[tenant]
	if !ok {
		return ErrUnknownTenant
	}
	for id := range t.reservationIDs {
		if r, exists := g.reservations[id]; exists && r.State == ReservationPending {
			g.pendingCount--
		}
		delete(g.reservations, id)
	}
	delete(g.tenants, tenant)
	return nil
}

// Tenants lists the known tenants ordered by name.
func (g *SwapQuotaGovernor) Tenants() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	tenants := make([]string, 0, len(g.tenants))
	for tenant := range g.tenants {
		tenants = append(tenants, tenant)
	}
	sort.Strings(tenants)
	return tenants
}

// PendingCount reports how many reservations are still waiting for a commit.
func (g *SwapQuotaGovernor) PendingCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.pendingCount
}

const snapshotVersion = 1

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotState struct {
	NextID         uint64           `json:"next_id"`
	NextGeneration int64            `json:"next_generation"`
	Tenants        []snapshotTenant `json:"tenants"`
	Reservations   []Reservation    `json:"reservations"`
}

type snapshotTenant struct {
	Tenant        string          `json:"tenant"`
	Quota         TenantQuota     `json:"quota"`
	Generation    int64           `json:"generation"`
	Committed     int64           `json:"committed"`
	Pending       int64           `json:"pending"`
	ThrottleUntil int64           `json:"throttle_until,omitempty"`
	Workloads     []WorkloadUsage `json:"workloads"`
}

// Snapshot serialises the tenants, the bills and the reservation table.
func (g *SwapQuotaGovernor) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	state := snapshotState{
		NextID:         g.nextID,
		NextGeneration: g.nextGeneration,
		Tenants:        make([]snapshotTenant, 0, len(g.tenants)),
		Reservations:   make([]Reservation, 0, len(g.reservations)),
	}

	tenantNames := make([]string, 0, len(g.tenants))
	for tenant := range g.tenants {
		tenantNames = append(tenantNames, tenant)
	}
	sort.Strings(tenantNames)
	for _, tenant := range tenantNames {
		t := g.tenants[tenant]
		workloads := make([]WorkloadUsage, 0, len(t.workloads))
		for _, name := range g.workloadNamesLocked(t) {
			w := t.workloads[name]
			workloads = append(workloads, WorkloadUsage{
				Workload: name,
				Pages:    w.pages,
				Failures: w.failures,
			})
		}
		throttleUntil := int64(0)
		if !t.throttleUntil.IsZero() {
			throttleUntil = t.throttleUntil.UnixNano()
		}
		state.Tenants = append(state.Tenants, snapshotTenant{
			Tenant:        tenant,
			Quota:         t.quota,
			Generation:    t.generation,
			Committed:     t.committed,
			Pending:       t.pending,
			ThrottleUntil: throttleUntil,
			Workloads:     workloads,
		})
	}

	reservationIDs := make([]string, 0, len(g.reservations))
	for id := range g.reservations {
		reservationIDs = append(reservationIDs, id)
	}
	sort.Strings(reservationIDs)
	for _, id := range reservationIDs {
		state.Reservations = append(state.Reservations, *g.reservations[id])
	}

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope, err := json.Marshal(snapshotEnvelope{
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *SwapQuotaGovernor) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if !strings.EqualFold(envelope.Checksum, hex.EncodeToString(sum[:])) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil {
		return ErrBadSnapshot
	}
	tenants, reservations, pendingCount, err := g.decodeSnapshot(state)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	g.tenants = tenants
	g.reservations = reservations
	g.nextID = state.NextID
	g.nextGeneration = state.NextGeneration
	g.pendingCount = pendingCount
	g.mu.Unlock()
	return nil
}

func (g *SwapQuotaGovernor) decodeSnapshot(state snapshotState) (map[string]*tenantState, map[string]*Reservation, int, error) {
	if state.NextID == 0 || state.NextGeneration <= 0 || len(state.Tenants) > g.cfg.MaxTenants {
		return nil, nil, 0, ErrBadSnapshot
	}

	tenants := make(map[string]*tenantState, len(state.Tenants))
	maxGeneration := int64(0)
	for _, st := range state.Tenants {
		if blankName(st.Tenant) || !validQuota(st.Quota) || st.Generation <= 0 {
			return nil, nil, 0, ErrBadSnapshot
		}
		if _, exists := tenants[st.Tenant]; exists {
			return nil, nil, 0, ErrBadSnapshot
		}
		if st.Committed < 0 || st.Pending < 0 || st.ThrottleUntil < 0 {
			return nil, nil, 0, ErrBadSnapshot
		}
		if st.Committed > int64(^uint64(0)>>1)-st.Pending {
			return nil, nil, 0, ErrBadSnapshot
		}
		t := &tenantState{
			quota:          st.Quota,
			generation:     st.Generation,
			committed:      st.Committed,
			pending:        st.Pending,
			workloads:      make(map[string]*workloadState, len(st.Workloads)),
			reservationIDs: make(map[string]struct{}),
		}
		if st.ThrottleUntil > 0 {
			t.throttleUntil = time.Unix(0, st.ThrottleUntil)
		}
		var workloadPages int64
		for _, wu := range st.Workloads {
			if blankName(wu.Workload) || wu.Pages < 0 || wu.Failures < 0 {
				return nil, nil, 0, ErrBadSnapshot
			}
			if _, exists := t.workloads[wu.Workload]; exists {
				return nil, nil, 0, ErrBadSnapshot
			}
			if workloadPages > int64(^uint64(0)>>1)-wu.Pages {
				return nil, nil, 0, ErrBadSnapshot
			}
			t.workloads[wu.Workload] = &workloadState{pages: wu.Pages, failures: wu.Failures}
			workloadPages += wu.Pages
		}
		if workloadPages != st.Committed {
			return nil, nil, 0, ErrBadSnapshot
		}
		tenants[st.Tenant] = t
		if st.Generation > maxGeneration {
			maxGeneration = st.Generation
		}
	}
	if state.NextGeneration <= maxGeneration {
		return nil, nil, 0, ErrBadSnapshot
	}

	reservations := make(map[string]*Reservation, len(state.Reservations))
	pendingByTenant := make(map[string]int64)
	pendingCount := 0
	for i := range state.Reservations {
		r := state.Reservations[i]
		if blankName(r.ID) || blankName(r.Tenant) || blankName(r.Workload) || r.Pages <= 0 || r.Reclaimed < 0 {
			return nil, nil, 0, ErrBadSnapshot
		}
		if r.State != ReservationPending && r.State != ReservationCommitted && r.State != ReservationReleased {
			return nil, nil, 0, ErrBadSnapshot
		}
		if _, exists := reservations[r.ID]; exists {
			return nil, nil, 0, ErrBadSnapshot
		}
		t, exists := tenants[r.Tenant]
		if !exists || t.generation != r.Generation {
			return nil, nil, 0, ErrBadSnapshot
		}
		copy := r
		reservations[r.ID] = &copy
		t.reservationIDs[r.ID] = struct{}{}
		if r.State == ReservationPending {
			if pendingByTenant[r.Tenant] > int64(^uint64(0)>>1)-r.Pages {
				return nil, nil, 0, ErrBadSnapshot
			}
			pendingByTenant[r.Tenant] += r.Pages
			pendingCount++
		}
	}
	if pendingCount > g.cfg.MaxPending {
		return nil, nil, 0, ErrBadSnapshot
	}
	for name, t := range tenants {
		if pendingByTenant[name] != t.pending {
			return nil, nil, 0, ErrBadSnapshot
		}
	}
	return tenants, reservations, pendingCount, nil
}

func (g *SwapQuotaGovernor) workloadNamesLocked(t *tenantState) []string {
	names := make([]string, 0, len(t.workloads))
	for name := range t.workloads {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (g *SwapQuotaGovernor) allocateReservationIDLocked() string {
	for {
		id := "res-" + strconv.FormatUint(g.nextID, 10)
		g.nextID++
		if _, exists := g.reservations[id]; !exists {
			return id
		}
	}
}

func (g *SwapQuotaGovernor) reclaimLocked(t *tenantState, requested int64) int64 {
	if requested <= 0 || g.cfg.ReclaimBatchPages <= 0 || t.committed <= 0 {
		return 0
	}
	limit := requested
	if limit > g.cfg.ReclaimBatchPages {
		limit = g.cfg.ReclaimBatchPages
	}
	if limit > t.committed {
		limit = t.committed
	}
	reclaimed := int64(0)
	for _, name := range g.workloadNamesLocked(t) {
		if limit == 0 {
			break
		}
		w := t.workloads[name]
		if w.pages == 0 {
			continue
		}
		take := limit
		if take > w.pages {
			take = w.pages
		}
		w.pages -= take
		t.committed -= take
		reclaimed += take
		limit -= take
	}
	return reclaimed
}

func (g *SwapQuotaGovernor) positiveWorkloadNamesLocked(t *tenantState) []string {
	names := make([]string, 0)
	for _, name := range g.workloadNamesLocked(t) {
		if t.workloads[name].pages > 0 {
			names = append(names, name)
		}
	}
	return names
}

func (g *SwapQuotaGovernor) incrementFailuresLocked(t *tenantState, names []string) []string {
	failed := make([]string, 0)
	for _, name := range names {
		w := t.workloads[name]
		w.failures++
		failed = append(failed, name)
	}
	return failed
}
