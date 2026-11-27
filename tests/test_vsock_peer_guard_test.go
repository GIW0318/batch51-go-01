package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-d-05"
)

func vsockConfig() impl.VsockConfig {
	return impl.VsockConfig{MaxConnsPerEndpoint: 8, MaxConns: 64, BaseCredit: 64, MaxCredit: 256}
}

func defaultPolicy() impl.PortPolicy {
	return impl.PortPolicy{
		Tenant:      "tenant-a",
		AllowedCIDs: []uint32{7, 8},
		RequireAuth: true,
		MaxCredit:   128,
	}
}

func mustListen(t *testing.T, g *impl.VsockPeerGuard, policy impl.PortPolicy) impl.Listener {
	t.Helper()
	l, err := g.Listen(impl.CidHost, 5000, policy)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return l
}

func guestPeer(cid uint32) impl.PeerAddr {
	return impl.PeerAddr{Side: impl.SideGuest, CID: cid, Port: 40000}
}

func peerCred(tenant, token string) impl.PeerCred {
	return impl.PeerCred{Tenant: tenant, UID: 1000, Token: token}
}

func mustConnect(t *testing.T, g *impl.VsockPeerGuard, peer impl.PeerAddr) impl.Conn {
	t.Helper()
	c, err := g.Connect(peer, impl.ConnectRequest{Port: 5000, Nonce: "n-1"})
	if err != nil {
		t.Fatalf("connect from %+v: %v", peer, err)
	}
	return c
}

func mustAuthorize(t *testing.T, g *impl.VsockPeerGuard, connID string) impl.Decision {
	t.Helper()
	d, err := g.Authorize(connID, peerCred("tenant-a", "tok-1"))
	if err != nil {
		t.Fatalf("authorize %s: %v", connID, err)
	}
	return d
}

func TestListenValidatesPolicy(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	blankTenant := defaultPolicy()
	blankTenant.Tenant = "   "
	if _, err := g.Listen(impl.CidHost, 5000, blankTenant); !errors.Is(err, impl.ErrBadPolicy) {
		t.Fatalf("blank tenant must fail with ErrBadPolicy, got %v", err)
	}
	noCIDs := defaultPolicy()
	noCIDs.AllowedCIDs = nil
	if _, err := g.Listen(impl.CidHost, 5000, noCIDs); !errors.Is(err, impl.ErrBadPolicy) {
		t.Fatalf("an empty cid allow list must fail with ErrBadPolicy, got %v", err)
	}
	if _, err := g.Listen(5, 5000, defaultPolicy()); !errors.Is(err, impl.ErrBadCID) {
		t.Fatalf("a guest context id must fail with ErrBadCID, got %v", err)
	}
	if _, err := g.Listen(impl.CidHost, 0, defaultPolicy()); !errors.Is(err, impl.ErrBadPort) {
		t.Fatalf("port 0 must fail with ErrBadPort, got %v", err)
	}
	if got := len(g.Listeners()); got != 0 {
		t.Fatalf("listeners after the rejects = %d, want 0", got)
	}
}

func TestListenRejectsDuplicatePort(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	l := mustListen(t, g, defaultPolicy())
	if l.ID == "" {
		t.Fatalf("listener id must not be empty")
	}
	if l.CID != impl.CidHost || l.Port != 5000 {
		t.Fatalf("listener cid=%d port=%d, want host/5000", l.CID, l.Port)
	}
	if l.Tenant != "tenant-a" {
		t.Fatalf("listener tenant = %q, want tenant-a", l.Tenant)
	}
	if len(l.AllowedCIDs) != 2 || l.AllowedCIDs[0] != 7 || l.AllowedCIDs[1] != 8 {
		t.Fatalf("listener allow list = %v, want [7 8]", l.AllowedCIDs)
	}
	if _, err := g.Listen(impl.CidHost, 5000, defaultPolicy()); !errors.Is(err, impl.ErrPortInUse) {
		t.Fatalf("binding the same port twice must fail with ErrPortInUse, got %v", err)
	}
	if got := len(g.Listeners()); got != 1 {
		t.Fatalf("listeners = %d, want 1", got)
	}
}

