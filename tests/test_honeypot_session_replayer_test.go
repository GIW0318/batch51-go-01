package task

import (
	"errors"
	"strings"
	"testing"

	impl "gsb/batch51-go-safety-c-32"
)

func rootMeta() impl.SessionMeta {
	return impl.SessionMeta{Conn: "10.0.0.7:51234", Identity: "root"}
}

func replaySession(t *testing.T, chunks []impl.Chunk) (*impl.HoneypotSessionReplayer, string) {
	t.Helper()
	r := impl.NewHoneypotSessionReplayer(500)
	key, err := r.Ingest(rootMeta(), chunks)
	if err != nil {
		t.Fatalf("ingest must succeed, got %v", err)
	}
	return r, key
}

func TestIngestRegistersSessionUnderKey(t *testing.T) {
	r, key := replaySession(t, []impl.Chunk{{Dir: impl.DirIn, AtMs: 10, Data: "id\n"}})
	want := r.SessionKey("10.0.0.7:51234", "root")
	if key != want {
		t.Fatalf("ingest key = %q, want %q", key, want)
	}
	sessions := r.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v, want exactly one entry", sessions)
	}
	if sessions[0] != want {
		t.Fatalf("stored key = %q, want %q", sessions[0], want)
	}
}

func TestIngestRejectsUnknownDirection(t *testing.T) {
	r := impl.NewHoneypotSessionReplayer(500)
	_, err := r.Ingest(rootMeta(), []impl.Chunk{{Dir: "shell", AtMs: 1, Data: "id\n"}})
	if !errors.Is(err, impl.ErrBadDirection) {
		t.Fatalf("a chunk without a known direction must fail with ErrBadDirection, got %v", err)
	}
	_, err = r.Ingest(impl.SessionMeta{Identity: "root"}, []impl.Chunk{{Dir: impl.DirIn, AtMs: 1, Data: "id\n"}})
	if !errors.Is(err, impl.ErrEmptyConn) {
		t.Fatalf("a session without a connection must fail with ErrEmptyConn, got %v", err)
	}
}

func TestIngestDuplicateImportKeepsOneCopy(t *testing.T) {
	chunks := []impl.Chunk{
		{Dir: impl.DirIn, AtMs: 10, Data: "id\n"},
		{Dir: impl.DirIn, AtMs: 20, Data: "whoami\n"},
	}
	r := impl.NewHoneypotSessionReplayer(500)
	first, err := r.Ingest(rootMeta(), chunks)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	second, err := r.Ingest(rootMeta(), chunks)
	if err != nil {
		t.Fatalf("second ingest of the same session must be accepted, got %v", err)
	}
	if first != second {
		t.Fatalf("importing the same session produced %q and %q", first, second)
	}
	if got := len(r.Sessions()); got != 1 {
		t.Fatalf("sessions after a duplicate import = %d, want 1", got)
	}
	stats, err := r.Stats(first)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Chunks != 2 {
		t.Fatalf("stored chunks = %d, want the original 2", stats.Chunks)
	}
}

func TestSegmentSplitsInboundLines(t *testing.T) {
	r, key := replaySession(t, []impl.Chunk{
		{Dir: impl.DirIn, AtMs: 10, Data: "id\n"},
		{Dir: impl.DirIn, AtMs: 30, Data: "whoami\n"},
	})
	cmds, err := r.Segment(key)
	if err != nil {
		t.Fatalf("segment: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("segmented commands = %d, want 2", len(cmds))
	}
	if cmds[0].Text != "id" || cmds[1].Text != "whoami" {
		t.Fatalf("command texts = %q and %q, want id and whoami", cmds[0].Text, cmds[1].Text)
	}
	if cmds[0].Index != 0 || cmds[1].Index != 1 {
		t.Fatalf("command indexes = %d and %d, want 0 and 1", cmds[0].Index, cmds[1].Index)
	}
	if cmds[0].AtMs != 10 || cmds[1].AtMs != 30 {
		t.Fatalf("command timestamps = %d and %d, want 10 and 30", cmds[0].AtMs, cmds[1].AtMs)
	}
	for _, c := range cmds {
		if c.Incomplete {
			t.Fatalf("a newline terminated command must be complete, got %+v", c)
		}
	}
}

func TestTimelineAttachesOutputToCommand(t *testing.T) {
	r, key := replaySession(t, []impl.Chunk{
		{Dir: impl.DirIn, AtMs: 10, Data: "cat /etc/passwd\n"},
		{Dir: impl.DirOut, AtMs: 20, Data: "root:x:0:0\n"},
		{Dir: impl.DirOut, AtMs: 25, Data: "daemon:x:1:1\n"},
	})
	tl, err := r.Timeline(key)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if tl.Session != key {
		t.Fatalf("timeline session = %q, want %q", tl.Session, key)
	}
	if len(tl.Entries) != 1 {
		t.Fatalf("timeline entries = %d, want 1", len(tl.Entries))
	}
	if tl.Entries[0].Command != "cat /etc/passwd" {
		t.Fatalf("entry command = %q, want cat /etc/passwd", tl.Entries[0].Command)
	}
	out := tl.Entries[0].Output
	if !strings.Contains(out, "root:x:0:0") || !strings.Contains(out, "daemon:x:1:1") {
		t.Fatalf("entry output = %q, want both output lines attached to the command", out)
	}
}

func TestNormalizeConvertsCarriageReturns(t *testing.T) {
	r := impl.NewHoneypotSessionReplayer(500)
	got := r.Normalize("whoami\r\nid\r\n")
	if got != "whoami\nid\n" {
		t.Fatalf("normalized text = %q, want %q", got, "whoami\nid\n")
	}
}

func TestNormalizeStripsEscapeSequences(t *testing.T) {
	r := impl.NewHoneypotSessionReplayer(500)
	got := r.Normalize("\x1b[31mred\x1b[0m\n")
	if got != "red\n" {
		t.Fatalf("normalized text = %q, want %q after the colour escapes are removed", got, "red\n")
	}
}

func TestCollapseRepeatsFoldsConsecutiveDuplicates(t *testing.T) {
	r := impl.NewHoneypotSessionReplayer(500)
	kept, folded := r.CollapseRepeats([]string{"id", "id", "whoami", "id"})
	if folded != 1 {
		t.Fatalf("folded count = %d, want 1", folded)
	}
	want := []string{"id", "whoami", "id"}
	if len(kept) != len(want) {
		t.Fatalf("kept lines = %v, want %v", kept, want)
	}
	for i, line := range want {
		if kept[i] != line {
			t.Fatalf("kept line %d = %q, want %q", i, kept[i], line)
		}
	}
}

func TestTimelineClampsIdleGaps(t *testing.T) {
	r, key := replaySession(t, []impl.Chunk{
		{Dir: impl.DirIn, AtMs: 0, Data: "a\n"},
		{Dir: impl.DirIn, AtMs: 100, Data: "b\n"},
		{Dir: impl.DirIn, AtMs: 6000, Data: "c\n"},
	})
	tl, err := r.Timeline(key)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(tl.Entries) != 3 {
		t.Fatalf("timeline entries = %d, want 3", len(tl.Entries))
	}
	want := []int64{0, 100, 500}
	for i, gap := range want {
		if tl.Entries[i].GapMs != gap {
			t.Fatalf("entry %d gap = %d, want %d after clamping to the 500ms idle cap", i, tl.Entries[i].GapMs, gap)
		}
	}
}

func TestReplayCompressesIdleButKeepsOrder(t *testing.T) {
	r, key := replaySession(t, []impl.Chunk{
		{Dir: impl.DirIn, AtMs: 0, Data: "a\n"},
		{Dir: impl.DirIn, AtMs: 100, Data: "b\n"},
		{Dir: impl.DirIn, AtMs: 6000, Data: "c\n"},
	})
	entries, err := r.Replay(key)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("replay entries = %d, want 3", len(entries))
	}
	wantText := []string{"a", "b", "c"}
	wantAt := []int64{0, 100, 600}
	for i := range wantText {
		if entries[i].Command != wantText[i] {
			t.Fatalf("replay entry %d = %q, want %q", i, entries[i].Command, wantText[i])
		}
		if entries[i].AtMs != wantAt[i] {
			t.Fatalf("replay offset %d = %d, want %d", i, entries[i].AtMs, wantAt[i])
		}
	}
}

