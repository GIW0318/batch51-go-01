package task

import (
	"errors"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-25"
)

func newHostProber(t *testing.T) *impl.HostHeaderPoisonProber {
	t.Helper()
	return impl.NewHostHeaderPoisonProber(impl.ProberConfig{
		MaxConcurrent:        2,
		MaxProbesPerEndpoint: 200,
	})
}

func hostPolicy(name string) impl.HostPolicy {
	return impl.HostPolicy{
		Name:         name,
		FrontSource:  impl.SourceHost,
		BackSource:   impl.SourceHost,
		CacheSource:  impl.SourceHost,
		RedirectPath: "/portal",
		LinkPath:     "/assets/app.js",
	}
}

func TestBuildHostSourceRequest(t *testing.T) {
	p := newHostProber(t)
	req, err := p.Build(impl.SourceHost, "evil.test")
	if err != nil {
		t.Fatalf("build host request: %v", err)
	}
	if req.Source != impl.SourceHost || req.Host != "evil.test" {
		t.Fatalf("request identity mismatch: source=%q host=%q", req.Source, req.Host)
	}
	if req.HostHeader != "evil.test" {
		t.Fatalf("host header = %q, want evil.test", req.HostHeader)
	}
	if req.Forwarded != "" || req.RequestLine != "" {
		t.Fatalf("only the direct host source must be populated, got forwarded=%q line=%q", req.Forwarded, req.RequestLine)
	}
	if req.Marker == "" {
		t.Fatalf("every probe request must carry a recyclable marker")
	}
}

func TestBuildForwardedSourceRequest(t *testing.T) {
	p := newHostProber(t)
	req, err := p.Build(impl.SourceForwarded, "evil.test")
	if err != nil {
		t.Fatalf("build forwarded request: %v", err)
	}
	if req.Source != impl.SourceForwarded || req.Forwarded != "evil.test" {
		t.Fatalf("forwarded value = %q source = %q, want evil.test via %q", req.Forwarded, req.Source, impl.SourceForwarded)
	}
	if req.HostHeader != "" {
		t.Fatalf("a forwarded probe must not fill the direct host header, got %q", req.HostHeader)
	}
}

func TestBuildRequestLineSource(t *testing.T) {
	p := newHostProber(t)
	req, err := p.Build(impl.SourceRequestLine, "evil.test")
	if err != nil {
		t.Fatalf("build absolute line request: %v", err)
	}
	if req.Source != impl.SourceRequestLine {
		t.Fatalf("source = %q, want %q", req.Source, impl.SourceRequestLine)
	}
	if !strings.HasPrefix(req.RequestLine, "GET ") || !strings.Contains(req.RequestLine, "http://evil.test") {
		t.Fatalf("request line = %q, want an absolute GET line carrying http://evil.test", req.RequestLine)
	}
	if req.HostHeader != "" || req.Forwarded != "" {
		t.Fatalf("an absolute line probe must not fill the header sources, got %q / %q", req.HostHeader, req.Forwarded)
	}
}

func TestBuildRejectsBadInputs(t *testing.T) {
	p := newHostProber(t)
	if _, err := p.Build(impl.SourceHost, "   "); !errors.Is(err, impl.ErrBadHost) {
		t.Fatalf("blank host must be rejected with ErrBadHost, got %v", err)
	}
	if _, err := p.Build("blind", "evil.test"); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("unknown source must be rejected with ErrBadSource, got %v", err)
	}
}

func TestAddEntrypointRejectsBlankAndDuplicate(t *testing.T) {
	p := newHostProber(t)
	blank := hostPolicy("   ")
	if err := p.AddEntrypoint(blank); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("blank entrypoint name must be rejected with ErrBadEndpoint, got %v", err)
	}
	if err := p.AddEntrypoint(hostPolicy("edge")); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	if err := p.AddEntrypoint(hostPolicy("edge")); !errors.Is(err, impl.ErrDuplicateEndpoint) {
		t.Fatalf("duplicate entrypoint must be rejected with ErrDuplicateEndpoint, got %v", err)
	}
	if names := p.Entrypoints(); len(names) != 1 || names[0] != "edge" {
		t.Fatalf("entrypoints = %v, want exactly [edge]", names)
	}
}

func TestProbeRejectsUnknownEndpoint(t *testing.T) {
	p := newHostProber(t)
	req, err := p.Build(impl.SourceHost, "evil.test")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if _, err := p.Probe("missing", req); !errors.Is(err, impl.ErrUnknownEndpoint) {
		t.Fatalf("unknown entrypoint must be rejected with ErrUnknownEndpoint, got %v", err)
	}
}

