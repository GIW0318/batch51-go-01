package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-41"
)

type funcTransport struct {
	mu    sync.Mutex
	calls int
	fn    func(impl.VersionSurface, impl.Request) (impl.Response, error)
}

func (f *funcTransport) Do(surface impl.VersionSurface, req impl.Request) (impl.Response, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.fn == nil {
		return impl.Response{Status: 200}, nil
	}
	return f.fn(surface, req)
}

func (f *funcTransport) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newProber(tr impl.Transport, maxProbes, breaker int) *impl.ApiVersionDowngradeProber {
	return impl.NewApiVersionDowngradeProber(tr, impl.Limits{MaxProbes: maxProbes, MaxConsecutiveServerErrors: breaker})
}

func testSchema() impl.Schema {
	return impl.Schema{
		Versions:       []string{"v1", "v2"},
		Resources:      []string{"orders", "users"},
		DefaultVersion: "v2",
		HeaderName:     "X-Api-Version",
		QueryName:      "api-version",
	}
}

func surfaceOf(version, carrier string) impl.VersionSurface {
	return impl.VersionSurface{
		ID:          "orders|" + version + "|" + carrier,
		Resource:    "orders",
		Version:     version,
		Carrier:     carrier,
		Path:        "https://api.example.test/" + version + "/orders",
		HeaderName:  "X-Api-Version",
		HeaderValue: version,
		QueryName:   "api-version",
		QueryValue:  version,
		Default:     version == "v2",
	}
}

func observationOf(surface impl.VersionSurface, class, errorCode string, fields ...string) impl.Observation {
	return impl.Observation{
		SurfaceID: surface.ID,
		Resource:  surface.Resource,
		Version:   surface.Version,
		Carrier:   surface.Carrier,
		Status:    200,
		Class:     class,
		Fields:    fields,
		ErrorCode: errorCode,
		Default:   surface.Default,
	}
}

func TestDiscoverExpandsEveryCarrier(t *testing.T) {
	p := newProber(&funcTransport{}, 32, 3)
	surfaces, err := p.Discover("https://api.example.test", testSchema())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(surfaces) != 12 {
		t.Fatalf("surfaces = %d, want 2 resources x 2 versions x 3 carriers = 12", len(surfaces))
	}
	seen := make(map[string]bool, len(surfaces))
	previous := ""
	for _, s := range surfaces {
		if s.ID == "" {
			t.Fatalf("surface without an identifier: %+v", s)
		}
		if seen[s.ID] {
			t.Fatalf("duplicate surface identifier %q", s.ID)
		}
		seen[s.ID] = true
		if previous != "" && s.ID < previous {
			t.Fatalf("surfaces are not ordered by identifier: %q after %q", s.ID, previous)
		}
		previous = s.ID
	}
	path := "orders|v1|path"
	if !seen[path] {
		t.Fatalf("missing path surface %q, got %v", path, seen)
	}
	for _, s := range surfaces {
		switch {
		case s.Resource == "orders" && s.Version == "v1" && s.Carrier == impl.CarrierPath:
			if s.Path != "https://api.example.test/v1/orders" {
				t.Fatalf("path carrier = %q", s.Path)
			}
			if s.Default {
				t.Fatalf("v1 must not be the default surface")
			}
		case s.Resource == "orders" && s.Version == "v1" && s.Carrier == impl.CarrierHeader:
			if s.HeaderName != "X-Api-Version" || s.HeaderValue != "v1" {
				t.Fatalf("header carrier = %q:%q", s.HeaderName, s.HeaderValue)
			}
		case s.Resource == "users" && s.Version == "v2" && s.Carrier == impl.CarrierQuery:
			if s.QueryName != "api-version" || s.QueryValue != "v2" {
				t.Fatalf("query carrier = %q:%q", s.QueryName, s.QueryValue)
			}
			if !s.Default {
				t.Fatalf("the declared default version must be flagged on every carrier")
			}
		}
	}
	listed := p.Surfaces()
	if len(listed) != 12 || listed[0].ID != surfaces[0].ID {
		t.Fatalf("Surfaces() = %d entries, want the discovered 12 in the same order", len(listed))
	}
}

