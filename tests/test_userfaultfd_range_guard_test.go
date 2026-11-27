package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-c-20"
)

type uffdClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *uffdClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *uffdClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newUffdGuard(cfg impl.Config) *impl.UserfaultRangeGuard {
	if cfg.PageSize == 0 {
		cfg.PageSize = impl.DefaultPageSize
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Unix(1700000000, 0) }
	}
	if cfg.MaxRangesPerProcess == 0 {
		cfg.MaxRangesPerProcess = 16
	}
	if cfg.HeartbeatTTL == 0 {
		cfg.HeartbeatTTL = time.Second
	}
	return impl.NewUserfaultRangeGuard(cfg)
}

func mustRegisterHandler(t *testing.T, g *impl.UserfaultRangeGuard, id string) {
	t.Helper()
	if err := g.RegisterHandler(id); err != nil {
		t.Fatalf("register handler %s: %v", id, err)
	}
}

func mustRegisterRange(t *testing.T, g *impl.UserfaultRangeGuard, reg impl.Registration) uint64 {
	t.Helper()
	id, err := g.Register(reg)
	if err != nil {
		t.Fatalf("register range %+v: %v", reg, err)
	}
	return id
}

func TestHandlerLifecycle(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	if !g.HandlerAlive("h1") {
		t.Fatalf("a freshly registered handler must be alive")
	}
	if err := g.RegisterHandler("h1"); !errors.Is(err, impl.ErrHandlerExists) {
		t.Fatalf("duplicate handler must report ErrHandlerExists, got %v", err)
	}
	for _, id := range []string{"", "   "} {
		if err := g.RegisterHandler(id); !errors.Is(err, impl.ErrInvalidHandler) {
			t.Fatalf("handler id %q must report ErrInvalidHandler, got %v", id, err)
		}
	}
	if g.HandlerAlive("h404") {
		t.Fatalf("an unknown handler must not be reported alive")
	}
}

func TestHeartbeatKeepsHandlerAlive(t *testing.T) {
	clock := &uffdClock{now: time.Unix(1700000000, 0)}
	g := newUffdGuard(impl.Config{Now: clock.Now, HeartbeatTTL: time.Second})
	mustRegisterHandler(t, g, "h1")
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 4096, HandlerID: "h1", Backup: "snap-a"})
	if err := g.Heartbeat("h404", clock.Now()); !errors.Is(err, impl.ErrUnknownHandler) {
		t.Fatalf("heartbeat of an unknown handler must report ErrUnknownHandler, got %v", err)
	}
	clock.Advance(800 * time.Millisecond)
	if err := g.Heartbeat("h1", clock.Now()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	clock.Advance(800 * time.Millisecond)
	if got := g.Sweep(clock.Now()); len(got) != 0 {
		t.Fatalf("sweep reclaimed %v although the heartbeat is recent", got)
	}
	if !g.HandlerAlive("h1") {
		t.Fatalf("a heartbeat must keep the handler alive")
	}
	clock.Advance(400 * time.Millisecond)
	if got := g.Sweep(clock.Now()); len(got) != 1 || got[0] != 1 {
		t.Fatalf("sweep after the heartbeat expired returned %v, want range 1", got)
	}
	if g.HandlerAlive("h1") {
		t.Fatalf("an expired handler must be reported dead")
	}
}

func TestRegisterStoresRangeFields(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	id := mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 8192, HandlerID: "h1", Backup: "snap-a"})
	if id != 1 {
		t.Fatalf("first range id = %d, want 1", id)
	}
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 8192, End: 12288, HandlerID: "h1", Backup: "snap-b"})
	ranges := g.Ranges(100)
	if len(ranges) != 2 {
		t.Fatalf("ranges = %+v, want two entries", ranges)
	}
	first := ranges[0]
	if first.ID != id || first.PID != 100 || first.Start != 0 || first.End != 8192 {
		t.Fatalf("stored range = %+v, want the registered identity and bounds", first)
	}
	if first.HandlerID != "h1" || first.Backup != "snap-a" || first.Generation != 1 {
		t.Fatalf("stored range = %+v, want handler h1, backup snap-a and generation 1", first)
	}
	if ranges[1].Start != 8192 || ranges[1].Backup != "snap-b" {
		t.Fatalf("second range = %+v, want the later entry ordered after the first", ranges[1])
	}
	if stat := g.Stat(100); stat.Ranges != 2 {
		t.Fatalf("process stat = %+v, want two ranges", stat)
	}
}

