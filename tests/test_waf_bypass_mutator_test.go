package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-45"
)

type funcProbe struct {
	mu     sync.Mutex
	status int
	effect bool
	sends  []string
}

func (f *funcProbe) Send(point impl.InjectionPoint, encoded string) (impl.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, encoded)
	return impl.Response{Status: f.status, Effect: f.effect}, nil
}

func (f *funcProbe) set(status int, effect bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
	f.effect = effect
}

func (f *funcProbe) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sends))
	copy(out, f.sends)
	return out
}

func testPoint() impl.InjectionPoint {
	return impl.InjectionPoint{ID: "pt-search", Path: "/search", Param: "q", Layer: "query", Baseline: "union select 1"}
}

func testPayload(raw string) impl.Payload {
	return impl.Payload{ID: "p1", Point: "pt-search", Raw: raw}
}

func variantOp(t *testing.T, variants []impl.PayloadVariant, op string) impl.PayloadVariant {
	t.Helper()
	for _, v := range variants {
		if v.Op == op {
			return v
		}
	}
	t.Fatalf("variant for operator %q is missing from %+v", op, variants)
	return impl.PayloadVariant{}
}

func newMutator(probe impl.Probe, maxSends, throttleAfter int) *impl.WafBypassMutator {
	return impl.NewWafBypassMutator(probe, impl.Limits{MaxSends: maxSends, ThrottleAfter: throttleAfter})
}

func TestVariantsRejectsBadPayload(t *testing.T) {
	m := newMutator(&funcProbe{status: 200}, 16, 3)
	if _, err := m.Variants(testPayload("")); !errors.Is(err, impl.ErrEmptyPayload) {
		t.Fatalf("an empty payload must fail with ErrEmptyPayload, got %v", err)
	}
	noPoint := testPayload("<script>")
	noPoint.Point = ""
	if _, err := m.Variants(noPoint); !errors.Is(err, impl.ErrBadPoint) {
		t.Fatalf("a payload without a point must fail with ErrBadPoint, got %v", err)
	}
	if _, err := m.Variants(impl.Payload{}); !errors.Is(err, impl.ErrEmptyPayload) {
		t.Fatalf("a zero payload must fail with ErrEmptyPayload, got %v", err)
	}
}

