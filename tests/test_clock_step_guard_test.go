package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-c-02"
)

func clockPolicy() impl.ClockPolicy {
	return impl.ClockPolicy{
		ManualStepLimit: time.Second,
		NTPStepLimit:    500 * time.Millisecond,
		MaxTickets:      4,
		MaxSubscribers:  4,
		MaxWatermarks:   8,
	}
}

func seedClock(t *testing.T, g *impl.ClockStepGuard, src string) time.Time {
	t.Helper()
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	if err := g.SetSource(src); err != nil {
		t.Fatalf("set source %q: %v", src, err)
	}
	if err := g.SetWall(t0, 1000); err != nil {
		t.Fatalf("seed wall clock: %v", err)
	}
	return t0
}

func TestSetWallSeedsStatus(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	t0 := seedClock(t, g, impl.SourceNTP)
	st := g.Status()
	if !st.Wall.Equal(t0) {
		t.Fatalf("wall = %v, want %v", st.Wall, t0)
	}
	if st.Mono != 1000 {
		t.Fatalf("mono = %d, want 1000", st.Mono)
	}
	if st.Source != impl.SourceNTP {
		t.Fatalf("source = %q, want %q", st.Source, impl.SourceNTP)
	}
	if st.Steps != 0 {
		t.Fatalf("steps = %d, want 0 right after seeding", st.Steps)
	}
	if st.Paused {
		t.Fatalf("clock must not be paused before any step")
	}
}

func TestSetWallRejectsBadReadings(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	if err := g.SetWall(time.Time{}, 1000); !errors.Is(err, impl.ErrBadTimestamp) {
		t.Fatalf("zero wall clock = %v, want ErrBadTimestamp", err)
	}
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	if err := g.SetWall(t0, -1); !errors.Is(err, impl.ErrBadMono) {
		t.Fatalf("negative monotonic reading = %v, want ErrBadMono", err)
	}
	if got := g.Status().Steps; got != 0 {
		t.Fatalf("steps = %d, want 0 after rejected seeds", got)
	}
}

func TestApplyUnknownSourceRejected(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	if err := g.SetWall(t0, 1000); err != nil {
		t.Fatalf("seed wall clock: %v", err)
	}
	if err := g.Apply(t0.Add(100*time.Millisecond), 1100); !errors.Is(err, impl.ErrUntrustedSource) {
		t.Fatalf("apply with an unknown source = %v, want ErrUntrustedSource", err)
	}
	if got := g.Status().Steps; got != 0 {
		t.Fatalf("steps = %d, want 0 after a refused step", got)
	}
	if got := len(g.Watermarks()); got != 0 {
		t.Fatalf("watermarks = %d, want 0 after a refused step", got)
	}
}

func TestApplyNTPWithinLimit(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	t0 := seedClock(t, g, impl.SourceNTP)
	next := t0.Add(200 * time.Millisecond)
	if err := g.Apply(next, 1200); err != nil {
		t.Fatalf("apply a 200ms ntp correction: %v", err)
	}
	st := g.Status()
	if !st.Wall.Equal(next) || st.Mono != 1200 || st.Steps != 1 {
		t.Fatalf("status = %+v, want wall %v mono 1200 steps 1", st, next)
	}
	marks := g.Watermarks()
	if len(marks) != 1 {
		t.Fatalf("watermarks = %+v, want exactly one", marks)
	}
	if !marks[0].Wall.Equal(next) || marks[0].Mono != 1200 {
		t.Fatalf("watermark = %+v, want the applied reading", marks[0])
	}
	if marks[0].Delta != 200*time.Millisecond {
		t.Fatalf("watermark delta = %v, want 200ms", marks[0].Delta)
	}
	if marks[0].Source != impl.SourceNTP {
		t.Fatalf("watermark source = %q, want %q", marks[0].Source, impl.SourceNTP)
	}
}

func TestApplyManualOverLimitNeedsTicket(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	t0 := seedClock(t, g, impl.SourceManual)
	if err := g.Apply(t0.Add(5*time.Second), 6000); !errors.Is(err, impl.ErrApprovalRequired) {
		t.Fatalf("five second manual jump = %v, want ErrApprovalRequired", err)
	}
	if got := g.Status().Steps; got != 0 {
		t.Fatalf("steps = %d, want 0 without a ticket", got)
	}
	if got := len(g.Watermarks()); got != 0 {
		t.Fatalf("watermarks = %d, want 0 without a ticket", got)
	}
}

func TestRequestRejectsBlankReason(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	seedClock(t, g, impl.SourceManual)
	if _, err := g.Request(5*time.Second, "   ", "ops-1"); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason = %v, want ErrBadReason", err)
	}
	if got := g.Pending(); got != 0 {
		t.Fatalf("pending = %d, want 0 after a rejected request", got)
	}
}

