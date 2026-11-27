package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-39"
)

type routeTransport struct {
	mu    sync.Mutex
	calls int
	route map[string]impl.Response
	def   impl.Response
}

func (r *routeTransport) Fetch(path string) (impl.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if resp, ok := r.route[path]; ok {
		return resp, nil
	}
	return r.def, nil
}

func (r *routeTransport) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type fixedClock struct {
	now int64
}

func (f *fixedClock) Now() int64 { return f.now }

func testConfig() impl.ProberConfig {
	return impl.ProberConfig{MaxRequests: 32,
		ProtectedMarkers: []string{"admin-console", "user-list"}}
}

func newProber(t *testing.T, cfg impl.ProberConfig, tr impl.Transport) *impl.PathAccessBypassProber {
	t.Helper()
	p := impl.NewPathAccessBypassProber(cfg)
	if err := p.SetTransport(tr); err != nil {
		t.Fatalf("set transport must succeed, got %v", err)
	}
	if err := p.SetClock(&fixedClock{now: 1000}); err != nil {
		t.Fatalf("set clock must succeed, got %v", err)
	}
	return p
}

func variantByDimension(t *testing.T, vs []impl.PathVariant, dim string) impl.PathVariant {
	t.Helper()
	for _, v := range vs {
		if v.Dimension == dim {
			return v
		}
	}
	t.Fatalf("no variant for dimension %q", dim)
	return impl.PathVariant{}
}

func TestBaselineCapturesProtectedFingerprint(t *testing.T) {
	body := "admin-console dashboard"
	tr := &routeTransport{def: impl.Response{Status: 200, Body: body}}
	p := newProber(t, testConfig(), tr)
	base, err := p.Baseline("/admin")
	if err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	if base.Path != "/admin" {
		t.Fatalf("baseline path = %q, want /admin", base.Path)
	}
	if base.Status != 200 {
		t.Fatalf("baseline status = %d, want 200", base.Status)
	}
	if base.Length != len(body) {
		t.Fatalf("baseline length = %d, want %d", base.Length, len(body))
	}
	if len(base.Markers) != 1 || base.Markers[0] != "admin-console" {
		t.Fatalf("baseline markers = %v, want [admin-console]", base.Markers)
	}
	if base.Fingerprint == "" {
		t.Fatalf("baseline fingerprint must not be empty")
	}
	if got := tr.count(); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}

func TestBaselineRejectsBlankPath(t *testing.T) {
	p := newProber(t, testConfig(), &routeTransport{})
	if _, err := p.Baseline(""); !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("empty path error = %v, want %v", err, impl.ErrBadPath)
	}
	if _, err := p.Baseline("   "); !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("blank path error = %v, want %v", err, impl.ErrBadPath)
	}
	bare := impl.NewPathAccessBypassProber(testConfig())
	if _, err := bare.Baseline("/admin"); !errors.Is(err, impl.ErrNoTransport) {
		t.Fatalf("baseline without a transport error = %v, want %v", err, impl.ErrNoTransport)
	}
}

func TestVariantsCoverSixDimensionsInFixedOrder(t *testing.T) {
	p := newProber(t, testConfig(), &routeTransport{})
	vs, err := p.Variants("/admin/users")
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	want := []string{impl.DimSingleEncode, impl.DimDoubleEncode, impl.DimDotSegment,
		impl.DimSemicolon, impl.DimCase, impl.DimTrailingSlash}
	if len(vs) != len(want) {
		t.Fatalf("variant count = %d, want %d", len(vs), len(want))
	}
	seen := map[string]bool{}
	for i, v := range vs {
		if v.Dimension != want[i] {
			t.Fatalf("variant %d dimension = %q, want %q", i, v.Dimension, want[i])
		}
		if v.ID == "" || seen[v.ID] {
			t.Fatalf("variant %d id = %q is empty or repeated", i, v.ID)
		}
		seen[v.ID] = true
		if v.Path == "" || v.Path == "/admin/users" {
			t.Fatalf("variant %d path = %q, want a mutated path", i, v.Path)
		}
	}
}

func TestVariantsAreStableAndTrailingSlashAppends(t *testing.T) {
	p := newProber(t, testConfig(), &routeTransport{})
	first, err := p.Variants("/admin/users")
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	second, err := p.Variants("/admin/users")
	if err != nil {
		t.Fatalf("second variants call must succeed, got %v", err)
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Path != second[i].Path {
			t.Fatalf("variant %d is not stable across calls", i)
		}
	}
	slash := variantByDimension(t, first, impl.DimTrailingSlash)
	if slash.Path != "/admin/users/" {
		t.Fatalf("trailing slash variant = %q, want /admin/users/", slash.Path)
	}
	cased := variantByDimension(t, first, impl.DimCase)
	if cased.Path != "/ADMIN/USERS" {
		t.Fatalf("case variant = %q, want /ADMIN/USERS", cased.Path)
	}
}

