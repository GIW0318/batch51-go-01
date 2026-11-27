package task

import (
	"encoding/binary"
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-b-10"
)

func ipv6Header(next byte) []byte {
	b := make([]byte, 40)
	b[0] = 0x60
	b[6] = next
	copy(b[8:24], []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	copy(b[24:40], []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2})
	return b
}

func finish(b []byte) []byte {
	binary.BigEndian.PutUint16(b[4:6], uint16(len(b)-40))
	return b
}

func join(parts ...[]byte) []byte {
	out := make([]byte, 0, 64)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func extHeader(next byte, total int) []byte {
	b := make([]byte, total)
	b[0] = next
	b[1] = byte(total/8 - 1)
	return b
}

func routingHeader(next byte, total int, routingType byte, segments byte) []byte {
	b := extHeader(next, total)
	b[2] = routingType
	b[3] = segments
	return b
}

func fragmentHeader(next byte, offset int, more bool, id uint32) []byte {
	b := make([]byte, 8)
	b[0] = next
	word := uint16(offset) << 3
	if more {
		word |= 1
	}
	binary.BigEndian.PutUint16(b[2:4], word)
	binary.BigEndian.PutUint32(b[4:8], id)
	return b
}

func pad1() []byte {
	return []byte{0x00}
}

func padN(n int) []byte {
	b := make([]byte, 2+n)
	b[0] = 0x01
	b[1] = byte(n)
	return b
}

func rawOption(typ byte, dataLen int) []byte {
	b := make([]byte, 2+dataLen)
	b[0] = typ
	b[1] = byte(dataLen)
	return b
}

func hbhWith(options ...[]byte) []byte {
	hdr := extHeader(6, 8)
	area := join(options...)
	if len(area) > 6 {
		hdr = extHeader(6, 8*(1+(len(area)+1)/8))
	}
	copy(hdr[2:], area)
	return hdr
}

func basePolicy() impl.Policy {
	return impl.Policy{MaxHeaders: 8, MaxOptions: 16}
}

func newGuard() *impl.Ipv6ExtChainGuard {
	return impl.NewIpv6ExtChainGuard(impl.ChainPolicy{Default: basePolicy(), MaxIfaces: 8})
}

func TestParseUpperTCPNoExt(t *testing.T) {
	g := newGuard()
	pkt := finish(join(ipv6Header(6), []byte{1, 2, 3, 4}))
	chain, err := g.Parse(pkt)
	if err != nil {
		t.Fatalf("parse plain tcp packet: %v", err)
	}
	if chain.Version != 6 || chain.NextHeader != 6 {
		t.Fatalf("chain head = version %d next %d, want 6/6", chain.Version, chain.NextHeader)
	}
	if len(chain.Headers) != 0 {
		t.Fatalf("plain packet must have no extension headers, got %d", len(chain.Headers))
	}
	if chain.Upper != impl.UpperTCP || chain.UpperOffset != 40 {
		t.Fatalf("upper = %q at offset %d, want tcp at 40", chain.Upper, chain.UpperOffset)
	}
	if len(chain.Payload) != 4 || chain.Payload[0] != 1 || chain.TotalLen != 44 || chain.PayloadLen != 4 {
		t.Fatalf("payload = %v total %d payload len %d, want the four bytes after the fixed header", chain.Payload, chain.TotalLen, chain.PayloadLen)
	}
}

func TestParseHopByHopThenTCP(t *testing.T) {
	g := newGuard()
	hbh := extHeader(6, 8)
	copy(hbh[2:], join(pad1(), padN(3)))
	pkt := finish(join(ipv6Header(0), hbh, []byte{9, 8, 7, 6, 5, 4, 3, 2}))
	chain, err := g.Parse(pkt)
	if err != nil {
		t.Fatalf("parse hop by hop: %v", err)
	}
	if len(chain.Headers) != 1 {
		t.Fatalf("header count = %d, want 1", len(chain.Headers))
	}
	h := chain.Headers[0]
	if h.Type != impl.HeaderHopByHop || h.Offset != 40 || h.Length != 8 || h.Next != impl.UpperTCP {
		t.Fatalf("parsed header = %+v, want hop_by_hop at 40 with length 8 followed by tcp", h)
	}
	if len(h.Options) != 2 {
		t.Fatalf("option count = %d, want pad1 plus padN", len(h.Options))
	}
	if h.Options[0].Type != 0 || h.Options[0].Length != 1 {
		t.Fatalf("first option = %+v, want pad1", h.Options[0])
	}
	if h.Options[1].Type != 1 || h.Options[1].Length != 5 || h.Options[1].Critical {
		t.Fatalf("second option = %+v, want a five byte non critical padN", h.Options[1])
	}
	if chain.Upper != impl.UpperTCP || chain.UpperOffset != 48 || len(chain.Payload) != 8 {
		t.Fatalf("upper = %q at %d with %d payload bytes, want tcp at 48 with 8", chain.Upper, chain.UpperOffset, len(chain.Payload))
	}
}

func TestParseRejectsShortBadVersionAndTruncated(t *testing.T) {
	g := newGuard()
	if _, err := g.Parse(nil); !errors.Is(err, impl.ErrShortPacket) {
		t.Fatalf("nil packet must fail with ErrShortPacket, got %v", err)
	}
	if _, err := g.Parse(make([]byte, 39)); !errors.Is(err, impl.ErrShortPacket) {
		t.Fatalf("39 byte packet must fail with ErrShortPacket, got %v", err)
	}
	badVersion := finish(ipv6Header(6))
	badVersion[0] = 0x50
	if _, err := g.Parse(badVersion); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("version 5 packet must fail with ErrBadVersion, got %v", err)
	}
	claimsSixteen := finish(join(ipv6Header(0), extHeader(6, 8)))
	claimsSixteen[41] = 1
	if _, err := g.Parse(claimsSixteen); !errors.Is(err, impl.ErrTruncatedChain) {
		t.Fatalf("header claiming 16 bytes inside an 8 byte packet must fail with ErrTruncatedChain, got %v", err)
	}
	badOption := extHeader(6, 8)
	badOption[2] = 0x01
	badOption[3] = 200
	if _, err := g.Parse(finish(join(ipv6Header(0), badOption))); !errors.Is(err, impl.ErrTruncatedChain) {
		t.Fatalf("option overrunning its header must fail with ErrTruncatedChain, got %v", err)
	}
}