func TestDiscoverRejectsIncompleteSchema(t *testing.T) {
	p := newProber(&funcTransport{}, 32, 3)
	if _, err := p.Discover("", testSchema()); !errors.Is(err, impl.ErrEmptyBase) {
		t.Fatalf("empty base must fail with ErrEmptyBase, got %v", err)
	}
	if _, err := p.Discover("   ", testSchema()); !errors.Is(err, impl.ErrEmptyBase) {
		t.Fatalf("blank base must fail with ErrEmptyBase, got %v", err)
	}
	if got := len(p.Surfaces()); got != 0 {
		t.Fatalf("a rejected discovery must not register surfaces, got %d", got)
	}
	noResources := testSchema()
	noResources.Resources = nil
	if _, err := p.Discover("https://api.example.test", noResources); !errors.Is(err, impl.ErrNoResources) {
		t.Fatalf("schema without resources must fail with ErrNoResources, got %v", err)
	}
	noVersions := testSchema()
	noVersions.Versions = nil
	if _, err := p.Discover("https://api.example.test", noVersions); !errors.Is(err, impl.ErrNoVersions) {
		t.Fatalf("schema without versions must fail with ErrNoVersions, got %v", err)
	}
	strayDefault := testSchema()
	strayDefault.DefaultVersion = "v9"
	if _, err := p.Discover("https://api.example.test", strayDefault); !errors.Is(err, impl.ErrBadSchema) {
		t.Fatalf("an undeclared default version must fail with ErrBadSchema, got %v", err)
	}
}

func TestProbeBucketsStatusCodes(t *testing.T) {
	codes := []struct {
		version string
		status  int
		want    string
	}{
		{"v1", 401, impl.StatusAuthGate},
		{"v2", 200, impl.StatusOK},
		{"v3", 403, impl.StatusAuthGate},
		{"v4", 404, impl.StatusNotFound},
		{"v5", 500, impl.StatusServer},
	}
	byVersion := make(map[string]int, len(codes))
	for _, c := range codes {
		byVersion[c.version] = c.status
	}
	tr := &funcTransport{fn: func(s impl.VersionSurface, r impl.Request) (impl.Response, error) {
		return impl.Response{Status: byVersion[s.Version], Fields: []string{"id"}, ErrorCode: "err_" + s.Version}, nil
	}}
	p := newProber(tr, 16, 6)
	for _, c := range codes {
		obs, err := p.Probe(surfaceOf(c.version, impl.CarrierPath), impl.Request{Method: "GET", Auth: true})
		if err != nil {
			t.Fatalf("probe %s: %v", c.version, err)
		}
		if obs.Class != c.want {
			t.Fatalf("status %d bucketed as %q, want %q", c.status, obs.Class, c.want)
		}
		if obs.Status != c.status {
			t.Fatalf("observation status = %d, want the raw %d", obs.Status, c.status)
		}
		if obs.Cached {
			t.Fatalf("first probe of %s must not be marked cached", c.version)
		}
		if obs.ErrorCode != "err_"+c.version {
			t.Fatalf("error code = %q, want it preserved verbatim", obs.ErrorCode)
		}
	}
}

