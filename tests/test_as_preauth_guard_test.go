package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-06"
)

var clockNow = time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)

func testPolicy() impl.RealmPolicy {
	return impl.RealmPolicy{
		Tenant:        "acme",
		Realm:         "ACME.LOCAL",
		AllowedETypes: []int{17, 18, 23},
		MinEType:      18,
		ClockSkew:     time.Minute,
		MaxReplay:     16,
		MaxPending:    4,
		MaxFailures:   3,
		Now:           func() time.Time { return clockNow },
	}
}

func beginFor(t *testing.T, g *impl.AsPreauthGuard, client string) impl.AsReply {
	t.Helper()
	reply, err := g.Begin(impl.AsRequest{Client: client, Realm: "ACME.LOCAL", Tenant: "acme", Nonce: "n-" + client})
	if err != nil {
		t.Fatalf("begin for %s: %v", client, err)
	}
	return reply
}

func TestBeginReturnsPreauthRequired(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	reply, err := g.Begin(impl.AsRequest{Client: "carol", Realm: "ACME.LOCAL", Tenant: "acme", Nonce: "n-1"})
	if err != nil {
		t.Fatalf("begin must open a preauth session, got %v", err)
	}
	if reply.Status != impl.StatusContinue || reply.Reason != impl.ReasonPreauthRequired {
		t.Fatalf("status=%q reason=%q, want continue/preauth_required", reply.Status, reply.Reason)
	}
	if reply.SessionID == "" {
		t.Fatalf("begin must hand out a session id")
	}
	want := []int{23, 18, 17}
	if len(reply.ETypes) != len(want) {
		t.Fatalf("advertised etypes = %v, want %v", reply.ETypes, want)
	}
	for i, v := range want {
		if reply.ETypes[i] != v {
			t.Fatalf("advertised etypes = %v, want strongest first %v", reply.ETypes, want)
		}
	}
	if reply.Salt != "ACME.LOCAL/carol" {
		t.Fatalf("salt = %q, want the recomputable realm/client string", reply.Salt)
	}
	if pending, ok := g.Pending(reply.SessionID); !ok || pending.Client != "carol" {
		t.Fatalf("pending session = %+v ok=%v, want carol waiting", pending, ok)
	}
	if st := g.Stats("acme"); st.Pending != 1 || st.Tenant != "acme" {
		t.Fatalf("stats = %+v, want one pending session on acme", st)
	}
}

func TestBeginRejectsBlankRequest(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	bad := []impl.AsRequest{
		{Realm: "ACME.LOCAL", Tenant: "acme"},
		{Client: "carol", Tenant: "acme"},
		{Client: "carol", Realm: "ACME.LOCAL"},
	}
	for _, req := range bad {
		if _, err := g.Begin(req); !errors.Is(err, impl.ErrBadRequest) {
			t.Fatalf("begin %+v must fail with ErrBadRequest, got %v", req, err)
		}
	}
	if st := g.Stats("acme"); st.Pending != 0 {
		t.Fatalf("rejected begins left %d pending sessions", st.Pending)
	}
}

func TestBeginEnforcesPendingLimit(t *testing.T) {
	cfg := testPolicy()
	cfg.MaxPending = 2
	g := impl.NewAsPreauthGuard(cfg)
	for i := 0; i < 2; i++ {
		if _, err := g.Begin(impl.AsRequest{Client: "dave", Realm: "ACME.LOCAL", Tenant: "acme"}); err != nil {
			t.Fatalf("begin %d for dave: %v", i, err)
		}
	}
	if _, err := g.Begin(impl.AsRequest{Client: "dave", Realm: "ACME.LOCAL", Tenant: "acme"}); !errors.Is(err, impl.ErrPendingLimit) {
		t.Fatalf("third pending session must hit ErrPendingLimit, got %v", err)
	}
	if st := g.Stats("acme"); st.Pending != 2 {
		t.Fatalf("pending sessions = %d, want the cap 2", st.Pending)
	}
	if _, err := g.Begin(impl.AsRequest{Client: "erin", Realm: "ACME.LOCAL", Tenant: "acme"}); err != nil {
		t.Fatalf("another account must not inherit the pending cap, got %v", err)
	}
}

