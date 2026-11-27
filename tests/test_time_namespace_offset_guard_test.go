package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-11"
)

func fixedClock(realtimeNanos, boottimeNanos int64) func() impl.HostTime {
	return func() impl.HostTime {
		return impl.HostTime{RealtimeNanos: realtimeNanos, BoottimeNanos: boottimeNanos}
	}
}

func newGuard(realtimeNanos, boottimeNanos int64) *impl.TimeNamespaceOffsetGuard {
	return impl.NewTimeNamespaceOffsetGuard(impl.GuardConfig{
		HostClock:      fixedClock(realtimeNanos, boottimeNanos),
		MaxNamespaces:  8,
		MaxOffsetNanos: 3600 * 1000 * 1000 * 1000,
	})
}

func TestCreateNamespaceStartsAtInitialGeneration(t *testing.T) {
	g := newGuard(1_000_000_000, 400_000_000)
	h, err := g.CreateNamespace("ns-a")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if h.Namespace != "ns-a" {
		t.Fatalf("handle namespace = %q, want ns-a", h.Namespace)
	}
	if h.Token == 0 {
		t.Fatalf("handle token must not be zero")
	}
	gen, err := g.Generation(h)
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if gen != impl.InitialGeneration {
		t.Fatalf("fresh generation = %d, want %d", gen, impl.InitialGeneration)
	}
	snap, err := g.Now(h)
	if err != nil {
		t.Fatalf("now: %v", err)
	}
	if snap.Namespace != "ns-a" || snap.Generation != impl.InitialGeneration {
		t.Fatalf("snapshot identity = %q/%d, want ns-a/%d", snap.Namespace, snap.Generation, impl.InitialGeneration)
	}
	if snap.RealtimeNanos != 1_000_000_000 || snap.BoottimeNanos != 400_000_000 {
		t.Fatalf("fresh clock = %d/%d, want the raw host sample 1000000000/400000000", snap.RealtimeNanos, snap.BoottimeNanos)
	}
	if snap.RealtimeOffset != 0 || snap.BoottimeOffset != 0 {
		t.Fatalf("fresh offsets = %d/%d, want zero", snap.RealtimeOffset, snap.BoottimeOffset)
	}
	infos := g.Namespaces()
	if len(infos) != 1 || infos[0].Namespace != "ns-a" || !infos[0].Live {
		t.Fatalf("registry = %+v, want exactly one live ns-a", infos)
	}
}

func TestCreateNamespaceRejectsBadNames(t *testing.T) {
	g := newGuard(0, 0)
	for _, name := range []string{"", "   ", "ns/a", ".."} {
		if _, err := g.CreateNamespace(name); !errors.Is(err, impl.ErrInvalidNamespace) {
			t.Fatalf("name %q must be rejected with ErrInvalidNamespace, got %v", name, err)
		}
	}
	if got := len(g.Namespaces()); got != 0 {
		t.Fatalf("rejected registrations left %d namespaces, want 0", got)
	}
}

func TestCreateNamespaceRejectsDuplicate(t *testing.T) {
	g := newGuard(0, 0)
	first, err := g.CreateNamespace("ns-dup")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if _, err := g.CreateNamespace("ns-dup"); !errors.Is(err, impl.ErrNamespaceExists) {
		t.Fatalf("duplicate namespace must fail with ErrNamespaceExists, got %v", err)
	}
	second, err := g.Handle("ns-dup")
	if err != nil {
		t.Fatalf("lookup after duplicate: %v", err)
	}
	if second.Token != first.Token {
		t.Fatalf("duplicate registration replaced the handle: %d vs %d", second.Token, first.Token)
	}
}

