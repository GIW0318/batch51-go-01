package task

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	impl "gsb/batch51-go-game-a-19"
)

const testKey = "key-appstore"

func newValidator() *impl.PurchaseReceiptValidator {
	return impl.NewPurchaseReceiptValidator(impl.IAPLimits{MaxReconcileBatch: 2, MaxRetries: 3})
}

func canonical(r impl.Receipt) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d",
		r.Platform, r.TransactionID, r.ProductID, r.PlayerID, r.Amount, r.PurchasedAt)
}

func signReceipt(r impl.Receipt) impl.Receipt {
	mac := hmac.New(sha256.New, []byte(testKey))
	mac.Write([]byte(canonical(r)))
	r.Signature = hex.EncodeToString(mac.Sum(nil))
	return r
}

func setupStore(t *testing.T, v *impl.PurchaseReceiptValidator) {
	t.Helper()
	cat := []impl.Product{
		{ID: "gem-100", Units: 100, Price: 600},
		{ID: "gem-500", Units: 500, Price: 2400},
	}
	if err := v.SetProductCatalog(cat); err != nil {
		t.Fatalf("set catalog: %v", err)
	}
	if err := v.SetPlatformKey("appstore", testKey); err != nil {
		t.Fatalf("set platform key: %v", err)
	}
}

func makeReceipt(txn, product, player string, sandbox bool) impl.Receipt {
	r := impl.Receipt{
		Platform:      "appstore",
		TransactionID: txn,
		ProductID:     product,
		PlayerID:      player,
		Amount:        600,
		PurchasedAt:   1700000000,
		Sandbox:       sandbox,
	}
	return signReceipt(r)
}

func submit(t *testing.T, v *impl.PurchaseReceiptValidator, r impl.Receipt) string {
	t.Helper()
	id, err := v.Submit(r)
	if err != nil {
		t.Fatalf("submit receipt %s: %v", r.TransactionID, err)
	}
	return id
}

func verify(t *testing.T, v *impl.PurchaseReceiptValidator, id string) impl.VerifyResult {
	t.Helper()
	res, err := v.Verify(id)
	if err != nil {
		t.Fatalf("verify order %s: %v", id, err)
	}
	return res
}

func grant(t *testing.T, v *impl.PurchaseReceiptValidator, id string) impl.GrantResult {
	t.Helper()
	res, err := v.Grant(id)
	if err != nil {
		t.Fatalf("grant order %s: %v", id, err)
	}
	return res
}

func TestSetProductCatalogRejectsBadCatalog(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	bad := [][]impl.Product{
		{{ID: "", Units: 10, Price: 1}},
		{{ID: "a", Units: 0, Price: 1}},
		{{ID: "a", Units: 10, Price: -1}},
		{{ID: "dup", Units: 1, Price: 1}, {ID: "dup", Units: 2, Price: 2}},
	}
	for i, cat := range bad {
		if err := v.SetProductCatalog(cat); !errors.Is(err, impl.ErrBadCatalog) {
			t.Fatalf("catalog case %d must fail with ErrBadCatalog, got %v", i, err)
		}
	}
	if err := v.SetPlatformKey("", testKey); !errors.Is(err, impl.ErrBadKey) {
		t.Fatalf("blank platform must fail with ErrBadKey, got %v", err)
	}
	if err := v.SetPlatformKey("appstore", "  "); !errors.Is(err, impl.ErrBadKey) {
		t.Fatalf("blank key must fail with ErrBadKey, got %v", err)
	}
	id := submit(t, v, makeReceipt("txn-cat-1", "gem-100", "player-1", false))
	if id == "" {
		t.Fatalf("the previous catalog must keep serving after rejected updates")
	}
}

func TestSubmitRejectsBadReceipt(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	good := makeReceipt("txn-bad-0", "gem-100", "player-2", false)
	cases := []impl.Receipt{
		{TransactionID: good.TransactionID, ProductID: good.ProductID, PlayerID: good.PlayerID, Amount: good.Amount, PurchasedAt: good.PurchasedAt, Signature: good.Signature},
		{Platform: good.Platform, ProductID: "gem-100", PlayerID: "player-2", Amount: 600, PurchasedAt: 1, Signature: "sig"},
		{Platform: good.Platform, TransactionID: "t", PlayerID: "player-2", Amount: 600, PurchasedAt: 1, Signature: "sig"},
		{Platform: good.Platform, TransactionID: "t", ProductID: "gem-100", Amount: 600, PurchasedAt: 1, Signature: "sig"},
		{Platform: good.Platform, TransactionID: "t", ProductID: "gem-100", PlayerID: "player-2", PurchasedAt: 1, Signature: "sig"},
		{Platform: good.Platform, TransactionID: "t", ProductID: "gem-100", PlayerID: "player-2", Amount: 600, Signature: "sig"},
		{Platform: good.Platform, TransactionID: "t", ProductID: "gem-100", PlayerID: "player-2", Amount: 600, PurchasedAt: 1},
	}
	for i, r := range cases {
		if _, err := v.Submit(r); !errors.Is(err, impl.ErrBadReceipt) {
			t.Fatalf("receipt case %d must fail with ErrBadReceipt, got %v", i, err)
		}
	}
	if got := v.Stats().Submitted; got != 0 {
		t.Fatalf("submitted counter = %d, want 0 after rejected receipts", got)
	}
}