func TestProbeSortsAndDedupesFields(t *testing.T) {
	tr := &funcTransport{fn: func(s impl.VersionSurface, r impl.Request) (impl.Response, error) {
		return impl.Response{Status: 200, Fields: []string{"total", "id", "total", "Amount", "id"}, ErrorCode: ""}, nil
	}}
	p := newProber(tr, 8, 3)
	obs, err := p.Probe(surfaceOf("v1", impl.CarrierPath), impl.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	want := []string{"Amount", "id", "total"}
	if len(obs.Fields) != len(want) {
		t.Fatalf("fields = %v, want the deduplicated %v", obs.Fields, want)
	}
	for i, name := range want {
		if obs.Fields[i] != name {
			t.Fatalf("fields = %v, want the sorted %v", obs.Fields, want)
		}
	}
	if obs.ErrorCode != "" {
		t.Fatalf("an empty error code must stay empty, got %q", obs.ErrorCode)
	}
}

func TestProbeMemoisesPerSurface(t *testing.T) {
	tr := &funcTransport{fn: func(s impl.VersionSurface, r impl.Request) (impl.Response, error) {
		return impl.Response{Status: 200, Fields: []string{"id"}}, nil
	}}
	p := newProber(tr, 8, 3)
	surface := surfaceOf("v1", impl.CarrierPath)
	first, err := p.Probe(surface, impl.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("first probe: %v", err)
	}
	second, err := p.Probe(surface, impl.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if tr.callCount() != 1 {
		t.Fatalf("transport called %d times, want exactly 1 effective probe", tr.callCount())
	}
	if first.Cached {
		t.Fatalf("the first observation must not be cached")
	}
	if !second.Cached {
		t.Fatalf("a repeated probe must be replayed as cached")
	}
	if second.Status != first.Status || second.Class != first.Class {
		t.Fatalf("replayed observation %+v differs from %+v", second, first)
	}
	state, err := p.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if state.Probes != 1 || state.MaxProbes != 8 || state.Remaining != 7 {
		t.Fatalf("budget = %+v, want 1 used of 8", state)
	}
	if obs, ok := p.Observation(surface.ID, surface.Resource); !ok || obs.Status != 200 {
		t.Fatalf("ledger lookup = %+v ok=%v, want the recorded observation", obs, ok)
	}
}

func TestProbeStopsAtBudget(t *testing.T) {
	tr := &funcTransport{fn: func(s impl.VersionSurface, r impl.Request) (impl.Response, error) {
		return impl.Response{Status: 200}, nil
	}}
	p := newProber(tr, 2, 3)
	for _, version := range []string{"v1", "v2"} {
		if _, err := p.Probe(surfaceOf(version, impl.CarrierPath), impl.Request{Method: "GET"}); err != nil {
			t.Fatalf("probe %s within budget: %v", version, err)
		}
	}
	if _, err := p.Probe(surfaceOf("v3", impl.CarrierPath), impl.Request{Method: "GET"}); !errors.Is(err, impl.ErrProbeBudget) {
		t.Fatalf("third effective probe must fail with ErrProbeBudget, got %v", err)
	}
	state, err := p.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if state.Probes != 2 || state.Remaining != 0 {
		t.Fatalf("budget after exhaustion = %+v, want 2 used and 0 left", state)
	}
	if tr.callCount() != 2 {
		t.Fatalf("transport called %d times, want 2", tr.callCount())
	}
	if _, err := p.Probe(surfaceOf("v1", impl.CarrierPath), impl.Request{Method: "GET"}); err != nil {
		t.Fatalf("replaying a probed surface must still work after the budget is gone, got %v", err)
	}
}

func TestProbeOpensBreakerOnServerErrors(t *testing.T) {
	tr := &funcTransport{fn: func(s impl.VersionSurface, r impl.Request) (impl.Response, error) {
		return impl.Response{Status: 503}, nil
	}}
	p := newProber(tr, 16, 2)
	for _, version := range []string{"v1", "v2"} {
		if _, err := p.Probe(surfaceOf(version, impl.CarrierPath), impl.Request{Method: "GET"}); err != nil {
			t.Fatalf("probe %s: %v", version, err)
		}
	}
	if _, err := p.Probe(surfaceOf("v3", impl.CarrierPath), impl.Request{Method: "GET"}); !errors.Is(err, impl.ErrCircuitOpen) {
		t.Fatalf("third consecutive server error must open the breaker, got %v", err)
	}
	state, err := p.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if !state.CircuitOpen {
		t.Fatalf("budget must report the open breaker, got %+v", state)
	}
	if tr.callCount() != 2 {
		t.Fatalf("an open breaker must not reach the transport, calls = %d", tr.callCount())
	}
}

func TestProbeRateLimitDoesNotConsumeBudget(t *testing.T) {
	tr := &funcTransport{fn: func(s impl.VersionSurface, r impl.Request) (impl.Response, error) {
		return impl.Response{Status: 200}, nil
	}}
	p := newProber(tr, 4, 3)
	p.SetRateLimited(true)
	if _, err := p.Probe(surfaceOf("v1", impl.CarrierPath), impl.Request{Method: "GET"}); !errors.Is(err, impl.ErrRateLimited) {
		t.Fatalf("a rate limited probe must fail with ErrRateLimited, got %v", err)
	}
	state, err := p.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if state.Probes != 0 || !state.RateLimited {
		t.Fatalf("rate limited state = %+v, want 0 probes and the flag set", state)
	}
	if tr.callCount() != 0 {
		t.Fatalf("a rate limited probe reached the transport %d times", tr.callCount())
	}
	p.SetRateLimited(false)
	if _, err := p.Probe(surfaceOf("v1", impl.CarrierPath), impl.Request{Method: "GET"}); err != nil {
		t.Fatalf("probe after clearing the rate limit: %v", err)
	}
}

func TestProbeConcurrentSameSurfaceSingleExchange(t *testing.T) {
	tr := &funcTransport{fn: func(s impl.VersionSurface, r impl.Request) (impl.Response, error) {
		return impl.Response{Status: 200, Fields: []string{"id"}}, nil
	}}
	p := newProber(tr, 8, 3)
	surface := surfaceOf("v1", impl.CarrierPath)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	classes := make([]string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			obs, err := p.Probe(surface, impl.Request{Method: "GET"})
			errs[i] = err
			classes[i] = obs.Class
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent probe %d: %v", i, err)
		}
		if classes[i] != impl.StatusOK {
			t.Fatalf("concurrent probe %d classified %q, want %q", i, classes[i], impl.StatusOK)
		}
	}
	if tr.callCount() != 1 {
		t.Fatalf("transport called %d times, want one effective exchange per surface", tr.callCount())
	}
	state, err := p.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if state.Probes != 1 {
		t.Fatalf("budget consumed %d probes for one surface, want 1", state.Probes)
	}
}

func TestAlignGroupsByResourceAndSorts(t *testing.T) {
	base := observationOf(impl.VersionSurface{ID: "orders|v2|path", Resource: "orders", Version: "v2", Carrier: impl.CarrierPath, Default: true}, impl.StatusOK, "E_OK", "id", "status", "total")
	v1 := observationOf(impl.VersionSurface{ID: "orders|v1|path", Resource: "orders", Version: "v1", Carrier: impl.CarrierPath}, impl.StatusOK, "E_OK", "id", "total")
	v3 := observationOf(impl.VersionSurface{ID: "orders|v3|path", Resource: "orders", Version: "v3", Carrier: impl.CarrierPath}, impl.StatusOK, "E_OK", "debug", "id", "status", "total")
	usersBase := observationOf(impl.VersionSurface{ID: "users|v2|path", Resource: "users", Version: "v2", Carrier: impl.CarrierPath, Default: true}, impl.StatusOK, "E_OK", "email", "id")
	usersOld := observationOf(impl.VersionSurface{ID: "users|v1|path", Resource: "users", Version: "v1", Carrier: impl.CarrierPath}, impl.StatusOK, "E_OK", "id")
	p := newProber(&funcTransport{}, 8, 3)
	diffs, err := p.Align([]impl.Observation{usersOld, v3, base, usersBase, v1})
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	if len(diffs) != 3 {
		t.Fatalf("diffs = %d, want one per candidate surface", len(diffs))
	}
	wantOrder := []string{"orders|v1|path", "orders|v3|path", "users|v1|path"}
	for i, want := range wantOrder {
		if diffs[i].Candidate != want {
			t.Fatalf("diff %d candidate = %q, want the sorted order %v", i, diffs[i].Candidate, wantOrder)
		}
	}
	if diffs[0].Baseline != "orders|v2|path" {
		t.Fatalf("baseline = %q, want the default surface of the resource", diffs[0].Baseline)
	}
	if len(diffs[0].MissingFields) != 1 || diffs[0].MissingFields[0] != "status" {
		t.Fatalf("v1 missing fields = %v, want [status]", diffs[0].MissingFields)
	}
	if len(diffs[0].AddedFields) != 0 {
		t.Fatalf("v1 added fields = %v, want none", diffs[0].AddedFields)
	}
	if len(diffs[1].AddedFields) != 1 || diffs[1].AddedFields[0] != "debug" {
		t.Fatalf("v3 added fields = %v, want [debug]", diffs[1].AddedFields)
	}
	if diffs[2].Baseline != "users|v2|path" || len(diffs[2].MissingFields) != 1 || diffs[2].MissingFields[0] != "email" {
		t.Fatalf("users diff = %+v, want email missing against users|v2|path", diffs[2])
	}
}

func TestAlignRequiresBaselineAndObservations(t *testing.T) {
	p := newProber(&funcTransport{}, 8, 3)
	if _, err := p.Align(nil); !errors.Is(err, impl.ErrNoObservations) {
		t.Fatalf("aligning nothing must fail with ErrNoObservations, got %v", err)
	}
	orphan := observationOf(impl.VersionSurface{ID: "orders|v1|path", Resource: "orders", Version: "v1", Carrier: impl.CarrierPath}, impl.StatusOK, "E_OK", "id")
	if _, err := p.Align([]impl.Observation{orphan}); !errors.Is(err, impl.ErrNoBaseline) {
		t.Fatalf("a resource without a default observation must fail with ErrNoBaseline, got %v", err)
	}
}

func TestAlignFlagsPatternMismatch(t *testing.T) {
	base := observationOf(impl.VersionSurface{ID: "orders|v2|path", Resource: "orders", Version: "v2", Carrier: impl.CarrierPath, Default: true}, impl.StatusOK, "E_OK", "id")
	same := observationOf(impl.VersionSurface{ID: "orders|v1|path", Resource: "orders", Version: "v1", Carrier: impl.CarrierPath}, impl.StatusOK, "E_OK", "id")
	other := observationOf(impl.VersionSurface{ID: "orders|v3|path", Resource: "orders", Version: "v3", Carrier: impl.CarrierPath}, impl.StatusOK, "E_LEGACY", "id")
	p := newProber(&funcTransport{}, 8, 3)
	diffs, err := p.Align([]impl.Observation{other, base, same})
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("diffs = %d, want 2", len(diffs))
	}
	if diffs[0].Candidate != "orders|v1|path" || diffs[0].PatternMismatch {
		t.Fatalf("v1 diff = %+v, want the same error pattern with no mismatch", diffs[0])
	}
	if diffs[1].Candidate != "orders|v3|path" || !diffs[1].PatternMismatch {
		t.Fatalf("v3 diff = %+v, want a pattern mismatch", diffs[1])
	}
}

