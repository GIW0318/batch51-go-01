package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-game-a-12"
)

func codexConfig() impl.CodexConfig {
	return impl.CodexConfig{
		Version:         1,
		DailyRegionGain: 10,
		EntryRegion: map[string]string{
			"e1": "forest",
			"e2": "forest",
			"e3": "cavern",
			"e4": "forest",
			"e5": "forest",
		},
		EntryVersion: map[string]int{"e1": 1, "e2": 1, "e3": 2, "e4": 1, "e5": 1},
		RegionEntries: map[string][]string{
			"forest": {"e1", "e2", "e4", "e5"},
			"cavern": {"e3"},
		},
	}
}

func codexStart() time.Time {
	return time.Date(2026, 4, 2, 9, 30, 0, 0, time.UTC)
}

func killSource() impl.Source {
	return impl.Source{Kind: impl.SourceKill}
}

func TestDiscoverReturnsFirstDiscovery(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	start := codexStart()
	d, err := l.Discover("acct-1", "e1", killSource(), start)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !d.First || !d.Counted {
		t.Fatalf("discovery = %+v, want first and counted", d)
	}
	if d.Seq != 1 {
		t.Fatalf("discovery sequence = %d, want 1", d.Seq)
	}
	if d.RegionID != "forest" {
		t.Fatalf("discovery region = %q, want forest", d.RegionID)
	}
	entry, ok := l.Entry("acct-1", "e1")
	if !ok {
		t.Fatalf("entry record must exist after the first discovery")
	}
	if len(entry.Sources) != 1 || entry.Sources[0] != "kill" {
		t.Fatalf("entry sources = %+v, want [kill]", entry.Sources)
	}
	prog, err := l.Progress("acct-1", "forest")
	if err != nil || prog.Discovered != 1 || prog.Total != 4 {
		t.Fatalf("progress = %+v, %v; want 1 of 4", prog, err)
	}
}

func TestDiscoverRejectsBlankAccount(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	if _, err := l.Discover("  ", "e1", killSource(), codexStart()); !errors.Is(err, impl.ErrBlankAccount) {
		t.Fatalf("blank account must fail with ErrBlankAccount, got %v", err)
	}
	if _, ok := l.Entry("  ", "e1"); ok {
		t.Fatalf("rejected discovery must not create an entry record")
	}
}

func TestDiscoverRejectsUnknownEntry(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	start := codexStart()
	if _, err := l.Discover("acct-1", "no-such-entry", killSource(), start); !errors.Is(err, impl.ErrUnknownEntry) {
		t.Fatalf("unknown entry must fail with ErrUnknownEntry, got %v", err)
	}
	if _, err := l.Discover("acct-1", "e3", killSource(), start); !errors.Is(err, impl.ErrUnknownEntry) {
		t.Fatalf("entry from a later version must fail with ErrUnknownEntry, got %v", err)
	}
	if _, err := l.Discover("acct-1", "e1", killSource(), start); err != nil {
		t.Fatalf("entry known at the current version must be accepted, got %v", err)
	}
}

func TestDiscoverRejectsBadSourceKind(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	start := codexStart()
	if _, err := l.Discover("acct-1", "e1", impl.Source{Kind: "trade"}, start); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("unknown source must fail with ErrBadSource, got %v", err)
	}
	d, err := l.Discover("acct-1", "e1", killSource(), start)
	if err != nil || !d.First {
		t.Fatalf("valid discovery after a rejected one = %+v, %v; want a first discovery", d, err)
	}
}

func TestDiscoverKeepsAllSourcesOnce(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	start := codexStart()
	first, err := l.Discover("acct-1", "e1", killSource(), start)
	if err != nil || !first.First {
		t.Fatalf("first discovery = %+v, %v", first, err)
	}
	second, err := l.Discover("acct-1", "e1", impl.Source{Kind: impl.SourceObserve}, start.Add(time.Minute))
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if second.First || second.Counted {
		t.Fatalf("repeat discovery = %+v, want not first and not counted", second)
	}
	third, err := l.Discover("acct-1", "e1", impl.Source{Kind: impl.SourceShare, FromAccount: "friend"}, start.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("shared discovery: %v", err)
	}
	if third.First || third.Counted {
		t.Fatalf("shared discovery = %+v, want not first and not counted", third)
	}
	entry, ok := l.Entry("acct-1", "e1")
	if !ok {
		t.Fatalf("entry record must exist")
	}
	if len(entry.Sources) != 3 || entry.Sources[0] != "kill" || entry.Sources[1] != "observe" || entry.Sources[2] != "share" {
		t.Fatalf("entry sources = %+v, want [kill observe share]", entry.Sources)
	}
	prog, err := l.Progress("acct-1", "forest")
	if err != nil || prog.Discovered != 1 {
		t.Fatalf("progress = %+v, %v; want exactly one counted entry", prog, err)
	}
}