func TestSubmitReturnsMonotonicOrderIDs(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	first := submit(t, v, makeReceipt("txn-ord-1", "gem-100", "player-3", false))
	second := submit(t, v, makeReceipt("txn-ord-2", "gem-500", "player-3", false))
	third := submit(t, v, makeReceipt("txn-ord-3", "gem-100", "player-4", false))
	if first != "ord-1" || second != "ord-2" || third != "ord-3" {
		t.Fatalf("order ids = %s,%s,%s, want ord-1,ord-2,ord-3", first, second, third)
	}
	if got := v.Stats().Submitted; got != 3 {
		t.Fatalf("submitted counter = %d, want 3", got)
	}
}

func TestSubmitRejectsUnknownProduct(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	if _, err := v.Submit(makeReceipt("txn-unk-1", "gem-999", "player-5", false)); !errors.Is(err, impl.ErrUnknownProduct) {
		t.Fatalf("unknown product must fail with ErrUnknownProduct, got %v", err)
	}
	if got := v.Stats().Submitted; got != 0 {
		t.Fatalf("submitted counter = %d, want 0", got)
	}
}

func TestVerifyRejectsUnknownOrder(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	if _, err := v.Verify("ord-999"); !errors.Is(err, impl.ErrUnknownOrder) {
		t.Fatalf("unknown order must fail with ErrUnknownOrder, got %v", err)
	}
}

func TestVerifyGoodReceiptVerifies(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	id := submit(t, v, makeReceipt("txn-ok-1", "gem-100", "player-6", false))
	res := verify(t, v, id)
	if res.Status != impl.VerifyVerified || !res.Grantable {
		t.Fatalf("good receipt = %s grantable=%v, want verified/grantable", res.Status, res.Grantable)
	}
	if res.OrderID != id {
		t.Fatalf("verify result order = %q, want %q", res.OrderID, id)
	}
	if got := v.Stats().Verified; got != 1 {
		t.Fatalf("verified counter = %d, want 1", got)
	}
}

func TestVerifyBadSignatureClassified(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	malformed := makeReceipt("txn-sig-1", "gem-100", "player-7", false)
	malformed.Signature = "zz"
	id1 := submit(t, v, malformed)
	res1 := verify(t, v, id1)
	if res1.Status != impl.VerifyRetryable || res1.Grantable {
		t.Fatalf("malformed signature = %s grantable=%v, want retryable and not grantable", res1.Status, res1.Grantable)
	}
	forged := makeReceipt("txn-sig-2", "gem-100", "player-7", false)
	forged.Signature = strings.Repeat("a", 64)
	id2 := submit(t, v, forged)
	res2 := verify(t, v, id2)
	if res2.Status != impl.VerifyRejected || res2.Grantable {
		t.Fatalf("forged signature = %s grantable=%v, want rejected and not grantable", res2.Status, res2.Grantable)
	}
	if got := v.Stats().Rejected; got != 1 {
		t.Fatalf("rejected counter = %d, want 1", got)
	}
}

func TestGrantCreditsBalanceExactlyOnce(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	id := submit(t, v, makeReceipt("txn-grant-1", "gem-100", "player-8", false))
	if _, err := v.Grant(id); !errors.Is(err, impl.ErrOrderNotVerified) {
		t.Fatalf("grant before verify must fail with ErrOrderNotVerified, got %v", err)
	}
	verify(t, v, id)
	first := grant(t, v, id)
	if first.Units != 100 || first.Balance != 100 || first.Status != impl.StatusGranted {
		t.Fatalf("first grant = %+v, want 100 units at balance 100", first)
	}
	second := grant(t, v, id)
	if second.Units != 100 || second.Balance != 100 {
		t.Fatalf("repeated grant = %+v, want the same 100 units and balance 100", second)
	}
	if got := v.Balance("player-8"); got != 100 {
		t.Fatalf("balance = %d, want 100 after a repeated grant", got)
	}
	if got := v.Stats().Granted; got != 1 {
		t.Fatalf("granted counter = %d, want 1", got)
	}
	if got := v.Stats().UnitsGranted; got != 100 {
		t.Fatalf("units granted = %d, want 100", got)
	}
}

func TestRefundWithinBalanceClawsBack(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	id := submit(t, v, makeReceipt("txn-refund-1", "gem-100", "player-9", false))
	verify(t, v, id)
	grant(t, v, id)
	res, err := v.Refund(id, "player request")
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if res.ClawedBack != 100 || res.Balance != 0 || res.Status != impl.StatusRefunded {
		t.Fatalf("refund = %+v, want 100 clawed back down to balance 0", res)
	}
	if got := v.Balance("player-9"); got != 0 {
		t.Fatalf("balance = %d, want 0", got)
	}
	stats := v.Stats()
	if stats.Refunded != 1 || stats.UnitsClawedBack != 100 {
		t.Fatalf("stats = %+v, want one refund and 100 clawed back", stats)
	}
	if _, err := v.Refund(id, "again"); !errors.Is(err, impl.ErrOrderNotGranted) {
		t.Fatalf("second refund must fail with ErrOrderNotGranted, got %v", err)
	}
}