func TestVariantsSixOpsInOrder(t *testing.T) {
	m := newMutator(&funcProbe{status: 200}, 16, 3)
	variants, err := m.Variants(testPayload("<script>"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	wantOps := []string{impl.OpURL, impl.OpDouble, impl.OpUnicode, impl.OpSeparator, impl.OpSplit, impl.OpCase}
	if len(variants) != len(wantOps) {
		t.Fatalf("variants = %d, want %d", len(variants), len(wantOps))
	}
	for i, op := range wantOps {
		if variants[i].Op != op {
			t.Fatalf("variant %d operator = %q, want %q", i, variants[i].Op, op)
		}
		if variants[i].ID != "p1|"+op {
			t.Fatalf("variant %d id = %q, want p1|%s", i, variants[i].ID, op)
		}
		if variants[i].Base != "p1" || variants[i].Point != "pt-search" {
			t.Fatalf("variant %d lost its payload binding: %+v", i, variants[i])
		}
		if variants[i].Encoded == "" {
			t.Fatalf("variant %d has no encoded form", i)
		}
	}
	again, err := m.Variants(testPayload("<script>"))
	if err != nil {
		t.Fatalf("variants again: %v", err)
	}
	for i := range again {
		if again[i].ID != variants[i].ID || again[i].Encoded != variants[i].Encoded {
			t.Fatalf("variant generation is not stable at index %d", i)
		}
	}
}

func TestVariantURLEncoding(t *testing.T) {
	m := newMutator(&funcProbe{status: 200}, 16, 3)
	variants, err := m.Variants(testPayload("<script>alert(1)</script> a b"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	got := variantOp(t, variants, impl.OpURL).Encoded
	want := "%3Cscript%3Ealert%281%29%3C%2Fscript%3E%20a%20b"
	if got != want {
		t.Fatalf("url encoded form = %q, want %q", got, want)
	}
}

func TestVariantDoubleEncoding(t *testing.T) {
	m := newMutator(&funcProbe{status: 200}, 16, 3)
	variants, err := m.Variants(testPayload("<a>"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	got := variantOp(t, variants, impl.OpDouble).Encoded
	want := "%253Ca%253E"
	if got != want {
		t.Fatalf("double encoded form = %q, want %q", got, want)
	}
}

func TestVariantUnicodeEscape(t *testing.T) {
	m := newMutator(&funcProbe{status: 200}, 16, 3)
	variants, err := m.Variants(testPayload("a<b"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	got := variantOp(t, variants, impl.OpUnicode).Encoded
	want := "\\u0061\\u003c\\u0062"
	if got != want {
		t.Fatalf("unicode escaped form = %q, want %q", got, want)
	}
}

func TestVariantSeparatorEquivalence(t *testing.T) {
	m := newMutator(&funcProbe{status: 200}, 16, 3)
	variants, err := m.Variants(testPayload("union select 1"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	got := variantOp(t, variants, impl.OpSeparator).Encoded
	want := "union/**/select/**/1"
	if got != want {
		t.Fatalf("separator equivalent form = %q, want %q", got, want)
	}
}

func TestVariantKeywordSplit(t *testing.T) {
	m := newMutator(&funcProbe{status: 200}, 16, 3)
	variants, err := m.Variants(testPayload("union select a"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	got := variantOp(t, variants, impl.OpSplit).Encoded
	want := "u/**/nion s/**/elect a"
	if got != want {
		t.Fatalf("keyword split form = %q, want %q", got, want)
	}
}

func TestVariantCaseSwap(t *testing.T) {
	m := newMutator(&funcProbe{status: 200}, 16, 3)
	variants, err := m.Variants(testPayload("select 1"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	got := variantOp(t, variants, impl.OpCase).Encoded
	want := "SeLeCt 1"
	if got != want {
		t.Fatalf("case swapped form = %q, want %q", got, want)
	}
}

func TestBaselineClassifies(t *testing.T) {
	probe := &funcProbe{status: 403}
	m := newMutator(probe, 16, 3)
	blocked, err := m.Baseline(testPoint())
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if !blocked.Blocked || blocked.Reason != impl.ReasonWAFRule {
		t.Fatalf("baseline = %+v, want the waf rule classification", blocked)
	}
	if blocked.Status != 403 {
		t.Fatalf("baseline status = %d, want the raw 403", blocked.Status)
	}
	probe.set(200, false)
	allowed, err := m.Baseline(testPoint())
	if err != nil {
		t.Fatalf("baseline allowed: %v", err)
	}
	if allowed.Blocked || allowed.Reason != impl.ReasonAllowed {
		t.Fatalf("baseline = %+v, want the allowed classification", allowed)
	}
	if _, err := m.Baseline(impl.InjectionPoint{Path: "/search"}); !errors.Is(err, impl.ErrBadPoint) {
		t.Fatalf("a point without an id must fail with ErrBadPoint, got %v", err)
	}
	empty := impl.NewWafBypassMutator(nil, impl.Limits{MaxSends: 4, ThrottleAfter: 2})
	if _, err := empty.Baseline(testPoint()); !errors.Is(err, impl.ErrNilProbe) {
		t.Fatalf("a nil probe must fail with ErrNilProbe, got %v", err)
	}
}

func TestSubmitMarksEffectiveOnEffect(t *testing.T) {
	probe := &funcProbe{status: 200, effect: true}
	m := newMutator(probe, 16, 3)
	if _, err := m.Baseline(testPoint()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	variants, err := m.Variants(testPayload("union select 1"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	outcome, err := m.Submit(variants[0])
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !outcome.Passed || !outcome.Reached || !outcome.Effect || !outcome.Effective {
		t.Fatalf("outcome = %+v, want a passed, reached and effective bypass", outcome)
	}
	if outcome.Reason != impl.ReasonBypass {
		t.Fatalf("reason = %q, want %q", outcome.Reason, impl.ReasonBypass)
	}
	if got := probe.sent(); len(got) != 2 || got[1] != variants[0].Encoded {
		t.Fatalf("probe received %v, want the baseline then the variant bytes", got)
	}
	probe.set(403, true)
	second, err := m.Submit(variants[1])
	if err != nil {
		t.Fatalf("submit blocked: %v", err)
	}
	if second.Passed || second.Effective {
		t.Fatalf("a 403 answer must not be treated as a bypass, got %+v", second)
	}
	if second.Reason != impl.ReasonBlocked {
		t.Fatalf("blocked reason = %q, want %q", second.Reason, impl.ReasonBlocked)
	}
	if _, err := m.Submit(variants[2]); err != nil {
		t.Fatalf("submit without a baseline of another point must work for this point: %v", err)
	}
	noBaseline := variants[3]
	noBaseline.Point = "pt-ghost"
	if _, err := m.Submit(noBaseline); !errors.Is(err, impl.ErrNoBaseline) {
		t.Fatalf("a point without a baseline must fail with ErrNoBaseline, got %v", err)
	}
}

func TestSubmitRejectsDuplicateVariant(t *testing.T) {
	probe := &funcProbe{status: 200, effect: true}
	m := newMutator(probe, 16, 3)
	if _, err := m.Baseline(testPoint()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	variants, err := m.Variants(testPayload("union select 1"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	if _, err := m.Submit(variants[0]); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := m.Submit(variants[0]); !errors.Is(err, impl.ErrDuplicateVariant) {
		t.Fatalf("resubmitting the same variant must fail with ErrDuplicateVariant, got %v", err)
	}
	if got := m.Tried(); len(got) != 1 || got[0] != variants[0].ID {
		t.Fatalf("tried set = %v, want the single submitted variant", got)
	}
	if got := probe.sent(); len(got) != 2 {
		t.Fatalf("a duplicate must not reach the probe, sends = %v", got)
	}
}

func TestSubmitThrottlesAfterConsecutiveBlocks(t *testing.T) {
	probe := &funcProbe{status: 403}
	m := newMutator(probe, 16, 2)
	if _, err := m.Baseline(testPoint()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	variants, err := m.Variants(testPayload("union select 1"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := m.Submit(variants[i]); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	if !m.Throttled() {
		t.Fatalf("two consecutive blocks must throttle the run")
	}
	if _, err := m.Submit(variants[2]); !errors.Is(err, impl.ErrThrottled) {
		t.Fatalf("a throttled submit must fail with ErrThrottled, got %v", err)
	}
	if got := probe.sent(); len(got) != 3 {
		t.Fatalf("a throttled submit must not reach the probe, sends = %v", got)
	}
	stats, err := m.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Blocked != 2 || stats.ConsecutiveBlocks != 2 || !stats.Throttled {
		t.Fatalf("stats = %+v, want two consecutive blocks recorded", stats)
	}
}

func TestStopBlocksFurtherSubmits(t *testing.T) {
	probe := &funcProbe{status: 200, effect: true}
	m := newMutator(probe, 16, 3)
	if err := m.Stop("   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("stopping without a reason must fail with ErrBadReason, got %v", err)
	}
	if _, err := m.Baseline(testPoint()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	variants, err := m.Variants(testPayload("union select 1"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	if err := m.Stop("edge started rate limiting"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := m.Stop("a second reason"); err != nil {
		t.Fatalf("a repeated stop must be idempotent, got %v", err)
	}
	if _, err := m.Submit(variants[0]); !errors.Is(err, impl.ErrStopped) {
		t.Fatalf("submit after stop must fail with ErrStopped, got %v", err)
	}
	stats, err := m.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if !stats.Stopped || stats.StopReason != "edge started rate limiting" {
		t.Fatalf("stats = %+v, want the first stop reason kept", stats)
	}
}

func TestRankOrdersBySuccesses(t *testing.T) {
	probe := &funcProbe{status: 200, effect: true}
	m := newMutator(probe, 16, 3)
	if _, err := m.Baseline(testPoint()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	variants, err := m.Variants(testPayload("union select 1"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	url := variantOp(t, variants, impl.OpURL)
	sep := variantOp(t, variants, impl.OpSeparator)
	swapped := variantOp(t, variants, impl.OpCase)
	if _, err := m.Submit(sep); err != nil {
		t.Fatalf("submit sep: %v", err)
	}
	if _, err := m.Submit(url); err != nil {
		t.Fatalf("submit url: %v", err)
	}
	ranked, err := m.Rank([]impl.PayloadVariant{swapped, sep, url})
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	want := []string{url.ID, sep.ID, swapped.ID}
	for i, id := range want {
		if ranked[i].ID != id {
			t.Fatalf("rank order = %+v, want %v", ranked, want)
		}
	}
	if len(m.Effective()) != 2 {
		t.Fatalf("effective set = %v, want both submitted variants", m.Effective())
	}
	if _, err := m.Rank(nil); err != nil {
		t.Fatalf("ranking nothing must be a no-op, got %v", err)
	}
}

func TestSnapshotRestoreKeepsTriedAndEffective(t *testing.T) {
	probe := &funcProbe{status: 200, effect: true}
	m := newMutator(probe, 16, 3)
	if _, err := m.Baseline(testPoint()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	variants, err := m.Variants(testPayload("union select 1"))
	if err != nil {
		t.Fatalf("variants: %v", err)
	}
	if _, err := m.Submit(variants[0]); err != nil {
		t.Fatalf("submit: %v", err)
	}
	before, err := m.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewWafBypassMutator(probe, impl.Limits{MaxSends: 16, ThrottleAfter: 3})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after, err := restored.Stats()
	if err != nil {
		t.Fatalf("stats after restore: %v", err)
	}
	if after.Sent != before.Sent || after.Effective != before.Effective {
		t.Fatalf("restored stats = %+v, want the snapshot values %+v", after, before)
	}
	if got := restored.Tried(); len(got) != 1 || got[0] != variants[0].ID {
		t.Fatalf("restored tried set = %v, want the submitted variant", got)
	}
	if got := restored.Effective(); len(got) != 1 {
		t.Fatalf("restored effective set = %v, want the bypass", got)
	}
	if _, err := restored.Submit(variants[0]); !errors.Is(err, impl.ErrDuplicateVariant) {
		t.Fatalf("the tried set must survive a restore, got %v", err)
	}
	if _, err := restored.Submit(variants[1]); err != nil {
		t.Fatalf("the baseline table must survive a restore, got %v", err)
	}
	if err := restored.Restore([]byte("}{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	kept, err := restored.Stats()
	if err != nil || kept.Sent != before.Sent+1 {
		t.Fatalf("a rejected restore must not touch live state, got %+v err=%v", kept, err)
	}
}