func TestRequestRejectsBlankActor(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	seedClock(t, g, impl.SourceManual)
	if _, err := g.Request(5*time.Second, "ntp drift", "  "); !errors.Is(err, impl.ErrBadActor) {
		t.Fatalf("blank actor = %v, want ErrBadActor", err)
	}
	if _, ok := g.Ticket(""); ok {
		t.Fatalf("a rejected request must not leave a ticket behind")
	}
}

func TestRequestIssuesTicketForManualStep(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	t0 := seedClock(t, g, impl.SourceManual)
	tk, err := g.Request(5*time.Second, "cmos battery drift", "ops-2")
	if err != nil {
		t.Fatalf("request ticket: %v", err)
	}
	if tk.ID == "" || tk.State != impl.TicketIssued {
		t.Fatalf("ticket = %+v, want an issued ticket with an id", tk)
	}
	if tk.Delta != 5*time.Second || tk.Source != impl.SourceManual {
		t.Fatalf("ticket delta=%v source=%q, want 5s and manual", tk.Delta, tk.Source)
	}
	if err := g.Apply(t0.Add(5*time.Second), 6000); err != nil {
		t.Fatalf("apply the approved step: %v", err)
	}
	rec, ok := g.Ticket(tk.ID)
	if !ok || rec.State != impl.TicketUsed {
		t.Fatalf("ticket after apply = %+v ok=%v, want state %q", rec, ok, impl.TicketUsed)
	}
	if got := g.Pending(); got != 0 {
		t.Fatalf("pending = %d, want 0 after the ticket is spent", got)
	}
	if got := g.Status().Steps; got != 1 {
		t.Fatalf("steps = %d, want 1", got)
	}
}

func TestApplyRejectsTicketMismatch(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	t0 := seedClock(t, g, impl.SourceManual)
	tk, err := g.Request(5*time.Second, "cmos drift", "ops-3")
	if err != nil {
		t.Fatalf("request ticket: %v", err)
	}
	if err := g.Apply(t0.Add(4*time.Second), 5000); !errors.Is(err, impl.ErrTicketMismatch) {
		t.Fatalf("apply a four second step against a five second ticket = %v, want ErrTicketMismatch", err)
	}
	if got := g.Pending(); got != 1 {
		t.Fatalf("pending = %d, want the ticket to stay usable", got)
	}
	if rec, _ := g.Ticket(tk.ID); rec.State != impl.TicketIssued {
		t.Fatalf("ticket state = %q, want %q after a mismatch", rec.State, impl.TicketIssued)
	}
	if err := g.Apply(t0.Add(5*time.Second), 6000); err != nil {
		t.Fatalf("apply the matching step afterwards: %v", err)
	}
}

