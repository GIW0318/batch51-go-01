package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-b-07"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testConfig(clock *fakeClock) impl.GateConfig {
	return impl.GateConfig{
		MaxFlows:           64,
		MaxFlowsPerSource:  8,
		MaxPacketsPerFlow:  32,
		TCPTimeout:         time.Minute,
		UDPTimeout:         10 * time.Second,
		ICMPTimeout:        5 * time.Second,
		ReEvaluateOnReload: false,
		Now:                clock.Now,
	}
}

func allowTCPRule() impl.Rule {
	return impl.Rule{ID: "rule-tcp", Action: impl.ActionAllow, Proto: impl.ProtoTCP, SrcCIDR: "10.0.0.0/8", DstPort: 443}
}

func allowUDPRule() impl.Rule {
	return impl.Rule{ID: "rule-udp", Action: impl.ActionAllow, Proto: impl.ProtoUDP, SrcCIDR: "10.0.0.0/8", DstPort: 53}
}

func newGate(t *testing.T, cfg impl.GateConfig, rules ...impl.Rule) *impl.ConntrackPolicyGate {
	t.Helper()
	g := impl.NewConntrackPolicyGate(cfg)
	if err := g.Reload(rules); err != nil {
		t.Fatalf("load rules: %v", err)
	}
	return g
}

func synPacket() impl.Packet {
	return impl.Packet{Proto: impl.ProtoTCP, SrcIP: "10.1.2.3", SrcPort: 51000, DstIP: "192.0.2.10", DstPort: 443, Flags: []string{impl.FlagSYN}, Length: 60}
}

func udpPacket(src, dst string, sport, dport int) impl.Packet {
	return impl.Packet{Proto: impl.ProtoUDP, SrcIP: src, SrcPort: sport, DstIP: dst, DstPort: dport, Length: 80}
}

func keyOf(p impl.Packet) impl.FlowKey {
	return impl.FlowKey{Proto: p.Proto, SrcIP: p.SrcIP, SrcPort: p.SrcPort, DstIP: p.DstIP, DstPort: p.DstPort}
}

func reversed(p impl.Packet) impl.Packet {
	r := p
	r.SrcIP, r.DstIP = p.DstIP, p.SrcIP
	r.SrcPort, r.DstPort = p.DstPort, p.SrcPort
	return r
}

func admit(t *testing.T, g *impl.ConntrackPolicyGate, p impl.Packet) impl.Verdict {
	t.Helper()
	v, err := g.Admit(p)
	if err != nil {
		t.Fatalf("admit %+v: %v", p, err)
	}
	return v
}

func TestAdmitSYNMatchesRuleCreatesFlow(t *testing.T) {
	clock := newFakeClock()
	g := newGate(t, testConfig(clock), allowTCPRule())
	syn := synPacket()
	v := admit(t, g, syn)
	if v.Action != impl.ActionAllow || v.State != impl.StateNew || v.Reason != impl.ReasonRuleAllow {
		t.Fatalf("verdict = %q/%q/%q, want allow/new/rule allow", v.Action, v.State, v.Reason)
	}
	if v.Gen != 1 {
		t.Fatalf("verdict generation = %d, want 1", v.Gen)
	}
	if v.Key != keyOf(syn) {
		t.Fatalf("verdict key = %+v, want the packet five tuple", v.Key)
	}
	st, ok := g.Lookup(keyOf(syn))
	if !ok {
		t.Fatalf("an admitted SYN must create a table entry")
	}
	if st.State != impl.StateNew || st.Packets != 1 || st.Bytes != syn.Length || st.Gen != 1 {
		t.Fatalf("entry = %+v, want new with one 60 byte packet on generation 1", st)
	}
	if got := g.FlowCount(); got != 1 {
		t.Fatalf("flow count = %d, want 1", got)
	}
	stats := g.Stats()
	if stats.Admitted != 1 || stats.Dropped != 0 || stats.Flows != 1 {
		t.Fatalf("stats = %+v, want one admitted packet and one flow", stats)
	}
}

