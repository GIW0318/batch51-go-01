package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-safety-a-08"
)

func certView(subject, issuer string, oids ...string) impl.CertView {
	policies := make([]impl.PolicyOID, 0, len(oids))
	for _, oid := range oids {
		policies = append(policies, impl.PolicyOID{OID: oid})
	}
	return impl.CertView{
		Subject:          subject,
		Issuer:           issuer,
		Policies:         policies,
		RequireExplicit:  -1,
		InhibitMapping:   -1,
		InhibitAnyPolicy: -1,
	}
}

func evalConfig() impl.EvalConfig {
	return impl.EvalConfig{
		MaxNodes:           64,
		MaxMappingDepth:    4,
		MaxPoliciesPerCert: 8,
		CacheSize:          32,
	}
}

func TestEvaluateEmptyChainRejected(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	if _, err := e.Evaluate(nil, impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}}); !errors.Is(err, impl.ErrEmptyChain) {
		t.Fatalf("nil chain must fail with ErrEmptyChain, got %v", err)
	}
	if _, err := e.Evaluate([]impl.CertView{}, impl.PolicyRequest{}); !errors.Is(err, impl.ErrEmptyChain) {
		t.Fatalf("empty chain must fail with ErrEmptyChain, got %v", err)
	}
}

func TestEvaluateBrokenChainRejected(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	mid := certView("CN=Mid", "CN=Other", "1.2.3.1")
	if _, err := e.Evaluate([]impl.CertView{root, mid}, impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}}); !errors.Is(err, impl.ErrChainBroken) {
		t.Fatalf("an unlinked chain must fail with ErrChainBroken, got %v", err)
	}
}

func TestEvaluateSingleAnchorAcceptsInitial(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1", "1.2.3.2")
	res, err := e.Evaluate([]impl.CertView{root}, impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}})
	if err != nil {
		t.Fatalf("evaluate a single anchor: %v", err)
	}
	if !res.Valid || res.Reason != impl.ReasonOK {
		t.Fatalf("result = %+v, want a valid ok result", res)
	}
	if len(res.Policies) != 1 || res.Policies[0] != "1.2.3.1" {
		t.Fatalf("policies = %v, want only the requested 1.2.3.1", res.Policies)
	}
	if res.ChainFP == "" {
		t.Fatalf("result must carry the chain fingerprint")
	}
	if res.Steps < 1 {
		t.Fatalf("result steps = %d, want at least one visited certificate", res.Steps)
	}
	tr, err := e.Explain("1.2.3.1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !tr.Accepted || tr.OID != "1.2.3.1" || tr.ChainFP != res.ChainFP {
		t.Fatalf("trace = %+v, want an accepted trace for the same chain", tr)
	}
	if len(tr.Steps) == 0 || tr.Steps[0].Subject != "CN=Root" {
		t.Fatalf("trace steps = %+v, want the anchor visited first", tr.Steps)
	}
}

func TestEvaluateIntersectsAcrossPath(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1", "1.2.3.2")
	leaf := certView("CN=Leaf", "CN=Root", "1.2.3.2", "1.2.3.1", "1.2.3.1")
	res, err := e.Evaluate([]impl.CertView{root, leaf}, impl.PolicyRequest{})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !res.Valid || res.Reason != impl.ReasonOK {
		t.Fatalf("result = %+v, want valid", res)
	}
	want := []string{"1.2.3.1", "1.2.3.2"}
	if len(res.Policies) != len(want) {
		t.Fatalf("policies = %v, want the sorted deduplicated set %v", res.Policies, want)
	}
	for i, oid := range want {
		if res.Policies[i] != oid {
			t.Fatalf("policies = %v, want ascending order %v", res.Policies, want)
		}
	}
	if res.Steps != 2 {
		t.Fatalf("steps = %d, want both certificates visited", res.Steps)
	}
}

func TestEvaluateDisjointPathRejected(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	leaf := certView("CN=Leaf", "CN=Root", "1.2.3.9")
	res, err := e.Evaluate([]impl.CertView{root, leaf}, impl.PolicyRequest{})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Valid {
		t.Fatalf("a disjoint policy set must not be accepted, got %+v", res)
	}
	if res.Reason != impl.ReasonNoValidPolicy {
		t.Fatalf("reason = %q, want %q", res.Reason, impl.ReasonNoValidPolicy)
	}
	if len(res.Policies) != 0 {
		t.Fatalf("rejected path still reports policies %v", res.Policies)
	}
}