func TestTicketLimitEnforced(t *testing.T) {
	cfg := clockPolicy()
	cfg.MaxTickets = 2
	g := impl.NewClockStepGuard(cfg)
	seedClock(t, g, impl.SourceManual)
	for i := 0; i < 2; i++ {
		if _, err := g.Request(time.Duration(i+1)*time.Second, "drift", "ops-4"); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := g.Request(3*time.Second, "drift", "ops-4"); !errors.Is(err, impl.ErrTicketLimit) {
		t.Fatalf("third ticket = %v, want ErrTicketLimit", err)
	}
	if got := g.Pending(); got != 2 {
		t.Fatalf("pending = %d, want the configured cap 2", got)
	}
}

func TestRevokeTicketBlocksApply(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	t0 := seedClock(t, g, impl.SourceManual)
	tk, err := g.Request(5*time.Second, "drift", "ops-5")
	if err != nil {
		t.Fatalf("request ticket: %v", err)
	}
	if err := g.RevokeTicket(tk.ID); err != nil {
		t.Fatalf("revoke ticket: %v", err)
	}
	if err := g.Apply(t0.Add(5*time.Second), 6000); !errors.Is(err, impl.ErrApprovalRequired) {
		t.Fatalf("apply after revoke = %v, want ErrApprovalRequired", err)
	}
	if rec, _ := g.Ticket(tk.ID); rec.State != impl.TicketRevoked {
		t.Fatalf("ticket state = %q, want %q", rec.State, impl.TicketRevoked)
	}
	if err := g.RevokeTicket("ticket-404"); !errors.Is(err, impl.ErrTicketUnknown) {
		t.Fatalf("revoking an unknown ticket = %v, want ErrTicketUnknown", err)
	}
}

func TestSubscribeLifecycle(t *testing.T) {
	cfg := clockPolicy()
	cfg.MaxSubscribers = 1
	g := impl.NewClockStepGuard(cfg)
	if err := g.Subscribe("  "); !errors.Is(err, impl.ErrBadSubscriber) {
		t.Fatalf("blank subscriber = %v, want ErrBadSubscriber", err)
	}
	if err := g.Subscribe("totp"); err != nil {
		t.Fatalf("subscribe totp: %v", err)
	}
	if err := g.Subscribe("totp"); !errors.Is(err, impl.ErrDuplicateSubscriber) {
		t.Fatalf("duplicate subscriber = %v, want ErrDuplicateSubscriber", err)
	}
	if err := g.Subscribe("lease"); !errors.Is(err, impl.ErrSubscriberLimit) {
		t.Fatalf("second subscriber over the cap = %v, want ErrSubscriberLimit", err)
	}
	subs := g.Subscribers()
	if len(subs) != 1 || subs[0].Name != "totp" || subs[0].State != impl.SubscriberActive {
		t.Fatalf("subscribers = %+v, want one active totp", subs)
	}
	if err := g.Unsubscribe("totp"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if got := len(g.Subscribers()); got != 0 {
		t.Fatalf("subscribers = %d, want 0", got)
	}
	if err := g.Unsubscribe("totp"); !errors.Is(err, impl.ErrUnknownSubscriber) {
		t.Fatalf("unsubscribing an unknown name = %v, want ErrUnknownSubscriber", err)
	}
}

func TestLargeStepPausesSubscribers(t *testing.T) {
	g := impl.NewClockStepGuard(clockPolicy())
	t0 := seedClock(t, g, impl.SourceManual)
	if err := g.Subscribe("totp"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := g.Request(5*time.Second, "drift", "ops-6"); err != nil {
		t.Fatalf("request ticket: %v", err)
	}
	if err := g.Apply(t0.Add(5*time.Second), 6000); err != nil {
		t.Fatalf("apply the large step: %v", err)
	}
	subs := g.Subscribers()
	if len(subs) != 1 || subs[0].State != impl.SubscriberPaused {
		t.Fatalf("subscribers = %+v, want the only one paused", subs)
	}
	if !g.Status().Paused {
		t.Fatalf("status must report the pause while a jump above the limit is applied")
	}
	if subs[0].Pauses != 1 {
		t.Fatalf("pause counter = %d, want 1", subs[0].Pauses)
	}
	if err := g.Apply(t0.Add(5*time.Second+100*time.Millisecond), 6100); err != nil {
		t.Fatalf("apply a small recovery step: %v", err)
	}
	subs = g.Subscribers()
	if len(subs) != 1 || subs[0].State != impl.SubscriberActive {
		t.Fatalf("subscribers after recovery = %+v, want the only one active", subs)
	}
	if g.Status().Paused {
		t.Fatalf("status must clear the pause after a small step")
	}
	if subs[0].Pauses != 1 {
		t.Fatalf("pause counter = %d, want it to stay at 1", subs[0].Pauses)
	}
}

func TestWatermarkCapRetainsNewest(t *testing.T) {
	cfg := clockPolicy()
	cfg.MaxWatermarks = 3
	g := impl.NewClockStepGuard(cfg)
	t0 := seedClock(t, g, impl.SourceNTP)
	for i := 1; i <= 5; i++ {
		next := t0.Add(time.Duration(i) * 100 * time.Millisecond)
		if err := g.Apply(next, int64(1000+i*100)); err != nil {
			t.Fatalf("apply step %d: %v", i, err)
		}
	}
	marks := g.Watermarks()
	if len(marks) != 3 {
		t.Fatalf("watermarks = %d, want the configured cap 3", len(marks))
	}
	if marks[0].Seq != 3 || marks[1].Seq != 4 || marks[2].Seq != 5 {
		t.Fatalf("retained sequence = [%d %d %d], want the newest three [3 4 5]", marks[0].Seq, marks[1].Seq, marks[2].Seq)
	}
	if marks[2].Mono != 1500 {
		t.Fatalf("newest watermark mono = %d, want 1500", marks[2].Mono)
	}
	if !marks[2].Wall.Equal(t0.Add(500 * time.Millisecond)) {
		t.Fatalf("newest watermark wall = %v, want the last applied reading", marks[2].Wall)
	}
	for i := 1; i < len(marks); i++ {
		if marks[i].Seq <= marks[i-1].Seq {
			t.Fatalf("watermark sequence must be strictly increasing, got %+v", marks)
		}
	}
}

func TestSnapshotRestoreKeepsClock(t *testing.T) {
	cfg := clockPolicy()
	g := impl.NewClockStepGuard(cfg)
	t0 := seedClock(t, g, impl.SourceNTP)
	for i := 1; i <= 2; i++ {
		if err := g.Apply(t0.Add(time.Duration(i)*100*time.Millisecond), int64(1000+i*100)); err != nil {
			t.Fatalf("apply step %d: %v", i, err)
		}
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewClockStepGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	st := restored.Status()
	if !st.Wall.Equal(t0.Add(200 * time.Millisecond)) {
		t.Fatalf("restored wall = %v, want the last applied reading", st.Wall)
	}
	if st.Mono != 1200 || st.Steps != 2 {
		t.Fatalf("restored mono=%d steps=%d, want 1200 and 2", st.Mono, st.Steps)
	}
	if got := len(restored.Watermarks()); got != 2 {
		t.Fatalf("restored watermarks = %d, want 2", got)
	}
	if err := restored.Restore([]byte("{not-a-clock-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	if got := restored.Status().Steps; got != 2 {
		t.Fatalf("a rejected restore must keep the live clock, got steps %d", got)
	}
}
