package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-13"
)

func testQuota() impl.QuotaConfig {
	return impl.QuotaConfig{MaxTenants: 8, MaxPending: 32, ReclaimBatchPages: 4, ThrottleWindow: 0}
}

func mustSetQuota(t *testing.T, g *impl.SwapQuotaGovernor, tenant string, hard, soft int64) {
	t.Helper()
	if err := g.SetQuota(tenant, impl.TenantQuota{HardPages: hard, SoftPages: soft, Priority: 10}); err != nil {
		t.Fatalf("set quota for %s: %v", tenant, err)
	}
}

func mustReserve(t *testing.T, g *impl.SwapQuotaGovernor, tenant, workload string, pages int64) impl.Reservation {
	t.Helper()
	r, err := g.Reserve(tenant, workload, pages)
	if err != nil {
		t.Fatalf("reserve %d pages for %s/%s: %v", pages, tenant, workload, err)
	}
	return r
}

func TestSetQuotaCreatesTenant(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(impl.QuotaConfig{MaxTenants: 2, MaxPending: 32, ReclaimBatchPages: 4})
	mustSetQuota(t, g, "tenant-a", 20, 10)
	tenants := g.Tenants()
	if len(tenants) != 1 || tenants[0] != "tenant-a" {
		t.Fatalf("tenants = %v, want [tenant-a]", tenants)
	}
	usage, err := g.Usage("tenant-a")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Tenant != "tenant-a" {
		t.Fatalf("usage tenant = %q, want tenant-a", usage.Tenant)
	}
	if usage.Committed != 0 || usage.Pending != 0 {
		t.Fatalf("fresh bill = %d committed / %d pending, want zeros", usage.Committed, usage.Pending)
	}
	if usage.HardPages != 20 || usage.SoftPages != 10 {
		t.Fatalf("fresh quota = %d/%d, want 20/10", usage.HardPages, usage.SoftPages)
	}
	if usage.SoftExceeded {
		t.Fatalf("a fresh tenant must not be reported as above the soft limit")
	}
	if usage.Generation <= 0 {
		t.Fatalf("tenant generation = %d, want a positive value", usage.Generation)
	}
	if len(usage.Workloads) != 0 {
		t.Fatalf("fresh workload list = %v, want empty", usage.Workloads)
	}
	if got := g.PendingCount(); got != 0 {
		t.Fatalf("fresh pending count = %d, want 0", got)
	}
	mustSetQuota(t, g, "tenant-a2", 10, 5)
	if err := g.SetQuota("tenant-a3", impl.TenantQuota{HardPages: 10, SoftPages: 5}); !errors.Is(err, impl.ErrTenantLimit) {
		t.Fatalf("third tenant must fail with ErrTenantLimit, got %v", err)
	}
	if got := len(g.Tenants()); got != 2 {
		t.Fatalf("tenant count = %d, want the configured cap 2", got)
	}
	if err := g.SetQuota("tenant-a", impl.TenantQuota{HardPages: 30, SoftPages: 20}); err != nil {
		t.Fatalf("replacing the quota of an existing tenant must succeed, got %v", err)
	}
}

func TestSetQuotaRejectsInvalidQuota(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	if err := g.SetQuota("  ", impl.TenantQuota{HardPages: 10, SoftPages: 5}); !errors.Is(err, impl.ErrInvalidTenant) {
		t.Fatalf("blank tenant must fail with ErrInvalidTenant, got %v", err)
	}
	cases := []impl.TenantQuota{
		{HardPages: 0, SoftPages: 0},
		{HardPages: -4, SoftPages: 1},
		{HardPages: 10, SoftPages: 0},
		{HardPages: 10, SoftPages: 11},
	}
	for i, q := range cases {
		if err := g.SetQuota("tenant-x", q); !errors.Is(err, impl.ErrInvalidQuota) {
			t.Fatalf("case %d quota %+v must fail with ErrInvalidQuota, got %v", i, q, err)
		}
	}
	if got := len(g.Tenants()); got != 0 {
		t.Fatalf("rejected quotas left %d tenants, want 0", got)
	}
}

