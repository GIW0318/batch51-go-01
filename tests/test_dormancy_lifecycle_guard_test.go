package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-07"
)

var dormBase = time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)

func testDormancyPolicy() impl.DormancyPolicy {
	return impl.DormancyPolicy{
		Tenant:            "acme",
		IdleAfter:         time.Hour,
		WarningAfter:      2 * time.Hour,
		SuspendAfter:      3 * time.Hour,
		ReclaimAfter:      4 * time.Hour,
		HoldAfter:         30 * 24 * time.Hour,
		ShardSize:         4,
		BatchLimit:        16,
		MaxPendingNotices: 8,
		MaxExempt:         4,
		Now:               func() time.Time { return dormBase },
	}
}

func sweepTo(t *testing.T, g *impl.DormancyLifecycleGuard, account, want string, at time.Time) {
	t.Helper()
	step := at
	for i := 0; i < 8; i++ {
		if _, err := g.Sweep(step); err != nil {
			t.Fatalf("sweep step %d: %v", i, err)
		}
		st, err := g.State(account)
		if err != nil {
			t.Fatalf("state of %s: %v", account, err)
		}
		if st.Level == want {
			return
		}
		step = step.Add(time.Second)
	}
	t.Fatalf("account %s never reached level %s", account, want)
}

func TestRegisterCreatesActiveState(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("register: %v", err)
	}
	st, err := g.State("acct-1")
	if err != nil {
		t.Fatalf("state of a registered account: %v", err)
	}
	if st.Level != impl.LevelActive || st.Tenant != "acme" {
		t.Fatalf("state = %+v, want an active acme account", st)
	}
	if !st.LastActive.Equal(dormBase) || !st.Since.Equal(dormBase) {
		t.Fatalf("watermarks = %v / %v, want the registration instant", st.LastActive, st.Since)
	}
	if st.WarningCount != 0 || st.Reclaimed {
		t.Fatalf("fresh account must start clean, got %+v", st)
	}
}

func TestRegisterRejectsDuplicateBlankAndExisting(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Register("   ", dormBase); !errors.Is(err, impl.ErrBlankAccount) {
		t.Fatalf("blank account must fail with ErrBlankAccount, got %v", err)
	}
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := g.Register("acct-1", dormBase); !errors.Is(err, impl.ErrAccountExists) {
		t.Fatalf("second register must fail with ErrAccountExists, got %v", err)
	}
}

func TestTouchUpdatesWatermark(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("register: %v", err)
	}
	later := dormBase.Add(30 * time.Minute)
	if err := g.Touch("acct-1", later, "login"); err != nil {
		t.Fatalf("touch with a login source: %v", err)
	}
	st, _ := g.State("acct-1")
	if !st.LastActive.Equal(later) {
		t.Fatalf("watermark = %v, want %v", st.LastActive, later)
	}
	if st.Level != impl.LevelActive {
		t.Fatalf("level = %q, want active after a successful sign in", st.Level)
	}
	if err := g.Touch("acct-1", later, ""); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("touch without a source must fail with ErrBadSource, got %v", err)
	}
}

func TestTouchRejectsUnknownAndBlocked(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Touch("ghost", dormBase, "login"); !errors.Is(err, impl.ErrUnknownAccount) {
		t.Fatalf("touch on an untracked account must fail with ErrUnknownAccount, got %v", err)
	}
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("register: %v", err)
	}
	sweepTo(t, g, "acct-1", impl.LevelSuspended, dormBase.Add(5*time.Hour))
	if err := g.Touch("acct-1", dormBase.Add(5*time.Hour+time.Minute), "login"); !errors.Is(err, impl.ErrLoginBlocked) {
		t.Fatalf("a suspended account must reject sign in, got %v", err)
	}
}

func TestSweepAdvancesOneLevelPerCall(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("register: %v", err)
	}
	at := dormBase.Add(5 * time.Hour)
	first, err := g.Sweep(at)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first sweep produced %d transitions, want 1", len(first))
	}
	if first[0].From != impl.LevelActive || first[0].To != impl.LevelIdle {
		t.Fatalf("transition = %s -> %s, want active -> idle", first[0].From, first[0].To)
	}
	if !first[0].At.Equal(at) || first[0].Seq <= 0 {
		t.Fatalf("transition time/seq = %v/%d, want the sweep instant and a positive seq", first[0].At, first[0].Seq)
	}
	again, err := g.Sweep(at)
	if err != nil {
		t.Fatalf("repeated sweep: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("a repeated sweep at the same instant produced %d transitions, want 0", len(again))
	}
	second, err := g.Sweep(at.Add(time.Second))
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(second) != 1 || second[0].To != impl.LevelWarning {
		t.Fatalf("second sweep = %+v, want a single idle -> warning transition", second)
	}
	st, _ := g.State("acct-1")
	if st.WarningCount != 1 {
		t.Fatalf("warning count = %d, want 1 after entering warning", st.WarningCount)
	}
}