func TestAdmitHandshakePacketWithoutBareSYNDropped(t *testing.T) {
	clock := newFakeClock()
	g := newGate(t, testConfig(clock), allowTCPRule())
	for _, flags := range [][]string{{impl.FlagACK}, {impl.FlagSYN, impl.FlagACK}, {impl.FlagFIN}} {
		p := synPacket()
		p.Flags = flags
		v := admit(t, g, p)
		if v.Action != impl.ActionDrop || v.Reason != impl.ReasonNoSYN {
			t.Fatalf("flags %v verdict = %q/%q, want drop/no syn", flags, v.Action, v.Reason)
		}
	}
	if got := g.FlowCount(); got != 0 {
		t.Fatalf("flow count after rejected first packets = %d, want 0", got)
	}
	if got := g.Stats().Dropped; got != 3 {
		t.Fatalf("drop counter = %d, want 3", got)
	}
}

func TestAdmitDefaultDenyAndExplicitDeny(t *testing.T) {
	clock := newFakeClock()
	denyUDP := impl.Rule{ID: "rule-deny-udp", Action: impl.ActionDeny, Proto: impl.ProtoUDP}
	g := newGate(t, testConfig(clock), allowTCPRule(), denyUDP)
	otherPort := synPacket()
	otherPort.DstPort = 8443
	v := admit(t, g, otherPort)
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonNoRule {
		t.Fatalf("packet without a matching rule = %q/%q, want drop/no rule", v.Action, v.Reason)
	}
	v = admit(t, g, udpPacket("10.1.2.3", "192.0.2.53", 40000, 53))
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonRuleDeny {
		t.Fatalf("explicit deny verdict = %q/%q, want drop/rule deny", v.Action, v.Reason)
	}
	if got := g.FlowCount(); got != 0 {
		t.Fatalf("denied packets must not create entries, flow count = %d", got)
	}
}

func TestHandshakeTransitionsToEstablished(t *testing.T) {
	clock := newFakeClock()
	g := newGate(t, testConfig(clock), allowTCPRule())
	syn := synPacket()
	admit(t, g, syn)
	reply := reversed(syn)
	reply.Flags = []string{impl.FlagSYN, impl.FlagACK}
	reply.Length = 60
	v := admit(t, g, reply)
	if v.Action != impl.ActionAllow || v.State != impl.StateEstablished || v.Reason != impl.ReasonHandshake {
		t.Fatalf("syn-ack verdict = %q/%q/%q, want allow/established/handshake", v.Action, v.State, v.Reason)
	}
	ack := syn
	ack.Flags = []string{impl.FlagACK}
	ack.Length = 40
	v = admit(t, g, ack)
	if v.State != impl.StateEstablished || v.Reason != impl.ReasonHandshake {
		t.Fatalf("completing ack verdict = %q/%q, want established/handshake", v.State, v.Reason)
	}
	data := syn
	data.Flags = []string{impl.FlagACK}
	data.Length = 120
	v = admit(t, g, data)
	if v.Action != impl.ActionAllow || v.Reason != impl.ReasonEstablished {
		t.Fatalf("data verdict = %q/%q, want allow/established", v.Action, v.Reason)
	}
	st, ok := g.Lookup(keyOf(syn))
	if !ok {
		t.Fatalf("established flow disappeared")
	}
	if st.State != impl.StateEstablished || st.Packets != 4 {
		t.Fatalf("entry = %+v, want established with 4 packets", st)
	}
	if st.Bytes != 280 {
		t.Fatalf("entry bytes = %d, want 60+60+40+120 = 280", st.Bytes)
	}
}

