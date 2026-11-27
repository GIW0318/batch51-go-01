package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-r-07"
)

func testAllowConfig() impl.AllowlistConfig {
	return impl.AllowlistConfig{MaxDepth: 6, MaxNodes: 16, MaxAudit: 8}
}

func newTestAllowGuard(t *testing.T, cfg impl.AllowlistConfig) *impl.DeserializationAllowlistGuard {
	t.Helper()
	g, err := impl.NewDeserializationAllowlistGuard(cfg)
	if err != nil {
		t.Fatalf("building the guard must succeed, got %v", err)
	}
	if g == nil {
		t.Fatalf("guard must not be nil")
	}
	return g
}

// chainPayload builds a linear type chain: each node points at the next one.
func chainPayload(names ...string) impl.Payload {
	nodes := make([]impl.TypeNode, 0, len(names))
	for i, name := range names {
		fields := map[string]string{}
		if i+1 < len(names) {
			fields["next"] = names[i+1]
		}
		nodes = append(nodes, impl.TypeNode{Name: name, Fields: fields})
	}
	return impl.Payload{Root: names[0], Nodes: nodes}
}

const allowedRawPayload = `{"root":"pkg.Order","nodes":[{"name":"pkg.Order","fields":{"line":"pkg.Line"}},{"name":"pkg.Line","fields":{"sku":"pkg.Sku"}},{"name":"pkg.Sku","fields":{}}]}`

const deniedRawPayload = `{"root":"evil.Probe","nodes":[{"name":"evil.Probe","fields":{}}]}`

func TestNewGuardRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  impl.AllowlistConfig
	}{
		{"depth cap", impl.AllowlistConfig{MaxDepth: 0, MaxNodes: 8, MaxAudit: 4}},
		{"node cap", impl.AllowlistConfig{MaxDepth: 4, MaxNodes: -1, MaxAudit: 4}},
		{"audit cap", impl.AllowlistConfig{MaxDepth: 4, MaxNodes: 8, MaxAudit: 0}},
	}
	for _, c := range cases {
		if _, err := impl.NewDeserializationAllowlistGuard(c.cfg); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("bad %s must be rejected with ErrBadConfig, got %v", c.name, err)
		}
	}
	g := newTestAllowGuard(t, testAllowConfig())
	if got := len(g.Policy()); got != 0 {
		t.Fatalf("fresh guard allowlist size = %d, want 0", got)
	}
	if got := len(g.Audit()); got != 0 {
		t.Fatalf("fresh guard audit size = %d, want 0", got)
	}
	if got := g.MaxDepth(); got != 6 {
		t.Fatalf("max depth = %d, want 6", got)
	}
}

func TestAllowRejectsUnqualifiedName(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	if err := g.Allow("Request"); !errors.Is(err, impl.ErrBadTypeName) {
		t.Fatalf("an unqualified name must report ErrBadTypeName, got %v", err)
	}
	if err := g.Allow("   "); !errors.Is(err, impl.ErrBadTypeName) {
		t.Fatalf("a blank name must report ErrBadTypeName, got %v", err)
	}
	if err := g.Allow("pkg.Request"); err != nil {
		t.Fatalf("admitting a qualified name: %v", err)
	}
	if err := g.Allow("pkg.Request"); err != nil {
		t.Fatalf("admitting the same name twice must be idempotent, got %v", err)
	}
	if err := g.Allow("zzz.Second"); err != nil {
		t.Fatalf("admitting the second name: %v", err)
	}
	policy := g.Policy()
	if len(policy) != 2 {
		t.Fatalf("allowlist size = %d, want 2", len(policy))
	}
	if policy[0] != "pkg.Request" || policy[1] != "zzz.Second" {
		t.Fatalf("allowlist = %v, want ascending order [pkg.Request zzz.Second]", policy)
	}
	if got := g.Stats().AllowedTypes; got != 2 {
		t.Fatalf("allowed type counter = %d, want 2", got)
	}
}

