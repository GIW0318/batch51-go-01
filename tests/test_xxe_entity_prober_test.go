package task

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-22"
)

func newEntityProber(t *testing.T) *impl.XxeEntityProber {
	t.Helper()
	return impl.NewXxeEntityProber(impl.ProberConfig{
		MaxConcurrent:        2,
		MaxProbesPerEndpoint: 200,
		CallbackWindow:       100,
		MaxCallbacks:         128,
	})
}

func TestBuildInternalEntityPayload(t *testing.T) {
	p := newEntityProber(t)
	payload, err := p.BuildPayload(impl.KindInternal, "probe-in-1")
	if err != nil {
		t.Fatalf("build internal payload: %v", err)
	}
	if payload.Kind != impl.KindInternal || payload.ProbeID != "probe-in-1" {
		t.Fatalf("payload identity mismatch: kind=%q id=%q", payload.Kind, payload.ProbeID)
	}
	if !strings.Contains(payload.Body, "<!ENTITY probe-in-1") {
		t.Fatalf("internal payload must declare the entity, body=%q", payload.Body)
	}
	if !strings.Contains(payload.Body, "&probe-in-1;") {
		t.Fatalf("internal payload must reference the entity, body=%q", payload.Body)
	}
}

func TestBuildExternalEntityPayloadEmbedsProbeID(t *testing.T) {
	p := newEntityProber(t)
	payload, err := p.BuildPayload(impl.KindExternal, "probe-ex-1")
	if err != nil {
		t.Fatalf("build external payload: %v", err)
	}
	if payload.Kind != impl.KindExternal {
		t.Fatalf("payload kind = %q, want %q", payload.Kind, impl.KindExternal)
	}
	if !strings.Contains(payload.Body, "SYSTEM") {
		t.Fatalf("external payload must declare a SYSTEM entity, body=%q", payload.Body)
	}
	if !strings.Contains(payload.Body, "http://collector.gsb/probe-ex-1") {
		t.Fatalf("external payload must embed the probe id in the callback url, body=%q", payload.Body)
	}
	if !strings.Contains(payload.Body, "&probe-ex-1;") {
		t.Fatalf("external payload must reference the entity, body=%q", payload.Body)
	}
}

func TestBuildPayloadRejectsBadInputs(t *testing.T) {
	p := newEntityProber(t)
	if _, err := p.BuildPayload(impl.KindInternal, "   "); !errors.Is(err, impl.ErrBadProbeID) {
		t.Fatalf("blank probe id must be rejected with ErrBadProbeID, got %v", err)
	}
	if _, err := p.BuildPayload("blind", "probe-1"); !errors.Is(err, impl.ErrBadPayloadKind) {
		t.Fatalf("unknown kind must be rejected with ErrBadPayloadKind, got %v", err)
	}
}

func TestRegisterEndpointRejectsBlankAndDuplicate(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "   "}); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("blank endpoint name must be rejected with ErrBadEndpoint, got %v", err)
	}
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "echo", ExpandsGeneral: true, MaxBodyBytes: 4096}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "echo", ExpandsGeneral: true}); !errors.Is(err, impl.ErrDuplicateEndpoint) {
		t.Fatalf("duplicate endpoint must be rejected with ErrDuplicateEndpoint, got %v", err)
	}
	if endpoints := p.Endpoints(); len(endpoints) != 1 || endpoints[0] != "echo" {
		t.Fatalf("endpoints = %v, want exactly [echo]", endpoints)
	}
}

func TestSubmitRejectsUnknownEndpoint(t *testing.T) {
	p := newEntityProber(t)
	payload, err := p.BuildPayload(impl.KindInternal, "probe-1")
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if _, err := p.Submit("missing", payload); !errors.Is(err, impl.ErrUnknownEndpoint) {
		t.Fatalf("unknown endpoint must be rejected with ErrUnknownEndpoint, got %v", err)
	}
}

func TestSubmitRejectsPayloadWithoutDoctype(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "echo", ExpandsGeneral: true, MaxBodyBytes: 4096}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	broken := impl.Payload{ProbeID: "probe-raw-1", Kind: impl.KindInternal, Body: "<r>&probe-raw-1;</r>"}
	if _, err := p.Submit("echo", broken); !errors.Is(err, impl.ErrBadPayload) {
		t.Fatalf("payload without doctype must be rejected with ErrBadPayload, got %v", err)
	}
	if records := p.Probes("echo"); len(records) != 0 {
		t.Fatalf("a refused payload must not leave a probe record, got %d", len(records))
	}
}