func TestParseOptionsPadAndUnknown(t *testing.T) {
	g := newGuard()
	hbh := hbhWith(pad1(), padN(9), rawOption(0x05, 0))
	pkt := finish(join(ipv6Header(0), hbh))
	chain, err := g.Parse(pkt)
	if err != nil {
		t.Fatalf("parse options: %v", err)
	}
	if len(chain.Headers) != 1 {
		t.Fatalf("header count = %d, want 1", len(chain.Headers))
	}
	opts := chain.Headers[0].Options
	if len(opts) != 3 {
		t.Fatalf("option count = %d, want 3: %+v", len(opts), opts)
	}
	if opts[0].Type != 0 || opts[0].Length != 1 {
		t.Fatalf("pad1 = %+v, want type 0 length 1", opts[0])
	}
	if opts[1].Type != 1 || opts[1].Length != 11 {
		t.Fatalf("padN = %+v, want type 1 length 11", opts[1])
	}
	if opts[2].Type != 5 || opts[2].Length != 2 || opts[2].Critical {
		t.Fatalf("unknown option = %+v, want a two byte non critical option", opts[2])
	}
}

func TestCheckRoutingTypes(t *testing.T) {
	g := newGuard()
	policy := basePolicy()
	policy.AllowRoutingTypes = []int{0, 4}
	build := func(routingType byte) impl.Chain {
		pkt := finish(join(ipv6Header(43), routingHeader(6, 8, routingType, 0)))
		chain, err := g.Parse(pkt)
		if err != nil {
			t.Fatalf("parse routing type %d: %v", routingType, err)
		}
		return chain
	}
	v, err := g.Check(build(0), policy)
	if err != nil {
		t.Fatalf("check routing type 0: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonRoutingType0 {
		t.Fatalf("routing type 0 verdict = %q/%q, want drop/routing type0", v.Action, v.Reason)
	}
	v, err = g.Check(build(2), policy)
	if err != nil {
		t.Fatalf("check routing type 2: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonRoutingNotAllowed {
		t.Fatalf("routing type 2 verdict = %q/%q, want drop/routing not allowed", v.Action, v.Reason)
	}
	v, err = g.Check(build(4), policy)
	if err != nil {
		t.Fatalf("check routing type 4: %v", err)
	}
	if v.Action != impl.ActionAccept || v.Reason != impl.ReasonAccept {
		t.Fatalf("routing type 4 verdict = %q/%q, want accept", v.Action, v.Reason)
	}
	if v.Headers != 1 {
		t.Fatalf("verdict header count = %d, want 1", v.Headers)
	}
}

func TestCheckUnknownCriticalOptionDropped(t *testing.T) {
	g := newGuard()
	policy := basePolicy()
	critical := hbhWith(rawOption(0x81, 0))
	chain, err := g.Parse(finish(join(ipv6Header(0), critical)))
	if err != nil {
		t.Fatalf("parse critical option: %v", err)
	}
	v, err := g.Check(chain, policy)
	if err != nil {
		t.Fatalf("check critical option: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonUnknownCritical {
		t.Fatalf("unknown critical option verdict = %q/%q, want drop/unknown critical", v.Action, v.Reason)
	}
	benign := hbhWith(rawOption(0x05, 0))
	chain, err = g.Parse(finish(join(ipv6Header(0), benign)))
	if err != nil {
		t.Fatalf("parse non critical option: %v", err)
	}
	v, err = g.Check(chain, policy)
	if err != nil {
		t.Fatalf("check non critical option: %v", err)
	}
	if v.Action != impl.ActionAccept {
		t.Fatalf("unknown non critical option verdict = %q/%q, want accept", v.Action, v.Reason)
	}
}

func TestCheckFragmentDuplicateRejected(t *testing.T) {
	g := newGuard()
	pkt := finish(join(ipv6Header(44), fragmentHeader(44, 0, true, 11), fragmentHeader(6, 0, false, 22)))
	chain, err := g.Parse(pkt)
	if err != nil {
		t.Fatalf("parse two fragment headers: %v", err)
	}
	if len(chain.Headers) != 2 {
		t.Fatalf("header count = %d, want 2", len(chain.Headers))
	}
	v, err := g.Check(chain, basePolicy())
	if err != nil {
		t.Fatalf("check duplicate fragments: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonDuplicateFragment {
		t.Fatalf("duplicate fragment verdict = %q/%q, want drop/duplicate fragment", v.Action, v.Reason)
	}
}

func TestCheckAtomicFragmentPolicy(t *testing.T) {
	g := newGuard()
	atomicPkt := finish(join(ipv6Header(44), fragmentHeader(6, 0, false, 7), []byte{1, 2, 3, 4}))
	chain, err := g.Parse(atomicPkt)
	if err != nil {
		t.Fatalf("parse atomic fragment: %v", err)
	}
	strict := basePolicy()
	v, err := g.Check(chain, strict)
	if err != nil {
		t.Fatalf("check atomic fragment: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonAtomicFragment {
		t.Fatalf("atomic fragment under a strict policy = %q/%q, want drop/atomic fragment", v.Action, v.Reason)
	}
	lenient := basePolicy()
	lenient.AllowAtomicFragments = true
	v, err = g.Check(chain, lenient)
	if err != nil {
		t.Fatalf("check allowed atomic fragment: %v", err)
	}
	if v.Action != impl.ActionAccept {
		t.Fatalf("allowed atomic fragment verdict = %q/%q, want accept", v.Action, v.Reason)
	}
	nonAtomic := finish(join(ipv6Header(44), fragmentHeader(6, 100, true, 9), []byte{1, 2, 3, 4}))
	chain, err = g.Parse(nonAtomic)
	if err != nil {
		t.Fatalf("parse non atomic fragment: %v", err)
	}
	v, err = g.Check(chain, strict)
	if err != nil {
		t.Fatalf("check non atomic fragment: %v", err)
	}
	if v.Action != impl.ActionAccept {
		t.Fatalf("non atomic fragment verdict = %q/%q, want accept without the atomic switch", v.Action, v.Reason)
	}
}

func TestCheckMaxOptionsLimit(t *testing.T) {
	g := newGuard()
	hbh := hbhWith(rawOption(0x05, 0), rawOption(0x05, 0), rawOption(0x05, 0))
	chain, err := g.Parse(finish(join(ipv6Header(0), hbh)))
	if err != nil {
		t.Fatalf("parse three options: %v", err)
	}
	if len(chain.Headers[0].Options) != 3 {
		t.Fatalf("option count = %d, want 3", len(chain.Headers[0].Options))
	}
	tight := basePolicy()
	tight.MaxOptions = 2
	v, err := g.Check(chain, tight)
	if err != nil {
		t.Fatalf("check tight option budget: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonTooManyOptions {
		t.Fatalf("three options against a budget of two = %q/%q, want drop/too many options", v.Action, v.Reason)
	}
	roomy := basePolicy()
	roomy.MaxOptions = 3
	v, err = g.Check(chain, roomy)
	if err != nil {
		t.Fatalf("check roomy option budget: %v", err)
	}
	if v.Action != impl.ActionAccept {
		t.Fatalf("three options against a budget of three = %q/%q, want accept", v.Action, v.Reason)
	}
}

func TestCheckJumboPayloadCrossCheck(t *testing.T) {
	g := newGuard()
	hbh := extHeader(6, 16)
	jumbo := []byte{0xC2, 0x04, 0, 0, 0, 24}
	copy(hbh[2:], join(jumbo, padN(6)))
	pkt := finish(join(ipv6Header(0), hbh, []byte{1, 2, 3, 4, 5, 6, 7, 8}))
	binary.BigEndian.PutUint16(pkt[4:6], 0)
	chain, err := g.Parse(pkt)
	if err != nil {
		t.Fatalf("parse jumbo packet: %v", err)
	}
	if !chain.Headers[0].HasJumbo || chain.Headers[0].JumboLength != 24 {
		t.Fatalf("jumbo option = has %v value %d, want true and 24", chain.Headers[0].HasJumbo, chain.Headers[0].JumboLength)
	}
	v, err := g.Check(chain, basePolicy())
	if err != nil {
		t.Fatalf("check jumbo packet: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonJumboForbidden {
		t.Fatalf("jumbo packet under a policy without jumbo = %q/%q, want drop/jumbo forbidden", v.Action, v.Reason)
	}
	allowed := basePolicy()
	allowed.AllowJumbo = true
	v, err = g.Check(chain, allowed)
	if err != nil {
		t.Fatalf("check allowed jumbo packet: %v", err)
	}
	if v.Action != impl.ActionAccept {
		t.Fatalf("matching jumbo length verdict = %q/%q, want accept", v.Action, v.Reason)
	}
	mismatched := finish(join(ipv6Header(0), hbh, []byte{1, 2, 3, 4, 5, 6, 7, 8}))
	binary.BigEndian.PutUint16(mismatched[4:6], 0)
	mismatched[47] = 23
	chain, err = g.Parse(mismatched)
	if err != nil {
		t.Fatalf("parse mismatched jumbo packet: %v", err)
	}
	v, err = g.Check(chain, allowed)
	if err != nil {
		t.Fatalf("check mismatched jumbo packet: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonJumboMismatch {
		t.Fatalf("jumbo length mismatch verdict = %q/%q, want drop/jumbo mismatch", v.Action, v.Reason)
	}
}

func TestCheckUnknownNextHeaderPolicy(t *testing.T) {
	g := newGuard()
	pkt := finish(join(ipv6Header(253), []byte{1, 2, 3, 4}))
	chain, err := g.Parse(pkt)
	if err != nil {
		t.Fatalf("parse unknown next header: %v", err)
	}
	if chain.Upper != impl.UpperUnknown {
		t.Fatalf("upper = %q, want unknown", chain.Upper)
	}
	v, err := g.Check(chain, basePolicy())
	if err != nil {
		t.Fatalf("check unknown next header: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonUnknownNextHeader {
		t.Fatalf("unknown next header under the default policy = %q/%q, want drop/unknown next header", v.Action, v.Reason)
	}
	allow := basePolicy()
	allow.AllowUnknown = true
	v, err = g.Check(chain, allow)
	if err != nil {
		t.Fatalf("check allowed unknown next header: %v", err)
	}
	if v.Action != impl.ActionAccept {
		t.Fatalf("unknown next header with the switch on = %q/%q, want accept", v.Action, v.Reason)
	}
}

func TestInspectUsesPerInterfacePolicy(t *testing.T) {
	g := newGuard()
	eth0 := basePolicy()
	eth0.AllowRoutingTypes = []int{4}
	if err := g.SetPolicy("eth0", eth0); err != nil {
		t.Fatalf("set eth0 policy: %v", err)
	}
	if err := g.SetDefault(basePolicy()); err != nil {
		t.Fatalf("set default policy: %v", err)
	}
	pkt := finish(join(ipv6Header(43), routingHeader(6, 8, 4, 0)))
	v, err := g.Inspect("eth0", pkt)
	if err != nil {
		t.Fatalf("inspect on eth0: %v", err)
	}
	if v.Action != impl.ActionAccept || v.Iface != "eth0" {
		t.Fatalf("eth0 verdict = %+v, want accept on the interface", v)
	}
	if got := g.Stats("eth0"); got.Accepted != 1 || got.Dropped != 0 {
		t.Fatalf("eth0 counters = %+v, want one accepted packet", got)
	}
	v, err = g.Inspect("eth1", pkt)
	if err != nil {
		t.Fatalf("inspect on eth1: %v", err)
	}
	if v.Action != impl.ActionDrop {
		t.Fatalf("eth1 verdict = %+v, want the fallback policy to drop", v)
	}
	if got := g.Stats("eth1"); got.Dropped != 1 || got.Accepted != 0 {
		t.Fatalf("eth1 counters = %+v, want one dropped packet", got)
	}
	if got := g.Stats("eth0"); got.Dropped != 0 {
		t.Fatalf("eth0 counters changed by another interface: %+v", got)
	}
	p, ok := g.Policy("eth0")
	if !ok || len(p.AllowRoutingTypes) != 1 || p.AllowRoutingTypes[0] != 4 {
		t.Fatalf("stored eth0 policy = %+v ok=%v, want routing type 4 allowed", p, ok)
	}
	if _, ok := g.Policy("eth9"); ok {
		t.Fatalf("an unset interface must not report a policy")
	}
	if err := g.SetPolicy("  ", basePolicy()); !errors.Is(err, impl.ErrBadIface) {
		t.Fatalf("blank interface must fail with ErrBadIface, got %v", err)
	}
}

func TestExplainReportsChainSteps(t *testing.T) {
	g := newGuard()
	pkt := finish(join(ipv6Header(43), routingHeader(6, 8, 0, 0), []byte{1, 2, 3, 4}))
	trace, err := g.Explain(pkt)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if trace.PacketLen != len(pkt) {
		t.Fatalf("trace packet length = %d, want %d", trace.PacketLen, len(pkt))
	}
	if trace.Verdict.Action != impl.ActionDrop || trace.Verdict.Reason != impl.ReasonRoutingType0 {
		t.Fatalf("trace verdict = %q/%q, want drop/routing type0", trace.Verdict.Action, trace.Verdict.Reason)
	}
	if len(trace.Steps) != 2 {
		t.Fatalf("trace steps = %d, want the routing header and the upper layer", len(trace.Steps))
	}
	if trace.Steps[0].Type != impl.HeaderRouting || trace.Steps[1].Type != impl.UpperTCP {
		t.Fatalf("trace steps = %+v, want routing followed by tcp", trace.Steps)
	}
	if trace.Chain.Upper != impl.UpperTCP || len(trace.Chain.Headers) != 1 {
		t.Fatalf("trace chain = %+v, want one routing header and a tcp upper layer", trace.Chain)
	}
}

func TestSnapshotRestoreAndCorrupt(t *testing.T) {
	g := newGuard()
	if err := g.SetDefault(basePolicy()); err != nil {
		t.Fatalf("set default: %v", err)
	}
	eth0 := basePolicy()
	eth0.AllowRoutingTypes = []int{4}
	if err := g.SetPolicy("eth0", eth0); err != nil {
		t.Fatalf("set eth0: %v", err)
	}
	allow := finish(join(ipv6Header(43), routingHeader(6, 8, 4, 0)))
	deny := finish(join(ipv6Header(43), routingHeader(6, 8, 2, 0)))
	if _, err := g.Inspect("eth0", allow); err != nil {
		t.Fatalf("inspect allow: %v", err)
	}
	if _, err := g.Inspect("eth0", deny); err != nil {
		t.Fatalf("inspect deny: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newGuard()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := restored.Stats("eth0")
	if got.Accepted != 1 || got.Dropped != 1 || got.ParseErrors != 0 {
		t.Fatalf("restored counters = %+v, want one accepted and one dropped", got)
	}
	p, ok := restored.Policy("eth0")
	if !ok || len(p.AllowRoutingTypes) != 1 || p.AllowRoutingTypes[0] != 4 {
		t.Fatalf("restored policy = %+v ok=%v, want routing type 4", p, ok)
	}
	if _, err := restored.Inspect("eth0", allow); err != nil {
		t.Fatalf("inspect after restore: %v", err)
	}
	if got := restored.Stats("eth0").Accepted; got != 2 {
		t.Fatalf("accepted counter after restore = %d, want 2", got)
	}
	if err := restored.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Stats("eth0").Accepted; got != 2 {
		t.Fatalf("rejected restore changed the counters: %d", got)
	}
}

func TestConcurrentInspectCounts(t *testing.T) {
	g := newGuard()
	pkt := finish(join(ipv6Header(6), []byte{1, 2, 3, 4}))
	var wg sync.WaitGroup
	var mu sync.Mutex
	bad := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := g.Inspect("eth2", pkt)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || v.Action != impl.ActionAccept {
				bad++
			}
		}()
	}
	wg.Wait()
	if bad != 0 {
		t.Fatalf("%d concurrent inspections failed or dropped a valid packet", bad)
	}
	got := g.Stats("eth2")
	if got.Accepted != 32 {
		t.Fatalf("accepted counter = %d, want 32", got.Accepted)
	}
	if got.Accepted+got.Dropped+got.ParseErrors != 32 {
		t.Fatalf("counters = %+v, want exactly 32 accounted packets", got)
	}
}
