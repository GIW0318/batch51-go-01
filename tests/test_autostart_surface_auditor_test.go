package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-c-05"
)

var auditBaseTime = time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

func auditConfig() impl.AuditConfig {
	return impl.AuditConfig{
		MaxEntries:    32,
		MaxBaselines:  4,
		MaxAllowances: 4,
		Protected:     []string{"/etc/systemd/system/sshd.service"},
	}
}

func unitItem(name, content string, mtime time.Time) impl.Item {
	return impl.Item{
		Source:  impl.SourceSystemdSystem,
		Path:    "/etc/systemd/system/" + name,
		Name:    name,
		Content: content,
		MTime:   mtime,
	}
}

func scanEntries(t *testing.T, a *impl.AutostartSurfaceAuditor, scope string) []impl.Entry {
	t.Helper()
	entries, err := a.Scan(scope)
	if err != nil {
		t.Fatalf("scan %q: %v", scope, err)
	}
	return entries
}

func TestRegisterRejectsUnknownSource(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	err := a.Register([]impl.Item{{Source: "registry_run", Path: "/etc/rc.local", Name: "rc.local", Content: "x", MTime: auditBaseTime}})
	if !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("register with an unknown source = %v, want ErrBadSource", err)
	}
	if got := len(a.Entries(impl.ScopeAll)); got != 0 {
		t.Fatalf("entries = %d, want 0 after a rejected batch", got)
	}
}

func TestRegisterRejectsBlankPath(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	err := a.Register([]impl.Item{{Source: impl.SourceCron, Path: "   ", Name: "job", Content: "* * * * * /bin/true", MTime: auditBaseTime}})
	if !errors.Is(err, impl.ErrBadItem) {
		t.Fatalf("register with a blank path = %v, want ErrBadItem", err)
	}
}