func TestCreateNamespaceEnforcesLimit(t *testing.T) {
	g := impl.NewTimeNamespaceOffsetGuard(impl.GuardConfig{
		HostClock:      fixedClock(0, 0),
		MaxNamespaces:  2,
		MaxOffsetNanos: 1000,
	})
	for _, name := range []string{"ns-1", "ns-2"} {
		if _, err := g.CreateNamespace(name); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	if _, err := g.CreateNamespace("ns-3"); !errors.Is(err, impl.ErrNamespaceLimit) {
		t.Fatalf("third namespace must hit ErrNamespaceLimit, got %v", err)
	}
	if got := len(g.Namespaces()); got != 2 {
		t.Fatalf("namespace count = %d, want the configured cap 2", got)
	}
}

func TestApplyOffsetAdvancesGeneration(t *testing.T) {
	g := newGuard(1_000_000_000, 400_000_000)
	h, err := g.CreateNamespace("ns-off")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	gen, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: 5_000_000_000, BoottimeNanos: 2_000_000_000})
	if err != nil {
		t.Fatalf("apply offset: %v", err)
	}
	if gen != impl.InitialGeneration+1 {
		t.Fatalf("generation after one swap = %d, want %d", gen, impl.InitialGeneration+1)
	}
	snap, err := g.Now(h)
	if err != nil {
		t.Fatalf("now: %v", err)
	}
	if snap.Generation != gen {
		t.Fatalf("snapshot generation = %d, want %d", snap.Generation, gen)
	}
	if snap.RealtimeNanos != 6_000_000_000 || snap.BoottimeNanos != 2_400_000_000 {
		t.Fatalf("clock after swap = %d/%d, want 6000000000/2400000000", snap.RealtimeNanos, snap.BoottimeNanos)
	}
	if snap.RealtimeOffset != 5_000_000_000 || snap.BoottimeOffset != 2_000_000_000 {
		t.Fatalf("snapshot offsets = %d/%d, want 5000000000/2000000000", snap.RealtimeOffset, snap.BoottimeOffset)
	}
	next, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: -1_000_000_000, BoottimeNanos: 0})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if next != gen+1 {
		t.Fatalf("generation after second swap = %d, want %d", next, gen+1)
	}
}

func TestApplyOffsetRejectsOutOfRange(t *testing.T) {
	g := impl.NewTimeNamespaceOffsetGuard(impl.GuardConfig{
		HostClock:      fixedClock(0, 0),
		MaxNamespaces:  4,
		MaxOffsetNanos: 1_000_000_000,
	})
	h, err := g.CreateNamespace("ns-range")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	before, err := g.Generation(h)
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if _, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: 2_000_000_000}); !errors.Is(err, impl.ErrOffsetRange) {
		t.Fatalf("oversized realtime offset must fail with ErrOffsetRange, got %v", err)
	}
	if _, err := g.ApplyOffset(h, impl.TimeOffset{BoottimeNanos: -2_000_000_000}); !errors.Is(err, impl.ErrOffsetRange) {
		t.Fatalf("oversized boottime offset must fail with ErrOffsetRange, got %v", err)
	}
	if _, err := g.ApplyOffset(h, impl.TimeOffset{SlewPPM: impl.MaxSlewPPM + 1}); !errors.Is(err, impl.ErrOffsetRange) {
		t.Fatalf("oversized slew rate must fail with ErrOffsetRange, got %v", err)
	}
	after, err := g.Generation(h)
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if after != before {
		t.Fatalf("rejected offsets advanced the generation from %d to %d", before, after)
	}
	if _, err := g.ApplyOffset(h, impl.TimeOffset{SlewPPM: -impl.MaxSlewPPM - 1}); !errors.Is(err, impl.ErrOffsetRange) {
		t.Fatalf("negative slew beyond the bound must fail with ErrOffsetRange, got %v", err)
	}
}

func TestTranslateDeadlineUsesCurrentGeneration(t *testing.T) {
	g := newGuard(1_000_000_000, 400_000_000)
	h, err := g.CreateNamespace("ns-dl")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	gen, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: 2_000_000_000, BoottimeNanos: -1_000_000_000})
	if err != nil {
		t.Fatalf("apply offset: %v", err)
	}
	d, err := g.TranslateDeadline(h, 5_000_000_000)
	if err != nil {
		t.Fatalf("translate deadline: %v", err)
	}
	if d.Namespace != "ns-dl" || d.Generation != gen {
		t.Fatalf("deadline identity = %q/%d, want ns-dl/%d", d.Namespace, d.Generation, gen)
	}
	if d.RemainingNanos != 2_000_000_000 {
		t.Fatalf("remaining = %d, want 5000000000-2000000000-1000000000 = 2000000000", d.RemainingNanos)
	}
	if d.HostBoottimeNanos != 2_400_000_000 {
		t.Fatalf("host boottime target = %d, want host boottime 400000000 plus 2000000000", d.HostBoottimeNanos)
	}
	if d.Expired {
		t.Fatalf("a deadline two seconds out must not be reported as expired")
	}
}

