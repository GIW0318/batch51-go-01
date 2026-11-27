package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-b-19"
)

func smbConfig() impl.ServerConfig {
	return impl.ServerConfig{Dialect: 0x0311, InitialCredits: 16, MaxCredits: 32, MaxLeases: 64}
}

func negotiateRequest(connID, guid string, credits uint16) impl.NegotiateReq {
	return impl.NegotiateReq{ConnID: connID, ClientGUID: guid, Dialect: 0x0311, CreditsRequested: credits}
}

func mustNegotiate(t *testing.T, g *impl.Smb2CreditLeaseGuard, connID, guid string, credits uint16) impl.NegotiateResp {
	t.Helper()
	resp, err := g.Negotiate(negotiateRequest(connID, guid, credits))
	if err != nil {
		t.Fatalf("negotiate %s failed: %v", connID, err)
	}
	return resp
}

func mustOpen(t *testing.T, g *impl.Smb2CreditLeaseGuard, connID, path, leaseKey string, desired impl.LeaseState) impl.FileID {
	t.Helper()
	id, err := g.Open(connID, impl.CreateReq{Path: path, LeaseKey: leaseKey, Desired: desired})
	if err != nil {
		t.Fatalf("open %s failed: %v", path, err)
	}
	return id
}

func TestNegotiateGrantsInitialWindow(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	resp := mustNegotiate(t, g, "conn-1", "guid-a", 100)
	if resp.Dialect != 0x0311 {
		t.Fatalf("granted dialect = %#x, want 0x0311", resp.Dialect)
	}
	if resp.Credits != 16 {
		t.Fatalf("granted credits = %d, want the initial window 16", resp.Credits)
	}
	if resp.ConnID != "conn-1" || resp.ClientGUID != "guid-a" {
		t.Fatalf("response identity = %+v, want conn-1/guid-a", resp)
	}
	if stats := g.Stats(); stats.Outstanding != 16 {
		t.Fatalf("stats = %+v, want 16 outstanding credits", stats)
	}
}

func TestNegotiateRejectsBadRequest(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	if _, err := g.Negotiate(negotiateRequest("  ", "guid-a", 8)); !errors.Is(err, impl.ErrBlankConn) {
		t.Fatalf("blank connection = %v, want ErrBlankConn", err)
	}
	if _, err := g.Negotiate(negotiateRequest("conn-1", "  ", 8)); !errors.Is(err, impl.ErrBlankClient) {
		t.Fatalf("blank client guid = %v, want ErrBlankClient", err)
	}
	bad := negotiateRequest("conn-1", "guid-a", 8)
	bad.Dialect = 0x0210
	if _, err := g.Negotiate(bad); !errors.Is(err, impl.ErrBadDialect) {
		t.Fatalf("old dialect = %v, want ErrBadDialect", err)
	}
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	if _, err := g.Negotiate(negotiateRequest("conn-1", "guid-b", 8)); !errors.Is(err, impl.ErrConnExists) {
		t.Fatalf("duplicate connection = %v, want ErrConnExists", err)
	}
}

func TestChargeRejectsOverdraft(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	left, err := g.Charge("conn-1", 5)
	if err != nil || left != 3 {
		t.Fatalf("first charge = %d/%v, want 3/nil", left, err)
	}
	left, err = g.Charge("conn-1", 5)
	if !errors.Is(err, impl.ErrCreditExhausted) {
		t.Fatalf("overdraft charge = %v, want ErrCreditExhausted", err)
	}
	if left != 3 {
		t.Fatalf("overdraft charge reported %d credits left, want 3", left)
	}
	left, err = g.Charge("conn-1", 3)
	if err != nil || left != 0 {
		t.Fatalf("charge of the remaining window = %d/%v, want 0/nil", left, err)
	}
	if stats := g.Stats(); stats.Outstanding != 0 {
		t.Fatalf("stats after charges = %+v, want outstanding 0", stats)
	}
}

func TestRefundClampsToWindow(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-1", "guid-a", 4)
	if left, err := g.Charge("conn-1", 4); err != nil || left != 0 {
		t.Fatalf("charge of the whole window = %d/%v, want 0/nil", left, err)
	}
	left, err := g.Refund("conn-1", 10)
	if err != nil || left != 4 {
		t.Fatalf("over refund = %d/%v, want the window size 4", left, err)
	}
	if left, err := g.Refund("conn-1", 2); err != nil || left != 4 {
		t.Fatalf("refund on a full window = %d/%v, want 4/nil", left, err)
	}
}

func TestChargeUnknownConnection(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	if _, err := g.Charge("conn-x", 1); !errors.Is(err, impl.ErrConnUnknown) {
		t.Fatalf("charge on an unknown connection = %v, want ErrConnUnknown", err)
	}
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	if err := g.Disconnect("conn-1"); err != nil {
		t.Fatalf("disconnect failed: %v", err)
	}
	if _, err := g.Charge("conn-1", 1); !errors.Is(err, impl.ErrConnUnknown) {
		t.Fatalf("charge after disconnect = %v, want ErrConnUnknown", err)
	}
	if err := g.Disconnect("conn-1"); !errors.Is(err, impl.ErrConnUnknown) {
		t.Fatalf("second disconnect = %v, want ErrConnUnknown", err)
	}
}

