package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-a-23"
)

func ledgerConfig() impl.LedgerConfig {
	return impl.LedgerConfig{Seed: 20261127, MaxHistory: 5, MaxPity: 10}
}

func sureCurve() impl.EnhancementCurve {
	return impl.EnhancementCurve{
		ID:                  "std",
		MaxLevel:            5,
		Rates:               []int{1000, 1000, 1000, 1000, 1000},
		MaterialsPerAttempt: 2,
		GoldPerAttempt:      50,
	}
}

func failureCurve(downgrade bool) impl.EnhancementCurve {
	return impl.EnhancementCurve{
		ID:                  "brutal",
		MaxLevel:            5,
		Rates:               []int{0, 0, 0, 0, 0},
		MaterialsPerAttempt: 2,
		GoldPerAttempt:      50,
		DowngradeOnFail:     downgrade,
	}
}

func newLedger(t *testing.T) *impl.EnhancementAttemptLedger {
	t.Helper()
	return impl.NewEnhancementAttemptLedger(ledgerConfig())
}

func mustCurve(t *testing.T, l *impl.EnhancementAttemptLedger, c impl.EnhancementCurve) {
	t.Helper()
	if err := l.SetCurve(c); err != nil {
		t.Fatalf("set curve: %v", err)
	}
}

func mustMaterials(t *testing.T, l *impl.EnhancementAttemptLedger, player string, n int) {
	t.Helper()
	if err := l.GrantMaterials(player, n); err != nil {
		t.Fatalf("grant materials: %v", err)
	}
}

func TestSetCurveRejectsBadCurve(t *testing.T) {
	l := newLedger(t)
	bad := []impl.EnhancementCurve{
		{ID: "  ", MaxLevel: 3, Rates: []int{500, 500, 500}, MaterialsPerAttempt: 1},
		{ID: "std", MaxLevel: 0, Rates: []int{}, MaterialsPerAttempt: 1},
		{ID: "std", MaxLevel: 3, Rates: []int{500, 500}, MaterialsPerAttempt: 1},
		{ID: "std", MaxLevel: 2, Rates: []int{500, 1001}, MaterialsPerAttempt: 1},
	}
	for i, c := range bad {
		if err := l.SetCurve(c); !errors.Is(err, impl.ErrBadCurve) {
			t.Fatalf("curve case %d = %v, want ErrBadCurve", i, err)
		}
	}
	if err := l.SetCurve(sureCurve()); err != nil {
		t.Fatalf("valid curve = %v, want nil", err)
	}
}

func TestSetLevelRejectsOutOfRange(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, sureCurve())
	if err := l.SetLevel("sword", -1); !errors.Is(err, impl.ErrBadLevel) {
		t.Fatalf("level -1 = %v, want ErrBadLevel", err)
	}
	if err := l.SetLevel("sword", 6); !errors.Is(err, impl.ErrBadLevel) {
		t.Fatalf("level 6 on a five step curve = %v, want ErrBadLevel", err)
	}
	if err := l.SetLevel("sword", 2); err != nil {
		t.Fatalf("level 2 = %v, want nil", err)
	}
	level, err := l.Level("sword")
	if err != nil {
		t.Fatalf("read level: %v", err)
	}
	if level != 2 {
		t.Fatalf("level = %d, want 2", level)
	}
}

func TestPreviewReportsRateAndCost(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, impl.EnhancementCurve{
		ID:                  "std",
		MaxLevel:            5,
		Rates:               []int{500, 500, 500, 500, 500},
		MaterialsPerAttempt: 2,
		GoldPerAttempt:      50,
	})
	if err := l.SetLevel("sword", 2); err != nil {
		t.Fatalf("set level: %v", err)
	}
	o, err := l.Preview("p1", "sword")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if o.Level != 2 || o.SuccessPerMille != 500 {
		t.Fatalf("preview = %+v, want level 2 at 500 per mille", o)
	}
	if o.Materials != 2 || o.Gold != 50 {
		t.Fatalf("preview cost = %d materials and %d gold, want 2 and 50", o.Materials, o.Gold)
	}
	if o.PityCount != 0 {
		t.Fatalf("pity counter = %d, want 0", o.PityCount)
	}
	if err := l.SetLevel("sword", 5); err != nil {
		t.Fatalf("set level: %v", err)
	}
	if _, err := l.Preview("p1", "sword"); !errors.Is(err, impl.ErrMaxLevel) {
		t.Fatalf("preview at the maximum level = %v, want ErrMaxLevel", err)
	}
}

