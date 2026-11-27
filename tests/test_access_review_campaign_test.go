package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-15"
)

var reviewBase = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)

func newCampaignGuard(defaultRevoke bool) *impl.AccessReviewCampaign {
	return impl.NewAccessReviewCampaign(impl.CampaignConfig{
		DefaultRevoke: defaultRevoke,
		MaxItems:     64,
	})
}

func seedGrants(t *testing.T, g *impl.AccessReviewCampaign, accounts []string, permission string) {
	t.Helper()
	for _, account := range accounts {
		if err := g.Grant(account, permission); err != nil {
			t.Fatalf("grant %s/%s: %v", account, permission, err)
		}
	}
}

func openCampaign(t *testing.T, g *impl.AccessReviewCampaign, id string) []impl.Item {
	t.Helper()
	state, err := g.Open(id, reviewBase)
	if err != nil {
		t.Fatalf("open campaign %s: %v", id, err)
	}
	if state.ID != id {
		t.Fatalf("opened campaign = %q, want %q", state.ID, id)
	}
	items, err := g.Items(id)
	if err != nil {
		t.Fatalf("items of %s: %v", id, err)
	}
	return items
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestOpenFreezesGrantSnapshot(t *testing.T) {
	g := newCampaignGuard(true)
	if err := g.Grant(" Alice ", " Read "); err != nil {
		t.Fatalf("grant with padding: %v", err)
	}
	if err := g.Grant("bob", "write"); err != nil {
		t.Fatalf("grant bob: %v", err)
	}
	state, err := g.Open("Q3-2026", reviewBase)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if state.Total != 2 || state.Pending != 2 || state.Decided != 0 || state.Closed {
		t.Fatalf("opened state = %+v, want two pending items in an open campaign", state)
	}
	if err := g.Grant("carol", "admin"); err != nil {
		t.Fatalf("grant after open: %v", err)
	}
	items, err := g.Items("Q3-2026")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want the frozen pair only", items)
	}
	if items[0].Account != "alice" || items[1].Account != "bob" {
		t.Fatalf("frozen accounts = %q,%q, want normalized alice then bob", items[0].Account, items[1].Account)
	}
	if items[0].Permission != "read" {
		t.Fatalf("frozen permission = %q, want the normalized read", items[0].Permission)
	}
	if items[0].Decision != "" {
		t.Fatalf("a fresh item already carries decision %q", items[0].Decision)
	}
}

func TestOpenRejectsDuplicateCampaign(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice"}, "read")
	openCampaign(t, g, "Q3-2026")
	if _, err := g.Open("Q3-2026", reviewBase); !errors.Is(err, impl.ErrCampaignExists) {
		t.Fatalf("opening the same campaign twice must fail with ErrCampaignExists, got %v", err)
	}
	if _, err := g.Open("Q4-2026", reviewBase); err != nil {
		t.Fatalf("a second distinct campaign must open: %v", err)
	}
}

func TestOpenRejectsBlankCampaignID(t *testing.T) {
	g := newCampaignGuard(true)
	if _, err := g.Open("  ", reviewBase); !errors.Is(err, impl.ErrBadCampaign) {
		t.Fatalf("a blank campaign id must fail with ErrBadCampaign, got %v", err)
	}
	if _, err := g.Progress("missing"); !errors.Is(err, impl.ErrUnknownCampaign) {
		t.Fatalf("progress of an unknown campaign must fail with ErrUnknownCampaign, got %v", err)
	}
}

func TestDecideAppliesKeep(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice"}, "read")
	items := openCampaign(t, g, "Q3-2026")
	res, err := g.Decide("Q3-2026", items[0].ID, "reviewer-7", impl.DecisionKeep, reviewBase.Add(time.Hour))
	if err != nil {
		t.Fatalf("decide keep: %v", err)
	}
	if !res.Applied {
		t.Fatalf("decision result = %+v, want it applied", res)
	}
	if res.Seq != 1 {
		t.Fatalf("decision seq = %d, want 1", res.Seq)
	}
	after, err := g.Items("Q3-2026")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if after[0].Decision != impl.DecisionKeep || after[0].Reviewer != "reviewer-7" {
		t.Fatalf("item after decision = %+v, want keep by reviewer-7", after[0])
	}
	prog, err := g.Progress("Q3-2026")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if prog.Decided != 1 || prog.Kept != 1 || prog.Pending != 0 {
		t.Fatalf("progress = %+v, want one kept decision", prog)
	}
}

func TestDecideIsIdempotentForSameDecision(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice"}, "read")
	items := openCampaign(t, g, "Q3-2026")
	first, err := g.Decide("Q3-2026", items[0].ID, "reviewer-7", impl.DecisionRevoke, reviewBase)
	if err != nil {
		t.Fatalf("first decision: %v", err)
	}
	second, err := g.Decide("Q3-2026", items[0].ID, "reviewer-8", impl.DecisionRevoke, reviewBase.Add(time.Minute))
	if err != nil {
		t.Fatalf("repeating the same decision must not fail: %v", err)
	}
	if second.Applied {
		t.Fatalf("a repeated decision reported Applied=true")
	}
	if second.Seq != first.Seq {
		t.Fatalf("repeated decision seq = %d, want the original %d", second.Seq, first.Seq)
	}
	after, err := g.Items("Q3-2026")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if after[0].Reviewer != "reviewer-7" {
		t.Fatalf("a repeated decision changed the reviewer to %q", after[0].Reviewer)
	}
	prog, err := g.Progress("Q3-2026")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if prog.Revoked != 1 || prog.Decided != 1 {
		t.Fatalf("progress = %+v, want the item counted once", prog)
	}
}

