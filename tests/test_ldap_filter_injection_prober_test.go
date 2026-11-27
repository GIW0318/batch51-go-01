package task

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-c-35"
)

type staticResponder struct {
	count int
	attrs []string
	err   error
}

func (r staticResponder) Query(filter string) (int, []string, error) {
	return r.count, r.attrs, r.err
}

type trackingResponder struct {
	inFlight int32
	maxSeen  int32
}

func (r *trackingResponder) Query(filter string) (int, []string, error) {
	n := atomic.AddInt32(&r.inFlight, 1)
	for {
		old := atomic.LoadInt32(&r.maxSeen)
		if n <= old || atomic.CompareAndSwapInt32(&r.maxSeen, old, n) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	atomic.AddInt32(&r.inFlight, -1)
	return 1, []string{"cn=probe"}, nil
}

func probeTarget() impl.Target {
	return impl.Target{ID: "dc=corp", Base: "(&(uid={input})(objectClass=person))", Cap: 2}
}

func TestBuildReturnsThreePayloadKinds(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	payloads, err := p.Build(probeTarget().Base)
	if err != nil {
		t.Fatalf("build must succeed, got %v", err)
	}
	if len(payloads) != 3 {
		t.Fatalf("payloads = %d, want 3", len(payloads))
	}
	want := []string{impl.PayloadClosure, impl.PayloadWildcard, impl.PayloadLogic}
	for i, kind := range want {
		if payloads[i].Kind != kind {
			t.Fatalf("payload %d kind = %q, want %q", i, payloads[i].Kind, kind)
		}
		if payloads[i].Filter == "" {
			t.Fatalf("payload %d has an empty filter", i)
		}
	}
}

func TestBuildRejectsEmptyBase(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	if _, err := p.Build("   "); !errors.Is(err, impl.ErrEmptyBase) {
		t.Fatalf("an empty base filter must fail with ErrEmptyBase, got %v", err)
	}
}

func TestBuildPayloadsCarryNonce(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	payloads, err := p.Build(probeTarget().Base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for i, payload := range payloads {
		if payload.Nonce == "" {
			t.Fatalf("payload %d has no probe prefix", i)
		}
		if !strings.Contains(payload.Filter, payload.Nonce) {
			t.Fatalf("payload %d filter = %q, want the probe prefix %q inside it", i, payload.Filter, payload.Nonce)
		}
	}
}

func TestNonceDiffersAcrossRounds(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	first := p.Nonce()
	second := p.Nonce()
	if first == "" || second == "" {
		t.Fatalf("probe prefixes must not be empty, got %q and %q", first, second)
	}
	if first == second {
		t.Fatalf("two rounds produced the same probe prefix %q, so a cached answer cannot be told apart", first)
	}
}

func TestSubmitReturnsObservedCount(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	target := probeTarget()
	res, err := p.Submit(target, staticResponder{count: 7, attrs: []string{"cn=a"}}, impl.Payload{Kind: impl.PayloadClosure, Filter: "(x)"})
	if err != nil {
		t.Fatalf("submit must succeed, got %v", err)
	}
	if res.Count != 7 {
		t.Fatalf("observed count = %d, want 7", res.Count)
	}
	if res.Target != target.ID {
		t.Fatalf("result target = %q, want %q", res.Target, target.ID)
	}
	if res.Payload.Kind != impl.PayloadClosure {
		t.Fatalf("result payload kind = %q, want %q", res.Payload.Kind, impl.PayloadClosure)
	}
}

func TestSubmitWrapsResponderError(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	sentinel := errors.New("connection reset by peer")
	_, err := p.Submit(probeTarget(), staticResponder{err: sentinel}, impl.Payload{Kind: impl.PayloadLogic, Filter: "(x)"})
	if err == nil {
		t.Fatalf("a failing query must be reported")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("submission error = %v, want it to wrap the responder error", err)
	}
}

func TestSubmitSerialisesPerTarget(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(4)
	responder := &trackingResponder{}
	target := probeTarget()
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Submit(target, responder, impl.Payload{Kind: impl.PayloadWildcard, Filter: "(x)"})
			if err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d of 8 submissions failed", failures)
	}
	if got := atomic.LoadInt32(&responder.maxSeen); got != 1 {
		t.Fatalf("peak in flight queries on one entry point = %d, want 1 because probes are serialised", got)
	}
}

func TestSubmitSortsAttributes(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	res, err := p.Submit(probeTarget(), staticResponder{count: 3, attrs: []string{"cn=b", "cn=a", "cn=a"}}, impl.Payload{Kind: impl.PayloadClosure, Filter: "(x)"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(res.Attributes) != 2 {
		t.Fatalf("attributes = %v, want two distinct ordered entries", res.Attributes)
	}
	if res.Attributes[0] != "cn=a" || res.Attributes[1] != "cn=b" {
		t.Fatalf("attributes = %v, want cn=a then cn=b", res.Attributes)
	}
}

func TestDiffConfirmsCountIncrease(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	baseline := impl.ProbeResult{Count: 3, Attributes: []string{"cn=a"}}
	injected := impl.ProbeResult{Count: 9, Attributes: []string{"cn=a"}, Payload: impl.Payload{Kind: impl.PayloadClosure}}
	d := p.Diff(baseline, injected)
	if d.Verdict != impl.VerdictConfirmed {
		t.Fatalf("verdict = %q, want %q when the injected filter returns more rows", d.Verdict, impl.VerdictConfirmed)
	}
	if d.Baseline != 3 || d.Injected != 9 {
		t.Fatalf("diff counts = %d/%d, want 3/9", d.Baseline, d.Injected)
	}
	if d.Kind != impl.PayloadClosure {
		t.Fatalf("diff kind = %q, want %q", d.Kind, impl.PayloadClosure)
	}
}

func TestDiffNotConfirmedOnIdenticalResults(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	baseline := impl.ProbeResult{Count: 4, Attributes: []string{"cn=a", "cn=b"}}
	injected := impl.ProbeResult{Count: 4, Attributes: []string{"cn=a", "cn=b"}, Payload: impl.Payload{Kind: impl.PayloadWildcard}}
	d := p.Diff(baseline, injected)
	if d.Verdict != impl.VerdictNotConfirmed {
		t.Fatalf("verdict = %q, want %q when both sides match exactly", d.Verdict, impl.VerdictNotConfirmed)
	}
}

func TestDiffConfirmsAttributeDifference(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	baseline := impl.ProbeResult{Count: 2, Attributes: []string{"cn=a", "cn=b"}}
	injected := impl.ProbeResult{Count: 2, Attributes: []string{"cn=a", "cn=admin"}, Payload: impl.Payload{Kind: impl.PayloadLogic}}
	d := p.Diff(baseline, injected)
	if d.Verdict != impl.VerdictConfirmed {
		t.Fatalf("verdict = %q, want %q when the attribute set changes at the same row count", d.Verdict, impl.VerdictConfirmed)
	}
}

func TestDiffComparesSortedAttributes(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	baseline := impl.ProbeResult{Count: 2, Attributes: []string{"cn=a", "cn=b"}}
	injected := impl.ProbeResult{Count: 2, Attributes: []string{"cn=b", "cn=a"}, Payload: impl.Payload{Kind: impl.PayloadLogic}}
	d := p.Diff(baseline, injected)
	if d.Verdict != impl.VerdictNotConfirmed {
		t.Fatalf("verdict = %q, want %q when only the returned order differs", d.Verdict, impl.VerdictNotConfirmed)
	}
}

func TestSortEntriesDedupesAndSorts(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	got := p.SortEntries([]string{"cn=b", "cn=a", "cn=b"})
	if len(got) != 2 {
		t.Fatalf("sorted entries = %v, want two entries", got)
	}
	if got[0] != "cn=a" || got[1] != "cn=b" {
		t.Fatalf("sorted entries = %v, want cn=a then cn=b", got)
	}
}

func TestDiffReportsErrorVerdict(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	baseline := impl.ProbeResult{Count: 2, Attributes: []string{"cn=a"}}
	injected := impl.ProbeResult{Error: "server closed the connection", Payload: impl.Payload{Kind: impl.PayloadClosure}}
	d := p.Diff(baseline, injected)
	if d.Verdict != impl.VerdictError {
		t.Fatalf("verdict = %q, want %q when the probe itself failed", d.Verdict, impl.VerdictError)
	}
	if d.Note == "" {
		t.Fatalf("a failed diff must carry a note")
	}
}

func TestSubmitRejectsNilResponder(t *testing.T) {
	p := impl.NewLdapFilterInjectionProber(2)
	if _, err := p.Submit(probeTarget(), nil, impl.Payload{Kind: impl.PayloadClosure, Filter: "(x)"}); !errors.Is(err, impl.ErrNilResponder) {
		t.Fatalf("submitting without a responder must fail with ErrNilResponder, got %v", err)
	}
}