func TestRefundValidation(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	if _, err := v.Refund("ord-999", "why"); !errors.Is(err, impl.ErrUnknownOrder) {
		t.Fatalf("unknown refund must fail with ErrUnknownOrder, got %v", err)
	}
	id := submit(t, v, makeReceipt("txn-refund-2", "gem-100", "player-10", false))
	if _, err := v.Refund(id, "why"); !errors.Is(err, impl.ErrOrderNotGranted) {
		t.Fatalf("ungranted refund must fail with ErrOrderNotGranted, got %v", err)
	}
	verify(t, v, id)
	grant(t, v, id)
	if _, err := v.Refund(id, "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must fail with ErrBadReason, got %v", err)
	}
}

func TestReconcileRespectsBatchLimit(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	for i := 0; i < 5; i++ {
		r := makeReceipt(fmt.Sprintf("txn-rec-%d", i), "gem-100", "player-11", false)
		id := submit(t, v, r)
		verify(t, v, id)
	}
	wantScans := []int{2, 2, 1, 0}
	for i, wantScanned := range wantScans {
		report, err := v.Reconcile(0, 2000000000)
		if err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
		if report.Scanned != wantScanned {
			t.Fatalf("reconcile %d scanned = %d, want %d", i, report.Scanned, wantScanned)
		}
		if report.Granted != wantScanned {
			t.Fatalf("reconcile %d granted = %d, want %d", i, report.Granted, wantScanned)
		}
	}
	if got := v.Stats().Granted; got != 5 {
		t.Fatalf("granted counter = %d, want 5", got)
	}
	if got := v.Balance("player-11"); got != 500 {
		t.Fatalf("balance = %d, want 500", got)
	}
	if _, err := v.Reconcile(9, 1); !errors.Is(err, impl.ErrBadReconcile) {
		t.Fatalf("inverted range must fail with ErrBadReconcile, got %v", err)
	}
}

func TestReconcileSkipsTerminalOrders(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	kept := submit(t, v, makeReceipt("txn-term-1", "gem-100", "player-12", false))
	verify(t, v, kept)
	grant(t, v, kept)

	rejected := makeReceipt("txn-term-2", "gem-100", "player-12", false)
	rejected.Signature = strings.Repeat("b", 64)
	badID := submit(t, v, rejected)
	verify(t, v, badID)

	refunded := submit(t, v, makeReceipt("txn-term-3", "gem-100", "player-12", false))
	verify(t, v, refunded)
	grant(t, v, refunded)
	if _, err := v.Refund(refunded, "duplicate purchase"); err != nil {
		t.Fatalf("refund terminal order: %v", err)
	}

	report, err := v.Reconcile(0, 2000000000)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.Scanned != 3 || report.Skipped != 3 || report.Granted != 0 {
		t.Fatalf("terminal reconcile = %+v, want 3 scanned and 3 skipped", report)
	}
	if got := v.Balance("player-12"); got != 100 {
		t.Fatalf("balance = %d, want the single kept purchase 100", got)
	}
	if got := v.Stats().Granted; got != 2 {
		t.Fatalf("granted counter = %d, want 2", got)
	}
}

func TestSandboxReceiptAcceptedInSandboxMode(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	if err := v.SetSandbox(true); err != nil {
		t.Fatalf("set sandbox: %v", err)
	}
	id := submit(t, v, makeReceipt("txn-sandbox-1", "gem-100", "player-13", true))
	res := verify(t, v, id)
	if res.Status != impl.VerifyVerified {
		t.Fatalf("sandbox receipt status = %s, want verified", res.Status)
	}
	g := grant(t, v, id)
	if g.Balance != 100 {
		t.Fatalf("sandbox grant balance = %d, want 100", g.Balance)
	}
}

func TestSnapshotRestoreKeepsLedger(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	id := submit(t, v, makeReceipt("txn-snap-1", "gem-100", "player-14", false))
	verify(t, v, id)
	grant(t, v, id)
	data, err := v.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newValidator()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Balance("player-14"); got != 100 {
		t.Fatalf("restored balance = %d, want 100", got)
	}
	again := grant(t, restored, id)
	if again.Balance != 100 {
		t.Fatalf("grant after restore = %d, want the ledger balance 100", again.Balance)
	}
	if got := restored.Stats().Granted; got != 1 {
		t.Fatalf("restored granted counter = %d, want 1", got)
	}
	next := submit(t, restored, makeReceipt("txn-snap-2", "gem-100", "player-14", false))
	if next != "ord-2" {
		t.Fatalf("order id after restore = %q, want ord-2", next)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	v := newValidator()
	setupStore(t, v)
	if err := v.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	id := submit(t, v, makeReceipt("txn-corrupt-1", "gem-100", "player-15", false))
	if id != "ord-1" {
		t.Fatalf("live state must keep working after a rejected restore, got %q", id)
	}
}