func TestDecideRejectsConflictingDecision(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice"}, "read")
	items := openCampaign(t, g, "Q3-2026")
	if _, err := g.Decide("Q3-2026", items[0].ID, "reviewer-7", impl.DecisionKeep, reviewBase); err != nil {
		t.Fatalf("first decision: %v", err)
	}
	if _, err := g.Decide("Q3-2026", items[0].ID, "reviewer-8", impl.DecisionRevoke, reviewBase); !errors.Is(err, impl.ErrAlreadyDecided) {
		t.Fatalf("a conflicting decision must fail with ErrAlreadyDecided, got %v", err)
	}
	after, err := g.Items("Q3-2026")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if after[0].Decision != impl.DecisionKeep {
		t.Fatalf("the conflicting decision overwrote the item: %+v", after[0])
	}
}

func TestDecideRejectsUnknownItem(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice"}, "read")
	openCampaign(t, g, "Q3-2026")
	if _, err := g.Decide("Q3-2026", "item-does-not-exist", "reviewer-7", impl.DecisionKeep, reviewBase); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("an unknown item must fail with ErrUnknownItem, got %v", err)
	}
	if _, err := g.Decide("Q9-2026", "item-does-not-exist", "reviewer-7", impl.DecisionKeep, reviewBase); !errors.Is(err, impl.ErrUnknownCampaign) {
		t.Fatalf("an unknown campaign must fail with ErrUnknownCampaign, got %v", err)
	}
}

func TestDecideRejectsUnknownDecisionValue(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice"}, "read")
	items := openCampaign(t, g, "Q3-2026")
	if _, err := g.Decide("Q3-2026", items[0].ID, "reviewer-7", "approve", reviewBase); !errors.Is(err, impl.ErrBadDecision) {
		t.Fatalf("an unsupported decision must fail with ErrBadDecision, got %v", err)
	}
	prog, err := g.Progress("Q3-2026")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if prog.Decided != 0 || prog.Pending != 1 {
		t.Fatalf("progress = %+v after a rejected decision, want the item still pending", prog)
	}
}

