package task

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-30"
)

func flowKey() impl.FlowKey {
	return impl.FlowKey{SrcIP: "10.0.0.1", DstIP: "10.0.0.2", SrcPort: 40000, DstPort: 443, Proto: 6}
}

func tcpSegment(seq uint32, data string, at int64) impl.Segment {
	return impl.Segment{Key: flowKey(), Seq: seq, Data: []byte(data), At: at}
}

func basePolicy() impl.Policy {
	return impl.Policy{IdleTimeout: 100, MaxBuffer: 64, WindowSize: 64, Linger: 5, MaxFlows: 8}
}

func TestFeedInOrderDeliversStream(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	ev, err := r.Feed(tcpSegment(100, "abc", 1000))
	if err != nil {
		t.Fatalf("feed first segment: %v", err)
	}
	if string(ev.Delivered) != "abc" {
		t.Fatalf("delivered = %q, want abc", string(ev.Delivered))
	}
	if ev.Buffered != 0 || ev.Gap {
		t.Fatalf("event buffered=%d gap=%v, want 0 and false", ev.Buffered, ev.Gap)
	}
	state, ok := r.Flow(flowKey())
	if !ok {
		t.Fatalf("flow must exist after the first segment")
	}
	if state.NextSeq != 103 || state.Delivered != 3 || state.Buffered != 0 {
		t.Fatalf("state = %+v, want next 103 and 3 delivered bytes", state)
	}
	ev, err = r.Feed(tcpSegment(103, "def", 1001))
	if err != nil {
		t.Fatalf("feed contiguous segment: %v", err)
	}
	if string(ev.Delivered) != "def" {
		t.Fatalf("delivered = %q, want def", string(ev.Delivered))
	}
	state, _ = r.Flow(flowKey())
	if state.NextSeq != 106 || state.Delivered != 6 {
		t.Fatalf("state = %+v, want next 106 and 6 delivered bytes", state)
	}
}

func TestFeedOutOfOrderBuffersUntilGapFilled(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	if _, err := r.Feed(tcpSegment(100, "a", 1000)); err != nil {
		t.Fatalf("feed first byte: %v", err)
	}
	ev, err := r.Feed(tcpSegment(102, "c", 1001))
	if err != nil {
		t.Fatalf("feed out of order byte: %v", err)
	}
	if len(ev.Delivered) != 0 {
		t.Fatalf("delivered = %q before the gap is filled, want nothing", string(ev.Delivered))
	}
	if ev.Buffered != 1 || !ev.Gap {
		t.Fatalf("event buffered=%d gap=%v, want 1 and true", ev.Buffered, ev.Gap)
	}
	ev, err = r.Feed(tcpSegment(101, "b", 1002))
	if err != nil {
		t.Fatalf("feed the missing byte: %v", err)
	}
	if string(ev.Delivered) != "bc" {
		t.Fatalf("delivered = %q, want both buffered bytes released in order", string(ev.Delivered))
	}
	if ev.Buffered != 0 || ev.Gap {
		t.Fatalf("event buffered=%d gap=%v after the gap closed, want 0 and false", ev.Buffered, ev.Gap)
	}
	stream, err := r.Flush(flowKey())
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if string(stream) != "abc" {
		t.Fatalf("stream = %q, want abc", string(stream))
	}
}

func TestFeedOverlapDeliversBytesOnce(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	if _, err := r.Feed(tcpSegment(100, "abc", 1000)); err != nil {
		t.Fatalf("feed first segment: %v", err)
	}
	ev, err := r.Feed(tcpSegment(101, "bcde", 1001))
	if err != nil {
		t.Fatalf("feed overlapping segment: %v", err)
	}
	if string(ev.Delivered) != "de" {
		t.Fatalf("delivered = %q, want only the new bytes de", string(ev.Delivered))
	}
	if ev.Duplicate != 2 {
		t.Fatalf("duplicate bytes = %d, want 2 trimmed bytes", ev.Duplicate)
	}
	stream, err := r.Flush(flowKey())
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if string(stream) != "abcde" {
		t.Fatalf("stream = %q, want each byte exactly once", string(stream))
	}
	if got := r.Stats().Duplicates; got != 2 {
		t.Fatalf("duplicate counter = %d, want 2", got)
	}
}

