package task

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-21"
)

func operatorDocs() []impl.Document {
	return []impl.Document{
		{ID: "u1", Fields: map[string]string{"name": "alice", "age": "20", "tenant": "t1"}},
		{ID: "u2", Fields: map[string]string{"name": "bob", "age": "30", "tenant": "t1"}},
		{ID: "u3", Fields: map[string]string{"name": "adam", "age": "40", "tenant": "t2"}},
	}
}

func newOperatorProber(t *testing.T) *impl.NoSqlOperatorProber {
	t.Helper()
	p, err := impl.NewNoSqlOperatorProber(impl.ProberConfig{
		MaxConcurrent:      2,
		MaxProbesPerTarget: 200,
		MaxRecords:         64,
	})
	if err != nil {
		t.Fatalf("build prober: %v", err)
	}
	return p
}

func TestGenerateBuildsGreaterThanPayload(t *testing.T) {
	p := newOperatorProber(t)
	payload, err := p.Generate("age", "25", impl.OpGreater)
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	if payload.Field != "age" || payload.Op != impl.OpGreater || payload.Value != "25" {
		t.Fatalf("payload identity mismatch: field=%q op=%q value=%q", payload.Field, payload.Op, payload.Value)
	}
	var body map[string]map[string]string
	if err := json.Unmarshal([]byte(payload.Body), &body); err != nil {
		t.Fatalf("payload body is not a nested json object: %v (body=%q)", err, payload.Body)
	}
	if got := body["age"][impl.OpGreater]; got != "25" {
		t.Fatalf("payload body age.$gt = %q, want 25 (body=%q)", got, payload.Body)
	}
}

func TestGenerateRejectsBadFieldAndOperator(t *testing.T) {
	p := newOperatorProber(t)
	if _, err := p.Generate("   ", "1", impl.OpGreater); !errors.Is(err, impl.ErrBadField) {
		t.Fatalf("blank field must be rejected with ErrBadField, got %v", err)
	}
	if _, err := p.Generate("age", "1", "$where"); !errors.Is(err, impl.ErrBadOperator) {
		t.Fatalf("unsupported operator must be rejected with ErrBadOperator, got %v", err)
	}
}

func TestRegisterTargetRejectsBlankAndDuplicate(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("   ", operatorDocs(), impl.ModeObject); !errors.Is(err, impl.ErrBadTarget) {
		t.Fatalf("blank target name must be rejected with ErrBadTarget, got %v", err)
	}
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); !errors.Is(err, impl.ErrDuplicateTarget) {
		t.Fatalf("duplicate target must be rejected with ErrDuplicateTarget, got %v", err)
	}
	if targets := p.Targets(); len(targets) != 1 || targets[0] != "users" {
		t.Fatalf("targets = %v, want exactly [users]", targets)
	}
}

func TestInjectRejectsUnknownTarget(t *testing.T) {
	p := newOperatorProber(t)
	payload, err := p.Generate("age", "25", impl.OpGreater)
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	if _, err := p.Inject("missing", payload); !errors.Is(err, impl.ErrUnknownTarget) {
		t.Fatalf("unknown target must be rejected with ErrUnknownTarget, got %v", err)
	}
}

func TestInjectGreaterThanMatchesDocuments(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	payload, err := p.Generate("age", "25", impl.OpGreater)
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	resp, err := p.Inject("users", payload)
	if err != nil {
		t.Fatalf("inject payload: %v", err)
	}
	if resp.Status != 200 || resp.Matched != 2 {
		t.Fatalf("status=%d matched=%d, want status 200 and 2 matches", resp.Status, resp.Matched)
	}
	if len(resp.DocIDs) != 2 || resp.DocIDs[0] != "u2" || resp.DocIDs[1] != "u3" {
		t.Fatalf("matched ids = %v, want [u2 u3] in document order", resp.DocIDs)
	}
}

