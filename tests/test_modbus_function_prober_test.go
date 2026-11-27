package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-26"
)

type fakeSlave struct {
	mu                sync.Mutex
	regs              map[uint16]uint16
	exceptions        map[uint16]uint8
	failAddr          map[uint16]bool
	writeCount        map[uint16]int
	rejectSecondWrite bool
	exchanges         int
	writes            int
}

func newFakeSlave() *fakeSlave {
	return &fakeSlave{
		regs:       map[uint16]uint16{},
		exceptions: map[uint16]uint8{},
		failAddr:   map[uint16]bool{},
		writeCount: map[uint16]int{},
	}
}

func (f *fakeSlave) Exchange(req impl.Request) impl.Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanges++
	if f.failAddr[req.Addr] {
		return impl.Response{Unit: req.Unit, Func: req.Func, Err: errors.New("slave timeout")}
	}
	if code, ok := f.exceptions[req.Addr]; ok {
		return impl.Response{Unit: req.Unit, Func: req.Func, Exception: code}
	}
	switch req.Func {
	case impl.FuncReadCoils, impl.FuncReadDiscrete, impl.FuncReadHolding, impl.FuncReadInput:
		return impl.Response{Unit: req.Unit, Func: req.Func, Values: []uint16{f.regs[req.Addr]}}
	case impl.FuncWriteCoil, impl.FuncWriteRegister:
		if f.rejectSecondWrite && f.writeCount[req.Addr] >= 1 {
			return impl.Response{Unit: req.Unit, Func: req.Func, Exception: 4}
		}
		f.writeCount[req.Addr]++
		if len(req.Values) > 0 {
			f.regs[req.Addr] = req.Values[0]
		}
		f.writes++
		return impl.Response{Unit: req.Unit, Func: req.Func}
	default:
		return impl.Response{Unit: req.Unit, Func: req.Func, Exception: 1}
	}
}

func holdingPlan(unit uint8, addrs ...uint16) impl.ProbePlan {
	plan := impl.ProbePlan{Unit: unit, Bank: impl.BankHolding}
	for _, addr := range addrs {
		plan.Requests = append(plan.Requests, impl.Request{Unit: unit, Func: impl.FuncReadHolding, Addr: addr, Count: 1})
	}
	return plan
}

func TestPlanEnumeratesBankBoundaries(t *testing.T) {
	p := impl.NewModbusFunctionProber(newFakeSlave(), impl.Limits{MaxRequests: 64, MaxSpan: 512})
	plan, err := p.Plan(impl.Target{Unit: 1, Bank: impl.BankHolding, From: 100, To: 110}, impl.PlanOptions{Stride: 4})
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	want := []uint16{100, 101, 104, 108, 109, 110}
	if len(plan.Requests) != len(want) {
		t.Fatalf("plan has %d requests, want %d", len(plan.Requests), len(want))
	}
	for i, addr := range want {
		req := plan.Requests[i]
		if req.Addr != addr {
			t.Fatalf("request %d address = %d, want %d", i, req.Addr, addr)
		}
		if req.Func != impl.FuncReadHolding {
			t.Fatalf("request %d function = %d, want %d", i, req.Func, impl.FuncReadHolding)
		}
		if req.Unit != 1 || req.Count != 1 {
			t.Fatalf("request %d unit=%d count=%d, want unit 1 count 1", i, req.Unit, req.Count)
		}
	}
}

func TestPlanRejectsInvalidRange(t *testing.T) {
	p := impl.NewModbusFunctionProber(newFakeSlave(), impl.Limits{MaxRequests: 64, MaxSpan: 128})
	if _, err := p.Plan(impl.Target{Unit: 1, Bank: impl.BankHolding, From: 200, To: 100}, impl.PlanOptions{Stride: 4}); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("inverted range must be rejected with ErrBadRange, got %v", err)
	}
	if _, err := p.Plan(impl.Target{Unit: 1, Bank: impl.BankHolding, From: 0, To: 400}, impl.PlanOptions{Stride: 4}); !errors.Is(err, impl.ErrSpanTooLarge) {
		t.Fatalf("over long span must be rejected with ErrSpanTooLarge, got %v", err)
	}
	if _, err := p.Plan(impl.Target{Unit: 1, Bank: "timers", From: 0, To: 4}, impl.PlanOptions{Stride: 4}); !errors.Is(err, impl.ErrUnknownBank) {
		t.Fatalf("unknown bank must be rejected with ErrUnknownBank, got %v", err)
	}
}

