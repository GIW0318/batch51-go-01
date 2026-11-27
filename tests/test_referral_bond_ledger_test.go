package task

import (
	"errors"
	"strings"
	"testing"
	"time"

	impl "gsb/batch51-go-game-a-08"
)

func referralPolicy() impl.ReferralPolicy {
	return impl.ReferralPolicy{
		BindWindowHours:    72,
		MaxBindingsPerCode: 3,
		MaxCodesPerAccount: 3,
		RetryAfterSeconds:  600,
		Thresholds: map[impl.Milestone]int64{
			impl.MilestoneLevel10: 100,
			impl.MilestoneLevel30: 300,
			impl.MilestonePaid:    1,
			impl.MilestoneActive:  7,
		},
		Rewards: map[impl.Milestone]int64{
			impl.MilestoneLevel10: 100,
			impl.MilestoneLevel30: 250,
			impl.MilestonePaid:    80,
			impl.MilestoneActive:  60,
		},
	}
}

func ledgerBase() time.Time {
	return time.Date(2026, 5, 6, 9, 0, 0, 0, time.UTC)
}

func newLedgerWithAccounts(t *testing.T, ids ...string) *impl.ReferralBondLedger {
	t.Helper()
	l := impl.NewReferralBondLedger(referralPolicy())
	base := ledgerBase()
	for _, id := range ids {
		if err := l.Register(id, base); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	return l
}

func mustCode(t *testing.T, l *impl.ReferralBondLedger, account string) string {
	t.Helper()
	code, err := l.IssueCode(account)
	if err != nil {
		t.Fatalf("issue code for %s: %v", account, err)
	}
	return code
}

func mustBind(t *testing.T, l *impl.ReferralBondLedger, invitee, code string) impl.Bond {
	t.Helper()
	bond, err := l.Bind(invitee, code, ledgerBase().Add(time.Hour))
	if err != nil {
		t.Fatalf("bind %s: %v", invitee, err)
	}
	return bond
}

func hasMilestone(list []impl.Milestone, want impl.Milestone) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

func TestIssueCodeFormatAndLimit(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice")
	first := mustCode(t, l, "alice")
	if first != "alice#0001" {
		t.Fatalf("first code = %q, want alice#0001", first)
	}
	second := mustCode(t, l, "alice")
	if second != "alice#0002" {
		t.Fatalf("second code = %q, want alice#0002", second)
	}
	if first == second {
		t.Fatalf("two issued codes collapsed to %q", first)
	}
	mustCode(t, l, "alice")
	if _, err := l.IssueCode("alice"); !errors.Is(err, impl.ErrCodeLimit) {
		t.Fatalf("fourth code must yield ErrCodeLimit, got %v", err)
	}
	codes := l.Codes("alice")
	if len(codes) != 3 {
		t.Fatalf("codes = %v, want three codes", codes)
	}
	if codes[0] != "alice#0001" || codes[2] != "alice#0003" {
		t.Fatalf("codes not in issue order: %v", codes)
	}
	if _, err := l.IssueCode("   "); !errors.Is(err, impl.ErrEmptyAccount) {
		t.Fatalf("blank account must yield ErrEmptyAccount, got %v", err)
	}
	if _, err := l.IssueCode("ghost"); !errors.Is(err, impl.ErrUnknownAccount) {
		t.Fatalf("unknown account must yield ErrUnknownAccount, got %v", err)
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	l := impl.NewReferralBondLedger(referralPolicy())
	if err := l.Register("  ", ledgerBase()); !errors.Is(err, impl.ErrEmptyAccount) {
		t.Fatalf("blank account must yield ErrEmptyAccount, got %v", err)
	}
	if err := l.Register("alice", ledgerBase()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := l.Register("alice", ledgerBase()); !errors.Is(err, impl.ErrAccountExists) {
		t.Fatalf("duplicate register must yield ErrAccountExists, got %v", err)
	}
	if got := len(l.Codes("alice")); got != 0 {
		t.Fatalf("registration must not mint codes, got %d", got)
	}
}

func TestBindRejectsUnknownCode(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob")
	mustCode(t, l, "alice")
	if _, err := l.Bind("bob", "alice#0099", ledgerBase().Add(time.Hour)); !errors.Is(err, impl.ErrUnknownCode) {
		t.Fatalf("unknown code must yield ErrUnknownCode, got %v", err)
	}
	if _, err := l.Bind("bob", "", ledgerBase().Add(time.Hour)); !errors.Is(err, impl.ErrUnknownCode) {
		t.Fatalf("blank code must yield ErrUnknownCode, got %v", err)
	}
	if _, ok := l.BondOf("bob"); ok {
		t.Fatalf("rejected bind must not create a bond")
	}
}

func TestBindRejectsSelfReferral(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice")
	code := mustCode(t, l, "alice")
	if _, err := l.Bind("alice", code, ledgerBase().Add(time.Hour)); !errors.Is(err, impl.ErrSelfReferral) {
		t.Fatalf("self referral must yield ErrSelfReferral, got %v", err)
	}
	if _, ok := l.BondOf("alice"); ok {
		t.Fatalf("self referral must not create a bond")
	}
}

func TestBindRejectsWindowClosed(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob")
	code := mustCode(t, l, "alice")
	base := ledgerBase()
	if _, err := l.Bind("bob", code, base.Add(73*time.Hour)); !errors.Is(err, impl.ErrBindWindowClosed) {
		t.Fatalf("binding after the window must yield ErrBindWindowClosed, got %v", err)
	}
	if _, ok := l.BondOf("bob"); ok {
		t.Fatalf("late bind must not create a bond")
	}
	if _, err := l.Bind("bob", code, base.Add(71*time.Hour)); err != nil {
		t.Fatalf("binding inside the window must succeed, got %v", err)
	}
}

func TestBindRejectsDuplicateBinding(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob")
	first := mustCode(t, l, "alice")
	second := mustCode(t, l, "alice")
	bond := mustBind(t, l, "bob", first)
	if _, err := l.Bind("bob", second, ledgerBase().Add(2*time.Hour)); !errors.Is(err, impl.ErrAlreadyBound) {
		t.Fatalf("second bind must yield ErrAlreadyBound, got %v", err)
	}
	kept, ok := l.BondOf("bob")
	if !ok {
		t.Fatalf("bond disappeared after the rejected rebind")
	}
	if kept.Code != bond.Code || kept.Code != first {
		t.Fatalf("rejected rebind changed the code to %q, want %q", kept.Code, first)
	}
}

func TestBindRecordsBond(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob")
	code := mustCode(t, l, "alice")
	at := ledgerBase().Add(2 * time.Hour)
	bond, err := l.Bind("bob", code, at)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if bond.InviteeID != "bob" || bond.InviterID != "alice" {
		t.Fatalf("bond identities = %q/%q, want bob/alice", bond.InviteeID, bond.InviterID)
	}
	if bond.Code != code {
		t.Fatalf("bond code = %q, want %q", bond.Code, code)
	}
	if !bond.BoundAt.Equal(at) {
		t.Fatalf("bond bound at %v, want %v", bond.BoundAt, at)
	}
	if bond.Quarantined {
		t.Fatalf("a first binding must not be quarantined")
	}
	stored, ok := l.BondOf("bob")
	if !ok || stored.Code != code {
		t.Fatalf("BondOf = %+v/%v, want the stored bond of %q", stored, ok, code)
	}
}

func TestBindQuarantinesSharedFingerprint(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob", "carol")
	code := mustCode(t, l, "alice")
	at := ledgerBase().Add(time.Hour)
	first, err := l.BindWithFingerprint("bob", code, impl.Fingerprint{Device: "dev-shared"}, at)
	if err != nil {
		t.Fatalf("first fingerprinted bind: %v", err)
	}
	if first.Quarantined {
		t.Fatalf("the first account on a device must not be quarantined")
	}
	second, err := l.BindWithFingerprint("carol", code, impl.Fingerprint{Device: "dev-shared"}, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("second fingerprinted bind: %v", err)
	}
	if !second.Quarantined {
		t.Fatalf("a second account on the same device must be quarantined")
	}
	list := l.Quarantined()
	if len(list) != 1 || list[0] != "carol" {
		t.Fatalf("quarantine list = %v, want [carol]", list)
	}
}

func TestAccrueRejectsBadInput(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob")
	code := mustCode(t, l, "alice")
	if err := l.Accrue("bob", impl.MilestoneLevel10, 10); !errors.Is(err, impl.ErrNotBound) {
		t.Fatalf("accrue before bind must yield ErrNotBound, got %v", err)
	}
	mustBind(t, l, "bob", code)
	if err := l.Accrue("bob", impl.MilestoneLevel10, 0); !errors.Is(err, impl.ErrBadDelta) {
		t.Fatalf("zero delta must yield ErrBadDelta, got %v", err)
	}
	if err := l.Accrue("bob", impl.MilestoneLevel10, -3); !errors.Is(err, impl.ErrBadDelta) {
		t.Fatalf("negative delta must yield ErrBadDelta, got %v", err)
	}
	if err := l.Accrue("bob", impl.Milestone("level_99"), 1); !errors.Is(err, impl.ErrUnknownMilestone) {
		t.Fatalf("unknown milestone must yield ErrUnknownMilestone, got %v", err)
	}
	if err := l.Accrue("ghost", impl.MilestoneLevel10, 1); !errors.Is(err, impl.ErrNotBound) {
		t.Fatalf("unknown invitee must yield ErrNotBound, got %v", err)
	}
}

func TestAccrueUnlocksAtThreshold(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob")
	mustBind(t, l, "bob", mustCode(t, l, "alice"))
	if err := l.Accrue("bob", impl.MilestoneLevel10, 60); err != nil {
		t.Fatalf("first accrue: %v", err)
	}
	progress, err := l.Progress("bob")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if progress.Credited[impl.MilestoneLevel10] != 60 || progress.Totals[impl.MilestoneLevel10] != 60 {
		t.Fatalf("credited/total after 60 = %d/%d, want 60/60",
			progress.Credited[impl.MilestoneLevel10], progress.Totals[impl.MilestoneLevel10])
	}
	if hasMilestone(progress.Unlocked, impl.MilestoneLevel10) {
		t.Fatalf("milestone unlocked before the threshold: %v", progress.Unlocked)
	}
	if err := l.Accrue("bob", impl.MilestoneLevel10, 50); err != nil {
		t.Fatalf("second accrue: %v", err)
	}
	progress, err = l.Progress("bob")
	if err != nil {
		t.Fatalf("progress after threshold: %v", err)
	}
	if progress.Credited[impl.MilestoneLevel10] != 110 {
		t.Fatalf("credited after 110 = %d, want 110", progress.Credited[impl.MilestoneLevel10])
	}
	if !hasMilestone(progress.Unlocked, impl.MilestoneLevel10) {
		t.Fatalf("milestone not unlocked at the threshold: %v", progress.Unlocked)
	}
}

func TestClaimPaysUnlockedMilestones(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob")
	mustBind(t, l, "bob", mustCode(t, l, "alice"))
	if err := l.Accrue("bob", impl.MilestoneLevel10, 100); err != nil {
		t.Fatalf("accrue: %v", err)
	}
	grant, err := l.Claim("bob")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if grant.Amount != 100 {
		t.Fatalf("grant amount = %d, want 100", grant.Amount)
	}
	if len(grant.Milestones) != 1 || grant.Milestones[0] != impl.MilestoneLevel10 {
		t.Fatalf("grant milestones = %v, want [level_10]", grant.Milestones)
	}
	if grant.Balance != 100 {
		t.Fatalf("grant balance = %d, want 100", grant.Balance)
	}
	if grant.Seq != 1 {
		t.Fatalf("grant sequence = %d, want 1", grant.Seq)
	}
	balance, err := l.Balance("bob")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 100 {
		t.Fatalf("ledger balance = %d, want 100", balance)
	}
}

func TestClaimIsIdempotent(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob")
	mustBind(t, l, "bob", mustCode(t, l, "alice"))
	if err := l.Accrue("bob", impl.MilestoneLevel10, 100); err != nil {
		t.Fatalf("accrue: %v", err)
	}
	if _, err := l.Claim("bob"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	second, err := l.Claim("bob")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second.Amount != 0 {
		t.Fatalf("repeat claim paid %d, want 0", second.Amount)
	}
	if len(second.Milestones) != 0 {
		t.Fatalf("repeat claim reported milestones %v, want none", second.Milestones)
	}
	balance, err := l.Balance("bob")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 100 {
		t.Fatalf("balance after repeat claim = %d, want 100", balance)
	}
	if err := l.Accrue("bob", impl.MilestonePaid, 1); err != nil {
		t.Fatalf("accrue paid milestone: %v", err)
	}
	third, err := l.Claim("bob")
	if err != nil {
		t.Fatalf("third claim: %v", err)
	}
	if third.Amount != 80 {
		t.Fatalf("third claim amount = %d, want 80", third.Amount)
	}
	if third.Balance != 180 {
		t.Fatalf("balance after third claim = %d, want 180", third.Balance)
	}
}

func TestReverseRecyclesUnclaimedRewards(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob")
	mustBind(t, l, "bob", mustCode(t, l, "alice"))
	if err := l.Accrue("bob", impl.MilestoneLevel10, 100); err != nil {
		t.Fatalf("accrue: %v", err)
	}
	clawback, err := l.Reverse("bob", "chargeback")
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if clawback.Reversed != 100 {
		t.Fatalf("reversed = %d, want 100", clawback.Reversed)
	}
	if clawback.Recovered != 0 {
		t.Fatalf("recovered = %d, want 0 for a never claimed reward", clawback.Recovered)
	}
	if clawback.Reason != "chargeback" {
		t.Fatalf("clawback reason = %q, want chargeback", clawback.Reason)
	}
	grant, err := l.Claim("bob")
	if err != nil {
		t.Fatalf("claim after reverse: %v", err)
	}
	if grant.Amount != 0 {
		t.Fatalf("recycled milestone still paid %d, want 0", grant.Amount)
	}
	progress, err := l.Progress("bob")
	if err != nil {
		t.Fatalf("progress after reverse: %v", err)
	}
	if hasMilestone(progress.Unlocked, impl.MilestoneLevel10) {
		t.Fatalf("recycled milestone is still unlocked: %v", progress.Unlocked)
	}
	if progress.Credited[impl.MilestoneLevel10] != 100 {
		t.Fatalf("reversal must keep the accrued total, got %d", progress.Credited[impl.MilestoneLevel10])
	}
}

func TestReverseRejectsBadInput(t *testing.T) {
	l := newLedgerWithAccounts(t, "alice", "bob", "carol")
	mustBind(t, l, "bob", mustCode(t, l, "alice"))
	if _, err := l.Reverse("bob", "  "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must yield ErrBadReason, got %v", err)
	}
	if _, err := l.Reverse("ghost", "refund"); !errors.Is(err, impl.ErrUnknownAccount) {
		t.Fatalf("unknown account must yield ErrUnknownAccount, got %v", err)
	}
	if _, err := l.Reverse("carol", "refund"); !errors.Is(err, impl.ErrNotBound) {
		t.Fatalf("unbound account must yield ErrNotBound, got %v", err)
	}
	if _, err := l.Claim("carol"); !errors.Is(err, impl.ErrNotBound) {
		t.Fatalf("claim by an unbound account must yield ErrNotBound, got %v", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	policy := referralPolicy()
	base := ledgerBase()
	src := impl.NewReferralBondLedger(policy)
	for _, id := range []string{"alice", "bob", "carol"} {
		if err := src.Register(id, base); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	code := mustCode(t, src, "alice")
	second := mustCode(t, src, "alice")
	if _, err := src.BindWithFingerprint("bob", code, impl.Fingerprint{Device: "dev-a"}, base.Add(time.Hour)); err != nil {
		t.Fatalf("bind bob: %v", err)
	}
	if _, err := src.BindWithFingerprint("carol", second, impl.Fingerprint{Device: "dev-a"}, base.Add(time.Hour)); err != nil {
		t.Fatalf("bind carol: %v", err)
	}
	if err := src.Accrue("bob", impl.MilestoneLevel10, 100); err != nil {
		t.Fatalf("accrue: %v", err)
	}
	if _, err := src.Claim("bob"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	blob, err := src.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	dst := impl.NewReferralBondLedger(policy)
	if err := dst.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	codes := dst.Codes("alice")
	if strings.Join(codes, ",") != "alice#0001,alice#0002" {
		t.Fatalf("restored codes = %v, want alice#0001,alice#0002", codes)
	}
	bond, ok := dst.BondOf("bob")
	if !ok || bond.Code != code || !bond.BoundAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("restored bond = %+v/%v", bond, ok)
	}
	balance, err := dst.Balance("bob")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 100 {
		t.Fatalf("restored balance = %d, want 100", balance)
	}
	if list := dst.Quarantined(); len(list) != 1 || list[0] != "carol" {
		t.Fatalf("restored quarantine list = %v, want [carol]", list)
	}
	if err := dst.Accrue("bob", impl.MilestoneLevel30, 300); err != nil {
		t.Fatalf("accrue after restore: %v", err)
	}
	grant, err := dst.Claim("bob")
	if err != nil {
		t.Fatalf("claim after restore: %v", err)
	}
	if grant.Amount != 250 {
		t.Fatalf("post restore claim amount = %d, want 250", grant.Amount)
	}
	if grant.Seq != 2 {
		t.Fatalf("post restore claim sequence = %d, want 2", grant.Seq)
	}
	if err := dst.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must yield ErrBadSnapshot, got %v", err)
	}
	after, err := dst.Balance("bob")
	if err != nil {
		t.Fatalf("balance after corrupt restore: %v", err)
	}
	if after != 350 {
		t.Fatalf("corrupt restore changed the balance to %d, want 350", after)
	}
}
