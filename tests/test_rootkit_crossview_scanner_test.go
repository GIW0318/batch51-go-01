package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-29"
)

type scriptedCollector struct {
	mu     sync.Mutex
	rounds map[string][][]impl.Object
	idx    map[string]int
}

func newScriptedCollector(rounds map[string][][]impl.Object) *scriptedCollector {
	return &scriptedCollector{rounds: rounds, idx: map[string]int{}}
}

func (c *scriptedCollector) Collect(kind string, now int64) ([]impl.Object, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.rounds[kind]
	if len(list) == 0 {
		return nil, nil
	}
	i := c.idx[kind]
	if i >= len(list) {
		i = len(list) - 1
	}
	c.idx[kind] = i + 1
	out := make([]impl.Object, len(list[i]))
	copy(out, list[i])
	return out, nil
}

func obj(kind, key, name string, seen int64) impl.Object {
	return impl.Object{Kind: kind, Key: key, Name: name, SeenAt: seen}
}

func testPolicy() impl.Policy {
	return impl.Policy{
		TimeWindow:        1000,
		MaxObjectsPerKind: 16,
		MaxRecollect:      2,
		Now:               func() int64 { return 5000 },
	}
}

func TestEnumerateReturnsBothViewsWithWindow(t *testing.T) {
	direct := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:2/start:2", "beta", 200), obj(impl.KindProcesses, "pid:1/start:1", "alpha", 100)}},
	})
	conv := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1/start:1", "alpha", 150), obj(impl.KindProcesses, "pid:2/start:2", "beta", 250)}},
	})
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{Direct: direct, Conventional: conv}, testPolicy())
	view, err := s.Enumerate(impl.KindProcesses)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if view.Kind != impl.KindProcesses {
		t.Fatalf("view kind = %q, want %q", view.Kind, impl.KindProcesses)
	}
	if len(view.Direct) != 2 || len(view.Conventional) != 2 {
		t.Fatalf("view sides = %d/%d objects, want 2/2", len(view.Direct), len(view.Conventional))
	}
	if view.Direct[0].Key != "pid:1/start:1" || view.Direct[1].Key != "pid:2/start:2" {
		t.Fatalf("direct side is not sorted by key: %+v", view.Direct)
	}
	if view.WindowStart != 100 || view.WindowEnd != 250 {
		t.Fatalf("window = %d..%d, want 100..250", view.WindowStart, view.WindowEnd)
	}
	if view.Recollects != 0 || view.Truncated {
		t.Fatalf("unexpected recollects=%d truncated=%v", view.Recollects, view.Truncated)
	}
	stats := s.Stats()
	if stats.Enumerations != 1 || stats.Recollects != 0 {
		t.Fatalf("stats = %+v, want one enumeration and no recollect", stats)
	}
}

func TestEnumerateRejectsUnknownKind(t *testing.T) {
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{
		Direct:       newScriptedCollector(nil),
		Conventional: newScriptedCollector(nil),
	}, testPolicy())
	if _, err := s.Enumerate("threads"); !errors.Is(err, impl.ErrUnknownKind) {
		t.Fatalf("unknown kind must fail with ErrUnknownKind, got %v", err)
	}
}

func TestEnumerateMarksTruncatedAtCapacity(t *testing.T) {
	direct := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:3", "c", 10), obj(impl.KindProcesses, "pid:1", "a", 10), obj(impl.KindProcesses, "pid:2", "b", 10)}},
	})
	conv := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1", "a", 10), obj(impl.KindProcesses, "pid:2", "b", 10), obj(impl.KindProcesses, "pid:3", "c", 10)}},
	})
	policy := testPolicy()
	policy.MaxObjectsPerKind = 2
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{Direct: direct, Conventional: conv}, policy)
	view, err := s.Enumerate(impl.KindProcesses)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if !view.Truncated {
		t.Fatalf("view must be marked truncated when a side exceeds the per kind cap")
	}
	if len(view.Direct) != 2 || len(view.Conventional) != 2 {
		t.Fatalf("truncated sides = %d/%d, want 2/2", len(view.Direct), len(view.Conventional))
	}
	if view.Direct[0].Key != "pid:1" || view.Direct[1].Key != "pid:2" {
		t.Fatalf("truncation must keep the lowest keys, got %+v", view.Direct)
	}
}

