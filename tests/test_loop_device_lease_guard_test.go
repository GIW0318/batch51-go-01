package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-d-06"
)

func leaseGuardConfig() impl.GuardConfig {
	return impl.GuardConfig{
		DeviceBase:  16,
		DeviceCount: 8,
		MaxLeases:   8,
		StaleAfter:  time.Hour,
		BlockSizes:  []int{512, 4096},
	}
}

func attachLease(t *testing.T, g *impl.LoopDeviceLeaseGuard, backing string, req impl.AttachRequest) impl.Lease {
	t.Helper()
	l, err := g.Attach(backing, req)
	if err != nil {
		t.Fatalf("attach %s must succeed, got %v", backing, err)
	}
	return l
}

func TestAttachAssignsDeviceAndLease(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	at := time.Unix(1700000000, 0).UTC()
	l := attachLease(t, g, "/srv/images/root.img", impl.AttachRequest{
		Holder:    "svc-a",
		ReadOnly:  true,
		AutoClear: true,
		Refs:      1,
		At:        at,
	})
	if l.ID == "" {
		t.Fatalf("lease id must not be empty")
	}
	if l.Device != 16 {
		t.Fatalf("first device number = %d, want the pool base 16", l.Device)
	}
	if l.Inode == 0 {
		t.Fatalf("lease must carry the backing file handle, got inode 0")
	}
	if l.State != impl.LeaseActive {
		t.Fatalf("lease state = %q, want %q", l.State, impl.LeaseActive)
	}
	if !l.ReadOnly || !l.AutoClear {
		t.Fatalf("lease flags lost: read_only=%v auto_clear=%v", l.ReadOnly, l.AutoClear)
	}
	if l.Refs != 1 {
		t.Fatalf("lease references = %d, want 1", l.Refs)
	}
	if !l.AttachedAt.Equal(at) {
		t.Fatalf("lease attach time = %v, want %v", l.AttachedAt, at)
	}
	view, ok := g.Lease(l.ID)
	if !ok {
		t.Fatalf("lease must be readable after attach")
	}
	if view.Device != 16 || view.Holder != "svc-a" || view.Backing != "/srv/images/root.img" {
		t.Fatalf("lease view mismatch: %+v", view)
	}
}

func TestAttachRejectsBlankInput(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	if _, err := g.Attach("   ", impl.AttachRequest{Holder: "svc-a"}); !errors.Is(err, impl.ErrBlankBacking) {
		t.Fatalf("blank backing path must be rejected with ErrBlankBacking, got %v", err)
	}
	if _, err := g.Attach("/srv/images/root.img", impl.AttachRequest{Holder: "  "}); !errors.Is(err, impl.ErrBlankHolder) {
		t.Fatalf("blank holder must be rejected with ErrBlankHolder, got %v", err)
	}
	if got := len(g.List()); got != 0 {
		t.Fatalf("rejected attaches created %d leases, want 0", got)
	}
	if _, err := g.Backing(" "); !errors.Is(err, impl.ErrBlankBacking) {
		t.Fatalf("blank backing lookup must fail with ErrBlankBacking, got %v", err)
	}
}

func TestWriteLeaseIsExclusive(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	first := attachLease(t, g, "/srv/data/one.img", impl.AttachRequest{Holder: "svc-a", Refs: 1})
	if _, err := g.Attach("/srv/data/one.img", impl.AttachRequest{Holder: "svc-b", Refs: 1}); !errors.Is(err, impl.ErrBackingBusy) {
		t.Fatalf("second write lease on the same backing must fail with ErrBackingBusy, got %v", err)
	}
	second := attachLease(t, g, "/srv/data/two.img", impl.AttachRequest{Holder: "svc-b", Refs: 1})
	if second.Device != first.Device+1 {
		t.Fatalf("second backing got device %d, want the next number %d", second.Device, first.Device+1)
	}
	if got := len(g.List()); got != 2 {
		t.Fatalf("live leases after two attaches = %d, want 2", got)
	}
}