func TestAttemptRejectsBadInput(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, sureCurve())
	if _, err := l.Attempt("  ", "sword", false, "k1"); !errors.Is(err, impl.ErrBadInput) {
		t.Fatalf("blank player = %v, want ErrBadInput", err)
	}
	if _, err := l.Attempt("p1", "  ", false, "k1"); !errors.Is(err, impl.ErrBadInput) {
		t.Fatalf("blank item = %v, want ErrBadInput", err)
	}
	if _, err := l.Attempt("p1", "sword", false, "  "); !errors.Is(err, impl.ErrBadInput) {
		t.Fatalf("blank request key = %v, want ErrBadInput", err)
	}
}

func TestAttemptRejectsInsufficientMaterials(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, sureCurve())
	mustMaterials(t, l, "p1", 1)
	if _, err := l.Attempt("p1", "sword", false, "k1"); !errors.Is(err, impl.ErrInsufficientMaterials) {
		t.Fatalf("attempt with one material = %v, want ErrInsufficientMaterials", err)
	}
	if level, err := l.Level("sword"); err != nil || level != 0 {
		t.Fatalf("level after a rejected attempt = %d (%v), want 0", level, err)
	}
	if hist, err := l.History("sword"); err != nil || len(hist) != 0 {
		t.Fatalf("history after a rejected attempt = %+v (%v), want empty", hist, err)
	}
	if stats := l.Stats(); stats.Attempts != 0 {
		t.Fatalf("attempt counter = %d, want 0", stats.Attempts)
	}
}

func TestAttemptRejectsMissingCharm(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, sureCurve())
	mustMaterials(t, l, "p1", 10)
	if _, err := l.Attempt("p1", "sword", true, "k1"); !errors.Is(err, impl.ErrNoCharm) {
		t.Fatalf("attempt without a charm = %v, want ErrNoCharm", err)
	}
	if got := l.Materials("p1"); got != 10 {
		t.Fatalf("materials after the rejected attempt = %d, want 10", got)
	}
}

func TestAttemptSuccessAdvancesLevel(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, sureCurve())
	mustMaterials(t, l, "p1", 10)
	if err := l.SetLevel("sword", 1); err != nil {
		t.Fatalf("set level: %v", err)
	}
	r, err := l.Attempt("p1", "sword", false, "k1")
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if !r.Success || r.FromLevel != 1 || r.ToLevel != 2 {
		t.Fatalf("result = %+v, want a success from level 1 to level 2", r)
	}
	if r.MaterialsSpent != 2 || r.GoldSpent != 50 {
		t.Fatalf("spent %d materials and %d gold, want 2 and 50", r.MaterialsSpent, r.GoldSpent)
	}
	if r.CharmUsed || r.PityApplied || r.Replayed {
		t.Fatalf("result flags = %+v, want a plain fresh success", r)
	}
	if got := l.Materials("p1"); got != 8 {
		t.Fatalf("materials after the attempt = %d, want 8", got)
	}
	if level, _ := l.Level("sword"); level != 2 {
		t.Fatalf("level after the attempt = %d, want 2", level)
	}
	stats := l.Stats()
	if stats.Attempts != 1 || stats.Successes != 1 {
		t.Fatalf("stats = %+v, want one attempt and one success", stats)
	}
}

func TestAttemptFailureWithoutDowngradeKeepsLevel(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, failureCurve(false))
	mustMaterials(t, l, "p1", 10)
	if err := l.SetLevel("sword", 3); err != nil {
		t.Fatalf("set level: %v", err)
	}
	r, err := l.Attempt("p1", "sword", false, "k1")
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if r.Success || r.FromLevel != 3 || r.ToLevel != 3 {
		t.Fatalf("result = %+v, want a failure that keeps level 3", r)
	}
	if level, _ := l.Level("sword"); level != 3 {
		t.Fatalf("level after the failure = %d, want 3", level)
	}
}

