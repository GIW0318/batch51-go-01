package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-r-08"
)

func testLimitConfig() impl.LimitConfig {
	return impl.LimitConfig{
		BurstTokens:  2,
		SteadyTokens: 4,
		RefillPerSec: 1,
		TenantQuota:  16,
		GlobalQuota:  64,
		MaxBuckets:   16,
		MaxAudit:     8,
	}
}

func newTestLimitGuard(t *testing.T, cfg impl.LimitConfig) *impl.RateLimitBypassGuard {
	t.Helper()
	g, err := impl.NewRateLimitBypassGuard(cfg)
	if err != nil {
		t.Fatalf("building the guard must succeed, got %v", err)
	}
	if g == nil {
		t.Fatalf("guard must not be nil")
	}
	return g
}

func TestNewGuardRejectsBadConfig(t *testing.T) {
	mutations := []struct {
		name string
		edit func(*impl.LimitConfig)
	}{
		{"burst", func(c *impl.LimitConfig) { c.BurstTokens = -1 }},
		{"steady", func(c *impl.LimitConfig) { c.SteadyTokens = 0 }},
		{"refill", func(c *impl.LimitConfig) { c.RefillPerSec = 0 }},
		{"tenant quota", func(c *impl.LimitConfig) { c.TenantQuota = 0 }},
		{"global quota", func(c *impl.LimitConfig) { c.GlobalQuota = -2 }},
		{"bucket cap", func(c *impl.LimitConfig) { c.MaxBuckets = 0 }},
		{"audit cap", func(c *impl.LimitConfig) { c.MaxAudit = 0 }},
	}
	for _, m := range mutations {
		cfg := testLimitConfig()
		m.edit(&cfg)
		if _, err := impl.NewRateLimitBypassGuard(cfg); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("bad %s must be rejected with ErrBadConfig, got %v", m.name, err)
		}
	}
	g := newTestLimitGuard(t, testLimitConfig())
	if got := len(g.Buckets()); got != 0 {
		t.Fatalf("fresh guard bucket count = %d, want 0", got)
	}
	if got := g.Global(); got.Used != 0 || got.Limit != 64 {
		t.Fatalf("fresh global quota = %+v, want used=0 limit=64", got)
	}
}

func TestNormalizeKeyRejectsBlankInput(t *testing.T) {
	g := newTestLimitGuard(t, testLimitConfig())
	if _, err := g.NormalizeKey("   ", "alice"); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank tenant must report ErrBlankKey, got %v", err)
	}
	if _, err := g.NormalizeKey("tenant-mall", "  "); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank client must report ErrBlankKey, got %v", err)
	}
	key, err := g.NormalizeKey("tenant-mall", "alice")
	if err != nil {
		t.Fatalf("normalising a plain key: %v", err)
	}
	if key != "tenant-mall/alice" {
		t.Fatalf("normalised key = %q, want tenant-mall/alice", key)
	}
	trimmed, err := g.NormalizeKey("  tenant-mall ", " alice  ")
	if err != nil {
		t.Fatalf("normalising a padded key: %v", err)
	}
	if trimmed != key {
		t.Fatalf("padded key folded to %q, want the same bucket %q", trimmed, key)
	}
}

func TestAllowReturnsAllowedDecision(t *testing.T) {
	g := newTestLimitGuard(t, testLimitConfig())
	first, err := g.Allow("tenant-mall", "c1", 1000)
	if err != nil {
		t.Fatalf("first request must be allowed, got %v", err)
	}
	if !first.Allowed {
		t.Fatalf("first decision must be marked allowed")
	}
	if first.Reason != impl.ReasonAllowed {
		t.Fatalf("reason = %q, want %q", first.Reason, impl.ReasonAllowed)
	}
	if first.Key != "tenant-mall/c1" || first.Tenant != "tenant-mall" {
		t.Fatalf("decision key=%q tenant=%q, want tenant-mall/c1 and tenant-mall", first.Key, first.Tenant)
	}
	if first.Seq <= 0 {
		t.Fatalf("decision seq = %d, want a positive number", first.Seq)
	}
	if first.Remaining != 5 {
		t.Fatalf("remaining tokens = %d, want 5 after one call from a 6 token meter", first.Remaining)
	}
	second, err := g.Allow("tenant-mall", "c1", 1000)
	if err != nil {
		t.Fatalf("second request must be allowed, got %v", err)
	}
	if second.Remaining != 4 {
		t.Fatalf("remaining tokens = %d, want 4", second.Remaining)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("decision sequence must keep growing, got %d after %d", second.Seq, first.Seq)
	}
}