func TestConnectRequiresListener(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	if _, err := g.Connect(guestPeer(7), impl.ConnectRequest{Port: 6000}); !errors.Is(err, impl.ErrNoListener) {
		t.Fatalf("connecting to a free port must fail with ErrNoListener, got %v", err)
	}
	if _, err := g.Connect(guestPeer(7), impl.ConnectRequest{Port: 0}); !errors.Is(err, impl.ErrBadPort) {
		t.Fatalf("connecting to port 0 must fail with ErrBadPort, got %v", err)
	}
	if got := len(g.Listeners()); got != 1 {
		t.Fatalf("listeners after the rejected connects = %d, want 1", got)
	}
}

func TestConnectDeniesCidOutsideAllowList(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	if _, err := g.Connect(guestPeer(9), impl.ConnectRequest{Port: 5000}); !errors.Is(err, impl.ErrCidDenied) {
		t.Fatalf("a cid outside the allow list must fail with ErrCidDenied, got %v", err)
	}
	if _, err := g.Connect(guestPeer(8), impl.ConnectRequest{Port: 5000}); err != nil {
		t.Fatalf("an allowed cid must connect, got %v", err)
	}
}

func TestConnectRejectsSideCidMismatch(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	cases := []impl.PeerAddr{
		{Side: impl.SideGuest, CID: impl.CidHost, Port: 40000},
		{Side: impl.SideGuest, CID: impl.CidHypervisor, Port: 40000},
		{Side: impl.SideGuest, CID: impl.CidAny, Port: 40000},
		{Side: impl.SideHost, CID: 7, Port: 40000},
	}
	for i, peer := range cases {
		if _, err := g.Connect(peer, impl.ConnectRequest{Port: 5000}); !errors.Is(err, impl.ErrCidMismatch) {
			t.Fatalf("side/cid case %d must fail with ErrCidMismatch, got %v", i, err)
		}
	}
	if _, err := g.Connect(impl.PeerAddr{Side: "vm", CID: 7, Port: 40000}, impl.ConnectRequest{Port: 5000}); !errors.Is(err, impl.ErrBadSide) {
		t.Fatalf("an unknown side must fail with ErrBadSide, got %v", err)
	}
}

func TestConnectEnforcesEndpointLimit(t *testing.T) {
	cfg := vsockConfig()
	cfg.MaxConnsPerEndpoint = 2
	g := impl.NewVsockPeerGuard(cfg)
	mustListen(t, g, defaultPolicy())
	first := mustConnect(t, g, guestPeer(7))
	second := mustConnect(t, g, guestPeer(8))
	if first.ID == second.ID {
		t.Fatalf("connection ids must differ, both were %q", first.ID)
	}
	if first.Generation >= second.Generation {
		t.Fatalf("connection generations = %d then %d, want a rising counter", first.Generation, second.Generation)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.Connect(guestPeer(7), impl.ConnectRequest{Port: 5000}); !errors.Is(err, impl.ErrConnLimit) {
			t.Fatalf("a connection past the endpoint cap must fail with ErrConnLimit, got %v", err)
		}
	}
	if state, ok := g.ConnState(second.ID); !ok || state.State != impl.StateHandshake {
		t.Fatalf("second connection state=%+v ok=%v, want a live handshaking connection", state, ok)
	}
}

func TestAuthorizeRejectsBadCredentials(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	conn := mustConnect(t, g, guestPeer(7))
	if _, err := g.Authorize(conn.ID, peerCred("tenant-b", "tok-1")); !errors.Is(err, impl.ErrTenantMismatch) {
		t.Fatalf("a foreign tenant must fail with ErrTenantMismatch, got %v", err)
	}
	if _, err := g.Authorize(conn.ID, peerCred("tenant-a", "   ")); !errors.Is(err, impl.ErrBadCredential) {
		t.Fatalf("a blank token must fail with ErrBadCredential, got %v", err)
	}
	state, ok := g.ConnState(conn.ID)
	if !ok {
		t.Fatalf("the connection must survive the rejected handshakes")
	}
	if state.State != impl.StateHandshake || state.Authenticated {
		t.Fatalf("state=%q authenticated=%v, want handshaking and false", state.State, state.Authenticated)
	}
	if _, err := g.Authorize("vc-404", peerCred("tenant-a", "tok-1")); !errors.Is(err, impl.ErrConnNotFound) {
		t.Fatalf("authorizing an unknown connection must fail with ErrConnNotFound, got %v", err)
	}
}

