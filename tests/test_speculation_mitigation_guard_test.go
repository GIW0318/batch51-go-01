package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-d-02"
)

func hostProfile() impl.HostProfile {
	return impl.HostProfile{
		Model:     "Xeon Gold 6338",
		Vulns:     impl.VulnSpectre1 | impl.VulnSpectre2 | impl.VulnMeltdown,
		Supported: []string{impl.FeatRetpoline, impl.FeatIbrs, impl.FeatSsbd, impl.FeatIbpb, impl.FeatL1dFlush},
	}
}

func basePolicy() impl.Policy {
	return impl.Policy{
		Name:     "baseline",
		Required: []string{impl.FeatSsbd, impl.FeatRetpoline, impl.FeatSsbd, impl.FeatIbrs},
	}
}

func guardConfig() impl.GuardConfig {
	return impl.GuardConfig{MaxRelax: 30 * time.Minute, DefaultLevel: 0}
}

func mustPlan(t *testing.T, g *impl.SpeculationMitigationGuard, host impl.HostProfile, policy impl.Policy) impl.MitigationPlan {
	t.Helper()
	plan, err := g.Plan(host, policy)
	if err != nil {
		t.Fatalf("plan %q: %v", policy.Name, err)
	}
	return plan
}

func TestPlanSortsDedupsAndScoresFeatures(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	plan := mustPlan(t, g, hostProfile(), basePolicy())
	want := []string{impl.FeatIbrs, impl.FeatRetpoline, impl.FeatSsbd}
	if len(plan.Features) != len(want) {
		t.Fatalf("plan features = %v, want the deduplicated set %v", plan.Features, want)
	}
	for i, name := range want {
		if plan.Features[i] != name {
			t.Fatalf("plan features = %v, want the sorted set %v", plan.Features, want)
		}
	}
	wantLevel := impl.WeightIbrs + impl.WeightRetpoline + impl.WeightSsbd
	if plan.Level != wantLevel {
		t.Fatalf("plan level = %d, want the summed weight %d", plan.Level, wantLevel)
	}
	if plan.Policy != "baseline" {
		t.Fatalf("plan policy = %q, want baseline", plan.Policy)
	}
	if !plan.Satisfiable {
		t.Fatalf("plan satisfiable = false, want true for a fully supported policy")
	}
	if len(plan.Missing) != 0 {
		t.Fatalf("plan missing = %v, want none", plan.Missing)
	}
	floored := mustPlan(t, g, hostProfile(), impl.Policy{Name: "raised", Required: []string{impl.FeatIbpb}, MinLevel: 9})
	if floored.Level != 9 {
		t.Fatalf("plan level with a floor of 9 = %d, want 9", floored.Level)
	}
	if len(floored.Features) != 1 || floored.Features[0] != impl.FeatIbpb {
		t.Fatalf("floored plan features = %v, want [%s]", floored.Features, impl.FeatIbpb)
	}
}

func TestPlanValidatesPolicy(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	blank := hostProfile()
	blank.Model = "   "
	if _, err := g.Plan(blank, basePolicy()); !errors.Is(err, impl.ErrBadPolicy) {
		t.Fatalf("blank host model must fail with ErrBadPolicy, got %v", err)
	}
	if _, err := g.Plan(hostProfile(), impl.Policy{Name: "empty"}); !errors.Is(err, impl.ErrBadPolicy) {
		t.Fatalf("a policy without features must fail with ErrBadPolicy, got %v", err)
	}
	if _, err := g.Plan(hostProfile(), impl.Policy{Name: "odd", Required: []string{"magic_shield"}}); !errors.Is(err, impl.ErrBadPolicy) {
		t.Fatalf("an unknown feature name must fail with ErrBadPolicy, got %v", err)
	}
	if got := impl.FeatureWeight(impl.FeatMdClear); got != impl.WeightMdClear {
		t.Fatalf("weight of %s = %d, want %d", impl.FeatMdClear, got, impl.WeightMdClear)
	}
	if got := impl.FeatureWeight("magic_shield"); got != 0 {
		t.Fatalf("weight of an unknown feature = %d, want 0", got)
	}
}