func TestInjectRegexMatchesFieldValue(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	payload, err := p.Generate("name", "^ad", impl.OpRegex)
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	resp, err := p.Inject("users", payload)
	if err != nil {
		t.Fatalf("inject payload: %v", err)
	}
	if resp.Matched != 1 || len(resp.DocIDs) != 1 || resp.DocIDs[0] != "u3" {
		t.Fatalf("regex matched %d ids %v, want exactly [u3]", resp.Matched, resp.DocIDs)
	}
}

func TestInjectEqualityMatchesSingleDocument(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	payload, err := p.Generate("name", "bob", impl.OpEqual)
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	resp, err := p.Inject("users", payload)
	if err != nil {
		t.Fatalf("inject payload: %v", err)
	}
	if resp.Matched != 1 || len(resp.DocIDs) != 1 || resp.DocIDs[0] != "u2" {
		t.Fatalf("equality matched %d ids %v, want exactly [u2]", resp.Matched, resp.DocIDs)
	}
}

func TestVerdictMarksEvaluatedOperator(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	v, err := p.Verdict("users", "age")
	if err != nil {
		t.Fatalf("verdict: %v", err)
	}
	if !v.Injectable {
		t.Fatalf("operator injection must be graded as effective, got %+v", v)
	}
	if v.Reason != impl.ReasonOperatorEvaluated {
		t.Fatalf("verdict reason = %q, want %q", v.Reason, impl.ReasonOperatorEvaluated)
	}
	if v.Channel != impl.ChannelPair {
		t.Fatalf("verdict channel = %q, want %q", v.Channel, impl.ChannelPair)
	}
	if v.TrueHits != 3 || v.FalseHits != 0 || v.Baseline != 3 {
		t.Fatalf("true=%d false=%d baseline=%d, want 3/0/3", v.TrueHits, v.FalseHits, v.Baseline)
	}
	if v.Field != "age" || v.Seq <= 0 {
		t.Fatalf("verdict field=%q seq=%d, want field age and a positive sequence", v.Field, v.Seq)
	}
}

func TestVerdictRecordsBothPairProbes(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	if _, err := p.Verdict("users", "age"); err != nil {
		t.Fatalf("verdict: %v", err)
	}
	records := p.ProbeLog("users")
	if len(records) != 2 {
		t.Fatalf("probe records = %d, want exactly 2 for one true and one false pair", len(records))
	}
	if records[0].Pair != impl.PairTrue || records[1].Pair != impl.PairFalse {
		t.Fatalf("pair order = [%q %q], want [true false]", records[0].Pair, records[1].Pair)
	}
	if records[0].Matched != 3 || records[1].Matched != 0 {
		t.Fatalf("pair hits = [%d %d], want [3 0]", records[0].Matched, records[1].Matched)
	}
	if records[0].BaselineSeq == 0 || records[0].BaselineSeq != records[1].BaselineSeq {
		t.Fatalf("pair baseline sequences = [%d %d], want one shared positive baseline",
			records[0].BaselineSeq, records[1].BaselineSeq)
	}
	if records[0].Field != "age" || records[1].Field != "age" {
		t.Fatalf("pair fields = [%q %q], want [age age]", records[0].Field, records[1].Field)
	}
}

func TestVerdictReusesSharedBaseline(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	baseline, err := p.Baseline("users", "age")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if baseline.Matched != 3 || baseline.Seq == 0 {
		t.Fatalf("baseline matched=%d seq=%d, want 3 and a positive sequence", baseline.Matched, baseline.Seq)
	}
	v, err := p.Verdict("users", "age")
	if err != nil {
		t.Fatalf("verdict: %v", err)
	}
	if v.Baseline != baseline.Matched {
		t.Fatalf("verdict baseline = %d, want the stored snapshot %d", v.Baseline, baseline.Matched)
	}
	for _, record := range p.ProbeLog("users") {
		if record.BaselineSeq != baseline.Seq {
			t.Fatalf("record baseline seq = %d, want the shared snapshot %d", record.BaselineSeq, baseline.Seq)
		}
	}
}

