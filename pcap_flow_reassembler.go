// Package task reassembles captured TCP flows from out of order and
// retransmitted segments and keeps the per flow lifecycle bounded.
package task

import "errors"

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// PcapFlowReassembler owns the flow table and the delivered byte streams.
type PcapFlowReassembler struct {
	policy Policy
}

// NewPcapFlowReassembler builds a reassembler with the given policy.
func NewPcapFlowReassembler(policy Policy) *PcapFlowReassembler {
	return &PcapFlowReassembler{policy: policy}
}

// Feed inserts one segment and returns the bytes that became deliverable.
func (r *PcapFlowReassembler) Feed(seg Segment) (FlowEvent, error) {
	return FlowEvent{}, errNotImplemented
}

// Flush returns the delivered byte stream of one flow.
func (r *PcapFlowReassembler) Flush(key FlowKey) ([]byte, error) {
	return nil, errNotImplemented
}

// Close finalises one flow with an explicit reason.
func (r *PcapFlowReassembler) Close(key FlowKey, reason string) error {
	return errNotImplemented
}

// Flow reads the durable state of one flow.
func (r *PcapFlowReassembler) Flow(key FlowKey) (FlowState, bool) {
	return FlowState{}, false
}

// Active lists the flows that are still open.
func (r *PcapFlowReassembler) Active() []FlowKey {
	return nil
}

// Expire closes the flows that stayed idle for longer than the policy allows.
func (r *PcapFlowReassembler) Expire(now int64) []FlowKey {
	return nil
}

// Snapshot serialises every flow and the counters.
func (r *PcapFlowReassembler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *PcapFlowReassembler) Restore(data []byte) error {
	return errNotImplemented
}

// Stats returns a copy of the counters.
func (r *PcapFlowReassembler) Stats() Stats {
	return Stats{}
}

// Reset drops every flow and counter.
func (r *PcapFlowReassembler) Reset() {
}