func TestPlanRejectsUnsupportedFeature(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	host := impl.HostProfile{
		Model:     "EPYC 7443",
		Vulns:     impl.VulnMds,
		Supported: []string{impl.FeatRetpoline},
	}
	plan, err := g.Plan(host, impl.Policy{Name: "strict", Required: []string{impl.FeatRetpoline, impl.FeatMdClear}})
	if !errors.Is(err, impl.ErrUnsatisfiable) {
		t.Fatalf("a policy needing an unavailable mitigation must fail with ErrUnsatisfiable, got %v", err)
	}
	if len(plan.Missing) != 1 || plan.Missing[0] != impl.FeatMdClear {
		t.Fatalf("plan missing = %v, want [%s]", plan.Missing, impl.FeatMdClear)
	}
	if len(plan.Features) != 1 || plan.Features[0] != impl.FeatRetpoline {
		t.Fatalf("plan features = %v, want only the supported %s", plan.Features, impl.FeatRetpoline)
	}
}

func TestPlanDigestIsStable(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	first := mustPlan(t, g, hostProfile(), basePolicy())
	second := mustPlan(t, g, hostProfile(), basePolicy())
	if first.Digest == "" {
		t.Fatalf("plan digest must not be empty")
	}
	if len(first.Digest) != 64 {
		t.Fatalf("plan digest = %q, want 64 hex characters", first.Digest)
	}
	if first.Digest != second.Digest {
		t.Fatalf("the same policy produced %q then %q, want a stable digest", first.Digest, second.Digest)
	}
	other := mustPlan(t, g, hostProfile(), impl.Policy{Name: "other", Required: []string{impl.FeatSsbd, impl.FeatRetpoline, impl.FeatIbrs}})
	if other.Digest == first.Digest {
		t.Fatalf("a different policy name reused the digest %q", other.Digest)
	}
}