func TestPlanSelectsReadFunctionPerBank(t *testing.T) {
	p := impl.NewModbusFunctionProber(newFakeSlave(), impl.Limits{MaxRequests: 64, MaxSpan: 64})
	cases := []struct {
		bank string
		fn   uint8
	}{
		{impl.BankCoils, impl.FuncReadCoils},
		{impl.BankDiscrete, impl.FuncReadDiscrete},
		{impl.BankHolding, impl.FuncReadHolding},
		{impl.BankInput, impl.FuncReadInput},
	}
	for _, c := range cases {
		plan, err := p.Plan(impl.Target{Unit: 2, Bank: c.bank, From: 0, To: 2}, impl.PlanOptions{Stride: 8})
		if err != nil {
			t.Fatalf("plan for %s: %v", c.bank, err)
		}
		if len(plan.Requests) == 0 {
			t.Fatalf("plan for %s must contain requests", c.bank)
		}
		for i, req := range plan.Requests {
			if req.Func != c.fn {
				t.Fatalf("%s request %d function = %d, want %d", c.bank, i, req.Func, c.fn)
			}
		}
	}
}

func TestProbeClassifiesIllegalAddressAsOutOfRange(t *testing.T) {
	dev := newFakeSlave()
	dev.exceptions[109] = 2
	p := impl.NewModbusFunctionProber(dev, impl.Limits{MaxRequests: 16, MaxSpan: 512})
	report, err := p.Probe(holdingPlan(3, 109, 108, 110))
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	if report.Requests != 3 {
		t.Fatalf("report requests = %d, want 3", report.Requests)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one entry", report.Findings)
	}
	f := report.Findings[0]
	if f.Addr != 109 || f.Code != 2 {
		t.Fatalf("finding addr=%d code=%d, want addr 109 code 2", f.Addr, f.Code)
	}
	if f.Verdict != impl.VerdictOutOfRange {
		t.Fatalf("finding verdict = %q, want %q", f.Verdict, impl.VerdictOutOfRange)
	}
	if f.Unit != 3 || f.Func != impl.FuncReadHolding {
		t.Fatalf("finding unit=%d func=%d, want unit 3 holding read", f.Unit, f.Func)
	}
}

func TestProbeStopsOnTransportTimeout(t *testing.T) {
	dev := newFakeSlave()
	dev.failAddr[104] = true
	p := impl.NewModbusFunctionProber(dev, impl.Limits{MaxRequests: 16, MaxSpan: 512})
	report, err := p.Probe(holdingPlan(4, 100, 101, 104, 108, 109, 110))
	if err != nil {
		t.Fatalf("probe must return a report on a transport timeout, got %v", err)
	}
	if !report.TimedOut {
		t.Fatalf("report must be marked as timed out")
	}
	if report.Requests != 3 {
		t.Fatalf("executed requests = %d, want 3 (two good and one timeout)", report.Requests)
	}
	if report.Skipped != 3 {
		t.Fatalf("skipped requests = %d, want 3", report.Skipped)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("a timeout alone must not create findings, got %+v", report.Findings)
	}
}