func TestReadOnlyLeasesShareDevice(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	first := attachLease(t, g, "/srv/images/shared.img", impl.AttachRequest{Holder: "svc-a", ReadOnly: true, Refs: 1})
	second := attachLease(t, g, "/srv/images/shared.img", impl.AttachRequest{Holder: "svc-b", ReadOnly: true, Refs: 1})
	if first.Device != second.Device {
		t.Fatalf("read only leases on one backing got devices %d and %d, want the same device", first.Device, second.Device)
	}
	if second.Refs != 2 {
		t.Fatalf("shared device reference count = %d, want 2", second.Refs)
	}
	views := g.List()
	if len(views) != 2 {
		t.Fatalf("live leases = %d, want 2", len(views))
	}
	if views[0].Device != views[1].Device || views[0].ID == views[1].ID {
		t.Fatalf("shared leases must keep one device and distinct ids, got %+v", views)
	}
}

func TestAttachEnforcesDeviceCap(t *testing.T) {
	cfg := leaseGuardConfig()
	cfg.MaxLeases = 2
	cfg.DeviceCount = 2
	g := impl.NewLoopDeviceLeaseGuard(cfg)
	attachLease(t, g, "/srv/a.img", impl.AttachRequest{Holder: "svc-a", Refs: 1})
	attachLease(t, g, "/srv/b.img", impl.AttachRequest{Holder: "svc-a", Refs: 1})
	if _, err := g.Attach("/srv/c.img", impl.AttachRequest{Holder: "svc-a", Refs: 1}); !errors.Is(err, impl.ErrNoFreeDevice) {
		t.Fatalf("third backing must fail with ErrNoFreeDevice, got %v", err)
	}
	if got := len(g.List()); got != 2 {
		t.Fatalf("live leases = %d, want the configured cap 2", got)
	}
}

func TestDetachWithoutRefsReleasesDevice(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	l := attachLease(t, g, "/srv/images/scratch.img", impl.AttachRequest{Holder: "svc-a", Refs: 0})
	if err := g.Detach(l.ID); err != nil {
		t.Fatalf("detach must succeed, got %v", err)
	}
	view, ok := g.Lease(l.ID)
	if !ok {
		t.Fatalf("released lease must stay readable for auditing")
	}
	if view.State != impl.LeaseReleased {
		t.Fatalf("lease state after detach = %q, want %q", view.State, impl.LeaseReleased)
	}
	if got := len(g.List()); got != 0 {
		t.Fatalf("live leases after detach = %d, want 0", got)
	}
	reused := attachLease(t, g, "/srv/images/next.img", impl.AttachRequest{Holder: "svc-a", Refs: 0})
	if reused.Device != 16 {
		t.Fatalf("freed device number was not returned to the pool, got %d", reused.Device)
	}
}

func TestDetachUnknownOrReleasedLease(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	if err := g.Detach("lease-404"); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("detaching an unknown lease must fail with ErrUnknownLease, got %v", err)
	}
	l := attachLease(t, g, "/srv/images/once.img", impl.AttachRequest{Holder: "svc-a", Refs: 0})
	if err := g.Detach(l.ID); err != nil {
		t.Fatalf("first detach must succeed, got %v", err)
	}
	if err := g.Detach(l.ID); !errors.Is(err, impl.ErrLeaseReleased) {
		t.Fatalf("second detach must fail with ErrLeaseReleased, got %v", err)
	}
}

func TestCloseRefCountsDown(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	l := attachLease(t, g, "/srv/images/multi.img", impl.AttachRequest{Holder: "svc-a", ReadOnly: true, Refs: 2})
	remaining, err := g.CloseRef(l.ID)
	if err != nil {
		t.Fatalf("close reference must succeed, got %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining references = %d, want 1", remaining)
	}
	remaining, err = g.CloseRef(l.ID)
	if err != nil {
		t.Fatalf("closing the last reference must succeed, got %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining references = %d, want 0", remaining)
	}
	if _, err := g.CloseRef(l.ID); !errors.Is(err, impl.ErrBadRefCount) {
		t.Fatalf("closing a zero reference count must fail with ErrBadRefCount, got %v", err)
	}
	opened, err := g.OpenRef(l.ID)
	if err != nil {
		t.Fatalf("open reference must succeed, got %v", err)
	}
	if opened != 1 {
		t.Fatalf("references after reopen = %d, want 1", opened)
	}
}