func TestProbeBudgetEnforced(t *testing.T) {
	p, err := impl.NewNoSqlOperatorProber(impl.ProberConfig{
		MaxConcurrent:      1,
		MaxProbesPerTarget: 2,
		MaxRecords:         16,
	})
	if err != nil {
		t.Fatalf("build prober: %v", err)
	}
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	if _, err := p.Verdict("users", "age"); err != nil {
		t.Fatalf("first pair must fit the budget of two probes: %v", err)
	}
	payload, err := p.Generate("age", "25", impl.OpGreater)
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	if _, err := p.Inject("users", payload); !errors.Is(err, impl.ErrProbeBudget) {
		t.Fatalf("third probe must be refused with ErrProbeBudget, got %v", err)
	}
	if stats := p.Stats("users"); stats.Probes != 2 || stats.Rejected != 1 {
		t.Fatalf("stats probes=%d rejected=%d, want 2 and 1", stats.Probes, stats.Rejected)
	}
}

func TestConcurrentProbesRespectGlobalLimit(t *testing.T) {
	p, err := impl.NewNoSqlOperatorProber(impl.ProberConfig{
		MaxConcurrent:      2,
		MaxProbesPerTarget: 400,
		MaxRecords:         128,
	})
	if err != nil {
		t.Fatalf("build prober: %v", err)
	}
	for _, name := range []string{"left", "right"} {
		if err := p.RegisterTarget(name, operatorDocs(), impl.ModeObject); err != nil {
			t.Fatalf("register target %s: %v", name, err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		target := "left"
		if i%2 == 1 {
			target = "right"
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			payload, genErr := p.Generate("age", "25", impl.OpGreater)
			if genErr != nil {
				return
			}
			_, _ = p.Inject(name, payload)
		}(target)
	}
	wg.Wait()
	global := p.GlobalStats()
	if global.PeakInFlight < 1 {
		t.Fatalf("peak in flight = %d, want at least one observed probe", global.PeakInFlight)
	}
	if global.PeakInFlight > 2 {
		t.Fatalf("peak in flight = %d, want it capped at the configured 2", global.PeakInFlight)
	}
	if global.Probes != 24 {
		t.Fatalf("accepted probes = %d, want 24", global.Probes)
	}
}

func TestConcurrentProbesSerializedPerTarget(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload, genErr := p.Generate("age", "25", impl.OpGreater)
			if genErr != nil {
				return
			}
			_, _ = p.Inject("users", payload)
		}()
	}
	wg.Wait()
	if peak := p.Stats("users").PeakInFlight; peak != 1 {
		t.Fatalf("per target peak in flight = %d, want exactly 1 because one target submits serially", peak)
	}
}

func TestSnapshotRestoreKeepsBaselineAndRecords(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	if _, err := p.Verdict("users", "age"); err != nil {
		t.Fatalf("verdict: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newOperatorProber(t)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if records := restored.ProbeLog("users"); len(records) != 2 {
		t.Fatalf("restored records = %d, want 2", len(records))
	}
	if stats := restored.Stats("users"); stats.Probes != 2 {
		t.Fatalf("restored probes = %d, want 2", stats.Probes)
	}
	v, err := restored.Verdict("users", "age")
	if err != nil {
		t.Fatalf("verdict after restore: %v", err)
	}
	if v.TrueHits != 3 || v.Baseline != 3 {
		t.Fatalf("restored target true=%d baseline=%d, want 3 and 3", v.TrueHits, v.Baseline)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	p := newOperatorProber(t)
	if err := p.RegisterTarget("users", operatorDocs(), impl.ModeObject); err != nil {
		t.Fatalf("register target: %v", err)
	}
	if err := p.Restore([]byte(`{"targets":[{"name":`)); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("truncated snapshot must be refused with ErrBadSnapshot, got %v", err)
	}
	if targets := p.Targets(); len(targets) != 1 || targets[0] != "users" {
		t.Fatalf("a refused restore must keep the live state, targets=%v", targets)
	}
}
