package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-b-13"
)

const testDpid = "00:00:00:00:00:00:00:01"

func bundleLimits() impl.Limits {
	return impl.Limits{MaxBundles: 32, MaxMsgsPerBundle: 64, MaxPending: 1024}
}

func flowMsg(xid uint32, table, priority int, match string) impl.OfMsg {
	return impl.OfMsg{Xid: xid, Type: "flow_mod", Table: table, Priority: priority, Match: match}
}

func mustOpen(t *testing.T, g *impl.OpenflowBundleGuard, dpid string, xid uint32) string {
	t.Helper()
	id, err := g.OpenBundle(dpid, impl.BundleReq{Xid: xid, Kind: "atomic"})
	if err != nil {
		t.Fatalf("open bundle on %s: %v", dpid, err)
	}
	return id
}

func TestOpenBundleValidatesDpid(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	if _, err := g.OpenBundle("   ", impl.BundleReq{Xid: 1}); !errors.Is(err, impl.ErrBadDpid) {
		t.Fatalf("blank dpid must fail with ErrBadDpid, got %v", err)
	}
	if _, err := g.OpenBundle("", impl.BundleReq{Xid: 1}); !errors.Is(err, impl.ErrBadDpid) {
		t.Fatalf("empty dpid must fail with ErrBadDpid, got %v", err)
	}
	id := mustOpen(t, g, testDpid, 7)
	if id == "" {
		t.Fatalf("a bundle must carry an identifier")
	}
	other := mustOpen(t, g, testDpid, 8)
	if other == id {
		t.Fatalf("bundle identifiers must not repeat")
	}
}