func TestResolveConvertsGuestStamp(t *testing.T) {
	g := newGuard(1_000_000_000, 400_000_000)
	h, err := g.CreateNamespace("ns-res")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if _, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: 2_000_000_000}); err != nil {
		t.Fatalf("apply offset: %v", err)
	}
	got, err := g.Resolve(h, 7_000_000_000)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != 5_000_000_000 {
		t.Fatalf("resolved host stamp = %d, want 7000000000-2000000000 = 5000000000", got)
	}
	after, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: -3_000_000_000})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if after != 3 {
		t.Fatalf("generation = %d, want 3 after two swaps", after)
	}
	if got, err := g.Resolve(h, 1_000_000_000); err != nil || got != 4_000_000_000 {
		t.Fatalf("resolve with the new offsets = %d/%v, want 4000000000 and no error", got, err)
	}
}

func TestResolveRejectsZeroStamp(t *testing.T) {
	g := newGuard(0, 0)
	h, err := g.CreateNamespace("ns-stamp")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if _, err := g.Resolve(h, 0); !errors.Is(err, impl.ErrInvalidStamp) {
		t.Fatalf("zero stamp must fail with ErrInvalidStamp, got %v", err)
	}
	if _, err := g.Resolve(h, -5); !errors.Is(err, impl.ErrInvalidStamp) {
		t.Fatalf("negative stamp must fail with ErrInvalidStamp, got %v", err)
	}
}

func TestReleaseInvalidatesHandle(t *testing.T) {
	g := newGuard(0, 0)
	h, err := g.CreateNamespace("ns-rel")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if err := g.Release(h); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := g.Release(h); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("second release must fail with ErrStaleHandle, got %v", err)
	}
	if _, err := g.Now(h); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("now on a released handle must fail with ErrStaleHandle, got %v", err)
	}
	if _, err := g.ApplyOffset(h, impl.TimeOffset{}); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("offset swap on a released handle must fail with ErrStaleHandle, got %v", err)
	}
	if _, err := g.Generation(h); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("generation on a released handle must fail with ErrStaleHandle, got %v", err)
	}
	if got := len(g.Namespaces()); got != 0 {
		t.Fatalf("released namespace still listed, count = %d", got)
	}
}

