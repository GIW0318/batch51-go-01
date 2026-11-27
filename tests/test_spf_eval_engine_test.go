package task

import (
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-b-09"
)

type fakeResolver struct {
	mu    sync.Mutex
	txt   map[string][]string
	a     map[string][]netip.Addr
	mx    map[string][]string
	ptr   map[string][]string
	calls []string
	delay time.Duration
}

func newResolver() *fakeResolver {
	return &fakeResolver{
		txt: map[string][]string{},
		a:   map[string][]netip.Addr{},
		mx:  map[string][]string{},
		ptr: map[string][]string{},
	}
}

func (r *fakeResolver) TXT(name string) ([]string, error) {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "TXT "+name)
	return append([]string(nil), r.txt[name]...), nil
}

func (r *fakeResolver) A(name string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "A "+name)
	return append([]netip.Addr(nil), r.a[name]...), nil
}

func (r *fakeResolver) MX(name string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "MX "+name)
	return append([]string(nil), r.mx[name]...), nil
}

func (r *fakeResolver) PTR(addr netip.Addr) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "PTR "+addr.String())
	return append([]string(nil), r.ptr[addr.String()]...), nil
}

func (r *fakeResolver) callCount(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, call := range r.calls {
		if len(call) > len(kind) && call[:len(kind)] == kind {
			n++
		}
	}
	return n
}

func (r *fakeResolver) lastCalls(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.calls) {
		n = len(r.calls)
	}
	out := make([]string, n)
	copy(out, r.calls[len(r.calls)-n:])
	return out
}

func baseConfig() impl.SpfConfig {
	return impl.SpfConfig{
		MaxDNSQueries:   10,
		MaxVoidLookups:  2,
		MaxRecursion:    10,
		MaxMechanisms:   32,
		CacheTTL:        time.Minute,
		MaxCacheEntries: 16,
	}
}

func newEngine(t *testing.T, r impl.Resolver, cfg impl.SpfConfig) *impl.SpfEvalEngine {
	t.Helper()
	e := impl.NewSpfEvalEngine(cfg)
	if err := e.SetResolver(r); err != nil {
		t.Fatalf("set resolver: %v", err)
	}
	return e
}

func ip(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse address %q: %v", s, err)
	}
	return a
}