func TestCloseAppliesDefaultRevoke(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice", "bob", "carol"}, "read")
	items := openCampaign(t, g, "Q3-2026")
	if _, err := g.Decide("Q3-2026", items[0].ID, "reviewer-7", impl.DecisionKeep, reviewBase); err != nil {
		t.Fatalf("decide keep: %v", err)
	}
	res, err := g.Close("Q3-2026", reviewBase.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if !sameIDs(res.Kept, []string{items[0].ID}) {
		t.Fatalf("kept = %v, want %v", res.Kept, []string{items[0].ID})
	}
	if !sameIDs(res.Revoked, []string{items[1].ID, items[2].ID}) {
		t.Fatalf("revoked = %v, want the two undecided items", res.Revoked)
	}
	if !sameIDs(res.Defaulted, []string{items[1].ID, items[2].ID}) {
		t.Fatalf("defaulted = %v, want the two undecided items", res.Defaulted)
	}
	state, ok := g.Campaign("Q3-2026")
	if !ok || !state.Closed {
		t.Fatalf("campaign after close = %+v ok=%v, want it closed", state, ok)
	}
	if state.Kept != 1 || state.Revoked != 2 || state.Pending != 0 {
		t.Fatalf("campaign tally = %+v, want one kept and two revoked", state)
	}
}

func TestCloseSettlesUndecidedWithKeepDefault(t *testing.T) {
	g := newCampaignGuard(false)
	seedGrants(t, g, []string{"alice", "bob", "carol"}, "read")
	items := openCampaign(t, g, "Q3-2026")
	if _, err := g.Decide("Q3-2026", items[0].ID, "reviewer-7", impl.DecisionKeep, reviewBase); err != nil {
		t.Fatalf("decide keep: %v", err)
	}
	if _, err := g.Decide("Q3-2026", items[1].ID, "reviewer-7", impl.DecisionRevoke, reviewBase); err != nil {
		t.Fatalf("decide revoke: %v", err)
	}
	res, err := g.Close("Q3-2026", reviewBase.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if !sameIDs(res.Kept, []string{items[0].ID, items[2].ID}) {
		t.Fatalf("kept = %v, want the kept item and the defaulted one", res.Kept)
	}
	if !sameIDs(res.Revoked, []string{items[1].ID}) {
		t.Fatalf("revoked = %v, want only the explicit revoke", res.Revoked)
	}
	if !sameIDs(res.Defaulted, []string{items[2].ID}) {
		t.Fatalf("defaulted = %v, want the single undecided item", res.Defaulted)
	}
	prog, err := g.Progress("Q3-2026")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if prog.Pending != 0 || prog.Kept != 2 || prog.Revoked != 1 {
		t.Fatalf("progress after close = %+v, want 2 kept and 1 revoked", prog)
	}
}

func TestCloseRejectsSecondClose(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice"}, "read")
	openCampaign(t, g, "Q3-2026")
	nextItems := openCampaign(t, g, "Q4-2026")
	if _, err := g.Close("Q3-2026", reviewBase); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if _, err := g.Close("Q3-2026", reviewBase); !errors.Is(err, impl.ErrAlreadyClosed) {
		t.Fatalf("closing twice must fail with ErrAlreadyClosed, got %v", err)
	}
	if _, err := g.Decide("Q3-2026", nextItems[0].ID, "reviewer-7", impl.DecisionKeep, reviewBase); !errors.Is(err, impl.ErrAlreadyClosed) {
		t.Fatalf("deciding a closed campaign must fail with ErrAlreadyClosed, got %v", err)
	}
	if _, err := g.Decide("Q4-2026", nextItems[0].ID, "reviewer-7", impl.DecisionKeep, reviewBase); err != nil {
		t.Fatalf("the still open campaign must accept a decision: %v", err)
	}
	if _, err := g.Close("Q4-2026", reviewBase); err != nil {
		t.Fatalf("closing the second campaign: %v", err)
	}
}

func TestProgressTracksPendingAndDecided(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice", "bob"}, "read")
	items := openCampaign(t, g, "Q3-2026")
	prog, err := g.Progress("Q3-2026")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if prog.Total != 2 || prog.Decided != 0 || prog.Pending != 2 {
		t.Fatalf("initial progress = %+v, want two pending items", prog)
	}
	if _, err := g.Decide("Q3-2026", items[1].ID, "reviewer-7", impl.DecisionRevoke, reviewBase); err != nil {
		t.Fatalf("decide revoke: %v", err)
	}
	prog, err = g.Progress("Q3-2026")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if prog.Decided != 1 || prog.Pending != 1 || prog.Revoked != 1 || prog.Kept != 0 {
		t.Fatalf("progress = %+v, want one revoked and one pending", prog)
	}
}

func TestConcurrentDecideHasSingleEffectiveDecision(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice"}, "read")
	items := openCampaign(t, g, "Q3-2026")
	const workers = 8
	results := make([]impl.DecisionResult, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = g.Decide("Q3-2026", items[0].ID, "reviewer", impl.DecisionKeep, reviewBase)
		}(i)
	}
	wg.Wait()
	applied, rejected := 0, 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("worker %d failed: %v", i, errs[i])
		}
		if results[i].Applied {
			applied++
		} else {
			rejected++
		}
	}
	if applied != 1 {
		t.Fatalf("effective decisions = %d, want exactly 1", applied)
	}
	if rejected != workers-1 {
		t.Fatalf("no-op decisions = %d, want %d", rejected, workers-1)
	}
	prog, err := g.Progress("Q3-2026")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if prog.Decided != 1 || prog.Kept != 1 {
		t.Fatalf("progress = %+v, want the item counted once", prog)
	}
}

func TestSnapshotRestoreKeepsCampaignState(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice", "bob"}, "read")
	items := openCampaign(t, g, "Q3-2026")
	first, err := g.Decide("Q3-2026", items[0].ID, "reviewer-7", impl.DecisionKeep, reviewBase)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newCampaignGuard(true)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	gotItems, err := restored.Items("Q3-2026")
	if err != nil || len(gotItems) != 2 {
		t.Fatalf("items after restore = %+v err=%v, want the frozen pair", gotItems, err)
	}
	if gotItems[0].Decision != impl.DecisionKeep || gotItems[0].Reviewer != "reviewer-7" {
		t.Fatalf("restored item = %+v, want the keep decision back", gotItems[0])
	}
	second, err := restored.Decide("Q3-2026", gotItems[1].ID, "reviewer-8", impl.DecisionKeep, reviewBase.Add(time.Minute))
	if err != nil {
		t.Fatalf("decide after restore: %v", err)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("decision seq after restore = %d, want it past %d", second.Seq, first.Seq)
	}
	res, err := restored.Close("Q3-2026", reviewBase.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("close after restore: %v", err)
	}
	if !sameIDs(res.Kept, []string{gotItems[0].ID, gotItems[1].ID}) {
		t.Fatalf("kept = %v, want both items kept", res.Kept)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newCampaignGuard(true)
	seedGrants(t, g, []string{"alice", "bob"}, "read")
	openCampaign(t, g, "Q3-2026")
	if err := g.Restore([]byte("<not-a-snapshot>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	items, err := g.Items("Q3-2026")
	if err != nil || len(items) != 2 {
		t.Fatalf("items after a rejected restore = %+v err=%v, want the campaign untouched", items, err)
	}
	prog, err := g.Progress("Q3-2026")
	if err != nil || prog.Pending != 2 {
		t.Fatalf("progress after a rejected restore = %+v err=%v, want two pending items", prog, err)
	}
}