func TestProgressReportsRegionCompletion(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	start := codexStart()
	for i, entry := range []string{"e1", "e2"} {
		if _, err := l.Discover("acct-1", entry, killSource(), start.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("discover %s: %v", entry, err)
		}
	}
	prog, err := l.Progress("acct-1", "forest")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if prog.Discovered != 2 || prog.Total != 4 || prog.Percent != 50 || prog.Complete || prog.Backlog != 0 {
		t.Fatalf("progress = %+v, want 2/4 at 50 percent without backlog", prog)
	}
}

func TestProgressUnknownRegionIsZero(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	if _, err := l.Discover("acct-1", "e1", killSource(), codexStart()); err != nil {
		t.Fatalf("discover: %v", err)
	}
	unknown, err := l.Progress("acct-1", "desert")
	if err != nil {
		t.Fatalf("unknown region must not error, got %v", err)
	}
	if unknown.Discovered != 0 || unknown.Total != 0 || unknown.Percent != 0 || unknown.Complete {
		t.Fatalf("unknown region progress = %+v, want zero", unknown)
	}
	known, err := l.Progress("acct-1", "forest")
	if err != nil || known.Total != 4 {
		t.Fatalf("known region progress = %+v, %v; want total 4", known, err)
	}
}

func TestDiscoverDefersAtDailyCap(t *testing.T) {
	cfg := codexConfig()
	cfg.DailyRegionGain = 2
	l := impl.NewCodexLedger(cfg)
	start := codexStart()
	if _, err := l.Discover("acct-1", "e1", killSource(), start); err != nil {
		t.Fatalf("discover e1: %v", err)
	}
	if _, err := l.Discover("acct-1", "e2", killSource(), start.Add(time.Minute)); err != nil {
		t.Fatalf("discover e2: %v", err)
	}
	third, err := l.Discover("acct-1", "e4", killSource(), start.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("discover e4: %v", err)
	}
	if third.Counted {
		t.Fatalf("third discovery = %+v, want deferred by the daily cap", third)
	}
	if third.Deferred != 1 {
		t.Fatalf("deferred count = %d, want 1", third.Deferred)
	}
	prog, err := l.Progress("acct-1", "forest")
	if err != nil || prog.Discovered != 2 || prog.Backlog != 1 || prog.Complete {
		t.Fatalf("progress = %+v, %v; want 2 counted and 1 backlog", prog, err)
	}
}

func TestRolloverAppliesBacklogNextDay(t *testing.T) {
	cfg := codexConfig()
	cfg.DailyRegionGain = 2
	l := impl.NewCodexLedger(cfg)
	start := codexStart()
	for i, entry := range []string{"e1", "e2", "e4"} {
		if _, err := l.Discover("acct-1", entry, killSource(), start.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("discover %s: %v", entry, err)
		}
	}
	applied, err := l.Rollover("acct-1", start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("rollover: %v", err)
	}
	if applied != 1 {
		t.Fatalf("rollover applied = %d, want 1", applied)
	}
	prog, err := l.Progress("acct-1", "forest")
	if err != nil || prog.Discovered != 3 || prog.Backlog != 0 {
		t.Fatalf("progress after rollover = %+v, %v; want 3 counted and no backlog", prog, err)
	}
}