func TestEnumerateRecollectsOutsideWindow(t *testing.T) {
	direct := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {
			{obj(impl.KindProcesses, "pid:1", "a", 0), obj(impl.KindProcesses, "pid:2", "b", 1000)},
			{obj(impl.KindProcesses, "pid:1", "a", 100), obj(impl.KindProcesses, "pid:2", "b", 150)},
		},
	})
	conv := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1", "a", 120)}},
	})
	policy := testPolicy()
	policy.TimeWindow = 100
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{Direct: direct, Conventional: conv}, policy)
	view, err := s.Enumerate(impl.KindProcesses)
	if err != nil {
		t.Fatalf("enumerate with one recollect: %v", err)
	}
	if view.Recollects != 1 {
		t.Fatalf("view recollects = %d, want 1", view.Recollects)
	}
	if view.WindowStart != 100 || view.WindowEnd != 150 {
		t.Fatalf("window after recollect = %d..%d, want 100..150", view.WindowStart, view.WindowEnd)
	}
	stats := s.Stats()
	if stats.Recollects != 1 || stats.Enumerations != 1 {
		t.Fatalf("stats = %+v, want one recollect and one enumeration", stats)
	}
}

func TestEnumerateFailsWhenWindowNeverMet(t *testing.T) {
	direct := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1", "a", 0), obj(impl.KindProcesses, "pid:2", "b", 1000)}},
	})
	conv := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1", "a", 10)}},
	})
	policy := testPolicy()
	policy.TimeWindow = 100
	policy.MaxRecollect = 1
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{Direct: direct, Conventional: conv}, policy)
	if _, err := s.Enumerate(impl.KindProcesses); !errors.Is(err, impl.ErrWindowExceeded) {
		t.Fatalf("window that never closes must fail with ErrWindowExceeded, got %v", err)
	}
	if got := s.Stats().Indeterminate; got != 1 {
		t.Fatalf("indeterminate counter = %d, want 1", got)
	}
}

func TestDiffFindsHiddenProcess(t *testing.T) {
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{}, testPolicy())
	view := impl.View{
		Kind: impl.KindProcesses,
		Direct: []impl.Object{
			obj(impl.KindProcesses, "pid:42/start:1000", "worker", 100),
		},
		Conventional: []impl.Object{
			obj(impl.KindProcesses, "pid:42/start:1000", "worker", 110),
			obj(impl.KindProcesses, "pid:77/start:3000", "daemon", 120),
		},
	}
	report, err := s.Diff(view)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(report.Items) != 1 {
		t.Fatalf("diff items = %+v, want exactly one hidden process", report.Items)
	}
	item := report.Items[0]
	if item.Kind != impl.KindProcesses || item.Key != "pid:77/start:3000" {
		t.Fatalf("item = %+v, want the conventional only process", item)
	}
	if item.Name != "daemon" {
		t.Fatalf("item name = %q, want daemon", item.Name)
	}
	if item.MissingFrom != "direct" {
		t.Fatalf("missing from = %q, want direct", item.MissingFrom)
	}
	if report.HiddenCount != 1 || report.PhantomCount != 0 {
		t.Fatalf("counts hidden=%d phantom=%d, want 1/0", report.HiddenCount, report.PhantomCount)
	}
}

func TestDiffFindsHiddenModule(t *testing.T) {
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{}, testPolicy())
	view := impl.View{
		Kind: impl.KindModules,
		Direct: []impl.Object{
			obj(impl.KindModules, "mod:0xffffffffc0a00000/.text", "driver", 400),
		},
		Conventional: []impl.Object{
			obj(impl.KindModules, "mod:0xffffffffc0a00000/.text", "driver", 410),
			obj(impl.KindModules, "mod:0xffffffffc0b00000/.data", "rootkit", 420),
		},
	}
	report, err := s.Diff(view)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(report.Items) != 1 || report.Items[0].Key != "mod:0xffffffffc0b00000/.data" {
		t.Fatalf("items = %+v, want the conventional only module", report.Items)
	}
	if report.Items[0].MissingFrom != "direct" || report.Items[0].Kind != impl.KindModules {
		t.Fatalf("item = %+v, want a module missing from the direct view", report.Items[0])
	}
	if report.HiddenCount != 1 {
		t.Fatalf("hidden count = %d, want 1", report.HiddenCount)
	}
}

