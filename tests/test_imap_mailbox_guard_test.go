package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-b-14"
)

func mailboxLimits() impl.Limits {
	return impl.Limits{MaxChangeLog: 32, MaxMailboxes: 16}
}

func message(body string, flags ...string) impl.RawMessage {
	return impl.RawMessage{Body: body, Flags: flags}
}

func uidSet(t *testing.T, got []uint32, want ...uint32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("uids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uids = %v, want %v", got, want)
		}
	}
}

func TestSelectAllocatesMailbox(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	view, err := g.Select("alice", "INBOX")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if view.User != "alice" || view.Mailbox != "INBOX" {
		t.Fatalf("view = %+v, want alice/INBOX echoed", view)
	}
	if view.UIDValidity != 1 {
		t.Fatalf("uidvalidity = %d, want 1 for a fresh mailbox", view.UIDValidity)
	}
	if view.UIDNext != 1 {
		t.Fatalf("uidnext = %d, want 1 for a fresh mailbox", view.UIDNext)
	}
	if view.Messages != 0 {
		t.Fatalf("messages = %d, want 0 for a fresh mailbox", view.Messages)
	}
	again, err := g.Select("alice", "INBOX")
	if err != nil {
		t.Fatalf("second select: %v", err)
	}
	if again.UIDValidity != view.UIDValidity || again.ModSeq != view.ModSeq {
		t.Fatalf("a second select must not move the mailbox: %+v then %+v", view, again)
	}
}

func TestSelectRejectsBlankOwner(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	if _, err := g.Select("  ", "INBOX"); !errors.Is(err, impl.ErrBadOwner) {
		t.Fatalf("blank user must fail with ErrBadOwner, got %v", err)
	}
	if _, err := g.Select("alice", ""); !errors.Is(err, impl.ErrBadOwner) {
		t.Fatalf("blank mailbox must fail with ErrBadOwner, got %v", err)
	}
}

func TestAppendAssignsMonotonicUIDs(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	if _, err := g.Select("alice", "INBOX"); err != nil {
		t.Fatalf("select: %v", err)
	}
	for i := 1; i <= 3; i++ {
		uid, err := g.Append("alice", "INBOX", message("body-"+string(rune('a'+i-1)), "\\Draft"))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if uid != uint32(i) {
			t.Fatalf("append %d returned uid %d, want %d", i, uid, i)
		}
	}
	view, err := g.Select("alice", "INBOX")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if view.UIDNext != 4 {
		t.Fatalf("uidnext = %d, want 4 after three appends", view.UIDNext)
	}
	if view.Messages != 3 {
		t.Fatalf("messages = %d, want 3", view.Messages)
	}
	if view.ModSeq != 4 {
		t.Fatalf("modseq = %d, want 4 after three changes", view.ModSeq)
	}
}

func TestAppendRejectsEmptyBody(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	if _, err := g.Append("alice", "INBOX", message("   ")); !errors.Is(err, impl.ErrBadMessage) {
		t.Fatalf("blank body must fail with ErrBadMessage, got %v", err)
	}
	if _, err := g.Append("alice", "INBOX", message("")); !errors.Is(err, impl.ErrBadMessage) {
		t.Fatalf("empty body must fail with ErrBadMessage, got %v", err)
	}
	view, _ := g.Select("alice", "INBOX")
	if view.Messages != 0 || view.UIDNext != 1 {
		t.Fatalf("rejected appends moved the mailbox to %+v", view)
	}
}

func TestConcurrentAppendKeepsUIDsUnique(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	if _, err := g.Select("alice", "INBOX"); err != nil {
		t.Fatalf("select: %v", err)
	}
	const writers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[uint32]bool, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uid, err := g.Append("alice", "INBOX", message("concurrent body"))
			if err != nil {
				return
			}
			mu.Lock()
			seen[uid] = true
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(seen) != writers {
		t.Fatalf("concurrent appends produced %d distinct uids, want %d", len(seen), writers)
	}
	view, _ := g.Select("alice", "INBOX")
	if view.UIDNext != writers+1 {
		t.Fatalf("uidnext = %d after %d appends, want %d", view.UIDNext, writers, writers+1)
	}
	if view.Messages != writers {
		t.Fatalf("messages = %d after the race, want %d", view.Messages, writers)
	}
}

