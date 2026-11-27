package task

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-23"
)

func newPollutionProber(t *testing.T) *impl.PrototypePollutionProber {
	t.Helper()
	return impl.NewPrototypePollutionProber(impl.ProberConfig{
		MaxConcurrent:      2,
		MaxProbesPerTarget: 200,
		MaxResidualKeys:    32,
	})
}

func containsKey(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestGenerateProtoPayloadBody(t *testing.T) {
	p := newPollutionProber(t)
	payload, err := p.Generate(impl.PathProto, "role", "admin")
	if err != nil {
		t.Fatalf("generate proto payload: %v", err)
	}
	if payload.Kind != impl.PathProto || payload.Key != "role" || payload.Value != "admin" {
		t.Fatalf("payload identity mismatch: %+v", payload)
	}
	if got := strings.Join(payload.Path, "."); got != "__proto__.role" {
		t.Fatalf("payload path = %q, want __proto__.role", got)
	}
	var body map[string]map[string]string
	if err := json.Unmarshal([]byte(payload.Body), &body); err != nil {
		t.Fatalf("proto body is not a nested object: %v (body=%q)", err, payload.Body)
	}
	if body["__proto__"]["role"] != "admin" {
		t.Fatalf("proto body = %v, want __proto__.role = admin", body)
	}
}

func TestGenerateConstructorPayloadBody(t *testing.T) {
	p := newPollutionProber(t)
	payload, err := p.Generate(impl.PathConstructor, "role", "admin")
	if err != nil {
		t.Fatalf("generate constructor payload: %v", err)
	}
	if got := strings.Join(payload.Path, "."); got != "constructor.prototype.role" {
		t.Fatalf("payload path = %q, want constructor.prototype.role", got)
	}
	var body map[string]map[string]map[string]string
	if err := json.Unmarshal([]byte(payload.Body), &body); err != nil {
		t.Fatalf("constructor body is not three levels deep: %v (body=%q)", err, payload.Body)
	}
	if body["constructor"]["prototype"]["role"] != "admin" {
		t.Fatalf("constructor body = %v, want constructor.prototype.role = admin", body)
	}
}

func TestGenerateArrayPayloadBody(t *testing.T) {
	p := newPollutionProber(t)
	payload, err := p.Generate(impl.PathArray, "role", "admin")
	if err != nil {
		t.Fatalf("generate array payload: %v", err)
	}
	if got := strings.Join(payload.Path, "."); got != "[0].__proto__.role" {
		t.Fatalf("payload path = %q, want [0].__proto__.role", got)
	}
	var body []map[string]map[string]string
	if err := json.Unmarshal([]byte(payload.Body), &body); err != nil {
		t.Fatalf("array body is not a top level array: %v (body=%q)", err, payload.Body)
	}
	if len(body) != 1 || body[0]["__proto__"]["role"] != "admin" {
		t.Fatalf("array body = %v, want one element carrying __proto__.role", body)
	}
}

func TestGenerateRejectsBadInputs(t *testing.T) {
	p := newPollutionProber(t)
	if _, err := p.Generate(impl.PathProto, "   ", "admin"); !errors.Is(err, impl.ErrBadKey) {
		t.Fatalf("blank key must be rejected with ErrBadKey, got %v", err)
	}
	if _, err := p.Generate("blind", "role", "admin"); !errors.Is(err, impl.ErrBadPathKind) {
		t.Fatalf("unknown path kind must be rejected with ErrBadPathKind, got %v", err)
	}
}

func TestAddTargetRejectsBlankAndDuplicate(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{Name: "   ", Mode: impl.ModeVulnerable}); !errors.Is(err, impl.ErrBadTarget) {
		t.Fatalf("blank target name must be rejected with ErrBadTarget, got %v", err)
	}
	if err := p.AddTarget(impl.TargetSpec{Name: "profile", Mode: impl.ModeVulnerable}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	if err := p.AddTarget(impl.TargetSpec{Name: "profile", Mode: impl.ModeSafe}); !errors.Is(err, impl.ErrDuplicateTarget) {
		t.Fatalf("duplicate target must be rejected with ErrDuplicateTarget, got %v", err)
	}
	if targets := p.Targets(); len(targets) != 1 || targets[0] != "profile" {
		t.Fatalf("targets = %v, want exactly [profile]", targets)
	}
}

func TestInjectRejectsUnknownTarget(t *testing.T) {
	p := newPollutionProber(t)
	payload, err := p.Generate(impl.PathProto, "role", "admin")
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	if _, err := p.Inject("missing", payload); !errors.Is(err, impl.ErrUnknownTarget) {
		t.Fatalf("unknown target must be rejected with ErrUnknownTarget, got %v", err)
	}
}

func TestInjectVulnerableTargetAppliesProto(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{Name: "profile", Mode: impl.ModeVulnerable}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	payload, err := p.Generate(impl.PathProto, "role", "admin")
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	resp, err := p.Inject("profile", payload)
	if err != nil {
		t.Fatalf("inject payload: %v", err)
	}
	if !resp.Accepted || resp.Status != 200 {
		t.Fatalf("response accepted=%v status=%d, want an accepted 200", resp.Accepted, resp.Status)
	}
	if len(resp.Merged) != 1 || resp.Merged[0] != "role" {
		t.Fatalf("merged keys = %v, want [role]", resp.Merged)
	}
	verification, err := p.Verify("profile", "role")
	if err != nil {
		t.Fatalf("verify marker: %v", err)
	}
	if !verification.Inherited || verification.Value != "admin" {
		t.Fatalf("verification = %+v, want role inherited with value admin", verification)
	}
	if verification.Channel != impl.ChannelResponse {
		t.Fatalf("verification channel = %q, want %q", verification.Channel, impl.ChannelResponse)
	}
}

func TestInjectSafeTargetIgnoresProto(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{Name: "profile", Mode: impl.ModeSafe}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	payload, err := p.Generate(impl.PathProto, "role", "admin")
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	resp, err := p.Inject("profile", payload)
	if err != nil {
		t.Fatalf("inject payload: %v", err)
	}
	if !resp.Accepted {
		t.Fatalf("a safe target still answers the merge request, got %+v", resp)
	}
	if len(resp.Merged) != 0 {
		t.Fatalf("merged keys = %v, want none on a safe target", resp.Merged)
	}
	verification, err := p.Verify("profile", "role")
	if err != nil {
		t.Fatalf("verify marker: %v", err)
	}
	if verification.Inherited {
		t.Fatalf("safe target must not inherit the proto key, got %+v", verification)
	}
}

func TestBaselineListsDefaultKeys(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{
		Name:     "profile",
		Mode:     impl.ModeVulnerable,
		Defaults: map[string]string{"theme": "dark", "lang": "zh"},
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	baseline, err := p.Baseline("profile")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if baseline.Seq <= 0 {
		t.Fatalf("baseline sequence = %d, want a positive value", baseline.Seq)
	}
	if len(baseline.Keys) != 2 || baseline.Keys["theme"] != "dark" || baseline.Keys["lang"] != "zh" {
		t.Fatalf("baseline keys = %v, want theme=dark and lang=zh", baseline.Keys)
	}
}

func TestProbeReportsConfirmed(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{Name: "profile", Mode: impl.ModeVulnerable}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	finding, err := p.Probe("profile", impl.PathConstructor, "role", "admin")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !finding.Confirmed {
		t.Fatalf("prototype pollution must be confirmed, got %+v", finding)
	}
	if finding.Reason != impl.ReasonInherited || finding.Channel != impl.ChannelResponse {
		t.Fatalf("finding reason=%q channel=%q, want inherited/response", finding.Reason, finding.Channel)
	}
	if finding.Key != "role" || finding.Target != "profile" || finding.Seq <= 0 {
		t.Fatalf("finding identity mismatch: %+v", finding)
	}
	if finding.BaselineSeq <= 0 {
		t.Fatalf("finding baseline sequence = %d, want a positive snapshot", finding.BaselineSeq)
	}
}

func TestProbeSharesBaselineSnapshot(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{
		Name:     "profile",
		Mode:     impl.ModeVulnerable,
		Defaults: map[string]string{"theme": "dark"},
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	baseline, err := p.Baseline("profile")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	finding, err := p.Probe("profile", impl.PathProto, "role", "admin")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if finding.BaselineSeq != baseline.Seq {
		t.Fatalf("finding baseline seq = %d, want the stored snapshot %d", finding.BaselineSeq, baseline.Seq)
	}
	after, err := p.Baseline("profile")
	if err != nil {
		t.Fatalf("baseline after probe: %v", err)
	}
	if after.Keys["theme"] != "dark" || after.Keys["role"] != "admin" {
		t.Fatalf("baseline after probe = %v, want the polluted key included", after.Keys)
	}
}

func TestRecoverClearsPollutedKeys(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{
		Name:     "profile",
		Mode:     impl.ModeVulnerable,
		Defaults: map[string]string{"theme": "dark"},
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	payload, err := p.Generate(impl.PathProto, "role", "admin")
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	if _, err := p.Inject("profile", payload); err != nil {
		t.Fatalf("inject payload: %v", err)
	}
	if residual := p.ResidualKeys("profile"); !containsKey(residual, "role") {
		t.Fatalf("residual keys = %v, want role before recovery", residual)
	}
	recovery, err := p.Recover("profile")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !containsKey(recovery.Removed, "role") {
		t.Fatalf("removed keys = %v, want role", recovery.Removed)
	}
	if len(recovery.Residual) != 0 {
		t.Fatalf("residual keys = %v, want none after a clean recovery", recovery.Residual)
	}
	if recovery.Seq <= 0 {
		t.Fatalf("recovery sequence = %d, want a positive value", recovery.Seq)
	}
	if residual := p.ResidualKeys("profile"); len(residual) != 0 {
		t.Fatalf("residual keys after recovery = %v, want empty", residual)
	}
	verification, err := p.Verify("profile", "role")
	if err != nil {
		t.Fatalf("verify marker: %v", err)
	}
	if verification.Inherited {
		t.Fatalf("recovered target must not inherit role anymore, got %+v", verification)
	}
	if after, err := p.Baseline("profile"); err != nil {
		t.Fatalf("baseline after recovery: %v", err)
	} else if after.Keys["theme"] != "dark" {
		t.Fatalf("recovery must keep the untouched defaults, got %v", after.Keys)
	}
}

func TestConcurrentInjectSerializedPerTarget(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{Name: "profile", Mode: impl.ModeVulnerable}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload, err := p.Generate(impl.PathProto, "role", "admin")
			if err != nil {
				return
			}
			_, _ = p.Inject("profile", payload)
		}()
	}
	wg.Wait()
	stats := p.Stats("profile")
	if stats.PeakInFlight != 1 {
		t.Fatalf("per target peak in flight = %d, want exactly 1", stats.PeakInFlight)
	}
	if stats.Probes != 16 {
		t.Fatalf("probes = %d, want 16", stats.Probes)
	}
	if stats.Polluted < 1 {
		t.Fatalf("polluted count = %d, want at least one applied key", stats.Polluted)
	}
}

func TestExportImportKeepsPollution(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{
		Name:     "profile",
		Mode:     impl.ModeVulnerable,
		Defaults: map[string]string{"theme": "dark"},
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	payload, err := p.Generate(impl.PathProto, "role", "admin")
	if err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	if _, err := p.Inject("profile", payload); err != nil {
		t.Fatalf("inject payload: %v", err)
	}
	data, err := p.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	restored := newPollutionProber(t)
	if err := restored.Import(data); err != nil {
		t.Fatalf("import: %v", err)
	}
	if residual := restored.ResidualKeys("profile"); !containsKey(residual, "role") {
		t.Fatalf("restored residual keys = %v, want role preserved", residual)
	}
	if stats := restored.Stats("profile"); stats.Polluted != 1 {
		t.Fatalf("restored polluted count = %d, want 1", stats.Polluted)
	}
	verification, err := restored.Verify("profile", "role")
	if err != nil {
		t.Fatalf("verify marker after import: %v", err)
	}
	if !verification.Inherited || verification.Value != "admin" {
		t.Fatalf("restored verification = %+v, want role inherited with value admin", verification)
	}
}

func TestImportRejectsCorruptSnapshot(t *testing.T) {
	p := newPollutionProber(t)
	if err := p.AddTarget(impl.TargetSpec{Name: "profile", Mode: impl.ModeVulnerable}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	if err := p.Import([]byte(`{"targets":[{"name":`)); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("truncated snapshot must be refused with ErrBadSnapshot, got %v", err)
	}
	if targets := p.Targets(); len(targets) != 1 || targets[0] != "profile" {
		t.Fatalf("a refused import must keep the live targets, got %v", targets)
	}
}
