package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-40"
)

type scriptedTransport struct {
	mu     sync.Mutex
	calls  int
	base   string
	mutate func(req impl.Request) string
}

func (s *scriptedTransport) Send(req impl.Request) (impl.Response, error) {
	s.mu.Lock()
	s.calls++
	mutate := s.mutate
	base := s.base
	s.mu.Unlock()
	body := base
	if mutate != nil {
		body = mutate(req)
	}
	return impl.Response{Status: 200, Body: body}, nil
}

func (s *scriptedTransport) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func testConfig() impl.ProberConfig {
	return impl.ProberConfig{MaxRequests: 32, BaselineSamples: 2, MaxSplits: 4}
}

func newMiner(t *testing.T, cfg impl.ProberConfig, tr impl.Transport) *impl.HiddenParamMiner {
	t.Helper()
	m := impl.NewHiddenParamMiner(cfg)
	if err := m.SetTransport(tr); err != nil {
		t.Fatalf("set transport must succeed, got %v", err)
	}
	return m
}

func searchEndpoint() impl.Endpoint {
	return impl.Endpoint{Name: "search", Method: "GET", Path: "/api/search",
		ContentType: "form", Params: []string{"page"}, Headers: map[string]string{"Accept": "text/html"}}
}

func TestBaselineCapturesShape(t *testing.T) {
	body := "id=1&name=alice&debug=off"
	tr := &scriptedTransport{base: body}
	m := newMiner(t, testConfig(), tr)
	base, err := m.Baseline(searchEndpoint())
	if err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	if base.Endpoint != "search" || base.Status != 200 {
		t.Fatalf("baseline identity = %q/%d, want search/200", base.Endpoint, base.Status)
	}
	if base.Length != len(body) {
		t.Fatalf("baseline length = %d, want %d", base.Length, len(body))
	}
	if len(base.Fields) != 3 || base.Fields[0] != "debug" || base.Fields[1] != "id" || base.Fields[2] != "name" {
		t.Fatalf("baseline fields = %v, want [debug id name]", base.Fields)
	}
	if base.Fingerprint == "" {
		t.Fatalf("baseline fingerprint must not be empty")
	}
	if base.Samples != 2 {
		t.Fatalf("baseline samples = %d, want 2", base.Samples)
	}
	if got := tr.count(); got != 2 {
		t.Fatalf("transport calls = %d, want 2 baseline samples", got)
	}
}

func TestBaselineRejectsBadEndpoint(t *testing.T) {
	m := newMiner(t, testConfig(), &scriptedTransport{base: "id=1"})
	if _, err := m.Baseline(impl.Endpoint{Path: "/api/search"}); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("baseline without a name error = %v, want %v", err, impl.ErrBadEndpoint)
	}
	if _, err := m.Baseline(impl.Endpoint{Name: "search"}); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("baseline without a path error = %v, want %v", err, impl.ErrBadEndpoint)
	}
	bare := impl.NewHiddenParamMiner(testConfig())
	if _, err := bare.Baseline(searchEndpoint()); errors.Is(err, nil) {
		t.Fatalf("baseline without a transport must fail")
	}
}

func TestCandidatesOrderedBySource(t *testing.T) {
	m := newMiner(t, testConfig(), &scriptedTransport{base: "id=1"})
	ep := searchEndpoint()
	ep.Body = `<input type="text" name="token" value="">`
	cands, err := m.Candidates(ep, []string{"debug", "admin", "DEBUG"})
	if err != nil {
		t.Fatalf("candidates must succeed, got %v", err)
	}
	wantNames := []string{"admin", "debug", "page", "token"}
	wantSources := []string{impl.SourceSeed, impl.SourceSeed, impl.SourceExisting, impl.SourcePage}
	if len(cands) != len(wantNames) {
		t.Fatalf("candidate count = %d, want %d (%v)", len(cands), len(wantNames), cands)
	}
	for i := range wantNames {
		if cands[i].Name != wantNames[i] || cands[i].Source != wantSources[i] {
			t.Fatalf("candidate %d = %q/%q, want %q/%q", i, cands[i].Name, cands[i].Source, wantNames[i], wantSources[i])
		}
	}
}