func TestReconfigureUpdatesOptions(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	l := attachLease(t, g, "/srv/images/tuned.img", impl.AttachRequest{Holder: "svc-a", ReadOnly: true, Refs: 1})
	if err := g.Reconfigure(l.ID, impl.LoopOptions{ReadOnly: false, BlockSize: 4096}); err != nil {
		t.Fatalf("reconfigure must succeed, got %v", err)
	}
	view, _ := g.Lease(l.ID)
	if view.ReadOnly {
		t.Fatalf("reconfigure did not clear the read only flag")
	}
	if view.BlockSize != 4096 {
		t.Fatalf("block size = %d, want 4096", view.BlockSize)
	}
	if err := g.Reconfigure(l.ID, impl.LoopOptions{BlockSize: 777}); !errors.Is(err, impl.ErrBadOptions) {
		t.Fatalf("unsupported block size must fail with ErrBadOptions, got %v", err)
	}
	view, _ = g.Lease(l.ID)
	if view.BlockSize != 4096 {
		t.Fatalf("rejected reconfigure changed the block size to %d", view.BlockSize)
	}
	if err := g.Reconfigure("lease-404", impl.LoopOptions{BlockSize: 512}); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("reconfigure of an unknown lease must fail with ErrUnknownLease, got %v", err)
	}
}

