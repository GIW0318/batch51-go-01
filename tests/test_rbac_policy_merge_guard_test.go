package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-r-02"
)

func readPolicy() impl.Policy {
	return impl.Policy{
		ID:      "p-read",
		Tenant:  "t1",
		Version: 3,
		Roles: map[string]impl.RoleDef{
			"reader": {
				Rules: []impl.Rule{
					{Action: "read", Resource: "bucket/report", Effect: impl.EffectAllow},
				},
			},
		},
	}
}

func devPolicy() impl.Policy {
	return impl.Policy{
		ID:      "p-dev",
		Tenant:  "t1",
		Version: 1,
		Roles: map[string]impl.RoleDef{
			"dev": {
				Parents: []string{"reader"},
				Rules: []impl.Rule{
					{Action: "write", Resource: "queue/jobs", Effect: impl.EffectAllow},
				},
			},
		},
	}
}

func newGuard(t *testing.T) *impl.RbacPolicyMergeGuard {
	t.Helper()
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 8, MaxDepth: 6, MaxAudit: 512})
	if err := g.Load(readPolicy()); err != nil {
		t.Fatalf("baseline policy must load, got %v", err)
	}
	return g
}

func TestLoadRejectsBlankPolicyID(t *testing.T) {
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 4, MaxDepth: 4, MaxAudit: 16})
	bad := readPolicy()
	bad.ID = "  "
	if err := g.Load(bad); !errors.Is(err, impl.ErrBadPolicy) {
		t.Fatalf("policy without an id must be refused, got %v", err)
	}
	if got := len(g.Policies("t1")); got != 0 {
		t.Fatalf("refused policy must not occupy the table, holds %d", got)
	}
}

func TestLoadRejectsUnknownParentRole(t *testing.T) {
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 4, MaxDepth: 4, MaxAudit: 16})
	bad := impl.Policy{
		ID: "p-bad", Tenant: "t1", Version: 1,
		Roles: map[string]impl.RoleDef{
			"dev": {Parents: []string{"ghost"}},
		},
	}
	if err := g.Load(bad); !errors.Is(err, impl.ErrUnknownRole) {
		t.Fatalf("dangling parent must be refused with ErrUnknownRole, got %v", err)
	}
	if got := g.Roles("t1"); len(got) != 0 {
		t.Fatalf("refused policy must not publish roles, got %v", got)
	}
}

func TestLoadRejectsInheritanceCycle(t *testing.T) {
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 4, MaxDepth: 4, MaxAudit: 16})
	cyclic := impl.Policy{
		ID: "p-cycle", Tenant: "t1", Version: 1,
		Roles: map[string]impl.RoleDef{
			"alpha": {Parents: []string{"beta"}},
			"beta":  {Parents: []string{"alpha"}},
		},
	}
	if err := g.Load(cyclic); !errors.Is(err, impl.ErrInheritanceCycle) {
		t.Fatalf("two role cycle must be refused with ErrInheritanceCycle, got %v", err)
	}
}

func TestLoadRejectsCrossTenantParent(t *testing.T) {
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 4, MaxDepth: 4, MaxAudit: 16})
	crossed := impl.Policy{
		ID: "p-cross", Tenant: "t1", Version: 1,
		Roles: map[string]impl.RoleDef{
			"dev": {Parents: []string{"t2:admin"}},
		},
	}
	if err := g.Load(crossed); !errors.Is(err, impl.ErrTenantBoundary) {
		t.Fatalf("parent of another tenant must be refused with ErrTenantBoundary, got %v", err)
	}
	if got := len(g.Roles("t1")); got != 0 {
		t.Fatalf("refused policy must not leave roles behind, got %d", got)
	}
}

func TestLoadRejectsStaleVersion(t *testing.T) {
	g := newGuard(t)
	older := readPolicy()
	older.Version = 2
	if err := g.Load(older); !errors.Is(err, impl.ErrStaleVersion) {
		t.Fatalf("older version must be refused with ErrStaleVersion, got %v", err)
	}
	version, ok := g.Version("p-read")
	if !ok || version != 3 {
		t.Fatalf("loaded version must stay at 3, got %d ok=%v", version, ok)
	}
}

func TestMergeIsAllOrNothing(t *testing.T) {
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 8, MaxDepth: 6, MaxAudit: 64})
	broken := impl.Policy{
		ID: "p-broken", Tenant: "t1", Version: 1,
		Roles: map[string]impl.RoleDef{
			"dev": {Parents: []string{"missing-role"}},
		},
	}
	if err := g.Merge([]impl.Policy{readPolicy(), broken}); !errors.Is(err, impl.ErrUnknownRole) {
		t.Fatalf("batch with a dangling parent must be refused, got %v", err)
	}
	if got := g.Policies("t1"); len(got) != 0 {
		t.Fatalf("failed batch must install nothing, table holds %v", got)
	}
	if got := len(g.Roles("t1")); got != 0 {
		t.Fatalf("failed batch must publish no roles, got %d", got)
	}
}