func TestRegisterRejectsInvalidAndUnalignedRanges(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	cases := []struct {
		reg  impl.Registration
		want error
	}{
		{impl.Registration{PID: 100, Start: 100, End: 4096, HandlerID: "h1"}, impl.ErrUnalignedRange},
		{impl.Registration{PID: 100, Start: 0, End: 1000, HandlerID: "h1"}, impl.ErrUnalignedRange},
		{impl.Registration{PID: 100, Start: 4096, End: 4096, HandlerID: "h1"}, impl.ErrInvalidRange},
		{impl.Registration{PID: 100, Start: 8192, End: 4096, HandlerID: "h1"}, impl.ErrInvalidRange},
		{impl.Registration{PID: 0, Start: 0, End: 4096, HandlerID: "h1"}, impl.ErrInvalidRange},
	}
	for _, c := range cases {
		if _, err := g.Register(c.reg); !errors.Is(err, c.want) {
			t.Fatalf("registration %+v must report %v, got %v", c.reg, c.want, err)
		}
	}
	if got := len(g.Ranges(100)); got != 0 {
		t.Fatalf("rejected registrations left %d ranges", got)
	}
}

func TestRangesOfOneProcessMustNotOverlap(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 8192, HandlerID: "h1"})
	if _, err := g.Register(impl.Registration{PID: 100, Start: 4096, End: 12288, HandlerID: "h1"}); !errors.Is(err, impl.ErrRangeOverlap) {
		t.Fatalf("an overlapping range must report ErrRangeOverlap, got %v", err)
	}
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 8192, End: 12288, HandlerID: "h1"})
	if got := len(g.Ranges(100)); got != 2 {
		t.Fatalf("adjacent ranges must both be accepted, got %d", got)
	}
}

func TestFaultsRouteByProcess(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	mustRegisterHandler(t, g, "h2")
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 4096, HandlerID: "h1", Backup: "snap-a"})
	mustRegisterRange(t, g, impl.Registration{PID: 200, Start: 0, End: 4096, HandlerID: "h2", Backup: "snap-b"})
	resA, err := g.Handle(impl.Fault{PID: 100, Addr: 512})
	if err != nil {
		t.Fatalf("handle fault for pid 100: %v", err)
	}
	if resA.HandlerID != "h1" || resA.Backup != "snap-a" {
		t.Fatalf("pid 100 resolution = %+v, want handler h1", resA)
	}
	resB, err := g.Handle(impl.Fault{PID: 200, Addr: 512})
	if err != nil {
		t.Fatalf("handle fault for pid 200: %v", err)
	}
	if resB.HandlerID != "h2" || resB.Backup != "snap-b" {
		t.Fatalf("pid 200 resolution = %+v, want handler h2", resB)
	}
}

func TestRegisterRequiresLiveHandler(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	if _, err := g.Register(impl.Registration{PID: 100, Start: 0, End: 4096, HandlerID: "h404"}); !errors.Is(err, impl.ErrUnknownHandler) {
		t.Fatalf("registering with an unknown handler must report ErrUnknownHandler, got %v", err)
	}
	if got := len(g.Ranges(100)); got != 0 {
		t.Fatalf("a rejected registration left %d ranges", got)
	}
}

func TestPerProcessRangeLimit(t *testing.T) {
	g := newUffdGuard(impl.Config{MaxRangesPerProcess: 2})
	mustRegisterHandler(t, g, "h1")
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 4096, HandlerID: "h1"})
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 4096, End: 8192, HandlerID: "h1"})
	if _, err := g.Register(impl.Registration{PID: 100, Start: 8192, End: 12288, HandlerID: "h1"}); !errors.Is(err, impl.ErrRangeLimit) {
		t.Fatalf("a third range must report ErrRangeLimit, got %v", err)
	}
	if got := len(g.Ranges(100)); got != 2 {
		t.Fatalf("ranges after the rejected registration = %d, want 2", got)
	}
}

func TestHandleReturnsResolutionForOwningHandler(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	id := mustRegisterRange(t, g, impl.Registration{PID: 7, Start: 4096, End: 8192, HandlerID: "h1", Backup: "snap-1"})
	first, err := g.Handle(impl.Fault{PID: 7, Addr: 5000})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if first.RangeID != id || first.PID != 7 || first.Page != 4096 {
		t.Fatalf("resolution = %+v, want range %d on page 4096", first, id)
	}
	if first.HandlerID != "h1" || first.Backup != "snap-1" || first.Generation != 1 {
		t.Fatalf("resolution = %+v, want handler h1, backup snap-1 and generation 1", first)
	}
	second, err := g.Handle(impl.Fault{PID: 7, Addr: 5000})
	if err != nil {
		t.Fatalf("second handle: %v", err)
	}
	if second.Token <= first.Token {
		t.Fatalf("resolution token %d must increase past %d", second.Token, first.Token)
	}
	if stat := g.Stat(7); stat.Resolutions != 2 {
		t.Fatalf("process stat = %+v, want two resolutions", stat)
	}
}