func TestOpenRequiresNegotiatedConnection(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	req := impl.CreateReq{Path: "/share/a.txt", LeaseKey: "L1", Desired: impl.LeaseRead | impl.LeaseWrite}
	if _, err := g.Open("conn-x", req); !errors.Is(err, impl.ErrConnUnknown) {
		t.Fatalf("open on an unknown connection = %v, want ErrConnUnknown", err)
	}
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	if _, err := g.Open("conn-1", impl.CreateReq{Path: "", Desired: impl.LeaseRead}); !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("open with a blank path = %v, want ErrBadPath", err)
	}
	if _, err := g.Open("conn-1", impl.CreateReq{Path: "share/a.txt", Desired: impl.LeaseRead}); !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("open with a relative path = %v, want ErrBadPath", err)
	}
	unleased := mustOpen(t, g, "conn-1", "/share/b.txt", "", impl.LeaseRead)
	if unleased.Persistent == 0 {
		t.Fatalf("unleased open returned a zero file id")
	}
	if _, ok := g.LeaseOf(""); ok {
		t.Fatalf("open without a lease key must not create a lease")
	}
}

func TestOpenSameLeaseKeyIsIdempotent(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	first := mustOpen(t, g, "conn-1", "/share/a.txt", "L1", impl.LeaseRead|impl.LeaseWrite)
	second := mustOpen(t, g, "conn-1", "/share/a.txt", "L1", impl.LeaseRead|impl.LeaseWrite)
	if first != second {
		t.Fatalf("reopen returned %+v, want the same file id %+v", second, first)
	}
	entry, ok := g.LeaseOf("L1")
	if !ok {
		t.Fatalf("lease L1 was not recorded")
	}
	if entry.FileID != first {
		t.Fatalf("lease file id = %+v, want %+v", entry.FileID, first)
	}
	if entry.State != impl.LeaseRead|impl.LeaseWrite {
		t.Fatalf("lease state = %d, want read|write", entry.State)
	}
	if entry.Owner != "guid-a" || entry.ConnID != "conn-1" {
		t.Fatalf("lease owner = %q conn = %q, want guid-a/conn-1", entry.Owner, entry.ConnID)
	}
}

func TestBreakRecordsPendingTarget(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	mustOpen(t, g, "conn-1", "/share/a.txt", "L1", impl.LeaseRead|impl.LeaseWrite)
	before, _ := g.LeaseOf("L1")
	result, err := g.Break("L1", impl.LeaseRead)
	if err != nil {
		t.Fatalf("break failed: %v", err)
	}
	if result.From != impl.LeaseRead|impl.LeaseWrite || result.To != impl.LeaseRead {
		t.Fatalf("break from/to = %d/%d, want read|write/read", result.From, result.To)
	}
	if !result.AckRequired {
		t.Fatalf("break must require an acknowledgement")
	}
	if result.Rev <= before.Rev {
		t.Fatalf("break revision = %d, want greater than %d", result.Rev, before.Rev)
	}
	entry, ok := g.LeaseOf("L1")
	if !ok || !entry.Breaking {
		t.Fatalf("lease after break = %+v ok=%v, want a pending break", entry, ok)
	}
	if entry.Target != impl.LeaseRead {
		t.Fatalf("pending target = %d, want read", entry.Target)
	}
}

func TestAckBreakSettlesLease(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	mustOpen(t, g, "conn-1", "/share/a.txt", "L1", impl.LeaseRead|impl.LeaseWrite)
	if _, err := g.Break("L1", impl.LeaseRead); err != nil {
		t.Fatalf("break failed: %v", err)
	}
	if err := g.AckBreak("L1", impl.LeaseRead); err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	entry, ok := g.LeaseOf("L1")
	if !ok {
		t.Fatalf("lease disappeared after the ack")
	}
	if entry.Breaking {
		t.Fatalf("lease still marked as breaking after the ack")
	}
	if entry.State != impl.LeaseRead {
		t.Fatalf("lease state after ack = %d, want read", entry.State)
	}
}

func TestAckBreakMismatchRejected(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	mustOpen(t, g, "conn-1", "/share/a.txt", "L1", impl.LeaseRead|impl.LeaseWrite)
	if _, err := g.Break("L1", impl.LeaseRead); err != nil {
		t.Fatalf("break failed: %v", err)
	}
	if err := g.AckBreak("L1", impl.LeaseWrite); !errors.Is(err, impl.ErrBreakMismatch) {
		t.Fatalf("ack with the old state = %v, want ErrBreakMismatch", err)
	}
	entry, _ := g.LeaseOf("L1")
	if !entry.Breaking || entry.State != impl.LeaseRead|impl.LeaseWrite {
		t.Fatalf("rejected ack changed the lease: %+v", entry)
	}
}