func TestDropRemovesPolicyEntry(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	if err := g.Allow("pkg.Request"); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if err := g.Allow("pkg.Second"); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if err := g.Drop("pkg.Request"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	policy := g.Policy()
	if len(policy) != 1 || policy[0] != "pkg.Second" {
		t.Fatalf("allowlist = %v, want only pkg.Second", policy)
	}
	if err := g.Drop("pkg.Request"); !errors.Is(err, impl.ErrUnknownType) {
		t.Fatalf("dropping a missing name must report ErrUnknownType, got %v", err)
	}
	if err := g.Drop("Request"); !errors.Is(err, impl.ErrBadTypeName) {
		t.Fatalf("dropping an unqualified name must report ErrBadTypeName, got %v", err)
	}
	if got := g.Stats().AllowedTypes; got != 1 {
		t.Fatalf("allowed type counter = %d, want 1", got)
	}
}

func TestParseRejectsInvalidJSON(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	if _, err := g.Parse([]byte("{not a payload")); !errors.Is(err, impl.ErrBadPayload) {
		t.Fatalf("invalid json must report ErrBadPayload, got %v", err)
	}
	if _, err := g.Parse(nil); !errors.Is(err, impl.ErrBadPayload) {
		t.Fatalf("empty input must report ErrBadPayload, got %v", err)
	}
	p, err := g.Parse([]byte(allowedRawPayload))
	if err != nil {
		t.Fatalf("parsing a well formed payload: %v", err)
	}
	if p.Root != "pkg.Order" {
		t.Fatalf("parsed root = %q, want pkg.Order", p.Root)
	}
	if len(p.Nodes) != 3 {
		t.Fatalf("parsed node count = %d, want 3", len(p.Nodes))
	}
}

func TestParseRejectsEmptyRootAndDuplicateType(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	if _, err := g.Parse([]byte(`{"root":"","nodes":[]}`)); !errors.Is(err, impl.ErrBadRoot) {
		t.Fatalf("an empty root must report ErrBadRoot, got %v", err)
	}
	dup := `{"root":"a.B","nodes":[{"name":"a.B","fields":{}},{"name":"a.B","fields":{}}]}`
	if _, err := g.Parse([]byte(dup)); !errors.Is(err, impl.ErrDuplicateType) {
		t.Fatalf("a duplicated type must report ErrDuplicateType, got %v", err)
	}
}

func TestBuildGraphRejectsMissingRoot(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	p := impl.Payload{
		Root:  "pkg.Missing",
		Nodes: []impl.TypeNode{{Name: "pkg.Real", Fields: map[string]string{}}},
	}
	if _, err := g.BuildGraph(p); !errors.Is(err, impl.ErrMissingRoot) {
		t.Fatalf("a root outside the payload must report ErrMissingRoot, got %v", err)
	}
}

func TestBuildGraphRejectsUnknownFieldType(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	plain := impl.Payload{
		Root:  "pkg.A",
		Nodes: []impl.TypeNode{{Name: "pkg.A", Fields: map[string]string{"body": "pkg.Nope"}}},
	}
	if _, err := g.BuildGraph(plain); !errors.Is(err, impl.ErrUnknownType) {
		t.Fatalf("an undeclared plain field type must report ErrUnknownType, got %v", err)
	}
	decorated := impl.Payload{
		Root:  "pkg.A",
		Nodes: []impl.TypeNode{{Name: "pkg.A", Fields: map[string]string{"items": "[]pkg.Nope"}}},
	}
	if _, err := g.BuildGraph(decorated); !errors.Is(err, impl.ErrUnknownType) {
		t.Fatalf("an undeclared slice field type must report ErrUnknownType, got %v", err)
	}
	good := impl.Payload{
		Root: "pkg.A",
		Nodes: []impl.TypeNode{
			{Name: "pkg.A", Fields: map[string]string{"items": "[]pkg.B"}},
			{Name: "pkg.B", Fields: map[string]string{}},
		},
	}
	graph, err := g.BuildGraph(good)
	if err != nil {
		t.Fatalf("a fully declared graph must build, got %v", err)
	}
	if graph.Root != "pkg.A" {
		t.Fatalf("graph root = %q, want pkg.A", graph.Root)
	}
	if len(graph.Order) != 2 {
		t.Fatalf("graph order length = %d, want 2", len(graph.Order))
	}
	if _, ok := graph.Nodes["pkg.B"]; !ok {
		t.Fatalf("graph lost the pkg.B node")
	}
}

func TestAdjudicateAllowsFullyAllowedGraph(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	for _, name := range []string{"pkg.A", "pkg.B", "pkg.C"} {
		if err := g.Allow(name); err != nil {
			t.Fatalf("allow %s: %v", name, err)
		}
	}
	graph, err := g.BuildGraph(chainPayload("pkg.A", "pkg.B", "pkg.C"))
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	d, err := g.Adjudicate(graph)
	if err != nil {
		t.Fatalf("adjudicate: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("a fully allowed graph must pass, got reason %q on %q", d.Reason, d.Type)
	}
	if d.Reason != impl.ReasonTypesAllowed {
		t.Fatalf("reason = %q, want %q", d.Reason, impl.ReasonTypesAllowed)
	}
	if d.Visited != 3 {
		t.Fatalf("visited types = %d, want 3", d.Visited)
	}
	if d.Depth != 3 {
		t.Fatalf("reached depth = %d, want 3", d.Depth)
	}
	if d.Seq <= 0 {
		t.Fatalf("decision seq = %d, want a positive number", d.Seq)
	}
	if got := g.Stats().Allowed; got != 1 {
		t.Fatalf("allowed counter = %d, want 1", got)
	}
	if got := len(g.Audit()); got != 1 {
		t.Fatalf("audit size = %d, want 1", got)
	}
}

func TestAdjudicateDeniesUnlistedRoot(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	if err := g.Allow("pkg.B"); err != nil {
		t.Fatalf("allow pkg.B: %v", err)
	}
	graph, err := g.BuildGraph(chainPayload("pkg.A", "pkg.B"))
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	d, err := g.Adjudicate(graph)
	if err != nil {
		t.Fatalf("adjudicate: %v", err)
	}
	if d.Allowed {
		t.Fatalf("a root outside the allowlist must be denied")
	}
	if d.Reason != impl.ReasonRootDenied {
		t.Fatalf("reason = %q, want %q", d.Reason, impl.ReasonRootDenied)
	}
	if d.Type != "pkg.A" {
		t.Fatalf("offending type = %q, want pkg.A", d.Type)
	}
	if got := g.Stats().Denied; got != 1 {
		t.Fatalf("denied counter = %d, want 1", got)
	}
	records := g.Audit()
	if len(records) != 1 || records[0].Allowed || records[0].Root != "pkg.A" {
		t.Fatalf("audit = %+v, want one denied record for pkg.A", records)
	}
}

func TestAdjudicateDeniesDepthBeyondLimit(t *testing.T) {
	cfg := impl.AllowlistConfig{MaxDepth: 3, MaxNodes: 16, MaxAudit: 8}
	g := newTestAllowGuard(t, cfg)
	for _, name := range []string{"pkg.A", "pkg.B", "pkg.C", "pkg.D"} {
		if err := g.Allow(name); err != nil {
			t.Fatalf("allow %s: %v", name, err)
		}
	}
	graph, err := g.BuildGraph(chainPayload("pkg.A", "pkg.B", "pkg.C", "pkg.D"))
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	d, err := g.Adjudicate(graph)
	if err != nil {
		t.Fatalf("adjudicate: %v", err)
	}
	if d.Allowed {
		t.Fatalf("a graph deeper than the cap must be denied")
	}
	if d.Reason != impl.ReasonDepthExceeded {
		t.Fatalf("reason = %q, want %q", d.Reason, impl.ReasonDepthExceeded)
	}
	if d.Type != "pkg.D" {
		t.Fatalf("offending type = %q, want pkg.D at depth 4", d.Type)
	}
	if d.Depth != 4 {
		t.Fatalf("reported depth = %d, want 4", d.Depth)
	}
}

func TestBuildGraphEnforcesNodeCap(t *testing.T) {
	cfg := impl.AllowlistConfig{MaxDepth: 6, MaxNodes: 2, MaxAudit: 8}
	g := newTestAllowGuard(t, cfg)
	graph := chainPayload("pkg.A", "pkg.B", "pkg.C")
	if _, err := g.BuildGraph(graph); !errors.Is(err, impl.ErrGraphTooLarge) {
		t.Fatalf("a graph over the node cap must report ErrGraphTooLarge, got %v", err)
	}
	if _, err := g.BuildGraph(chainPayload("pkg.A", "pkg.B")); err != nil {
		t.Fatalf("a graph exactly at the node cap must build, got %v", err)
	}
}

func TestCheckRecordsAuditWithinCap(t *testing.T) {
	cfg := impl.AllowlistConfig{MaxDepth: 6, MaxNodes: 16, MaxAudit: 3}
	g := newTestAllowGuard(t, cfg)
	for _, name := range []string{"pkg.Order", "pkg.Line", "pkg.Sku"} {
		if err := g.Allow(name); err != nil {
			t.Fatalf("allow %s: %v", name, err)
		}
	}
	seqs := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		raw := []byte(allowedRawPayload)
		wantAllowed := true
		if i%2 == 1 {
			raw = []byte(deniedRawPayload)
			wantAllowed = false
		}
		d, err := g.Check(raw)
		if err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
		if d.Allowed != wantAllowed {
			t.Fatalf("check %d allowed = %v, want %v", i, d.Allowed, wantAllowed)
		}
		seqs = append(seqs, d.Seq)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("decision sequence must keep growing, got %v", seqs)
		}
	}
	audit := g.Audit()
	if len(audit) != 3 {
		t.Fatalf("retained audit records = %d, want the configured cap 3", len(audit))
	}
	for i, want := range seqs[2:] {
		if audit[i].Seq != want {
			t.Fatalf("retained record %d has seq %d, want the newest window %d", i, audit[i].Seq, want)
		}
	}
	stats := g.Stats()
	if stats.Checks != 5 || stats.Allowed != 3 || stats.Denied != 2 {
		t.Fatalf("stats = %+v, want checks=5 allowed=3 denied=2", stats)
	}
}

