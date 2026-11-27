package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-a-24"
)

func auditLimits() impl.AuditLimits {
	return impl.AuditLimits{MaxAccounts: 8, MaxCurrencies: 4, MaxWindowEntries: 64}
}

func newAuditor(t *testing.T) *impl.CurrencySupplyAuditor {
	t.Helper()
	return impl.NewCurrencySupplyAuditor(auditLimits())
}

func register(t *testing.T, a *impl.CurrencySupplyAuditor, acct, currency string) {
	t.Helper()
	if err := a.RegisterAccount(acct, currency); err != nil {
		t.Fatalf("register %s/%s: %v", acct, currency, err)
	}
}

func book(t *testing.T, a *impl.CurrencySupplyAuditor, id, acct, currency string, amount, bizTime int64) {
	t.Helper()
	err := a.Record(impl.LedgerEntry{
		EntryID:  id,
		Account:  acct,
		Currency: currency,
		Amount:   amount,
		BizTime:  bizTime,
		Kind:     "issue",
	})
	if err != nil {
		t.Fatalf("record %s: %v", id, err)
	}
}

func TestRegisterAccountRejectsBlankAndDuplicate(t *testing.T) {
	a := newAuditor(t)
	if err := a.RegisterAccount("  ", "gold"); !errors.Is(err, impl.ErrBadAccount) {
		t.Fatalf("blank account = %v, want ErrBadAccount", err)
	}
	if err := a.RegisterAccount("a1", "  "); !errors.Is(err, impl.ErrBadAccount) {
		t.Fatalf("blank currency = %v, want ErrBadAccount", err)
	}
	register(t, a, "a1", "gold")
	if err := a.RegisterAccount("a1", "gold"); !errors.Is(err, impl.ErrDuplicateAccount) {
		t.Fatalf("duplicate account = %v, want ErrDuplicateAccount", err)
	}
	stats := a.Stats()
	if stats.Accounts != 1 || stats.Currencies != 1 {
		t.Fatalf("stats = %+v, want one account on one currency", stats)
	}
}

func TestRegisterAccountRejectsLimit(t *testing.T) {
	a := impl.NewCurrencySupplyAuditor(impl.AuditLimits{MaxAccounts: 2, MaxCurrencies: 4, MaxWindowEntries: 16})
	register(t, a, "a1", "gold")
	register(t, a, "a2", "gold")
	if err := a.RegisterAccount("a3", "gold"); !errors.Is(err, impl.ErrAccountLimit) {
		t.Fatalf("third account = %v, want ErrAccountLimit", err)
	}
	if stats := a.Stats(); stats.Accounts != 2 {
		t.Fatalf("account count = %d, want 2", stats.Accounts)
	}
}

func TestRecordRejectsUnknownAccount(t *testing.T) {
	a := newAuditor(t)
	err := a.Record(impl.LedgerEntry{EntryID: "e1", Account: "ghost", Currency: "gold", Amount: 10, BizTime: 5})
	if !errors.Is(err, impl.ErrUnknownAccount) {
		t.Fatalf("entry on an unknown account = %v, want ErrUnknownAccount", err)
	}
	if stats := a.Stats(); stats.Entries != 0 {
		t.Fatalf("entry count = %d, want 0", stats.Entries)
	}
}

func TestRecordRejectsBadEntry(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	if err := a.Record(impl.LedgerEntry{EntryID: "  ", Account: "a1", Currency: "gold", Amount: 10, BizTime: 5}); !errors.Is(err, impl.ErrBadEntry) {
		t.Fatalf("blank entry id = %v, want ErrBadEntry", err)
	}
	if err := a.Record(impl.LedgerEntry{EntryID: "e1", Account: "a1", Currency: "gold", Amount: 0, BizTime: 5}); !errors.Is(err, impl.ErrBadEntry) {
		t.Fatalf("zero amount = %v, want ErrBadEntry", err)
	}
	if err := a.Record(impl.LedgerEntry{EntryID: "e2", Account: "a1", Currency: "gold", Amount: 10, BizTime: 0}); !errors.Is(err, impl.ErrBadEntry) {
		t.Fatalf("zero business time = %v, want ErrBadEntry", err)
	}
	if stats := a.Stats(); stats.Entries != 0 {
		t.Fatalf("entry count after rejected writes = %d, want 0", stats.Entries)
	}
}

