package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-24"
)

const exactOrigin = "https://app.example.com"

func newCorsProber(t *testing.T) *impl.CorsMisconfigProber {
	t.Helper()
	return impl.NewCorsMisconfigProber(impl.ProberConfig{
		MaxConcurrent:        2,
		MaxProbesPerEndpoint: 200,
	})
}

func exactPolicy(name string) impl.CorsPolicy {
	return impl.CorsPolicy{
		Name:             name,
		AllowedOrigins:   []string{exactOrigin},
		PreflightAllowed: true,
		ActualAllowed:    true,
	}
}

func TestBuildExactOriginRequest(t *testing.T) {
	p := newCorsProber(t)
	req, err := p.Build(impl.OriginExact, exactOrigin, false, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.Kind != impl.OriginExact || req.Origin != exactOrigin {
		t.Fatalf("request identity mismatch: kind=%q origin=%q", req.Kind, req.Origin)
	}
	if req.Method != "GET" || req.Preflight {
		t.Fatalf("method=%q preflight=%v, want a plain GET", req.Method, req.Preflight)
	}
	if req.Headers["Origin"] != exactOrigin {
		t.Fatalf("origin header = %q, want %q", req.Headers["Origin"], exactOrigin)
	}
	if _, ok := req.Headers["Cookie"]; ok {
		t.Fatalf("a request without credentials must not carry a cookie header")
	}
	if req.Marker == "" {
		t.Fatalf("every probe request must carry a recyclable marker")
	}
}

func TestBuildPreflightWithCredentials(t *testing.T) {
	p := newCorsProber(t)
	req, err := p.Build(impl.OriginSuffix, "https://app.example.com.evil.test", true, true)
	if err != nil {
		t.Fatalf("build preflight request: %v", err)
	}
	if !req.Preflight || req.Method != "OPTIONS" {
		t.Fatalf("method=%q preflight=%v, want an OPTIONS preflight", req.Method, req.Preflight)
	}
	if !req.WithCredentials {
		t.Fatalf("request must remember that credentials were attached")
	}
	if req.Headers["Access-Control-Request-Method"] != "GET" {
		t.Fatalf("preflight must announce the actual method, headers=%v", req.Headers)
	}
	if req.Headers["Cookie"] == "" {
		t.Fatalf("a credentialed request must carry the cookie header, headers=%v", req.Headers)
	}
}

func TestBuildRejectsBadInputs(t *testing.T) {
	p := newCorsProber(t)
	if _, err := p.Build(impl.OriginExact, "   ", false, false); !errors.Is(err, impl.ErrBadOrigin) {
		t.Fatalf("blank origin must be rejected with ErrBadOrigin, got %v", err)
	}
	if _, err := p.Build("blind", exactOrigin, false, false); !errors.Is(err, impl.ErrBadKind) {
		t.Fatalf("unknown origin kind must be rejected with ErrBadKind, got %v", err)
	}
}

func TestAddEndpointRejectsBlankAndDuplicate(t *testing.T) {
	p := newCorsProber(t)
	blank := exactPolicy("   ")
	if err := p.AddEndpoint(blank); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("blank endpoint name must be rejected with ErrBadEndpoint, got %v", err)
	}
	if err := p.AddEndpoint(exactPolicy("app")); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	if err := p.AddEndpoint(exactPolicy("app")); !errors.Is(err, impl.ErrDuplicateEndpoint) {
		t.Fatalf("duplicate endpoint must be rejected with ErrDuplicateEndpoint, got %v", err)
	}
	if endpoints := p.Endpoints(); len(endpoints) != 1 || endpoints[0] != "app" {
		t.Fatalf("endpoints = %v, want exactly [app]", endpoints)
	}
}