func TestInternalEntityEchoConfirmsProbe(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "echo", ExpandsGeneral: true, MaxBodyBytes: 4096}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	payload, err := p.BuildPayload(impl.KindInternal, "probe-echo-1")
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	resp, err := p.Submit("echo", payload)
	if err != nil {
		t.Fatalf("submit payload: %v", err)
	}
	if resp.Status != 200 || resp.Rejected {
		t.Fatalf("response status=%d rejected=%v, want an accepted 200", resp.Status, resp.Rejected)
	}
	if !strings.Contains(resp.Body, impl.EchoMarker+"probe-echo-1") {
		t.Fatalf("expanded body must echo %q, body=%q", impl.EchoMarker+"probe-echo-1", resp.Body)
	}
	records := p.Probes("echo")
	if len(records) != 1 || records[0].Status != impl.StatusConfirmed || records[0].Channel != impl.ChannelEcho {
		t.Fatalf("probe record = %+v, want one confirmed echo record", records)
	}
	if !records[0].Echoed {
		t.Fatalf("probe record must be marked as echoed")
	}
	correlations := p.Correlate("echo")
	if len(correlations) != 1 || !correlations[0].Confirmed || correlations[0].Channel != impl.ChannelEcho {
		t.Fatalf("correlation = %+v, want one confirmed echo correlation", correlations)
	}
}

func TestInternalEntityWithoutExpansionNotConfirmed(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "plain", MaxBodyBytes: 4096}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	finding, err := p.Probe("plain", impl.KindInternal)
	if err != nil {
		t.Fatalf("probe endpoint: %v", err)
	}
	if finding.Confirmed {
		t.Fatalf("a parser that does not expand entities must not be confirmed, got %+v", finding)
	}
	if finding.Reason != impl.ReasonEntityNotExpanded {
		t.Fatalf("finding reason = %q, want %q", finding.Reason, impl.ReasonEntityNotExpanded)
	}
	if finding.Status != impl.StatusPending {
		t.Fatalf("finding status = %q, want %q", finding.Status, impl.StatusPending)
	}
	if finding.Kind != impl.KindInternal || finding.Endpoint != "plain" {
		t.Fatalf("finding identity mismatch: %+v", finding)
	}
}

func TestExternalEntityCallbackConfirmsProbe(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{
		Name:             "resolve",
		ResolvesExternal: true,
		SourceAddr:       "198.51.100.9",
		MaxBodyBytes:     4096,
	}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	payload, err := p.BuildPayload(impl.KindExternal, "probe-oob-1")
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	resp, err := p.Submit("resolve", payload)
	if err != nil {
		t.Fatalf("submit payload: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("response status = %d, want 200", resp.Status)
	}
	callbacks := p.Callbacks("resolve")
	if len(callbacks) != 1 || callbacks[0].ProbeID != "probe-oob-1" || callbacks[0].SourceAddr != "198.51.100.9" {
		t.Fatalf("callbacks = %+v, want one hit for probe-oob-1 from 198.51.100.9", callbacks)
	}
	correlations := p.Correlate("resolve")
	if len(correlations) != 1 {
		t.Fatalf("correlations = %d, want 1", len(correlations))
	}
	got := correlations[0]
	if !got.Confirmed || got.Channel != impl.ChannelOOB || got.Callbacks != 1 || got.Late {
		t.Fatalf("correlation = %+v, want one confirmed out of band hit without a late mark", got)
	}
	if got.Status != impl.StatusConfirmed {
		t.Fatalf("correlation status = %q, want %q", got.Status, impl.StatusConfirmed)
	}
}

func TestLateCallbackBackfillsOriginalProbe(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "quiet", MaxBodyBytes: 4096}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	if now := p.Advance(10); now != 10 {
		t.Fatalf("clock after first advance = %d, want 10", now)
	}
	payload, err := p.BuildPayload(impl.KindExternal, "probe-late-1")
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if _, err := p.Submit("quiet", payload); err != nil {
		t.Fatalf("submit payload: %v", err)
	}
	if now := p.Advance(400); now != 410 {
		t.Fatalf("clock after second advance = %d, want 410", now)
	}
	if err := p.DeliverCallback(impl.Callback{
		ProbeID:    "probe-late-1",
		SourceAddr: "203.0.113.5",
		At:         410,
	}); err != nil {
		t.Fatalf("deliver late callback: %v", err)
	}
	correlations := p.Correlate("quiet")
	if len(correlations) != 1 {
		t.Fatalf("correlations = %d, want the late hit backfilled into the original probe", len(correlations))
	}
	got := correlations[0]
	if !got.Confirmed || got.Channel != impl.ChannelOOB {
		t.Fatalf("correlation = %+v, want a confirmed out of band hit", got)
	}
	if !got.Late {
		t.Fatalf("correlation must be marked late when the hit arrives past the callback window, got %+v", got)
	}
}

