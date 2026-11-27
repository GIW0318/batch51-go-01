package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-d-09"
)

func socketConfig() impl.GuardConfig {
	return impl.GuardConfig{
		MaxSockets:      16,
		PortBase:        100,
		PortCount:       32,
		MaxQueue:        4,
		DumpPageSize:    2,
		AllowedFamilies: []int{0, 2, 9, 16},
	}
}

func plainCred() impl.Cred {
	return impl.Cred{UID: 1000, NS: "netns-a"}
}

func openSocket(t *testing.T, g *impl.NetlinkSocketGuard, family int, cred impl.Cred) impl.Socket {
	t.Helper()
	s, err := g.Open(family, cred)
	if err != nil {
		t.Fatalf("open family %d must succeed, got %v", family, err)
	}
	return s
}

func TestOpenAssignsSocketAndRejectsUnknownFamily(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	if _, err := g.Open(-1, plainCred()); !errors.Is(err, impl.ErrBadFamily) {
		t.Fatalf("negative family must fail with ErrBadFamily, got %v", err)
	}
	if _, err := g.Open(99, plainCred()); !errors.Is(err, impl.ErrUnsupportedFamily) {
		t.Fatalf("unknown family must fail with ErrUnsupportedFamily, got %v", err)
	}
	s := openSocket(t, g, 0, plainCred())
	if s.ID == "" {
		t.Fatalf("socket id must not be empty")
	}
	if s.State != impl.SocketOpened {
		t.Fatalf("socket state = %q, want %q", s.State, impl.SocketOpened)
	}
	if s.PortID != 0 {
		t.Fatalf("socket port id before bind = %d, want 0", s.PortID)
	}
	if s.NS != "netns-a" {
		t.Fatalf("socket namespace = %q, want the caller namespace", s.NS)
	}
	if view, ok := g.Socket(s.ID); !ok || view.Family != 0 {
		t.Fatalf("socket view after open = %+v ok=%v", view, ok)
	}
}

func TestOpenPrivilegedFamilyRequiresCapability(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	if _, err := g.Open(9, plainCred()); !errors.Is(err, impl.ErrPermission) {
		t.Fatalf("audit family without NET_ADMIN must fail with ErrPermission, got %v", err)
	}
	admin := impl.Cred{UID: 0, CapNetAdmin: true, NS: "netns-a"}
	s := openSocket(t, g, 9, admin)
	if s.Family != 9 {
		t.Fatalf("socket family = %d, want 9", s.Family)
	}
	if _, err := g.Open(12, admin); !errors.Is(err, impl.ErrUnsupportedFamily) {
		t.Fatalf("family 12 is outside the allowed list, got %v", err)
	}
}

func TestOpenEnforcesSocketLimit(t *testing.T) {
	cfg := socketConfig()
	cfg.MaxSockets = 2
	g := impl.NewNetlinkSocketGuard(cfg)
	openSocket(t, g, 0, plainCred())
	openSocket(t, g, 2, plainCred())
	if _, err := g.Open(0, plainCred()); !errors.Is(err, impl.ErrSocketLimit) {
		t.Fatalf("third socket must fail with ErrSocketLimit, got %v", err)
	}
}

func TestBindAssignsPortFromPool(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	s := openSocket(t, g, 0, plainCred())
	result, err := g.Bind(s.ID, []uint32{3, 1, 2})
	if err != nil {
		t.Fatalf("bind must succeed, got %v", err)
	}
	if result.PortID != 100 {
		t.Fatalf("first port id = %d, want the pool base 100", result.PortID)
	}
	if result.Gen == 0 {
		t.Fatalf("bind generation must be a positive number")
	}
	if len(result.Groups) != 3 || result.Groups[0] != 1 || result.Groups[1] != 2 || result.Groups[2] != 3 {
		t.Fatalf("bound groups = %v, want the sorted set [1 2 3]", result.Groups)
	}
	view, ok := g.Socket(s.ID)
	if !ok {
		t.Fatalf("socket must stay readable after bind")
	}
	if view.State != impl.SocketBound || view.PortID != 100 || len(view.Groups) != 3 {
		t.Fatalf("socket view after bind = %+v", view)
	}
	other := openSocket(t, g, 0, plainCred())
	second, err := g.Bind(other.ID, []uint32{5})
	if err != nil {
		t.Fatalf("second bind must succeed, got %v", err)
	}
	if second.PortID != 101 {
		t.Fatalf("second port id = %d, want 101", second.PortID)
	}
}