func TestPolicyResolvesTenantView(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	p := g.Policy("acme")
	if p.Realm != "ACME.LOCAL" || p.MinEType != 18 || p.ClockSkew != time.Minute {
		t.Fatalf("policy view = %+v, want the tenant settings", p)
	}
	if len(p.ETypes) != 3 || p.ETypes[0] != 23 || p.ETypes[2] != 17 {
		t.Fatalf("policy etypes = %v, want strongest first", p.ETypes)
	}
	p.ETypes[0] = 99
	if again := g.Policy("acme"); again.ETypes[0] != 23 {
		t.Fatalf("policy view must be a copy, internal etypes became %v", again.ETypes)
	}
	if other := g.Policy("globex"); other.Realm != "" || len(other.ETypes) != 0 {
		t.Fatalf("unknown tenant must resolve to a zero view, got %+v", other)
	}
}

func TestPreauthIssuesTicketAndConsumesSession(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	begin := beginFor(t, g, "carol")
	reply, err := g.Preauth(begin.SessionID, impl.PreauthData{
		Client: "carol",
		Realm:  "ACME.LOCAL",
		EType:  18,
		Time:   clockNow.Add(-5 * time.Second),
	})
	if err != nil {
		t.Fatalf("preauth with a fresh timestamp must succeed, got %v", err)
	}
	if reply.Status != impl.StatusIssued || reply.Reason != impl.ReasonOK {
		t.Fatalf("status=%q reason=%q, want issued/ok", reply.Status, reply.Reason)
	}
	if reply.EType != 18 {
		t.Fatalf("negotiated etype = %d, want the requested 18", reply.EType)
	}
	if reply.Seq <= begin.Seq {
		t.Fatalf("issued seq = %d, must be greater than the begin seq %d", reply.Seq, begin.Seq)
	}
	if _, ok := g.Pending(begin.SessionID); ok {
		t.Fatalf("an issued session must leave the pending table")
	}
	if _, err := g.Preauth(begin.SessionID, impl.PreauthData{Client: "carol", Realm: "ACME.LOCAL", EType: 18, Time: clockNow}); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("a consumed session must not be reusable, got %v", err)
	}
	if st := g.Stats("acme"); st.Issued != 1 || st.Pending != 0 {
		t.Fatalf("stats = %+v, want one issued and nothing pending", st)
	}
	if g.ReplaySize() != 1 {
		t.Fatalf("replay cache size = %d, want the accepted timestamp recorded once", g.ReplaySize())
	}
}

func TestPreauthRejectsUnknownSession(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	pa := impl.PreauthData{Client: "carol", Realm: "ACME.LOCAL", EType: 18, Time: clockNow}
	if _, err := g.Preauth("sess-404", pa); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("unknown session must fail with ErrUnknownSession, got %v", err)
	}
	if _, err := g.Preauth("", pa); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("empty session id must fail with ErrUnknownSession, got %v", err)
	}
	if g.ReplaySize() != 0 {
		t.Fatalf("rejected sessions must not touch the replay cache")
	}
}

func TestPreauthRejectsWeakEType(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	begin := beginFor(t, g, "carol")
	_, err := g.Preauth(begin.SessionID, impl.PreauthData{Client: "carol", Realm: "ACME.LOCAL", EType: 17, Time: clockNow})
	if !errors.Is(err, impl.ErrWeakEType) {
		t.Fatalf("etype 17 is below the floor 18 and must fail with ErrWeakEType, got %v", err)
	}
	if _, ok := g.Pending(begin.SessionID); !ok {
		t.Fatalf("a weak etype attempt must not consume the pending session")
	}
	if got := g.Failures("carol"); got != 1 {
		t.Fatalf("failure counter = %d, want 1 after the weak etype rejection", got)
	}
	if g.ReplaySize() != 0 {
		t.Fatalf("a rejected etype must not enter the replay cache")
	}
}