func TestAddRejectsUnknownOrClosedBundle(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	if err := g.Add("bundle-404", flowMsg(11, 0, 10, "eth_type=0x800")); !errors.Is(err, impl.ErrUnknownBundle) {
		t.Fatalf("adding to an unknown bundle must fail with ErrUnknownBundle, got %v", err)
	}
	id := mustOpen(t, g, testDpid, 1)
	if err := g.Add(id, flowMsg(11, 0, 10, "eth_type=0x800")); err != nil {
		t.Fatalf("add to an open bundle: %v", err)
	}
	if _, err := g.Commit(id, impl.CommitProps{}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := g.Add(id, flowMsg(12, 0, 20, "ipv4_dst=10.0.0.0/8")); !errors.Is(err, impl.ErrUnknownBundle) {
		t.Fatalf("adding to a committed bundle must fail with ErrUnknownBundle, got %v", err)
	}
}

func TestAddRejectsUnsupportedMessageType(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	id := mustOpen(t, g, testDpid, 1)
	if err := g.Add(id, impl.OfMsg{Xid: 21, Type: "barrier_request"}); !errors.Is(err, impl.ErrBadMessage) {
		t.Fatalf("an unsupported message type must fail with ErrBadMessage, got %v", err)
	}
	if err := g.Add(id, impl.OfMsg{Xid: 0, Type: "flow_mod"}); !errors.Is(err, impl.ErrBadMessage) {
		t.Fatalf("a message without an xid must fail with ErrBadMessage, got %v", err)
	}
	if err := g.Add(id, flowMsg(22, 0, 5, "eth_type=0x86dd")); err != nil {
		t.Fatalf("a supported message must be accepted, got %v", err)
	}
}

func TestCommitAppliesAllMessagesAtomically(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	id := mustOpen(t, g, testDpid, 1)
	msgs := []impl.OfMsg{
		flowMsg(11, 0, 10, "eth_type=0x800"),
		flowMsg(12, 0, 20, "ipv4_dst=10.0.0.0/8"),
		flowMsg(13, 1, 5, "tcp_dst=443"),
	}
	for _, m := range msgs {
		if err := g.Add(id, m); err != nil {
			t.Fatalf("add %d: %v", m.Xid, err)
		}
	}
	cr, err := g.Commit(id, impl.CommitProps{Atomic: true})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if cr.Applied != 3 {
		t.Fatalf("commit applied %d messages, want 3", cr.Applied)
	}
	if cr.BundleID != id {
		t.Fatalf("commit result bundle = %q, want %q", cr.BundleID, id)
	}
	table := g.FlowTable(testDpid)
	if len(table) != 3 {
		t.Fatalf("flow table holds %d messages, want 3", len(table))
	}
	for i, want := range msgs {
		if table[i].Xid != want.Xid {
			t.Fatalf("flow table entry %d xid = %d, want %d in add order", i, table[i].Xid, want.Xid)
		}
		if table[i].Priority != want.Priority || table[i].Match != want.Match {
			t.Fatalf("flow table entry %d = %+v, want %+v", i, table[i], want)
		}
	}
}

func TestCommitEmptyBundleRejected(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	id := mustOpen(t, g, testDpid, 1)
	if _, err := g.Commit(id, impl.CommitProps{}); !errors.Is(err, impl.ErrEmptyBundle) {
		t.Fatalf("committing an empty bundle must fail with ErrEmptyBundle, got %v", err)
	}
	if got := len(g.FlowTable(testDpid)); got != 0 {
		t.Fatalf("a rejected commit changed the flow table to %d entries", got)
	}
	b, ok := g.BundleState(id)
	if !ok || b.State != impl.BundleOpen {
		t.Fatalf("the bundle after a rejected commit state=%q ok=%v, want open", b.State, ok)
	}
}

func TestCommitIsSerializedPerDpid(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(impl.Limits{MaxBundles: 64, MaxMsgsPerBundle: 8, MaxPending: 1024})
	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	applied := make([]int, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := g.OpenBundle(testDpid, impl.BundleReq{Xid: uint32(100 + i)})
			if err != nil {
				errs[i] = err
				return
			}
			if err := g.Add(id, flowMsg(uint32(200+i), 0, i, fmt.Sprintf("cookie=%d", i))); err != nil {
				errs[i] = err
				return
			}
			cr, err := g.Commit(id, impl.CommitProps{})
			if err != nil {
				errs[i] = err
				return
			}
			applied[i] = cr.Applied
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
		if applied[i] != 1 {
			t.Fatalf("writer %d applied %d messages, want 1", i, applied[i])
		}
	}
	if got := len(g.FlowTable(testDpid)); got != writers {
		t.Fatalf("flow table holds %d messages after the race, want %d with none lost", got, writers)
	}
}

func TestBarrierCountsOutstandingReplies(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	id := mustOpen(t, g, testDpid, 1)
	if err := g.Add(id, flowMsg(11, 0, 10, "eth_type=0x800")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := g.Add(id, flowMsg(12, 0, 20, "eth_type=0x86dd")); err != nil {
		t.Fatalf("add: %v", err)
	}
	cr, err := g.Commit(id, impl.CommitProps{Barrier: true})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if cr.Barrier != 2 {
		t.Fatalf("commit left %d replies outstanding, want 2", cr.Barrier)
	}
	ticket, err := g.Barrier(testDpid)
	if err != nil {
		t.Fatalf("barrier: %v", err)
	}
	if ticket.Pending != 2 {
		t.Fatalf("barrier pending = %d, want 2", ticket.Pending)
	}
	if ticket.Done {
		t.Fatalf("a barrier with outstanding replies must not be done")
	}
	if ticket.Dpid != testDpid {
		t.Fatalf("ticket dpid = %q, want %q", ticket.Dpid, testDpid)
	}
	if ticket.Role != impl.RoleMaster {
		t.Fatalf("ticket role = %d, want master for a fresh datapath", ticket.Role)
	}
	if err := g.BarrierReply(testDpid, 11); err != nil {
		t.Fatalf("first reply: %v", err)
	}
	state, ok := g.BarrierState(testDpid)
	if !ok {
		t.Fatalf("barrier state must be readable after a barrier")
	}
	if state.Pending != 1 {
		t.Fatalf("barrier pending = %d after one reply, want 1", state.Pending)
	}
	if err := g.BarrierReply(testDpid, 12); err != nil {
		t.Fatalf("second reply: %v", err)
	}
	state, _ = g.BarrierState(testDpid)
	if state.Pending != 0 || !state.Done {
		t.Fatalf("barrier state = %+v, want pending 0 and done", state)
	}
}

func TestBarrierUnknownDpid(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	if _, err := g.Barrier("00:00:00:00:00:00:00:99"); !errors.Is(err, impl.ErrUnknownDpid) {
		t.Fatalf("barrier on an unregistered dpid must fail with ErrUnknownDpid, got %v", err)
	}
	if err := g.BarrierReply("00:00:00:00:00:00:00:99", 1); !errors.Is(err, impl.ErrUnknownDpid) {
		t.Fatalf("reply on an unregistered dpid must fail with ErrUnknownDpid, got %v", err)
	}
	if _, ok := g.BarrierState("00:00:00:00:00:00:00:99"); ok {
		t.Fatalf("an unregistered dpid must have no barrier state")
	}
}

func TestSetRoleMovesGenerationForward(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	st, err := g.SetRole(testDpid, impl.RoleMaster, 1)
	if err != nil {
		t.Fatalf("set master: %v", err)
	}
	if st.Role != impl.RoleMaster || st.Gen != 1 || st.Dpid != testDpid {
		t.Fatalf("role state = %+v, want master generation 1", st)
	}
	st, err = g.SetRole(testDpid, impl.RoleSlave, 2)
	if err != nil {
		t.Fatalf("set slave: %v", err)
	}
	if st.Role != impl.RoleSlave || st.Gen != 2 {
		t.Fatalf("role state = %+v, want slave generation 2", st)
	}
	if _, err := g.SetRole(testDpid, impl.Role(9), 3); !errors.Is(err, impl.ErrRoleDenied) {
		t.Fatalf("an unknown role value must fail with ErrRoleDenied, got %v", err)
	}
}

func TestSetRoleRejectsStaleGeneration(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	if _, err := g.SetRole(testDpid, impl.RoleMaster, 4); err != nil {
		t.Fatalf("set master: %v", err)
	}
	if _, err := g.SetRole(testDpid, impl.RoleEqual, 4); !errors.Is(err, impl.ErrStaleGeneration) {
		t.Fatalf("the same generation must fail with ErrStaleGeneration, got %v", err)
	}
	if _, err := g.SetRole(testDpid, impl.RoleEqual, 3); !errors.Is(err, impl.ErrStaleGeneration) {
		t.Fatalf("a lower generation must fail with ErrStaleGeneration, got %v", err)
	}
	if _, err := g.SetRole(testDpid, impl.RoleEqual, 0); !errors.Is(err, impl.ErrStaleGeneration) {
		t.Fatalf("generation 0 must fail with ErrStaleGeneration, got %v", err)
	}
}

func TestSlaveRoleRejectsWrites(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	committed := mustOpen(t, g, testDpid, 1)
	if err := g.Add(committed, flowMsg(11, 0, 10, "eth_type=0x800")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := g.Commit(committed, impl.CommitProps{}); err != nil {
		t.Fatalf("commit as master: %v", err)
	}
	pending := mustOpen(t, g, testDpid, 2)
	if err := g.Add(pending, flowMsg(12, 0, 20, "eth_type=0x86dd")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := g.SetRole(testDpid, impl.RoleSlave, 5); err != nil {
		t.Fatalf("set slave: %v", err)
	}
	if _, err := g.OpenBundle(testDpid, impl.BundleReq{Xid: 3}); !errors.Is(err, impl.ErrNotMaster) {
		t.Fatalf("opening a bundle as slave must fail with ErrNotMaster, got %v", err)
	}
	if err := g.Add(pending, flowMsg(13, 0, 30, "tcp_dst=22")); !errors.Is(err, impl.ErrNotMaster) {
		t.Fatalf("adding a message as slave must fail with ErrNotMaster, got %v", err)
	}
	if _, err := g.Commit(pending, impl.CommitProps{}); !errors.Is(err, impl.ErrNotMaster) {
		t.Fatalf("committing as slave must fail with ErrNotMaster, got %v", err)
	}
	table := g.FlowTable(testDpid)
	if len(table) != 1 {
		t.Fatalf("flow table holds %d messages, want only the master era one", len(table))
	}
	if table[0].Xid != 11 {
		t.Fatalf("flow table entry xid = %d, want 11", table[0].Xid)
	}
	ticket, err := g.Barrier(testDpid)
	if err != nil {
		t.Fatalf("barrier as slave must still be allowed, got %v", err)
	}
	if ticket.Role != impl.RoleSlave {
		t.Fatalf("ticket role = %d, want slave", ticket.Role)
	}
}

func TestReconnectRearmsPendingBarriers(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	id := mustOpen(t, g, testDpid, 1)
	if err := g.Add(id, flowMsg(11, 0, 10, "eth_type=0x800")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := g.Add(id, flowMsg(12, 0, 20, "eth_type=0x86dd")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := g.Commit(id, impl.CommitProps{Barrier: true}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	rearmed, err := g.Reconnect(testDpid)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if rearmed != 2 {
		t.Fatalf("reconnect re-armed %d fences, want 2", rearmed)
	}
	state, ok := g.BarrierState(testDpid)
	if !ok || state.Pending != 2 {
		t.Fatalf("barrier state after reconnect = %+v ok=%v, want pending 2", state, ok)
	}
	if _, err := g.Reconnect("00:00:00:00:00:00:00:99"); !errors.Is(err, impl.ErrUnknownDpid) {
		t.Fatalf("reconnect on an unregistered dpid must fail with ErrUnknownDpid, got %v", err)
	}
}

func TestBundleStateReportsMessages(t *testing.T) {
	g := impl.NewOpenflowBundleGuard(bundleLimits())
	id := mustOpen(t, g, testDpid, 1)
	if err := g.Add(id, flowMsg(11, 0, 10, "eth_type=0x800")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := g.Add(id, impl.OfMsg{Xid: 12, Type: "meter_mod", Table: 0, Match: "meter=1"}); err != nil {
		t.Fatalf("add meter: %v", err)
	}
	b, ok := g.BundleState(id)
	if !ok {
		t.Fatalf("bundle state must be readable")
	}
	if b.ID != id || b.Dpid != testDpid {
		t.Fatalf("bundle state = %+v, want id and dpid echoed", b)
	}
	if b.State != impl.BundleOpen {
		t.Fatalf("bundle state = %q, want %q", b.State, impl.BundleOpen)
	}
	if len(b.Msgs) != 2 || b.Msgs[0].Xid != 11 || b.Msgs[1].Xid != 12 {
		t.Fatalf("bundle messages = %+v, want xids 11 then 12", b.Msgs)
	}
	if _, ok := g.BundleState("bundle-404"); ok {
		t.Fatalf("an unknown bundle must not be readable")
	}
}

func TestSnapshotRestoreKeepsRoleAndPending(t *testing.T) {
	limits := bundleLimits()
	g := impl.NewOpenflowBundleGuard(limits)
	id := mustOpen(t, g, testDpid, 1)
	if err := g.Add(id, flowMsg(11, 0, 10, "eth_type=0x800")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := g.Commit(id, impl.CommitProps{Barrier: true}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := g.SetRole(testDpid, impl.RoleEqual, 3); err != nil {
		t.Fatalf("set role: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewOpenflowBundleGuard(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.FlowTable(testDpid)); got != 1 {
		t.Fatalf("restored flow table holds %d messages, want 1", got)
	}
	b, ok := restored.BundleState(id)
	if !ok || b.State != impl.BundleCommitted || len(b.Msgs) != 1 {
		t.Fatalf("restored bundle = %+v ok=%v, want a committed bundle with one message", b, ok)
	}
	state, ok := restored.BarrierState(testDpid)
	if !ok {
		t.Fatalf("restored guard lost the barrier state")
	}
	if state.Pending != 1 {
		t.Fatalf("restored barrier pending = %d, want 1", state.Pending)
	}
	if _, err := restored.SetRole(testDpid, impl.RoleMaster, 3); !errors.Is(err, impl.ErrStaleGeneration) {
		t.Fatalf("restored generation must not be reopened, got %v", err)
	}
	if err := restored.BarrierReply(testDpid, 11); err != nil {
		t.Fatalf("reply after restore: %v", err)
	}
	state, _ = restored.BarrierState(testDpid)
	if state.Pending != 0 || !state.Done {
		t.Fatalf("restored barrier state = %+v, want it to drain", state)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	limits := bundleLimits()
	g := impl.NewOpenflowBundleGuard(limits)
	id := mustOpen(t, g, testDpid, 1)
	if err := g.Add(id, flowMsg(11, 0, 10, "eth_type=0x800")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := g.Commit(id, impl.CommitProps{}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := g.Restore([]byte("<<<not json>>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(g.FlowTable(testDpid)); got != 1 {
		t.Fatalf("a rejected restore changed the flow table to %d entries", got)
	}
	next := mustOpen(t, g, testDpid, 2)
	if err := g.Add(next, flowMsg(12, 0, 20, "eth_type=0x86dd")); err != nil {
		t.Fatalf("the live guard must keep working, got %v", err)
	}
	if _, err := g.Commit(next, impl.CommitProps{}); err != nil {
		t.Fatalf("commit after a rejected restore: %v", err)
	}
	if got := len(g.FlowTable(testDpid)); got != 2 {
		t.Fatalf("flow table holds %d messages, want 2", got)
	}
}
