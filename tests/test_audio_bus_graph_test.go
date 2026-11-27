package task

import (
	"errors"
	"strings"
	"testing"

	impl "gsb/batch51-go-game-c-06"
)

func testLimits() impl.GraphLimits {
	return impl.GraphLimits{MaxBuses: 8, MaxSendsPerBus: 4, MaxBlockFrames: 128}
}

func addBus(t *testing.T, g *impl.AudioBusGraph, id string, level float32) {
	t.Helper()
	if err := g.AddBus(id, impl.BusConfig{Level: level}); err != nil {
		t.Fatalf("add bus %q: %v", id, err)
	}
}

func commitBlock(t *testing.T, g *impl.AudioBusGraph) {
	t.Helper()
	if _, err := g.Render(1); err != nil {
		t.Fatalf("commit block: %v", err)
	}
}

func TestTopologyEditsCommitAtBlockBoundary(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "music", 0.5)
	if got := g.Stats().Buses; got != 0 {
		t.Fatalf("committed buses before the first render = %d, want 0 (edits still staged)", got)
	}
	if got := g.Stats().PendingEdits; got != 1 {
		t.Fatalf("pending edits after one AddBus = %d, want 1", got)
	}
	if got := len(g.Buses()); got != 0 {
		t.Fatalf("committed bus list before the first render has %d entries, want 0", got)
	}
	out, err := g.Render(1)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("rendered %d samples, want 1", len(out))
	}
	if out[0] != 0.5 {
		t.Fatalf("rendered level = %v, want the bus source level 0.5", out[0])
	}
	if got := g.Stats().Buses; got != 1 {
		t.Fatalf("committed buses after the first render = %d, want 1", got)
	}
	if got := g.Stats().PendingEdits; got != 0 {
		t.Fatalf("pending edits after the commit = %d, want 0", got)
	}
	buses := g.Buses()
	if len(buses) != 1 || buses[0] != "music" {
		t.Fatalf("committed bus list = %v, want [music]", buses)
	}
	if got := g.Stats().RenderedBlocks; got != 1 {
		t.Fatalf("rendered blocks = %d, want 1", got)
	}
}

func TestAddBusValidatesIdentityAndLevel(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	if err := g.AddBus("   ", impl.BusConfig{}); !errors.Is(err, impl.ErrBlankBusID) {
		t.Fatalf("blank bus id must fail with ErrBlankBusID, got %v", err)
	}
	addBus(t, g, "lead", 0.2)
	if err := g.AddBus("lead", impl.BusConfig{}); !errors.Is(err, impl.ErrDuplicateBus) {
		t.Fatalf("duplicate bus id must fail with ErrDuplicateBus, got %v", err)
	}
	if err := g.AddBus("hot", impl.BusConfig{Level: 1.5}); !errors.Is(err, impl.ErrLevelRange) {
		t.Fatalf("source level above one must fail with ErrLevelRange, got %v", err)
	}
	if err := g.AddBus("loud", impl.BusConfig{GainDB: 20}); !errors.Is(err, impl.ErrGainRange) {
		t.Fatalf("initial gain above the window must fail with ErrGainRange, got %v", err)
	}
	commitBlock(t, g)
	if got := g.Stats().Buses; got != 1 {
		t.Fatalf("committed buses = %d, want only the accepted bus", got)
	}
	buses := g.Buses()
	if len(buses) != 1 || buses[0] != "lead" {
		t.Fatalf("committed bus list = %v, want [lead]", buses)
	}
}

func TestAddBusEnforcesBusLimit(t *testing.T) {
	g := impl.NewAudioBusGraph(impl.GraphLimits{MaxBuses: 2, MaxSendsPerBus: 4, MaxBlockFrames: 64})
	addBus(t, g, "a", 0)
	addBus(t, g, "b", 0)
	if err := g.AddBus("c", impl.BusConfig{}); !errors.Is(err, impl.ErrBusLimit) {
		t.Fatalf("third bus with MaxBuses 2 must fail with ErrBusLimit, got %v", err)
	}
	commitBlock(t, g)
	if got := g.Stats().Buses; got != 2 {
		t.Fatalf("committed buses = %d, want the cap 2", got)
	}
}