func TestEvaluateIP4PassAndFail(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 ip4:192.0.2.0/24 -all"}
	e := newEngine(t, r, baseConfig())
	res, err := e.Evaluate(ip(t, "192.0.2.55"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate inside prefix: %v", err)
	}
	if res.Result != impl.ResultPass || res.Qualifier != impl.QualifierPass {
		t.Fatalf("inside prefix result = %q qualifier %q, want pass with +", res.Result, res.Qualifier)
	}
	if res.Mechanism != "+ip4:192.0.2.0/24" {
		t.Fatalf("matched mechanism = %q, want the canonical term", res.Mechanism)
	}
	if res.Queries != 1 {
		t.Fatalf("query count = %d, want the single record lookup", res.Queries)
	}
	if res.Record != "v=spf1 ip4:192.0.2.0/24 -all" || res.Domain != "example.com" || res.Sender != "bob@example.com" {
		t.Fatalf("result identity = %+v, want the record and request echoed", res)
	}
	res, err = e.Evaluate(ip(t, "198.51.100.1"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate outside prefix: %v", err)
	}
	if res.Result != impl.ResultFail || res.Qualifier != impl.QualifierFail || res.Mechanism != "-all" {
		t.Fatalf("outside prefix result = %+v, want fail from -all", res)
	}
}

func TestEvaluateAllQualifiersAndNone(t *testing.T) {
	r := newResolver()
	r.txt["fail.example"] = []string{"v=spf1 -all"}
	r.txt["soft.example"] = []string{"v=spf1 ~all"}
	r.txt["neutral.example"] = []string{"v=spf1 ?all"}
	e := newEngine(t, r, baseConfig())
	cases := []struct {
		domain string
		want   string
		term   string
	}{
		{"fail.example", impl.ResultFail, "-all"},
		{"soft.example", impl.ResultSoftfail, "~all"},
		{"neutral.example", impl.ResultNeutral, "?all"},
	}
	for _, c := range cases {
		res, err := e.Evaluate(ip(t, "192.0.2.1"), "user@"+c.domain, c.domain)
		if err != nil {
			t.Fatalf("evaluate %s: %v", c.domain, err)
		}
		if res.Result != c.want || res.Mechanism != c.term {
			t.Fatalf("%s result = %q mechanism %q, want %q via %s", c.domain, res.Result, res.Mechanism, c.want, c.term)
		}
	}
	res, err := e.Evaluate(ip(t, "192.0.2.1"), "user@none.example", "none.example")
	if err != nil {
		t.Fatalf("evaluate a domain without a record: %v", err)
	}
	if res.Result != impl.ResultNone {
		t.Fatalf("missing record result = %q, want none", res.Result)
	}
}

func TestEvaluateDefaultQualifierIsPass(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 ip4:203.0.113.0/24"}
	e := newEngine(t, r, baseConfig())
	res, err := e.Evaluate(ip(t, "203.0.113.9"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Result != impl.ResultPass || res.Qualifier != impl.QualifierPass {
		t.Fatalf("unqualified ip4 result = %q qualifier %q, want pass with an explicit +", res.Result, res.Qualifier)
	}
	if res.Mechanism != "+ip4:203.0.113.0/24" {
		t.Fatalf("matched mechanism = %q, want the canonical term with +", res.Mechanism)
	}
	res, err = e.Evaluate(ip(t, "10.0.0.1"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate a non matching address: %v", err)
	}
	if res.Result != impl.ResultNeutral {
		t.Fatalf("no matching mechanism and no redirect = %q, want neutral", res.Result)
	}
}

func TestEvaluateMultipleRecordsIsPermError(t *testing.T) {
	r := newResolver()
	r.txt["multi.example"] = []string{"v=spf1 -all", "v=spf1 +all"}
	r.txt["plain.example"] = []string{"contact us at postmaster"}
	e := newEngine(t, r, baseConfig())
	res, err := e.Evaluate(ip(t, "192.0.2.1"), "user@multi.example", "multi.example")
	if err != nil {
		t.Fatalf("evaluate two records: %v", err)
	}
	if res.Result != impl.ResultPermError {
		t.Fatalf("two published records result = %q, want permerror", res.Result)
	}
	res, err = e.Evaluate(ip(t, "192.0.2.1"), "user@plain.example", "plain.example")
	if err != nil {
		t.Fatalf("evaluate a non spf txt: %v", err)
	}
	if res.Result != impl.ResultNone {
		t.Fatalf("non spf txt result = %q, want none", res.Result)
	}
}

func TestEvaluateUnknownMechanismPermError(t *testing.T) {
	r := newResolver()
	r.txt["unknown.example"] = []string{"v=spf1 frobnicate:example.com -all"}
	r.txt["badip.example"] = []string{"v=spf1 ip4:999.1.1.1 -all"}
	e := newEngine(t, r, baseConfig())
	for _, domain := range []string{"unknown.example", "badip.example"} {
		res, err := e.Evaluate(ip(t, "192.0.2.1"), "user@"+domain, domain)
		if err != nil {
			t.Fatalf("evaluate %s: %v", domain, err)
		}
		if res.Result != impl.ResultPermError {
			t.Fatalf("%s result = %q, want permerror", domain, res.Result)
		}
	}
}

func TestEvaluateIncludePass(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 include:_spf.example.com -all"}
	r.txt["_spf.example.com"] = []string{"v=spf1 ip4:192.0.2.0/24 -all"}
	e := newEngine(t, r, baseConfig())
	res, err := e.Evaluate(ip(t, "192.0.2.9"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate include: %v", err)
	}
	if res.Result != impl.ResultPass || res.Qualifier != impl.QualifierPass {
		t.Fatalf("include pass result = %q qualifier %q, want pass", res.Result, res.Qualifier)
	}
	if res.Mechanism != "+include:_spf.example.com" {
		t.Fatalf("matched mechanism = %q, want the include term", res.Mechanism)
	}
	if res.Queries != 2 {
		t.Fatalf("query count = %d, want the record lookup plus the include lookup", res.Queries)
	}
}

func TestEvaluateIncludeFailContinues(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 include:_spf.example.com ~all"}
	r.txt["_spf.example.com"] = []string{"v=spf1 -all"}
	e := newEngine(t, r, baseConfig())
	res, err := e.Evaluate(ip(t, "192.0.2.9"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate include fail: %v", err)
	}
	if res.Result != impl.ResultSoftfail || res.Mechanism != "~all" {
		t.Fatalf("include fail result = %q mechanism %q, want softfail via ~all", res.Result, res.Mechanism)
	}
}

func TestEvaluateRedirectTargetResult(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 redirect=_spf.example.com"}
	r.txt["_spf.example.com"] = []string{"v=spf1 ip4:192.0.2.0/24 -all"}
	r.txt["broken.example"] = []string{"v=spf1 redirect=_spf2.example.com"}
	r.txt["_spf2.example.com"] = []string{"v=spf1 -all"}
	e := newEngine(t, r, baseConfig())
	res, err := e.Evaluate(ip(t, "192.0.2.9"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate redirect: %v", err)
	}
	if res.Result != impl.ResultPass {
		t.Fatalf("redirect to a passing record = %q, want pass", res.Result)
	}
	if res.Mechanism != "redirect=_spf.example.com" {
		t.Fatalf("redirect mechanism = %q, want the redirect term", res.Mechanism)
	}
	res, err = e.Evaluate(ip(t, "192.0.2.9"), "bob@broken.example", "broken.example")
	if err != nil {
		t.Fatalf("evaluate failing redirect: %v", err)
	}
	if res.Result != impl.ResultFail || res.Mechanism != "redirect=_spf2.example.com" {
		t.Fatalf("redirect to a failing record = %q/%q, want fail via the redirect term", res.Result, res.Mechanism)
	}
}

func TestEvaluateQueryBudgetExceededPermError(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 a:a.example.com a:b.example.com a:c.example.com -all"}
	for _, name := range []string{"a.example.com", "b.example.com", "c.example.com"} {
		r.a[name] = []netip.Addr{ip(t, "10.1.1.1")}
	}
	cfg := baseConfig()
	cfg.MaxDNSQueries = 2
	e := newEngine(t, r, cfg)
	res, err := e.Evaluate(ip(t, "192.0.2.1"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate over budget: %v", err)
	}
	if res.Result != impl.ResultPermError {
		t.Fatalf("exceeding the dns budget = %q, want permerror", res.Result)
	}
}

func TestEvaluateVoidBudgetExceededPermError(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 exists:a.example.com exists:b.example.com exists:c.example.com -all"}
	cfg := baseConfig()
	cfg.MaxVoidLookups = 2
	e := newEngine(t, r, cfg)
	res, err := e.Evaluate(ip(t, "192.0.2.1"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate over the void budget: %v", err)
	}
	if res.Result != impl.ResultPermError {
		t.Fatalf("exceeding the void budget = %q, want permerror", res.Result)
	}
}

func TestEvaluateMacroExpansion(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 exists:%{l}.%{d} exists:%{i}.bl.example.net -all"}
	r.a["203.0.113.7.bl.example.net"] = []netip.Addr{ip(t, "203.0.113.7")}
	e := newEngine(t, r, baseConfig())
	res, err := e.Evaluate(ip(t, "203.0.113.7"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate macros: %v", err)
	}
	if res.Result != impl.ResultPass || res.Mechanism != "+exists:%{i}.bl.example.net" {
		t.Fatalf("macro result = %q mechanism %q, want pass via the second exists", res.Result, res.Mechanism)
	}
	calls := r.lastCalls(2)
	if calls[0] != "A bob.example.com" || calls[1] != "A 203.0.113.7.bl.example.net" {
		t.Fatalf("queries = %v, want the expanded names in order", calls)
	}
}

func TestEvaluateAWithCIDR(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 a:mail.example.com/24 -all"}
	r.a["mail.example.com"] = []netip.Addr{ip(t, "192.0.2.10")}
	e := newEngine(t, r, baseConfig())
	res, err := e.Evaluate(ip(t, "192.0.2.201"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate a mechanism: %v", err)
	}
	if res.Result != impl.ResultPass || res.Mechanism != "+a:mail.example.com/24" {
		t.Fatalf("a/24 match = %q/%q, want pass via the a term", res.Result, res.Mechanism)
	}
	res, err = e.Evaluate(ip(t, "198.51.100.1"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluate a mechanism miss: %v", err)
	}
	if res.Result != impl.ResultFail || res.Mechanism != "-all" {
		t.Fatalf("a/24 miss = %q/%q, want fail via -all", res.Result, res.Mechanism)
	}
}

func TestExplainReturnsSteps(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 ip4:192.0.2.0/24 include:_spf.example.com -all"}
	e := newEngine(t, r, baseConfig())
	trace, err := e.Explain("example.com")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if trace.Domain != "example.com" || trace.Record != "v=spf1 ip4:192.0.2.0/24 include:_spf.example.com -all" {
		t.Fatalf("trace head = %+v, want the domain and raw record", trace)
	}
	want := []string{"+ip4:192.0.2.0/24", "+include:_spf.example.com", "-all"}
	if len(trace.Steps) != len(want) {
		t.Fatalf("trace steps = %d, want %d", len(trace.Steps), len(want))
	}
	for i, mechanism := range want {
		if trace.Steps[i].Mechanism != mechanism {
			t.Fatalf("step %d = %q, want %q", i, trace.Steps[i].Mechanism, mechanism)
		}
	}
	if trace.Result != "parsed" {
		t.Fatalf("trace result = %q, want parsed", trace.Result)
	}
	record, err := e.ResolveRecord("example.com")
	if err != nil {
		t.Fatalf("resolve record: %v", err)
	}
	if len(record.Mechanisms) != 3 || record.Redirect != nil {
		t.Fatalf("parsed record = %+v, want three mechanisms and no redirect", record)
	}
	if _, err := e.ResolveRecord("missing.example"); !errors.Is(err, impl.ErrNoRecord) {
		t.Fatalf("missing record must fail with ErrNoRecord, got %v", err)
	}
	if _, err := e.Explain("missing.example"); !errors.Is(err, impl.ErrNoRecord) {
		t.Fatalf("explaining a missing record must fail with ErrNoRecord, got %v", err)
	}
}

func TestSnapshotRestoreCacheAndCorrupt(t *testing.T) {
	r := newResolver()
	r.txt["example.com"] = []string{"v=spf1 ip4:192.0.2.0/24 -all"}
	cfg := baseConfig()
	e := newEngine(t, r, cfg)
	first, err := e.Evaluate(ip(t, "192.0.2.5"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if first.Cached {
		t.Fatalf("the first evaluation of a key must not be marked cached")
	}
	second, err := e.Evaluate(ip(t, "192.0.2.5"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("second evaluation: %v", err)
	}
	if !second.Cached {
		t.Fatalf("the repeated evaluation must be served from the cache")
	}
	if got := e.Stats().Hits; got != 1 {
		t.Fatalf("cache hit counter = %d, want 1", got)
	}
	if got := e.Stats().Cache; got != 1 {
		t.Fatalf("cache size = %d, want 1 result entry", got)
	}
	data, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newEngine(t, r, cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	third, err := restored.Evaluate(ip(t, "192.0.2.5"), "bob@example.com", "example.com")
	if err != nil {
		t.Fatalf("evaluation after restore: %v", err)
	}
	if !third.Cached {
		t.Fatalf("restored cache must serve the same key")
	}
	if got := r.callCount("TXT"); got != 1 {
		t.Fatalf("resolver txt calls = %d, want 1 thanks to the restored cache", got)
	}
	if err := restored.Restore([]byte("{bad json")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Stats().Cache; got != 1 {
		t.Fatalf("rejected restore must keep the live cache, size = %d", got)
	}
}

func TestConcurrentEvaluateSingleFlight(t *testing.T) {
	r := newResolver()
	r.delay = 30 * time.Millisecond
	r.txt["example.com"] = []string{"v=spf1 ip4:192.0.2.0/24 -all"}
	e := newEngine(t, r, baseConfig())
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	bad := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := e.Evaluate(ip(t, "192.0.2.7"), "bob@example.com", "example.com")
			mu.Lock()
			defer mu.Unlock()
			if err != nil || res.Result != impl.ResultPass {
				bad++
			}
		}()
	}
	close(start)
	wg.Wait()
	if bad != 0 {
		t.Fatalf("%d concurrent evaluations failed or returned a wrong result", bad)
	}
	if got := r.callCount("TXT"); got != 1 {
		t.Fatalf("resolver txt calls = %d, want the concurrent lookups merged into one", got)
	}
	if got := e.Stats().Cache; got != 1 {
		t.Fatalf("cache size = %d, want 1", got)
	}
}
