package task

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-game-a-17"
)

func newModerator() *impl.ChatContentModerator {
	return impl.NewChatContentModerator(
		impl.ModLimits{MaxTerms: 32, MaxNormalizedLen: 200, MaxWindowEntries: 16},
		impl.Policy{WarnSeverity: 1, MuteSeverity: 2, BlockSeverity: 3},
	)
}

func mustLexicon(t *testing.T, m *impl.ChatContentModerator, ver string, entries ...impl.LexEntry) {
	t.Helper()
	if err := m.SetLexicon(ver, entries); err != nil {
		t.Fatalf("set lexicon %s: %v", ver, err)
	}
}

func baseText() time.Time {
	return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
}

func msg(id, sender, text string) impl.ChatMsg {
	return impl.ChatMsg{ID: id, Channel: impl.ChannelWorld, Sender: sender, Text: text, At: baseText()}
}

func TestSetLexiconRejectsInvalidVersion(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3})
	bad := []struct {
		ver     string
		entries []impl.LexEntry
	}{
		{"", []impl.LexEntry{{Term: "cheat", Severity: 1}}},
		{"   ", []impl.LexEntry{{Term: "cheat", Severity: 1}}},
		{"v2", nil},
		{"v2", []impl.LexEntry{{Term: "  ", Severity: 1}}},
		{"v2", []impl.LexEntry{{Term: "cheat", Severity: 0}}},
		{"v2", []impl.LexEntry{{Term: "cheat", Severity: 1}, {Term: "cheat", Severity: 2}}},
	}
	for i, c := range bad {
		if err := m.SetLexicon(c.ver, c.entries); !errors.Is(err, impl.ErrBadLexicon) {
			t.Fatalf("lexicon case %d must fail with ErrBadLexicon, got %v", i, err)
		}
	}
	v, err := m.Check(msg("m-1", "player-1", "you cheat"))
	if err != nil {
		t.Fatalf("check after rejected lexicon: %v", err)
	}
	if v.LexiconVer != "v1" {
		t.Fatalf("lexicon version = %q, want the previous v1", v.LexiconVer)
	}
}

func TestSetLexiconRejectsOversizeLexicon(t *testing.T) {
	m := impl.NewChatContentModerator(
		impl.ModLimits{MaxTerms: 3, MaxNormalizedLen: 200, MaxWindowEntries: 16},
		impl.Policy{WarnSeverity: 1, MuteSeverity: 2, BlockSeverity: 3},
	)
	entries := []impl.LexEntry{
		{Term: "alpha", Severity: 1},
		{Term: "beta", Severity: 1},
		{Term: "gamma", Severity: 1},
		{Term: "delta", Severity: 1},
	}
	if err := m.SetLexicon("v1", entries); !errors.Is(err, impl.ErrLexiconTooLarge) {
		t.Fatalf("oversize lexicon must fail with ErrLexiconTooLarge, got %v", err)
	}
	if err := m.SetLexicon("v1", entries[:3]); err != nil {
		t.Fatalf("lexicon at the cap must be accepted, got %v", err)
	}
}

func TestCheckAllowsCleanMessage(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3})
	v, err := m.Check(msg("m-2", "player-2", "good run tonight"))
	if err != nil {
		t.Fatalf("check clean message: %v", err)
	}
	if v.Action != impl.ActionAllow || v.Reason != impl.ReasonClean {
		t.Fatalf("clean verdict = %s/%s, want allow/clean", v.Action, v.Reason)
	}
	if v.Severity != 0 || len(v.Matched) != 0 {
		t.Fatalf("clean verdict severity=%d matched=%v, want 0 and none", v.Severity, v.Matched)
	}
	if v.LexiconVer != "v1" {
		t.Fatalf("verdict lexicon version = %q, want v1", v.LexiconVer)
	}
	if _, err := m.Check(impl.ChatMsg{Sender: "player-2", Text: "hello", At: baseText()}); !errors.Is(err, impl.ErrBadMessage) {
		t.Fatalf("missing message id must fail with ErrBadMessage, got %v", err)
	}
	if _, err := m.Check(impl.ChatMsg{ID: "m-3", Text: "hello", At: baseText()}); !errors.Is(err, impl.ErrBadMessage) {
		t.Fatalf("missing sender must fail with ErrBadMessage, got %v", err)
	}
}

func TestCheckMatchesTermAndBlocks(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3})
	v, err := m.Check(msg("m-4", "player-4", "you CHEAT the ladder"))
	if err != nil {
		t.Fatalf("check message: %v", err)
	}
	if v.Action != impl.ActionBlock || v.Reason != impl.ReasonLexiconHit {
		t.Fatalf("verdict = %s/%s, want block/lexicon_hit", v.Action, v.Reason)
	}
	if v.Severity != 3 {
		t.Fatalf("verdict severity = %d, want 3", v.Severity)
	}
	if !reflect.DeepEqual(v.Matched, []string{"cheat"}) {
		t.Fatalf("matched = %v, want [cheat]", v.Matched)
	}
}