func TestSweepWarningEmitsCancelableNotice(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("register: %v", err)
	}
	sweepTo(t, g, "acct-1", impl.LevelWarning, dormBase.Add(5*time.Hour))
	if got := g.PendingNotices("acme"); got != 1 {
		t.Fatalf("pending notices = %d, want 1 when the account enters warning", got)
	}
	notices := g.Notices("acme")
	if len(notices) != 1 {
		t.Fatalf("notice list = %+v, want one entry", notices)
	}
	if !notices[0].Cancelable || notices[0].Cancelled {
		t.Fatalf("notice = %+v, want an outstanding cancelable notice", notices[0])
	}
	if notices[0].Account != "acct-1" || notices[0].Level != impl.LevelWarning {
		t.Fatalf("notice = %+v, want it attached to acct-1 at warning", notices[0])
	}
	if err := g.CancelNotice("acct-1"); err != nil {
		t.Fatalf("cancel notice: %v", err)
	}
	if got := g.PendingNotices("acme"); got != 0 {
		t.Fatalf("pending notices after cancel = %d, want 0", got)
	}
	if err := g.CancelNotice("acct-1"); !errors.Is(err, impl.ErrNoticeNotFound) {
		t.Fatalf("cancelling twice must fail with ErrNoticeNotFound, got %v", err)
	}
}

