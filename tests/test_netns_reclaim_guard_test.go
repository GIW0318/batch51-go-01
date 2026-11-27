package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-14"
)

func testReclaimConfig() impl.ReclaimConfig {
	return impl.ReclaimConfig{MaxNamespaces: 16, MaxHoldersPerNetns: 8, CleanupAttempts: 3}
}

func mustRegister(t *testing.T, g *impl.NetnsReclaimGuard, id string, inode uint64) impl.NetnsInfo {
	t.Helper()
	info, err := g.Register(id, inode)
	if err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	return info
}

func mustAcquire(t *testing.T, g *impl.NetnsReclaimGuard, id, kind, key string, inode uint64) {
	t.Helper()
	if err := g.Acquire(id, kind, key, inode); err != nil {
		t.Fatalf("acquire %s/%s/%s: %v", id, kind, key, err)
	}
}

func containsID(ids []string, id string) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}

func TestRegisterStoresLiveNamespace(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	info := mustRegister(t, g, "ns-1", 4242)
	if info.ID != "ns-1" || info.Inode != 4242 {
		t.Fatalf("registered info = %+v, want ns-1 with inode 4242", info)
	}
	if info.Refs != 0 {
		t.Fatalf("fresh reference count = %d, want 0", info.Refs)
	}
	if info.State != impl.NetnsLive {
		t.Fatalf("fresh state = %q, want %q", info.State, impl.NetnsLive)
	}
	if info.Pinned {
		t.Fatalf("a fresh namespace must not be pinned")
	}
	if info.Tombstone {
		t.Fatalf("a fresh namespace must not carry a tombstone")
	}
	stored, ok := g.Info("ns-1")
	if !ok {
		t.Fatalf("registered namespace is not readable")
	}
	if stored.Inode != 4242 || stored.Refs != 0 {
		t.Fatalf("stored info = %+v, want inode 4242 and zero refs", stored)
	}
	if _, ok := g.Info("ns-404"); ok {
		t.Fatalf("unknown namespace must not be readable")
	}
	if got := g.Pending(); len(got) != 0 {
		t.Fatalf("fresh queue = %v, want empty", got)
	}
}

func TestRegisterRejectsInvalidID(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	for _, id := range []string{"", "   "} {
		if _, err := g.Register(id, 1); !errors.Is(err, impl.ErrInvalidID) {
			t.Fatalf("id %q must be rejected with ErrInvalidID, got %v", id, err)
		}
	}
	if _, ok := g.Info(""); ok {
		t.Fatalf("rejected registrations must not create entries")
	}
}

func TestRegisterRejectsDuplicateAndLimit(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(impl.ReclaimConfig{MaxNamespaces: 2, MaxHoldersPerNetns: 4, CleanupAttempts: 2})
	mustRegister(t, g, "ns-a", 1)
	if _, err := g.Register("ns-a", 1); !errors.Is(err, impl.ErrAlreadyRegistered) {
		t.Fatalf("duplicate registration must fail with ErrAlreadyRegistered, got %v", err)
	}
	mustRegister(t, g, "ns-b", 2)
	if _, err := g.Register("ns-c", 3); !errors.Is(err, impl.ErrNamespaceLimit) {
		t.Fatalf("third namespace must fail with ErrNamespaceLimit, got %v", err)
	}
	if _, ok := g.Info("ns-c"); ok {
		t.Fatalf("rejected namespace must not be registered")
	}
}

func TestAcquireCountsDistinctHolders(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	mustRegister(t, g, "ns-hold", 7)
	mustAcquire(t, g, "ns-hold", impl.HolderProcess, "pid-100", 0)
	mustAcquire(t, g, "ns-hold", impl.HolderFD, "fd-3", 0)
	mustAcquire(t, g, "ns-hold", impl.HolderMount, "mnt-a", 9001)
	info, ok := g.Info("ns-hold")
	if !ok {
		t.Fatalf("namespace disappeared")
	}
	if info.Refs != 3 {
		t.Fatalf("reference count = %d, want 3 distinct holders", info.Refs)
	}
	if info.State != impl.NetnsLive {
		t.Fatalf("state = %q, want %q while holders exist", info.State, impl.NetnsLive)
	}
	if len(g.Pending()) != 0 {
		t.Fatalf("a namespace with holders must not be queued, queue = %v", g.Pending())
	}
}