func TestProbeReservesReadBudgetUpFront(t *testing.T) {
	p := impl.NewModbusFunctionProber(newFakeSlave(), impl.Limits{MaxRequests: 4, MaxSpan: 512})
	if _, err := p.Probe(holdingPlan(5, 100, 101, 102, 103, 104, 105)); !errors.Is(err, impl.ErrReadBudget) {
		t.Fatalf("over budget plan must fail with ErrReadBudget, got %v", err)
	}
	if got := p.Stats().ReadRequests; got != 0 {
		t.Fatalf("rejected plan consumed %d read requests, want 0", got)
	}
	if _, err := p.Probe(holdingPlan(5, 100, 101, 102, 103)); err != nil {
		t.Fatalf("plan fitting the budget must run, got %v", err)
	}
	if got := p.Stats().ReadRequests; got != 4 {
		t.Fatalf("read requests after a four request plan = %d, want 4", got)
	}
}

func TestClassifyUnknownExceptionCode(t *testing.T) {
	p := impl.NewModbusFunctionProber(newFakeSlave(), impl.Limits{})
	if _, err := p.Classify(0x7f); !errors.Is(err, impl.ErrUnknownException) {
		t.Fatalf("unknown exception code must fail with ErrUnknownException, got %v", err)
	}
	got, err := p.Classify(3)
	if err != nil {
		t.Fatalf("classify code 3: %v", err)
	}
	if got != impl.VerdictDenied {
		t.Fatalf("code 3 verdict = %q, want %q", got, impl.VerdictDenied)
	}
}

func TestWriteProbeRequiresAuthorization(t *testing.T) {
	dev := newFakeSlave()
	dev.regs[100] = 7
	p := impl.NewModbusFunctionProber(dev, impl.Limits{MaxWrites: 4, MaxWritesPerUnit: 4})
	_, err := p.WriteProbe(impl.WriteRequest{Unit: 1, Func: impl.FuncWriteRegister, Addr: 100, Value: 42, ScaleMin: 0, ScaleMax: 1000})
	if !errors.Is(err, impl.ErrWriteNotAuthorized) {
		t.Fatalf("unauthorised write must fail with ErrWriteNotAuthorized, got %v", err)
	}
	if dev.writes != 0 || dev.exchanges != 0 {
		t.Fatalf("unauthorised write touched the device: writes=%d exchanges=%d", dev.writes, dev.exchanges)
	}
	if len(p.Writes()) != 0 {
		t.Fatalf("unauthorised write must not be recorded")
	}
}

func TestWriteProbeRestoresOriginalValue(t *testing.T) {
	dev := newFakeSlave()
	dev.regs[100] = 7
	p := impl.NewModbusFunctionProber(dev, impl.Limits{MaxWrites: 4, MaxWritesPerUnit: 4})
	if err := p.AuthorizeWrites([]uint8{1}, 4); err != nil {
		t.Fatalf("authorise writes: %v", err)
	}
	res, err := p.WriteProbe(impl.WriteRequest{Unit: 1, Func: impl.FuncWriteRegister, Addr: 100, Value: 42, ScaleMin: 0, ScaleMax: 1000})
	if err != nil {
		t.Fatalf("write probe: %v", err)
	}
	if res.Original != 7 || res.Written != 42 || res.ReadBack != 42 {
		t.Fatalf("result original=%d written=%d readback=%d, want 7/42/42", res.Original, res.Written, res.ReadBack)
	}
	if !res.Restored {
		t.Fatalf("result restored=%v, want the original value written back", res.Restored)
	}
	if got := dev.regs[100]; got != 7 {
		t.Fatalf("register after the probe = %d, want the original 7", got)
	}
	writes := p.Writes()
	if len(writes) != 1 {
		t.Fatalf("write ledger has %d entries, want 1", len(writes))
	}
	if !writes[0].Restored || writes[0].Addr != 100 || writes[0].Written != 42 {
		t.Fatalf("write ledger entry = %+v, want restored write of 42 to 100", writes[0])
	}
	if got := p.Stats().Restores; got != 1 {
		t.Fatalf("restore counter = %d, want 1", got)
	}
}