func TestEvaluateAppliesMapping(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	mid := certView("CN=Mid", "CN=Root", "1.2.3.9")
	mid.Mappings = []impl.PolicyMap{{Issuer: "1.2.3.1", Subject: "1.2.3.9"}}
	leaf := certView("CN=Leaf", "CN=Mid", "1.2.3.9")
	res, err := e.Evaluate([]impl.CertView{root, mid, leaf}, impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !res.Valid {
		t.Fatalf("a mapped policy present in the next certificate must be accepted, got %+v", res)
	}
	if len(res.Policies) != 1 || res.Policies[0] != "1.2.3.9" {
		t.Fatalf("policies = %v, want the mapped 1.2.3.9", res.Policies)
	}
}

func TestEvaluateExpandsAnyPolicy(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	mid := certView("CN=Mid", "CN=Root", impl.AnyPolicyOID)
	leaf := certView("CN=Leaf", "CN=Mid", "1.2.3.1")
	res, err := e.Evaluate([]impl.CertView{root, mid, leaf}, impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !res.Valid {
		t.Fatalf("anyPolicy in the middle certificate must keep the path valid, got %+v", res)
	}
	if len(res.Policies) != 1 || res.Policies[0] != "1.2.3.1" {
		t.Fatalf("policies = %v, want the expanded 1.2.3.1", res.Policies)
	}
}

func TestEvaluateInhibitAnyPolicySuppressesExpansion(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	mid := certView("CN=Mid", "CN=Root", impl.AnyPolicyOID)
	mid.InhibitAnyPolicy = 0
	leaf := certView("CN=Leaf", "CN=Mid", "1.2.3.1")
	res, err := e.Evaluate([]impl.CertView{root, mid, leaf}, impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Valid {
		t.Fatalf("an inhibited anyPolicy must not satisfy the path, got %+v", res)
	}
	if res.Reason != impl.ReasonNoValidPolicy {
		t.Fatalf("reason = %q, want %q", res.Reason, impl.ReasonNoValidPolicy)
	}
}

func TestEvaluateLimitsPoliciesPerCert(t *testing.T) {
	cfg := evalConfig()
	cfg.MaxPoliciesPerCert = 3
	e := impl.NewPolicyTreeEvaluator(cfg)
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	leaf := certView("CN=Leaf", "CN=Root", "1.2.3.1", "1.2.3.2", "1.2.3.3", "1.2.3.4")
	if _, err := e.Evaluate([]impl.CertView{root, leaf}, impl.PolicyRequest{}); !errors.Is(err, impl.ErrTooManyPolicies) {
		t.Fatalf("four policies with a cap of three must fail with ErrTooManyPolicies, got %v", err)
	}
}

func TestEvaluateLimitsMappingDepth(t *testing.T) {
	cfg := evalConfig()
	cfg.MaxMappingDepth = 2
	e := impl.NewPolicyTreeEvaluator(cfg)
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	one := certView("CN=One", "CN=Root", "1.2.3.2")
	one.Mappings = []impl.PolicyMap{{Issuer: "1.2.3.1", Subject: "1.2.3.2"}}
	two := certView("CN=Two", "CN=One", "1.2.3.3")
	two.Mappings = []impl.PolicyMap{{Issuer: "1.2.3.2", Subject: "1.2.3.3"}}
	three := certView("CN=Three", "CN=Two", "1.2.3.4")
	three.Mappings = []impl.PolicyMap{{Issuer: "1.2.3.3", Subject: "1.2.3.4"}}
	chain := []impl.CertView{root, one, two, three}
	if _, err := e.Evaluate(chain, impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}}); !errors.Is(err, impl.ErrMappingDepth) {
		t.Fatalf("three stacked mappings with a cap of two must fail with ErrMappingDepth, got %v", err)
	}
}

func TestEvaluateNodeBudgetRejectsInsteadOfTruncating(t *testing.T) {
	cfg := evalConfig()
	cfg.MaxNodes = 5
	e := impl.NewPolicyTreeEvaluator(cfg)
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	mid := certView("CN=Mid", "CN=Root", "1.2.3.1")
	leaf := certView("CN=Leaf", "CN=Mid", "1.2.3.1")
	if _, err := e.Evaluate([]impl.CertView{root, mid, leaf}, impl.PolicyRequest{}); !errors.Is(err, impl.ErrNodeBudget) {
		t.Fatalf("a three certificate path exceeds a five node budget and must fail with ErrNodeBudget, got %v", err)
	}
}

func TestValidPolicyAgreesAndUnknownOID(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	leaf := certView("CN=Leaf", "CN=Root", "1.2.3.1")
	chain := []impl.CertView{root, leaf}
	ok, err := e.ValidPolicy(chain, "1.2.3.1")
	if err != nil {
		t.Fatalf("valid policy: %v", err)
	}
	if !ok {
		t.Fatalf("1.2.3.1 travels through the path and must be valid")
	}
	ok, err = e.ValidPolicy(chain, "1.2.3.7")
	if err != nil {
		t.Fatalf("valid policy for an absent oid: %v", err)
	}
	if ok {
		t.Fatalf("1.2.3.7 never appears in the path and must not be valid")
	}
	if _, err := e.Explain("1.2.3.7"); !errors.Is(err, impl.ErrUnknownPolicy) {
		t.Fatalf("explaining an unevaluated policy must fail with ErrUnknownPolicy, got %v", err)
	}
}

func TestCachedResultsAreReadOnlyCopies(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	leaf := certView("CN=Leaf", "CN=Root", "1.2.3.1")
	chain := []impl.CertView{root, leaf}
	req := impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}}
	res, err := e.Evaluate(chain, req)
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if !e.Cached(chain, req) {
		t.Fatalf("the evaluated pair must be cached")
	}
	res.Policies[0] = "9.9.9"
	again, err := e.Evaluate(chain, req)
	if err != nil {
		t.Fatalf("second evaluation: %v", err)
	}
	if again.Policies[0] != "1.2.3.1" {
		t.Fatalf("cached result was mutated through the returned slice: %v", again.Policies)
	}
	if e.Cached(chain, impl.PolicyRequest{InitialPolicies: []string{"1.2.3.2"}}) {
		t.Fatalf("a different initial policy set must not hit the cache")
	}
	if err := e.Invalidate(); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if e.Cached(chain, req) {
		t.Fatalf("cache must be empty after invalidation")
	}
}

