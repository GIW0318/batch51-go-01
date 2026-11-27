package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-10"
)

var aliasBase = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func testAliasConfig() impl.AliasConfig {
	return impl.AliasConfig{
		Tenant:        "acme",
		Retention:     72 * time.Hour,
		MaxChainDepth: 8,
		MaxHistory:    8,
		MaxReserved:   8,
		Now:           func() time.Time { return aliasBase },
	}
}

func mustRegister(t *testing.T, l *impl.PrincipalAliasLedger, principal, name string) {
	t.Helper()
	if err := l.Register(principal, name, aliasBase); err != nil {
		t.Fatalf("register %s as %s: %v", principal, name, err)
	}
}

func TestRegisterCreatesLiveName(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	mustRegister(t, l, "p1", "alice")
	res, err := l.Resolve("alice")
	if err != nil {
		t.Fatalf("resolve a live name: %v", err)
	}
	if !res.Found || res.Alias || res.Principal != "p1" {
		t.Fatalf("resolution = %+v, want a live hit on p1", res)
	}
	if res.Canonical != "alice" {
		t.Fatalf("canonical name = %q, want alice", res.Canonical)
	}
	chain, err := l.Chain("p1")
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 1 || chain[0] != "alice" {
		t.Fatalf("chain = %v, want just the live name", chain)
	}
	history, err := l.History("p1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("a fresh principal must have no retired aliases, got %+v", history)
	}
}

func TestRegisterRejectsBlankDuplicateAndExistingPrincipal(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	if err := l.Register("", "alice", aliasBase); !errors.Is(err, impl.ErrBlankName) {
		t.Fatalf("blank principal must fail with ErrBlankName, got %v", err)
	}
	if err := l.Register("p1", "  ", aliasBase); !errors.Is(err, impl.ErrBlankName) {
		t.Fatalf("blank name must fail with ErrBlankName, got %v", err)
	}
	mustRegister(t, l, "p1", "alice")
	if err := l.Register("p2", "alice", aliasBase); !errors.Is(err, impl.ErrNameTaken) {
		t.Fatalf("a live name must not be registered twice, got %v", err)
	}
	if err := l.Register("p1", "alice2", aliasBase); !errors.Is(err, impl.ErrPrincipalExists) {
		t.Fatalf("an existing principal must not register again, got %v", err)
	}
}

func TestRenameMovesNameAndKeepsAlias(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	mustRegister(t, l, "p1", "alice")
	res, err := l.Rename("p1", "alice2", aliasBase)
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if res.Principal != "p1" || res.OldName != "alice" || res.NewName != "alice2" {
		t.Fatalf("rename result = %+v, want p1 alice -> alice2", res)
	}
	if res.Seq <= 0 {
		t.Fatalf("rename seq = %d, want a positive number", res.Seq)
	}
	if len(res.Chain) != 2 || res.Chain[0] != "alice" || res.Chain[1] != "alice2" {
		t.Fatalf("rename chain = %v, want [alice alice2]", res.Chain)
	}
	old, err := l.Resolve("alice")
	if err != nil {
		t.Fatalf("resolve the retired name inside retention: %v", err)
	}
	if !old.Found || !old.Alias || old.Principal != "p1" || old.Canonical != "alice2" {
		t.Fatalf("retired name resolved to %+v, want an alias hit on p1", old)
	}
	live, err := l.Resolve("alice2")
	if err != nil {
		t.Fatalf("resolve the new name: %v", err)
	}
	if live.Alias || live.Canonical != "alice2" || live.Principal != "p1" {
		t.Fatalf("new name resolved to %+v, want a live hit", live)
	}
	history, err := l.History("p1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 || history[0].Name != "alice" {
		t.Fatalf("history = %+v, want the retired name alice", history)
	}
	if !history[0].Until.Equal(aliasBase.Add(72 * time.Hour)) {
		t.Fatalf("retention until = %v, want signing instant plus the 72h retention", history[0].Until)
	}
}