func TestBackfillAddsHistoricalKills(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	start := codexStart()
	res, err := l.Backfill("acct-1", []impl.KillRecord{{EntryID: "e1", At: start, Source: impl.SourceKill}})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if len(res.Added) != 1 || res.Added[0] != "e1" || res.Rewarded != 1 {
		t.Fatalf("backfill result = %+v, want e1 added and rewarded once", res)
	}
	prog, err := l.Progress("acct-1", "forest")
	if err != nil || prog.Discovered != 1 {
		t.Fatalf("progress after backfill = %+v, %v; want 1", prog, err)
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	start := codexStart()
	record := impl.KillRecord{EntryID: "e1", At: start, Source: impl.SourceKill}
	if _, err := l.Backfill("acct-1", []impl.KillRecord{record}); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	res, err := l.Backfill("acct-1", []impl.KillRecord{record})
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if len(res.Added) != 0 || res.Rewarded != 0 {
		t.Fatalf("second backfill = %+v, want nothing added and nothing rewarded", res)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "e1" {
		t.Fatalf("second backfill skipped = %+v, want [e1]", res.Skipped)
	}
	prog, err := l.Progress("acct-1", "forest")
	if err != nil || prog.Discovered != 1 {
		t.Fatalf("progress after duplicate backfill = %+v, %v; want 1", prog, err)
	}
}

func TestSetVersionUnlocksNewEntries(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	start := codexStart()
	res, err := l.Backfill("acct-1", []impl.KillRecord{{EntryID: "e3", At: start, Source: impl.SourceKill}})
	if err != nil {
		t.Fatalf("backfill before upgrade: %v", err)
	}
	if len(res.Added) != 0 || len(res.Skipped) != 1 || res.Skipped[0] != "e3" {
		t.Fatalf("backfill at version 1 = %+v, want e3 skipped", res)
	}
	before, err := l.Progress("acct-1", "cavern")
	if err != nil || before.Total != 0 || before.Discovered != 0 {
		t.Fatalf("cavern at version 1 = %+v, %v; want empty", before, err)
	}
	if err := l.SetVersion(2); err != nil {
		t.Fatalf("set version 2: %v", err)
	}
	if got := l.Version(); got != 2 {
		t.Fatalf("version = %d, want 2", got)
	}
	after, err := l.Progress("acct-1", "cavern")
	if err != nil {
		t.Fatalf("cavern progress after upgrade: %v", err)
	}
	if after.Total != 1 || after.Discovered != 1 || !after.Complete {
		t.Fatalf("cavern after upgrade = %+v, want the skipped kill replayed", after)
	}
}

func TestSetVersionRejectsNonPositive(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	if err := l.SetVersion(0); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("zero version = %v, want ErrBadVersion", err)
	}
	if got := l.Version(); got != 1 {
		t.Fatalf("version after rejection = %d, want 1", got)
	}
	if err := l.SetVersion(2); err != nil {
		t.Fatalf("valid upgrade: %v", err)
	}
	if got := l.Version(); got != 2 {
		t.Fatalf("version after upgrade = %d, want 2", got)
	}
}

func TestSnapshotRestoreKeepsProgressAndSources(t *testing.T) {
	cfg := codexConfig()
	l := impl.NewCodexLedger(cfg)
	start := codexStart()
	if _, err := l.Discover("acct-1", "e1", killSource(), start); err != nil {
		t.Fatalf("discover e1: %v", err)
	}
	if _, err := l.Discover("acct-1", "e1", impl.Source{Kind: impl.SourceObserve}, start.Add(time.Minute)); err != nil {
		t.Fatalf("observe e1: %v", err)
	}
	if _, err := l.Discover("acct-1", "e2", killSource(), start.Add(2*time.Minute)); err != nil {
		t.Fatalf("discover e2: %v", err)
	}
	snap, err := l.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot = %d bytes, %v", len(snap), err)
	}
	restored := impl.NewCodexLedger(codexConfig())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Version(); got != 1 {
		t.Fatalf("restored version = %d, want 1", got)
	}
	prog, err := restored.Progress("acct-1", "forest")
	if err != nil || prog.Discovered != 2 || prog.Total != 4 {
		t.Fatalf("restored progress = %+v, %v; want 2 of 4", prog, err)
	}
	entry, ok := restored.Entry("acct-1", "e1")
	if !ok || len(entry.Sources) != 2 || entry.Sources[0] != "kill" || entry.Sources[1] != "observe" {
		t.Fatalf("restored entry = %+v, %v; want both sources", entry, ok)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	l := impl.NewCodexLedger(codexConfig())
	if _, err := l.Discover("acct-1", "e1", killSource(), codexStart()); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := l.Restore([]byte("<<<corrupt>>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	prog, err := l.Progress("acct-1", "forest")
	if err != nil || prog.Discovered != 1 {
		t.Fatalf("progress after failed restore = %+v, %v; want the original state", prog, err)
	}
}