func TestRecordRejectsCurrencyMismatch(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	err := a.Record(impl.LedgerEntry{EntryID: "e1", Account: "a1", Currency: "gem", Amount: 10, BizTime: 5})
	if !errors.Is(err, impl.ErrCurrencyMismatch) {
		t.Fatalf("entry on the wrong currency = %v, want ErrCurrencyMismatch", err)
	}
}

func TestRecordIsIdempotentByEntryID(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	entry := impl.LedgerEntry{EntryID: "e1", Account: "a1", Currency: "gold", Amount: 100, BizTime: 5}
	if err := a.Record(entry); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := a.Record(entry); err != nil {
		t.Fatalf("repeated record = %v, want nil", err)
	}
	totals, err := a.SnapshotTotals("gold")
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if totals.Issued != 100 || totals.Entries != 1 {
		t.Fatalf("totals after the replay = %+v, want 100 issued once", totals)
	}
	if stats := a.Stats(); stats.Duplicates != 1 {
		t.Fatalf("duplicate counter = %d, want 1", stats.Duplicates)
	}
}

func TestRecordKeepsTotalsIndependentOfArrivalOrder(t *testing.T) {
	inOrder := newAuditor(t)
	register(t, inOrder, "a1", "gold")
	book(t, inOrder, "e1", "a1", "gold", 50, 10)
	book(t, inOrder, "e2", "a1", "gold", 20, 30)
	outOfOrder := newAuditor(t)
	register(t, outOfOrder, "a1", "gold")
	book(t, outOfOrder, "e2", "a1", "gold", 20, 30)
	book(t, outOfOrder, "e1", "a1", "gold", 50, 10)
	first, err := inOrder.SnapshotTotals("gold")
	if err != nil {
		t.Fatalf("totals in order: %v", err)
	}
	second, err := outOfOrder.SnapshotTotals("gold")
	if err != nil {
		t.Fatalf("totals out of order: %v", err)
	}
	if first.Issued != 70 || first.Entries != 2 || first.Net != second.Net {
		t.Fatalf("totals = %+v and %+v, want the same net of 70 over two entries", first, second)
	}
}

func TestSnapshotTotalsAggregates(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	book(t, a, "e1", "a1", "gold", 100, 10)
	book(t, a, "e2", "a1", "gold", -30, 20)
	totals, err := a.SnapshotTotals("gold")
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if totals.Currency != "gold" || totals.Issued != 100 || totals.Recycled != 30 || totals.Net != 70 {
		t.Fatalf("totals = %+v, want 100 issued, 30 recycled and a net of 70", totals)
	}
	if totals.Entries != 2 || totals.Accounts != 1 {
		t.Fatalf("totals counters = %+v, want two entries on one account", totals)
	}
}

func TestAuditSplitsTheWindow(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	book(t, a, "e1", "a1", "gold", 50, 10)
	book(t, a, "e2", "a1", "gold", 30, 20)
	book(t, a, "e3", "a1", "gold", -20, 30)
	rep, err := a.Audit("gold", 15, 35)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if rep.Opening != 50 || rep.Issued != 30 || rep.Recycled != 20 || rep.Closing != 60 {
		t.Fatalf("audit = %+v, want opening 50, issued 30, recycled 20 and closing 60", rep)
	}
	if rep.Delta != 0 || !rep.WithinTolerance || len(rep.Anomalies) != 0 {
		t.Fatalf("audit delta = %d, tolerance %v, anomalies %+v; want a clean window", rep.Delta, rep.WithinTolerance, rep.Anomalies)
	}
}

func TestAuditRejectsBadWindowAndCurrency(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	if _, err := a.Audit("gold", 30, 10); !errors.Is(err, impl.ErrBadWindow) {
		t.Fatalf("inverted window = %v, want ErrBadWindow", err)
	}
	if _, err := a.Audit("gem", 0, 10); !errors.Is(err, impl.ErrUnknownCurrency) {
		t.Fatalf("unknown currency = %v, want ErrUnknownCurrency", err)
	}
}