func TestAllowDeniesAfterTokensExhausted(t *testing.T) {
	cfg := impl.LimitConfig{
		BurstTokens:  1,
		SteadyTokens: 2,
		RefillPerSec: 1,
		TenantQuota:  16,
		GlobalQuota:  64,
		MaxBuckets:   16,
		MaxAudit:     8,
	}
	g := newTestLimitGuard(t, cfg)
	wantRemaining := []int{2, 1, 0}
	for i, want := range wantRemaining {
		d, err := g.Allow("tenant-a", "c1", 1000)
		if err != nil {
			t.Fatalf("call %d must be allowed, got %v", i, err)
		}
		if d.Remaining != want {
			t.Fatalf("call %d remaining = %d, want %d", i, d.Remaining, want)
		}
	}
	d, err := g.Allow("tenant-a", "c1", 1000)
	if !errors.Is(err, impl.ErrRateLimited) {
		t.Fatalf("an empty meter must report ErrRateLimited, got %v", err)
	}
	if d.Allowed {
		t.Fatalf("a refused decision must not be marked allowed")
	}
	if d.Reason != impl.ReasonRateLimited {
		t.Fatalf("deny reason = %q, want %q", d.Reason, impl.ReasonRateLimited)
	}
	stats := g.Stats()
	if stats.Requests != 4 || stats.Allowed != 3 || stats.Rejected != 1 {
		t.Fatalf("stats = %+v, want requests=4 allowed=3 rejected=1", stats)
	}
	if got := g.Quota("tenant-a").Used; got != 0 {
		t.Fatalf("a refused call left %d quota slots held, want 0", got)
	}
}

func TestRefillRestoresCapacity(t *testing.T) {
	cfg := impl.LimitConfig{
		BurstTokens:  0,
		SteadyTokens: 2,
		RefillPerSec: 1,
		TenantQuota:  16,
		GlobalQuota:  64,
		MaxBuckets:   16,
		MaxAudit:     8,
	}
	g := newTestLimitGuard(t, cfg)
	if _, err := g.Allow("tenant-b", "c1", 2000); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := g.Allow("tenant-b", "c1", 2000); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if _, err := g.Allow("tenant-b", "c1", 2000); !errors.Is(err, impl.ErrRateLimited) {
		t.Fatalf("third call must be refused, got %v", err)
	}
	g.Refill(2001)
	d, err := g.Allow("tenant-b", "c1", 2001)
	if err != nil {
		t.Fatalf("a refilled meter must admit one more call, got %v", err)
	}
	if d.Remaining != 0 {
		t.Fatalf("remaining after the refilled call = %d, want 0", d.Remaining)
	}
	if _, err := g.Allow("tenant-b", "c1", 2001); !errors.Is(err, impl.ErrRateLimited) {
		t.Fatalf("the refilled token must be the only one, got %v", err)
	}
}