func TestScanReturnsEntriesWithStableIDs(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	if err := a.Register([]impl.Item{
		unitItem("backup.service", "ExecStart=/usr/bin/backup", auditBaseTime),
		{Source: impl.SourceCron, Path: "/etc/cron.d/nightly", Name: "nightly", Content: "0 3 * * * root /usr/bin/job", MTime: auditBaseTime},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	entries := scanEntries(t, a, impl.ScopeAll)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	ids := map[string]bool{}
	for _, e := range entries {
		if e.ID == "" {
			t.Fatalf("entry %q has an empty id", e.Name)
		}
		if e.Digest == "" {
			t.Fatalf("entry %q has an empty digest", e.Name)
		}
		ids[e.ID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("entry ids must be distinct, got %v", ids)
	}
	again := scanEntries(t, a, impl.ScopeAll)
	if len(again) != 2 || again[0].ID != entries[0].ID || again[1].ID != entries[1].ID {
		t.Fatalf("second scan = %+v, want the same ids as %+v", again, entries)
	}
	if _, ok := a.Entry(entries[0].ID); !ok {
		t.Fatalf("Entry() must find the scanned id %q", entries[0].ID)
	}
	if got := a.Stats().Entries; got != 2 {
		t.Fatalf("stats entries = %d, want 2", got)
	}
}

func TestScanRejectsUnknownScope(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	if _, err := a.Scan("bogus"); !errors.Is(err, impl.ErrUnknownScope) {
		t.Fatalf("scan of an unknown scope = %v, want ErrUnknownScope", err)
	}
	if _, err := a.Scan(""); !errors.Is(err, impl.ErrUnknownScope) {
		t.Fatalf("scan of an empty scope = %v, want ErrUnknownScope", err)
	}
}

func TestScanEnforcesEntryLimit(t *testing.T) {
	cfg := auditConfig()
	cfg.MaxEntries = 2
	a := impl.NewAutostartSurfaceAuditor(cfg)
	if err := a.Register([]impl.Item{
		unitItem("a.service", "a", auditBaseTime),
		unitItem("b.service", "b", auditBaseTime),
		unitItem("c.service", "c", auditBaseTime),
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := a.Scan(impl.ScopeAll); !errors.Is(err, impl.ErrScanLimit) {
		t.Fatalf("scan above the entry cap = %v, want ErrScanLimit", err)
	}
}

func TestScanDeduplicatesIdenticalDeclarations(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	item := unitItem("dup.service", "ExecStart=/usr/bin/dup", auditBaseTime)
	if err := a.Register([]impl.Item{item, item}); err != nil {
		t.Fatalf("register: %v", err)
	}
	entries := scanEntries(t, a, impl.ScopeAll)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want one merged entry", len(entries))
	}
	if len(entries[0].Sources) != 1 || entries[0].Sources[0] != impl.SourceSystemdSystem {
		t.Fatalf("sources = %v, want the single systemd source", entries[0].Sources)
	}
	if entries[0].Path != item.Path {
		t.Fatalf("entry path = %q, want %q", entries[0].Path, item.Path)
	}
}

func TestContentChangeProducesNewID(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	path := "/etc/systemd/system/mut.service"
	if err := a.Register([]impl.Item{{Source: impl.SourceSystemdSystem, Path: path, Name: "mut.service", Content: "ExecStart=/usr/bin/v1", MTime: auditBaseTime}}); err != nil {
		t.Fatalf("register v1: %v", err)
	}
	first := scanEntries(t, a, impl.ScopeAll)
	if len(first) != 1 {
		t.Fatalf("entries after v1 = %d, want 1", len(first))
	}
	if err := a.Register([]impl.Item{{Source: impl.SourceSystemdSystem, Path: path, Name: "mut.service", Content: "ExecStart=/usr/bin/v2", MTime: auditBaseTime}}); err != nil {
		t.Fatalf("register v2: %v", err)
	}
	second := scanEntries(t, a, impl.ScopeAll)
	if len(second) != 1 {
		t.Fatalf("entries after v2 = %d, want 1", len(second))
	}
	if second[0].ID == first[0].ID {
		t.Fatalf("changed content must produce a new entry id, both were %q", second[0].ID)
	}
}

func TestBaselineRequiredForDiff(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	if _, err := a.Diff(impl.ScopeAll); !errors.Is(err, impl.ErrNoBaseline) {
		t.Fatalf("diff before any baseline = %v, want ErrNoBaseline", err)
	}
	if err := a.Baseline(impl.ScopeUser); err != nil {
		t.Fatalf("baseline for the user scope: %v", err)
	}
	if _, err := a.Diff(impl.ScopeSystem); !errors.Is(err, impl.ErrNoBaseline) {
		t.Fatalf("diff of a scope without a baseline = %v, want ErrNoBaseline", err)
	}
	if got := a.Stats().Baselines; got != 1 {
		t.Fatalf("baseline counter = %d, want 1", got)
	}
}

func TestDiffReportsAddedEntry(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	if err := a.Baseline(impl.ScopeAll); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if err := a.Register([]impl.Item{unitItem("new.service", "ExecStart=/usr/bin/new", auditBaseTime)}); err != nil {
		t.Fatalf("register: %v", err)
	}
	entries := scanEntries(t, a, impl.ScopeAll)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	deltas, err := a.Diff(impl.ScopeAll)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %+v, want exactly one added entry", deltas)
	}
	if deltas[0].Kind != impl.DeltaAdded {
		t.Fatalf("delta kind = %q, want %q", deltas[0].Kind, impl.DeltaAdded)
	}
	if deltas[0].ID != entries[0].ID || deltas[0].Path != entries[0].Path {
		t.Fatalf("delta = %+v, want the scanned entry %+v", deltas[0], entries[0])
	}
}

func TestDiffReportsModifiedEntry(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	path := "/etc/systemd/system/mod.service"
	if err := a.Register([]impl.Item{{Source: impl.SourceSystemdSystem, Path: path, Name: "mod.service", Content: "ExecStart=/usr/bin/old", MTime: auditBaseTime}}); err != nil {
		t.Fatalf("register old: %v", err)
	}
	if err := a.Baseline(impl.ScopeAll); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if err := a.Register([]impl.Item{{Source: impl.SourceSystemdSystem, Path: path, Name: "mod.service", Content: "ExecStart=/usr/bin/new", MTime: auditBaseTime}}); err != nil {
		t.Fatalf("register new: %v", err)
	}
	deltas, err := a.Diff(impl.ScopeAll)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %+v, want exactly one modified entry", deltas)
	}
	if deltas[0].Kind != impl.DeltaModified {
		t.Fatalf("delta kind = %q, want %q", deltas[0].Kind, impl.DeltaModified)
	}
	if deltas[0].Path != path {
		t.Fatalf("delta path = %q, want %q", deltas[0].Path, path)
	}
	if deltas[0].Entry.Digest == "" {
		t.Fatalf("modified delta must carry the current entry with its digest")
	}
}

func TestDiffReportsRemovedEntry(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	if err := a.Register([]impl.Item{unitItem("gone.service", "ExecStart=/usr/bin/gone", auditBaseTime)}); err != nil {
		t.Fatalf("register: %v", err)
	}
	entries := scanEntries(t, a, impl.ScopeAll)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	id := entries[0].ID
	if err := a.Baseline(impl.ScopeAll); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if err := a.Remove(id); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := len(scanEntries(t, a, impl.ScopeAll)); got != 0 {
		t.Fatalf("entries after removal = %d, want 0", got)
	}
	deltas, err := a.Diff(impl.ScopeAll)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(deltas) != 1 || deltas[0].Kind != impl.DeltaRemoved || deltas[0].ID != id {
		t.Fatalf("deltas = %+v, want one removed entry %q", deltas, id)
	}
	if got := a.Stats().Removals; got != 1 {
		t.Fatalf("removal counter = %d, want 1", got)
	}
}

func TestDiffReportsTimestampOnly(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	path := "/etc/systemd/system/touch.service"
	if err := a.Register([]impl.Item{{Source: impl.SourceSystemdSystem, Path: path, Name: "touch.service", Content: "ExecStart=/usr/bin/same", MTime: auditBaseTime}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	before := scanEntries(t, a, impl.ScopeAll)
	if err := a.Baseline(impl.ScopeAll); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if err := a.Register([]impl.Item{{Source: impl.SourceSystemdSystem, Path: path, Name: "touch.service", Content: "ExecStart=/usr/bin/same", MTime: auditBaseTime.Add(time.Hour)}}); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	after := scanEntries(t, a, impl.ScopeAll)
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("content identical means the id must not change, before %+v after %+v", before, after)
	}
	deltas, err := a.Diff(impl.ScopeAll)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(deltas) != 1 || deltas[0].Kind != impl.DeltaTimestamp {
		t.Fatalf("deltas = %+v, want one timestamp_only delta", deltas)
	}
}

func TestAllowRejectsProtectedEntry(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	protected := impl.Item{Source: impl.SourceSystemdSystem, Path: "/etc/systemd/system/sshd.service", Name: "sshd.service", Content: "ExecStart=/usr/sbin/sshd", MTime: auditBaseTime}
	if err := a.Register([]impl.Item{protected}); err != nil {
		t.Fatalf("register: %v", err)
	}
	entries := scanEntries(t, a, impl.ScopeAll)
	if len(entries) != 1 || !entries[0].Protected {
		t.Fatalf("entries = %+v, want one protected entry", entries)
	}
	if err := a.Allow(entries[0].ID, auditBaseTime.Add(24*time.Hour)); !errors.Is(err, impl.ErrProtectedEntry) {
		t.Fatalf("allow on a protected entry = %v, want ErrProtectedEntry", err)
	}
	if err := a.Remove(entries[0].ID); !errors.Is(err, impl.ErrProtectedEntry) {
		t.Fatalf("remove of a protected entry = %v, want ErrProtectedEntry", err)
	}
	if err := a.Allow("entry-404", auditBaseTime.Add(time.Hour)); !errors.Is(err, impl.ErrUnknownEntry) {
		t.Fatalf("allow on an unknown entry = %v, want ErrUnknownEntry", err)
	}
}

func TestAllowAndRevoke(t *testing.T) {
	a := impl.NewAutostartSurfaceAuditor(auditConfig())
	if err := a.Register([]impl.Item{unitItem("ok.service", "ExecStart=/usr/bin/ok", auditBaseTime)}); err != nil {
		t.Fatalf("register: %v", err)
	}
	entries := scanEntries(t, a, impl.ScopeAll)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	id := entries[0].ID
	until := auditBaseTime.Add(48 * time.Hour)
	if err := a.Allow(id, until); err != nil {
		t.Fatalf("allow: %v", err)
	}
	list := a.AllowList()
	if len(list) != 1 || list[0].EntryID != id || !list[0].Until.Equal(until) {
		t.Fatalf("allow list = %+v, want the recorded allowance", list)
	}
	if err := a.Allow(id, time.Time{}); !errors.Is(err, impl.ErrBadUntil) {
		t.Fatalf("allow with a zero expiry = %v, want ErrBadUntil", err)
	}
	if err := a.RevokeAllow(id); err != nil {
		t.Fatalf("revoke allowance: %v", err)
	}
	if got := len(a.AllowList()); got != 0 {
		t.Fatalf("allow list = %d, want 0 after revoke", got)
	}
	if err := a.RevokeAllow(id); !errors.Is(err, impl.ErrUnknownAllowance) {
		t.Fatalf("revoking twice = %v, want ErrUnknownAllowance", err)
	}
}

func TestSnapshotRestoreKeepsBaselineAndAllowances(t *testing.T) {
	cfg := auditConfig()
	a := impl.NewAutostartSurfaceAuditor(cfg)
	if err := a.Register([]impl.Item{unitItem("keep.service", "ExecStart=/usr/bin/keep", auditBaseTime)}); err != nil {
		t.Fatalf("register: %v", err)
	}
	entries := scanEntries(t, a, impl.ScopeAll)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if err := a.Baseline(impl.ScopeAll); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if err := a.Allow(entries[0].ID, auditBaseTime.Add(72*time.Hour)); err != nil {
		t.Fatalf("allow: %v", err)
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewAutostartSurfaceAuditor(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	deltas, err := restored.Diff(impl.ScopeAll)
	if err != nil {
		t.Fatalf("diff after restore: %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("deltas after restore = %+v, want none against the restored baseline", deltas)
	}
	if got := len(restored.AllowList()); got != 1 {
		t.Fatalf("restored allowances = %d, want 1", got)
	}
	if err := restored.Restore([]byte("{not-an-audit-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	if _, err := restored.Diff(impl.ScopeAll); err != nil {
		t.Fatalf("a rejected restore must keep the live baseline, got %v", err)
	}
}