func TestBindRejectsBadGroups(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	s := openSocket(t, g, 0, plainCred())
	if _, err := g.Bind(s.ID, []uint32{1, 1}); !errors.Is(err, impl.ErrDuplicateGroup) {
		t.Fatalf("duplicate group must fail with ErrDuplicateGroup, got %v", err)
	}
	if _, err := g.Bind(s.ID, []uint32{0}); !errors.Is(err, impl.ErrBadGroup) {
		t.Fatalf("group zero must fail with ErrBadGroup, got %v", err)
	}
	if _, err := g.Bind(s.ID, []uint32{33}); !errors.Is(err, impl.ErrBadGroup) {
		t.Fatalf("group above the mask must fail with ErrBadGroup, got %v", err)
	}
	if view, _ := g.Socket(s.ID); view.State != impl.SocketOpened {
		t.Fatalf("rejected binds changed the socket state to %q", view.State)
	}
}

func TestBindTwiceRejected(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	s := openSocket(t, g, 0, plainCred())
	if _, err := g.Bind(s.ID, []uint32{1}); err != nil {
		t.Fatalf("first bind must succeed, got %v", err)
	}
	if _, err := g.Bind(s.ID, []uint32{2}); !errors.Is(err, impl.ErrAlreadyBound) {
		t.Fatalf("second bind on the same socket must fail with ErrAlreadyBound, got %v", err)
	}
	if _, err := g.Bind("sock-404", []uint32{1}); !errors.Is(err, impl.ErrUnknownSocket) {
		t.Fatalf("bind on an unknown socket must fail with ErrUnknownSocket, got %v", err)
	}
}

func TestBindPortExclusivePerPort(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	first := openSocket(t, g, 0, plainCred())
	second := openSocket(t, g, 0, plainCred())
	result, err := g.BindPort(first.ID, 105, []uint32{2})
	if err != nil {
		t.Fatalf("explicit bind must succeed, got %v", err)
	}
	if result.PortID != 105 {
		t.Fatalf("explicit port id = %d, want 105", result.PortID)
	}
	if _, err := g.BindPort(second.ID, 105, []uint32{2}); !errors.Is(err, impl.ErrPortInUse) {
		t.Fatalf("second bind on one port must fail with ErrPortInUse, got %v", err)
	}
	if _, err := g.BindPort(second.ID, 900, []uint32{2}); !errors.Is(err, impl.ErrBadPort) {
		t.Fatalf("port outside the range must fail with ErrBadPort, got %v", err)
	}
	if _, err := g.BindPort(second.ID, 106, []uint32{2}); err != nil {
		t.Fatalf("free explicit port must be bindable, got %v", err)
	}
}

func TestConcurrentBindPortSingleWinner(t *testing.T) {
	cfg := socketConfig()
	cfg.MaxSockets = 32
	g := impl.NewNetlinkSocketGuard(cfg)
	const workers = 16
	sockets := make([]impl.Socket, 0, workers)
	for i := 0; i < workers; i++ {
		sockets = append(sockets, openSocket(t, g, 0, plainCred()))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := make(chan struct{})
	won, inuse, other := 0, 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-started
			_, err := g.BindPort(id, 110, []uint32{4})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, impl.ErrPortInUse):
				inuse++
			default:
				other++
			}
		}(sockets[i].ID)
	}
	close(started)
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d concurrent binds won port 110, want exactly 1", won)
	}
	if inuse != workers-1 {
		t.Fatalf("%d binds reported ErrPortInUse, want %d", inuse, workers-1)
	}
	if other != 0 {
		t.Fatalf("%d binds failed with an unexpected error", other)
	}
}

func TestSendRequiresBoundSocketAndKind(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	s := openSocket(t, g, 0, plainCred())
	if err := g.Send(s.ID, impl.Message{Kind: impl.KindData, Payload: "x"}); !errors.Is(err, impl.ErrNotBound) {
		t.Fatalf("send on an unbound socket must fail with ErrNotBound, got %v", err)
	}
	if _, err := g.Bind(s.ID, []uint32{1}); err != nil {
		t.Fatalf("bind must succeed, got %v", err)
	}
	if err := g.Send(s.ID, impl.Message{Kind: "garbage", Payload: "x"}); !errors.Is(err, impl.ErrBadMessage) {
		t.Fatalf("unknown message kind must fail with ErrBadMessage, got %v", err)
	}
	if err := g.Send(s.ID, impl.Message{Kind: impl.KindData, Group: 7, Payload: "x"}); !errors.Is(err, impl.ErrUnknownGroup) {
		t.Fatalf("send to an unsubscribed group must fail with ErrUnknownGroup, got %v", err)
	}
	if err := g.Send(s.ID, impl.Message{Kind: impl.KindAck, Group: 1, Payload: "hello"}); err != nil {
		t.Fatalf("send to a subscribed group must succeed, got %v", err)
	}
	if err := g.Send("sock-404", impl.Message{Kind: impl.KindData}); !errors.Is(err, impl.ErrUnknownSocket) {
		t.Fatalf("send on an unknown socket must fail with ErrUnknownSocket, got %v", err)
	}
}