func TestTenantQuotaEnforced(t *testing.T) {
	g := newTestLimitGuard(t, impl.LimitConfig{
		BurstTokens:  8,
		SteadyTokens: 8,
		RefillPerSec: 1,
		TenantQuota:  2,
		GlobalQuota:  64,
		MaxBuckets:   16,
		MaxAudit:     8,
	})
	if _, err := g.Reserve("tenant-q", "c1", 1000); err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	if _, err := g.Reserve("tenant-q", "c2", 1000); err != nil {
		t.Fatalf("second reservation: %v", err)
	}
	if _, err := g.Reserve("tenant-q", "c3", 1000); !errors.Is(err, impl.ErrTenantQuota) {
		t.Fatalf("a third hold over a quota of two must report ErrTenantQuota, got %v", err)
	}
	quota := g.Quota("tenant-q")
	if quota.Used != 2 || quota.Limit != 2 {
		t.Fatalf("tenant quota = %+v, want used=2 limit=2", quota)
	}
	global := g.Global()
	if global.Used != 2 || global.Limit != 64 {
		t.Fatalf("global quota = %+v, want used=2 limit=64", global)
	}
}

func TestReserveThenCommitReleasesQuota(t *testing.T) {
	g := newTestLimitGuard(t, testLimitConfig())
	res, err := g.Reserve("tenant-r", "c1", 1000)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if res.ID == "" {
		t.Fatalf("reservation id must not be empty")
	}
	if res.Key != "tenant-r/c1" || res.Tenant != "tenant-r" {
		t.Fatalf("reservation key=%q tenant=%q, want tenant-r/c1 and tenant-r", res.Key, res.Tenant)
	}
	if res.Closed {
		t.Fatalf("a fresh reservation must not be closed")
	}
	if got := g.Quota("tenant-r").Used; got != 1 {
		t.Fatalf("quota used = %d, want 1 while the reservation is open", got)
	}
	if got := g.Stats().Inflight; got != 1 {
		t.Fatalf("in-flight counter = %d, want 1", got)
	}
	if err := g.Commit(res); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := g.Quota("tenant-r").Used; got != 0 {
		t.Fatalf("quota used = %d, want 0 after the commit", got)
	}
	if got := g.Stats().Inflight; got != 0 {
		t.Fatalf("in-flight counter = %d, want 0 after the commit", got)
	}
	if err := g.Commit(res); !errors.Is(err, impl.ErrReservationClosed) {
		t.Fatalf("committing the same reservation twice must report ErrReservationClosed, got %v", err)
	}
}

func TestCancelReturnsTokenAndQuota(t *testing.T) {
	cfg := impl.LimitConfig{
		BurstTokens:  0,
		SteadyTokens: 1,
		RefillPerSec: 1,
		TenantQuota:  16,
		GlobalQuota:  64,
		MaxBuckets:   16,
		MaxAudit:     8,
	}
	g := newTestLimitGuard(t, cfg)
	res, err := g.Reserve("tenant-c", "c1", 1000)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := g.Allow("tenant-c", "c1", 1000); !errors.Is(err, impl.ErrRateLimited) {
		t.Fatalf("the only token is held, so a second call must be refused, got %v", err)
	}
	if err := g.Cancel(res); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := g.Quota("tenant-c").Used; got != 0 {
		t.Fatalf("quota used = %d, want 0 after the cancel", got)
	}
	if _, err := g.Allow("tenant-c", "c1", 1000); err != nil {
		t.Fatalf("a cancelled hold must hand the token back, got %v", err)
	}
	if err := g.Cancel(res); !errors.Is(err, impl.ErrReservationClosed) {
		t.Fatalf("cancelling the same reservation twice must report ErrReservationClosed, got %v", err)
	}
}

func TestUnknownReservationRejected(t *testing.T) {
	g := newTestLimitGuard(t, testLimitConfig())
	ghost := impl.Reservation{ID: "hold-404", Key: "tenant-x/c1", Tenant: "tenant-x"}
	if err := g.Commit(ghost); !errors.Is(err, impl.ErrUnknownReservation) {
		t.Fatalf("committing an unknown reservation must report ErrUnknownReservation, got %v", err)
	}
	if err := g.Cancel(ghost); !errors.Is(err, impl.ErrUnknownReservation) {
		t.Fatalf("cancelling an unknown reservation must report ErrUnknownReservation, got %v", err)
	}
	if err := g.Commit(impl.Reservation{ID: "", Tenant: "tenant-x"}); !errors.Is(err, impl.ErrUnknownReservation) {
		t.Fatalf("an empty reservation id must report ErrUnknownReservation, got %v", err)
	}
	if got := g.Quota("tenant-x").Used; got != 0 {
		t.Fatalf("refused reservation calls changed the quota to %d, want 0", got)
	}
}

