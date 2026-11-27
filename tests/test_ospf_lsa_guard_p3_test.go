package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-b-20"
)

func ospfConfig() impl.AreaConfig {
	return impl.AreaConfig{RouterID: "10.0.0.1", Areas: []string{"0.0.0.0"}, MaxAge: 3600}
}

func ospfBaseTime() time.Time {
	return time.Date(2026, 3, 9, 8, 0, 0, 0, time.UTC)
}

func ifaceConfig(priority uint8) impl.IfaceConfig {
	return impl.IfaceConfig{Area: "0.0.0.0", Priority: priority}
}

func routerLSA(advertising string, linkStateID string, seq uint32, checksum uint32, body string) impl.LSA {
	return impl.LSA{
		Type:              1,
		LinkStateID:       linkStateID,
		AdvertisingRouter: advertising,
		Sequence:          seq,
		Age:               0,
		Checksum:          checksum,
		Body:              body,
	}
}

func mustAge(t *testing.T, g *impl.OspfLsaGuardP3, now time.Time) []impl.LSAEvent {
	t.Helper()
	events, err := g.Age(now)
	if err != nil {
		t.Fatalf("age at %v failed: %v", now, err)
	}
	return events
}

func mustRecv(t *testing.T, g *impl.OspfLsaGuardP3, area string, lsa impl.LSA) impl.LSAStatus {
	t.Helper()
	status, err := g.RecvLSA(area, lsa)
	if err != nil {
		t.Fatalf("recv lsa %s failed: %v", lsa.LinkStateID, err)
	}
	return status
}

func mustHelloFull(t *testing.T, g *impl.OspfLsaGuardP3, iface, routerID string, priority uint8, dead uint32) {
	t.Helper()
	pkt := impl.HelloPacket{RouterID: routerID, Priority: priority, DeadInterval: dead, Neighbors: []string{"10.0.0.1"}}
	if _, err := g.Hello(iface, pkt); err != nil {
		t.Fatalf("hello from %s failed: %v", routerID, err)
	}
}

func hasLSAAEvent(events []impl.LSAEvent, kind, router, key string) bool {
	for _, ev := range events {
		if ev.Kind == kind && ev.Router == router && ev.Key == key {
			return true
		}
	}
	return false
}

func TestAddInterfaceValidatesIdentity(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	if err := g.AddInterface("  ", ifaceConfig(1)); !errors.Is(err, impl.ErrBlankInterface) {
		t.Fatalf("blank interface = %v, want ErrBlankInterface", err)
	}
	badArea := impl.IfaceConfig{Area: "0.0.0.9", Priority: 1}
	if err := g.AddInterface("eth0", badArea); !errors.Is(err, impl.ErrAreaNotFound) {
		t.Fatalf("unconfigured area = %v, want ErrAreaNotFound", err)
	}
	noRouter := impl.NewOspfLsaGuardP3(impl.AreaConfig{RouterID: "  ", Areas: []string{"0.0.0.0"}, MaxAge: 3600})
	if err := noRouter.AddInterface("eth0", ifaceConfig(1)); !errors.Is(err, impl.ErrBlankRouterID) {
		t.Fatalf("blank router id = %v, want ErrBlankRouterID", err)
	}
	if err := g.AddInterface("eth0", ifaceConfig(10)); err != nil {
		t.Fatalf("first add failed: %v", err)
	}
	if err := g.AddInterface("eth0", ifaceConfig(20)); !errors.Is(err, impl.ErrInterfaceExists) {
		t.Fatalf("duplicate interface = %v, want ErrInterfaceExists", err)
	}
}