func TestConcurrentCheckIsStable(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	for _, name := range []string{"pkg.Order", "pkg.Line", "pkg.Sku"} {
		if err := g.Allow(name); err != nil {
			t.Fatalf("allow %s: %v", name, err)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed, denied := 0, 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := g.Check([]byte(allowedRawPayload))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("concurrent check: %v", err)
				return
			}
			if d.Allowed {
				allowed++
			} else {
				denied++
			}
		}()
	}
	wg.Wait()
	if allowed != 32 || denied != 0 {
		t.Fatalf("concurrent checks allowed=%d denied=%d, want 32 and 0", allowed, denied)
	}
	if got := g.Stats().Checks; got != 32 {
		t.Fatalf("check counter = %d, want 32", got)
	}
}

func TestSnapshotRestoreKeepsAllowlist(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	for _, name := range []string{"pkg.Order", "pkg.Line", "pkg.Sku"} {
		if err := g.Allow(name); err != nil {
			t.Fatalf("allow %s: %v", name, err)
		}
	}
	first, err := g.Check([]byte(allowedRawPayload))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newTestAllowGuard(t, testAllowConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Policy()); got != 3 {
		t.Fatalf("restored allowlist size = %d, want 3", got)
	}
	if got := restored.Stats().Checks; got != 1 {
		t.Fatalf("restored check counter = %d, want 1", got)
	}
	second, err := restored.Check([]byte(allowedRawPayload))
	if err != nil {
		t.Fatalf("check after restore: %v", err)
	}
	if !second.Allowed {
		t.Fatalf("restored allowlist stopped admitting the payload")
	}
	if second.Seq <= first.Seq {
		t.Fatalf("sequence after restore = %d, must continue past %d", second.Seq, first.Seq)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newTestAllowGuard(t, testAllowConfig())
	for _, name := range []string{"pkg.Order", "pkg.Line", "pkg.Sku"} {
		if err := g.Allow(name); err != nil {
			t.Fatalf("allow %s: %v", name, err)
		}
	}
	if _, err := g.Check([]byte(allowedRawPayload)); err != nil {
		t.Fatalf("check: %v", err)
	}
	if err := g.Restore([]byte("<<not a snapshot>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if got := len(g.Policy()); got != 3 {
		t.Fatalf("failed restore changed the allowlist size to %d, want 3", got)
	}
	if got := g.Stats().Checks; got != 1 {
		t.Fatalf("failed restore changed the check counter to %d, want 1", got)
	}
	if got := len(g.Audit()); got != 1 {
		t.Fatalf("failed restore changed the audit size to %d, want 1", got)
	}
}
