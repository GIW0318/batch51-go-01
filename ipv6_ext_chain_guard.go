// Package task parses the IPv6 extension header chain of one packet and
// applies the per interface ingress policy to it.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Verdict actions.
const (
	ActionAccept = "accept"
	ActionDrop   = "drop"
)

// Extension header type names.
const (
	HeaderHopByHop = "hop_by_hop"
	HeaderRouting  = "routing"
	HeaderFragment = "fragment"
	HeaderDestOpts = "dest_opts"
	HeaderAH       = "ah"
)

// Upper layer names.
const (
	UpperTCP     = "tcp"
	UpperUDP     = "udp"
	UpperICMPv6  = "icmpv6"
	UpperESP     = "esp"
	UpperNoNext  = "no_next"
	UpperUnknown = "unknown"
)

// Reason codes attached to a verdict.
const (
	ReasonAccept            = "reason_accept"
	ReasonRoutingType0      = "reason_routing_type0"
	ReasonRoutingNotAllowed = "reason_routing_not_allowed"
	ReasonHopByHopNotFirst  = "reason_hop_by_hop_not_first"
	ReasonUnknownCritical   = "reason_unknown_critical_option"
	ReasonDuplicateFragment = "reason_duplicate_fragment"
	ReasonAtomicFragment    = "reason_atomic_fragment"
	ReasonBadOrder          = "reason_bad_header_order"
	ReasonTooManyHeaders    = "reason_too_many_headers"
	ReasonTooManyOptions    = "reason_too_many_options"
	ReasonJumboForbidden    = "reason_jumbo_forbidden"
	ReasonJumboMismatch     = "reason_jumbo_mismatch"
	ReasonUnknownNextHeader = "reason_unknown_next_header"
	ReasonTruncated         = "reason_truncated"
)

// Errors returned by the guard.
var (
	ErrShortPacket    = errors.New("packet is shorter than the ipv6 header")
	ErrBadVersion     = errors.New("packet is not ipv6")
	ErrTruncatedChain = errors.New("extension header overruns the packet")
	ErrBadIface       = errors.New("interface name is required")
	ErrIfaceLimit     = errors.New("interface policy table is full")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Option is one parsed hop by hop or destination option.
type Option struct {
	Type     uint8 `json:"type"`
	Critical bool  `json:"critical"`
	Length   int   `json:"length"`
}

// ExtHeader is one parsed extension header.
type ExtHeader struct {
	Type           string   `json:"type"`
	Offset         int      `json:"offset"`
	Length         int      `json:"length"`
	Next           string   `json:"next"`
	RoutingType    int      `json:"routing_type"`
	SegmentsLeft   int      `json:"segments_left"`
	FragOffset     int      `json:"frag_offset"`
	MoreFragments  bool     `json:"more_fragments"`
	Identification uint32   `json:"identification"`
	Options        []Option `json:"options"`
	HasJumbo       bool     `json:"has_jumbo"`
	JumboLength    uint32   `json:"jumbo_length"`
}

// Chain is the parsed shape of one packet.
type Chain struct {
	Version     int         `json:"version"`
	PayloadLen  int         `json:"payload_len"`
	NextHeader  int         `json:"next_header"`
	Headers     []ExtHeader `json:"headers"`
	Upper       string      `json:"upper"`
	UpperOffset int         `json:"upper_offset"`
	Payload     []byte      `json:"payload"`
	TotalLen    int         `json:"total_len"`
	Truncated   bool        `json:"truncated"`
}

// Policy is the ingress policy of one interface.
type Policy struct {
	AllowRoutingTypes    []int `json:"allow_routing_types"`
	AllowJumbo           bool  `json:"allow_jumbo"`
	AllowAtomicFragments bool  `json:"allow_atomic_fragments"`
	AllowUnknown         bool  `json:"allow_unknown"`
	MaxHeaders           int   `json:"max_headers"`
	MaxOptions           int   `json:"max_options"`
}

// Verdict is the outcome of one Check or Inspect call.
type Verdict struct {
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Iface   string `json:"iface"`
	Headers int    `json:"headers"`
}

// TraceStep is one header of the parsed chain in an explanation.
type TraceStep struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Detail string `json:"detail"`
}

// Trace is the explainable view of one packet.
type Trace struct {
	PacketLen int         `json:"packet_len"`
	Steps     []TraceStep `json:"steps"`
	Chain     Chain       `json:"chain"`
	Verdict   Verdict     `json:"verdict"`
	Err       string      `json:"err"`
}

// Counters are the per interface packet counters.
type Counters struct {
	Accepted    uint64 `json:"accepted"`
	Dropped     uint64 `json:"dropped"`
	ParseErrors uint64 `json:"parse_errors"`
}

// ChainPolicy configures the guard table and the fallback policy.
type ChainPolicy struct {
	Default   Policy
	MaxIfaces int
}

// Ipv6ExtChainGuard owns the per interface policies, the counters and the
// snapshot state.
type Ipv6ExtChainGuard struct {
	cfg ChainPolicy
}

// NewIpv6ExtChainGuard builds a guard with the given configuration.
func NewIpv6ExtChainGuard(cfg ChainPolicy) *Ipv6ExtChainGuard {
	return &Ipv6ExtChainGuard{cfg: cfg}
}

// Parse walks the next header chain of one packet.
func (g *Ipv6ExtChainGuard) Parse(pkt []byte) (Chain, error) {
	return Chain{}, errNotImplemented
}

// Check applies one policy to an already parsed chain.
func (g *Ipv6ExtChainGuard) Check(chain Chain, policy Policy) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Explain parses one packet and records the chain steps and the verdict.
func (g *Ipv6ExtChainGuard) Explain(pkt []byte) (Trace, error) {
	return Trace{}, errNotImplemented
}

// SetPolicy installs the policy of one interface.
func (g *Ipv6ExtChainGuard) SetPolicy(iface string, p Policy) error {
	return errNotImplemented
}

// Policy reads the policy of one interface.
func (g *Ipv6ExtChainGuard) Policy(iface string) (Policy, bool) {
	return Policy{}, false
}

// SetDefault installs the fallback policy.
func (g *Ipv6ExtChainGuard) SetDefault(p Policy) error {
	return errNotImplemented
}

// Inspect parses a packet, applies the interface policy and counts it.
func (g *Ipv6ExtChainGuard) Inspect(iface string, pkt []byte) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Stats returns the counters of one interface.
func (g *Ipv6ExtChainGuard) Stats(iface string) Counters {
	return Counters{}
}

// Snapshot serialises policies and counters.
func (g *Ipv6ExtChainGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *Ipv6ExtChainGuard) Restore(data []byte) error {
	return errNotImplemented
}