func TestHelloTransitionsToFull(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	if err := g.AddInterface("eth0", ifaceConfig(100)); err != nil {
		t.Fatalf("add interface failed: %v", err)
	}
	t0 := ospfBaseTime()
	mustAge(t, g, t0)
	init := impl.HelloPacket{RouterID: "10.0.0.2", Priority: 50, DeadInterval: 40}
	state, err := g.Hello("eth0", init)
	if err != nil {
		t.Fatalf("hello failed: %v", err)
	}
	if state.State != impl.NeighborInit {
		t.Fatalf("state of a new neighbour = %q, want %q", state.State, impl.NeighborInit)
	}
	if state.RouterID != "10.0.0.2" || state.DeadInSec != 40 {
		t.Fatalf("neighbour view = %+v, want router 10.0.0.2 with 40s left", state)
	}
	full := impl.HelloPacket{RouterID: "10.0.0.2", Priority: 50, DeadInterval: 40, Neighbors: []string{"10.0.0.1"}}
	state, err = g.Hello("eth0", full)
	if err != nil {
		t.Fatalf("second hello failed: %v", err)
	}
	if state.State != impl.NeighborFull {
		t.Fatalf("state after seeing ourselves listed = %q, want %q", state.State, impl.NeighborFull)
	}
	neighbors := g.Neighbors("eth0")
	if len(neighbors) != 1 || neighbors[0].RouterID != "10.0.0.2" || neighbors[0].State != impl.NeighborFull {
		t.Fatalf("neighbours = %+v, want one full 10.0.0.2", neighbors)
	}
}

func TestHelloRejectsBadPackets(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	if err := g.AddInterface("eth0", ifaceConfig(10)); err != nil {
		t.Fatalf("add interface failed: %v", err)
	}
	mustAge(t, g, ospfBaseTime())
	if _, err := g.Hello("eth9", impl.HelloPacket{RouterID: "10.0.0.2", DeadInterval: 40}); !errors.Is(err, impl.ErrInterfaceNotFound) {
		t.Fatalf("hello on an unknown interface = %v, want ErrInterfaceNotFound", err)
	}
	if _, err := g.Hello("eth0", impl.HelloPacket{RouterID: "  ", DeadInterval: 40}); !errors.Is(err, impl.ErrBadHello) {
		t.Fatalf("hello without router id = %v, want ErrBadHello", err)
	}
	if _, err := g.Hello("eth0", impl.HelloPacket{RouterID: "10.0.0.2", DeadInterval: 0}); !errors.Is(err, impl.ErrBadHello) {
		t.Fatalf("hello without dead interval = %v, want ErrBadHello", err)
	}
	if neighbors := g.Neighbors("eth0"); len(neighbors) != 0 {
		t.Fatalf("rejected hello created neighbours: %+v", neighbors)
	}
}

func TestHelloTimeoutDownsNeighbor(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	if err := g.AddInterface("eth0", ifaceConfig(10)); err != nil {
		t.Fatalf("add interface failed: %v", err)
	}
	t0 := ospfBaseTime()
	mustAge(t, g, t0)
	mustHelloFull(t, g, "eth0", "10.0.0.2", 50, 40)
	early := mustAge(t, g, t0.Add(39*time.Second))
	if hasLSAAEvent(early, impl.EventNeighborDown, "10.0.0.2", "") {
		t.Fatalf("neighbour declared dead before the dead interval: %+v", early)
	}
	if neighbors := g.Neighbors("eth0"); len(neighbors) != 1 {
		t.Fatalf("neighbours before the dead interval = %+v, want one", neighbors)
	}
	late := mustAge(t, g, t0.Add(41*time.Second))
	if !hasLSAAEvent(late, impl.EventNeighborDown, "10.0.0.2", "") {
		t.Fatalf("events after the dead interval = %+v, want a neighbor_down for 10.0.0.2", late)
	}
	if neighbors := g.Neighbors("eth0"); len(neighbors) != 0 {
		t.Fatalf("timed out neighbour is still listed: %+v", neighbors)
	}
}