func TestACKBeforeSynAckDropped(t *testing.T) {
	clock := newFakeClock()
	g := newGate(t, testConfig(clock), allowTCPRule())
	syn := synPacket()
	admit(t, g, syn)
	ack := syn
	ack.Flags = []string{impl.FlagACK}
	v := admit(t, g, ack)
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonBadState {
		t.Fatalf("ack before syn-ack = %q/%q, want drop/bad state", v.Action, v.Reason)
	}
	st, ok := g.Lookup(keyOf(syn))
	if !ok || st.State != impl.StateNew || st.Packets != 1 {
		t.Fatalf("entry after the rejected ack = %+v ok=%v, want an untouched new entry", st, ok)
	}
	reply := reversed(syn)
	reply.Flags = []string{impl.FlagSYN, impl.FlagACK}
	v = admit(t, g, reply)
	if v.State != impl.StateEstablished {
		t.Fatalf("proper syn-ack after the rejection = %q, want established", v.State)
	}
}

func TestFINClosesAndExpireRemoves(t *testing.T) {
	clock := newFakeClock()
	g := newGate(t, testConfig(clock), allowTCPRule())
	syn := synPacket()
	admit(t, g, syn)
	reply := reversed(syn)
	reply.Flags = []string{impl.FlagSYN, impl.FlagACK}
	admit(t, g, reply)
	fin := syn
	fin.Flags = []string{impl.FlagFIN}
	v := admit(t, g, fin)
	if v.Action != impl.ActionAllow || v.State != impl.StateClosed || v.Reason != impl.ReasonClosed {
		t.Fatalf("fin verdict = %q/%q/%q, want allow/closed/closed", v.Action, v.State, v.Reason)
	}
	clock.Advance(61 * time.Second)
	if got := g.Expire(clock.Now()); got != 1 {
		t.Fatalf("expire returned %d, want 1", got)
	}
	if got := g.FlowCount(); got != 0 {
		t.Fatalf("flow count after expiry = %d, want 0", got)
	}
	if got := g.Stats().Expired; got != 1 {
		t.Fatalf("expired counter = %d, want 1", got)
	}
	if got := g.Expire(clock.Now()); got != 0 {
		t.Fatalf("second expire returned %d, want 0", got)
	}
}

func TestUDPStatesAndPerProtocolExpiry(t *testing.T) {
	clock := newFakeClock()
	g := newGate(t, testConfig(clock), allowTCPRule(), allowUDPRule())
	first := udpPacket("10.3.0.5", "192.0.2.53", 40000, 53)
	v := admit(t, g, first)
	if v.Action != impl.ActionAllow || v.State != impl.StateNew || v.Reason != impl.ReasonRuleAllow {
		t.Fatalf("first udp packet = %q/%q/%q, want allow/new/rule allow", v.Action, v.State, v.Reason)
	}
	v = admit(t, g, reversed(first))
	if v.State != impl.StateEstablished || v.Reason != impl.ReasonEstablished {
		t.Fatalf("udp reply = %q/%q, want established/established", v.State, v.Reason)
	}
	syn := synPacket()
	admit(t, g, syn)
	reply := reversed(syn)
	reply.Flags = []string{impl.FlagSYN, impl.FlagACK}
	admit(t, g, reply)
	clock.Advance(11 * time.Second)
	if got := g.Expire(clock.Now()); got != 1 {
		t.Fatalf("expire returned %d, want only the udp flow to age out", got)
	}
	if _, ok := g.Lookup(keyOf(first)); ok {
		t.Fatalf("the udp flow must expire after its shorter timeout")
	}
	if _, ok := g.Lookup(keyOf(syn)); !ok {
		t.Fatalf("the tcp flow must survive the udp timeout")
	}
	if got := g.FlowCount(); got != 1 {
		t.Fatalf("flow count = %d, want the surviving tcp flow", got)
	}
}

func TestTableFullWithoutEvictableDrops(t *testing.T) {
	clock := newFakeClock()
	cfg := testConfig(clock)
	cfg.MaxFlows = 1
	g := newGate(t, cfg, allowTCPRule(), allowUDPRule())
	syn := synPacket()
	admit(t, g, syn)
	reply := reversed(syn)
	reply.Flags = []string{impl.FlagSYN, impl.FlagACK}
	admit(t, g, reply)
	v := admit(t, g, udpPacket("10.4.0.6", "192.0.2.53", 41000, 53))
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonTableFull {
		t.Fatalf("packet on a full all-established table = %q/%q, want drop/table full", v.Action, v.Reason)
	}
	if got := g.FlowCount(); got != 1 {
		t.Fatalf("flow count = %d, want the established flow only", got)
	}
}