func TestAcquireIsIdempotentForSameHolder(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	mustRegister(t, g, "ns-idem", 8)
	mustAcquire(t, g, "ns-idem", impl.HolderProcess, "pid-1", 0)
	mustAcquire(t, g, "ns-idem", impl.HolderProcess, "pid-1", 0)
	info, _ := g.Info("ns-idem")
	if info.Refs != 1 {
		t.Fatalf("reference count = %d after re-acquiring the same holder, want 1", info.Refs)
	}
	if err := g.Release("ns-idem", impl.HolderProcess, "pid-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	info, _ = g.Info("ns-idem")
	if info.Refs != 0 {
		t.Fatalf("reference count = %d after the only holder left, want 0", info.Refs)
	}
}

func TestAcquireRejectsInvalidHolder(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	mustRegister(t, g, "ns-bad", 9)
	if err := g.Acquire("ns-bad", "thread", "t-1", 0); !errors.Is(err, impl.ErrInvalidHolder) {
		t.Fatalf("unknown holder kind must fail with ErrInvalidHolder, got %v", err)
	}
	if err := g.Acquire("ns-bad", impl.HolderFD, "   ", 0); !errors.Is(err, impl.ErrInvalidHolder) {
		t.Fatalf("blank holder key must fail with ErrInvalidHolder, got %v", err)
	}
	if err := g.Acquire("ns-404", impl.HolderFD, "fd-1", 0); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("acquire on an unknown namespace must fail with ErrUnknownNamespace, got %v", err)
	}
	info, _ := g.Info("ns-bad")
	if info.Refs != 0 {
		t.Fatalf("rejected holders changed the reference count to %d", info.Refs)
	}
}

func TestAcquireEnforcesHolderLimit(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(impl.ReclaimConfig{MaxNamespaces: 4, MaxHoldersPerNetns: 2, CleanupAttempts: 1})
	mustRegister(t, g, "ns-cap", 11)
	mustAcquire(t, g, "ns-cap", impl.HolderFD, "fd-1", 0)
	mustAcquire(t, g, "ns-cap", impl.HolderFD, "fd-2", 0)
	if err := g.Acquire("ns-cap", impl.HolderFD, "fd-3", 0); !errors.Is(err, impl.ErrHolderLimit) {
		t.Fatalf("third holder must fail with ErrHolderLimit, got %v", err)
	}
	info, _ := g.Info("ns-cap")
	if info.Refs != 2 {
		t.Fatalf("reference count = %d, want the configured cap 2", info.Refs)
	}
	if err := g.Acquire("ns-cap", impl.HolderFD, "fd-1", 0); err != nil {
		t.Fatalf("re-acquiring an existing holder must not hit the cap, got %v", err)
	}
}

func TestReleaseDropsHolderAndQueuesWhenZero(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	mustRegister(t, g, "ns-drop", 12)
	mustAcquire(t, g, "ns-drop", impl.HolderProcess, "pid-1", 0)
	mustAcquire(t, g, "ns-drop", impl.HolderFD, "fd-1", 0)
	if err := g.Release("ns-drop", impl.HolderFD, "fd-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	info, _ := g.Info("ns-drop")
	if info.Refs != 1 {
		t.Fatalf("reference count = %d, want 1", info.Refs)
	}
	if len(g.Pending()) != 0 {
		t.Fatalf("namespace queued while one holder is left, queue = %v", g.Pending())
	}
	if err := g.Release("ns-drop", impl.HolderProcess, "pid-1"); err != nil {
		t.Fatalf("release last holder: %v", err)
	}
	info, _ = g.Info("ns-drop")
	if info.Refs != 0 {
		t.Fatalf("reference count = %d, want 0", info.Refs)
	}
	if info.State != impl.NetnsPending {
		t.Fatalf("state = %q, want %q once the last holder left", info.State, impl.NetnsPending)
	}
	pending := g.Pending()
	if len(pending) != 1 || pending[0] != "ns-drop" {
		t.Fatalf("queue = %v, want exactly ns-drop", pending)
	}
}

func TestReleaseRejectsUnknownHolder(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	mustRegister(t, g, "ns-rel", 13)
	mustAcquire(t, g, "ns-rel", impl.HolderFD, "fd-1", 0)
	if err := g.Release("ns-rel", impl.HolderFD, "fd-9"); !errors.Is(err, impl.ErrUnknownHolder) {
		t.Fatalf("releasing an unknown holder must fail with ErrUnknownHolder, got %v", err)
	}
	if err := g.Release("ns-404", impl.HolderFD, "fd-1"); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("releasing on an unknown namespace must fail with ErrUnknownNamespace, got %v", err)
	}
	info, _ := g.Info("ns-rel")
	if info.Refs != 1 {
		t.Fatalf("reference count = %d after rejected releases, want 1", info.Refs)
	}
}