func TestNamespacesSortedAndLiveOnly(t *testing.T) {
	g := newGuard(0, 0)
	handles := map[string]impl.Handle{}
	for _, name := range []string{"ns-b", "ns-a", "ns-c"} {
		h, err := g.CreateNamespace(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		handles[name] = h
	}
	if err := g.Release(handles["ns-a"]); err != nil {
		t.Fatalf("release: %v", err)
	}
	infos := g.Namespaces()
	if len(infos) != 2 {
		t.Fatalf("live namespace count = %d, want 2", len(infos))
	}
	if infos[0].Namespace != "ns-b" || infos[1].Namespace != "ns-c" {
		t.Fatalf("namespace order = %q,%q, want ns-b,ns-c", infos[0].Namespace, infos[1].Namespace)
	}
	again, err := g.CreateNamespace("ns-a")
	if err != nil {
		t.Fatalf("re-register ns-a: %v", err)
	}
	if again.Token <= handles["ns-a"].Token {
		t.Fatalf("re-registered token %d must exceed the released token %d", again.Token, handles["ns-a"].Token)
	}
}

func TestLookupHandle(t *testing.T) {
	g := newGuard(0, 0)
	created, err := g.CreateNamespace("ns-look")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	found, err := g.Handle("ns-look")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if found.Namespace != created.Namespace || found.Token != created.Token {
		t.Fatalf("lookup returned %+v, want %+v", found, created)
	}
	if _, err := g.Handle("ns-missing"); !errors.Is(err, impl.ErrUnknownHandle) {
		t.Fatalf("unknown namespace lookup must fail with ErrUnknownHandle, got %v", err)
	}
	if _, err := g.Handle("   "); !errors.Is(err, impl.ErrInvalidNamespace) {
		t.Fatalf("blank lookup must fail with ErrInvalidNamespace, got %v", err)
	}
}

func TestSnapshotRestoreKeepsGenerations(t *testing.T) {
	cfg := impl.GuardConfig{
		HostClock:      fixedClock(1_000_000_000, 400_000_000),
		MaxNamespaces:  8,
		MaxOffsetNanos: 3600 * 1000 * 1000 * 1000,
	}
	g := impl.NewTimeNamespaceOffsetGuard(cfg)
	h, err := g.CreateNamespace("ns-snap")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if _, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: 1_000_000_000}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: 3_000_000_000, BoottimeNanos: 2_000_000_000}); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewTimeNamespaceOffsetGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rh, err := restored.Handle("ns-snap")
	if err != nil {
		t.Fatalf("lookup after restore: %v", err)
	}
	if rh.Token != h.Token {
		t.Fatalf("restored token = %d, want the snapshot token %d", rh.Token, h.Token)
	}
	gen, err := restored.Generation(rh)
	if err != nil {
		t.Fatalf("generation after restore: %v", err)
	}
	if gen != 3 {
		t.Fatalf("restored generation = %d, want 3", gen)
	}
	snap, err := restored.Now(rh)
	if err != nil {
		t.Fatalf("now after restore: %v", err)
	}
	if snap.RealtimeOffset != 3_000_000_000 || snap.BoottimeOffset != 2_000_000_000 {
		t.Fatalf("restored offsets = %d/%d, want 3000000000/2000000000", snap.RealtimeOffset, snap.BoottimeOffset)
	}
	if err := restored.Release(rh); err != nil {
		t.Fatalf("release after restore: %v", err)
	}
	fresh, err := restored.CreateNamespace("ns-snap")
	if err != nil {
		t.Fatalf("re-register after restore: %v", err)
	}
	if fresh.Token <= h.Token {
		t.Fatalf("token after restore = %d, must exceed the snapshot token %d", fresh.Token, h.Token)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newGuard(0, 0)
	h, err := g.CreateNamespace("ns-live")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if _, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: 7}); err != nil {
		t.Fatalf("apply offset: %v", err)
	}
	before, err := g.Generation(h)
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if err := g.Restore([]byte("{ not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, err := g.Handle("ns-live"); err != nil {
		t.Fatalf("rejected restore dropped the live registry: %v", err)
	}
	after, err := g.Generation(h)
	if err != nil {
		t.Fatalf("generation after rejected restore: %v", err)
	}
	if after != before {
		t.Fatalf("rejected restore changed the generation from %d to %d", before, after)
	}
}

func TestConcurrentOffsetSwapsKeepTriplesConsistent(t *testing.T) {
	g := newGuard(0, 0)
	h, err := g.CreateNamespace("ns-race")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	const writers = 10
	var wg sync.WaitGroup
	errs := make(chan error, writers+writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rt := int64(i) * 1000
			bt := 7 - rt
			if _, err := g.ApplyOffset(h, impl.TimeOffset{RealtimeNanos: rt, BoottimeNanos: bt}); err != nil {
				errs <- err
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 40; k++ {
				snap, err := g.Now(h)
				if err != nil {
					errs <- err
					return
				}
				if snap.RealtimeOffset+snap.BoottimeOffset != 7 {
					errs <- errors.New("snapshot mixed offsets of two generations")
					return
				}
				if snap.Generation <= 0 {
					errs <- errors.New("snapshot generation must stay positive")
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent swap: %v", err)
	}
	final, err := g.Generation(h)
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if final != impl.InitialGeneration+writers {
		t.Fatalf("final generation = %d, want %d after %d swaps", final, impl.InitialGeneration+writers, writers)
	}
}