func TestWriteProbeEnforcesPerUnitBudget(t *testing.T) {
	dev := newFakeSlave()
	p := impl.NewModbusFunctionProber(dev, impl.Limits{MaxWrites: 10, MaxWritesPerUnit: 2})
	if err := p.AuthorizeWrites([]uint8{1, 2}, 10); err != nil {
		t.Fatalf("authorise writes: %v", err)
	}
	for _, addr := range []uint16{10, 11} {
		if _, err := p.WriteProbe(impl.WriteRequest{Unit: 1, Func: impl.FuncWriteRegister, Addr: addr, Value: 5, ScaleMin: 0, ScaleMax: 100}); err != nil {
			t.Fatalf("write %d on unit 1: %v", addr, err)
		}
	}
	if _, err := p.WriteProbe(impl.WriteRequest{Unit: 1, Func: impl.FuncWriteRegister, Addr: 12, Value: 5, ScaleMin: 0, ScaleMax: 100}); !errors.Is(err, impl.ErrWriteBudget) {
		t.Fatalf("third write on unit 1 must hit the per unit budget, got %v", err)
	}
	if _, err := p.WriteProbe(impl.WriteRequest{Unit: 2, Func: impl.FuncWriteRegister, Addr: 20, Value: 5, ScaleMin: 0, ScaleMax: 100}); err != nil {
		t.Fatalf("unit 2 has its own budget, got %v", err)
	}
	if got := len(p.Writes()); got != 3 {
		t.Fatalf("write ledger has %d entries, want 3", got)
	}
}

func TestWriteProbeHaltsAfterRestoreFailure(t *testing.T) {
	dev := newFakeSlave()
	dev.regs[100] = 7
	dev.rejectSecondWrite = true
	p := impl.NewModbusFunctionProber(dev, impl.Limits{MaxWrites: 6, MaxWritesPerUnit: 6})
	if err := p.AuthorizeWrites([]uint8{1}, 6); err != nil {
		t.Fatalf("authorise writes: %v", err)
	}
	if _, err := p.WriteProbe(impl.WriteRequest{Unit: 1, Func: impl.FuncWriteRegister, Addr: 100, Value: 42, ScaleMin: 0, ScaleMax: 1000}); !errors.Is(err, impl.ErrRestoreFailed) {
		t.Fatalf("failed restore must surface ErrRestoreFailed, got %v", err)
	}
	writes := p.Writes()
	if len(writes) != 1 || writes[0].Restored {
		t.Fatalf("write ledger = %+v, want one entry with Restored=false", writes)
	}
	if writes[0].ReadBack != 42 {
		t.Fatalf("recorded read back = %d, want 42", writes[0].ReadBack)
	}
	before := dev.writes
	if _, err := p.WriteProbe(impl.WriteRequest{Unit: 1, Func: impl.FuncWriteRegister, Addr: 101, Value: 9, ScaleMin: 0, ScaleMax: 1000}); !errors.Is(err, impl.ErrWriteHalted) {
		t.Fatalf("write after a failed restore must fail with ErrWriteHalted, got %v", err)
	}
	if dev.writes != before {
		t.Fatalf("halted prober must not touch the device")
	}
}

func TestWriteProbeRejectsReadFunction(t *testing.T) {
	dev := newFakeSlave()
	p := impl.NewModbusFunctionProber(dev, impl.Limits{MaxWrites: 4, MaxWritesPerUnit: 4})
	if err := p.AuthorizeWrites([]uint8{1}, 4); err != nil {
		t.Fatalf("authorise writes: %v", err)
	}
	_, err := p.WriteProbe(impl.WriteRequest{Unit: 1, Func: impl.FuncReadHolding, Addr: 100, Value: 1})
	if !errors.Is(err, impl.ErrUnsupportedFunction) {
		t.Fatalf("read function as a write must fail with ErrUnsupportedFunction, got %v", err)
	}
	if dev.exchanges != 0 {
		t.Fatalf("rejected write function sent %d exchanges, want 0", dev.exchanges)
	}
}

