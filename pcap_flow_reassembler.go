// Package task reassembles captured TCP flows from out of order and
// retransmitted segments and keeps the per flow lifecycle bounded.
package task

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
)

// Close reasons recorded on a flow.
const (
	ReasonFin  = "fin"
	ReasonIdle = "idle"
	ReasonFull = "buffer_full"
)

// Errors returned by the reassembler.
var (
	ErrBadFlow        = errors.New("flow key is invalid")
	ErrEmptySegment   = errors.New("segment carries neither data nor a close flag")
	ErrSeqOutOfWindow = errors.New("segment sequence is outside the receive window")
	ErrFlowClosed     = errors.New("flow is closed")
	ErrUnknownFlow    = errors.New("unknown flow")
	ErrFlowLimit      = errors.New("flow table is full")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// FlowKey is the four tuple plus protocol of one direction.
type FlowKey struct {
	SrcIP   string
	DstIP   string
	SrcPort uint16
	DstPort uint16
	Proto   uint8
}

// Segment is one captured TCP segment.
type Segment struct {
	Key  FlowKey
	Seq  uint32
	Data []byte
	At   int64
	Fin  bool
	Syn  bool
}

// Policy bounds the flow table and the per flow buffering.
type Policy struct {
	IdleTimeout int64
	MaxBuffer   int
	WindowSize  uint32
	Linger      int64
	MaxFlows    int
}

// FlowState is the durable state of one flow.
type FlowState struct {
	Key       FlowKey
	NextSeq   uint32
	Buffered  int
	Delivered int
	Closed    bool
	Reason    string
	Truncated bool
	PeerSeen  bool
	Fin       bool
	LastAt    int64
	Conflicts int
}

// FlowEvent is the outcome of one Feed call.
type FlowEvent struct {
	Key       FlowKey
	Delivered []byte
	Buffered  int
	Gap       bool
	Duplicate int
	Closed    bool
	Truncated bool
}

// Stats counts segment and byte work.
type Stats struct {
	Segments       int
	DeliveredBytes int
	Duplicates     int
	Rejected       int
	Expired        int
	Conflicts      int
}

type flow struct {
	state    FlowState
	startSeq uint32
	stream   []byte
	buffered map[uint32]byte
	pending  pendingMaxHeap
	closedAt int64
}

// PcapFlowReassembler owns the flow table and the delivered byte streams.
type PcapFlowReassembler struct {
	mu     sync.RWMutex
	policy Policy
	flows  map[FlowKey]*flow
	stats  Stats
}

// NewPcapFlowReassembler builds a reassembler with the given policy.
func NewPcapFlowReassembler(policy Policy) *PcapFlowReassembler {
	if policy.IdleTimeout < 0 {
		policy.IdleTimeout = 0
	}
	if policy.MaxBuffer < 0 {
		policy.MaxBuffer = 0
	}
	if policy.Linger < 0 {
		policy.Linger = 0
	}
	if policy.MaxFlows < 0 {
		policy.MaxFlows = 0
	}
	return &PcapFlowReassembler{
		policy: policy,
		flows:  make(map[FlowKey]*flow),
	}
}

// Feed inserts one segment and returns the bytes that became deliverable.
func (r *PcapFlowReassembler) Feed(seg Segment) (FlowEvent, error) {
	if !validFlowKey(seg.Key) {
		return FlowEvent{}, ErrBadFlow
	}
	if len(seg.Data) == 0 && !seg.Fin {
		return FlowEvent{}, ErrEmptySegment
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	f, ok := r.flows[seg.Key]
	if !ok {
		if len(r.flows) >= r.policy.MaxFlows {
			return FlowEvent{}, ErrFlowLimit
		}
		f = newFlow(seg.Key, seg.Seq, seg.At)
		r.flows[seg.Key] = f
		if peer, exists := r.flows[peerKey(seg.Key)]; exists {
			peer.state.PeerSeen = true
			f.state.PeerSeen = true
		}
	} else if f.state.Closed && seg.At-f.closedAt > r.policy.Linger {
		return FlowEvent{}, ErrFlowClosed
	}

	behind := uint32(f.state.NextSeq - seg.Seq)
	if behind != 0 && behind < 1<<31 && behind > r.policy.WindowSize {
		r.stats.Rejected++
		return FlowEvent{}, ErrSeqOutOfWindow
	}

	if seg.At > f.state.LastAt {
		f.state.LastAt = seg.At
	}

	r.stats.Segments++
	ev := FlowEvent{Key: seg.Key}
	conflict := false
	droppedAhead := false

	for i, dataByte := range seg.Data {
		seq := seg.Seq + uint32(i)
		behind = uint32(f.state.NextSeq - seq)
		if behind != 0 && behind < 1<<31 {
			ev.Duplicate++
			if old, found := f.deliveredByte(seq); found && old != dataByte && !conflict {
				conflict = true
			}
			continue
		}

		if seq == f.state.NextSeq {
			if bufferedByte, found := f.buffered[seq]; found {
				ev.Duplicate++
				if bufferedByte != dataByte && !conflict {
					conflict = true
				}
				delete(f.buffered, seq)
				f.appendDelivered(bufferedByte, &ev)
			} else {
				f.appendDelivered(dataByte, &ev)
			}
			f.drainBuffered(&ev)
			continue
		}

		if bufferedByte, found := f.buffered[seq]; found {
			ev.Duplicate++
			if bufferedByte != dataByte && !conflict {
				conflict = true
			}
			continue
		}

		kept, truncated := f.bufferByte(seq, dataByte, r.policy.MaxBuffer)
		if truncated {
			f.state.Truncated = true
		}
		if !kept && truncated {
			droppedAhead = true
		}
	}

	if seg.Fin {
		f.state.Fin = true
		if !f.state.Closed {
			r.closeFlow(f, ReasonFin, seg.At)
		}
	}

	if conflict {
		f.state.Conflicts++
		r.stats.Conflicts++
	}
	r.stats.Duplicates += ev.Duplicate
	r.stats.DeliveredBytes += len(ev.Delivered)

	ev.Buffered = len(f.buffered)
	ev.Gap = ev.Buffered > 0 || droppedAhead
	ev.Closed = f.state.Closed
	ev.Truncated = f.state.Truncated
	return ev, nil
}

// Flush returns the delivered byte stream of one flow.
func (r *PcapFlowReassembler) Flush(key FlowKey) ([]byte, error) {
	if !validFlowKey(key) {
		return nil, ErrBadFlow
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	f, ok := r.flows[key]
	if !ok {
		return nil, ErrUnknownFlow
	}
	return append([]byte(nil), f.stream...), nil
}

// Close finalises one flow with an explicit reason.
func (r *PcapFlowReassembler) Close(key FlowKey, reason string) error {
	if !validFlowKey(key) {
		return ErrBadFlow
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	f, ok := r.flows[key]
	if !ok {
		return ErrUnknownFlow
	}
	if !f.state.Closed {
		r.closeFlow(f, reason, f.state.LastAt)
	}
	return nil
}

// Flow reads the durable state of one flow.
func (r *PcapFlowReassembler) Flow(key FlowKey) (FlowState, bool) {
	if !validFlowKey(key) {
		return FlowState{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	f, ok := r.flows[key]
	if !ok {
		return FlowState{}, false
	}
	state := f.state
	state.Buffered = len(f.buffered)
	state.Delivered = len(f.stream)
	return state, true
}

// Active lists the flows that are still open.
func (r *PcapFlowReassembler) Active() []FlowKey {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]FlowKey, 0, len(r.flows))
	for key, f := range r.flows {
		if !f.state.Closed {
			keys = append(keys, key)
		}
	}
	sortFlowKeys(keys)
	return keys
}

// Expire closes the flows that stayed idle for longer than the policy allows.
func (r *PcapFlowReassembler) Expire(now int64) []FlowKey {
	r.mu.Lock()
	defer r.mu.Unlock()

	keys := make([]FlowKey, 0)
	for key, f := range r.flows {
		if f.state.Closed {
			continue
		}
		if now-f.state.LastAt > r.policy.IdleTimeout {
			r.closeFlow(f, ReasonIdle, now)
			keys = append(keys, key)
		}
	}
	r.stats.Expired += len(keys)
	sortFlowKeys(keys)
	return keys
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotPayload struct {
	Flows []flowSnapshot `json:"flows"`
	Stats Stats          `json:"stats"`
}

type flowSnapshot struct {
	Key       FlowKey       `json:"key"`
	NextSeq   uint32        `json:"next_seq"`
	StartSeq  uint32        `json:"start_seq"`
	Stream    []byte        `json:"stream"`
	Buffer    []bufferChunk `json:"buffer,omitempty"`
	Buffered  int           `json:"buffered"`
	Delivered int           `json:"delivered"`
	Closed    bool          `json:"closed"`
	Reason    string        `json:"reason,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
	PeerSeen  bool          `json:"peer_seen,omitempty"`
	Fin       bool          `json:"fin,omitempty"`
	LastAt    int64         `json:"last_at"`
	ClosedAt  int64         `json:"closed_at,omitempty"`
	Conflicts int           `json:"conflicts,omitempty"`
}

type bufferChunk struct {
	Seq  uint32 `json:"seq"`
	Data []byte `json:"data"`
}

const snapshotMagic = "gsb-pcap-flow-reassembler"

// Snapshot serialises every flow and the counters.
func (r *PcapFlowReassembler) Snapshot() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	payload := snapshotPayload{
		Flows: make([]flowSnapshot, 0, len(r.flows)),
		Stats: r.stats,
	}
	keys := make([]FlowKey, 0, len(r.flows))
	for key := range r.flows {
		keys = append(keys, key)
	}
	sortFlowKeys(keys)
	for _, key := range keys {
		payload.Flows = append(payload.Flows, snapshotFlow(r.flows[key]))
	}

	payloadData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payloadData)
	envelopeData, err := json.Marshal(snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  1,
		Payload:  payloadData,
		Checksum: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return nil, err
	}
	return envelopeData, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *PcapFlowReassembler) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || envelope.Version != 1 || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if !checksumEqual(envelope.Checksum, hex.EncodeToString(sum[:])) {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return ErrBadSnapshot
	}
	if !validStats(payload.Stats) || len(payload.Flows) > r.policy.MaxFlows {
		return ErrBadSnapshot
	}

	flows := make(map[FlowKey]*flow, len(payload.Flows))
	for _, snap := range payload.Flows {
		f, ok := restoreFlow(snap, r.policy)
		if !ok {
			return ErrBadSnapshot
		}
		if _, exists := flows[f.state.Key]; exists {
			return ErrBadSnapshot
		}
		flows[f.state.Key] = f
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.flows = flows
	r.stats = payload.Stats
	return nil
}

// Stats returns a copy of the counters.
func (r *PcapFlowReassembler) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stats
}

// Reset drops every flow and counter.
func (r *PcapFlowReassembler) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flows = make(map[FlowKey]*flow)
	r.stats = Stats{}
}

func validFlowKey(key FlowKey) bool {
	return key.SrcIP != "" && key.DstIP != "" && key.SrcPort != 0 && key.DstPort != 0 && key.Proto != 0
}

func peerKey(key FlowKey) FlowKey {
	return FlowKey{
		SrcIP:   key.DstIP,
		DstIP:   key.SrcIP,
		SrcPort: key.DstPort,
		DstPort: key.SrcPort,
		Proto:   key.Proto,
	}
}

func newFlow(key FlowKey, seq uint32, at int64) *flow {
	f := &flow{
		state: FlowState{
			Key:     key,
			NextSeq: seq,
			LastAt:  at,
		},
		startSeq: seq,
		buffered: make(map[uint32]byte),
	}
	f.pending.next = seq
	return f
}

func (r *PcapFlowReassembler) closeFlow(f *flow, reason string, at int64) {
	f.state.Closed = true
	f.state.Reason = reason
	f.closedAt = at
}

func (f *flow) appendDelivered(dataByte byte, ev *FlowEvent) {
	f.stream = append(f.stream, dataByte)
	ev.Delivered = append(ev.Delivered, dataByte)
	f.state.NextSeq++
}

func (f *flow) drainBuffered(ev *FlowEvent) {
	if len(f.buffered) == 0 {
		f.pending.next = f.state.NextSeq
		f.pending.seqs = f.pending.seqs[:0]
		return
	}

	f.pending.next = f.state.NextSeq
	for {
		dataByte, ok := f.buffered[f.state.NextSeq]
		if !ok {
			break
		}
		delete(f.buffered, f.state.NextSeq)
		f.stream = append(f.stream, dataByte)
		ev.Delivered = append(ev.Delivered, dataByte)
		f.state.NextSeq++
	}
	if len(f.buffered) == 0 {
		f.pending.seqs = f.pending.seqs[:0]
	} else if f.pending.Len() > 2*len(f.buffered)+64 {
		f.rebuildPendingHeap()
	}
}

func (f *flow) deliveredByte(seq uint32) (byte, bool) {
	offset := uint32(seq - f.startSeq)
	if uint64(offset) >= uint64(len(f.stream)) {
		return 0, false
	}
	return f.stream[offset], true
}

func (f *flow) bufferByte(seq uint32, dataByte byte, limit int) (bool, bool) {
	if limit <= 0 {
		return false, true
	}

	f.buffered[seq] = dataByte
	f.pending.next = f.state.NextSeq
	heap.Push(&f.pending, seq)

	truncated := false
	kept := true
	for len(f.buffered) > limit {
		maxSeq, ok := f.popMaxBuffered()
		if !ok {
			break
		}
		delete(f.buffered, maxSeq)
		truncated = true
		if maxSeq == seq {
			kept = false
		}
	}
	if f.pending.Len() > 2*len(f.buffered)+64 {
		f.rebuildPendingHeap()
	}
	return kept, truncated
}

func (f *flow) popMaxBuffered() (uint32, bool) {
	for f.pending.Len() > 0 {
		seq := f.pending.seqs[0]
		if _, ok := f.buffered[seq]; ok {
			heap.Pop(&f.pending)
			return seq, true
		}
		heap.Pop(&f.pending)
	}
	return 0, false
}

func (f *flow) rebuildPendingHeap() {
	f.pending.next = f.state.NextSeq
	f.pending.seqs = f.pending.seqs[:0]
	for seq := range f.buffered {
		f.pending.seqs = append(f.pending.seqs, seq)
	}
	heap.Init(&f.pending)
}

type pendingMaxHeap struct {
	seqs []uint32
	next uint32
}

func (h pendingMaxHeap) Len() int { return len(h.seqs) }

func (h pendingMaxHeap) Less(i, j int) bool {
	return uint32(h.seqs[i]-h.next) > uint32(h.seqs[j]-h.next)
}

func (h pendingMaxHeap) Swap(i, j int) {
	h.seqs[i], h.seqs[j] = h.seqs[j], h.seqs[i]
}

func (h *pendingMaxHeap) Push(value any) {
	h.seqs = append(h.seqs, value.(uint32))
}

func (h *pendingMaxHeap) Pop() any {
	last := len(h.seqs) - 1
	value := h.seqs[last]
	h.seqs = h.seqs[:last]
	return value
}

func sortFlowKeys(keys []FlowKey) {
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.SrcIP != b.SrcIP {
			return a.SrcIP < b.SrcIP
		}
		if a.DstIP != b.DstIP {
			return a.DstIP < b.DstIP
		}
		if a.SrcPort != b.SrcPort {
			return a.SrcPort < b.SrcPort
		}
		if a.DstPort != b.DstPort {
			return a.DstPort < b.DstPort
		}
		return a.Proto < b.Proto
	})
}

func snapshotFlow(f *flow) flowSnapshot {
	snap := flowSnapshot{
		Key:       f.state.Key,
		NextSeq:   f.state.NextSeq,
		StartSeq:  f.startSeq,
		Stream:    append([]byte(nil), f.stream...),
		Buffered:  len(f.buffered),
		Delivered: len(f.stream),
		Closed:    f.state.Closed,
		Reason:    f.state.Reason,
		Truncated: f.state.Truncated,
		PeerSeen:  f.state.PeerSeen,
		Fin:       f.state.Fin,
		LastAt:    f.state.LastAt,
		ClosedAt:  f.closedAt,
		Conflicts: f.state.Conflicts,
	}

	if len(f.buffered) == 0 {
		return snap
	}

	seqs := make([]uint32, 0, len(f.buffered))
	for seq := range f.buffered {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool {
		return uint32(seqs[i]-f.state.NextSeq) < uint32(seqs[j]-f.state.NextSeq)
	})

	var current *bufferChunk
	var lastSeq uint32
	for i, seq := range seqs {
		if i == 0 || seq != lastSeq+1 {
			snap.Buffer = append(snap.Buffer, bufferChunk{Seq: seq})
			current = &snap.Buffer[len(snap.Buffer)-1]
		}
		current.Data = append(current.Data, f.buffered[seq])
		lastSeq = seq
	}
	return snap
}

func restoreFlow(snap flowSnapshot, policy Policy) (*flow, bool) {
	if !validFlowKey(snap.Key) || snap.Buffered < 0 || snap.Delivered < 0 || snap.Conflicts < 0 {
		return nil, false
	}
	if snap.Delivered != len(snap.Stream) || snap.Buffered > policy.MaxBuffer {
		return nil, false
	}
	if snap.NextSeq != snap.StartSeq+uint32(len(snap.Stream)) {
		return nil, false
	}

	f := &flow{
		state: FlowState{
			Key:       snap.Key,
			NextSeq:   snap.NextSeq,
			Buffered:  snap.Buffered,
			Delivered: snap.Delivered,
			Closed:    snap.Closed,
			Reason:    snap.Reason,
			Truncated: snap.Truncated,
			PeerSeen:  snap.PeerSeen,
			Fin:       snap.Fin,
			LastAt:    snap.LastAt,
			Conflicts: snap.Conflicts,
		},
		startSeq: snap.StartSeq,
		stream:   append([]byte(nil), snap.Stream...),
		buffered: make(map[uint32]byte, snap.Buffered),
		closedAt: snap.ClosedAt,
	}

	buffered := 0
	for _, chunk := range snap.Buffer {
		if len(chunk.Data) == 0 {
			return nil, false
		}
		for i, dataByte := range chunk.Data {
			seq := chunk.Seq + uint32(i)
			offset := uint32(seq - f.state.NextSeq)
			if offset == 0 || offset >= 1<<31 {
				return nil, false
			}
			if _, exists := f.buffered[seq]; exists {
				return nil, false
			}
			f.buffered[seq] = dataByte
			buffered++
			if buffered > policy.MaxBuffer {
				return nil, false
			}
		}
	}
	if buffered != snap.Buffered {
		return nil, false
	}
	f.rebuildPendingHeap()
	return f, true
}

func validStats(stats Stats) bool {
	return stats.Segments >= 0 &&
		stats.DeliveredBytes >= 0 &&
		stats.Duplicates >= 0 &&
		stats.Rejected >= 0 &&
		stats.Expired >= 0 &&
		stats.Conflicts >= 0
}

func checksumEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