func TestProbeHostSourceDrivesRedirect(t *testing.T) {
	p := newHostProber(t)
	if err := p.AddEntrypoint(hostPolicy("edge")); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	req, err := p.Build(impl.SourceHost, "evil.test")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	observation, err := p.Probe("edge", req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if observation.Status != 302 {
		t.Fatalf("status = %d, want 302", observation.Status)
	}
	if observation.Location != "https://evil.test/portal" {
		t.Fatalf("location = %q, want the injected host in the redirect target", observation.Location)
	}
	if !observation.Accepted || observation.UsedSource != impl.SourceHost || observation.Layer != impl.LayerFrontend {
		t.Fatalf("observation = %+v, want the frontend to take the host value", observation)
	}
	if observation.Marker != req.Marker {
		t.Fatalf("observation marker = %q, want %q", observation.Marker, req.Marker)
	}
}

func TestProbeForwardedSourceDrivesLink(t *testing.T) {
	p := newHostProber(t)
	policy := hostPolicy("edge")
	policy.BackSource = impl.SourceForwarded
	if err := p.AddEntrypoint(policy); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	req, err := p.Build(impl.SourceForwarded, "evil.test")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	observation, err := p.Probe("edge", req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if observation.Link != "https://evil.test/assets/app.js" {
		t.Fatalf("link = %q, want the injected host in the generated link", observation.Link)
	}
	if observation.Location != "https://app.internal/portal" {
		t.Fatalf("location = %q, want the frontend fallback because only the backend reads the forwarded source", observation.Location)
	}
	if !observation.Accepted || observation.Layer != impl.LayerBackend {
		t.Fatalf("observation = %+v, want the backend layer flagged", observation)
	}
}

func TestProbeRequestLineDrivesCacheKey(t *testing.T) {
	p := newHostProber(t)
	policy := hostPolicy("edge")
	policy.CacheSource = impl.SourceRequestLine
	if err := p.AddEntrypoint(policy); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	req, err := p.Build(impl.SourceRequestLine, "evil.test")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	observation, err := p.Probe("edge", req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if observation.CacheKey != "v1:evil.test" {
		t.Fatalf("cache key = %q, want v1:evil.test", observation.CacheKey)
	}
	if !observation.Accepted || observation.Layer != impl.LayerCache {
		t.Fatalf("observation = %+v, want the cache layer flagged", observation)
	}
	if observation.Location != "https://app.internal/portal" || observation.Link != "https://app.internal/assets/app.js" {
		t.Fatalf("only the cache layer reads the absolute line, got location=%q link=%q", observation.Location, observation.Link)
	}
}

func TestProbeIgnoresUnconfiguredSource(t *testing.T) {
	p := newHostProber(t)
	if err := p.AddEntrypoint(hostPolicy("edge")); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	req, err := p.Build(impl.SourceForwarded, "evil.test")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	observation, err := p.Probe("edge", req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if observation.Accepted || observation.UsedSource != "" || observation.Layer != "" {
		t.Fatalf("observation = %+v, want the forwarded value ignored by a host only policy", observation)
	}
	if observation.Location != "https://app.internal/portal" || observation.Link != "https://app.internal/assets/app.js" {
		t.Fatalf("location=%q link=%q, want both layers on the fallback host", observation.Location, observation.Link)
	}
	if stats := p.Stats("edge"); stats.Accepted != 0 || stats.Rejected != 1 {
		t.Fatalf("stats accepted=%d rejected=%d, want 0 and 1", stats.Accepted, stats.Rejected)
	}
}

func TestJudgeReportsEffectiveSource(t *testing.T) {
	p := newHostProber(t)
	if err := p.AddEntrypoint(hostPolicy("edge")); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	verdict, err := p.Judge("edge")
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if verdict.EffectiveSource != impl.SourceHost {
		t.Fatalf("effective source = %q, want %q", verdict.EffectiveSource, impl.SourceHost)
	}
	if verdict.Layer != impl.LayerFrontend {
		t.Fatalf("effective layer = %q, want %q", verdict.Layer, impl.LayerFrontend)
	}
	if !verdict.Poisoned {
		t.Fatalf("a policy that reads the direct host must be graded as poisoned, got %+v", verdict)
	}
	if verdict.Endpoint != "edge" || verdict.Seq <= 0 {
		t.Fatalf("verdict identity mismatch: %+v", verdict)
	}
	results := p.Sources("edge")
	if len(results) != 3 {
		t.Fatalf("source results = %d, want 3", len(results))
	}
	if results[0].Source != impl.SourceHost || !results[0].Accepted {
		t.Fatalf("first source result = %+v, want an accepted direct host probe", results[0])
	}
	if results[1].Source != impl.SourceForwarded || results[1].Accepted {
		t.Fatalf("second source result = %+v, want an ignored forwarded probe", results[1])
	}
	if results[2].Source != impl.SourceRequestLine || results[2].Accepted {
		t.Fatalf("third source result = %+v, want an ignored absolute line probe", results[2])
	}
}

func TestJudgeRecordsThreeSourcesInOrder(t *testing.T) {
	p := newHostProber(t)
	if err := p.AddEntrypoint(hostPolicy("edge")); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	if _, err := p.Judge("edge"); err != nil {
		t.Fatalf("judge: %v", err)
	}
	records := p.Records("edge")
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3, one per request source", len(records))
	}
	want := []string{impl.SourceHost, impl.SourceForwarded, impl.SourceRequestLine}
	for i, source := range want {
		if records[i].Source != source {
			t.Fatalf("record %d source = %q, want %q in the fixed source order", i, records[i].Source, source)
		}
		if i > 0 && records[i].Seq <= records[i-1].Seq {
			t.Fatalf("record %d sequence %d, want increasing order", i, records[i].Seq)
		}
		if records[i].Value != "evil.test" {
			t.Fatalf("record %d value = %q, want the injected host", i, records[i].Value)
		}
	}
	markers := make(map[string]bool, len(records))
	for _, record := range records {
		if record.Marker == "" || !strings.HasPrefix(record.Marker, impl.MarkerPrefix) {
			t.Fatalf("record %d marker = %q, want a %s prefixed marker", record.Seq, record.Marker, impl.MarkerPrefix)
		}
		if markers[record.Marker] {
			t.Fatalf("marker %q reused inside one judge round", record.Marker)
		}
		markers[record.Marker] = true
	}
	if released := p.RecycleMarkers("edge"); released != 3 {
		t.Fatalf("released markers = %d, want 3", released)
	}
	if released := p.RecycleMarkers("edge"); released != 0 {
		t.Fatalf("second recycle = %d, want 0 because markers are single use", released)
	}
	if stats := p.Stats("edge"); stats.Probes != 3 {
		t.Fatalf("probes = %d, want 3", stats.Probes)
	}
}

func TestConcurrentProbesSerializedPerEndpoint(t *testing.T) {
	p := newHostProber(t)
	if err := p.AddEntrypoint(hostPolicy("edge")); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := p.Build(impl.SourceHost, "evil.test")
			if err != nil {
				return
			}
			_, _ = p.Probe("edge", req)
		}()
	}
	wg.Wait()
	stats := p.Stats("edge")
	if stats.PeakInFlight != 1 {
		t.Fatalf("per entrypoint peak in flight = %d, want exactly 1", stats.PeakInFlight)
	}
	if stats.Probes != 16 {
		t.Fatalf("probes = %d, want 16", stats.Probes)
	}
}

func TestSaveReloadKeepsVerdict(t *testing.T) {
	p := newHostProber(t)
	if err := p.AddEntrypoint(hostPolicy("edge")); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	if _, err := p.Judge("edge"); err != nil {
		t.Fatalf("judge: %v", err)
	}
	data, err := p.Save()
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	restored := newHostProber(t)
	if err := restored.Reload(data); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if records := restored.Records("edge"); len(records) != 3 {
		t.Fatalf("restored records = %d, want 3", len(records))
	}
	if stats := restored.Stats("edge"); stats.Probes != 3 {
		t.Fatalf("restored probes = %d, want 3", stats.Probes)
	}
	if names := restored.Entrypoints(); len(names) != 1 || names[0] != "edge" {
		t.Fatalf("restored entrypoints = %v, want [edge]", names)
	}
	if results := restored.Sources("edge"); len(results) != 3 {
		t.Fatalf("restored source results = %d, want 3", len(results))
	}
}

func TestReloadRejectsCorruptSnapshot(t *testing.T) {
	p := newHostProber(t)
	if err := p.AddEntrypoint(hostPolicy("edge")); err != nil {
		t.Fatalf("add entrypoint: %v", err)
	}
	if err := p.Reload([]byte(`{"policies":[{"name":`)); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("truncated snapshot must be refused with ErrBadSnapshot, got %v", err)
	}
	if names := p.Entrypoints(); len(names) != 1 || names[0] != "edge" {
		t.Fatalf("a refused reload must keep the live policies, got %v", names)
	}
}