func TestBucketLimitEnforced(t *testing.T) {
	cfg := testLimitConfig()
	cfg.MaxBuckets = 2
	g := newTestLimitGuard(t, cfg)
	if _, err := g.Allow("tenant-d", "c1", 1000); err != nil {
		t.Fatalf("first bucket: %v", err)
	}
	if _, err := g.Allow("tenant-d", "c2", 1000); err != nil {
		t.Fatalf("second bucket: %v", err)
	}
	if _, err := g.Allow("tenant-d", "c3", 1000); !errors.Is(err, impl.ErrBucketLimit) {
		t.Fatalf("a third key over a cap of two must report ErrBucketLimit, got %v", err)
	}
	if got := len(g.Buckets()); got != 2 {
		t.Fatalf("bucket count = %d, want the configured cap 2", got)
	}
	rejects := g.Rejections()
	if len(rejects) != 1 {
		t.Fatalf("rejection log length = %d, want 1", len(rejects))
	}
	if rejects[0].Reason != impl.ReasonBucketLimit {
		t.Fatalf("rejection reason = %q, want %q", rejects[0].Reason, impl.ReasonBucketLimit)
	}
}

func TestBucketsListKeysInOrder(t *testing.T) {
	g := newTestLimitGuard(t, testLimitConfig())
	for _, client := range []string{"c3", "c1", "c2"} {
		if _, err := g.Allow("tenant-e", client, 1000); err != nil {
			t.Fatalf("allow %s: %v", client, err)
		}
	}
	buckets := g.Buckets()
	if len(buckets) != 3 {
		t.Fatalf("bucket count = %d, want 3", len(buckets))
	}
	want := []string{"tenant-e/c1", "tenant-e/c2", "tenant-e/c3"}
	for i, key := range want {
		if buckets[i].Key != key {
			t.Fatalf("bucket %d key = %q, want %q", i, buckets[i].Key, key)
		}
		if buckets[i].Tenant != "tenant-e" {
			t.Fatalf("bucket %d tenant = %q, want tenant-e", i, buckets[i].Tenant)
		}
	}
	if got := g.Stats().Buckets; got != 3 {
		t.Fatalf("bucket counter = %d, want 3", got)
	}
}

func TestConcurrentReserveRespectsTenantQuota(t *testing.T) {
	cfg := impl.LimitConfig{
		BurstTokens:  16,
		SteadyTokens: 16,
		RefillPerSec: 1,
		TenantQuota:  4,
		GlobalQuota:  64,
		MaxBuckets:   64,
		MaxAudit:     16,
	}
	g := newTestLimitGuard(t, cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted, refused := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := g.Reserve("tenant-race", fmt.Sprintf("client-%02d", i), 1000)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				granted++
			case errors.Is(err, impl.ErrTenantQuota):
				refused++
			}
		}(i)
	}
	wg.Wait()
	if granted != 4 {
		t.Fatalf("concurrent reservations granted %d holds, want exactly the quota 4", granted)
	}
	if refused != 12 {
		t.Fatalf("concurrent reservations refused %d holds, want 12", refused)
	}
	quota := g.Quota("tenant-race")
	if quota.Used != 4 {
		t.Fatalf("quota used after the race = %d, want 4", quota.Used)
	}
	if got := g.Stats().Inflight; got != 4 {
		t.Fatalf("in-flight counter after the race = %d, want 4", got)
	}
}