func TestFeedGapHoldsDelivery(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	if _, err := r.Feed(tcpSegment(100, "ab", 1000)); err != nil {
		t.Fatalf("feed prefix: %v", err)
	}
	ev, err := r.Feed(tcpSegment(105, "f", 1001))
	if err != nil {
		t.Fatalf("feed after the gap: %v", err)
	}
	if len(ev.Delivered) != 0 {
		t.Fatalf("bytes after a gap must stay buffered, delivered %q", string(ev.Delivered))
	}
	if !ev.Gap || ev.Buffered != 1 {
		t.Fatalf("event gap=%v buffered=%d, want true and 1", ev.Gap, ev.Buffered)
	}
	state, _ := r.Flow(flowKey())
	if state.NextSeq != 102 || state.Buffered != 1 {
		t.Fatalf("state = %+v, want next 102 with one buffered byte", state)
	}
}

func TestFlushReturnsDeliveredStream(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	if _, err := r.Feed(tcpSegment(100, "ab", 1000)); err != nil {
		t.Fatalf("feed prefix: %v", err)
	}
	if _, err := r.Feed(tcpSegment(105, "f", 1001)); err != nil {
		t.Fatalf("feed after the gap: %v", err)
	}
	stream, err := r.Flush(flowKey())
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if string(stream) != "ab" {
		t.Fatalf("flush = %q, want the contiguous prefix ab", string(stream))
	}
	if _, err := r.Flush(impl.FlowKey{SrcIP: "192.0.2.1", DstIP: "192.0.2.2", SrcPort: 1, DstPort: 2, Proto: 6}); !errors.Is(err, impl.ErrUnknownFlow) {
		t.Fatalf("flush of an unknown flow must fail with ErrUnknownFlow, got %v", err)
	}
}

func TestCloseFinishesFlow(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	fin := tcpSegment(100, "hello", 1000)
	fin.Fin = true
	if _, err := r.Feed(fin); err != nil {
		t.Fatalf("feed fin segment: %v", err)
	}
	if err := r.Close(flowKey(), impl.ReasonFin); err != nil {
		t.Fatalf("close: %v", err)
	}
	state, ok := r.Flow(flowKey())
	if !ok {
		t.Fatalf("closed flow must stay readable")
	}
	if !state.Closed || state.Reason != impl.ReasonFin {
		t.Fatalf("state closed=%v reason=%q, want true and fin", state.Closed, state.Reason)
	}
	if !state.Fin {
		t.Fatalf("flow must remember the observed fin flag")
	}
	if got := len(r.Active()); got != 0 {
		t.Fatalf("active flows = %d, want 0 after close", got)
	}
	stream, err := r.Flush(flowKey())
	if err != nil {
		t.Fatalf("flush closed flow: %v", err)
	}
	if string(stream) != "hello" {
		t.Fatalf("stream = %q, want hello", string(stream))
	}
}