func TestEvaluateAllowsMatchedRole(t *testing.T) {
	g := newGuard(t)
	if err := g.Load(devPolicy()); err != nil {
		t.Fatalf("dependent policy must load once its parent role exists, got %v", err)
	}
	d, err := g.Evaluate(impl.Request{Tenant: "t1", Role: "dev", Action: "read", Resource: "bucket/report"})
	if err != nil {
		t.Fatalf("inherited allow must produce a verdict, got error %v", err)
	}
	if !d.Allowed || d.Code != impl.CodeAllow {
		t.Fatalf("inherited allow must be granted, got %s", d.Code)
	}
	if len(d.Matched) != 1 {
		t.Fatalf("exactly one inherited rule must match, got %d", len(d.Matched))
	}
	if d.Matched[0].Policy != "p-read" || d.Matched[0].Role != "reader" {
		t.Fatalf("match must name the source policy and role, got %+v", d.Matched[0])
	}
	if d.Seq != 1 {
		t.Fatalf("first verdict must take sequence 1, got %d", d.Seq)
	}
}

func TestEvaluateUsesWildcardAction(t *testing.T) {
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 4, MaxDepth: 4, MaxAudit: 16})
	p := impl.Policy{
		ID: "p-ops", Tenant: "t1", Version: 1,
		Roles: map[string]impl.RoleDef{
			"ops": {Rules: []impl.Rule{{Action: "*", Resource: "bucket/q", Effect: impl.EffectAllow}}},
		},
	}
	if err := g.Load(p); err != nil {
		t.Fatalf("policy must load, got %v", err)
	}
	d, err := g.Evaluate(impl.Request{Tenant: "t1", Role: "ops", Action: "purge", Resource: "bucket/q"})
	if err != nil {
		t.Fatalf("wildcard action must produce a verdict, got %v", err)
	}
	if !d.Allowed {
		t.Fatalf("wildcard action must cover purge, got %s", d.Code)
	}
	if len(d.Matched) != 1 || d.Matched[0].Action != "*" {
		t.Fatalf("match must report the wildcard action, got %+v", d.Matched)
	}
}

func TestEvaluateUsesWildcardResource(t *testing.T) {
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 4, MaxDepth: 4, MaxAudit: 16})
	p := impl.Policy{
		ID: "p-bucket", Tenant: "t1", Version: 1,
		Roles: map[string]impl.RoleDef{
			"reader": {Rules: []impl.Rule{{Action: "read", Resource: "bucket/*", Effect: impl.EffectAllow}}},
		},
	}
	if err := g.Load(p); err != nil {
		t.Fatalf("policy must load, got %v", err)
	}
	inside, err := g.Evaluate(impl.Request{Tenant: "t1", Role: "reader", Action: "read", Resource: "bucket/archive"})
	if err != nil {
		t.Fatalf("wildcard resource must produce a verdict, got %v", err)
	}
	if !inside.Allowed {
		t.Fatalf("bucket/archive must fall under bucket/*, got %s", inside.Code)
	}
	outside, err := g.Evaluate(impl.Request{Tenant: "t1", Role: "reader", Action: "read", Resource: "queue/archive"})
	if err != nil {
		t.Fatalf("second verdict must be produced, got %v", err)
	}
	if outside.Allowed || outside.Code != impl.CodeNoMatch {
		t.Fatalf("queue/archive is outside the wildcard, got allowed=%v code=%s", outside.Allowed, outside.Code)
	}
}

func TestEvaluateDeniesWhenNoRuleMatches(t *testing.T) {
	g := newGuard(t)
	d, err := g.Evaluate(impl.Request{Tenant: "t1", Role: "reader", Action: "delete", Resource: "bucket/report"})
	if err != nil {
		t.Fatalf("unmatched request must produce a verdict, got %v", err)
	}
	if d.Allowed {
		t.Fatalf("request outside every rule must be refused")
	}
	if d.Code != impl.CodeNoMatch {
		t.Fatalf("expected %s, got %s", impl.CodeNoMatch, d.Code)
	}
	if len(d.Matched) != 0 {
		t.Fatalf("no rule matched, but %d sources were reported", len(d.Matched))
	}
}

func TestEvaluateAppliesDenyRule(t *testing.T) {
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 4, MaxDepth: 4, MaxAudit: 16})
	p := impl.Policy{
		ID: "p-audit", Tenant: "t1", Version: 1,
		Roles: map[string]impl.RoleDef{
			"auditor": {Rules: []impl.Rule{{Action: "write", Resource: "secret/*", Effect: impl.EffectDeny}}},
		},
	}
	if err := g.Load(p); err != nil {
		t.Fatalf("policy must load, got %v", err)
	}
	d, err := g.Evaluate(impl.Request{Tenant: "t1", Role: "auditor", Action: "write", Resource: "secret/keys"})
	if err != nil {
		t.Fatalf("deny rule must produce a verdict, got %v", err)
	}
	if d.Allowed {
		t.Fatalf("deny rule must refuse the request")
	}
	if d.Code != impl.CodeDenyRule {
		t.Fatalf("expected %s, got %s", impl.CodeDenyRule, d.Code)
	}
}