func TestSweepRespectsBatchLimit(t *testing.T) {
	cfg := testDormancyPolicy()
	cfg.BatchLimit = 2
	g := impl.NewDormancyLifecycleGuard(cfg)
	names := []string{"acct-1", "acct-2", "acct-3", "acct-4", "acct-5"}
	for _, name := range names {
		if err := g.Register(name, dormBase); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	at := dormBase.Add(5 * time.Hour)
	seen := map[string]int{}
	counts := []int{}
	for i := 0; i < 3; i++ {
		tr, err := g.Sweep(at)
		if err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		counts = append(counts, len(tr))
		for _, one := range tr {
			seen[one.Account]++
			if one.To != impl.LevelIdle {
				t.Fatalf("sweep %d moved %s to %s, want idle", i, one.Account, one.To)
			}
		}
	}
	for i, want := range []int{2, 2, 1} {
		if counts[i] != want {
			t.Fatalf("sweep batch counts = %v, want %v", counts, []int{2, 2, 1})
		}
	}
	for _, name := range names {
		if seen[name] != 1 {
			t.Fatalf("account %s advanced %d times across the batch, want exactly 1", name, seen[name])
		}
	}
	if extra, err := g.Sweep(at); err != nil || len(extra) != 0 {
		t.Fatalf("sweep after all accounts moved = %d transitions err=%v, want 0", len(extra), err)
	}
}

func TestSweepSkipsExemptAccounts(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	for _, name := range []string{"acct-1", "acct-2"} {
		if err := g.Register(name, dormBase); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if err := g.Exempt("acct-1", "legal hold", dormBase.Add(48*time.Hour)); err != nil {
		t.Fatalf("exempt: %v", err)
	}
	tr, err := g.Sweep(dormBase.Add(5 * time.Hour))
	if err != nil {
		t.Fatalf("sweep while exempt: %v", err)
	}
	if len(tr) != 1 || tr[0].Account != "acct-2" {
		t.Fatalf("sweep = %+v, want only acct-2 because acct-1 is exempt", tr)
	}
	if st, _ := g.State("acct-1"); st.Level != impl.LevelActive {
		t.Fatalf("exempt account level = %q, want active", st.Level)
	}
	tr, err = g.Sweep(dormBase.Add(49 * time.Hour))
	if err != nil {
		t.Fatalf("sweep after the exemption expires: %v", err)
	}
	found := false
	for _, one := range tr {
		if one.Account == "acct-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sweep after expiry = %+v, want acct-1 to move again", tr)
	}
}

func TestExemptValidatesReasonAndCapacity(t *testing.T) {
	cfg := testDormancyPolicy()
	cfg.MaxExempt = 2
	g := impl.NewDormancyLifecycleGuard(cfg)
	if err := g.Exempt("", "audit", dormBase.Add(time.Hour)); !errors.Is(err, impl.ErrBlankAccount) {
		t.Fatalf("blank account must fail with ErrBlankAccount, got %v", err)
	}
	if err := g.Exempt("ghost", "audit", dormBase.Add(time.Hour)); !errors.Is(err, impl.ErrUnknownAccount) {
		t.Fatalf("untracked account must fail with ErrUnknownAccount, got %v", err)
	}
	for _, name := range []string{"acct-1", "acct-2", "acct-3"} {
		if err := g.Register(name, dormBase); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if err := g.Exempt("acct-1", "  ", dormBase.Add(time.Hour)); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must fail with ErrBadReason, got %v", err)
	}
	if err := g.Exempt("acct-1", "audit", dormBase.Add(time.Hour)); err != nil {
		t.Fatalf("first exemption: %v", err)
	}
	if err := g.Exempt("acct-2", "audit", dormBase.Add(time.Hour)); err != nil {
		t.Fatalf("second exemption: %v", err)
	}
	if err := g.Exempt("acct-3", "audit", dormBase.Add(time.Hour)); !errors.Is(err, impl.ErrExemptionLimit) {
		t.Fatalf("third exemption must hit ErrExemptionLimit, got %v", err)
	}
}

func TestReactivateRequiresProofForDepth(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("register: %v", err)
	}
	sweepTo(t, g, "acct-1", impl.LevelSuspended, dormBase.Add(5*time.Hour))
	if err := g.Reactivate("acct-1", impl.Proof{Kind: impl.ProofSelfService, Verified: true}); !errors.Is(err, impl.ErrProofRejected) {
		t.Fatalf("a suspended account needs a manager proof, got %v", err)
	}
	if err := g.Reactivate("acct-1", impl.Proof{Kind: impl.ProofManager, Verified: false}); !errors.Is(err, impl.ErrProofRejected) {
		t.Fatalf("an unverified proof must be rejected, got %v", err)
	}
	if st, _ := g.State("acct-1"); st.Level != impl.LevelSuspended {
		t.Fatalf("failed reactivation changed the level to %q", st.Level)
	}
	if err := g.Reactivate("acct-1", impl.Proof{Kind: impl.ProofManager, Verified: true, Ticket: "CHG-77"}); err != nil {
		t.Fatalf("manager proof must reactivate the account, got %v", err)
	}
	st, _ := g.State("acct-1")
	if st.Level != impl.LevelActive {
		t.Fatalf("level after reactivation = %q, want active", st.Level)
	}
	if !st.LastActive.Equal(dormBase) {
		t.Fatalf("watermark after reactivation = %v, want the current clock %v", st.LastActive, dormBase)
	}
	if err := g.Touch("acct-1", dormBase.Add(time.Minute), "login"); err != nil {
		t.Fatalf("a reactivated account must accept sign in, got %v", err)
	}
}