func TestQueueCapRejects(t *testing.T) {
	cfg := socketConfig()
	cfg.MaxQueue = 2
	g := impl.NewNetlinkSocketGuard(cfg)
	s := openSocket(t, g, 0, plainCred())
	if _, err := g.Bind(s.ID, []uint32{1}); err != nil {
		t.Fatalf("bind must succeed, got %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := g.Send(s.ID, impl.Message{Kind: impl.KindData, Payload: "p"}); err != nil {
			t.Fatalf("send %d must succeed, got %v", i, err)
		}
	}
	if err := g.Send(s.ID, impl.Message{Kind: impl.KindData, Payload: "over"}); !errors.Is(err, impl.ErrQueueFull) {
		t.Fatalf("send past the queue cap must fail with ErrQueueFull, got %v", err)
	}
	view, _ := g.Socket(s.ID)
	if view.Queued != 2 {
		t.Fatalf("queued messages = %d, want the configured cap 2", view.Queued)
	}
}

func TestRecvPreservesOrder(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	s := openSocket(t, g, 0, plainCred())
	if _, err := g.Bind(s.ID, []uint32{1, 2}); err != nil {
		t.Fatalf("bind must succeed, got %v", err)
	}
	kinds := []string{impl.KindAck, impl.KindData, impl.KindControl}
	for _, kind := range kinds {
		if err := g.Send(s.ID, impl.Message{Kind: kind, Group: 1, Payload: kind}); err != nil {
			t.Fatalf("send %s: %v", kind, err)
		}
	}
	batch, err := g.Recv(s.ID, 2)
	if err != nil {
		t.Fatalf("recv must succeed, got %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("first batch holds %d messages, want 2", len(batch))
	}
	if batch[0].Kind != impl.KindAck || batch[1].Kind != impl.KindData {
		t.Fatalf("batch order = %s, %s; want ack then data", batch[0].Kind, batch[1].Kind)
	}
	if batch[0].Seq == 0 || batch[1].Seq <= batch[0].Seq {
		t.Fatalf("message sequence must increase: %d then %d", batch[0].Seq, batch[1].Seq)
	}
	if batch[0].PortID != 100 {
		t.Fatalf("delivered port id = %d, want the bound port 100", batch[0].PortID)
	}
	rest, err := g.Recv(s.ID, 8)
	if err != nil {
		t.Fatalf("second recv must succeed, got %v", err)
	}
	if len(rest) != 1 || rest[0].Kind != impl.KindControl {
		t.Fatalf("remaining batch = %+v, want the single control message", rest)
	}
	if got, err := g.Recv(s.ID, 4); err != nil || len(got) != 0 {
		t.Fatalf("empty recv returned %d messages, %v; want 0, nil", len(got), err)
	}
}

func TestRecvRejectsBadMax(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	s := openSocket(t, g, 0, plainCred())
	if _, err := g.Bind(s.ID, []uint32{1}); err != nil {
		t.Fatalf("bind must succeed, got %v", err)
	}
	if _, err := g.Recv(s.ID, 0); !errors.Is(err, impl.ErrBadMax) {
		t.Fatalf("zero limit must fail with ErrBadMax, got %v", err)
	}
	if _, err := g.Recv(s.ID, -3); !errors.Is(err, impl.ErrBadMax) {
		t.Fatalf("negative limit must fail with ErrBadMax, got %v", err)
	}
	if _, err := g.Recv("sock-404", 1); !errors.Is(err, impl.ErrUnknownSocket) {
		t.Fatalf("recv on an unknown socket must fail with ErrUnknownSocket, got %v", err)
	}
}

func TestDumpPagesThroughCursor(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	for _, name := range []string{"obj-a", "obj-b", "obj-c", "obj-d", "obj-e"} {
		if err := g.RegisterObject(0, "netns-a", name); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if err := g.RegisterObject(0, "netns-b", "obj-foreign"); err != nil {
		t.Fatalf("register foreign object: %v", err)
	}
	s := openSocket(t, g, 0, plainCred())
	if _, err := g.Bind(s.ID, []uint32{1}); err != nil {
		t.Fatalf("bind must succeed, got %v", err)
	}
	cursor, err := g.Dump(s.ID, impl.DumpRequest{Family: 0, Filter: "obj-"})
	if err != nil {
		t.Fatalf("dump must succeed, got %v", err)
	}
	if cursor.Total != 5 {
		t.Fatalf("dump total = %d, want the five objects of this namespace", cursor.Total)
	}
	if cursor.Done {
		t.Fatalf("fresh cursor must not be done")
	}
	page, err := g.DumpPage(cursor.ID)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0] != "obj-a" || page.Items[1] != "obj-b" {
		t.Fatalf("first page items = %v, want obj-a and obj-b", page.Items)
	}
	if page.Cursor.Done {
		t.Fatalf("cursor must not be done after the first page")
	}
	page, err = g.DumpPage(cursor.ID)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0] != "obj-c" {
		t.Fatalf("second page items = %v, want obj-c and obj-d", page.Items)
	}
	page, err = g.DumpPage(cursor.ID)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0] != "obj-e" {
		t.Fatalf("third page items = %v, want obj-e", page.Items)
	}
	if !page.Cursor.Done {
		t.Fatalf("cursor must be done after the last page")
	}
}