func TestAuditListsOverdraftAnomalies(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	book(t, a, "e1", "a1", "gold", 10, 10)
	book(t, a, "e2", "a1", "gold", -40, 20)
	rep, err := a.Audit("gold", 0, 100)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if rep.Closing != -30 || rep.Delta != 30 {
		t.Fatalf("audit closing = %d and delta = %d, want -30 and 30", rep.Closing, rep.Delta)
	}
	if rep.WithinTolerance {
		t.Fatalf("an overdraft of 30 must not sit inside the default tolerance")
	}
	if len(rep.Anomalies) != 1 {
		t.Fatalf("anomalies = %+v, want one overdraft", rep.Anomalies)
	}
	an := rep.Anomalies[0]
	if an.Account != "a1" || an.EntryID != "e2" || an.Amount != -40 || an.Reason != "overdraft" {
		t.Fatalf("anomaly = %+v, want a1/e2 overdraft of -40", an)
	}
}

func TestSetToleranceRejectsNegative(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	book(t, a, "e1", "a1", "gold", 10, 10)
	book(t, a, "e2", "a1", "gold", -40, 20)
	if err := a.SetTolerance(impl.Tolerances{Absolute: -1}); !errors.Is(err, impl.ErrBadTolerance) {
		t.Fatalf("negative absolute = %v, want ErrBadTolerance", err)
	}
	if err := a.SetTolerance(impl.Tolerances{Relative: -0.1}); !errors.Is(err, impl.ErrBadTolerance) {
		t.Fatalf("negative relative = %v, want ErrBadTolerance", err)
	}
	if err := a.SetTolerance(impl.Tolerances{Absolute: 100}); err != nil {
		t.Fatalf("tolerance 100 = %v, want nil", err)
	}
	rep, err := a.Audit("gold", 0, 100)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !rep.WithinTolerance {
		t.Fatalf("overdraft 30 with an absolute tolerance of 100 must be within tolerance")
	}
	if rep.Delta != 30 {
		t.Fatalf("delta = %d, want 30", rep.Delta)
	}
}

func TestFlagAccountRequiresReasonAndAccount(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	if err := a.FlagAccount("a1", "  "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason = %v, want ErrBadReason", err)
	}
	if err := a.FlagAccount("ghost", "manual"); !errors.Is(err, impl.ErrUnknownAccount) {
		t.Fatalf("unknown account = %v, want ErrUnknownAccount", err)
	}
	if err := a.FlagAccount("a1", "manual"); err != nil {
		t.Fatalf("flag a1 = %v, want nil", err)
	}
	if stats := a.Stats(); stats.Flagged != 1 {
		t.Fatalf("flag counter = %d, want 1", stats.Flagged)
	}
	rec, err := a.Reconcile("gold")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(rec.Flagged) != 1 || rec.Flagged[0] != "a1" {
		t.Fatalf("reconcile flagged = %+v, want a1", rec.Flagged)
	}
}

func TestReconcileMatchesTotals(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	book(t, a, "e1", "a1", "gold", 100, 10)
	book(t, a, "e2", "a1", "gold", -30, 20)
	rec, err := a.Reconcile("gold")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rec.Expected != 70 || rec.Actual != 70 || rec.Difference != 0 {
		t.Fatalf("reconcile = %+v, want expected and actual 70 with no difference", rec)
	}
	if rec.Entries != 2 {
		t.Fatalf("reconciled entries = %d, want 2", rec.Entries)
	}
}

func TestSnapshotRestoreKeepsTotalsAndIdempotency(t *testing.T) {
	a := newAuditor(t)
	register(t, a, "a1", "gold")
	entry := impl.LedgerEntry{EntryID: "e1", Account: "a1", Currency: "gold", Amount: 100, BizTime: 10}
	if err := a.Record(entry); err != nil {
		t.Fatalf("record: %v", err)
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewCurrencySupplyAuditor(auditLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	totals, err := restored.SnapshotTotals("gold")
	if err != nil {
		t.Fatalf("totals after restore: %v", err)
	}
	if totals.Issued != 100 || totals.Entries != 1 {
		t.Fatalf("restored totals = %+v, want 100 issued once", totals)
	}
	if err := restored.Record(entry); err != nil {
		t.Fatalf("replay after restore = %v, want nil", err)
	}
	again, err := restored.SnapshotTotals("gold")
	if err != nil {
		t.Fatalf("totals after the replay: %v", err)
	}
	if again.Entries != 1 || again.Issued != 100 {
		t.Fatalf("totals after the replay = %+v, want the original 100 once", again)
	}
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload = %v, want ErrBadSnapshot", err)
	}
	if stats := restored.Stats(); stats.Entries != 1 {
		t.Fatalf("entries after a failed restore = %d, want the previous state", stats.Entries)
	}
}
