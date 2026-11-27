package task

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func selfHost() HostProfile {
	return HostProfile{
		Model:     "Xeon",
		Vulns:     VulnSpectre1 | VulnSpectre2 | VulnMeltdown,
		Supported: []string{FeatRetpoline, FeatIbrs, FeatSsbd, FeatIbpb, FeatL1dFlush},
	}
}

func selfPlan(t *testing.T, g *SpeculationMitigationGuard) MitigationPlan {
	t.Helper()
	p, err := g.Plan(selfHost(), Policy{Name: "base", Required: []string{FeatSsbd, FeatRetpoline, FeatIbrs}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSelfTickAndRestoreDeadline(t *testing.T) {
	g := NewSpeculationMitigationGuard(GuardConfig{MaxRelax: 30 * time.Minute})
	p := selfPlan(t, g)
	if err := g.Apply(6001, p); err != nil {
		t.Fatal(err)
	}
	if err := g.Relax(6001, FeatRetpoline, "load test"); err != nil {
		t.Fatal(err)
	}
	before, _ := g.Read(6001)
	if before.Level != p.Level-WeightRetpoline || !before.Relaxed {
		t.Fatalf("relax state %+v", before)
	}
	if got := g.Tick(time.Now().Add(10 * time.Minute)); len(got) != 0 {
		t.Fatalf("early tick events = %+v", got)
	}

	data, err := g.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r := NewSpeculationMitigationGuard(GuardConfig{MaxRelax: 30 * time.Minute})
	if err := r.Restore(data); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(31 * time.Minute)
	events := r.Tick(now)
	if len(events) != 1 {
		t.Fatalf("expiry events = %+v", events)
	}
	e := events[0]
	if e.PID != 6001 || e.Feature != FeatRetpoline || e.Level != p.Level || !e.At.Equal(now) {
		t.Fatalf("event = %+v", e)
	}
	after, err := r.Read(6001)
	if err != nil {
		t.Fatal(err)
	}
	if after.Level != p.Level || after.Relaxed || after.RelaxFeature != "" || !after.RelaxUntil.IsZero() || after.BaseLevel != p.Level {
		t.Fatalf("restored state after expiry = %+v", after)
	}
}

func TestSelfInheritDuringRelaxUsesBaseline(t *testing.T) {
	g := NewSpeculationMitigationGuard(GuardConfig{MaxRelax: time.Hour})
	p := selfPlan(t, g)
	if err := g.Apply(6100, p); err != nil {
		t.Fatal(err)
	}
	if err := g.Relax(6100, FeatIbrs, "bench"); err != nil {
		t.Fatal(err)
	}
	if err := g.Inherit(6100, 6101); err != nil {
		t.Fatal(err)
	}
	child, err := g.Read(6101)
	if err != nil {
		t.Fatal(err)
	}
	if child.Level != p.Level || child.BaseLevel != p.Level || len(child.Features) != len(p.Features) || child.Generation != 1 || child.Relaxed {
		t.Fatalf("child during parent relax = %+v, want baseline %+v", child, p)
	}
}

func TestSelfConcurrentOperations(t *testing.T) {
	g := NewSpeculationMitigationGuard(GuardConfig{MaxRelax: time.Hour})
	if err := g.Apply(6200, selfPlan(t, g)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := g.Plan(selfHost(), Policy{Name: "tier", Required: []string{FeatIbpb}, MinLevel: i + 1})
			if err == nil {
				_ = g.Apply(6200, p)
			}
			_ = g.Inherit(6200, 6201)
			if i%4 == 0 {
				_ = g.Relax(6200, FeatIbpb, "concurrent")
				_, _ = g.Read(6200)
			}
			if i%8 == 0 {
				_, _ = g.Snapshot()
			}
		}(i)
	}
	wg.Wait()
	state, err := g.Read(6200)
	if err != nil {
		t.Fatal(err)
	}
	if state.Level != 32 {
		t.Fatalf("final level = %d, want 32", state.Level)
	}
}

func TestSelfCorruptRestoreLeavesState(t *testing.T) {
	g := NewSpeculationMitigationGuard(GuardConfig{MaxRelax: time.Hour})
	p := selfPlan(t, g)
	if err := g.Apply(6300, p); err != nil {
		t.Fatal(err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Restore(append(data[:len(data)-1], 'x')); !errors.Is(err, ErrBadSnapshot) {
		t.Fatalf("truncated payload error = %v", err)
	}
	state, err := g.Read(6300)
	if err != nil || state.Level != p.Level {
		t.Fatalf("state after failed restore = %+v, %v", state, err)
	}
	if err := g.Restore([]byte(`{"magic":"gsb-speculation-mitigation-guard","version":1,"processes":[{"pid":1,"level":10,"base_level":10,"features":["ibpb"],"base_features":["ibpb"],"generation":1,"relax_count":0}]}`)); !errors.Is(err, ErrBadSnapshot) {
		t.Fatalf("inconsistent snapshot error = %v", err)
	}
}