func TestRecvLSAInstallsNewer(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	mustAge(t, g, ospfBaseTime())
	if status := mustRecv(t, g, "0.0.0.0", routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence, 100, "body-a")); status != impl.StatusInstalled {
		t.Fatalf("first lsa status = %q, want %q", status, impl.StatusInstalled)
	}
	db := g.LSDB("0.0.0.0")
	if len(db) != 1 {
		t.Fatalf("lsdb size = %d, want 1", len(db))
	}
	if db[0].Sequence != impl.InitialSequence || db[0].Body != "body-a" {
		t.Fatalf("installed lsa = %+v, want sequence %#x body body-a", db[0], impl.InitialSequence)
	}
	newer := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence+1, 100, "body-b")
	if status := mustRecv(t, g, "0.0.0.0", newer); status != impl.StatusNewer {
		t.Fatalf("newer lsa status = %q, want %q", status, impl.StatusNewer)
	}
	db = g.LSDB("0.0.0.0")
	if len(db) != 1 || db[0].Body != "body-b" {
		t.Fatalf("lsdb after the newer instance = %+v, want one entry with body-b", db)
	}
}

func TestRecvLSADuplicateNotReinstalled(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	mustAge(t, g, ospfBaseTime())
	lsa := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence+1, 100, "body-a")
	mustRecv(t, g, "0.0.0.0", lsa)
	same := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence+1, 100, "body-tampered")
	if status := mustRecv(t, g, "0.0.0.0", same); status != impl.StatusDuplicate {
		t.Fatalf("duplicate lsa status = %q, want %q", status, impl.StatusDuplicate)
	}
	db := g.LSDB("0.0.0.0")
	if len(db) != 1 || db[0].Body != "body-a" {
		t.Fatalf("duplicate overwrote the database entry: %+v", db)
	}
}

func TestRecvLSAOlderIgnored(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	mustAge(t, g, ospfBaseTime())
	mustRecv(t, g, "0.0.0.0", routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence+3, 100, "body-new"))
	older := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence+2, 900, "body-old")
	if status := mustRecv(t, g, "0.0.0.0", older); status != impl.StatusOlder {
		t.Fatalf("older lsa status = %q, want %q", status, impl.StatusOlder)
	}
	db := g.LSDB("0.0.0.0")
	if len(db) != 1 || db[0].Body != "body-new" {
		t.Fatalf("older instance replaced the database entry: %+v", db)
	}
}

func TestRecvLSASameSeqChecksumTieBreak(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	mustAge(t, g, ospfBaseTime())
	mustRecv(t, g, "0.0.0.0", routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence+2, 100, "body-low"))
	higher := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence+2, 200, "body-high")
	if status := mustRecv(t, g, "0.0.0.0", higher); status != impl.StatusNewer {
		t.Fatalf("higher checksum status = %q, want %q", status, impl.StatusNewer)
	}
	if db := g.LSDB("0.0.0.0"); len(db) != 1 || db[0].Body != "body-high" {
		t.Fatalf("higher checksum did not win: %+v", db)
	}
	lower := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence+2, 150, "body-middle")
	if status := mustRecv(t, g, "0.0.0.0", lower); status != impl.StatusOlder {
		t.Fatalf("lower checksum status = %q, want %q", status, impl.StatusOlder)
	}
	if db := g.LSDB("0.0.0.0"); len(db) != 1 || db[0].Body != "body-high" {
		t.Fatalf("lower checksum replaced the entry: %+v", db)
	}
}

func TestRecvLSARejectsBadTypeAndSequence(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	mustAge(t, g, ospfBaseTime())
	badType := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence, 100, "body")
	badType.Type = 9
	if _, err := g.RecvLSA("0.0.0.0", badType); !errors.Is(err, impl.ErrBadLSAType) {
		t.Fatalf("type 9 = %v, want ErrBadLSAType", err)
	}
	zeroSeq := routerLSA("10.0.0.7", "10.0.0.7", 0, 100, "body")
	if _, err := g.RecvLSA("0.0.0.0", zeroSeq); !errors.Is(err, impl.ErrBadSequence) {
		t.Fatalf("sequence zero = %v, want ErrBadSequence", err)
	}
	reserved := routerLSA("10.0.0.7", "10.0.0.7", impl.ReservedSequence, 100, "body")
	if _, err := g.RecvLSA("0.0.0.0", reserved); !errors.Is(err, impl.ErrBadSequence) {
		t.Fatalf("reserved sequence = %v, want ErrBadSequence", err)
	}
	if db := g.LSDB("0.0.0.0"); len(db) != 0 {
		t.Fatalf("rejected lsa reached the database: %+v", db)
	}
}