func TestConnectRejectsUnknownEndpointsAndSelfSend(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "a", 0)
	addBus(t, g, "b", 0)
	if err := g.Connect("ghost", "b", impl.SendConfig{}); !errors.Is(err, impl.ErrUnknownBus) {
		t.Fatalf("unknown source must fail with ErrUnknownBus, got %v", err)
	}
	if err := g.Connect("a", "ghost", impl.SendConfig{}); !errors.Is(err, impl.ErrUnknownBus) {
		t.Fatalf("unknown destination must fail with ErrUnknownBus, got %v", err)
	}
	if err := g.Connect("a", "a", impl.SendConfig{}); !errors.Is(err, impl.ErrSelfSend) {
		t.Fatalf("self send must fail with ErrSelfSend, got %v", err)
	}
	commitBlock(t, g)
	if got := g.Stats().Sends; got != 0 {
		t.Fatalf("committed sends after rejected connects = %d, want 0", got)
	}
}

func TestConnectRejectsCycleWithFullPath(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "a", 0)
	addBus(t, g, "b", 0)
	addBus(t, g, "c", 0)
	if err := g.Connect("a", "b", impl.SendConfig{}); err != nil {
		t.Fatalf("connect a to b: %v", err)
	}
	if err := g.Connect("b", "c", impl.SendConfig{}); err != nil {
		t.Fatalf("connect b to c: %v", err)
	}
	err := g.Connect("c", "a", impl.SendConfig{})
	if !errors.Is(err, impl.ErrCycle) {
		t.Fatalf("closing the loop must fail with ErrCycle, got %v", err)
	}
	if !strings.Contains(err.Error(), "a -> b -> c -> a") {
		t.Fatalf("cycle error text = %q, want the full loop a -> b -> c -> a", err.Error())
	}
	commitBlock(t, g)
	if got := g.Stats().Sends; got != 2 {
		t.Fatalf("committed sends = %d, want the two accepted edges", got)
	}
	if got := g.Stats().RejectedCycles; got != 1 {
		t.Fatalf("rejected cycle counter = %d, want 1", got)
	}
}

func TestConnectEnforcesSendLimitAndUniqueness(t *testing.T) {
	g := impl.NewAudioBusGraph(impl.GraphLimits{MaxBuses: 8, MaxSendsPerBus: 2, MaxBlockFrames: 64})
	addBus(t, g, "src", 0.1)
	addBus(t, g, "d1", 0)
	addBus(t, g, "d2", 0)
	addBus(t, g, "d3", 0)
	if err := g.Connect("src", "d1", impl.SendConfig{}); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := g.Connect("src", "d1", impl.SendConfig{}); !errors.Is(err, impl.ErrDuplicateSend) {
		t.Fatalf("duplicate send must fail with ErrDuplicateSend, got %v", err)
	}
	if err := g.Connect("src", "d2", impl.SendConfig{}); err != nil {
		t.Fatalf("second send: %v", err)
	}
	if err := g.Connect("src", "d3", impl.SendConfig{}); !errors.Is(err, impl.ErrSendLimit) {
		t.Fatalf("third send with MaxSendsPerBus 2 must fail with ErrSendLimit, got %v", err)
	}
	commitBlock(t, g)
	if got := g.Stats().Sends; got != 2 {
		t.Fatalf("committed sends = %d, want the two accepted edges", got)
	}
}

func TestRemoveBusDropsOnlyThatBus(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "keep", 0.1)
	addBus(t, g, "drop", 0.2)
	commitBlock(t, g)
	if err := g.RemoveBus("ghost"); !errors.Is(err, impl.ErrUnknownBus) {
		t.Fatalf("removing an unknown bus must fail with ErrUnknownBus, got %v", err)
	}
	if err := g.RemoveBus("drop"); err != nil {
		t.Fatalf("remove bus: %v", err)
	}
	commitBlock(t, g)
	buses := g.Buses()
	if len(buses) != 1 || buses[0] != "keep" {
		t.Fatalf("bus list after removal = %v, want [keep]", buses)
	}
	if got := g.Stats().Buses; got != 1 {
		t.Fatalf("committed bus count = %d, want 1", got)
	}
}