func TestRenameRejectsUnknownAndSameName(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	if _, err := l.Rename("ghost", "alice2", aliasBase); !errors.Is(err, impl.ErrUnknownPrincipal) {
		t.Fatalf("renaming an unknown principal must fail with ErrUnknownPrincipal, got %v", err)
	}
	mustRegister(t, l, "p1", "alice")
	if _, err := l.Rename("p1", "alice", aliasBase); !errors.Is(err, impl.ErrSameName) {
		t.Fatalf("renaming to the current name must fail with ErrSameName, got %v", err)
	}
}

func TestRenameRejectsNameTakenByAnother(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	mustRegister(t, l, "p1", "alice")
	mustRegister(t, l, "p2", "bob")
	if _, err := l.Rename("p1", "bob", aliasBase); !errors.Is(err, impl.ErrNameTaken) {
		t.Fatalf("renaming onto a live name must fail with ErrNameTaken, got %v", err)
	}
	if res, err := l.Resolve("bob"); err != nil || res.Principal != "p2" {
		t.Fatalf("bob resolved to %+v err=%v, want p2 untouched", res, err)
	}
	if res, err := l.Resolve("alice"); err != nil || res.Principal != "p1" || res.Alias {
		t.Fatalf("alice resolved to %+v err=%v, want p1 still live there", res, err)
	}
}

func TestResolveUnknownNameFails(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	if _, err := l.Resolve("nobody"); !errors.Is(err, impl.ErrNameNotFound) {
		t.Fatalf("an unknown name must fail with ErrNameNotFound, got %v", err)
	}
	if _, err := l.Resolve("   "); !errors.Is(err, impl.ErrBlankName) {
		t.Fatalf("a blank name must fail with ErrBlankName, got %v", err)
	}
}

func TestMultiStepChainResolvesToCanonical(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	mustRegister(t, l, "p1", "alice")
	if _, err := l.Rename("p1", "alice2", aliasBase); err != nil {
		t.Fatalf("first rename: %v", err)
	}
	if _, err := l.Rename("p1", "alice3", aliasBase); err != nil {
		t.Fatalf("second rename: %v", err)
	}
	for _, retired := range []string{"alice", "alice2"} {
		res, err := l.Resolve(retired)
		if err != nil {
			t.Fatalf("resolve %s: %v", retired, err)
		}
		if !res.Found || !res.Alias || res.Principal != "p1" || res.Canonical != "alice3" {
			t.Fatalf("%s resolved to %+v, want the canonical alice3 of p1", retired, res)
		}
	}
	chain, err := l.Chain("p1")
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	want := []string{"alice", "alice2", "alice3"}
	if len(chain) != len(want) {
		t.Fatalf("chain = %v, want %v", chain, want)
	}
	for i, name := range want {
		if chain[i] != name {
			t.Fatalf("chain = %v, want %v", chain, want)
		}
	}
	history, err := l.History("p1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 || history[0].Name != "alice" || history[1].Name != "alice2" {
		t.Fatalf("history = %+v, want alice then alice2", history)
	}
	if history[1].Seq <= history[0].Seq {
		t.Fatalf("history sequence must grow, got %+v", history)
	}
}