func TestHandleFailsClosedOutsideAnyRange(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 4096, End: 8192, HandlerID: "h1"})
	if _, err := g.Handle(impl.Fault{PID: 100, Addr: 8192}); !errors.Is(err, impl.ErrNoHandler) {
		t.Fatalf("the range end is exclusive, a fault there must report ErrNoHandler, got %v", err)
	}
	if _, err := g.Handle(impl.Fault{PID: 999, Addr: 4096}); !errors.Is(err, impl.ErrNoHandler) {
		t.Fatalf("a process without ranges must report ErrNoHandler, got %v", err)
	}
}

func TestSweepExpiresHandlerAndDetachesRanges(t *testing.T) {
	clock := &uffdClock{now: time.Unix(1700000000, 0)}
	g := newUffdGuard(impl.Config{Now: clock.Now, HeartbeatTTL: time.Second})
	mustRegisterHandler(t, g, "h1")
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 4096, HandlerID: "h1"})
	clock.Advance(1500 * time.Millisecond)
	reclaimed := g.Sweep(clock.Now())
	if len(reclaimed) != 1 || reclaimed[0] != 1 {
		t.Fatalf("sweep returned %v, want the expired range 1", reclaimed)
	}
	if g.HandlerAlive("h1") {
		t.Fatalf("the expired handler must be removed")
	}
	if got := len(g.Ranges(100)); got != 0 {
		t.Fatalf("the expired handler still owns %d ranges", got)
	}
	if _, err := g.Handle(impl.Fault{PID: 100, Addr: 0}); !errors.Is(err, impl.ErrNoHandler) {
		t.Fatalf("a fault on a detached range must fail closed, got %v", err)
	}
}

func TestUnregisterDropsWholeRange(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	first := mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 4096, HandlerID: "h1"})
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 4096, End: 8192, HandlerID: "h1"})
	if err := g.Unregister(first); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	ranges := g.Ranges(100)
	if len(ranges) != 1 || ranges[0].Start != 4096 {
		t.Fatalf("ranges after unregister = %+v, want only the second range", ranges)
	}
	if err := g.Unregister(first); !errors.Is(err, impl.ErrUnknownRange) {
		t.Fatalf("unregistering twice must report ErrUnknownRange, got %v", err)
	}
	if err := g.Unregister(999); !errors.Is(err, impl.ErrUnknownRange) {
		t.Fatalf("unregistering an unknown range must report ErrUnknownRange, got %v", err)
	}
	if _, err := g.Handle(impl.Fault{PID: 100, Addr: 0}); !errors.Is(err, impl.ErrNoHandler) {
		t.Fatalf("a fault in the removed range must report ErrNoHandler, got %v", err)
	}
}

func TestConcurrentHandlesGetUniqueTokens(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 1 << 20, HandlerID: "h1"})
	const workers = 8
	const perWorker = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	tokens := make(map[uint64]bool, workers*perWorker)
	failures := 0
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				res, err := g.Handle(impl.Fault{PID: 100, Addr: uint64(w*perWorker+i) * 4096})
				mu.Lock()
				if err != nil || res.HandlerID != "h1" {
					failures++
				} else {
					tokens[res.Token] = true
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent handles failed or picked another handler", failures)
	}
	if len(tokens) != workers*perWorker {
		t.Fatalf("concurrent handles produced %d distinct tokens, want %d", len(tokens), workers*perWorker)
	}
}

func TestSnapshotRestoreKeepsHandlersAndRanges(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	id := mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 4096, HandlerID: "h1", Backup: "snap-a"})
	first, err := g.Handle(impl.Fault{PID: 100, Addr: 512})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := newUffdGuard(impl.Config{})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !restored.HandlerAlive("h1") {
		t.Fatalf("restored handler must stay alive")
	}
	ranges := restored.Ranges(100)
	if len(ranges) != 1 || ranges[0].ID != id || ranges[0].Backup != "snap-a" {
		t.Fatalf("restored ranges = %+v, want the original registration", ranges)
	}
	if stat := restored.Stat(100); stat.Ranges != 1 {
		t.Fatalf("restored process stat = %+v, want one range", stat)
	}
	second, err := restored.Handle(impl.Fault{PID: 100, Addr: 512})
	if err != nil {
		t.Fatalf("handle after restore: %v", err)
	}
	if second.Token <= first.Token {
		t.Fatalf("token after restore = %d, must continue past %d", second.Token, first.Token)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newUffdGuard(impl.Config{})
	mustRegisterHandler(t, g, "h1")
	mustRegisterRange(t, g, impl.Registration{PID: 100, Start: 0, End: 4096, HandlerID: "h1"})
	if err := g.Restore([]byte("}{ not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if got := len(g.Ranges(100)); got != 1 {
		t.Fatalf("a rejected restore changed the range count to %d", got)
	}
	if _, err := g.Handle(impl.Fault{PID: 100, Addr: 0}); err != nil {
		t.Fatalf("live ranges must keep serving after a rejected restore, got %v", err)
	}
}