func TestDisconnectRemovesOnlyTheNamedSend(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "a", 0.1)
	addBus(t, g, "b", 0)
	addBus(t, g, "c", 0)
	if err := g.Connect("a", "b", impl.SendConfig{}); err != nil {
		t.Fatalf("connect a to b: %v", err)
	}
	if err := g.Connect("a", "c", impl.SendConfig{}); err != nil {
		t.Fatalf("connect a to c: %v", err)
	}
	commitBlock(t, g)
	if got := g.Stats().Sends; got != 2 {
		t.Fatalf("committed sends = %d, want 2", got)
	}
	if err := g.Disconnect("a", "b"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	commitBlock(t, g)
	if got := g.Stats().Sends; got != 1 {
		t.Fatalf("committed sends after one disconnect = %d, want 1", got)
	}
	if err := g.Disconnect("a", "b"); !errors.Is(err, impl.ErrUnknownSend) {
		t.Fatalf("disconnecting a missing send must fail with ErrUnknownSend, got %v", err)
	}
}

func TestSetGainRejectsOutOfRangeValues(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "a", 0.1)
	commitBlock(t, g)
	if err := g.SetGain("a", 12.5); !errors.Is(err, impl.ErrGainRange) {
		t.Fatalf("gain above the window must fail with ErrGainRange, got %v", err)
	}
	if err := g.SetGain("a", -60.5); !errors.Is(err, impl.ErrGainRange) {
		t.Fatalf("gain below the window must fail with ErrGainRange, got %v", err)
	}
	if err := g.SetGain("ghost", 0); !errors.Is(err, impl.ErrUnknownBus) {
		t.Fatalf("unknown bus must fail with ErrUnknownBus, got %v", err)
	}
}

func TestSetGainScalesTheBlockLevel(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "tone", 1.0)
	commitBlock(t, g)
	if err := g.SetGain("tone", -6.0206); err != nil {
		t.Fatalf("set gain: %v", err)
	}
	out, err := g.Render(4)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("rendered %d samples, want 4", len(out))
	}
	for i, v := range out {
		if v < 0.499 || v > 0.501 {
			t.Fatalf("sample %d = %v, want 0.5 for -6.0206 dB on a unit source", i, v)
		}
	}
}

func TestSetSidechainValidatesDepthAndKey(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "music", 0.4)
	addBus(t, g, "voice", 0.4)
	commitBlock(t, g)
	if err := g.SetSidechain("music", "voice", 1.2); !errors.Is(err, impl.ErrSidechainDepth) {
		t.Fatalf("depth above one must fail with ErrSidechainDepth, got %v", err)
	}
	if err := g.SetSidechain("music", "ghost", 0.5); !errors.Is(err, impl.ErrUnknownBus) {
		t.Fatalf("unknown sidechain key must fail with ErrUnknownBus, got %v", err)
	}
	if err := g.SetSidechain("ghost", "voice", 0.5); !errors.Is(err, impl.ErrUnknownBus) {
		t.Fatalf("unknown sidechain destination must fail with ErrUnknownBus, got %v", err)
	}
	if err := g.SetSidechain("music", "voice", 0.4); err != nil {
		t.Fatalf("valid sidechain: %v", err)
	}
	if got := g.Stats().Sidechains; got != 1 {
		t.Fatalf("sidechain count = %d, want 1", got)
	}
}