func TestAuthorizeEstablishesAndRejectsRepeat(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	conn := mustConnect(t, g, guestPeer(7))
	decision := mustAuthorize(t, g, conn.ID)
	if !decision.Allowed {
		t.Fatalf("decision allowed = false, want true for matching credentials")
	}
	if decision.Tenant != "tenant-a" {
		t.Fatalf("decision tenant = %q, want tenant-a", decision.Tenant)
	}
	state, ok := g.ConnState(conn.ID)
	if !ok {
		t.Fatalf("the authorized connection must stay in the table")
	}
	if state.State != impl.StateEstablished {
		t.Fatalf("state = %q, want %q", state.State, impl.StateEstablished)
	}
	if !state.Authenticated {
		t.Fatalf("authenticated = false, want true")
	}
	if state.SendCredit != 64 || state.RecvCredit != 64 {
		t.Fatalf("credits = %d/%d, want the configured base 64/64", state.SendCredit, state.RecvCredit)
	}
	if _, err := g.Authorize(conn.ID, peerCred("tenant-a", "tok-1")); !errors.Is(err, impl.ErrAlreadyAuthorized) {
		t.Fatalf("a second handshake must fail with ErrAlreadyAuthorized, got %v", err)
	}
}

func TestCreditRequiresAuthorization(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	conn := mustConnect(t, g, guestPeer(7))
	if _, err := g.Credit(conn.ID, 1); !errors.Is(err, impl.ErrNotEstablished) {
		t.Fatalf("crediting a handshaking connection must fail with ErrNotEstablished, got %v", err)
	}
	state, ok := g.ConnState(conn.ID)
	if !ok {
		t.Fatalf("the connection must stay readable")
	}
	if state.SendCredit != 0 {
		t.Fatalf("send credit before the handshake = %d, want 0", state.SendCredit)
	}
	mustAuthorize(t, g, conn.ID)
	if _, err := g.Credit(conn.ID, 0); !errors.Is(err, impl.ErrBadCredit) {
		t.Fatalf("a zero credit consumption must fail with ErrBadCredit, got %v", err)
	}
}

func TestCreditRejectsOverspend(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	conn := mustConnect(t, g, guestPeer(7))
	mustAuthorize(t, g, conn.ID)
	if _, err := g.Credit(conn.ID, 65); !errors.Is(err, impl.ErrCreditExhausted) {
		t.Fatalf("spending past the send window must fail with ErrCreditExhausted, got %v", err)
	}
	state, ok := g.ConnState(conn.ID)
	if !ok {
		t.Fatalf("the connection must stay readable")
	}
	if state.SendCredit != 64 {
		t.Fatalf("send credit after the rejected spend = %d, want the untouched 64", state.SendCredit)
	}
	window, err := g.Credit(conn.ID, 64)
	if err != nil {
		t.Fatalf("spending the whole window: %v", err)
	}
	if window.Send != 0 {
		t.Fatalf("window send = %d, want 0", window.Send)
	}
	if window.Recv != 64 {
		t.Fatalf("window recv = %d, want the independent 64", window.Recv)
	}
	if _, err := g.Credit(conn.ID, 1); !errors.Is(err, impl.ErrCreditExhausted) {
		t.Fatalf("spending on an empty window must fail with ErrCreditExhausted, got %v", err)
	}
}

func TestGrantClampsToPolicyMax(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	conn := mustConnect(t, g, guestPeer(7))
	mustAuthorize(t, g, conn.ID)
	window, err := g.Grant(conn.ID, 100)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if window.Granted != 64 {
		t.Fatalf("granted credit = %d, want the 64 that still fit below the policy cap 128", window.Granted)
	}
	if !window.Clamped {
		t.Fatalf("clamped = false, want true when the grant hits the policy cap")
	}
	if window.Recv != 128 {
		t.Fatalf("receive window = %d, want the policy cap 128", window.Recv)
	}
	window, err = g.Grant(conn.ID, 10)
	if err != nil {
		t.Fatalf("grant at the cap: %v", err)
	}
	if window.Granted != 0 || !window.Clamped || window.Recv != 128 {
		t.Fatalf("grant at the cap = %+v, want 0 granted, clamped true and recv 128", window)
	}
}