func TestJudgeAuthGapIsDowngrade(t *testing.T) {
	p := newProber(&funcTransport{}, 8, 3)
	verdict, err := p.Judge(impl.VersionDiff{
		Resource:       "orders",
		Baseline:       "orders|v2|path",
		Candidate:      "orders|v1|path",
		BaselineClass:  impl.StatusAuthGate,
		CandidateClass: impl.StatusOK,
	})
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if verdict.Verdict != impl.VerdictDowngraded || verdict.Reason != impl.ReasonAuthGap {
		t.Fatalf("verdict = %q/%q, want downgraded/auth gap", verdict.Verdict, verdict.Reason)
	}
	if !verdict.AuthGap || !verdict.Conclusive {
		t.Fatalf("auth gap verdict must be conclusive with the gap flag, got %+v", verdict)
	}
	parity, err := p.Judge(impl.VersionDiff{
		Resource:       "orders",
		Baseline:       "orders|v2|path",
		Candidate:      "orders|v3|path",
		BaselineClass:  impl.StatusAuthGate,
		CandidateClass: impl.StatusAuthGate,
	})
	if err != nil {
		t.Fatalf("judge parity: %v", err)
	}
	if parity.Verdict != impl.VerdictConsistent || parity.Reason != impl.ReasonAuthParity {
		t.Fatalf("parity verdict = %q/%q, want consistent/auth parity", parity.Verdict, parity.Reason)
	}
	if parity.AuthGap {
		t.Fatalf("auth parity must not be reported as a gap")
	}
	if _, err := p.Judge(impl.VersionDiff{Resource: "orders"}); !errors.Is(err, impl.ErrBadDiff) {
		t.Fatalf("a diff without a candidate must fail with ErrBadDiff, got %v", err)
	}
}