func TestReserveBlocksRenameAndRegister(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	if err := l.Reserve("brand", aliasBase.Add(72*time.Hour), "p9"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	mustRegister(t, l, "p1", "alice")
	if _, err := l.Rename("p1", "brand", aliasBase); !errors.Is(err, impl.ErrNameTaken) {
		t.Fatalf("renaming onto a reserved name must fail with ErrNameTaken, got %v", err)
	}
	if err := l.Register("p2", "brand", aliasBase); !errors.Is(err, impl.ErrNameTaken) {
		t.Fatalf("registering a reserved name must fail with ErrNameTaken, got %v", err)
	}
	res, err := l.Resolve("brand")
	if err != nil {
		t.Fatalf("resolving a reserved name: %v", err)
	}
	if res.Found || !res.Reserved {
		t.Fatalf("reserved name resolved to %+v, want reserved and not found", res)
	}
}

func TestReserveReleaseFreesName(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	if err := l.Reserve("brand", aliasBase.Add(72*time.Hour), "p9"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := l.Release("brand"); err != nil {
		t.Fatalf("release: %v", err)
	}
	mustRegister(t, l, "p2", "brand")
	if err := l.Release("brand"); !errors.Is(err, impl.ErrNotReserved) {
		t.Fatalf("releasing twice must fail with ErrNotReserved, got %v", err)
	}
}

func TestReserveRejectsDuplicateAndCapacity(t *testing.T) {
	cfg := testAliasConfig()
	cfg.MaxReserved = 2
	l := impl.NewPrincipalAliasLedger(cfg)
	if err := l.Reserve("r1", aliasBase.Add(time.Hour), "p1"); err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	if err := l.Reserve("r1", aliasBase.Add(time.Hour), "p2"); !errors.Is(err, impl.ErrNameTaken) {
		t.Fatalf("a duplicate reservation must fail with ErrNameTaken, got %v", err)
	}
	if err := l.Reserve("r2", aliasBase.Add(time.Hour), "p1"); err != nil {
		t.Fatalf("second reservation: %v", err)
	}
	if err := l.Reserve("r3", aliasBase.Add(time.Hour), "p1"); !errors.Is(err, impl.ErrReserveLimit) {
		t.Fatalf("a third reservation must hit ErrReserveLimit, got %v", err)
	}
	if got := len(l.Reservations(aliasBase)); got != 2 {
		t.Fatalf("reservations = %d, want the cap 2", got)
	}
}

func TestChainDepthLimitRejectsRename(t *testing.T) {
	cfg := testAliasConfig()
	cfg.MaxChainDepth = 2
	l := impl.NewPrincipalAliasLedger(cfg)
	mustRegister(t, l, "p1", "alice")
	for _, name := range []string{"alice2", "alice3"} {
		if _, err := l.Rename("p1", name, aliasBase); err != nil {
			t.Fatalf("rename to %s must stay inside the depth: %v", name, err)
		}
	}
	if _, err := l.Rename("p1", "alice4", aliasBase); !errors.Is(err, impl.ErrChainDepth) {
		t.Fatalf("a third rename must hit ErrChainDepth, got %v", err)
	}
	chain, err := l.Chain("p1")
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 3 || chain[2] != "alice3" {
		t.Fatalf("chain = %v, want it unchanged at alice3", chain)
	}
	if res, err := l.Resolve("alice3"); err != nil || res.Principal != "p1" {
		t.Fatalf("alice3 resolved to %+v err=%v, want p1 still live there", res, err)
	}
}

func TestHistoryLimitRejectsRename(t *testing.T) {
	cfg := testAliasConfig()
	cfg.MaxHistory = 1
	l := impl.NewPrincipalAliasLedger(cfg)
	mustRegister(t, l, "p1", "alice")
	if _, err := l.Rename("p1", "alice2", aliasBase); err != nil {
		t.Fatalf("first rename: %v", err)
	}
	if _, err := l.Rename("p1", "alice3", aliasBase); !errors.Is(err, impl.ErrHistoryLimit) {
		t.Fatalf("a second rename must hit ErrHistoryLimit, got %v", err)
	}
	history, err := l.History("p1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %+v, want the single retained alias", history)
	}
	if res, err := l.Resolve("alice2"); err != nil || res.Principal != "p1" || res.Alias {
		t.Fatalf("alice2 resolved to %+v err=%v, want it still live", res, err)
	}
}

func TestConcurrentRenameSameTargetOnlyOneWins(t *testing.T) {
	cfg := testAliasConfig()
	l := impl.NewPrincipalAliasLedger(cfg)
	principals := []string{"p1", "p2", "p3", "p4", "p5", "p6"}
	for _, p := range principals {
		mustRegister(t, l, p, p+"-name")
	}
	results := make([]error, len(principals))
	done := make(chan struct{})
	for i, p := range principals {
		go func(i int, p string) {
			_, err := l.Rename(p, "shared", aliasBase)
			results[i] = err
			done <- struct{}{}
		}(i, p)
	}
	for range principals {
		<-done
	}
	winners, taken := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, impl.ErrNameTaken):
			taken++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent renames to one name produced %d winners, want 1 (%v)", winners, results)
	}
	if taken != 5 {
		t.Fatalf("concurrent renames reported %d ErrNameTaken, want 5 (%v)", taken, results)
	}
	res, err := l.Resolve("shared")
	if err != nil {
		t.Fatalf("resolve the winner: %v", err)
	}
	if !res.Found || res.Alias {
		t.Fatalf("shared resolved to %+v, want a live winner", res)
	}
	for _, p := range principals {
		if p == res.Principal {
			continue
		}
		name := p + "-name"
		if r, err := l.Resolve(name); err != nil || r.Principal != p || r.Alias {
			t.Fatalf("loser %s name resolved to %+v err=%v, want it untouched", p, r, err)
		}
	}
}