func TestSweepStopsWhenNoticeBudgetExhausted(t *testing.T) {
	cfg := testDormancyPolicy()
	cfg.MaxPendingNotices = 1
	g := impl.NewDormancyLifecycleGuard(cfg)
	for _, name := range []string{"acct-1", "acct-2"} {
		if err := g.Register(name, dormBase); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if _, err := g.Sweep(dormBase.Add(5 * time.Hour)); err != nil {
		t.Fatalf("sweep to idle: %v", err)
	}
	tr, err := g.Sweep(dormBase.Add(5*time.Hour + time.Second))
	if !errors.Is(err, impl.ErrNoticeBudget) {
		t.Fatalf("the second warning must exhaust the notice budget, got %v", err)
	}
	if len(tr) != 1 || tr[0].Account != "acct-1" {
		t.Fatalf("transitions = %+v, want only the first warning", tr)
	}
	if got := g.PendingNotices("acme"); got != 1 {
		t.Fatalf("pending notices = %d, want the budget 1", got)
	}
	if st, _ := g.State("acct-2"); st.Level != impl.LevelIdle {
		t.Fatalf("blocked account level = %q, want idle until the budget frees up", st.Level)
	}
	if err := g.CancelNotice("acct-1"); err != nil {
		t.Fatalf("cancel notice: %v", err)
	}
	if _, err := g.Sweep(dormBase.Add(5*time.Hour + 2*time.Second)); err != nil {
		t.Fatalf("sweep after the budget frees up: %v", err)
	}
	if st, _ := g.State("acct-2"); st.Level != impl.LevelWarning {
		t.Fatalf("blocked account level = %q, want warning once budget allows", st.Level)
	}
}

func TestHistoryRecordsTransitionsInOrder(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("register: %v", err)
	}
	sweepTo(t, g, "acct-1", impl.LevelReclaimable, dormBase.Add(5*time.Hour))
	history := g.History("acct-1")
	want := []string{impl.LevelIdle, impl.LevelWarning, impl.LevelSuspended, impl.LevelReclaimable}
	if len(history) != len(want) {
		t.Fatalf("history length = %d, want %d (%+v)", len(history), len(want), history)
	}
	for i, level := range want {
		if history[i].To != level {
			t.Fatalf("history[%d] = %s -> %s, want arrival at %s", i, history[i].From, history[i].To, level)
		}
		if i > 0 {
			if history[i].From != history[i-1].To {
				t.Fatalf("history chain broken at %d: %s -> %s", i, history[i].From, history[i].To)
			}
			if history[i].Seq <= history[i-1].Seq {
				t.Fatalf("history sequence not increasing: %+v", history)
			}
		}
	}
	if got := g.History("ghost"); len(got) != 0 {
		t.Fatalf("untracked account history = %+v, want empty", got)
	}
}

func TestSweepStopsAtReclaimable(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("register: %v", err)
	}
	sweepTo(t, g, "acct-1", impl.LevelReclaimable, dormBase.Add(5*time.Hour))
	st, _ := g.State("acct-1")
	if !st.Reclaimed {
		t.Fatalf("a reclaimable account must be marked reclaimed")
	}
	tr, err := g.Sweep(dormBase.Add(40 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("sweep past reclaimable: %v", err)
	}
	if len(tr) != 0 {
		t.Fatalf("sweep past reclaimable produced %d transitions, want 0", len(tr))
	}
	if st, _ := g.State("acct-1"); st.Level != impl.LevelReclaimable {
		t.Fatalf("level = %q, want reclaimable to be terminal", st.Level)
	}
}

func TestSnapshotRestoreResumesCursor(t *testing.T) {
	cfg := testDormancyPolicy()
	cfg.BatchLimit = 2
	g := impl.NewDormancyLifecycleGuard(cfg)
	names := []string{"acct-1", "acct-2", "acct-3", "acct-4"}
	for _, name := range names {
		if err := g.Register(name, dormBase); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	at := dormBase.Add(5 * time.Hour)
	first, err := g.Sweep(at)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	lastSeq := first[len(first)-1].Seq
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewDormancyLifecycleGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if st, err := restored.State(names[0]); err != nil || st.Level != impl.LevelIdle {
		t.Fatalf("restored state of %s = %+v err=%v, want idle", names[0], st, err)
	}
	tr, err := restored.Sweep(at.Add(time.Second))
	if err != nil {
		t.Fatalf("sweep after restore: %v", err)
	}
	idleMoves := 0
	for _, one := range tr {
		if one.To == impl.LevelIdle {
			idleMoves++
		}
		if one.Seq <= lastSeq {
			t.Fatalf("sequence after restore = %d, must continue past %d", one.Seq, lastSeq)
		}
	}
	if idleMoves != 2 {
		t.Fatalf("sweep after restore made %d idle moves, want the remaining 2 accounts", idleMoves)
	}
	for _, name := range names {
		st, err := restored.State(name)
		if err != nil {
			t.Fatalf("state of %s: %v", name, err)
		}
		if st.Level != impl.LevelIdle {
			t.Fatalf("account %s level = %q, want every account swept exactly once", name, st.Level)
		}
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewDormancyLifecycleGuard(testDormancyPolicy())
	if err := g.Register("acct-1", dormBase); err != nil {
		t.Fatalf("register: %v", err)
	}
	touched := dormBase.Add(15 * time.Minute)
	if err := g.Touch("acct-1", touched, "login"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if err := g.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	st, err := g.State("acct-1")
	if err != nil {
		t.Fatalf("state after a rejected restore: %v", err)
	}
	if st.Level != impl.LevelActive || !st.LastActive.Equal(touched) {
		t.Fatalf("rejected restore changed the live state to %+v", st)
	}
}