func TestRejectionsAreCappedAndOrdered(t *testing.T) {
	cfg := impl.LimitConfig{
		BurstTokens:  0,
		SteadyTokens: 1,
		RefillPerSec: 1,
		TenantQuota:  16,
		GlobalQuota:  64,
		MaxBuckets:   16,
		MaxAudit:     3,
	}
	g := newTestLimitGuard(t, cfg)
	if _, err := g.Allow("tenant-f", "c1", 1000); err != nil {
		t.Fatalf("the first call must use the single token, got %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := g.Allow("tenant-f", "c1", 1000); !errors.Is(err, impl.ErrRateLimited) {
			t.Fatalf("refused call %d must report ErrRateLimited, got %v", i, err)
		}
	}
	rejects := g.Rejections()
	if len(rejects) != 3 {
		t.Fatalf("retained rejections = %d, want the configured cap 3", len(rejects))
	}
	for i := 1; i < len(rejects); i++ {
		if rejects[i].Seq <= rejects[i-1].Seq {
			t.Fatalf("rejection sequence is not increasing: %+v", rejects)
		}
	}
	for i, record := range rejects {
		if record.Reason != impl.ReasonRateLimited {
			t.Fatalf("rejection %d reason = %q, want %q", i, record.Reason, impl.ReasonRateLimited)
		}
		if record.Key != "tenant-f/c1" {
			t.Fatalf("rejection %d key = %q, want tenant-f/c1", i, record.Key)
		}
	}
	if got := g.Stats().Rejected; got != 4 {
		t.Fatalf("rejected counter = %d, want 4", got)
	}
}

func TestSnapshotRestoreKeepsBuckets(t *testing.T) {
	cfg := impl.LimitConfig{
		BurstTokens:  1,
		SteadyTokens: 2,
		RefillPerSec: 1,
		TenantQuota:  16,
		GlobalQuota:  64,
		MaxBuckets:   16,
		MaxAudit:     8,
	}
	g := newTestLimitGuard(t, cfg)
	for i := 0; i < 3; i++ {
		if _, err := g.Allow("tenant-g", "c1", 1000); err != nil {
			t.Fatalf("call %d must be allowed, got %v", i, err)
		}
	}
	if _, err := g.Allow("tenant-g", "c1", 1000); !errors.Is(err, impl.ErrRateLimited) {
		t.Fatalf("the meter must be empty before the snapshot, got %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newTestLimitGuard(t, cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Buckets()); got != 1 {
		t.Fatalf("restored bucket count = %d, want 1", got)
	}
	if got := restored.Stats().Allowed; got != 3 {
		t.Fatalf("restored allowed counter = %d, want 3", got)
	}
	if got := len(restored.Rejections()); got != 1 {
		t.Fatalf("restored rejection log length = %d, want 1", got)
	}
	if _, err := restored.Allow("tenant-g", "c1", 1000); !errors.Is(err, impl.ErrRateLimited) {
		t.Fatalf("the restored meter must still be empty, got %v", err)
	}
	if got := len(restored.Rejections()); got != 2 {
		t.Fatalf("rejection log length after the restored refusal = %d, want 2", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newTestLimitGuard(t, testLimitConfig())
	if _, err := g.Allow("tenant-h", "c1", 1000); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if err := g.Restore([]byte("###not a snapshot###")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if got := len(g.Buckets()); got != 1 {
		t.Fatalf("failed restore changed the bucket count to %d, want 1", got)
	}
	if got := g.Stats().Allowed; got != 1 {
		t.Fatalf("failed restore changed the allowed counter to %d, want 1", got)
	}
	if got := g.Quota("tenant-h"); got.Used != 0 || got.Limit != 16 {
		t.Fatalf("failed restore changed the tenant quota to %+v", got)
	}
	if _, err := g.Allow("tenant-h", "c1", 1000); err != nil {
		t.Fatalf("the guard must keep working after a refused restore, got %v", err)
	}
}