func TestDiffOrdersItemsDeterministically(t *testing.T) {
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{}, testPolicy())
	view := impl.View{
		Kind: impl.KindProcesses,
		Direct: []impl.Object{
			obj(impl.KindModules, "mod:0x1000/.text", "visible", 10),
			obj(impl.KindListeners, "tcp:0.0.0.0:4444/inode:9", "sock", 10),
		},
		Conventional: []impl.Object{
			obj(impl.KindModules, "mod:0x2000/.data", "hidden", 20),
			obj(impl.KindProcesses, "pid:5/start:9", "ghost", 20),
		},
	}
	report, err := s.Diff(view)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	wantKinds := []string{"listeners", "modules", "modules", "processes"}
	wantKeys := []string{
		"tcp:0.0.0.0:4444/inode:9",
		"mod:0x1000/.text",
		"mod:0x2000/.data",
		"pid:5/start:9",
	}
	if len(report.Items) != len(wantKeys) {
		t.Fatalf("items = %+v, want %d entries", report.Items, len(wantKeys))
	}
	for i := range wantKeys {
		if report.Items[i].Kind != wantKinds[i] || report.Items[i].Key != wantKeys[i] {
			t.Fatalf("item %d = %s/%s, want %s/%s", i, report.Items[i].Kind, report.Items[i].Key, wantKinds[i], wantKeys[i])
		}
	}
	if report.HiddenCount != 2 || report.PhantomCount != 2 {
		t.Fatalf("counts hidden=%d phantom=%d, want 2/2", report.HiddenCount, report.PhantomCount)
	}
}

func TestDiffOfIdenticalViewsIsEmpty(t *testing.T) {
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{}, testPolicy())
	view := impl.View{
		Kind: impl.KindListeners,
		Direct: []impl.Object{
			obj(impl.KindListeners, "tcp:0.0.0.0:22/inode:3", "sshd", 10),
		},
		Conventional: []impl.Object{
			obj(impl.KindListeners, "tcp:0.0.0.0:22/inode:3", "sshd", 12),
		},
	}
	report, err := s.Diff(view)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(report.Items) != 0 || report.HiddenCount != 0 || report.PhantomCount != 0 {
		t.Fatalf("identical views produced %+v", report)
	}
}

func TestDiffTreatsSamePIDWithDifferentStartAsDistinct(t *testing.T) {
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{}, testPolicy())
	view := impl.View{
		Kind: impl.KindProcesses,
		Direct: []impl.Object{
			obj(impl.KindProcesses, "pid:42/start:1000", "old", 10),
		},
		Conventional: []impl.Object{
			obj(impl.KindProcesses, "pid:42/start:2000", "new", 20),
		},
	}
	report, err := s.Diff(view)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(report.Items) != 2 {
		t.Fatalf("items = %+v, want two distinct objects", report.Items)
	}
	if report.Items[0].Key != "pid:42/start:1000" || report.Items[0].MissingFrom != "conventional" {
		t.Fatalf("first item = %+v, want the direct only old process", report.Items[0])
	}
	if report.Items[1].Key != "pid:42/start:2000" || report.Items[1].MissingFrom != "direct" {
		t.Fatalf("second item = %+v, want the conventional only new process", report.Items[1])
	}
	if report.HiddenCount != 1 || report.PhantomCount != 1 {
		t.Fatalf("counts hidden=%d phantom=%d, want 1/1", report.HiddenCount, report.PhantomCount)
	}
}

func TestAttributeMarksHiddenFromDirect(t *testing.T) {
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{}, testPolicy())
	hidden := impl.DiffItem{Kind: impl.KindProcesses, Key: "pid:77/start:3000", Name: "daemon", MissingFrom: "direct"}
	attr, err := s.Attribute(hidden)
	if err != nil {
		t.Fatalf("attribute hidden item: %v", err)
	}
	if attr.Verdict != impl.VerdictHiddenFromDirect {
		t.Fatalf("verdict = %q, want %q", attr.Verdict, impl.VerdictHiddenFromDirect)
	}
	if attr.Severity != 80 || attr.Key != hidden.Key || attr.Kind != hidden.Kind {
		t.Fatalf("attribution = %+v, want severity 80 for the hidden object", attr)
	}
	phantom := impl.DiffItem{Kind: impl.KindProcesses, Key: "pid:42/start:1000", MissingFrom: "conventional"}
	attr, err = s.Attribute(phantom)
	if err != nil {
		t.Fatalf("attribute phantom item: %v", err)
	}
	if attr.Verdict != impl.VerdictOnlyInDirect || attr.Severity != 30 {
		t.Fatalf("attribution = %+v, want only_in_direct with severity 30", attr)
	}
}