func TestAttemptFailureDowngradesAndDamages(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, failureCurve(true))
	mustMaterials(t, l, "p1", 20)
	if err := l.SetLevel("sword", 3); err != nil {
		t.Fatalf("set level: %v", err)
	}
	r, err := l.Attempt("p1", "sword", false, "k1")
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if r.Success || r.FromLevel != 3 || r.ToLevel != 2 {
		t.Fatalf("result = %+v, want a failure that drops level 3 to level 2", r)
	}
	if level, _ := l.Level("sword"); level != 2 {
		t.Fatalf("level after the downgrade = %d, want 2", level)
	}
	if _, err := l.Attempt("p1", "sword", false, "k2"); !errors.Is(err, impl.ErrDamaged) {
		t.Fatalf("attempt on a damaged item = %v, want ErrDamaged", err)
	}
	if got := l.Materials("p1"); got != 18 {
		t.Fatalf("materials after the downgrade = %d, want 18", got)
	}
}

func TestCharmPreventsDowngradeAndIsConsumed(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, failureCurve(true))
	mustMaterials(t, l, "p1", 10)
	if err := l.GrantCharms("p1", 2); err != nil {
		t.Fatalf("grant charms: %v", err)
	}
	if err := l.SetLevel("sword", 3); err != nil {
		t.Fatalf("set level: %v", err)
	}
	r, err := l.Attempt("p1", "sword", true, "k1")
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if r.Success || !r.CharmUsed || r.ToLevel != 3 {
		t.Fatalf("result = %+v, want a protected failure that keeps level 3", r)
	}
	if got := l.Charms("p1"); got != 1 {
		t.Fatalf("charms after the protected failure = %d, want 1", got)
	}
	second, err := l.Attempt("p1", "sword", true, "k2")
	if err != nil {
		t.Fatalf("second protected attempt: %v", err)
	}
	if !second.CharmUsed || second.ToLevel != 3 {
		t.Fatalf("second result = %+v, want another protected failure", second)
	}
	if got := l.Charms("p1"); got != 0 {
		t.Fatalf("charms after two protected failures = %d, want 0", got)
	}
}

func TestRepairClearsDamage(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, failureCurve(true))
	mustMaterials(t, l, "p1", 10)
	if err := l.SetLevel("sword", 3); err != nil {
		t.Fatalf("set level: %v", err)
	}
	if _, err := l.Attempt("p1", "sword", false, "k1"); err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if err := l.Repair("sword"); err != nil {
		t.Fatalf("repair a damaged item: %v", err)
	}
	if err := l.Repair("sword"); !errors.Is(err, impl.ErrNotDamaged) {
		t.Fatalf("repair a healthy item = %v, want ErrNotDamaged", err)
	}
	if err := l.Repair("ghost"); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("repair an unknown item = %v, want ErrUnknownItem", err)
	}
	if _, err := l.Attempt("p1", "sword", false, "k2"); err != nil {
		t.Fatalf("attempt after repair: %v", err)
	}
}

func TestPityGuaranteesAfterThreshold(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, failureCurve(false))
	if err := l.SetPity(impl.PityConfig{Threshold: 2, ResetOnSuccess: true}); err != nil {
		t.Fatalf("set pity: %v", err)
	}
	mustMaterials(t, l, "p1", 20)
	for i, key := range []string{"k1", "k2"} {
		r, err := l.Attempt("p1", "sword", false, key)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		if r.Success || r.PityApplied {
			t.Fatalf("attempt %d = %+v, want a plain failure", i+1, r)
		}
	}
	o, err := l.Preview("p1", "sword")
	if err != nil {
		t.Fatalf("preview at the threshold: %v", err)
	}
	if o.PityCount != 2 {
		t.Fatalf("pity counter = %d, want 2", o.PityCount)
	}
	r, err := l.Attempt("p1", "sword", false, "k3")
	if err != nil {
		t.Fatalf("guaranteed attempt: %v", err)
	}
	if !r.Success || !r.PityApplied || r.ToLevel != 1 {
		t.Fatalf("guaranteed attempt = %+v, want a pity success to level 1", r)
	}
	after, err := l.Preview("p1", "sword")
	if err != nil {
		t.Fatalf("preview after the pity success: %v", err)
	}
	if after.PityCount != 0 {
		t.Fatalf("pity counter after the guaranteed success = %d, want 0", after.PityCount)
	}
}