func TestDumpRejectsFamilyMismatchAndUnknownCursor(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	s := openSocket(t, g, 0, plainCred())
	if _, err := g.Bind(s.ID, []uint32{1}); err != nil {
		t.Fatalf("bind must succeed, got %v", err)
	}
	if _, err := g.Dump(s.ID, impl.DumpRequest{Family: 2, Filter: ""}); !errors.Is(err, impl.ErrFamilyMismatch) {
		t.Fatalf("dump family mismatch must fail with ErrFamilyMismatch, got %v", err)
	}
	if _, err := g.Dump("sock-404", impl.DumpRequest{Family: 0}); !errors.Is(err, impl.ErrUnknownSocket) {
		t.Fatalf("dump on an unknown socket must fail with ErrUnknownSocket, got %v", err)
	}
	if _, err := g.DumpPage("dump-404"); !errors.Is(err, impl.ErrUnknownCursor) {
		t.Fatalf("unknown cursor must fail with ErrUnknownCursor, got %v", err)
	}
	if err := g.RegisterObject(0, "  ", "obj"); !errors.Is(err, impl.ErrBadObject) {
		t.Fatalf("blank namespace must fail with ErrBadObject, got %v", err)
	}
	if err := g.RegisterObject(0, "netns-a", ""); !errors.Is(err, impl.ErrBadObject) {
		t.Fatalf("blank object name must fail with ErrBadObject, got %v", err)
	}
}

func TestSnapshotRestoreKeepsBindingsAndCursors(t *testing.T) {
	g := impl.NewNetlinkSocketGuard(socketConfig())
	for _, name := range []string{"obj-a", "obj-b", "obj-c", "obj-d", "obj-e"} {
		if err := g.RegisterObject(0, "netns-a", name); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	s := openSocket(t, g, 0, plainCred())
	bind, err := g.Bind(s.ID, []uint32{1, 2})
	if err != nil {
		t.Fatalf("bind must succeed, got %v", err)
	}
	if err := g.Send(s.ID, impl.Message{Kind: impl.KindAck, Group: 1, Payload: "one"}); err != nil {
		t.Fatalf("send one: %v", err)
	}
	if err := g.Send(s.ID, impl.Message{Kind: impl.KindData, Group: 2, Payload: "two"}); err != nil {
		t.Fatalf("send two: %v", err)
	}
	cursor, err := g.Dump(s.ID, impl.DumpRequest{Family: 0, Filter: "obj-"})
	if err != nil {
		t.Fatalf("dump must succeed, got %v", err)
	}
	if _, err := g.DumpPage(cursor.ID); err != nil {
		t.Fatalf("first page: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewNetlinkSocketGuard(socketConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	view, ok := restored.Socket(s.ID)
	if !ok {
		t.Fatalf("restored guard lost the socket")
	}
	if view.PortID != bind.PortID || view.Gen != bind.Gen || view.State != impl.SocketBound {
		t.Fatalf("restored socket binding mismatch: %+v want port %d gen %d", view, bind.PortID, bind.Gen)
	}
	queued, err := restored.Recv(s.ID, 4)
	if err != nil {
		t.Fatalf("recv after restore: %v", err)
	}
	if len(queued) != 2 || queued[0].Payload != "one" || queued[1].Payload != "two" {
		t.Fatalf("restored queue = %+v, want the two pending messages in order", queued)
	}
	page, err := restored.DumpPage(cursor.ID)
	if err != nil {
		t.Fatalf("dump page after restore: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0] != "obj-c" {
		t.Fatalf("resumed dump page = %v, want the dump to continue at obj-c", page.Items)
	}
	if err := restored.Restore([]byte("{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if view, ok := restored.Socket(s.ID); !ok || view.PortID != bind.PortID {
		t.Fatalf("failed restore touched the socket table: %+v ok=%v", view, ok)
	}
}