func TestStoreFlagsEmitsChangePerUID(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	if _, err := g.Select("alice", "INBOX"); err != nil {
		t.Fatalf("select: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.Append("alice", "INBOX", message("draft body", "\\Draft")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	changes, err := g.StoreFlags("alice", "INBOX", []uint32{2, 1}, 0)
	if err != nil {
		t.Fatalf("store flags: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("store flags produced %d changes, want one per uid", len(changes))
	}
	if changes[0].UID != 1 || changes[0].ModSeq != 4 {
		t.Fatalf("first change = %+v, want uid 1 with modseq 4", changes[0])
	}
	if changes[1].UID != 2 || changes[1].ModSeq != 5 {
		t.Fatalf("second change = %+v, want uid 2 with modseq 5", changes[1])
	}
	for _, c := range changes {
		if c.Kind != impl.KindFlags {
			t.Fatalf("change kind = %q, want %q", c.Kind, impl.KindFlags)
		}
		if len(c.Added) != 1 || c.Added[0] != "\\Seen" {
			t.Fatalf("added flags = %v, want [\\Seen]", c.Added)
		}
		if len(c.Removed) != 1 || c.Removed[0] != "\\Draft" {
			t.Fatalf("removed flags = %v, want [\\Draft]", c.Removed)
		}
	}
	msgs, err := g.Fetch("alice", "INBOX", 1, []uint32{1, 2})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("fetch returned %d messages, want 2", len(msgs))
	}
	for _, m := range msgs {
		if len(m.Flags) != 1 || m.Flags[0] != "\\Seen" {
			t.Fatalf("message flags = %v, want [\\Seen]", m.Flags)
		}
	}
}

func TestStoreFlagsUnknownUIDIgnored(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	if _, err := g.Append("alice", "INBOX", message("only body")); err != nil {
		t.Fatalf("append: %v", err)
	}
	before, _ := g.Select("alice", "INBOX")
	changes, err := g.StoreFlags("alice", "INBOX", []uint32{9, 10}, 0)
	if err != nil {
		t.Fatalf("store flags on missing uids: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("store flags on missing uids produced %d changes, want none", len(changes))
	}
	after, _ := g.Select("alice", "INBOX")
	if after.ModSeq != before.ModSeq {
		t.Fatalf("modseq moved from %d to %d without a visible change", before.ModSeq, after.ModSeq)
	}
}

func TestStoreFlagsRejectsFutureModSeq(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	if _, err := g.StoreFlags("alice", "SENT", []uint32{1}, 0); !errors.Is(err, impl.ErrNoMailbox) {
		t.Fatalf("store flags on an unknown mailbox must fail with ErrNoMailbox, got %v", err)
	}
	if _, err := g.Append("alice", "INBOX", message("body one")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := g.StoreFlags("alice", "INBOX", []uint32{1}, 99); !errors.Is(err, impl.ErrModSeqAhead) {
		t.Fatalf("a modseq past the mailbox must fail with ErrModSeqAhead, got %v", err)
	}
	if _, err := g.StoreFlags("alice", "INBOX", []uint32{1}, 2); err != nil {
		t.Fatalf("a modseq at the mailbox waterline must be accepted, got %v", err)
	}
}

func TestExpungeRemovesAndReports(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	for i := 0; i < 3; i++ {
		if _, err := g.Append("alice", "INBOX", message("body")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	removed, err := g.Expunge("alice", "INBOX", []uint32{3, 2, 42})
	if err != nil {
		t.Fatalf("expunge: %v", err)
	}
	uidSet(t, removed, 2, 3)
	view, _ := g.Select("alice", "INBOX")
	if view.Messages != 1 {
		t.Fatalf("messages = %d after the expunge, want 1", view.Messages)
	}
	uidSet(t, g.Tombstones("alice", "INBOX"), 2, 3)
	changes := g.Changes("alice", "INBOX")
	if len(changes) != 5 {
		t.Fatalf("change log holds %d entries, want 5 (three appends and two expunges)", len(changes))
	}
	if changes[3].Kind != impl.KindExpunge || changes[3].UID != 2 {
		t.Fatalf("fourth change = %+v, want the expunge of uid 2", changes[3])
	}
	if changes[4].Kind != impl.KindExpunge || changes[4].UID != 3 {
		t.Fatalf("fifth change = %+v, want the expunge of uid 3", changes[4])
	}
	msgs, err := g.Fetch("alice", "INBOX", 1, []uint32{1, 2, 3})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "body" {
		t.Fatalf("fetch after expunge returned %+v, want only uid 1", msgs)
	}
}

func TestChangeLogRespectsCap(t *testing.T) {
	g := impl.NewImapMailboxGuard(impl.Limits{MaxChangeLog: 3, MaxMailboxes: 4})
	if _, err := g.Select("alice", "INBOX"); err != nil {
		t.Fatalf("select: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := g.Append("alice", "INBOX", message("body")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	changes := g.Changes("alice", "INBOX")
	if len(changes) != 3 {
		t.Fatalf("change log holds %d entries, want the cap 3", len(changes))
	}
	if changes[0].UID != 4 || changes[2].UID != 6 {
		t.Fatalf("retained change uids = %d..%d, want the newest 4..6", changes[0].UID, changes[2].UID)
	}
	for i := 1; i < len(changes); i++ {
		if changes[i].ModSeq <= changes[i-1].ModSeq {
			t.Fatalf("retained modseq values are not increasing: %d then %d", changes[i-1].ModSeq, changes[i].ModSeq)
		}
	}
}

func TestFetchSinceReturnsOnlyNewer(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	for i := 0; i < 3; i++ {
		if _, err := g.Append("alice", "INBOX", message("body")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	delta, err := g.FetchSince("alice", "INBOX", 2)
	if err != nil {
		t.Fatalf("fetch since: %v", err)
	}
	if delta.FullResync {
		t.Fatalf("a request inside the window must be served incrementally")
	}
	if delta.UIDValidity != 1 {
		t.Fatalf("delta uidvalidity = %d, want 1", delta.UIDValidity)
	}
	if delta.Since != 2 || delta.ModSeq != 4 {
		t.Fatalf("delta since/modseq = %d/%d, want 2/4", delta.Since, delta.ModSeq)
	}
	if len(delta.Changes) != 2 {
		t.Fatalf("delta carries %d changes, want 2", len(delta.Changes))
	}
	if delta.Changes[0].UID != 2 || delta.Changes[1].UID != 3 {
		t.Fatalf("delta uids = %d,%d, want 2,3", delta.Changes[0].UID, delta.Changes[1].UID)
	}
	empty, err := g.FetchSince("alice", "INBOX", 4)
	if err != nil {
		t.Fatalf("fetch since the waterline: %v", err)
	}
	if len(empty.Changes) != 0 || empty.FullResync {
		t.Fatalf("a request at the waterline returned %+v, want an empty incremental delta", empty)
	}
}

func TestFetchRejectsStaleUIDValidity(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	if _, err := g.Append("alice", "INBOX", message("body")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := g.Fetch("alice", "INBOX", 1, []uint32{1}); err != nil {
		t.Fatalf("fetch under the live uidvalidity: %v", err)
	}
	next, err := g.BumpUIDValidity("alice", "INBOX")
	if err != nil {
		t.Fatalf("bump uidvalidity: %v", err)
	}
	if next != 2 {
		t.Fatalf("new uidvalidity = %d, want 2", next)
	}
	if _, err := g.Fetch("alice", "INBOX", 1, []uint32{1}); !errors.Is(err, impl.ErrStaleValidity) {
		t.Fatalf("fetch under the retired uidvalidity must fail with ErrStaleValidity, got %v", err)
	}
	view, _ := g.Select("alice", "INBOX")
	if view.Messages != 0 {
		t.Fatalf("messages = %d after the rebuild, want 0", view.Messages)
	}
	if view.UIDNext != 1 {
		t.Fatalf("uidnext = %d after the rebuild, want 1", view.UIDNext)
	}
}

func TestUIDNeverReusedAfterExpunge(t *testing.T) {
	g := impl.NewImapMailboxGuard(mailboxLimits())
	for i := 0; i < 2; i++ {
		if _, err := g.Append("alice", "INBOX", message("body")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if _, err := g.Expunge("alice", "INBOX", []uint32{2}); err != nil {
		t.Fatalf("expunge: %v", err)
	}
	uid, err := g.Append("alice", "INBOX", message("body after expunge"))
	if err != nil {
		t.Fatalf("append after expunge: %v", err)
	}
	if uid != 3 {
		t.Fatalf("append after expunge returned uid %d, want 3 without recycling 2", uid)
	}
	for _, tomb := range g.Tombstones("alice", "INBOX") {
		if tomb == uid {
			t.Fatalf("the new uid %d collides with a tombstone", uid)
		}
	}
}

func TestSnapshotRestoreKeepsCounters(t *testing.T) {
	limits := mailboxLimits()
	g := impl.NewImapMailboxGuard(limits)
	if _, err := g.Append("alice", "INBOX", message("first", "\\Draft")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := g.Append("alice", "INBOX", message("second")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := g.Expunge("alice", "INBOX", []uint32{1}); err != nil {
		t.Fatalf("expunge: %v", err)
	}
	before, _ := g.Select("alice", "INBOX")
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewImapMailboxGuard(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after, err := restored.Select("alice", "INBOX")
	if err != nil {
		t.Fatalf("select after restore: %v", err)
	}
	if after.UIDValidity != before.UIDValidity || after.UIDNext != before.UIDNext || after.ModSeq != before.ModSeq {
		t.Fatalf("restored view = %+v, want the pre snapshot %+v", after, before)
	}
	if after.Messages != 1 {
		t.Fatalf("restored messages = %d, want 1", after.Messages)
	}
	uidSet(t, restored.Tombstones("alice", "INBOX"), 1)
	if got := len(restored.Changes("alice", "INBOX")); got != 3 {
		t.Fatalf("restored change log holds %d entries, want 3", got)
	}
	uid, err := restored.Append("alice", "INBOX", message("after restart"))
	if err != nil {
		t.Fatalf("append after restore: %v", err)
	}
	if uid != before.UIDNext {
		t.Fatalf("append after restore returned uid %d, want %d", uid, before.UIDNext)
	}
	delta, err := restored.FetchSince("alice", "INBOX", before.ModSeq)
	if err != nil {
		t.Fatalf("fetch since after restore: %v", err)
	}
	if delta.FullResync || len(delta.Changes) != 1 || delta.Changes[0].UID != uid {
		t.Fatalf("delta after restore = %+v, want one incremental append", delta)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	limits := mailboxLimits()
	g := impl.NewImapMailboxGuard(limits)
	if _, err := g.Append("alice", "INBOX", message("body")); err != nil {
		t.Fatalf("append: %v", err)
	}
	before, _ := g.Select("alice", "INBOX")
	if err := g.Restore([]byte(`{"mailboxes":{"alice/INBOX":`)); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	after, selErr := g.Select("alice", "INBOX")
	if selErr != nil {
		t.Fatalf("select after a rejected restore: %v", selErr)
	}
	if after.UIDNext != before.UIDNext || after.ModSeq != before.ModSeq || after.Messages != before.Messages {
		t.Fatalf("live view changed to %+v from %+v after a rejected restore", after, before)
	}
	uid, err := g.Append("alice", "INBOX", message("still writable"))
	if err != nil {
		t.Fatalf("the live guard must keep working, got %v", err)
	}
	if uid != 2 {
		t.Fatalf("append after a rejected restore returned uid %d, want 2", uid)
	}
}