func TestAttributeRejectsUnknownItem(t *testing.T) {
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{}, testPolicy())
	_, err := s.Attribute(impl.DiffItem{Kind: "threads", Key: "tid:9", MissingFrom: "direct"})
	if !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("unknown kind must fail with ErrUnknownItem, got %v", err)
	}
}

func TestScanIsIdempotentAcrossRepeats(t *testing.T) {
	direct := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1/start:1", "init", 100)}},
		impl.KindModules:   {{obj(impl.KindModules, "mod:0x1000/.text", "core", 100)}},
		impl.KindListeners: {{}},
	})
	conv := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {
			{obj(impl.KindProcesses, "pid:1/start:1", "init", 110), obj(impl.KindProcesses, "pid:9/start:2", "ghost", 120)},
		},
		impl.KindModules:   {{obj(impl.KindModules, "mod:0x1000/.text", "core", 110)}},
		impl.KindListeners: {{obj(impl.KindListeners, "tcp:0.0.0.0:9000/inode:8", "backdoor", 130)}},
	})
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{Direct: direct, Conventional: conv}, testPolicy())
	first, err := s.Scan()
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if first.NewFindings != 2 {
		t.Fatalf("first scan added %d findings, want 2", first.NewFindings)
	}
	second, err := s.Scan()
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if second.NewFindings != 0 {
		t.Fatalf("second scan added %d duplicate findings, want 0", second.NewFindings)
	}
	if got := len(s.Findings()); got != 2 {
		t.Fatalf("findings = %d, want 2", got)
	}
	if got := s.Stats().Scans; got != 2 {
		t.Fatalf("scan counter = %d, want 2", got)
	}
}

func TestSnapshotRestoreKeepsFindings(t *testing.T) {
	direct := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1/start:1", "init", 100)}},
		impl.KindModules:   {{}},
		impl.KindListeners: {{}},
	})
	conv := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1/start:1", "init", 110), obj(impl.KindProcesses, "pid:9/start:2", "ghost", 120)}},
		impl.KindModules:   {{}},
		impl.KindListeners: {{}},
	})
	collectors := impl.Collectors{Direct: direct, Conventional: conv}
	s := impl.NewRootkitCrossViewScanner(collectors, testPolicy())
	if _, err := s.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewRootkitCrossViewScanner(collectors, testPolicy())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	findings := restored.Findings()
	if len(findings) != 1 {
		t.Fatalf("restored findings = %+v, want one entry", findings)
	}
	if findings[0].Key != "pid:9/start:2" || findings[0].Verdict != impl.VerdictHiddenFromDirect {
		t.Fatalf("restored finding = %+v, want the hidden process", findings[0])
	}
	if err := restored.Restore([]byte("{broken-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(restored.Findings()); got != 1 {
		t.Fatalf("rejected restore dropped findings, now %d", got)
	}
}

func TestConcurrentScansSerialized(t *testing.T) {
	direct := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1/start:1", "init", 100)}},
		impl.KindModules:   {{}},
		impl.KindListeners: {{}},
	})
	conv := newScriptedCollector(map[string][][]impl.Object{
		impl.KindProcesses: {{obj(impl.KindProcesses, "pid:1/start:1", "init", 110), obj(impl.KindProcesses, "pid:9/start:2", "ghost", 120)}},
		impl.KindModules:   {{}},
		impl.KindListeners: {{}},
	})
	s := impl.NewRootkitCrossViewScanner(impl.Collectors{Direct: direct, Conventional: conv}, testPolicy())
	var wg sync.WaitGroup
	var mu sync.Mutex
	added := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Scan()
			if err != nil {
				t.Errorf("concurrent scan: %v", err)
				return
			}
			mu.Lock()
			added += result.NewFindings
			mu.Unlock()
		}()
	}
	wg.Wait()
	if got := s.Stats().Scans; got != 8 {
		t.Fatalf("scan counter = %d, want 8", got)
	}
	if got := len(s.Findings()); got != 1 {
		t.Fatalf("findings = %d, want one deduplicated entry", got)
	}
	if added != 1 {
		t.Fatalf("scans reported %d new findings in total, want 1", added)
	}
}