func TestExpireClosesIdleFlow(t *testing.T) {
	policy := basePolicy()
	policy.IdleTimeout = 100
	r := impl.NewPcapFlowReassembler(policy)
	if _, err := r.Feed(tcpSegment(100, "a", 1000)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if got := r.Expire(1050); len(got) != 0 {
		t.Fatalf("expire before the timeout returned %+v", got)
	}
	expired := r.Expire(1101)
	if len(expired) != 1 || expired[0] != flowKey() {
		t.Fatalf("expired = %+v, want the idle flow", expired)
	}
	state, _ := r.Flow(flowKey())
	if !state.Closed || state.Reason != impl.ReasonIdle {
		t.Fatalf("state closed=%v reason=%q, want true and idle", state.Closed, state.Reason)
	}
	if got := len(r.Active()); got != 0 {
		t.Fatalf("active flows = %d, want 0 after expiry", got)
	}
	if got := r.Stats().Expired; got != 1 {
		t.Fatalf("expire counter = %d, want 1", got)
	}
}

func TestBufferLimitTruncatesAndMarks(t *testing.T) {
	policy := basePolicy()
	policy.MaxBuffer = 4
	r := impl.NewPcapFlowReassembler(policy)
	if _, err := r.Feed(tcpSegment(100, "a", 1000)); err != nil {
		t.Fatalf("feed prefix: %v", err)
	}
	if _, err := r.Feed(tcpSegment(105, "XXXX", 1001)); err != nil {
		t.Fatalf("feed buffered bytes: %v", err)
	}
	ev, err := r.Feed(tcpSegment(109, "YYYY", 1002))
	if err != nil {
		t.Fatalf("feed over the buffer limit: %v", err)
	}
	if !ev.Truncated {
		t.Fatalf("event must be marked truncated when the buffer cap is hit")
	}
	state, _ := r.Flow(flowKey())
	if state.Buffered != 4 || !state.Truncated {
		t.Fatalf("state buffered=%d truncated=%v, want 4 and true", state.Buffered, state.Truncated)
	}
	if got := len(r.Active()); got != 1 {
		t.Fatalf("active flows = %d, want 1 while the flow stays open", got)
	}
}

func TestSequenceWraparoundStaysContiguous(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	if _, err := r.Feed(tcpSegment(4294967294, "abc", 1000)); err != nil {
		t.Fatalf("feed across the wraparound: %v", err)
	}
	state, _ := r.Flow(flowKey())
	if state.NextSeq != 1 {
		t.Fatalf("next sequence after the wrap = %d, want 1", state.NextSeq)
	}
	ev, err := r.Feed(tcpSegment(1, "d", 1001))
	if err != nil {
		t.Fatalf("feed after the wraparound: %v", err)
	}
	if string(ev.Delivered) != "d" {
		t.Fatalf("delivered = %q, want d", string(ev.Delivered))
	}
	stream, err := r.Flush(flowKey())
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if string(stream) != "abcd" {
		t.Fatalf("stream = %q, want abcd across the wrap", string(stream))
	}
}

func TestSequenceOutOfWindowRejected(t *testing.T) {
	policy := basePolicy()
	policy.WindowSize = 4
	r := impl.NewPcapFlowReassembler(policy)
	if _, err := r.Feed(tcpSegment(100, "abcde", 1000)); err != nil {
		t.Fatalf("feed prefix: %v", err)
	}
	if _, err := r.Feed(tcpSegment(95, "zz", 1001)); !errors.Is(err, impl.ErrSeqOutOfWindow) {
		t.Fatalf("stale segment outside the window must fail with ErrSeqOutOfWindow, got %v", err)
	}
	if got := r.Stats().Rejected; got != 1 {
		t.Fatalf("rejected counter = %d, want 1", got)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	unknown := impl.FlowKey{SrcIP: "192.0.2.9", DstIP: "192.0.2.10", SrcPort: 9, DstPort: 10, Proto: 6}
	if err := r.Close(unknown, impl.ReasonFin); !errors.Is(err, impl.ErrUnknownFlow) {
		t.Fatalf("closing an unknown flow must fail with ErrUnknownFlow, got %v", err)
	}
	if _, err := r.Feed(tcpSegment(100, "x", 1000)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if err := r.Close(flowKey(), impl.ReasonFin); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := r.Close(flowKey(), impl.ReasonIdle); err != nil {
		t.Fatalf("second close must be idempotent, got %v", err)
	}
	state, _ := r.Flow(flowKey())
	if !state.Closed || state.Reason != impl.ReasonFin {
		t.Fatalf("state closed=%v reason=%q, want the first close reason", state.Closed, state.Reason)
	}
}

func TestFeedRejectsBadSegment(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	if _, err := r.Feed(impl.Segment{}); !errors.Is(err, impl.ErrBadFlow) {
		t.Fatalf("zero flow key must fail with ErrBadFlow, got %v", err)
	}
	empty := impl.Segment{Key: flowKey(), Seq: 1, At: 1000}
	if _, err := r.Feed(empty); !errors.Is(err, impl.ErrEmptySegment) {
		t.Fatalf("segment without data or fin must fail with ErrEmptySegment, got %v", err)
	}
	if got := len(r.Active()); got != 0 {
		t.Fatalf("rejected segments created %d flows, want 0", got)
	}
}

func TestSnapshotRestoreKeepsFlowState(t *testing.T) {
	policy := basePolicy()
	r := impl.NewPcapFlowReassembler(policy)
	if _, err := r.Feed(tcpSegment(100, "ab", 1000)); err != nil {
		t.Fatalf("feed prefix: %v", err)
	}
	if _, err := r.Feed(tcpSegment(105, "f", 1001)); err != nil {
		t.Fatalf("feed buffered byte: %v", err)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewPcapFlowReassembler(policy)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	state, ok := restored.Flow(flowKey())
	if !ok {
		t.Fatalf("restored reassembler lost the flow")
	}
	if state.NextSeq != 102 || state.Buffered != 1 || state.Delivered != 2 {
		t.Fatalf("restored state = %+v, want next 102 with one buffered byte and two delivered", state)
	}
	stream, err := restored.Flush(flowKey())
	if err != nil {
		t.Fatalf("flush restored flow: %v", err)
	}
	if string(stream) != "ab" {
		t.Fatalf("restored stream = %q, want ab", string(stream))
	}
	ev, err := restored.Feed(tcpSegment(102, "XYZ", 1002))
	if err != nil {
		t.Fatalf("feed after restore: %v", err)
	}
	if string(ev.Delivered) != "XYZf" {
		t.Fatalf("delivered after restore = %q, want XYZf", string(ev.Delivered))
	}
	if err := restored.Restore([]byte("{broken-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := restored.Flow(flowKey()); !ok {
		t.Fatalf("rejected restore dropped the live flow")
	}
}

func TestConcurrentFeedDeliversEachByteOnce(t *testing.T) {
	policy := basePolicy()
	policy.MaxBuffer = 2048
	policy.WindowSize = 4096
	policy.IdleTimeout = 100000
	r := impl.NewPcapFlowReassembler(policy)
	if _, err := r.Feed(tcpSegment(1000, "A", 1)); err != nil {
		t.Fatalf("feed anchor byte: %v", err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			data := bytes.Repeat([]byte{byte(g + 1)}, 100)
			seq := uint32(1001 + g*100)
			if _, err := r.Feed(tcpSegment(seq, string(data), 2)); err != nil {
				t.Errorf("concurrent feed for range %d: %v", g, err)
			}
		}(g)
	}
	wg.Wait()
	stream, err := r.Flush(flowKey())
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(stream) != 400 {
		t.Fatalf("stream length = %d, want 400 bytes", len(stream))
	}
	if stream[0] != 'A' || stream[1] != 1 || stream[100] != 1 || stream[101] != 2 || stream[399] != 4 {
		t.Fatalf("stream content is wrong at the chunk boundaries")
	}
	if got := r.Stats().Segments; got != 5 {
		t.Fatalf("segment counter = %d, want 5", got)
	}
	if got := r.Stats().Duplicates; got != 0 {
		t.Fatalf("duplicate counter = %d, want 0 for disjoint ranges", got)
	}
}

func TestStatsCountsSegmentsAndDuplicates(t *testing.T) {
	r := impl.NewPcapFlowReassembler(basePolicy())
	for _, step := range []struct {
		seq  uint32
		data string
	}{{100, "a"}, {101, "b"}, {102, "c"}} {
		if _, err := r.Feed(tcpSegment(step.seq, step.data, 1000)); err != nil {
			t.Fatalf("feed %d: %v", step.seq, err)
		}
	}
	if _, err := r.Feed(tcpSegment(100, "abc", 1001)); err != nil {
		t.Fatalf("feed retransmission: %v", err)
	}
	stats := r.Stats()
	if stats.Segments != 4 {
		t.Fatalf("segments = %d, want 4", stats.Segments)
	}
	if stats.DeliveredBytes != 3 {
		t.Fatalf("delivered bytes = %d, want 3", stats.DeliveredBytes)
	}
	if stats.Duplicates != 3 {
		t.Fatalf("duplicates = %d, want 3", stats.Duplicates)
	}
	stream, err := r.Flush(flowKey())
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if string(stream) != "abc" {
		t.Fatalf("stream = %q, want abc", string(stream))
	}
}