func TestReserveThenCommitMovesPages(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	mustSetQuota(t, g, "tenant-b", 20, 15)
	r := mustReserve(t, g, "tenant-b", "wal", 4)
	if r.State != impl.ReservationPending {
		t.Fatalf("fresh reservation state = %q, want %q", r.State, impl.ReservationPending)
	}
	if r.Pages != 4 || r.Tenant != "tenant-b" || r.Workload != "wal" {
		t.Fatalf("reservation = %+v, want 4 pages for tenant-b/wal", r)
	}
	if r.ID == "" {
		t.Fatalf("reservation id must not be empty")
	}
	if got := g.PendingCount(); got != 1 {
		t.Fatalf("pending count = %d, want 1", got)
	}
	usage, err := g.Usage("tenant-b")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Committed != 0 || usage.Pending != 4 {
		t.Fatalf("bill before commit = %d/%d, want 0 committed and 4 pending", usage.Committed, usage.Pending)
	}
	if err := g.Commit(r.ID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	usage, err = g.Usage("tenant-b")
	if err != nil {
		t.Fatalf("usage after commit: %v", err)
	}
	if usage.Committed != 4 || usage.Pending != 0 {
		t.Fatalf("bill after commit = %d/%d, want 4 committed and 0 pending", usage.Committed, usage.Pending)
	}
	if len(usage.Workloads) != 1 || usage.Workloads[0].Workload != "wal" || usage.Workloads[0].Pages != 4 {
		t.Fatalf("workload bill = %+v, want wal with 4 pages", usage.Workloads)
	}
	if got := g.PendingCount(); got != 0 {
		t.Fatalf("pending count after commit = %d, want 0", got)
	}
}

func TestReserveRejectsInvalidPages(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	mustSetQuota(t, g, "tenant-c", 20, 10)
	for _, pages := range []int64{0, -1, -100} {
		if _, err := g.Reserve("tenant-c", "w", pages); !errors.Is(err, impl.ErrInvalidPages) {
			t.Fatalf("pages %d must fail with ErrInvalidPages, got %v", pages, err)
		}
	}
	if _, err := g.Reserve("tenant-c", "   ", 2); !errors.Is(err, impl.ErrInvalidWorkload) {
		t.Fatalf("blank workload must fail with ErrInvalidWorkload, got %v", err)
	}
	if _, err := g.Reserve("  ", "w", 2); !errors.Is(err, impl.ErrInvalidTenant) {
		t.Fatalf("blank tenant must fail with ErrInvalidTenant, got %v", err)
	}
	if _, err := g.Reserve("tenant-404", "w", 2); !errors.Is(err, impl.ErrUnknownTenant) {
		t.Fatalf("unknown tenant must fail with ErrUnknownTenant, got %v", err)
	}
	if got := g.PendingCount(); got != 0 {
		t.Fatalf("rejected reservations left %d pending, want 0", got)
	}
}

func TestReserveEnforcesHardLimit(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	mustSetQuota(t, g, "tenant-d", 10, 10)
	first := mustReserve(t, g, "tenant-d", "a", 6)
	second := mustReserve(t, g, "tenant-d", "b", 4)
	if _, err := g.Reserve("tenant-d", "c", 1); !errors.Is(err, impl.ErrQuotaExceeded) {
		t.Fatalf("reserve past the hard limit must fail with ErrQuotaExceeded, got %v", err)
	}
	if got := g.PendingCount(); got != 2 {
		t.Fatalf("pending count = %d, want 2 after the rejected reserve", got)
	}
	if err := g.Commit(first.ID); err != nil {
		t.Fatalf("commit first: %v", err)
	}
	if err := g.Commit(second.ID); err != nil {
		t.Fatalf("commit second: %v", err)
	}
	usage, err := g.Usage("tenant-d")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Committed != 10 {
		t.Fatalf("committed pages = %d, want the hard limit 10", usage.Committed)
	}
	if _, err := g.Reserve("tenant-d", "c", 1); !errors.Is(err, impl.ErrQuotaExceeded) {
		t.Fatalf("reserve on a full tenant must fail with ErrQuotaExceeded, got %v", err)
	}
}

func TestReserveEnforcesPendingLimit(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(impl.QuotaConfig{MaxTenants: 4, MaxPending: 2, ReclaimBatchPages: 2})
	mustSetQuota(t, g, "tenant-e", 50, 40)
	mustReserve(t, g, "tenant-e", "a", 1)
	mustReserve(t, g, "tenant-e", "b", 1)
	if _, err := g.Reserve("tenant-e", "c", 1); !errors.Is(err, impl.ErrPendingLimit) {
		t.Fatalf("third pending reservation must fail with ErrPendingLimit, got %v", err)
	}
	if got := g.PendingCount(); got != 2 {
		t.Fatalf("pending count = %d, want 2", got)
	}
}

func TestCommitRejectsUnknownAndReleased(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	mustSetQuota(t, g, "tenant-f", 30, 20)
	if err := g.Commit("res-404"); !errors.Is(err, impl.ErrUnknownReservation) {
		t.Fatalf("unknown reservation must fail with ErrUnknownReservation, got %v", err)
	}
	r := mustReserve(t, g, "tenant-f", "w", 3)
	if err := g.Release(r.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := g.Commit(r.ID); !errors.Is(err, impl.ErrReservationState) {
		t.Fatalf("committing a released reservation must fail with ErrReservationState, got %v", err)
	}
	r2 := mustReserve(t, g, "tenant-f", "w", 3)
	if err := g.Commit(r2.ID); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if err := g.Commit(r2.ID); !errors.Is(err, impl.ErrReservationState) {
		t.Fatalf("second commit must fail with ErrReservationState, got %v", err)
	}
}

func TestReleasePendingReservation(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	mustSetQuota(t, g, "tenant-g", 30, 20)
	r := mustReserve(t, g, "tenant-g", "w", 5)
	if err := g.Release(r.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	usage, err := g.Usage("tenant-g")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Committed != 0 || usage.Pending != 0 {
		t.Fatalf("bill after releasing a pending reservation = %d/%d, want 0/0", usage.Committed, usage.Pending)
	}
	if got := g.PendingCount(); got != 0 {
		t.Fatalf("pending count = %d, want 0", got)
	}
	if err := g.Release(r.ID); !errors.Is(err, impl.ErrReservationState) {
		t.Fatalf("second release must fail with ErrReservationState, got %v", err)
	}
}

func TestReleaseCommittedReservation(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	mustSetQuota(t, g, "tenant-h", 30, 25)
	r := mustReserve(t, g, "tenant-h", "w", 6)
	if err := g.Commit(r.ID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := g.Release(r.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	usage, err := g.Usage("tenant-h")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Committed != 0 {
		t.Fatalf("committed pages after release = %d, want 0", usage.Committed)
	}
	if len(usage.Workloads) != 1 || usage.Workloads[0].Pages != 0 {
		t.Fatalf("workload bill after release = %+v, want w with 0 pages", usage.Workloads)
	}
}

func TestReserveOverSoftTriggersReclaim(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	mustSetQuota(t, g, "tenant-i", 30, 10)
	base := mustReserve(t, g, "tenant-i", "w1", 12)
	if err := g.Commit(base.ID); err != nil {
		t.Fatalf("commit base: %v", err)
	}
	if base.Reclaimed != 0 {
		t.Fatalf("reservation under the soft limit reported %d reclaimed pages, want 0", base.Reclaimed)
	}
	over := mustReserve(t, g, "tenant-i", "w2", 2)
	if over.Reclaimed != 4 {
		t.Fatalf("reservation over the soft limit reclaimed %d pages, want the batch 4", over.Reclaimed)
	}
	usage, err := g.Usage("tenant-i")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Committed != 8 {
		t.Fatalf("committed pages after reclaim = %d, want 12-4 = 8", usage.Committed)
	}
	if usage.Pending != 2 {
		t.Fatalf("pending pages = %d, want the 2 pages of the new reservation", usage.Pending)
	}
	if usage.SoftExceeded {
		t.Fatalf("reclaim should have brought the bill back under the soft limit")
	}
}

func TestReclaimReportsFailureOncePerWorkload(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	mustSetQuota(t, g, "tenant-j", 40, 5)
	r := mustReserve(t, g, "tenant-j", "hot", 20)
	if err := g.Commit(r.ID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	report, err := g.Reclaim("tenant-j")
	if !errors.Is(err, impl.ErrReclaimFailed) {
		t.Fatalf("reclaim that cannot reach the soft limit must fail with ErrReclaimFailed, got %v", err)
	}
	if report.Reclaimed != 4 {
		t.Fatalf("reclaim freed %d pages, want the batch 4", report.Reclaimed)
	}
	if len(report.Failed) != 1 || report.Failed[0] != "hot" {
		t.Fatalf("failed list = %v, want [hot]", report.Failed)
	}
	usage, err := g.Usage("tenant-j")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Committed != 16 {
		t.Fatalf("committed pages = %d, want 20-4 = 16", usage.Committed)
	}
	if usage.Workloads[0].Failures != 1 {
		t.Fatalf("failure counter = %d, want exactly 1 after one failing pass", usage.Workloads[0].Failures)
	}
	if _, err := g.Reclaim("tenant-j"); !errors.Is(err, impl.ErrReclaimFailed) {
		t.Fatalf("second reclaim must fail with ErrReclaimFailed, got %v", err)
	}
	usage, err = g.Usage("tenant-j")
	if err != nil {
		t.Fatalf("usage after second reclaim: %v", err)
	}
	if usage.Committed != 12 {
		t.Fatalf("committed pages = %d, want 16-4 = 12", usage.Committed)
	}
	if usage.Workloads[0].Failures != 2 {
		t.Fatalf("failure counter = %d, want one increment per failing pass", usage.Workloads[0].Failures)
	}
	if _, err := g.Reclaim("tenant-404"); !errors.Is(err, impl.ErrUnknownTenant) {
		t.Fatalf("reclaim on an unknown tenant must fail with ErrUnknownTenant, got %v", err)
	}
}

func TestTenantsSortedAndRemovable(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	for _, tenant := range []string{"t-c", "t-a", "t-b"} {
		mustSetQuota(t, g, tenant, 20, 10)
	}
	tenants := g.Tenants()
	if len(tenants) != 3 || tenants[0] != "t-a" || tenants[1] != "t-b" || tenants[2] != "t-c" {
		t.Fatalf("tenants = %v, want sorted t-a,t-b,t-c", tenants)
	}
	r := mustReserve(t, g, "t-b", "w", 2)
	if err := g.RemoveTenant("t-b"); err != nil {
		t.Fatalf("remove tenant: %v", err)
	}
	if got := len(g.Tenants()); got != 2 {
		t.Fatalf("tenant count after removal = %d, want 2", got)
	}
	if _, err := g.Usage("t-b"); !errors.Is(err, impl.ErrUnknownTenant) {
		t.Fatalf("usage of a removed tenant must fail with ErrUnknownTenant, got %v", err)
	}
	if err := g.Commit(r.ID); !errors.Is(err, impl.ErrUnknownReservation) {
		t.Fatalf("committing a reservation of a removed tenant must fail with ErrUnknownReservation, got %v", err)
	}
	if err := g.RemoveTenant("t-b"); !errors.Is(err, impl.ErrUnknownTenant) {
		t.Fatalf("removing an unknown tenant must fail with ErrUnknownTenant, got %v", err)
	}
}

func TestConcurrentReserveRespectsHardLimit(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(impl.QuotaConfig{MaxTenants: 4, MaxPending: 400, ReclaimBatchPages: 1})
	mustSetQuota(t, g, "tenant-k", 100, 100)
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	rejected := 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.Reserve("tenant-k", "burst", 3)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				granted++
			case errors.Is(err, impl.ErrQuotaExceeded):
				rejected++
			}
		}()
	}
	wg.Wait()
	if granted != 33 {
		t.Fatalf("concurrent reserves granted = %d, want 33 x 3 pages under the cap 100", granted)
	}
	if rejected != 7 {
		t.Fatalf("concurrent reserves rejected = %d, want 7", rejected)
	}
	usage, err := g.Usage("tenant-k")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Pending != 99 {
		t.Fatalf("pending pages = %d, want exactly 99", usage.Pending)
	}
	if usage.Committed != 0 {
		t.Fatalf("committed pages = %d, want 0 before any commit", usage.Committed)
	}
}

func TestSnapshotRestoreKeepsLedger(t *testing.T) {
	cfg := testQuota()
	g := impl.NewSwapQuotaGovernor(cfg)
	mustSetQuota(t, g, "tenant-l", 40, 30)
	committed := mustReserve(t, g, "tenant-l", "w1", 7)
	if err := g.Commit(committed.ID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	pending := mustReserve(t, g, "tenant-l", "w2", 5)
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewSwapQuotaGovernor(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	usage, err := restored.Usage("tenant-l")
	if err != nil {
		t.Fatalf("usage after restore: %v", err)
	}
	if usage.Committed != 7 || usage.Pending != 5 {
		t.Fatalf("restored bill = %d/%d, want 7 committed and 5 pending", usage.Committed, usage.Pending)
	}
	if got := restored.PendingCount(); got != 1 {
		t.Fatalf("restored pending count = %d, want 1", got)
	}
	if err := restored.Commit(pending.ID); err != nil {
		t.Fatalf("commit of the restored pending reservation: %v", err)
	}
	usage, err = restored.Usage("tenant-l")
	if err != nil {
		t.Fatalf("usage after restored commit: %v", err)
	}
	if usage.Committed != 12 || usage.Pending != 0 {
		t.Fatalf("bill after restored commit = %d/%d, want 12/0", usage.Committed, usage.Pending)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewSwapQuotaGovernor(testQuota())
	mustSetQuota(t, g, "tenant-m", 30, 20)
	r := mustReserve(t, g, "tenant-m", "w", 6)
	if err := g.Commit(r.ID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := g.Restore([]byte("{not-json-here")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	usage, err := g.Usage("tenant-m")
	if err != nil {
		t.Fatalf("usage after rejected restore: %v", err)
	}
	if usage.Committed != 6 {
		t.Fatalf("rejected restore changed the bill to %d committed, want 6", usage.Committed)
	}
	if got := len(g.Tenants()); got != 1 {
		t.Fatalf("rejected restore left %d tenants, want 1", got)
	}
	if err := g.Release(r.ID); err != nil {
		t.Fatalf("release after rejected restore: %v", err)
	}
}