func TestSnapshotRestoreKeepsEvaluations(t *testing.T) {
	cfg := evalConfig()
	e := impl.NewPolicyTreeEvaluator(cfg)
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	leaf := certView("CN=Leaf", "CN=Root", "1.2.3.1")
	chain := []impl.CertView{root, leaf}
	req := impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}}
	res, err := e.Evaluate(chain, req)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	data, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewPolicyTreeEvaluator(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !restored.Cached(chain, req) {
		t.Fatalf("restored evaluator lost the cached evaluation")
	}
	again, err := restored.Evaluate(chain, req)
	if err != nil {
		t.Fatalf("evaluate after restore: %v", err)
	}
	if again.Steps != res.Steps || len(again.Policies) != len(res.Policies) || again.ChainFP != res.ChainFP {
		t.Fatalf("result after restore = %+v, want the same shape as %+v", again, res)
	}
	tr, err := restored.Explain("1.2.3.1")
	if err != nil || !tr.Accepted {
		t.Fatalf("explain after restore = %+v err=%v, want the restored trace", tr, err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	e := impl.NewPolicyTreeEvaluator(evalConfig())
	root := certView("CN=Root", "CN=Root", "1.2.3.1")
	leaf := certView("CN=Leaf", "CN=Root", "1.2.3.1")
	chain := []impl.CertView{root, leaf}
	if _, err := e.Evaluate(chain, impl.PolicyRequest{InitialPolicies: []string{"1.2.3.1"}}); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if err := e.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	tr, err := e.Explain("1.2.3.1")
	if err != nil {
		t.Fatalf("a rejected restore must keep the last evaluation explainable, got %v", err)
	}
	if !tr.Accepted {
		t.Fatalf("trace after a rejected restore = %+v, want the accepted trace", tr)
	}
}