func TestJudgeSerialisesUndeterminedCases(t *testing.T) {
	p := newProber(&funcTransport{}, 8, 3)
	server, err := p.Judge(impl.VersionDiff{
		Resource:       "orders",
		Baseline:       "orders|v2|path",
		Candidate:      "orders|v1|path",
		BaselineClass:  impl.StatusOK,
		CandidateClass: impl.StatusServer,
	})
	if err != nil {
		t.Fatalf("judge server error: %v", err)
	}
	if server.Verdict != impl.VerdictUndetermined || server.Reason != impl.ReasonServerError || server.Conclusive {
		t.Fatalf("server error verdict = %+v, want undetermined/server error and not conclusive", server)
	}
	cut, err := p.Judge(impl.VersionDiff{
		Resource:        "orders",
		Baseline:        "orders|v2|path",
		Candidate:       "orders|v4|path",
		MissingFields:   []string{"status"},
		BaselineClass:   impl.StatusOK,
		CandidateClass:  impl.StatusOK,
		PatternMismatch: true,
	})
	if err != nil {
		t.Fatalf("judge field cut: %v", err)
	}
	if cut.Verdict != impl.VerdictUndetermined || cut.Reason != impl.ReasonFieldCut || cut.Conclusive {
		t.Fatalf("field cut verdict = %+v, want undetermined/field cut and not conclusive", cut)
	}
	stable, err := p.Judge(impl.VersionDiff{
		Resource:       "orders",
		Baseline:       "orders|v2|path",
		Candidate:      "orders|v5|path",
		BaselineClass:  impl.StatusOK,
		CandidateClass: impl.StatusOK,
	})
	if err != nil {
		t.Fatalf("judge stable: %v", err)
	}
	if stable.Verdict != impl.VerdictConsistent || stable.Reason != impl.ReasonStable {
		t.Fatalf("stable verdict = %+v, want consistent/stable", stable)
	}
}