func TestPruneRemovesExpiredAliasesAndReservations(t *testing.T) {
	l := impl.NewPrincipalAliasLedger(testAliasConfig())
	mustRegister(t, l, "p1", "alice")
	if _, err := l.Rename("p1", "alice2", aliasBase); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := l.Reserve("brand", aliasBase.Add(24*time.Hour), "p9"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	later := aliasBase.Add(96 * time.Hour)
	if _, err := l.Prune(later); err != nil {
		t.Fatalf("prune: %v", err)
	}
	removed, err := l.Prune(later)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if removed != 0 {
		t.Fatalf("a repeated prune removed %d more entries, want 0", removed)
	}
	if _, err := l.Resolve("alice"); !errors.Is(err, impl.ErrNameNotFound) {
		t.Fatalf("an alias past retention must not resolve, got %v", err)
	}
	if got := len(l.Reservations(later)); got != 0 {
		t.Fatalf("reservations after prune = %d, want 0", got)
	}
	if res, err := l.Resolve("alice2"); err != nil || !res.Found || res.Alias {
		t.Fatalf("the live name must survive pruning, got %+v err=%v", res, err)
	}
}

func TestSnapshotRestoreKeepsAliasesAndReservations(t *testing.T) {
	cfg := testAliasConfig()
	l := impl.NewPrincipalAliasLedger(cfg)
	mustRegister(t, l, "p1", "alice")
	first, err := l.Rename("p1", "alice2", aliasBase)
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := l.Reserve("brand", aliasBase.Add(72*time.Hour), "p9"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewPrincipalAliasLedger(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	res, err := restored.Resolve("alice")
	if err != nil {
		t.Fatalf("resolve the retired name after restore: %v", err)
	}
	if !res.Alias || res.Canonical != "alice2" || res.Principal != "p1" {
		t.Fatalf("retired name after restore = %+v, want an alias of p1", res)
	}
	if err := restored.Reserve("brand", aliasBase.Add(time.Hour), "p2"); !errors.Is(err, impl.ErrNameTaken) {
		t.Fatalf("the restored reservation must still hold the name, got %v", err)
	}
	second, err := restored.Rename("p1", "alice3", aliasBase)
	if err != nil {
		t.Fatalf("rename after restore: %v", err)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("sequence after restore = %d, must continue past %d", second.Seq, first.Seq)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if live, err := restored.Resolve("alice3"); err != nil || live.Principal != "p1" || live.Alias {
		t.Fatalf("a rejected restore must leave alice3 live, got %+v err=%v", live, err)
	}
}