func TestPreauthRejectsUnsupportedEType(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	begin := beginFor(t, g, "carol")
	_, err := g.Preauth(begin.SessionID, impl.PreauthData{Client: "carol", Realm: "ACME.LOCAL", EType: 3, Time: clockNow})
	if !errors.Is(err, impl.ErrUnsupportedEType) {
		t.Fatalf("etype 3 is not enabled and must fail with ErrUnsupportedEType, got %v", err)
	}
	if got := g.Failures("carol"); got != 1 {
		t.Fatalf("failure counter = %d, want 1 after the unsupported etype rejection", got)
	}
	if _, ok := g.Pending(begin.SessionID); !ok {
		t.Fatalf("the session must stay pending so the client can retry")
	}
}

func TestPreauthRejectsTimestampOutsideWindow(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	begin := beginFor(t, g, "carol")
	_, err := g.Preauth(begin.SessionID, impl.PreauthData{
		Client: "carol",
		Realm:  "ACME.LOCAL",
		EType:  18,
		Time:   clockNow.Add(-5 * time.Minute),
	})
	if err == nil {
		t.Fatalf("a timestamp five minutes old must not be accepted with a one minute window")
	}
	if errors.Is(err, impl.ErrUnknownSession) || errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("a skew rejection must be reported as a timestamp problem, got %v", err)
	}
	if got := g.Failures("carol"); got != 1 {
		t.Fatalf("failure counter = %d, want 1 after the skew rejection", got)
	}
	if _, ok := g.Pending(begin.SessionID); !ok {
		t.Fatalf("a skew rejection must leave the session pending")
	}
}

func TestPreauthFailureLockBlocksClient(t *testing.T) {
	cfg := testPolicy()
	cfg.MaxFailures = 3
	g := impl.NewAsPreauthGuard(cfg)
	begin := beginFor(t, g, "carol")
	for i := 0; i < 3; i++ {
		if _, err := g.Preauth(begin.SessionID, impl.PreauthData{Client: "carol", Realm: "ACME.LOCAL", EType: 17, Time: clockNow}); !errors.Is(err, impl.ErrWeakEType) {
			t.Fatalf("attempt %d must fail with ErrWeakEType, got %v", i, err)
		}
	}
	if _, err := g.Begin(impl.AsRequest{Client: "carol", Realm: "ACME.LOCAL", Tenant: "acme"}); !errors.Is(err, impl.ErrTooManyFailures) {
		t.Fatalf("a locked account must not open a new session, got %v", err)
	}
	if _, err := g.Preauth(begin.SessionID, impl.PreauthData{Client: "carol", Realm: "ACME.LOCAL", EType: 18, Time: clockNow}); !errors.Is(err, impl.ErrTooManyFailures) {
		t.Fatalf("a locked account must not complete preauth, got %v", err)
	}
	if got := g.Failures("carol"); got != 3 {
		t.Fatalf("failure counter = %d, want the lock threshold 3", got)
	}
}

func TestCheckReplayRejectsDuplicate(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	ts := clockNow.Add(-10 * time.Second)
	if err := g.CheckReplay("carol", "ACME.LOCAL", ts, 18); err != nil {
		t.Fatalf("first tuple must be accepted, got %v", err)
	}
	if g.ReplaySize() != 1 {
		t.Fatalf("replay cache size = %d, want 1", g.ReplaySize())
	}
	if err := g.CheckReplay("carol", "ACME.LOCAL", ts, 18); !errors.Is(err, impl.ErrReplay) {
		t.Fatalf("the same tuple must be rejected as a replay, got %v", err)
	}
	if g.ReplaySize() != 1 {
		t.Fatalf("a replay must not add a second entry, cache size = %d", g.ReplaySize())
	}
	if err := g.CheckReplay("carol", "ACME.LOCAL", ts, 23); err != nil {
		t.Fatalf("the same timestamp under another etype is a different tuple, got %v", err)
	}
}