func TestAckWithoutBreakAndUnknownLease(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	if err := g.AckBreak("missing", impl.LeaseRead); !errors.Is(err, impl.ErrLeaseNotFound) {
		t.Fatalf("ack for an unknown lease = %v, want ErrLeaseNotFound", err)
	}
	mustOpen(t, g, "conn-1", "/share/a.txt", "L1", impl.LeaseRead|impl.LeaseWrite)
	if err := g.AckBreak("L1", impl.LeaseRead); !errors.Is(err, impl.ErrNoBreakPending) {
		t.Fatalf("ack without a pending break = %v, want ErrNoBreakPending", err)
	}
}

func TestBreakRejectsBadTarget(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	mustOpen(t, g, "conn-1", "/share/a.txt", "L1", impl.LeaseRead)
	if _, err := g.Break("L1", impl.LeaseWrite); !errors.Is(err, impl.ErrBadLeaseState) {
		t.Fatalf("break to a wider state = %v, want ErrBadLeaseState", err)
	}
	if _, err := g.Break("L1", impl.LeaseRead); !errors.Is(err, impl.ErrBadLeaseState) {
		t.Fatalf("break to the same state = %v, want ErrBadLeaseState", err)
	}
	if _, err := g.Break("missing", impl.LeaseNone); !errors.Is(err, impl.ErrLeaseNotFound) {
		t.Fatalf("break of an unknown lease = %v, want ErrLeaseNotFound", err)
	}
}

func TestReconnectRejectsForeignClient(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-a", "guid-a", 8)
	id := mustOpen(t, g, "conn-a", "/share/doc.txt", "L1", impl.LeaseRead|impl.LeaseWrite)
	mustNegotiate(t, g, "conn-b", "guid-b", 8)
	if _, err := g.Reconnect("conn-b", id); !errors.Is(err, impl.ErrHandleOwner) {
		t.Fatalf("reconnect by another client = %v, want ErrHandleOwner", err)
	}
	mustNegotiate(t, g, "conn-a2", "guid-a", 8)
	handle, err := g.Reconnect("conn-a2", id)
	if err != nil {
		t.Fatalf("reconnect by the owner failed: %v", err)
	}
	if handle.FileID != id || handle.Path != "/share/doc.txt" || handle.ConnID != "conn-a2" {
		t.Fatalf("handle after reconnect = %+v, want the same file id on conn-a2", handle)
	}
	if _, err := g.Reconnect("conn-z", id); !errors.Is(err, impl.ErrConnUnknown) {
		t.Fatalf("reconnect on an unknown connection = %v, want ErrConnUnknown", err)
	}
	if _, err := g.Reconnect("conn-a2", impl.FileID{Persistent: 9999}); !errors.Is(err, impl.ErrHandleNotFound) {
		t.Fatalf("reconnect with an unknown file id = %v, want ErrHandleNotFound", err)
	}
}

func TestConcurrentChargeNeverOverdraws(t *testing.T) {
	g := impl.NewSmb2CreditLeaseGuard(smbConfig())
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	const workers = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	exhausted := 0
	other := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.Charge("conn-1", 1)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, impl.ErrCreditExhausted):
				exhausted++
			default:
				other++
			}
		}()
	}
	wg.Wait()
	if ok != 8 {
		t.Fatalf("successful charges = %d, want exactly the window size 8", ok)
	}
	if exhausted != workers-8 {
		t.Fatalf("exhausted charges = %d, want %d", exhausted, workers-8)
	}
	if other != 0 {
		t.Fatalf("unexpected charge errors = %d", other)
	}
	if stats := g.Stats(); stats.Outstanding != 0 {
		t.Fatalf("outstanding credits after concurrent charges = %d, want 0", stats.Outstanding)
	}
}

func TestSnapshotRestoreKeepsLeasesAndCredits(t *testing.T) {
	cfg := smbConfig()
	g := impl.NewSmb2CreditLeaseGuard(cfg)
	mustNegotiate(t, g, "conn-1", "guid-a", 8)
	if left, err := g.Charge("conn-1", 3); err != nil || left != 5 {
		t.Fatalf("charge before snapshot = %d/%v, want 5/nil", left, err)
	}
	id := mustOpen(t, g, "conn-1", "/share/a.txt", "L1", impl.LeaseRead|impl.LeaseWrite)
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	restored := impl.NewSmb2CreditLeaseGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	entry, ok := restored.LeaseOf("L1")
	if !ok {
		t.Fatalf("restored guard lost lease L1")
	}
	if entry.FileID != id || entry.State != impl.LeaseRead|impl.LeaseWrite {
		t.Fatalf("restored lease = %+v, want file id %+v with read|write", entry, id)
	}
	left, err := restored.Charge("conn-1", 5)
	if err != nil || left != 0 {
		t.Fatalf("charge after restore = %d/%v, want 0/nil from the preserved window", left, err)
	}
	handle, err := restored.Reconnect("conn-1", id)
	if err != nil {
		t.Fatalf("reconnect after restore failed: %v", err)
	}
	if handle.Path != "/share/a.txt" {
		t.Fatalf("handle path after restore = %q, want /share/a.txt", handle.Path)
	}
}