func TestSnapshotRestoreKeepsHaltAndLedger(t *testing.T) {
	dev := newFakeSlave()
	dev.regs[100] = 7
	dev.rejectSecondWrite = true
	limits := impl.Limits{MaxWrites: 6, MaxWritesPerUnit: 6}
	p := impl.NewModbusFunctionProber(dev, limits)
	if err := p.AuthorizeWrites([]uint8{1}, 6); err != nil {
		t.Fatalf("authorise writes: %v", err)
	}
	if _, err := p.WriteProbe(impl.WriteRequest{Unit: 1, Func: impl.FuncWriteRegister, Addr: 100, Value: 42, ScaleMin: 0, ScaleMax: 1000}); !errors.Is(err, impl.ErrRestoreFailed) {
		t.Fatalf("prepare halted state: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewModbusFunctionProber(newFakeSlave(), limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Writes()); got != 1 {
		t.Fatalf("restored write ledger has %d entries, want 1", got)
	}
	if err := restored.AuthorizeWrites([]uint8{1}, 6); err != nil {
		t.Fatalf("authorise writes after restore: %v", err)
	}
	if _, err := restored.WriteProbe(impl.WriteRequest{Unit: 1, Func: impl.FuncWriteRegister, Addr: 101, Value: 3, ScaleMin: 0, ScaleMax: 100}); !errors.Is(err, impl.ErrWriteHalted) {
		t.Fatalf("restored prober must stay halted, got %v", err)
	}
	if err := restored.Restore([]byte("{broken-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(restored.Writes()); got != 1 {
		t.Fatalf("rejected restore touched the ledger, now %d entries", got)
	}
}

func TestConcurrentWriteProbesRespectTotalBudget(t *testing.T) {
	dev := newFakeSlave()
	p := impl.NewModbusFunctionProber(dev, impl.Limits{MaxWrites: 3, MaxWritesPerUnit: 10})
	if err := p.AuthorizeWrites([]uint8{1}, 3); err != nil {
		t.Fatalf("authorise writes: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, budget := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := p.WriteProbe(impl.WriteRequest{
				Unit:     1,
				Func:     impl.FuncWriteRegister,
				Addr:     uint16(100 + i),
				Value:    uint16(i + 1),
				ScaleMin: 0,
				ScaleMax: 1000,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, impl.ErrWriteBudget):
				budget++
			default:
				t.Errorf("unexpected write error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 {
		t.Fatalf("concurrent writes accepted %d probes, want exactly the budget 3", ok)
	}
	if budget != 9 {
		t.Fatalf("concurrent writes reported %d budget errors, want 9", budget)
	}
	if got := len(p.Writes()); got != 3 {
		t.Fatalf("write ledger has %d entries, want 3", got)
	}
}

func TestUnitReportAggregatesFindings(t *testing.T) {
	dev := newFakeSlave()
	dev.exceptions[109] = 2
	p := impl.NewModbusFunctionProber(dev, impl.Limits{MaxRequests: 32, MaxSpan: 512})
	if _, err := p.Probe(holdingPlan(1, 108, 109, 110)); err != nil {
		t.Fatalf("probe unit 1: %v", err)
	}
	if _, err := p.Probe(holdingPlan(2, 200, 201, 202)); err != nil {
		t.Fatalf("probe unit 2: %v", err)
	}
	report, ok := p.UnitReport(1)
	if !ok {
		t.Fatalf("unit 1 report must exist")
	}
	if report.Requests != 3 || report.Findings != 1 || report.OutOfRange != 1 {
		t.Fatalf("unit 1 report = %+v, want 3 requests and one out of range finding", report)
	}
	clean, ok := p.UnitReport(2)
	if !ok {
		t.Fatalf("unit 2 report must exist")
	}
	if clean.Requests != 3 || clean.Findings != 0 {
		t.Fatalf("unit 2 report = %+v, want 3 requests and no findings", clean)
	}
	if _, ok := p.UnitReport(9); ok {
		t.Fatalf("unit 9 was never probed and must not have a report")
	}
	if got := len(p.Findings()); got != 1 {
		t.Fatalf("global findings = %d, want 1", got)
	}
}