func TestSnapshotRestoreKeepsBudgetAndLedger(t *testing.T) {
	tr := &funcTransport{fn: func(s impl.VersionSurface, r impl.Request) (impl.Response, error) {
		return impl.Response{Status: 200, Fields: []string{"id", "total"}}, nil
	}}
	limits := impl.Limits{MaxProbes: 6, MaxConsecutiveServerErrors: 3}
	p := impl.NewApiVersionDowngradeProber(tr, limits)
	if _, err := p.Discover("https://api.example.test", testSchema()); err != nil {
		t.Fatalf("discover: %v", err)
	}
	surface := surfaceOf("v1", impl.CarrierPath)
	if _, err := p.Probe(surface, impl.Request{Method: "GET"}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewApiVersionDowngradeProber(tr, limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Surfaces()); got != 12 {
		t.Fatalf("restored surfaces = %d, want the discovered 12", got)
	}
	state, err := restored.Budget()
	if err != nil {
		t.Fatalf("budget after restore: %v", err)
	}
	if state.Probes != 1 || state.Remaining != 5 {
		t.Fatalf("restored budget = %+v, want the 1 used of 6 preserved", state)
	}
	obs, err := restored.Probe(surface, impl.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("probe after restore: %v", err)
	}
	if !obs.Cached {
		t.Fatalf("a surface probed before the snapshot must replay as cached after restore")
	}
	if tr.callCount() != 1 {
		t.Fatalf("transport called %d times, want the restored ledger to replay without a new exchange", tr.callCount())
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	after, err := restored.Budget()
	if err != nil {
		t.Fatalf("budget after a rejected restore: %v", err)
	}
	if after.Probes != 1 || len(restored.Surfaces()) != 12 {
		t.Fatalf("a rejected restore must not touch live state, got %+v", after)
	}
	if _, err := restored.Probe(surface, impl.Request{Method: "GET"}); err != nil {
		t.Fatalf("live ledger must keep working after a rejected restore, got %v", err)
	}
}