func TestPinKeepsNamespaceLiveUntilUnpin(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	mustRegister(t, g, "ns-pin", 14)
	mustAcquire(t, g, "ns-pin", impl.HolderFD, "fd-1", 0)
	if err := g.Pin("ns-pin"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := g.Release("ns-pin", impl.HolderFD, "fd-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	info, _ := g.Info("ns-pin")
	if !info.Pinned {
		t.Fatalf("pinned flag lost after the last holder left")
	}
	if info.State != impl.NetnsLive {
		t.Fatalf("state = %q, want %q while pinned", info.State, impl.NetnsLive)
	}
	if len(g.Pending()) != 0 {
		t.Fatalf("pinned namespace must not be queued, queue = %v", g.Pending())
	}
	if err := g.Unpin("ns-pin"); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if got := g.Pending(); len(got) != 1 || got[0] != "ns-pin" {
		t.Fatalf("queue after unpin = %v, want exactly ns-pin", got)
	}
	if err := g.Unpin("ns-pin"); !errors.Is(err, impl.ErrNotPinned) {
		t.Fatalf("second unpin must fail with ErrNotPinned, got %v", err)
	}
	if err := g.Pin("ns-404"); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("pinning an unknown namespace must fail with ErrUnknownNamespace, got %v", err)
	}
}

func TestSweepReclaimsCleanedNamespace(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	empty, err := g.Sweep()
	if err != nil {
		t.Fatalf("sweep of an empty queue: %v", err)
	}
	if len(empty.Reclaimed) != 0 || len(empty.Deferred) != 0 || len(empty.Failed) != 0 {
		t.Fatalf("empty sweep = %+v, want three empty lists", empty)
	}
	mustRegister(t, g, "ns-gone", 15)
	mustAcquire(t, g, "ns-gone", impl.HolderProcess, "pid-77", 0)
	if err := g.SetCleanup("ns-gone", true, true, true); err != nil {
		t.Fatalf("set cleanup: %v", err)
	}
	if err := g.Release("ns-gone", impl.HolderProcess, "pid-77"); err != nil {
		t.Fatalf("release: %v", err)
	}
	report, err := g.Sweep()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(report.Reclaimed) != 1 || report.Reclaimed[0] != "ns-gone" {
		t.Fatalf("reclaimed = %v, want exactly ns-gone", report.Reclaimed)
	}
	if len(report.Failed) != 0 || len(report.Deferred) != 0 {
		t.Fatalf("sweep report = %+v, want no failures and no deferrals", report)
	}
	if _, ok := g.Info("ns-gone"); ok {
		t.Fatalf("reclaimed namespace is still readable")
	}
	if got := g.Pending(); len(got) != 0 {
		t.Fatalf("queue after reclamation = %v, want empty", got)
	}
}

func TestSweepDefersUncleanedNamespace(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	mustRegister(t, g, "ns-dirty", 16)
	mustAcquire(t, g, "ns-dirty", impl.HolderFD, "fd-5", 0)
	if err := g.SetCleanup("ns-dirty", true, false, true); err != nil {
		t.Fatalf("set cleanup: %v", err)
	}
	if err := g.Release("ns-dirty", impl.HolderFD, "fd-5"); err != nil {
		t.Fatalf("release: %v", err)
	}
	report, err := g.Sweep()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(report.Reclaimed) != 0 {
		t.Fatalf("uncleaned namespace was reclaimed: %v", report.Reclaimed)
	}
	if len(report.Failed) != 1 || report.Failed[0] != "ns-dirty" {
		t.Fatalf("failed list = %v, want exactly ns-dirty", report.Failed)
	}
	if _, ok := g.Info("ns-dirty"); !ok {
		t.Fatalf("a failed reclamation must not silently drop the registration")
	}
	if !containsID(g.Pending(), "ns-dirty") {
		t.Fatalf("failed namespace left the reclaim queue, queue = %v", g.Pending())
	}
	if err := g.SetCleanup("ns-404", true, true, true); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("cleanup flags on an unknown namespace must fail with ErrUnknownNamespace, got %v", err)
	}
}