func TestReconcileReclaimsDeadHolder(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	now := time.Unix(2000000000, 0).UTC()
	g.SetHolderAlive("svc-dead", false)
	g.SetHolderAlive("svc-live", true)
	dead := attachLease(t, g, "/srv/images/dead.img", impl.AttachRequest{Holder: "svc-dead", Refs: 0, At: now.Add(-2 * time.Hour)})
	live := attachLease(t, g, "/srv/images/live.img", impl.AttachRequest{Holder: "svc-live", Refs: 0, At: now.Add(-2 * time.Hour)})
	events, err := g.Reconcile(now)
	if err != nil {
		t.Fatalf("reconcile must succeed, got %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("reconcile produced %d events, want exactly 1 for the dead holder", len(events))
	}
	if events[0].Kind != impl.EventReclaimed || events[0].Holder != "svc-dead" {
		t.Fatalf("reclaim event mismatch: %+v", events[0])
	}
	if events[0].LeaseID != dead.ID {
		t.Fatalf("reclaim event points at %q, want %q", events[0].LeaseID, dead.ID)
	}
	if events[0].Device != dead.Device {
		t.Fatalf("reclaim event device = %d, want %d", events[0].Device, dead.Device)
	}
	if view, ok := g.Lease(dead.ID); !ok || view.State != impl.LeaseReleased {
		t.Fatalf("reclaimed lease state = %+v, want released", view)
	}
	if view, ok := g.Lease(live.ID); !ok || view.State != impl.LeaseActive {
		t.Fatalf("live holder lease state = %+v, want active", view)
	}
	if got := len(g.Events()); got != 1 {
		t.Fatalf("event history holds %d entries, want 1", got)
	}
}

func TestReconcileSkipsFreshLease(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	now := time.Unix(2000000000, 0).UTC()
	g.SetHolderAlive("svc-crash", false)
	fresh := attachLease(t, g, "/srv/images/fresh.img", impl.AttachRequest{Holder: "svc-crash", Refs: 0, At: now.Add(-2 * time.Minute)})
	events, err := g.Reconcile(now)
	if err != nil {
		t.Fatalf("reconcile must succeed, got %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("reconcile reclaimed %d leases that are younger than the stale window", len(events))
	}
	view, ok := g.Lease(fresh.ID)
	if !ok || view.State != impl.LeaseActive {
		t.Fatalf("fresh lease state = %+v, want active", view)
	}
	if got := len(g.List()); got != 1 {
		t.Fatalf("live leases = %d, want 1", got)
	}
}

func TestSnapshotRestoreKeepsRefsAndDevices(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	base := attachLease(t, g, "/srv/images/dup.img", impl.AttachRequest{Holder: "svc-a", ReadOnly: true, Refs: 2, At: time.Unix(1500000000, 0).UTC()})
	attachLease(t, g, "/srv/images/dup.img", impl.AttachRequest{Holder: "svc-b", ReadOnly: true, Refs: 1, At: time.Unix(1500000100, 0).UTC()})
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	view, ok := restored.Lease(base.ID)
	if !ok {
		t.Fatalf("restored guard lost the lease record")
	}
	if view.Device != base.Device {
		t.Fatalf("restored device number = %d, want %d", view.Device, base.Device)
	}
	if view.Refs != 3 {
		t.Fatalf("restored reference count = %d, want 3", view.Refs)
	}
	if !view.ReadOnly || view.Backing != "/srv/images/dup.img" {
		t.Fatalf("restored lease flags lost: %+v", view)
	}
	if got := len(restored.List()); got != 2 {
		t.Fatalf("restored live leases = %d, want 2", got)
	}
	remaining, err := restored.CloseRef(base.ID)
	if err != nil {
		t.Fatalf("close reference after restore: %v", err)
	}
	if remaining != 2 {
		t.Fatalf("references after restore and close = %d, want 2", remaining)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	l := attachLease(t, g, "/srv/images/keep.img", impl.AttachRequest{Holder: "svc-a", Refs: 1})
	if err := g.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	view, ok := g.Lease(l.ID)
	if !ok || view.State != impl.LeaseActive || view.Refs != 1 {
		t.Fatalf("failed restore must not touch live state, got %+v ok=%v", view, ok)
	}
	next := attachLease(t, g, "/srv/images/after.img", impl.AttachRequest{Holder: "svc-a", Refs: 1})
	if next.ID == l.ID {
		t.Fatalf("lease ids must not be reused after a rejected restore")
	}
	if next.Device == l.Device {
		t.Fatalf("live lease already holds device %d, restored attach must take another one", l.Device)
	}
}

func TestConcurrentWriteAttachSingleWinner(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := make(chan struct{})
	won, busy, other := 0, 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-started
			_, err := g.Attach("/srv/images/race.img", impl.AttachRequest{Holder: fmt.Sprintf("svc-%02d", i), Refs: 1})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, impl.ErrBackingBusy):
				busy++
			default:
				other++
			}
		}(i)
	}
	close(started)
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d concurrent write attaches won on one backing, want exactly 1", won)
	}
	if busy != workers-1 {
		t.Fatalf("%d contenders reported ErrBackingBusy, want %d", busy, workers-1)
	}
	if other != 0 {
		t.Fatalf("%d contenders failed with an unexpected error", other)
	}
	if got := len(g.List()); got != 1 {
		t.Fatalf("live leases after the race = %d, want 1", got)
	}
}

func TestConcurrentDetachSingleWinner(t *testing.T) {
	g := impl.NewLoopDeviceLeaseGuard(leaseGuardConfig())
	l := attachLease(t, g, "/srv/images/once-more.img", impl.AttachRequest{Holder: "svc-a", Refs: 0})
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := make(chan struct{})
	done, released, other := 0, 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-started
			err := g.Detach(l.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				done++
			case errors.Is(err, impl.ErrLeaseReleased):
				released++
			default:
				other++
			}
		}()
	}
	close(started)
	wg.Wait()
	if done != 1 {
		t.Fatalf("%d concurrent detaches succeeded, want exactly 1", done)
	}
	if released != workers-1 {
		t.Fatalf("%d detaches reported ErrLeaseReleased, want %d", released, workers-1)
	}
	if other != 0 {
		t.Fatalf("%d detaches failed with an unexpected error", other)
	}
	view, ok := g.Lease(l.ID)
	if !ok || view.State != impl.LeaseReleased {
		t.Fatalf("lease state after the race = %+v, want released", view)
	}
}