func TestCheckSeverityThresholds(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1",
		impl.LexEntry{Term: "minor", Category: "tone", Severity: 1},
		impl.LexEntry{Term: "spam", Category: "spam", Severity: 2},
		impl.LexEntry{Term: "scam", Category: "fraud", Severity: 3},
	)
	cases := []struct {
		id     string
		text   string
		action string
		sev    int
	}{
		{"m-5", "a minor slip", impl.ActionWarn, 1},
		{"m-6", "buy spam now", impl.ActionMute, 2},
		{"m-7", "pure scam", impl.ActionBlock, 3},
	}
	for _, c := range cases {
		v, err := m.Check(msg(c.id, "player-5", c.text))
		if err != nil {
			t.Fatalf("check %s: %v", c.id, err)
		}
		if v.Action != c.action || v.Severity != c.sev {
			t.Fatalf("%s verdict = %s/%d, want %s/%d", c.id, v.Action, v.Severity, c.action, c.sev)
		}
	}
	stats := m.Stats()
	if stats.Warned != 1 || stats.Muted != 1 || stats.Blocked != 1 {
		t.Fatalf("stats warned=%d muted=%d blocked=%d, want 1/1/1", stats.Warned, stats.Muted, stats.Blocked)
	}
}

func TestFullWidthTextIsNormalized(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "free gift", Category: "scam", Severity: 3})
	v, err := m.Check(msg("m-8", "player-8", "ＦＲＥＥ　ＧＩＦＴ"))
	if err != nil {
		t.Fatalf("check full width message: %v", err)
	}
	if v.Action != impl.ActionBlock {
		t.Fatalf("full width text action = %q, want block", v.Action)
	}
	if !reflect.DeepEqual(v.Matched, []string{"free gift"}) {
		t.Fatalf("full width matched = %v, want [free gift]", v.Matched)
	}
}

func TestWhitelistBeatsLexicon(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "free gift", Category: "scam", Severity: 3})
	if err := m.SetPolicy(impl.Policy{
		WarnSeverity:  1,
		MuteSeverity:  2,
		BlockSeverity: 3,
		Whitelist:     []string{"free gift tutorial"},
	}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	v, err := m.Check(msg("m-9", "player-9", "free gift tutorial for new players"))
	if err != nil {
		t.Fatalf("check whitelisted message: %v", err)
	}
	if v.Action != impl.ActionAllow || v.Reason != impl.ReasonWhitelisted {
		t.Fatalf("whitelisted verdict = %s/%s, want allow/whitelisted", v.Action, v.Reason)
	}
}

func TestCheckSameMsgIDIsIdempotent(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3})
	first, err := m.Check(msg("m-10", "player-10", "cheat engine ready"))
	if err != nil {
		t.Fatalf("first check: %v", err)
	}
	second, err := m.Check(msg("m-10", "player-10", "cheat engine ready"))
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated check = %+v, want %+v", second, first)
	}
	stats := m.Stats()
	if stats.Checks != 1 || stats.Blocked != 1 {
		t.Fatalf("stats checks=%d blocked=%d, want 1/1 for one message", stats.Checks, stats.Blocked)
	}
}

func TestLexiconHotSwapAtomicUnderLoad(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "gold", Category: "trade", Severity: 1})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var problems []string
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("hot-%02d", i)
			for round := 0; round < 3; round++ {
				v, err := m.Check(msg(id, "player-hot", "sell gold cheap"))
				mu.Lock()
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s: %v", id, err))
					mu.Unlock()
					continue
				}
				switch v.LexiconVer {
				case "v1":
					if v.Severity != 1 || v.Action != impl.ActionWarn {
						problems = append(problems, fmt.Sprintf("%s: v1 graded %d/%s", id, v.Severity, v.Action))
					}
				case "v2":
					if v.Severity != 3 || v.Action != impl.ActionBlock {
						problems = append(problems, fmt.Sprintf("%s: v2 graded %d/%s", id, v.Severity, v.Action))
					}
				default:
					problems = append(problems, fmt.Sprintf("%s: unknown version %q", id, v.LexiconVer))
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			ver := "v1"
			entries := []impl.LexEntry{{Term: "gold", Category: "trade", Severity: 1}}
			if i%2 == 1 {
				ver = "v2"
				entries = []impl.LexEntry{{Term: "gold", Category: "trade", Severity: 3}}
			}
			if err := m.SetLexicon(ver, entries); err != nil {
				mu.Lock()
				problems = append(problems, fmt.Sprintf("swap %d to %s: %v", i, ver, err))
				mu.Unlock()
			}
		}
	}()
	wg.Wait()
	if len(problems) != 0 {
		t.Fatalf("hot swap produced %d inconsistent verdicts, first: %s", len(problems), problems[0])
	}
}

