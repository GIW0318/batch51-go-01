package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-37"
)

type countingTransport struct {
	mu    sync.Mutex
	calls int
	limit int
	last  impl.Request
}

func (c *countingTransport) Do(req impl.Request) (impl.Response, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.last = req
	c.mu.Unlock()
	if c.limit > 0 && call > c.limit {
		return impl.Response{Status: 429, Length: 0, Headers: map[string]string{}}, nil
	}
	return impl.Response{Status: 200, Length: 42, Headers: map[string]string{}}, nil
}

func (c *countingTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type fixedClock struct {
	now int64
}

func (f *fixedClock) Now() int64 { return f.now }

func newProber(t *testing.T, cfg impl.ProberConfig, tr impl.Transport) *impl.ApiRateLimitBypassProber {
	t.Helper()
	p := impl.NewApiRateLimitBypassProber(cfg)
	if err := p.SetTransport(tr); err != nil {
		t.Fatalf("set transport must succeed, got %v", err)
	}
	if err := p.SetClock(&fixedClock{now: 1000}); err != nil {
		t.Fatalf("set clock must succeed, got %v", err)
	}
	return p
}

func loginEndpoint() impl.Endpoint {
	return impl.Endpoint{Name: "login", Method: "POST", Path: "/api/login",
		Headers: map[string]string{"Content-Type": "application/json"}}
}

func loginProfile() impl.LimitProfile {
	return impl.LimitProfile{Endpoint: "login", Path: "/api/Login", Limit: 3, Probes: 4, Blocked: true}
}

func TestBaselineCountsAllowedBeforeFirst429(t *testing.T) {
	tr := &countingTransport{limit: 3}
	p := newProber(t, impl.ProberConfig{BaselineBurst: 6, MaxRequests: 50, BackoffMs: 100}, tr)
	prof, err := p.Baseline(loginEndpoint())
	if err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	if prof.Limit != 3 {
		t.Fatalf("observed limit = %d, want 3", prof.Limit)
	}
	if prof.Probes != 4 {
		t.Fatalf("baseline probe count = %d, want 4 (three passes plus the rejection)", prof.Probes)
	}
	if !prof.Blocked {
		t.Fatalf("profile must record that throttling was observed")
	}
	if prof.Endpoint != "login" || prof.Path != "/api/login" {
		t.Fatalf("profile identity = %q/%q, want login//api/login", prof.Endpoint, prof.Path)
	}
	if got := tr.count(); got != 4 {
		t.Fatalf("transport calls = %d, want 4", got)
	}
	budget, err := p.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 4 || budget.Limit != 50 || budget.Remaining != 46 {
		t.Fatalf("budget = %+v, want used 4 limit 50 remaining 46", budget)
	}
}

func TestBaselineWithoutLimitReturnsNoObservation(t *testing.T) {
	tr := &countingTransport{}
	p := newProber(t, impl.ProberConfig{BaselineBurst: 5, MaxRequests: 50, BackoffMs: 100}, tr)
	prof, err := p.Baseline(loginEndpoint())
	if !errors.Is(err, impl.ErrNoLimitObserved) {
		t.Fatalf("baseline on an unlimited endpoint error = %v, want %v", err, impl.ErrNoLimitObserved)
	}
	if prof.Limit != 0 || prof.Blocked {
		t.Fatalf("profile after a clean burst = %+v, want the zero value", prof)
	}
	if got := tr.count(); got != 5 {
		t.Fatalf("transport calls = %d, want 5", got)
	}
}

func TestBaselineRejectsBadEndpoint(t *testing.T) {
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, &countingTransport{})
	if _, err := p.Baseline(impl.Endpoint{Path: "/api/login"}); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("baseline without a name error = %v, want %v", err, impl.ErrBadEndpoint)
	}
	if _, err := p.Baseline(impl.Endpoint{Name: "login"}); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("baseline without a path error = %v, want %v", err, impl.ErrBadEndpoint)
	}
	if _, err := p.Burst(loginEndpoint(), 0); !errors.Is(err, impl.ErrBadBurst) {
		t.Fatalf("burst with zero size error = %v, want %v", err, impl.ErrBadBurst)
	}
}