func TestEvaluateDeniesUnknownTenant(t *testing.T) {
	g := newGuard(t)
	d, err := g.Evaluate(impl.Request{Tenant: "t9", Role: "reader", Action: "read", Resource: "bucket/report"})
	if err != nil {
		t.Fatalf("unknown tenant must still produce a verdict, got %v", err)
	}
	if d.Allowed {
		t.Fatalf("request from a tenant without policies must be refused")
	}
	if d.Code != impl.CodeUnknownTenant {
		t.Fatalf("expected %s, got %s", impl.CodeUnknownTenant, d.Code)
	}
}

func TestExpandDedupesDiamond(t *testing.T) {
	g := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 4, MaxDepth: 6, MaxAudit: 16})
	p := impl.Policy{
		ID: "p-diamond", Tenant: "t1", Version: 1,
		Roles: map[string]impl.RoleDef{
			"base": {Rules: []impl.Rule{{Action: "read", Resource: "meta", Effect: impl.EffectAllow}}},
			"a":    {Parents: []string{"base"}},
			"b":    {Parents: []string{"base"}},
			"dev":  {Parents: []string{"a", "b"}},
		},
	}
	if err := g.Load(p); err != nil {
		t.Fatalf("diamond policy must load, got %v", err)
	}
	got, err := g.Expand("t1", "dev")
	if err != nil {
		t.Fatalf("expansion must succeed, got %v", err)
	}
	want := []string{"a", "b", "base", "dev"}
	if len(got) != len(want) {
		t.Fatalf("expansion must hold %d roles, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expansion must be sorted and deduplicated, want %v got %v", want, got)
		}
	}
}

func TestSnapshotRestoreKeepsPolicies(t *testing.T) {
	g := newGuard(t)
	if err := g.Load(devPolicy()); err != nil {
		t.Fatalf("dependent policy must load, got %v", err)
	}
	before, err := g.Evaluate(impl.Request{Tenant: "t1", Role: "dev", Action: "write", Resource: "queue/jobs"})
	if err != nil {
		t.Fatalf("verdict must be produced, got %v", err)
	}
	if !before.Allowed {
		t.Fatalf("dev must hold write on queue/jobs before the snapshot")
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must be produced, got %v", err)
	}
	other := impl.NewRbacPolicyMergeGuard(impl.MergeConfig{MaxPolicies: 8, MaxDepth: 6, MaxAudit: 512})
	if err := other.Restore(data); err != nil {
		t.Fatalf("snapshot must restore, got %v", err)
	}
	if got := other.Policies("t1"); len(got) != 2 || got[0] != "p-dev" || got[1] != "p-read" {
		t.Fatalf("policy ids must survive restore in order, got %v", got)
	}
	version, ok := other.Version("p-read")
	if !ok || version != 3 {
		t.Fatalf("version must survive restore, got %d ok=%v", version, ok)
	}
	after, err := other.Evaluate(impl.Request{Tenant: "t1", Role: "dev", Action: "read", Resource: "bucket/report"})
	if err != nil {
		t.Fatalf("verdict after restore must be produced, got %v", err)
	}
	if !after.Allowed || after.Seq != 2 {
		t.Fatalf("inherited allow must survive restore with sequence 2, got allowed=%v seq=%d", after.Allowed, after.Seq)
	}
	if err := other.Restore([]byte("not-json")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must be refused with ErrBadSnapshot, got %v", err)
	}
}

func TestConcurrentEvaluateWhileReloading(t *testing.T) {
	g := newGuard(t)
	const readers = 40
	const rounds = 5
	var wg sync.WaitGroup
	failures := make(chan string, readers*rounds)
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				d, err := g.Evaluate(impl.Request{Tenant: "t1", Role: "reader", Action: "read", Resource: "bucket/report"})
				if err != nil {
					failures <- "verdict returned error: " + err.Error()
					return
				}
				if !d.Allowed || d.Code != impl.CodeAllow {
					failures <- "reload swapped in a partial policy table"
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for v := int64(4); v <= 13; v++ {
			next := readPolicy()
			next.Version = v
			if err := g.Load(next); err != nil {
				failures <- "reload rejected a newer version: " + err.Error()
				return
			}
		}
	}()
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Fatalf("%s", msg)
	}
	if version, ok := g.Version("p-read"); !ok || version != 13 {
		t.Fatalf("last reloaded version must be 13, got %d ok=%v", version, ok)
	}
	audit := g.Audit()
	if len(audit) != readers*rounds {
		t.Fatalf("audit log must hold %d records, got %d", readers*rounds, len(audit))
	}
	seen := make(map[int64]bool, len(audit))
	for _, ev := range audit {
		if seen[ev.Seq] {
			t.Fatalf("audit sequence %d was handed out twice", ev.Seq)
		}
		seen[ev.Seq] = true
	}
}
