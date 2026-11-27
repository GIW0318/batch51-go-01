// Package task keeps the swap accounting of every tenant and workload and
// reclaims pages when a tenant walks towards its soft limit.
package task

import "errors"

// errNotImplemented is returned by behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

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

// SwapQuotaGovernor owns the per tenant bills and the reservation table.
type SwapQuotaGovernor struct {
	cfg QuotaConfig
}

// NewSwapQuotaGovernor builds a governor around the given config.
func NewSwapQuotaGovernor(cfg QuotaConfig) *SwapQuotaGovernor {
	return &SwapQuotaGovernor{cfg: cfg}
}

// SetQuota creates or replaces the quota of one tenant.
func (g *SwapQuotaGovernor) SetQuota(tenant string, q TenantQuota) error {
	return errNotImplemented
}

// Reserve books pages for a workload without counting them as committed.
func (g *SwapQuotaGovernor) Reserve(tenant, workload string, pages int64) (Reservation, error) {
	return Reservation{}, errNotImplemented
}

// Commit turns a pending reservation into committed swap usage.
func (g *SwapQuotaGovernor) Commit(id string) error {
	return errNotImplemented
}

// Release gives booked pages back, whether they were committed or not.
func (g *SwapQuotaGovernor) Release(id string) error {
	return errNotImplemented
}

// Reclaim frees committed pages of one tenant up to the configured batch.
func (g *SwapQuotaGovernor) Reclaim(tenant string) (ReclaimReport, error) {
	return ReclaimReport{}, errNotImplemented
}

// Usage reads the bill of one tenant as a single moment snapshot.
func (g *SwapQuotaGovernor) Usage(tenant string) (UsageSnapshot, error) {
	return UsageSnapshot{}, errNotImplemented
}

// RemoveTenant drops a tenant together with its bills and reservations.
func (g *SwapQuotaGovernor) RemoveTenant(tenant string) error {
	return errNotImplemented
}

// Tenants lists the known tenants ordered by name.
func (g *SwapQuotaGovernor) Tenants() []string {
	return nil
}

// PendingCount reports how many reservations are still waiting for a commit.
func (g *SwapQuotaGovernor) PendingCount() int {
	return 0
}

// Snapshot serialises the tenants, the bills and the reservation table.
func (g *SwapQuotaGovernor) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *SwapQuotaGovernor) Restore(data []byte) error {
	return errNotImplemented
}