func TestBurstStopsAtFirst429(t *testing.T) {
	tr := &countingTransport{limit: 2}
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50, BackoffMs: 100}, tr)
	res, err := p.Burst(loginEndpoint(), 5)
	if err != nil {
		t.Fatalf("burst must succeed, got %v", err)
	}
	if res.Requests != 3 {
		t.Fatalf("burst requests = %d, want 3", res.Requests)
	}
	if res.Allowed != 2 {
		t.Fatalf("burst allowed = %d, want 2", res.Allowed)
	}
	if res.Blocked != 1 {
		t.Fatalf("burst blocked = %d, want 1", res.Blocked)
	}
	if res.LastStatus != 429 {
		t.Fatalf("burst last status = %d, want 429", res.LastStatus)
	}
	if got := tr.count(); got != 3 {
		t.Fatalf("transport calls = %d, want 3", got)
	}
}

func TestVariantsCoverSixDimensionsInFixedOrder(t *testing.T) {
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, &countingTransport{})
	vs, err := p.Variants(loginProfile())
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	want := []string{impl.DimSourceHeader, impl.DimIdentity, impl.DimPathCase,
		impl.DimTrailingSlash, impl.DimMethodOverride, impl.DimQueryOrder}
	if len(vs) != len(want) {
		t.Fatalf("variant count = %d, want %d", len(vs), len(want))
	}
	seen := map[string]bool{}
	for i, v := range vs {
		if v.Dimension != want[i] {
			t.Fatalf("variant %d dimension = %q, want %q", i, v.Dimension, want[i])
		}
		if v.ID == "" {
			t.Fatalf("variant %d has an empty id", i)
		}
		if seen[v.ID] {
			t.Fatalf("variant id %q is repeated", v.ID)
		}
		seen[v.ID] = true
		if v.Method == "" {
			t.Fatalf("variant %d has an empty method", i)
		}
	}
	again, err := p.Variants(loginProfile())
	if err != nil {
		t.Fatalf("second variants call must succeed, got %v", err)
	}
	for i := range again {
		if again[i].ID != vs[i].ID || again[i].Path != vs[i].Path || again[i].Dimension != vs[i].Dimension {
			t.Fatalf("variant %d is not stable across calls: %+v vs %+v", i, again[i], vs[i])
		}
	}
}

func TestVariantPathCaseChangesOnlyCase(t *testing.T) {
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, &countingTransport{})
	vs, err := p.Variants(loginProfile())
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	var caseVariant impl.BypassVariant
	var identityVariant impl.BypassVariant
	for _, v := range vs {
		switch v.Dimension {
		case impl.DimPathCase:
			caseVariant = v
		case impl.DimIdentity:
			identityVariant = v
		}
	}
	if caseVariant.Path != "/API/LOGIN" {
		t.Fatalf("path case variant = %q, want /API/LOGIN", caseVariant.Path)
	}
	if caseVariant.Method != "POST" {
		t.Fatalf("path case variant method = %q, want POST", caseVariant.Method)
	}
	if identityVariant.Path != "/api/Login" {
		t.Fatalf("identity variant path = %q, want the original /api/Login", identityVariant.Path)
	}
	if len(identityVariant.Headers) == 0 {
		t.Fatalf("identity variant must carry at least one header")
	}
}

func TestProbeSendsAtMostOncePerVariant(t *testing.T) {
	tr := &countingTransport{}
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, tr)
	vs, err := p.Variants(loginProfile())
	if err != nil || len(vs) == 0 {
		t.Fatalf("variants = %d err = %v, want at least one", len(vs), err)
	}
	obs, err := p.Probe(vs[0])
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	if obs.VariantID != vs[0].ID || obs.Dimension != vs[0].Dimension {
		t.Fatalf("observation identity = %q/%q, want %q/%q", obs.VariantID, obs.Dimension, vs[0].ID, vs[0].Dimension)
	}
	if obs.Status != 200 || !obs.Allowed {
		t.Fatalf("observation = %+v, want status 200 and allowed", obs)
	}
	if obs.At != 1000 {
		t.Fatalf("observation timestamp = %d, want the clock value 1000", obs.At)
	}
	if got := tr.count(); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
	if _, err := p.Probe(vs[0]); !errors.Is(err, impl.ErrVariantProbed) {
		t.Fatalf("second probe error = %v, want %v", err, impl.ErrVariantProbed)
	}
	if got := tr.count(); got != 1 {
		t.Fatalf("transport calls after the rejected probe = %d, want 1", got)
	}
	if got := len(p.Observations()); got != 1 {
		t.Fatalf("recorded observations = %d, want 1", got)
	}
}