func TestProbeRejectsUnknownEndpoint(t *testing.T) {
	p := newCorsProber(t)
	req, err := p.Build(impl.OriginExact, exactOrigin, false, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if _, err := p.Probe("missing", req); !errors.Is(err, impl.ErrUnknownEndpoint) {
		t.Fatalf("unknown endpoint must be rejected with ErrUnknownEndpoint, got %v", err)
	}
}

func TestProbeExactAllowlistEchoesOrigin(t *testing.T) {
	p := newCorsProber(t)
	if err := p.AddEndpoint(exactPolicy("app")); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	req, err := p.Build(impl.OriginExact, exactOrigin, false, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := p.Probe("app", req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if resp.AllowOrigin != exactOrigin {
		t.Fatalf("allow origin = %q, want the exact whitelisted %q", resp.AllowOrigin, exactOrigin)
	}
	if !resp.VaryOrigin {
		t.Fatalf("a reflected origin must be marked as varying on the origin header")
	}
	if resp.Marker != req.Marker {
		t.Fatalf("response marker = %q, want the request marker %q", resp.Marker, req.Marker)
	}
	records := p.Records("app")
	if len(records) != 1 || records[0].AllowOrigin != exactOrigin || records[0].StatusCode != 200 {
		t.Fatalf("records = %+v, want one allowed record", records)
	}
}

func TestProbeSuffixSimilarOriginDenied(t *testing.T) {
	p := newCorsProber(t)
	if err := p.AddEndpoint(exactPolicy("app")); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	req, err := p.Build(impl.OriginSuffix, exactOrigin+".evil.test", false, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := p.Probe("app", req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if resp.AllowOrigin != "" {
		t.Fatalf("suffix similar origin %q must not be echoed, got %q", req.Origin, resp.AllowOrigin)
	}
	if stats := p.Stats("app"); stats.Allowed != 0 || stats.Denied != 1 {
		t.Fatalf("stats allowed=%d denied=%d, want 0 and 1", stats.Allowed, stats.Denied)
	}
}

func TestProbeReflectAllEchoesAnyOrigin(t *testing.T) {
	p := newCorsProber(t)
	policy := exactPolicy("open")
	policy.ReflectAll = true
	if err := p.AddEndpoint(policy); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	req, err := p.Build(impl.OriginArbitrary, "https://evil.test", false, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := p.Probe("open", req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if resp.AllowOrigin != "https://evil.test" {
		t.Fatalf("allow origin = %q, want the arbitrary origin echoed verbatim", resp.AllowOrigin)
	}
	if !resp.VaryOrigin {
		t.Fatalf("reflect all endpoints must set vary origin")
	}
}

func TestCredentialsFollowPolicy(t *testing.T) {
	p := newCorsProber(t)
	strict := exactPolicy("strict")
	if err := p.AddEndpoint(strict); err != nil {
		t.Fatalf("add strict endpoint: %v", err)
	}
	trusted := exactPolicy("trusted")
	trusted.AllowCredentials = true
	if err := p.AddEndpoint(trusted); err != nil {
		t.Fatalf("add trusted endpoint: %v", err)
	}
	req, err := p.Build(impl.OriginExact, exactOrigin, true, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	strictResp, err := p.Probe("strict", req)
	if err != nil {
		t.Fatalf("probe strict: %v", err)
	}
	if strictResp.AllowCredentials {
		t.Fatalf("strict endpoint must not allow credentials, got %+v", strictResp)
	}
	trustedResp, err := p.Probe("trusted", req)
	if err != nil {
		t.Fatalf("probe trusted: %v", err)
	}
	if !trustedResp.AllowCredentials {
		t.Fatalf("trusted endpoint must allow credentials, got %+v", trustedResp)
	}
	if trustedResp.AllowOrigin != exactOrigin {
		t.Fatalf("trusted allow origin = %q, want %q", trustedResp.AllowOrigin, exactOrigin)
	}
}

func TestJudgeClassifiesExactAllowlist(t *testing.T) {
	p := newCorsProber(t)
	if err := p.AddEndpoint(exactPolicy("app")); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	verdict, err := p.Judge("app")
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if verdict.Class != impl.PolicyExact {
		t.Fatalf("policy class = %q, want %q", verdict.Class, impl.PolicyExact)
	}
	if verdict.Reflected {
		t.Fatalf("an exact allowlist must not be graded as reflecting arbitrary origins, got %+v", verdict)
	}
	if verdict.Endpoint != "app" || verdict.Seq <= 0 {
		t.Fatalf("verdict identity mismatch: %+v", verdict)
	}
}

func TestJudgeClassifiesReflectAll(t *testing.T) {
	p := newCorsProber(t)
	policy := exactPolicy("open")
	policy.ReflectAll = true
	if err := p.AddEndpoint(policy); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	verdict, err := p.Judge("open")
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if verdict.Class != impl.PolicyReflect {
		t.Fatalf("policy class = %q, want %q", verdict.Class, impl.PolicyReflect)
	}
	if !verdict.Reflected {
		t.Fatalf("reflect all endpoint must be graded as reflecting, got %+v", verdict)
	}
}

func TestJudgeRunsPairedRequestsInOrder(t *testing.T) {
	p := newCorsProber(t)
	if err := p.AddEndpoint(exactPolicy("app")); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	if _, err := p.Judge("app"); err != nil {
		t.Fatalf("judge: %v", err)
	}
	records := p.Records("app")
	if len(records) != 6 {
		t.Fatalf("records = %d, want 6, one preflight and one actual for three origin variants", len(records))
	}
	for pair := 0; pair < 3; pair++ {
		first := records[pair*2]
		second := records[pair*2+1]
		if !first.Preflight || second.Preflight {
			t.Fatalf("pair %d = preflight %v / %v, want preflight before actual", pair, first.Preflight, second.Preflight)
		}
		if first.Origin != second.Origin {
			t.Fatalf("pair %d origins = %q and %q, want the same origin", pair, first.Origin, second.Origin)
		}
		if second.Seq <= first.Seq {
			t.Fatalf("pair %d sequence = %d then %d, want increasing", pair, first.Seq, second.Seq)
		}
	}
	markers := make(map[string]bool, len(records))
	for _, record := range records {
		if record.Marker == "" {
			t.Fatalf("record %d has an empty marker", record.Seq)
		}
		if markers[record.Marker] {
			t.Fatalf("marker %q reused inside one judge round", record.Marker)
		}
		markers[record.Marker] = true
	}
	if released := p.RecycleMarkers("app"); released != 6 {
		t.Fatalf("released markers = %d, want 6", released)
	}
	if released := p.RecycleMarkers("app"); released != 0 {
		t.Fatalf("second recycle = %d, want 0 because markers are single use", released)
	}
}

func TestConcurrentProbesSerializedPerEndpoint(t *testing.T) {
	p := newCorsProber(t)
	if err := p.AddEndpoint(exactPolicy("app")); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := p.Build(impl.OriginExact, exactOrigin, false, false)
			if err != nil {
				return
			}
			_, _ = p.Probe("app", req)
		}()
	}
	wg.Wait()
	stats := p.Stats("app")
	if stats.PeakInFlight != 1 {
		t.Fatalf("per endpoint peak in flight = %d, want exactly 1", stats.PeakInFlight)
	}
	if stats.Probes != 16 {
		t.Fatalf("probes = %d, want 16", stats.Probes)
	}
	if global := p.GlobalStats(); global.Probes != 16 {
		t.Fatalf("global probes = %d, want 16", global.Probes)
	}
}

func TestDumpLoadKeepsRecords(t *testing.T) {
	p := newCorsProber(t)
	if err := p.AddEndpoint(exactPolicy("app")); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	if _, err := p.Judge("app"); err != nil {
		t.Fatalf("judge: %v", err)
	}
	data, err := p.Dump()
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	restored := newCorsProber(t)
	if err := restored.Load(data); err != nil {
		t.Fatalf("load: %v", err)
	}
	if records := restored.Records("app"); len(records) != 6 {
		t.Fatalf("restored records = %d, want 6", len(records))
	}
	if stats := restored.Stats("app"); stats.Probes != 6 {
		t.Fatalf("restored probes = %d, want 6", stats.Probes)
	}
	if endpoints := restored.Endpoints(); len(endpoints) != 1 || endpoints[0] != "app" {
		t.Fatalf("restored endpoints = %v, want [app]", endpoints)
	}
}

func TestLoadRejectsCorruptSnapshot(t *testing.T) {
	p := newCorsProber(t)
	if err := p.AddEndpoint(exactPolicy("app")); err != nil {
		t.Fatalf("add endpoint: %v", err)
	}
	if err := p.Load([]byte(`{"policies":[{"name":`)); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("truncated snapshot must be refused with ErrBadSnapshot, got %v", err)
	}
	if endpoints := p.Endpoints(); len(endpoints) != 1 || endpoints[0] != "app" {
		t.Fatalf("a refused load must keep the live policies, got %v", endpoints)
	}
}
