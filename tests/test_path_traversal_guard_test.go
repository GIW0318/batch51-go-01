package task

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	impl "gsb/batch51-go-safety-r-09"
)

const testRoot = "/srv/www"

func guardOptions() impl.GuardOptions {
	return impl.GuardOptions{MaxPathLen: 4096, MaxLinkHops: 4, AuditCap: 32, CaseInsensitive: false}
}

func newGuard(t *testing.T, opts impl.GuardOptions) *impl.PathTraversalGuard {
	t.Helper()
	g, err := impl.NewPathTraversalGuard(testRoot, opts)
	if err != nil {
		t.Fatalf("NewPathTraversalGuard(%q): %v", testRoot, err)
	}
	if g == nil {
		t.Fatalf("NewPathTraversalGuard(%q) returned nil", testRoot)
	}
	return g
}

func lastEntry(t *testing.T, g *impl.PathTraversalGuard) impl.AuditEntry {
	t.Helper()
	entries := g.Audit()
	if len(entries) == 0 {
		t.Fatalf("audit ledger is empty, want at least one recorded decision")
	}
	return entries[len(entries)-1]
}

func TestNormalizeCollapsesDotAndEmptySegments(t *testing.T) {
	g := newGuard(t, guardOptions())
	cases := []struct{ raw, want string }{
		{"./a//b/./c/", "a/b/c"},
		{"a\\b\\c", "a/b/c"},
		{"docs", "docs"},
		{"a/./b/../c//", "a/c"},
		{"a/b/", "a/b"},
	}
	for _, c := range cases {
		got, err := g.Normalize(c.raw)
		if err != nil {
			t.Fatalf("Normalize(%q) returned %v, want %q", c.raw, err, c.want)
		}
		if got != c.want {
			t.Fatalf("Normalize(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestNormalizeRejectsParentEscapeAboveRoot(t *testing.T) {
	g := newGuard(t, guardOptions())
	for _, raw := range []string{"..", "../x", "a/../../b", "a/b/../../../c"} {
		if _, err := g.Normalize(raw); !errors.Is(err, impl.ErrEscapeAttempt) {
			t.Fatalf("Normalize(%q) must report ErrEscapeAttempt, got %v", raw, err)
		}
	}
	got, err := g.Normalize("a/../b")
	if err != nil {
		t.Fatalf("Normalize(%q) must succeed, got %v", "a/../b", err)
	}
	if got != "b" {
		t.Fatalf("Normalize(%q) = %q, want %q", "a/../b", got, "b")
	}
}

func TestNormalizeRejectsAbsoluteAndBlankInput(t *testing.T) {
	g := newGuard(t, guardOptions())
	absolute := []string{"/etc/passwd", `\\server\share`, `C:\Windows`, "C:/Windows", `\Windows`}
	for _, raw := range absolute {
		if _, err := g.Normalize(raw); !errors.Is(err, impl.ErrAbsolutePath) {
			t.Fatalf("Normalize(%q) must report ErrAbsolutePath, got %v", raw, err)
		}
	}
	for _, raw := range []string{"", "   ", "\t"} {
		if _, err := g.Normalize(raw); !errors.Is(err, impl.ErrEmptyPath) {
			t.Fatalf("Normalize(%q) must report ErrEmptyPath, got %v", raw, err)
		}
	}
}

func TestNormalizeEnforcesConfiguredPathLength(t *testing.T) {
	opts := guardOptions()
	opts.MaxPathLen = 12
	g := newGuard(t, opts)
	if got, err := g.Normalize("abcdefghijkl"); err != nil || got != "abcdefghijkl" {
		t.Fatalf("path exactly at the limit must pass, got %q and %v", got, err)
	}
	if got, err := g.Normalize("abcdefghijkl/./"); err != nil || got != "abcdefghijkl" {
		t.Fatalf("the limit is measured on the normalised form, got %q and %v", got, err)
	}
	if _, err := g.Normalize("abcdefghijklm"); !errors.Is(err, impl.ErrPathTooLong) {
		t.Fatalf("path above the limit must report ErrPathTooLong, got %v", err)
	}
	if _, err := g.Normalize("deep/abcdefghijklm"); !errors.Is(err, impl.ErrPathTooLong) {
		t.Fatalf("longer path must report ErrPathTooLong, got %v", err)
	}
}

func TestWithinRootRejectsSiblingDirectory(t *testing.T) {
	g := newGuard(t, guardOptions())
	for _, abs := range []string{"/srv/www", "/srv/www/a.txt", "/srv/www/deep/dir/file"} {
		ok, err := g.WithinRoot(abs)
		if err != nil {
			t.Fatalf("WithinRoot(%q) returned %v", abs, err)
		}
		if !ok {
			t.Fatalf("WithinRoot(%q) = false, want true", abs)
		}
	}
	ok, err := g.WithinRoot("/srv/www2/index.html")
	if err != nil {
		t.Fatalf("WithinRoot sibling returned %v", err)
	}
	if ok {
		t.Fatalf("WithinRoot(%q) = true, want false for a sibling sharing the root prefix", "/srv/www2/index.html")
	}
	if _, err := g.WithinRoot("docs/a.txt"); !errors.Is(err, impl.ErrAbsolutePath) {
		t.Fatalf("WithinRoot must reject a relative argument with ErrAbsolutePath, got %v", err)
	}
}

func TestResolveRecordsAllowDecisionInAudit(t *testing.T) {
	g := newGuard(t, guardOptions())
	rp, err := g.Resolve("docs/readme.md")
	if err != nil {
		t.Fatalf("Resolve must succeed for a plain relative path, got %v", err)
	}
	if rp.Request != "docs/readme.md" || rp.Clean != "docs/readme.md" {
		t.Fatalf("resolved request=%q clean=%q, want the original and canonical form", rp.Request, rp.Clean)
	}
	if rp.Real != "/srv/www/docs/readme.md" {
		t.Fatalf("resolved real path = %q, want %q", rp.Real, "/srv/www/docs/readme.md")
	}
	if !rp.Inside {
		t.Fatalf("resolved path must be marked inside the root")
	}
	if rp.Reason != impl.ReasonOK {
		t.Fatalf("resolved reason = %q, want %q", rp.Reason, impl.ReasonOK)
	}
	entries := g.Audit()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Seq != 1 {
		t.Fatalf("first audit sequence = %d, want 1", e.Seq)
	}
	if e.Decision != impl.DecisionAllow || e.Reason != impl.ReasonOK {
		t.Fatalf("audit decision=%q reason=%q, want allow/ok", e.Decision, e.Reason)
	}
	if e.Request != "docs/readme.md" || e.Clean != "docs/readme.md" || e.Real != "/srv/www/docs/readme.md" {
		t.Fatalf("audit entry does not carry the resolved paths: %+v", e)
	}
	if got := g.Rejections(); got != 0 {
		t.Fatalf("rejections = %d, want 0 after a single allowed request", got)
	}
}

func TestResolveDeniesEscapeAndCountsRejection(t *testing.T) {
	g := newGuard(t, guardOptions())
	if _, err := g.Resolve("../secret"); !errors.Is(err, impl.ErrEscapeAttempt) {
		t.Fatalf("escaping path must report ErrEscapeAttempt, got %v", err)
	}
	entries := g.Audit()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1 after one denied request", len(entries))
	}
	if entries[0].Decision != impl.DecisionDeny || entries[0].Reason != impl.ReasonParentEscape {
		t.Fatalf("denied entry decision=%q reason=%q, want deny/parent escape", entries[0].Decision, entries[0].Reason)
	}
	if got := g.Rejections(); got != 1 {
		t.Fatalf("rejections = %d, want 1", got)
	}
	if _, err := g.Resolve("a/b.txt"); err != nil {
		t.Fatalf("allowed request after a denial must succeed, got %v", err)
	}
	if got := g.Rejections(); got != 1 {
		t.Fatalf("rejections = %d after an allowed request, want it unchanged at 1", got)
	}
	if got := len(g.Audit()); got != 2 {
		t.Fatalf("audit entries = %d, want 2", got)
	}
	if last := lastEntry(t, g); last.Decision != impl.DecisionAllow {
		t.Fatalf("newest audit decision = %q, want %q", last.Decision, impl.DecisionAllow)
	}
}

func TestResolveLinkHopLimitStopsLoop(t *testing.T) {
	g := newGuard(t, guardOptions())
	var calls int32
	err := g.SetLinkResolver(func(p string) (string, error) {
		atomic.AddInt32(&calls, 1)
		if p == "/srv/www/a" {
			return "/srv/www/b", nil
		}
		return "/srv/www/a", nil
	})
	if err != nil {
		t.Fatalf("SetLinkResolver must accept a function, got %v", err)
	}
	if _, err := g.Resolve("a"); !errors.Is(err, impl.ErrLinkLoop) {
		t.Fatalf("a link chain that never settles must report ErrLinkLoop, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Fatalf("resolver called %d times, want exactly the hop limit 4", got)
	}
}

func TestOpenReturnsHandleWithResolvedPaths(t *testing.T) {
	g := newGuard(t, guardOptions())
	h, err := g.Open("data/report.csv", true)
	if err != nil {
		t.Fatalf("Open must succeed for an allowed path, got %v", err)
	}
	if h.Clean != "data/report.csv" {
		t.Fatalf("handle clean path = %q, want %q", h.Clean, "data/report.csv")
	}
	if h.Real != "/srv/www/data/report.csv" {
		t.Fatalf("handle real path = %q, want %q", h.Real, "/srv/www/data/report.csv")
	}
	if !h.ReadOnly {
		t.Fatalf("handle must carry the read only marker")
	}
	if h.Seq != 1 {
		t.Fatalf("handle sequence = %d, want the first audit sequence 1", h.Seq)
	}
	h2, err := g.Open("data//report.csv", false)
	if err != nil {
		t.Fatalf("second Open must succeed, got %v", err)
	}
	if h2.ReadOnly {
		t.Fatalf("second handle must be writable")
	}
	if h2.Seq != 2 {
		t.Fatalf("second handle sequence = %d, want 2", h2.Seq)
	}
	if h2.Clean != "data/report.csv" {
		t.Fatalf("second handle clean path = %q, want the canonical form", h2.Clean)
	}
}

func TestOpenDeniedProducesNoHandle(t *testing.T) {
	g := newGuard(t, guardOptions())
	h, err := g.Open("../etc/shadow", false)
	if !errors.Is(err, impl.ErrEscapeAttempt) {
		t.Fatalf("Open on an escaping path must report ErrEscapeAttempt, got %v", err)
	}
	if h != (impl.Handle{}) {
		t.Fatalf("denied Open returned a non zero handle: %+v", h)
	}
	if h.Seq != 0 {
		t.Fatalf("denied handle sequence = %d, want 0", h.Seq)
	}
	if got := g.Rejections(); got != 1 {
		t.Fatalf("rejections = %d, want 1", got)
	}
	if last := lastEntry(t, g); last.Decision != impl.DecisionDeny {
		t.Fatalf("newest audit decision = %q, want %q", last.Decision, impl.DecisionDeny)
	}
}

func TestAuditCapKeepsNewestEntries(t *testing.T) {
	opts := guardOptions()
	opts.AuditCap = 3
	g := newGuard(t, opts)
	steps := []struct {
		raw  string
		deny bool
	}{
		{"d/1.txt", false},
		{"../x", true},
		{"d/2.txt", false},
		{"../y", true},
		{"d/3.txt", false},
	}
	for _, s := range steps {
		_, err := g.Resolve(s.raw)
		if s.deny {
			if !errors.Is(err, impl.ErrEscapeAttempt) {
				t.Fatalf("Resolve(%q) must be denied with ErrEscapeAttempt, got %v", s.raw, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Resolve(%q) must be allowed, got %v", s.raw, err)
		}
	}
	entries := g.Audit()
	if len(entries) != 3 {
		t.Fatalf("retained audit entries = %d, want the configured cap 3", len(entries))
	}
	wantSeq := []int64{3, 4, 5}
	wantDecision := []string{impl.DecisionAllow, impl.DecisionDeny, impl.DecisionAllow}
	for i, e := range entries {
		if e.Seq != wantSeq[i] {
			t.Fatalf("retained entry %d has sequence %d, want %d", i, e.Seq, wantSeq[i])
		}
		if e.Decision != wantDecision[i] {
			t.Fatalf("retained entry %d has decision %q, want %q", i, e.Decision, wantDecision[i])
		}
	}
	if got := g.Rejections(); got != 2 {
		t.Fatalf("rejections = %d, want 2 even though the audit ledger was trimmed", got)
	}
}

func TestAuditSequenceSurvivesSnapshotRestore(t *testing.T) {
	opts := guardOptions()
	g := newGuard(t, opts)
	if _, err := g.Resolve("one.txt"); err != nil {
		t.Fatalf("Resolve one.txt: %v", err)
	}
	if _, err := g.Resolve("../two"); !errors.Is(err, impl.ErrEscapeAttempt) {
		t.Fatalf("Resolve ../two must be denied, got %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("Snapshot returned an empty payload")
	}
	restored := newGuard(t, opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore of a valid snapshot must succeed, got %v", err)
	}
	if got := len(restored.Audit()); got != 2 {
		t.Fatalf("restored audit entries = %d, want 2", got)
	}
	if got := restored.Rejections(); got != 1 {
		t.Fatalf("restored rejections = %d, want 1", got)
	}
	rp, err := restored.Resolve("three.txt")
	if err != nil {
		t.Fatalf("Resolve after restore: %v", err)
	}
	if rp.Real != "/srv/www/three.txt" {
		t.Fatalf("resolved real path after restore = %q, want %q", rp.Real, "/srv/www/three.txt")
	}
	if last := lastEntry(t, restored); last.Seq != 3 {
		t.Fatalf("sequence after restore = %d, want it to continue at 3", last.Seq)
	}
}

func TestRestoreRejectsCorruptSnapshotKeepsState(t *testing.T) {
	g := newGuard(t, guardOptions())
	if _, err := g.Resolve("keep.txt"); err != nil {
		t.Fatalf("Resolve keep.txt: %v", err)
	}
	if err := g.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if got := len(g.Audit()); got != 1 {
		t.Fatalf("audit entries after a rejected restore = %d, want the live ledger untouched at 1", got)
	}
	rp, err := g.Resolve("after.txt")
	if err != nil {
		t.Fatalf("Resolve after a rejected restore: %v", err)
	}
	if rp.Real != "/srv/www/after.txt" {
		t.Fatalf("resolved real path = %q, want %q", rp.Real, "/srv/www/after.txt")
	}
	if last := lastEntry(t, g); last.Seq != 2 {
		t.Fatalf("sequence after a rejected restore = %d, want 2", last.Seq)
	}
}

func TestConcurrentResolveKeepsAuditSequenceUnique(t *testing.T) {
	opts := guardOptions()
	opts.AuditCap = 512
	g := newGuard(t, opts)
	const workers = 8
	const rounds = 20
	var wg sync.WaitGroup
	var denyHits, unexpected int32
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if i%2 == 0 {
					if _, err := g.Resolve(fmt.Sprintf("ok/%02d/%02d.txt", w, i)); err != nil {
						atomic.AddInt32(&unexpected, 1)
					}
					continue
				}
				if _, err := g.Resolve(fmt.Sprintf("../bad/%02d/%02d", w, i)); errors.Is(err, impl.ErrEscapeAttempt) {
					atomic.AddInt32(&denyHits, 1)
				} else {
					atomic.AddInt32(&unexpected, 1)
				}
			}
		}(w)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&unexpected); got != 0 {
		t.Fatalf("%d concurrent requests did not behave as the path rules require", got)
	}
	if got := atomic.LoadInt32(&denyHits); got != workers*rounds/2 {
		t.Fatalf("denied requests = %d, want %d", got, workers*rounds/2)
	}
	if got := g.Rejections(); got != workers*rounds/2 {
		t.Fatalf("rejections = %d, want %d", got, workers*rounds/2)
	}
	entries := g.Audit()
	if len(entries) != workers*rounds {
		t.Fatalf("audit entries = %d, want %d", len(entries), workers*rounds)
	}
	seen := make(map[int64]bool, len(entries))
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			t.Fatalf("audit entry %d has sequence %d, want %d", i, e.Seq, i+1)
		}
		if seen[e.Seq] {
			t.Fatalf("sequence %d was handed out twice", e.Seq)
		}
		seen[e.Seq] = true
	}
}

func TestSetLimitsRejectInvalidValues(t *testing.T) {
	g := newGuard(t, guardOptions())
	if err := g.SetMaxPathLen(0); !errors.Is(err, impl.ErrBadLimit) {
		t.Fatalf("SetMaxPathLen(0) must report ErrBadLimit, got %v", err)
	}
	if _, err := g.Normalize("abcdefghij"); err != nil {
		t.Fatalf("a rejected limit change must keep the previous limit, got %v", err)
	}
	if err := g.SetMaxPathLen(8); err != nil {
		t.Fatalf("SetMaxPathLen(8) must succeed, got %v", err)
	}
	if _, err := g.Normalize("abcdefghij"); !errors.Is(err, impl.ErrPathTooLong) {
		t.Fatalf("lowered limit must apply immediately, got %v", err)
	}
	if err := g.SetLinkResolver(nil); !errors.Is(err, impl.ErrBadResolver) {
		t.Fatalf("SetLinkResolver(nil) must report ErrBadResolver, got %v", err)
	}
}