func TestProbeSendsAtMostOncePerVariant(t *testing.T) {
	body := "admin-console dashboard"
	tr := &routeTransport{def: impl.Response{Status: 200, Body: body}}
	p := newProber(t, testConfig(), tr)
	vs, err := p.Variants("/admin")
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
	if obs.Path != vs[0].Path {
		t.Fatalf("observation path = %q, want %q", obs.Path, vs[0].Path)
	}
	if obs.Status != 200 || obs.Length != len(body) {
		t.Fatalf("observation = status %d length %d, want 200 and %d", obs.Status, obs.Length, len(body))
	}
	if len(obs.Markers) != 1 || obs.Markers[0] != "admin-console" {
		t.Fatalf("observation markers = %v, want [admin-console]", obs.Markers)
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

func TestProbeRejectsUnknownVariant(t *testing.T) {
	tr := &routeTransport{}
	p := newProber(t, testConfig(), tr)
	if _, err := p.Probe(impl.PathVariant{ID: "ghost", Dimension: impl.DimCase, Path: "/ADMIN"}); !errors.Is(err, impl.ErrUnknownVariant) {
		t.Fatalf("probe of a foreign variant error = %v, want %v", err, impl.ErrUnknownVariant)
	}
	if got := tr.count(); got != 0 {
		t.Fatalf("transport calls = %d, want 0", got)
	}
}

func TestProbeEnforcesRequestBudget(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRequests = 2
	tr := &routeTransport{}
	p := newProber(t, cfg, tr)
	vs, err := p.Variants("/admin")
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
}

func TestBudgetStateTracksUsage(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRequests = 10
	tr := &routeTransport{def: impl.Response{Status: 200, Body: "admin-console dashboard"}}
	p := newProber(t, cfg, tr)
	if _, err := p.Baseline("/admin"); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	vs, err := p.Variants("/admin")
	if err != nil || len(vs) < 2 {
		t.Fatalf("variants = %d err = %v, want at least two", len(vs), err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.Probe(vs[i]); err != nil {
			t.Fatalf("probe %d must succeed, got %v", i, err)
		}
	}
	budget, err := p.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 3 || budget.Limit != 10 || budget.Remaining != 7 {
		t.Fatalf("budget = %+v, want used 3 limit 10 remaining 7", budget)
	}
}

func TestJudgeMarksBypassWithProtectedMarker(t *testing.T) {
	tr := &routeTransport{
		route: map[string]impl.Response{
			"/admin":  {Status: 200, Body: "admin-console dashboard"},
			"/admin/": {Status: 200, Body: "admin-console dashboard with export buttons"},
		},
		def: impl.Response{Status: 403, Body: "forbidden"},
	}
	p := newProber(t, testConfig(), tr)
	if _, err := p.Baseline("/admin"); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	vs, err := p.Variants("/admin")
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	target := variantByDimension(t, vs, impl.DimTrailingSlash)
	obs, err := p.Probe(target)
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	verdict, err := p.Judge([]impl.Observation{obs})
	if err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	if len(verdict.Bypassed) != 1 || verdict.Bypassed[0] != target.ID {
		t.Fatalf("bypassed = %v, want exactly %q", verdict.Bypassed, target.ID)
	}
	if len(verdict.Rejected) != 0 {
		t.Fatalf("rejected = %v, want none", verdict.Rejected)
	}
	if verdict.Reason == "" {
		t.Fatalf("verdict reason must not be empty")
	}
}

func TestJudgeIgnoresForbiddenAndMissing(t *testing.T) {
	tr := &routeTransport{
		route: map[string]impl.Response{
			"/admin":        {Status: 200, Body: "admin-console dashboard"},
			"/admin/users/": {Status: 403, Body: "forbidden"},
		},
		def: impl.Response{Status: 404, Body: "not found"},
	}
	p := newProber(t, testConfig(), tr)
	if _, err := p.Baseline("/admin"); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	vs, err := p.Variants("/admin/users")
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	forbidden := variantByDimension(t, vs, impl.DimTrailingSlash)
	missing := variantByDimension(t, vs, impl.DimSemicolon)
	first, err := p.Probe(forbidden)
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	second, err := p.Probe(missing)
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	verdict, err := p.Judge([]impl.Observation{first, second})
	if err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	if len(verdict.Bypassed) != 0 {
		t.Fatalf("bypassed = %v, want none for 403 and 404", verdict.Bypassed)
	}
	if len(verdict.Rejected) != 2 {
		t.Fatalf("rejected = %v, want two entries", verdict.Rejected)
	}
}

func TestJudgeRequiresBaselineAndObservations(t *testing.T) {
	cfg := testConfig()
	p := newProber(t, cfg, &routeTransport{def: impl.Response{Status: 200, Body: "admin-console"}})
	if _, err := p.Baseline("/admin"); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	if _, err := p.Judge(nil); !errors.Is(err, impl.ErrNoObservations) {
		t.Fatalf("judge on empty observations error = %v, want %v", err, impl.ErrNoObservations)
	}
	fresh := newProber(t, cfg, &routeTransport{})
	obs := []impl.Observation{{VariantID: "admin/trailing_slash", Dimension: impl.DimTrailingSlash,
		Path: "/admin/", Status: 200, Markers: []string{"admin-console"}, Fingerprint: "dead-beef"}}
	if _, err := fresh.Judge(obs); !errors.Is(err, impl.ErrNoBaseline) {
		t.Fatalf("judge without a baseline error = %v, want %v", err, impl.ErrNoBaseline)
	}
}

func TestJudgeIsRepeatable(t *testing.T) {
	tr := &routeTransport{
		route: map[string]impl.Response{
			"/admin":  {Status: 200, Body: "admin-console dashboard"},
			"/admin/": {Status: 200, Body: "admin-console dashboard with export buttons"},
		},
		def: impl.Response{Status: 403, Body: "forbidden"},
	}
	p := newProber(t, testConfig(), tr)
	if _, err := p.Baseline("/admin"); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	vs, err := p.Variants("/admin")
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	obs, err := p.Probe(variantByDimension(t, vs, impl.DimTrailingSlash))
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	first, err := p.Judge([]impl.Observation{obs})
	if err != nil {
		t.Fatalf("first judge must succeed, got %v", err)
	}
	second, err := p.Judge([]impl.Observation{obs})
	if err != nil {
		t.Fatalf("second judge must succeed, got %v", err)
	}
	if len(first.Bypassed) != len(second.Bypassed) || first.Bypassed[0] != second.Bypassed[0] {
		t.Fatalf("verdicts differ between calls: %v vs %v", first.Bypassed, second.Bypassed)
	}
	if first.Reason != second.Reason {
		t.Fatalf("verdict reasons differ: %q vs %q", first.Reason, second.Reason)
	}
}

func TestConcurrentProbeSendsOneRequest(t *testing.T) {
	tr := &routeTransport{def: impl.Response{Status: 200, Body: "admin-console dashboard"}}
	p := newProber(t, testConfig(), tr)
	vs, err := p.Variants("/admin")
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

func TestSnapshotRestoreKeepsBaselineAndProbedSet(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRequests = 10
	tr := &routeTransport{
		route: map[string]impl.Response{
			"/admin":  {Status: 200, Body: "admin-console dashboard"},
			"/admin/": {Status: 200, Body: "admin-console dashboard with export buttons"},
		},
		def: impl.Response{Status: 403, Body: "forbidden"},
	}
	p := newProber(t, cfg, tr)
	if _, err := p.Baseline("/admin"); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	vs, err := p.Variants("/admin")
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	slash := variantByDimension(t, vs, impl.DimTrailingSlash)
	if _, err := p.Probe(slash); err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	snap, err := p.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot len = %d err = %v, want a payload", len(snap), err)
	}
	restored := impl.NewPathAccessBypassProber(cfg)
	if err := restored.SetTransport(&routeTransport{def: impl.Response{Status: 403}}); err != nil {
		t.Fatalf("set transport must succeed, got %v", err)
	}
	if err := restored.SetClock(&fixedClock{now: 2000}); err != nil {
		t.Fatalf("set clock must succeed, got %v", err)
	}
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	budget, err := restored.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 2 {
		t.Fatalf("restored budget used = %d, want 2", budget.Used)
	}
	if got := len(restored.Observations()); got != 1 {
		t.Fatalf("restored observations = %d, want 1", got)
	}
	if _, err := restored.Probe(slash); !errors.Is(err, impl.ErrVariantProbed) {
		t.Fatalf("replayed probe after restore error = %v, want %v", err, impl.ErrVariantProbed)
	}
	verdict, err := restored.Judge(restored.Observations())
	if err != nil {
		t.Fatalf("judge after restore must succeed, got %v", err)
	}
	if len(verdict.Bypassed) != 1 || verdict.Bypassed[0] != slash.ID {
		t.Fatalf("verdict after restore = %v, want exactly %q", verdict.Bypassed, slash.ID)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	cfg := testConfig()
	p := newProber(t, cfg, &routeTransport{def: impl.Response{Status: 200, Body: "admin-console dashboard"}})
	if _, err := p.Baseline("/admin"); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	vs, err := p.Variants("/admin")
	if err != nil || len(vs) == 0 {
		t.Fatalf("variants = %d err = %v, want at least one", len(vs), err)
	}
	if _, err := p.Probe(vs[0]); err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	if err := p.Restore([]byte("@@corrupt@@")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want %v", err, impl.ErrBadSnapshot)
	}
	if got := len(p.Observations()); got != 1 {
		t.Fatalf("observations after corrupt restore = %d, want 1", got)
	}
	budget, err := p.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 2 {
		t.Fatalf("budget used after corrupt restore = %d, want 2", budget.Used)
	}
}