func TestProbeEnforcesRequestBudget(t *testing.T) {
	tr := &countingTransport{}
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 2, BackoffMs: 100}, tr)
	vs, err := p.Variants(loginProfile())
	if err != nil || len(vs) < 3 {
		t.Fatalf("variants = %d err = %v, want at least three", len(vs), err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.Probe(vs[i]); err != nil {
			t.Fatalf("probe %d must succeed, got %v", i, err)
		}
	}
	if _, err := p.Probe(vs[2]); !errors.Is(err, impl.ErrBudgetExhausted) {
		t.Fatalf("over budget probe error = %v, want %v", err, impl.ErrBudgetExhausted)
	}
	if got := tr.count(); got != 2 {
		t.Fatalf("transport calls = %d, want 2", got)
	}
	budget, err := p.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 2 || budget.Remaining != 0 {
		t.Fatalf("budget = %+v, want used 2 remaining 0", budget)
	}
}

func TestProbeRejectsUnknownVariant(t *testing.T) {
	tr := &countingTransport{}
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, tr)
	if _, err := p.Probe(impl.BypassVariant{ID: "ghost", Dimension: impl.DimIdentity, Path: "/x"}); !errors.Is(err, impl.ErrUnknownVariant) {
		t.Fatalf("probe of a foreign variant error = %v, want %v", err, impl.ErrUnknownVariant)
	}
	if got := tr.count(); got != 0 {
		t.Fatalf("transport calls = %d, want 0", got)
	}
}

func TestJudgeClassifiesIndependentBucket(t *testing.T) {
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, &countingTransport{})
	obs := []impl.Observation{
		{VariantID: "login/identity", Dimension: impl.DimIdentity, Status: 200, Allowed: true},
		{VariantID: "login/source_header", Dimension: impl.DimSourceHeader, Status: 429},
		{VariantID: "login/path_case", Dimension: impl.DimPathCase, Status: 429},
	}
	verdict, err := p.Judge(obs)
	if err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	if verdict.Structure != impl.StructureIndependent {
		t.Fatalf("structure = %q, want %q", verdict.Structure, impl.StructureIndependent)
	}
	if len(verdict.Bypassed) != 1 || verdict.Bypassed[0] != "login/identity" {
		t.Fatalf("bypassed = %v, want exactly login/identity", verdict.Bypassed)
	}
	if len(verdict.Blocked) != 2 {
		t.Fatalf("blocked = %v, want the two rejected variants", verdict.Blocked)
	}
	if verdict.Reason == "" {
		t.Fatalf("verdict reason must not be empty")
	}
}

func TestJudgeClassifiesGlobalLimit(t *testing.T) {
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, &countingTransport{})
	obs := []impl.Observation{
		{VariantID: "login/identity", Dimension: impl.DimIdentity, Status: 429},
		{VariantID: "login/source_header", Dimension: impl.DimSourceHeader, Status: 429},
		{VariantID: "login/method_override", Dimension: impl.DimMethodOverride, Status: 429},
	}
	verdict, err := p.Judge(obs)
	if err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	if verdict.Structure != impl.StructureGlobal {
		t.Fatalf("structure = %q, want %q", verdict.Structure, impl.StructureGlobal)
	}
	if len(verdict.Bypassed) != 0 {
		t.Fatalf("bypassed = %v, want none", verdict.Bypassed)
	}
	if len(verdict.Blocked) != 3 {
		t.Fatalf("blocked count = %d, want 3", len(verdict.Blocked))
	}
}