func TestCheckReplayEnforcesCapacity(t *testing.T) {
	cfg := testPolicy()
	cfg.MaxReplay = 2
	g := impl.NewAsPreauthGuard(cfg)
	for i := 0; i < 2; i++ {
		ts := clockNow.Add(-time.Duration(i+1) * time.Second)
		if err := g.CheckReplay("carol", "ACME.LOCAL", ts, 18); err != nil {
			t.Fatalf("tuple %d must be accepted, got %v", i, err)
		}
	}
	if err := g.CheckReplay("carol", "ACME.LOCAL", clockNow.Add(-30*time.Second), 18); !errors.Is(err, impl.ErrReplayCacheFull) {
		t.Fatalf("a full replay cache must report ErrReplayCacheFull, got %v", err)
	}
	if g.ReplaySize() != 2 {
		t.Fatalf("cache size = %d, want the cap 2 with no eviction", g.ReplaySize())
	}
}

func TestConcurrentCheckReplayAdmitsOne(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	ts := clockNow.Add(-20 * time.Second)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, replayed := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := g.CheckReplay("carol", "ACME.LOCAL", ts, 18)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, impl.ErrReplay):
				replayed++
			}
		}()
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("concurrent tuples accepted %d times, want exactly 1", accepted)
	}
	if replayed != 15 {
		t.Fatalf("concurrent tuples replayed %d times, want 15", replayed)
	}
	if g.ReplaySize() != 1 {
		t.Fatalf("replay cache size = %d, want 1", g.ReplaySize())
	}
}

func TestSnapshotRestoreKeepsReplayAndFailures(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	begin := beginFor(t, g, "carol")
	for i := 0; i < 2; i++ {
		if _, err := g.Preauth(begin.SessionID, impl.PreauthData{Client: "carol", Realm: "ACME.LOCAL", EType: 17, Time: clockNow}); !errors.Is(err, impl.ErrWeakEType) {
			t.Fatalf("failure %d must be rejected as weak, got %v", i, err)
		}
	}
	ts := clockNow.Add(-40 * time.Second)
	if err := g.CheckReplay("carol", "ACME.LOCAL", ts, 18); err != nil {
		t.Fatalf("record replay tuple: %v", err)
	}
	before := beginFor(t, g, "dave")
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewAsPreauthGuard(testPolicy())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Failures("carol"); got != 2 {
		t.Fatalf("restored failure counter = %d, want 2", got)
	}
	if err := restored.CheckReplay("carol", "ACME.LOCAL", ts, 18); !errors.Is(err, impl.ErrReplay) {
		t.Fatalf("restored cache must still reject the recorded tuple, got %v", err)
	}
	after := beginFor(t, restored, "dave")
	if after.Seq <= before.Seq {
		t.Fatalf("sequence after restore = %d, must continue past %d", after.Seq, before.Seq)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewAsPreauthGuard(testPolicy())
	begin := beginFor(t, g, "carol")
	if _, err := g.Preauth(begin.SessionID, impl.PreauthData{Client: "carol", Realm: "ACME.LOCAL", EType: 17, Time: clockNow}); !errors.Is(err, impl.ErrWeakEType) {
		t.Fatalf("seed a failure: %v", err)
	}
	if err := g.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := g.Failures("carol"); got != 1 {
		t.Fatalf("failed restore changed the failure counter to %d", got)
	}
	if _, ok := g.Pending(begin.SessionID); !ok {
		t.Fatalf("failed restore dropped the pending session")
	}
}