func TestBypassMutesBusAndRestores(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "tone", 0.25)
	commitBlock(t, g)
	if err := g.Bypass("tone", true); err != nil {
		t.Fatalf("bypass on: %v", err)
	}
	out, err := g.Render(3)
	if err != nil {
		t.Fatalf("render while bypassed: %v", err)
	}
	for i, v := range out {
		if v != 0 {
			t.Fatalf("sample %d = %v, want silence while the bus is bypassed", i, v)
		}
	}
	if err := g.Bypass("tone", false); err != nil {
		t.Fatalf("bypass off: %v", err)
	}
	out, err = g.Render(3)
	if err != nil {
		t.Fatalf("render after bypass is released: %v", err)
	}
	for i, v := range out {
		if v != 0.25 {
			t.Fatalf("sample %d = %v, want the original level 0.25", i, v)
		}
	}
	if err := g.Bypass("ghost", true); !errors.Is(err, impl.ErrUnknownBus) {
		t.Fatalf("bypassing an unknown bus must fail with ErrUnknownBus, got %v", err)
	}
}

func TestRenderRejectsFrameCountOutOfRange(t *testing.T) {
	g := impl.NewAudioBusGraph(impl.GraphLimits{MaxBuses: 4, MaxSendsPerBus: 2, MaxBlockFrames: 64})
	addBus(t, g, "a", 0.1)
	if _, err := g.Render(0); !errors.Is(err, impl.ErrFramesRange) {
		t.Fatalf("zero frames must fail with ErrFramesRange, got %v", err)
	}
	if _, err := g.Render(-8); !errors.Is(err, impl.ErrFramesRange) {
		t.Fatalf("negative frames must fail with ErrFramesRange, got %v", err)
	}
	if _, err := g.Render(65); !errors.Is(err, impl.ErrFramesRange) {
		t.Fatalf("frames above MaxBlockFrames must fail with ErrFramesRange, got %v", err)
	}
	if _, err := g.Render(64); err != nil {
		t.Fatalf("the exact cap must render, got %v", err)
	}
}

func TestRenderAppliesSendLatency(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "src", 0.5)
	addBus(t, g, "sink", 0)
	if err := g.Connect("src", "sink", impl.SendConfig{LatencyFrames: 3}); err != nil {
		t.Fatalf("connect with latency: %v", err)
	}
	out, err := g.Render(8)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []float32{0, 0, 0, 0.5, 0.5, 0.5, 0.5, 0.5}
	if len(out) != len(want) {
		t.Fatalf("rendered %d samples, want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("sample %d = %v, want the delayed source %v", i, out[i], want[i])
		}
	}
	lat, err := g.Latency("sink")
	if err != nil {
		t.Fatalf("latency: %v", err)
	}
	if lat != 3 {
		t.Fatalf("sink latency = %d, want 3", lat)
	}
	if _, err := g.Latency("ghost"); !errors.Is(err, impl.ErrUnknownBus) {
		t.Fatalf("latency of an unknown bus must fail with ErrUnknownBus, got %v", err)
	}
}

func TestSnapshotRestoreKeepsCommittedGraph(t *testing.T) {
	g := impl.NewAudioBusGraph(testLimits())
	addBus(t, g, "src", 0.5)
	addBus(t, g, "sink", 0)
	if err := g.Connect("src", "sink", impl.SendConfig{LatencyFrames: 4}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	commitBlock(t, g)
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewAudioBusGraph(testLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Stats().Buses; got != 2 {
		t.Fatalf("restored bus count = %d, want 2", got)
	}
	if got := restored.Stats().Sends; got != 1 {
		t.Fatalf("restored send count = %d, want 1", got)
	}
	lat, err := restored.Latency("sink")
	if err != nil {
		t.Fatalf("restored latency: %v", err)
	}
	if lat != 4 {
		t.Fatalf("restored sink latency = %d, want 4", lat)
	}
	out, err := restored.Render(6)
	if err != nil {
		t.Fatalf("render after restore: %v", err)
	}
	want := []float32{0, 0, 0, 0, 0.5, 0.5}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("restored sample %d = %v, want %v", i, out[i], want[i])
		}
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Stats().Buses; got != 2 {
		t.Fatalf("rejected restore must not touch live state, bus count = %d", got)
	}
}