func TestConcurrentProbesIsolatedByProbeID(t *testing.T) {
	p := impl.NewXxeEntityProber(impl.ProberConfig{
		MaxConcurrent:        2,
		MaxProbesPerEndpoint: 200,
		CallbackWindow:       100,
		MaxCallbacks:         128,
	})
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "shared", MaxBodyBytes: 4096}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	const probes = 24
	var wg sync.WaitGroup
	for i := 0; i < probes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload, err := p.BuildPayload(impl.KindExternal, fmt.Sprintf("probe-c-%02d", i))
			if err != nil {
				return
			}
			_, _ = p.Submit("shared", payload)
		}(i)
	}
	wg.Wait()
	for i := 0; i < probes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = p.DeliverCallback(impl.Callback{
				ProbeID:    fmt.Sprintf("probe-c-%02d", i),
				SourceAddr: fmt.Sprintf("10.0.0.%d", i+1),
				At:         5,
			})
		}(i)
	}
	wg.Wait()
	correlations := p.Correlate("shared")
	if len(correlations) != probes {
		t.Fatalf("correlations = %d, want %d, one per probe id", len(correlations), probes)
	}
	seen := make(map[string]int, probes)
	for _, item := range correlations {
		if !item.Confirmed || item.Callbacks != 1 {
			t.Fatalf("correlation %q confirmed=%v callbacks=%d, want one hit each",
				item.ProbeID, item.Confirmed, item.Callbacks)
		}
		seen[item.ProbeID]++
	}
	if len(seen) != probes {
		t.Fatalf("distinct probe ids = %d, want %d", len(seen), probes)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("probe id %q correlated %d times, want once", id, count)
		}
	}
	stats := p.Stats("shared")
	if stats.Probes != probes || stats.Callbacks != probes {
		t.Fatalf("stats probes=%d callbacks=%d, want %d and %d", stats.Probes, stats.Callbacks, probes, probes)
	}
	if stats.PeakInFlight < 1 || stats.PeakInFlight > 2 {
		t.Fatalf("peak in flight = %d, want between 1 and the configured 2", stats.PeakInFlight)
	}
}

func TestExpiredProbeMarkedUncertain(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "quiet", MaxBodyBytes: 4096}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	p.Advance(5)
	payload, err := p.BuildPayload(impl.KindExternal, "probe-exp-1")
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if _, err := p.Submit("quiet", payload); err != nil {
		t.Fatalf("submit payload: %v", err)
	}
	p.Advance(200)
	if expired := p.Expire("quiet"); expired != 1 {
		t.Fatalf("expired probes = %d, want 1", expired)
	}
	records := p.Probes("quiet")
	if len(records) != 1 || records[0].Status != impl.StatusUncertain {
		t.Fatalf("probe records = %+v, want one uncertain record after the callback window", records)
	}
	correlations := p.Correlate("quiet")
	if len(correlations) != 1 {
		t.Fatalf("correlations = %d, want 1", len(correlations))
	}
	if correlations[0].Confirmed || correlations[0].Status != impl.StatusUncertain {
		t.Fatalf("correlation = %+v, want an uncertain outcome rather than a confirmed or absent one", correlations[0])
	}
}

func TestDuplicateCallbackKeepsSingleCorrelation(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{
		Name:             "resolve",
		ResolvesExternal: true,
		SourceAddr:       "198.51.100.9",
		MaxBodyBytes:     4096,
	}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	payload, err := p.BuildPayload(impl.KindExternal, "probe-dup-1")
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if _, err := p.Submit("resolve", payload); err != nil {
		t.Fatalf("submit payload: %v", err)
	}
	if err := p.DeliverCallback(impl.Callback{
		ProbeID:    "probe-dup-1",
		SourceAddr: "198.51.100.9",
		At:         3,
	}); err != nil {
		t.Fatalf("deliver duplicate callback: %v", err)
	}
	if callbacks := p.Callbacks("resolve"); len(callbacks) != 2 {
		t.Fatalf("callbacks = %d, want both hits retained", len(callbacks))
	}
	correlations := p.Correlate("resolve")
	if len(correlations) != 1 {
		t.Fatalf("correlations = %d, want a single correlation per probe id", len(correlations))
	}
	if correlations[0].Callbacks != 2 || !correlations[0].Confirmed {
		t.Fatalf("correlation = %+v, want two hits folded into one confirmed entry", correlations[0])
	}
}

func TestSnapshotLoadKeepsCallbacks(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{
		Name:             "resolve",
		ResolvesExternal: true,
		SourceAddr:       "198.51.100.9",
		MaxBodyBytes:     4096,
	}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	payload, err := p.BuildPayload(impl.KindExternal, "probe-snap-1")
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if _, err := p.Submit("resolve", payload); err != nil {
		t.Fatalf("submit payload: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newEntityProber(t)
	if err := restored.Load(data); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	records := restored.Probes("resolve")
	if len(records) != 1 || records[0].Status != impl.StatusConfirmed {
		t.Fatalf("restored records = %+v, want one confirmed probe", records)
	}
	correlations := restored.Correlate("resolve")
	if len(correlations) != 1 || !correlations[0].Confirmed || correlations[0].Channel != impl.ChannelOOB {
		t.Fatalf("restored correlations = %+v, want the out of band evidence preserved", correlations)
	}
}

func TestLoadRejectsCorruptSnapshot(t *testing.T) {
	p := newEntityProber(t)
	if err := p.RegisterEndpoint(impl.Endpoint{Name: "echo", ExpandsGeneral: true, MaxBodyBytes: 4096}); err != nil {
		t.Fatalf("register endpoint: %v", err)
	}
	if err := p.Load([]byte(`{"endpoints":[{"name":`)); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("truncated snapshot must be refused with ErrBadSnapshot, got %v", err)
	}
	if endpoints := p.Endpoints(); len(endpoints) != 1 || endpoints[0] != "echo" {
		t.Fatalf("a refused load must keep the live endpoints, got %v", endpoints)
	}
}