func TestCandidatesRejectsEndpointWithoutInputs(t *testing.T) {
	m := newMiner(t, testConfig(), &scriptedTransport{base: "id=1"})
	if _, err := m.Candidates(impl.Endpoint{Path: "/api/search"}, []string{"debug"}); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("candidates without a name error = %v, want %v", err, impl.ErrBadEndpoint)
	}
	bare := impl.Endpoint{Name: "search", Path: "/api/search"}
	if _, err := m.Candidates(bare, nil); !errors.Is(err, impl.ErrNoCandidates) {
		t.Fatalf("candidates without any input error = %v, want %v", err, impl.ErrNoCandidates)
	}
}

func TestProbeAttributesReflection(t *testing.T) {
	base := "id=1&name=alice"
	tr := &scriptedTransport{base: base, mutate: func(req impl.Request) string {
		for _, p := range req.Params {
			if p.Name == "q" {
				return base + "&q=" + p.Value
			}
		}
		return base
	}}
	m := newMiner(t, testConfig(), tr)
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	batch := []impl.ParamCandidate{{Name: "q", Source: impl.SourceSeed}, {Name: "zzz", Source: impl.SourceSeed}}
	obs, err := m.Probe(searchEndpoint(), batch)
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("observation count = %d, want 2 in input order", len(obs))
	}
	if obs[0].Name != "q" || obs[1].Name != "zzz" {
		t.Fatalf("observation order = %q,%q, want q,zzz", obs[0].Name, obs[1].Name)
	}
	if !obs[0].Reflected || obs[0].Location != "body" {
		t.Fatalf("reflected observation = %+v, want reflected in the body", obs[0])
	}
	if !obs[0].Diff || obs[0].Evidence != impl.EvidenceReflected {
		t.Fatalf("reflected evidence = %q diff=%v, want %q", obs[0].Evidence, obs[0].Diff, impl.EvidenceReflected)
	}
	if obs[0].Sentinel == "" {
		t.Fatalf("reflected observation must carry the sentinel that was injected")
	}
	if obs[1].Diff || obs[1].Evidence != impl.EvidenceNone {
		t.Fatalf("inert observation = %+v, want no difference", obs[1])
	}
	if obs[1].Length != len(base) {
		t.Fatalf("inert observation length = %d, want %d", obs[1].Length, len(base))
	}
}

func TestProbeIsolatesSingleChangedParameter(t *testing.T) {
	base := "id=1&name=alice"
	tr := &scriptedTransport{base: base, mutate: func(req impl.Request) string {
		for _, p := range req.Params {
			if p.Name == "debug" {
				return base + "&debug=on&trace=1"
			}
		}
		return base
	}}
	m := newMiner(t, testConfig(), tr)
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	batch := []impl.ParamCandidate{
		{Name: "alpha", Source: impl.SourceSeed},
		{Name: "beta", Source: impl.SourceSeed},
		{Name: "debug", Source: impl.SourceSeed},
		{Name: "gamma", Source: impl.SourceSeed},
	}
	obs, err := m.Probe(searchEndpoint(), batch)
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	if len(obs) != 4 {
		t.Fatalf("observation count = %d, want 4", len(obs))
	}
	for i, o := range obs {
		if o.Name != batch[i].Name {
			t.Fatalf("observation %d = %q, want %q", i, o.Name, batch[i].Name)
		}
	}
	if !obs[2].Diff {
		t.Fatalf("debug must be reported as the changed parameter: %+v", obs[2])
	}
	if obs[2].Evidence != impl.EvidenceFields && obs[2].Evidence != impl.EvidenceLength {
		t.Fatalf("debug evidence = %q, want fields or length", obs[2].Evidence)
	}
	for _, i := range []int{0, 1, 3} {
		if obs[i].Diff {
			t.Fatalf("parameter %q was blamed for a change it did not cause: %+v", obs[i].Name, obs[i])
		}
	}
	if got := tr.count(); got < 4 {
		t.Fatalf("transport calls = %d, want the batch plus at least three split rounds", got)
	}
}

func TestProbeRejectsEmptyBatch(t *testing.T) {
	tr := &scriptedTransport{base: "id=1"}
	m := newMiner(t, testConfig(), tr)
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	if _, err := m.Probe(searchEndpoint(), nil); !errors.Is(err, impl.ErrEmptyBatch) {
		t.Fatalf("empty batch error = %v, want %v", err, impl.ErrEmptyBatch)
	}
	if got := tr.count(); got != 2 {
		t.Fatalf("transport calls = %d, want only the two baseline samples", got)
	}
}