func TestShutdownWriteBlocksSend(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	conn := mustConnect(t, g, guestPeer(7))
	mustAuthorize(t, g, conn.ID)
	if err := g.Shutdown(conn.ID, impl.ShutdownWrite); err != nil {
		t.Fatalf("shutdown write: %v", err)
	}
	state, ok := g.ConnState(conn.ID)
	if !ok {
		t.Fatalf("a half closed connection must stay readable")
	}
	if state.State != impl.StateHalfClosed {
		t.Fatalf("state = %q, want %q after a write shutdown", state.State, impl.StateHalfClosed)
	}
	if state.LocalShutdown != impl.ShutdownWrite {
		t.Fatalf("local shutdown = %q, want %q", state.LocalShutdown, impl.ShutdownWrite)
	}
	if _, err := g.Credit(conn.ID, 1); !errors.Is(err, impl.ErrShutdown) {
		t.Fatalf("sending after the write shutdown must fail with ErrShutdown, got %v", err)
	}
	if err := g.Shutdown(conn.ID, "half"); !errors.Is(err, impl.ErrBadShutdown) {
		t.Fatalf("an unknown shutdown mode must fail with ErrBadShutdown, got %v", err)
	}
	if err := g.Shutdown("vc-404", impl.ShutdownBoth); !errors.Is(err, impl.ErrConnNotFound) {
		t.Fatalf("shutting down an unknown connection must fail with ErrConnNotFound, got %v", err)
	}
}

func TestConcurrentConnectRespectsLimit(t *testing.T) {
	cfg := vsockConfig()
	cfg.MaxConnsPerEndpoint = 3
	g := impl.NewVsockPeerGuard(cfg)
	mustListen(t, g, defaultPolicy())
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners, limited := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := g.Connect(guestPeer(7), impl.ConnectRequest{Port: 5000, Nonce: fmt.Sprintf("n-%02d", i)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, impl.ErrConnLimit):
				limited++
			}
		}(i)
	}
	wg.Wait()
	if winners != 3 {
		t.Fatalf("concurrent connects produced %d winners, want the endpoint cap 3", winners)
	}
	if limited != 13 {
		t.Fatalf("concurrent connects produced %d limit errors, want 13", limited)
	}
}

func TestSnapshotRestoreKeepsCredits(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	listener := mustListen(t, g, defaultPolicy())
	conn := mustConnect(t, g, guestPeer(7))
	mustAuthorize(t, g, conn.ID)
	if _, err := g.Credit(conn.ID, 10); err != nil {
		t.Fatalf("credit: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewVsockPeerGuard(vsockConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Listeners()); got != 1 {
		t.Fatalf("restored listeners = %d, want 1", got)
	}
	state, ok := restored.ConnState(conn.ID)
	if !ok {
		t.Fatalf("restored guard lost the connection")
	}
	if state.SendCredit != 54 {
		t.Fatalf("restored send credit = %d, want the spent down 54", state.SendCredit)
	}
	if state.RecvCredit != 64 {
		t.Fatalf("restored receive credit = %d, want 64", state.RecvCredit)
	}
	if state.State != impl.StateEstablished || !state.Authenticated {
		t.Fatalf("restored state=%q authenticated=%v, want established and true", state.State, state.Authenticated)
	}
	if _, err := restored.Authorize(conn.ID, peerCred("tenant-a", "tok-1")); !errors.Is(err, impl.ErrAlreadyAuthorized) {
		t.Fatalf("the restored handshake state must be kept, got %v", err)
	}
	if got := restored.Listeners()[0].ID; got != listener.ID {
		t.Fatalf("restored listener id = %q, want %q", got, listener.ID)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewVsockPeerGuard(vsockConfig())
	mustListen(t, g, defaultPolicy())
	conn := mustConnect(t, g, guestPeer(7))
	for _, payload := range [][]byte{nil, []byte("{not-a-snapshot")} {
		if err := g.Restore(payload); !errors.Is(err, impl.ErrBadSnapshot) {
			t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
		}
	}
	if _, ok := g.ConnState(conn.ID); !ok {
		t.Fatalf("a rejected restore must not drop the live connection")
	}
	if got := len(g.Listeners()); got != 1 {
		t.Fatalf("listeners after the rejected restore = %d, want 1", got)
	}
}