func TestAgeAgesInPlaceAndFlushes(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	t0 := ospfBaseTime()
	mustAge(t, g, t0)
	young := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence, 100, "young")
	young.Age = 100
	mustRecv(t, g, "0.0.0.0", young)
	old := routerLSA("10.0.0.8", "10.0.0.8", impl.InitialSequence, 100, "old")
	old.Age = 3550
	mustRecv(t, g, "0.0.0.0", old)
	events := mustAge(t, g, t0.Add(50*time.Second))
	if !hasLSAAEvent(events, impl.EventLSAMaxAge, "10.0.0.8", "1|10.0.0.8|10.0.0.8") {
		t.Fatalf("events = %+v, want a max age event for the older instance", events)
	}
	db := g.LSDB("0.0.0.0")
	if len(db) != 1 {
		t.Fatalf("lsdb after aging = %+v, want only the young entry", db)
	}
	if db[0].Age != 150 {
		t.Fatalf("aged instance = %d seconds, want 150", db[0].Age)
	}
	spf := g.SPFInput("0.0.0.0")
	if len(spf) != 1 || spf[0].AdvertisingRouter != "10.0.0.7" {
		t.Fatalf("spf input = %+v, want only the young instance", spf)
	}
}

func TestFloodReturnsAreaInterfaces(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	if err := g.AddInterface("eth2", ifaceConfig(10)); err != nil {
		t.Fatalf("add eth2 failed: %v", err)
	}
	if err := g.AddInterface("eth1", ifaceConfig(10)); err != nil {
		t.Fatalf("add eth1 failed: %v", err)
	}
	t0 := ospfBaseTime()
	mustAge(t, g, t0)
	mustHelloFull(t, g, "eth2", "10.0.0.2", 10, 40)
	mustHelloFull(t, g, "eth1", "10.0.0.3", 10, 40)
	lsa := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence, 100, "body")
	mustRecv(t, g, "0.0.0.0", lsa)
	ifaces, err := g.Flood("0.0.0.0", lsa)
	if err != nil {
		t.Fatalf("flood failed: %v", err)
	}
	if len(ifaces) != 2 || ifaces[0] != "eth1" || ifaces[1] != "eth2" {
		t.Fatalf("flood interfaces = %v, want [eth1 eth2]", ifaces)
	}
	if _, err := g.Flood("0.0.0.9", lsa); !errors.Is(err, impl.ErrAreaNotFound) {
		t.Fatalf("flood in an unknown area = %v, want ErrAreaNotFound", err)
	}
	missing := routerLSA("10.0.0.8", "10.0.0.8", impl.InitialSequence, 100, "missing")
	if _, err := g.Flood("0.0.0.0", missing); !errors.Is(err, impl.ErrLSAUnknown) {
		t.Fatalf("flood of an uninstalled lsa = %v, want ErrLSAUnknown", err)
	}
}

func TestElectDRByPriority(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	if err := g.AddInterface("eth0", ifaceConfig(100)); err != nil {
		t.Fatalf("add interface failed: %v", err)
	}
	t0 := ospfBaseTime()
	mustAge(t, g, t0)
	mustHelloFull(t, g, "eth0", "10.0.0.2", 50, 40)
	mustHelloFull(t, g, "eth0", "10.0.0.3", 200, 40)
	mustHelloFull(t, g, "eth0", "10.0.0.9", 0, 40)
	result, err := g.ElectDR("0.0.0.0", "eth0")
	if err != nil {
		t.Fatalf("elect dr failed: %v", err)
	}
	if result.DR != "10.0.0.3" {
		t.Fatalf("dr = %q, want the highest priority neighbour 10.0.0.3", result.DR)
	}
	if result.BDR != "10.0.0.1" {
		t.Fatalf("bdr = %q, want the local router 10.0.0.1", result.BDR)
	}
}