func TestProbeFailsAtomicallyWhenBudgetRunsOut(t *testing.T) {
	base := "id=1&name=alice"
	cfg := testConfig()
	cfg.MaxRequests = 4
	tr := &scriptedTransport{base: base, mutate: func(req impl.Request) string {
		for _, p := range req.Params {
			if p.Name == "debug" {
				return base + "&debug=on&trace=1"
			}
		}
		return base
	}}
	m := newMiner(t, cfg, tr)
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	batch := []impl.ParamCandidate{
		{Name: "alpha", Source: impl.SourceSeed},
		{Name: "beta", Source: impl.SourceSeed},
		{Name: "debug", Source: impl.SourceSeed},
		{Name: "gamma", Source: impl.SourceSeed},
	}
	if _, err := m.Probe(searchEndpoint(), batch); !errors.Is(err, impl.ErrBudgetExhausted) {
		t.Fatalf("probe over budget error = %v, want %v", err, impl.ErrBudgetExhausted)
	}
	if got := len(m.Observations()); got != 0 {
		t.Fatalf("recorded observations = %d, want none after an aborted probe", got)
	}
	budget, err := m.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 4 || budget.Remaining != 0 {
		t.Fatalf("budget = %+v, want used 4 remaining 0", budget)
	}
}

func TestJudgeReportsReflectedFinding(t *testing.T) {
	base := "id=1&name=alice"
	m := newMiner(t, testConfig(), &scriptedTransport{base: base})
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	obs := []impl.ParamObservation{{Name: "q", Source: impl.SourceSeed, Status: 200,
		Length: len(base) + 12, Reflected: true, Location: "body", Diff: true,
		Evidence: impl.EvidenceReflected, Sentinel: "probe-q-1"}}
	findings, err := m.Judge(obs)
	if err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("finding count = %d, want 1", len(findings))
	}
	if findings[0].Name != "q" || findings[0].Source != impl.SourceSeed {
		t.Fatalf("finding = %+v, want q from the seed list", findings[0])
	}
	if findings[0].Evidence != impl.EvidenceReflected {
		t.Fatalf("finding evidence = %q, want %q", findings[0].Evidence, impl.EvidenceReflected)
	}
	if got := len(m.Confirmed()); got != 1 {
		t.Fatalf("confirmed findings = %d, want 1", got)
	}
}

func TestJudgeReportsLengthAndFieldFindings(t *testing.T) {
	base := "id=1&name=alice"
	m := newMiner(t, testConfig(), &scriptedTransport{base: base})
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	obs := []impl.ParamObservation{
		{Name: "zone", Source: impl.SourceSeed, Status: 200, Length: len(base) + 5,
			Diff: true, Evidence: impl.EvidenceLength},
		{Name: "admin", Source: impl.SourcePage, Status: 200, Length: len(base),
			Fields: []string{"admin", "id", "name"}, Diff: true, Evidence: impl.EvidenceFields},
	}
	findings, err := m.Judge(obs)
	if err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("finding count = %d, want 2", len(findings))
	}
	if findings[0].Name != "admin" || findings[0].Evidence != impl.EvidenceFields {
		t.Fatalf("first finding = %+v, want admin with field evidence in name order", findings[0])
	}
	if findings[1].Name != "zone" || findings[1].Evidence != impl.EvidenceLength {
		t.Fatalf("second finding = %+v, want zone with length evidence", findings[1])
	}
	if findings[1].Delta != 5 {
		t.Fatalf("length finding delta = %d, want 5", findings[1].Delta)
	}
	if findings[0].Source != impl.SourcePage {
		t.Fatalf("page finding source = %q, want %q", findings[0].Source, impl.SourcePage)
	}
}

func TestJudgeSkipsUnchangedObservations(t *testing.T) {
	m := newMiner(t, testConfig(), &scriptedTransport{base: "id=1"})
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	obs := []impl.ParamObservation{
		{Name: "a", Source: impl.SourceSeed, Status: 200, Length: 4, Evidence: impl.EvidenceNone},
		{Name: "b", Source: impl.SourceSeed, Status: 200, Length: 4, Evidence: impl.EvidenceNone},
	}
	findings, err := m.Judge(obs)
	if err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none for unchanged observations", findings)
	}
	if got := len(m.Confirmed()); got != 0 {
		t.Fatalf("confirmed findings = %d, want 0", got)
	}
}