func TestConcurrentAcquireReleaseKeepsRefs(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(impl.ReclaimConfig{MaxNamespaces: 4, MaxHoldersPerNetns: 64, CleanupAttempts: 1})
	mustRegister(t, g, "ns-race", 17)
	var wg sync.WaitGroup
	bad := make(chan error, 32)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "fd-burst"
			if err := g.Acquire("ns-race", impl.HolderFD, key+string(rune('a'+i%20)), 0); err != nil {
				bad <- err
				return
			}
		}(i)
	}
	wg.Wait()
	info, _ := g.Info("ns-race")
	if info.Refs != 20 {
		t.Fatalf("reference count after the burst = %d, want 20", info.Refs)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := g.Release("ns-race", impl.HolderFD, "fd-burst"+string(rune('a'+i%20))); err != nil {
				bad <- err
			}
		}(i)
	}
	wg.Wait()
	close(bad)
	for err := range bad {
		t.Fatalf("concurrent holder work: %v", err)
	}
	info, _ = g.Info("ns-race")
	if info.Refs != 0 {
		t.Fatalf("reference count after the releases = %d, want 0", info.Refs)
	}
	if !containsID(g.Pending(), "ns-race") {
		t.Fatalf("namespace with no holders is missing from the queue: %v", g.Pending())
	}
}

func TestSnapshotRestoreKeepsHolders(t *testing.T) {
	cfg := testReclaimConfig()
	g := impl.NewNetnsReclaimGuard(cfg)
	mustRegister(t, g, "ns-snap", 18)
	mustAcquire(t, g, "ns-snap", impl.HolderProcess, "pid-9", 0)
	mustAcquire(t, g, "ns-snap", impl.HolderFD, "fd-9", 0)
	mustAcquire(t, g, "ns-snap", impl.HolderMount, "mnt-9", 12345)
	if err := g.Pin("ns-snap"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := g.SetCleanup("ns-snap", true, true, true); err != nil {
		t.Fatalf("set cleanup: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewNetnsReclaimGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	info, ok := restored.Info("ns-snap")
	if !ok {
		t.Fatalf("restored guard lost the namespace")
	}
	if info.Refs != 3 {
		t.Fatalf("restored reference count = %d, want 3", info.Refs)
	}
	if !info.Pinned {
		t.Fatalf("restored guard lost the pin")
	}
	if info.Inode != 18 {
		t.Fatalf("restored inode = %d, want 18", info.Inode)
	}
	if err := restored.Release("ns-snap", impl.HolderFD, "fd-9"); err != nil {
		t.Fatalf("release on the restored guard: %v", err)
	}
	info, _ = restored.Info("ns-snap")
	if info.Refs != 2 {
		t.Fatalf("reference count after release = %d, want 2", info.Refs)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewNetnsReclaimGuard(testReclaimConfig())
	mustRegister(t, g, "ns-live", 19)
	mustAcquire(t, g, "ns-live", impl.HolderProcess, "pid-1", 0)
	if err := g.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	info, ok := g.Info("ns-live")
	if !ok {
		t.Fatalf("rejected restore dropped the live registry")
	}
	if info.Refs != 1 || info.Inode != 19 {
		t.Fatalf("rejected restore changed the live entry to %+v", info)
	}
	if err := g.Release("ns-live", impl.HolderProcess, "pid-1"); err != nil {
		t.Fatalf("release after rejected restore: %v", err)
	}
}