func TestSessionKeyIsStableAndIdentityScoped(t *testing.T) {
	r := impl.NewHoneypotSessionReplayer(500)
	if r.SessionKey("c1", "root") != r.SessionKey("c1", "root") {
		t.Fatalf("session key must be stable for the same connection and identity")
	}
	if r.SessionKey("c1", "root") == r.SessionKey("c1", "www-data") {
		t.Fatalf("two identities on one connection must not share a key")
	}
	if r.SessionKey("c1", "root") == r.SessionKey("c2", "root") {
		t.Fatalf("the same identity on two connections must not share a key")
	}
}

func TestSegmentRejectsUnknownSession(t *testing.T) {
	r := impl.NewHoneypotSessionReplayer(500)
	if _, err := r.Segment("missing|root"); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("segmenting an unknown session must fail with ErrUnknownSession, got %v", err)
	}
	if _, err := r.Timeline("missing|root"); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("timeline of an unknown session must fail with ErrUnknownSession, got %v", err)
	}
}

func TestSegmentIgnoresOutboundOnlySession(t *testing.T) {
	r, key := replaySession(t, []impl.Chunk{
		{Dir: impl.DirOut, AtMs: 5, Data: "Welcome to the host\n"},
		{Dir: impl.DirOut, AtMs: 9, Data: "$ "},
	})
	cmds, err := r.Segment(key)
	if err != nil {
		t.Fatalf("segment: %v", err)
	}
	if len(cmds) != 0 {
		t.Fatalf("outbound traffic must not create commands, got %+v", cmds)
	}
}

func TestStatsCountsCommandsOutputAndRepeats(t *testing.T) {
	r, key := replaySession(t, []impl.Chunk{
		{Dir: impl.DirIn, AtMs: 10, Data: "id\n"},
		{Dir: impl.DirIn, AtMs: 20, Data: "id\n"},
		{Dir: impl.DirOut, AtMs: 30, Data: "uid=0\n"},
	})
	stats, err := r.Stats(key)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Chunks != 3 {
		t.Fatalf("chunk count = %d, want 3", stats.Chunks)
	}
	if stats.Commands != 2 {
		t.Fatalf("command count = %d, want 2", stats.Commands)
	}
	if stats.OutputBytes != len("uid=0\n") {
		t.Fatalf("output bytes = %d, want %d", stats.OutputBytes, len("uid=0\n"))
	}
	if stats.Repeated != 1 {
		t.Fatalf("repeated count = %d, want 1 folded duplicate", stats.Repeated)
	}
}

func TestIdleGapsReportsLaterIndex(t *testing.T) {
	r := impl.NewHoneypotSessionReplayer(500)
	gaps := r.IdleGaps([]int64{0, 100, 6000, 6100}, 1000)
	if len(gaps) != 1 {
		t.Fatalf("idle gaps = %v, want exactly one position", gaps)
	}
	if gaps[0] != 2 {
		t.Fatalf("idle gap position = %d, want 2 (the index whose predecessor is 5900ms earlier)", gaps[0])
	}
}