func TestOversizeMessageDegradesToWarn(t *testing.T) {
	m := impl.NewChatContentModerator(
		impl.ModLimits{MaxTerms: 32, MaxNormalizedLen: 20, MaxWindowEntries: 16},
		impl.Policy{WarnSeverity: 1, MuteSeverity: 2, BlockSeverity: 3},
	)
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3})
	v, err := m.Check(msg("m-11", "player-11", strings.Repeat("a", 30)))
	if err != nil {
		t.Fatalf("check oversize message: %v", err)
	}
	if v.Action != impl.ActionWarn || v.Reason != impl.ReasonOversize {
		t.Fatalf("oversize verdict = %s/%s, want warn/oversize", v.Action, v.Reason)
	}
	if got := m.Stats().Oversize; got != 1 {
		t.Fatalf("oversize counter = %d, want 1", got)
	}
}

func TestAppealRequiresKnownMessage(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3})
	if err := m.Appeal("m-404", "wrong call"); !errors.Is(err, impl.ErrUnknownMsg) {
		t.Fatalf("appealing an unknown message must fail with ErrUnknownMsg, got %v", err)
	}
	if _, err := m.Check(msg("m-12", "player-12", "cheat")); err != nil {
		t.Fatalf("check message: %v", err)
	}
	if err := m.Appeal("m-12", "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank appeal reason must fail with ErrBadReason, got %v", err)
	}
	if err := m.Appeal("m-12", "reviewed by hand"); err != nil {
		t.Fatalf("appeal: %v", err)
	}
	if err := m.Appeal("m-12", "reviewed by hand"); err != nil {
		t.Fatalf("repeated appeal must stay idempotent, got %v", err)
	}
	if got := m.Stats().Appealed; got != 1 {
		t.Fatalf("appealed counter = %d, want 1", got)
	}
}

func TestRateLimitMutesFlood(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3})
	if err := m.SetRateLimit(2, time.Minute); err != nil {
		t.Fatalf("set rate limit: %v", err)
	}
	base := baseText()
	for i := 0; i < 2; i++ {
		one := msg(fmt.Sprintf("m-13-%d", i), "flooder", "hello")
		one.At = base.Add(time.Duration(i) * time.Second)
		v, err := m.Check(one)
		if err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
		if v.Action != impl.ActionAllow {
			t.Fatalf("check %d action = %q, want allow", i, v.Action)
		}
	}
	third := msg("m-13-2", "flooder", "hello")
	third.At = base.Add(2 * time.Second)
	v, err := m.Check(third)
	if err != nil {
		t.Fatalf("check third message: %v", err)
	}
	if v.Action != impl.ActionMute || v.Reason != impl.ReasonRateLimited {
		t.Fatalf("flood verdict = %s/%s, want mute/rate_limited", v.Action, v.Reason)
	}
	if got := m.Stats().Throttled; got != 1 {
		t.Fatalf("throttled counter = %d, want 1", got)
	}
	other := msg("m-13-3", "friend", "hello")
	other.At = base.Add(3 * time.Second)
	friend, err := m.Check(other)
	if err != nil {
		t.Fatalf("check other sender: %v", err)
	}
	if friend.Action != impl.ActionAllow {
		t.Fatalf("other sender action = %q, want allow", friend.Action)
	}
}

func TestSnapshotRestoreKeepsVerdict(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v7", impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3})
	before, err := m.Check(msg("m-14", "player-14", "cheat"))
	if err != nil {
		t.Fatalf("check message: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newModerator()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after, err := restored.Check(msg("m-14", "player-14", "cheat"))
	if err != nil {
		t.Fatalf("check after restore: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restored verdict = %+v, want %+v", after, before)
	}
	stats := restored.Stats()
	if stats.Checks != 1 || stats.Blocked != 1 {
		t.Fatalf("restored stats checks=%d blocked=%d, want 1/1", stats.Checks, stats.Blocked)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1", impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3})
	if _, err := m.Check(msg("m-15", "player-15", "cheat")); err != nil {
		t.Fatalf("check message: %v", err)
	}
	if err := m.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	v, err := m.Check(msg("m-16", "player-16", "all clear"))
	if err != nil {
		t.Fatalf("check after rejected restore: %v", err)
	}
	if v.Action != impl.ActionAllow {
		t.Fatalf("live state must keep working after a rejected restore, got %q", v.Action)
	}
	if got := m.Stats().Blocked; got != 1 {
		t.Fatalf("blocked counter = %d, want the live 1", got)
	}
}

func TestExplainListsMatchesSorted(t *testing.T) {
	m := newModerator()
	mustLexicon(t, m, "v1",
		impl.LexEntry{Term: "gold", Category: "trade", Severity: 1},
		impl.LexEntry{Term: "cheat", Category: "fraud", Severity: 3},
	)
	hits := m.Explain(msg("m-17", "player-17", "cheap gold and cheat codes"))
	if len(hits) != 2 {
		t.Fatalf("explain hits = %d, want 2", len(hits))
	}
	if hits[0].Term != "cheat" || hits[1].Term != "gold" {
		t.Fatalf("explain order = %q,%q, want cheat,gold", hits[0].Term, hits[1].Term)
	}
	if hits[0].Severity != 3 || hits[0].Category != "fraud" {
		t.Fatalf("cheat hit = %+v, want severity 3 category fraud", hits[0])
	}
}