func TestJudgeRequiresBaselineAndObservations(t *testing.T) {
	m := newMiner(t, testConfig(), &scriptedTransport{base: "id=1"})
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	if _, err := m.Judge(nil); !errors.Is(err, impl.ErrNoObservations) {
		t.Fatalf("judge on empty observations error = %v, want %v", err, impl.ErrNoObservations)
	}
	fresh := newMiner(t, testConfig(), &scriptedTransport{base: "id=1"})
	obs := []impl.ParamObservation{{Name: "q", Source: impl.SourceSeed, Reflected: true,
		Diff: true, Evidence: impl.EvidenceReflected}}
	if _, err := fresh.Judge(obs); !errors.Is(err, impl.ErrNoBaseline) {
		t.Fatalf("judge without a baseline error = %v, want %v", err, impl.ErrNoBaseline)
	}
}

func TestBudgetStateTracksUsage(t *testing.T) {
	tr := &scriptedTransport{base: "id=1&name=alice"}
	m := newMiner(t, testConfig(), tr)
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	budget, err := m.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 2 || budget.Limit != 32 || budget.Remaining != 30 {
		t.Fatalf("budget = %+v, want used 2 limit 32 remaining 30", budget)
	}
	batch := []impl.ParamCandidate{{Name: "q", Source: impl.SourceSeed}}
	if _, err := m.Probe(searchEndpoint(), batch); err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	after, err := m.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if after.Used <= 2 {
		t.Fatalf("budget used after probing = %d, want more than the baseline cost", after.Used)
	}
	if after.Remaining != after.Limit-after.Used {
		t.Fatalf("budget remaining = %d, want limit-used = %d", after.Remaining, after.Limit-after.Used)
	}
}

func TestSnapshotRestoreKeepsConfirmedParameters(t *testing.T) {
	base := "id=1&name=alice"
	cfg := testConfig()
	tr := &scriptedTransport{base: base, mutate: func(req impl.Request) string {
		for _, p := range req.Params {
			if p.Name == "q" {
				return base + "&q=" + p.Value
			}
		}
		return base
	}}
	m := newMiner(t, cfg, tr)
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	batch := []impl.ParamCandidate{{Name: "q", Source: impl.SourceSeed}}
	obs, err := m.Probe(searchEndpoint(), batch)
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	if _, err := m.Judge(obs); err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	snap, err := m.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot len = %d err = %v, want a payload", len(snap), err)
	}
	callsBefore := tr.count()
	restored := impl.NewHiddenParamMiner(cfg)
	quiet := &scriptedTransport{base: "unexpected"}
	if err := restored.SetTransport(quiet); err != nil {
		t.Fatalf("set transport must succeed, got %v", err)
	}
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if got := len(restored.Confirmed()); got != 1 {
		t.Fatalf("restored confirmed findings = %d, want 1", got)
	}
	again, err := restored.Probe(searchEndpoint(), batch)
	if err != nil {
		t.Fatalf("probe after restore must succeed, got %v", err)
	}
	if len(again) != 1 || again[0].Name != "q" || !again[0].Reflected {
		t.Fatalf("cached observation = %+v, want the confirmed reflection of q", again)
	}
	if got := quiet.count(); got != 0 {
		t.Fatalf("confirmed parameter was probed again: %d transport calls", got)
	}
	if callsBefore < 3 {
		t.Fatalf("original transport calls = %d, want the baseline plus the batch", callsBefore)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	m := newMiner(t, testConfig(), &scriptedTransport{base: "id=1&name=alice"})
	if _, err := m.Baseline(searchEndpoint()); err != nil {
		t.Fatalf("baseline must succeed, got %v", err)
	}
	if err := m.Restore([]byte("garbage-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want %v", err, impl.ErrBadSnapshot)
	}
	budget, err := m.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 2 {
		t.Fatalf("budget used after corrupt restore = %d, want 2", budget.Used)
	}
	if got := len(m.Observations()); got != 0 {
		t.Fatalf("observations after corrupt restore = %d, want 0", got)
	}
}