func TestPityIsCountedPerItem(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, failureCurve(false))
	if err := l.SetPity(impl.PityConfig{Threshold: 2}); err != nil {
		t.Fatalf("set pity: %v", err)
	}
	mustMaterials(t, l, "p1", 20)
	for i, key := range []string{"b1", "b2"} {
		if _, err := l.Attempt("p1", "blade", false, key); err != nil {
			t.Fatalf("blade attempt %d: %v", i+1, err)
		}
	}
	shield, err := l.Attempt("p1", "shield", false, "s1")
	if err != nil {
		t.Fatalf("shield attempt: %v", err)
	}
	if shield.Success || shield.PityApplied {
		t.Fatalf("first shield attempt = %+v, want no pity carry over from the blade", shield)
	}
	o, err := l.Preview("p1", "shield")
	if err != nil {
		t.Fatalf("preview the shield: %v", err)
	}
	if o.PityCount != 1 {
		t.Fatalf("shield pity counter = %d, want 1", o.PityCount)
	}
	blade, err := l.Attempt("p1", "blade", false, "b3")
	if err != nil {
		t.Fatalf("third blade attempt: %v", err)
	}
	if !blade.Success || !blade.PityApplied {
		t.Fatalf("third blade attempt = %+v, want the blade pity guarantee", blade)
	}
}

func TestDuplicateRequestKeyReturnsSameResult(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, sureCurve())
	mustMaterials(t, l, "p1", 10)
	if err := l.SetLevel("sword", 1); err != nil {
		t.Fatalf("set level: %v", err)
	}
	first, err := l.Attempt("p1", "sword", false, "k1")
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	second, err := l.Attempt("p1", "sword", false, "k1")
	if err != nil {
		t.Fatalf("replayed attempt: %v", err)
	}
	if !second.Replayed {
		t.Fatalf("second attempt was not marked as a replay: %+v", second)
	}
	if second.FromLevel != first.FromLevel || second.ToLevel != first.ToLevel || second.Success != first.Success {
		t.Fatalf("replay = %+v, want the same outcome as %+v", second, first)
	}
	if got := l.Materials("p1"); got != 8 {
		t.Fatalf("materials after the replay = %d, want 8", got)
	}
	hist, err := l.History("sword")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history after the replay = %+v, want one record", hist)
	}
	stats := l.Stats()
	if stats.Attempts != 1 || stats.Replays != 1 {
		t.Fatalf("stats = %+v, want one attempt and one replay", stats)
	}
}

func TestSnapshotRestoreKeepsPityAndMaterials(t *testing.T) {
	l := newLedger(t)
	mustCurve(t, l, failureCurve(false))
	if err := l.SetPity(impl.PityConfig{Threshold: 2}); err != nil {
		t.Fatalf("set pity: %v", err)
	}
	mustMaterials(t, l, "p1", 20)
	for i, key := range []string{"k1", "k2"} {
		if _, err := l.Attempt("p1", "sword", false, key); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewEnhancementAttemptLedger(ledgerConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	o, err := restored.Preview("p1", "sword")
	if err != nil {
		t.Fatalf("preview after restore: %v", err)
	}
	if o.PityCount != 2 || o.Materials != 2 {
		t.Fatalf("restored odds = %+v, want pity 2 and a cost of 2", o)
	}
	if got := restored.Materials("p1"); got != 16 {
		t.Fatalf("restored materials = %d, want 16", got)
	}
	r, err := restored.Attempt("p1", "sword", false, "k3")
	if err != nil {
		t.Fatalf("attempt after restore: %v", err)
	}
	if !r.Success || !r.PityApplied {
		t.Fatalf("attempt after restore = %+v, want the pity success to survive the reload", r)
	}
}