func TestEvictsLeastRecentlyActiveNonEstablished(t *testing.T) {
	clock := newFakeClock()
	cfg := testConfig(clock)
	cfg.MaxFlows = 2
	g := newGate(t, cfg, allowTCPRule(), allowUDPRule())
	idle := udpPacket("10.6.0.1", "192.0.2.53", 42000, 53)
	admit(t, g, idle)
	syn := synPacket()
	admit(t, g, syn)
	reply := reversed(syn)
	reply.Flags = []string{impl.FlagSYN, impl.FlagACK}
	admit(t, g, reply)
	fresh := udpPacket("10.6.0.2", "192.0.2.53", 43000, 53)
	v := admit(t, g, fresh)
	if v.Action != impl.ActionAllow {
		t.Fatalf("new flow on a fillable table = %q, want allow", v.Action)
	}
	if _, ok := g.Lookup(keyOf(idle)); ok {
		t.Fatalf("the idle non-established flow must be evicted first")
	}
	if _, ok := g.Lookup(keyOf(fresh)); !ok {
		t.Fatalf("the freshly admitted flow must stay")
	}
	if _, ok := g.Lookup(keyOf(syn)); !ok {
		t.Fatalf("the established flow must not be evicted")
	}
	if got := g.Stats().Evicted; got != 1 {
		t.Fatalf("eviction counter = %d, want 1", got)
	}
}

func TestPerSourceFlowLimit(t *testing.T) {
	clock := newFakeClock()
	cfg := testConfig(clock)
	cfg.MaxFlowsPerSource = 1
	g := newGate(t, cfg, allowUDPRule())
	admit(t, g, udpPacket("10.9.9.9", "192.0.2.53", 50000, 53))
	v := admit(t, g, udpPacket("10.9.9.9", "192.0.2.53", 50001, 53))
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonSourceLimit {
		t.Fatalf("second flow from one source = %q/%q, want drop/source limit", v.Action, v.Reason)
	}
	if got := g.FlowCount(); got != 1 {
		t.Fatalf("flow count = %d, want 1", got)
	}
}

func TestPacketQuotaPerFlow(t *testing.T) {
	clock := newFakeClock()
	cfg := testConfig(clock)
	cfg.MaxPacketsPerFlow = 2
	g := newGate(t, cfg, allowUDPRule())
	first := udpPacket("10.8.0.1", "192.0.2.53", 45000, 53)
	admit(t, g, first)
	admit(t, g, reversed(first))
	v := admit(t, g, first)
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonFlowQuota {
		t.Fatalf("third packet of a two packet flow = %q/%q, want drop/flow quota", v.Action, v.Reason)
	}
	st, ok := g.Lookup(keyOf(first))
	if !ok || st.Packets != 2 {
		t.Fatalf("entry after the quota drop = %+v ok=%v, want 2 counted packets", st, ok)
	}
}

func TestReloadGenerationAndBadRules(t *testing.T) {
	clock := newFakeClock()
	g := newGate(t, testConfig(clock), allowTCPRule())
	admit(t, g, synPacket())
	if err := g.Reload([]impl.Rule{allowUDPRule()}); err != nil {
		t.Fatalf("reload with a valid rule set: %v", err)
	}
	v := admit(t, g, udpPacket("10.7.0.1", "192.0.2.53", 44000, 53))
	if v.Gen != 2 {
		t.Fatalf("flow admitted after reload has generation %d, want 2", v.Gen)
	}
	bad := impl.Rule{ID: "rule-bad", Action: impl.ActionAllow, Proto: impl.ProtoTCP, SrcCIDR: "10.0.0.0/33"}
	if err := g.Reload([]impl.Rule{bad}); !errors.Is(err, impl.ErrBadRule) {
		t.Fatalf("invalid cidr must fail with ErrBadRule, got %v", err)
	}
	rules := g.Rules()
	if len(rules) != 1 || rules[0].ID != "rule-udp" {
		t.Fatalf("rules after the rejected reload = %+v, want the previous generation", rules)
	}
	v = admit(t, g, udpPacket("10.7.0.2", "192.0.2.53", 44001, 53))
	if v.Gen != 2 {
		t.Fatalf("generation after the rejected reload = %d, want 2", v.Gen)
	}
}