func TestJudgeClassifiesSharedBucket(t *testing.T) {
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, &countingTransport{})
	obs := []impl.Observation{
		{VariantID: "login/path_case", Dimension: impl.DimPathCase, Status: 200, Allowed: true},
		{VariantID: "login/identity", Dimension: impl.DimIdentity, Status: 429},
		{VariantID: "login/source_header", Dimension: impl.DimSourceHeader, Status: 429},
	}
	verdict, err := p.Judge(obs)
	if err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	if verdict.Structure != impl.StructureShared {
		t.Fatalf("structure = %q, want %q", verdict.Structure, impl.StructureShared)
	}
	if len(verdict.Bypassed) != 1 || verdict.Bypassed[0] != "login/path_case" {
		t.Fatalf("bypassed = %v, want exactly login/path_case", verdict.Bypassed)
	}
}

func TestJudgeRejectsEmptyObservations(t *testing.T) {
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, &countingTransport{})
	if _, err := p.Judge(nil); !errors.Is(err, impl.ErrNoObservations) {
		t.Fatalf("judge on empty observations error = %v, want %v", err, impl.ErrNoObservations)
	}
}

func TestConcurrentProbeSendsOneRequest(t *testing.T) {
	tr := &countingTransport{}
	p := newProber(t, impl.ProberConfig{BaselineBurst: 4, MaxRequests: 50}, tr)
	vs, err := p.Variants(loginProfile())
	if err != nil || len(vs) == 0 {
		t.Fatalf("variants = %d err = %v, want at least one", len(vs), err)
	}
	const workers = 8
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, probeErr := p.Probe(vs[0])
			errs[idx] = probeErr
		}(i)
	}
	wg.Wait()
	winners := 0
	for i := 0; i < workers; i++ {
		if errs[i] == nil {
			winners++
			continue
		}
		if !errors.Is(errs[i], impl.ErrVariantProbed) {
			t.Fatalf("losing probe error = %v, want %v", errs[i], impl.ErrVariantProbed)
		}
	}
	if winners != 1 {
		t.Fatalf("successful probes = %d, want exactly 1", winners)
	}
	if got := tr.count(); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}

func TestSnapshotRestoreKeepsProbedSet(t *testing.T) {
	cfg := impl.ProberConfig{BaselineBurst: 4, MaxRequests: 20, BackoffMs: 100}
	tr := &countingTransport{}
	p := newProber(t, cfg, tr)
	vs, err := p.Variants(loginProfile())
	if err != nil || len(vs) < 2 {
		t.Fatalf("variants = %d err = %v, want at least two", len(vs), err)
	}
	if _, err := p.Probe(vs[0]); err != nil {
		t.Fatalf("first probe must succeed, got %v", err)
	}
	if _, err := p.Probe(vs[1]); err != nil {
		t.Fatalf("second probe must succeed, got %v", err)
	}
	snap, err := p.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot len = %d err = %v, want a payload", len(snap), err)
	}
	restored := impl.NewApiRateLimitBypassProber(cfg)
	if err := restored.SetTransport(&countingTransport{}); err != nil {
		t.Fatalf("set transport must succeed, got %v", err)
	}
	if err := restored.SetClock(&fixedClock{now: 2000}); err != nil {
		t.Fatalf("set clock must succeed, got %v", err)
	}
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if got := len(restored.Observations()); got != 2 {
		t.Fatalf("restored observations = %d, want 2", got)
	}
	budget, err := restored.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 2 {
		t.Fatalf("restored budget used = %d, want 2", budget.Used)
	}
	if _, err := restored.Probe(vs[0]); !errors.Is(err, impl.ErrVariantProbed) {
		t.Fatalf("replayed probe after restore error = %v, want %v", err, impl.ErrVariantProbed)
	}
	if err := restored.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want %v", err, impl.ErrBadSnapshot)
	}
	if got := len(restored.Observations()); got != 2 {
		t.Fatalf("observations after corrupt restore = %d, want 2", got)
	}
}