func TestNeighborDownRemovesItsLSAs(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	if err := g.AddInterface("eth0", ifaceConfig(10)); err != nil {
		t.Fatalf("add interface failed: %v", err)
	}
	t0 := ospfBaseTime()
	mustAge(t, g, t0)
	mustHelloFull(t, g, "eth0", "10.0.0.9", 10, 40)
	mustRecv(t, g, "0.0.0.0", routerLSA("10.0.0.9", "10.0.0.9", impl.InitialSequence, 100, "from-neighbour"))
	mustRecv(t, g, "0.0.0.0", routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence, 100, "from-elsewhere"))
	events := mustAge(t, g, t0.Add(41*time.Second))
	if !hasLSAAEvent(events, impl.EventNeighborDown, "10.0.0.9", "") {
		t.Fatalf("events = %+v, want a neighbor_down for 10.0.0.9", events)
	}
	db := g.LSDB("0.0.0.0")
	if len(db) != 1 || db[0].AdvertisingRouter != "10.0.0.7" {
		t.Fatalf("lsdb after the neighbour went down = %+v, want only the 10.0.0.7 instance", db)
	}
}

func TestSnapshotRestoreKeepsLSDBAndClock(t *testing.T) {
	cfg := ospfConfig()
	g := impl.NewOspfLsaGuardP3(cfg)
	if err := g.AddInterface("eth0", ifaceConfig(10)); err != nil {
		t.Fatalf("add interface failed: %v", err)
	}
	t0 := ospfBaseTime()
	mustAge(t, g, t0)
	mustHelloFull(t, g, "eth0", "10.0.0.2", 10, 120)
	young := routerLSA("10.0.0.7", "10.0.0.7", impl.InitialSequence, 100, "young")
	young.Age = 100
	mustRecv(t, g, "0.0.0.0", young)
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	restored := impl.NewOspfLsaGuardP3(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	ifacesRestored := restored.Neighbors("eth0")
	if len(ifacesRestored) != 1 || ifacesRestored[0].RouterID != "10.0.0.2" {
		t.Fatalf("neighbours after restore = %+v, want 10.0.0.2", ifacesRestored)
	}
	mustAge(t, restored, t0.Add(60*time.Second))
	db := restored.LSDB("0.0.0.0")
	if len(db) != 1 || db[0].Age != 160 {
		t.Fatalf("lsdb after restore and aging = %+v, want age 160", db)
	}
	events := mustAge(t, restored, t0.Add(121*time.Second))
	if !hasLSAAEvent(events, impl.EventNeighborDown, "10.0.0.2", "") {
		t.Fatalf("events after the dead interval = %+v, want a neighbor_down for 10.0.0.2", events)
	}
}

func TestConcurrentRecvLSASingleWinner(t *testing.T) {
	g := impl.NewOspfLsaGuardP3(ospfConfig())
	mustAge(t, g, ospfBaseTime())
	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seq := impl.InitialSequence + uint32(i) + 4
			g.RecvLSA("0.0.0.0", routerLSA("10.0.0.7", "10.0.0.7", seq, 100, "body"))
		}(i)
	}
	wg.Wait()
	db := g.LSDB("0.0.0.0")
	if len(db) != 1 {
		t.Fatalf("lsdb size after concurrent updates = %d, want 1", len(db))
	}
	if db[0].Sequence != impl.InitialSequence+uint32(workers)+3 {
		t.Fatalf("winning sequence = %#x, want the highest instance %#x", db[0].Sequence, impl.InitialSequence+uint32(workers)+3)
	}
}