func TestDeleteAndLookupUnknown(t *testing.T) {
	clock := newFakeClock()
	g := newGate(t, testConfig(clock), allowTCPRule())
	syn := synPacket()
	admit(t, g, syn)
	if _, ok := g.Lookup(impl.FlowKey{Proto: impl.ProtoTCP, SrcIP: "10.9.9.9", SrcPort: 1, DstIP: "192.0.2.10", DstPort: 443}); ok {
		t.Fatalf("an unknown five tuple must not resolve")
	}
	if !g.Delete(keyOf(syn)) {
		t.Fatalf("deleting a live entry must report true")
	}
	if got := g.FlowCount(); got != 0 {
		t.Fatalf("flow count after delete = %d, want 0", got)
	}
	if g.Delete(keyOf(syn)) {
		t.Fatalf("deleting a removed entry must report false")
	}
}

func TestSnapshotRestoreAndCorrupt(t *testing.T) {
	clock := newFakeClock()
	cfg := testConfig(clock)
	g := newGate(t, cfg, allowTCPRule(), allowUDPRule())
	syn := synPacket()
	admit(t, g, syn)
	reply := reversed(syn)
	reply.Flags = []string{impl.FlagSYN, impl.FlagACK}
	admit(t, g, reply)
	admit(t, g, udpPacket("10.6.5.4", "192.0.2.53", 46000, 53))
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewConntrackPolicyGate(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.FlowCount(); got != 2 {
		t.Fatalf("restored flow count = %d, want 2", got)
	}
	st, ok := restored.Lookup(keyOf(syn))
	if !ok || st.State != impl.StateEstablished || st.Packets != 2 {
		t.Fatalf("restored tcp entry = %+v ok=%v, want established with 2 packets", st, ok)
	}
	if got := len(restored.Rules()); got != 2 {
		t.Fatalf("restored rule count = %d, want 2", got)
	}
	v := admit(t, restored, udpPacket("10.6.5.5", "192.0.2.53", 46001, 53))
	if v.Gen != 1 {
		t.Fatalf("generation after restore = %d, want 1", v.Gen)
	}
	if err := restored.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.FlowCount(); got != 3 {
		t.Fatalf("a rejected restore must keep live state, flow count = %d", got)
	}
	if _, ok := restored.Lookup(keyOf(syn)); !ok {
		t.Fatalf("a rejected restore must not drop the live entries")
	}
}

func TestConcurrentAdmitDistinctFlows(t *testing.T) {
	clock := newFakeClock()
	cfg := testConfig(clock)
	cfg.MaxFlowsPerSource = 1
	g := newGate(t, cfg, allowTCPRule())
	var wg sync.WaitGroup
	var mu sync.Mutex
	bad := 0
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := synPacket()
			p.SrcIP = fmt.Sprintf("10.5.0.%d", i+1)
			v, err := g.Admit(p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || v.Action != impl.ActionAllow {
				bad++
			}
		}(i)
	}
	wg.Wait()
	if bad != 0 {
		t.Fatalf("%d concurrent first packets were rejected or failed", bad)
	}
	if got := g.FlowCount(); got != 24 {
		t.Fatalf("flow count = %d, want 24", got)
	}
	if got := g.Stats().Admitted; got != 24 {
		t.Fatalf("admitted counter = %d, want 24", got)
	}
}