func TestApplyStoresPlanState(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	plan := mustPlan(t, g, hostProfile(), basePolicy())
	if err := g.Apply(4242, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	state, err := g.Read(4242)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.PID != 4242 {
		t.Fatalf("state pid = %d, want 4242", state.PID)
	}
	if state.Level != plan.Level {
		t.Fatalf("state level = %d, want the plan level %d", state.Level, plan.Level)
	}
	if state.BaseLevel != plan.Level {
		t.Fatalf("state base level = %d, want %d", state.BaseLevel, plan.Level)
	}
	if len(state.Features) != len(plan.Features) {
		t.Fatalf("state features = %v, want %v", state.Features, plan.Features)
	}
	for i := range plan.Features {
		if state.Features[i] != plan.Features[i] {
			t.Fatalf("state features = %v, want %v", state.Features, plan.Features)
		}
	}
	if state.Generation != 1 {
		t.Fatalf("state generation = %d, want 1 after the first apply", state.Generation)
	}
	if state.Relaxed {
		t.Fatalf("state relaxed = true, want false before any relaxation")
	}
}

func TestApplyRejectsDowngrade(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	high := mustPlan(t, g, hostProfile(), basePolicy())
	if err := g.Apply(4300, high); err != nil {
		t.Fatalf("apply high plan: %v", err)
	}
	low := mustPlan(t, g, hostProfile(), impl.Policy{Name: "low", Required: []string{impl.FeatIbpb}})
	if err := g.Apply(4300, low); !errors.Is(err, impl.ErrDowngradeBlocked) {
		t.Fatalf("a lower plan must fail with ErrDowngradeBlocked, got %v", err)
	}
	state, err := g.Read(4300)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Level != high.Level {
		t.Fatalf("state level = %d, want the original %d after the rejected downgrade", state.Level, high.Level)
	}
	if state.Generation != 1 {
		t.Fatalf("state generation = %d, want the rejected apply to leave 1", state.Generation)
	}
}

func TestReadUnknownProcess(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	if _, err := g.Read(9999); !errors.Is(err, impl.ErrUnknownProcess) {
		t.Fatalf("reading an untracked process must fail with ErrUnknownProcess, got %v", err)
	}
	if err := g.Apply(0, impl.MitigationPlan{Level: 3, Satisfiable: true}); !errors.Is(err, impl.ErrBadProcess) {
		t.Fatalf("applying to pid 0 must fail with ErrBadProcess, got %v", err)
	}
}

func TestRelaxValidatesRequest(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	plan := mustPlan(t, g, hostProfile(), basePolicy())
	if err := g.Apply(4400, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := g.Relax(4400, impl.FeatIbrs, "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("a blank reason must fail with ErrBadReason, got %v", err)
	}
	state, err := g.Read(4400)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Level != plan.Level || state.Relaxed {
		t.Fatalf("state level=%d relaxed=%v, want the untouched %d and false", state.Level, state.Relaxed, plan.Level)
	}
	if err := g.Relax(4400, impl.FeatMdClear, "perf regression"); !errors.Is(err, impl.ErrUnknownFeature) {
		t.Fatalf("relaxing a feature outside the level must fail with ErrUnknownFeature, got %v", err)
	}
	if err := g.Relax(4444, impl.FeatIbrs, "perf regression"); !errors.Is(err, impl.ErrUnknownProcess) {
		t.Fatalf("relaxing an untracked process must fail with ErrUnknownProcess, got %v", err)
	}
}

func TestRelaxLowersEffectiveLevel(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	plan := mustPlan(t, g, hostProfile(), basePolicy())
	if err := g.Apply(4500, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := g.Relax(4500, impl.FeatIbrs, "customer benchmark"); err != nil {
		t.Fatalf("relax: %v", err)
	}
	state, err := g.Read(4500)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wantLevel := plan.Level - impl.WeightIbrs
	if state.Level != wantLevel {
		t.Fatalf("state level = %d, want %d after relaxing %s", state.Level, wantLevel, impl.FeatIbrs)
	}
	if !state.Relaxed || state.RelaxFeature != impl.FeatIbrs {
		t.Fatalf("state relaxed=%v feature=%q, want true and %q", state.Relaxed, state.RelaxFeature, impl.FeatIbrs)
	}
	if state.BaseLevel != plan.Level {
		t.Fatalf("state base level = %d, want the original %d", state.BaseLevel, plan.Level)
	}
	for _, name := range state.Features {
		if name == impl.FeatIbrs {
			t.Fatalf("state features = %v, want the relaxed feature removed", state.Features)
		}
	}
	if len(state.Features) != 2 {
		t.Fatalf("state features = %v, want the two remaining features", state.Features)
	}
	if state.Generation != 2 {
		t.Fatalf("state generation = %d, want 2 after apply and relax", state.Generation)
	}
}

func TestInheritRaisesLowChildAndKeepsHighChild(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	plan := mustPlan(t, g, hostProfile(), basePolicy())
	if err := g.Apply(4600, plan); err != nil {
		t.Fatalf("apply parent: %v", err)
	}
	if err := g.Inherit(4600, 4601); err != nil {
		t.Fatalf("inherit low child: %v", err)
	}
	child, err := g.Read(4601)
	if err != nil {
		t.Fatalf("read child: %v", err)
	}
	if child.Level != plan.Level {
		t.Fatalf("child level = %d, want the parent level %d", child.Level, plan.Level)
	}
	if child.Generation != 1 {
		t.Fatalf("child generation = %d, want 1 after the copy", child.Generation)
	}
	raised := mustPlan(t, g, hostProfile(), impl.Policy{Name: "raised", Required: []string{impl.FeatVerw}, MinLevel: 12})
	if err := g.Apply(4602, raised); err != nil {
		t.Fatalf("apply raised child: %v", err)
	}
	if err := g.Inherit(4600, 4602); err != nil {
		t.Fatalf("inherit onto a higher child: %v", err)
	}
	keeper, err := g.Read(4602)
	if err != nil {
		t.Fatalf("read raised child: %v", err)
	}
	if keeper.Level != 12 {
		t.Fatalf("child level = %d, want the higher 12 to survive inheritance", keeper.Level)
	}
	if keeper.Generation != 1 {
		t.Fatalf("child generation = %d, want the no-op inheritance to leave 1", keeper.Generation)
	}
	if err := g.Inherit(9999, 4700); !errors.Is(err, impl.ErrUnknownProcess) {
		t.Fatalf("inheriting from an untracked parent must fail with ErrUnknownProcess, got %v", err)
	}
}

func TestExecRetainsState(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	plan := mustPlan(t, g, hostProfile(), basePolicy())
	if err := g.Apply(4800, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	state, err := g.Exec(4800)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if state.Level != plan.Level {
		t.Fatalf("level across exec = %d, want %d", state.Level, plan.Level)
	}
	if state.Generation != 2 {
		t.Fatalf("generation across exec = %d, want 2", state.Generation)
	}
	read, err := g.Read(4800)
	if err != nil {
		t.Fatalf("read after exec: %v", err)
	}
	if read.Features == nil || len(read.Features) != len(plan.Features) {
		t.Fatalf("features after exec = %v, want %v", read.Features, plan.Features)
	}
	if _, err := g.Exec(4888); !errors.Is(err, impl.ErrUnknownProcess) {
		t.Fatalf("exec of an untracked process must fail with ErrUnknownProcess, got %v", err)
	}
}

func TestExitForgetsProcess(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	plan := mustPlan(t, g, hostProfile(), basePolicy())
	if err := g.Apply(4850, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := g.Exit(4850); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if _, err := g.Read(4850); !errors.Is(err, impl.ErrUnknownProcess) {
		t.Fatalf("reading a finished process must fail with ErrUnknownProcess, got %v", err)
	}
	if err := g.Exit(4850); !errors.Is(err, impl.ErrUnknownProcess) {
		t.Fatalf("repeating exit must fail with ErrUnknownProcess, got %v", err)
	}
	if err := g.Apply(4850, plan); err != nil {
		t.Fatalf("reusing the pid after exit must start from scratch, got %v", err)
	}
	state, err := g.Read(4850)
	if err != nil {
		t.Fatalf("read after reuse: %v", err)
	}
	if state.Generation != 1 {
		t.Fatalf("generation after pid reuse = %d, want 1", state.Generation)
	}
}

func TestConcurrentApplyKeepsHighestLevel(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			plan, err := g.Plan(hostProfile(), impl.Policy{
				Name:     "tier",
				Required: []string{impl.FeatIbpb},
				MinLevel: i + 1,
			})
			if err != nil {
				return
			}
			_ = g.Apply(4900, plan)
		}(i)
	}
	wg.Wait()
	state, err := g.Read(4900)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Level != 16 {
		t.Fatalf("level after the concurrent applies = %d, want the highest requested 16", state.Level)
	}
	if len(state.Features) == 0 {
		t.Fatalf("features after the concurrent applies = %v, want the winning plan feature", state.Features)
	}
}

func TestSnapshotRestoreKeepsState(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	plan := mustPlan(t, g, hostProfile(), basePolicy())
	if err := g.Apply(5000, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := g.Relax(5000, impl.FeatRetpoline, "capacity test"); err != nil {
		t.Fatalf("relax: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewSpeculationMitigationGuard(guardConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	state, err := restored.Read(5000)
	if err != nil {
		t.Fatalf("read after restore: %v", err)
	}
	if !state.Relaxed || state.RelaxFeature != impl.FeatRetpoline {
		t.Fatalf("restored relaxation = %v/%q, want true and %q", state.Relaxed, state.RelaxFeature, impl.FeatRetpoline)
	}
	if state.BaseLevel != plan.Level {
		t.Fatalf("restored base level = %d, want %d", state.BaseLevel, plan.Level)
	}
	if state.Level != plan.Level-impl.WeightRetpoline {
		t.Fatalf("restored level = %d, want %d", state.Level, plan.Level-impl.WeightRetpoline)
	}
	if state.Generation != 2 {
		t.Fatalf("restored generation = %d, want 2", state.Generation)
	}
	if _, err := restored.Read(5001); !errors.Is(err, impl.ErrUnknownProcess) {
		t.Fatalf("restore must not invent processes, got %v", err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewSpeculationMitigationGuard(guardConfig())
	plan := mustPlan(t, g, hostProfile(), basePolicy())
	if err := g.Apply(5100, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, payload := range [][]byte{nil, []byte("{not-a-snapshot")} {
		if err := g.Restore(payload); !errors.Is(err, impl.ErrBadSnapshot) {
			t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
		}
	}
	state, err := g.Read(5100)
	if err != nil {
		t.Fatalf("read after the rejected restore: %v", err)
	}
	if state.Level != plan.Level {
		t.Fatalf("level after the rejected restore = %d, want the original %d", state.Level, plan.Level)
	}
}
