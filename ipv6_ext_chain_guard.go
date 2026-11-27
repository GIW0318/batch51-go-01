// Package task parses the IPv6 extension header chain of one packet and
// applies the per interface ingress policy to it.
package task

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

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

const (
	ipv6FixedHeaderLen = 40
	snapshotVersion    = 1

	nextHopByHop = 0
	nextTCP      = 6
	nextUDP      = 17
	nextRouting  = 43
	nextFragment = 44
	nextESP      = 50
	nextAH       = 51
	nextICMPv6   = 58
	nextNoNext   = 59
	nextDestOpts = 60

	optionPad1  = 0
	optionPadN  = 1
	optionJumbo = 0xC2
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

	jumboInvalid bool
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

	mu            sync.RWMutex
	defaultPolicy Policy
	policies      map[string]Policy
	counters      map[string]Counters
}

type guardSnapshot struct {
	Version  int                 `json:"version"`
	Default  Policy              `json:"default"`
	Policies map[string]Policy   `json:"policies"`
	Counters map[string]Counters `json:"counters"`
}

// NewIpv6ExtChainGuard builds a guard with the given configuration.
func NewIpv6ExtChainGuard(cfg ChainPolicy) *Ipv6ExtChainGuard {
	return &Ipv6ExtChainGuard{
		cfg:           cfg,
		defaultPolicy: clonePolicy(cfg.Default),
		policies:      make(map[string]Policy),
		counters:      make(map[string]Counters),
	}
}

// Parse walks the next header chain of one packet.
func (g *Ipv6ExtChainGuard) Parse(pkt []byte) (Chain, error) {
	if len(pkt) < ipv6FixedHeaderLen {
		return Chain{}, ErrShortPacket
	}
	if pkt[0]>>4 != 6 {
		return Chain{}, ErrBadVersion
	}

	chain := Chain{
		Version:    int(pkt[0] >> 4),
		PayloadLen: int(binary.BigEndian.Uint16(pkt[4:6])),
		NextHeader: int(pkt[6]),
		TotalLen:   len(pkt),
	}
	next := chain.NextHeader
	offset := ipv6FixedHeaderLen

	for {
		if upperLayer(next) {
			chain.Upper = headerName(next)
			chain.UpperOffset = offset
			chain.Payload = pkt[offset:]
			return chain, nil
		}

		switch next {
		case nextESP, nextNoNext:
			chain.Upper = headerName(next)
			chain.UpperOffset = offset
			chain.Payload = pkt[offset:]
			return chain, nil
		case nextHopByHop, nextDestOpts, nextRouting, nextFragment, nextAH:
			hdr, following, err := parseExtensionHeader(pkt, offset, next)
			if err != nil {
				return Chain{}, err
			}
			chain.Headers = append(chain.Headers, hdr)
			offset += hdr.Length
			next = following
		default:
			chain.Upper = UpperUnknown
			chain.UpperOffset = offset
			chain.Payload = pkt[offset:]
			return chain, nil
		}
	}
}

// Check applies one policy to an already parsed chain.
func (g *Ipv6ExtChainGuard) Check(chain Chain, policy Policy) (Verdict, error) {
	verdict := Verdict{
		Action:  ActionAccept,
		Reason:  ReasonAccept,
		Headers: len(chain.Headers),
	}
	drop := func(reason string) (Verdict, error) {
		verdict.Action = ActionDrop
		verdict.Reason = reason
		return verdict, nil
	}

	if exceeds(verdict.Headers, policy.MaxHeaders) {
		return drop(ReasonTooManyHeaders)
	}

	optionCount := 0
	for _, hdr := range chain.Headers {
		optionCount += len(hdr.Options)
	}
	if exceeds(optionCount, policy.MaxOptions) {
		return drop(ReasonTooManyOptions)
	}

	seenFragment := false
	fragmentCount := 0
	for i, hdr := range chain.Headers {
		if seenFragment && (hdr.Type == HeaderRouting || hdr.Type == HeaderHopByHop) {
			return drop(ReasonBadOrder)
		}
		if hdr.Type == HeaderHopByHop && i != 0 {
			return drop(ReasonHopByHopNotFirst)
		}
		if hdr.Type == HeaderFragment {
			fragmentCount++
			seenFragment = true
		}
	}
	if fragmentCount > 1 {
		return drop(ReasonDuplicateFragment)
	}

	for _, hdr := range chain.Headers {
		if hdr.Type != HeaderRouting {
			continue
		}
		if hdr.RoutingType == 0 {
			return drop(ReasonRoutingType0)
		}
		if !containsRoutingType(policy.AllowRoutingTypes, hdr.RoutingType) {
			return drop(ReasonRoutingNotAllowed)
		}
	}

	for _, hdr := range chain.Headers {
		if hdr.Type != HeaderFragment {
			continue
		}
		if hdr.FragOffset == 0 && !hdr.MoreFragments && !policy.AllowAtomicFragments {
			return drop(ReasonAtomicFragment)
		}
	}

	for _, hdr := range chain.Headers {
		for _, opt := range hdr.Options {
			if opt.Critical && !knownOption(opt.Type) {
				return drop(ReasonUnknownCritical)
			}
		}
	}

	hasJumbo := false
	jumboMismatch := false
	for _, hdr := range chain.Headers {
		if !hdr.HasJumbo {
			continue
		}
		hasJumbo = true
		if hdr.jumboInvalid || chain.PayloadLen != 0 || !jumboLengthMatches(chain, hdr.JumboLength) {
			jumboMismatch = true
		}
	}
	if hasJumbo {
		if !policy.AllowJumbo {
			return drop(ReasonJumboForbidden)
		}
		if jumboMismatch {
			return drop(ReasonJumboMismatch)
		}
	}

	if !knownUpper(chain.Upper) && !policy.AllowUnknown {
		return drop(ReasonUnknownNextHeader)
	}

	return verdict, nil
}

// Explain parses one packet and records the chain steps and the verdict.
func (g *Ipv6ExtChainGuard) Explain(pkt []byte) (Trace, error) {
	trace := Trace{PacketLen: len(pkt)}
	chain, err := g.Parse(pkt)
	if err != nil {
		trace.Err = err.Error()
		trace.Verdict.Action = ActionDrop
		if errors.Is(err, ErrTruncatedChain) {
			trace.Verdict.Reason = ReasonTruncated
		}
		return trace, err
	}

	g.mu.RLock()
	policy := clonePolicy(g.defaultPolicy)
	g.mu.RUnlock()

	verdict, err := g.Check(chain, policy)
	if err != nil {
		trace.Err = err.Error()
		return trace, err
	}

	trace.Steps = make([]TraceStep, 0, len(chain.Headers)+1)
	for _, hdr := range chain.Headers {
		trace.Steps = append(trace.Steps, TraceStep{
			Type:   hdr.Type,
			Offset: hdr.Offset,
			Detail: describeHeader(hdr),
		})
	}
	trace.Steps = append(trace.Steps, TraceStep{
		Type:   chain.Upper,
		Offset: chain.UpperOffset,
		Detail: fmt.Sprintf("payload=%d", len(chain.Payload)),
	})
	trace.Chain = chain
	trace.Verdict = verdict
	return trace, nil
}

// SetPolicy installs the policy of one interface.
func (g *Ipv6ExtChainGuard) SetPolicy(iface string, p Policy) error {
	if strings.TrimSpace(iface) == "" {
		return ErrBadIface
	}
	p = clonePolicy(p)

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.policies[iface]; !exists &&
		g.cfg.MaxIfaces > 0 && len(g.policies) >= g.cfg.MaxIfaces {
		return ErrIfaceLimit
	}
	g.policies[iface] = p
	return nil
}

// Policy reads the policy of one interface.
func (g *Ipv6ExtChainGuard) Policy(iface string) (Policy, bool) {
	if strings.TrimSpace(iface) == "" {
		return Policy{}, false
	}
	g.mu.RLock()
	p, ok := g.policies[iface]
	g.mu.RUnlock()
	if !ok {
		return Policy{}, false
	}
	return clonePolicy(p), true
}

// SetDefault installs the fallback policy.
func (g *Ipv6ExtChainGuard) SetDefault(p Policy) error {
	p = clonePolicy(p)
	g.mu.Lock()
	g.defaultPolicy = p
	g.mu.Unlock()
	return nil
}

// Inspect parses a packet, applies the interface policy and counts it.
func (g *Ipv6ExtChainGuard) Inspect(iface string, pkt []byte) (Verdict, error) {
	if strings.TrimSpace(iface) == "" {
		return Verdict{}, ErrBadIface
	}

	g.mu.RLock()
	policy, configured := g.policies[iface]
	if configured {
		policy = clonePolicy(policy)
	} else {
		policy = clonePolicy(g.defaultPolicy)
	}
	g.mu.RUnlock()

	chain, err := g.Parse(pkt)
	if err != nil {
		g.mu.Lock()
		counters := g.counters[iface]
		counters.ParseErrors++
		g.counters[iface] = counters
		g.mu.Unlock()
		reason := ""
		if errors.Is(err, ErrTruncatedChain) {
			reason = ReasonTruncated
		}
		return Verdict{
			Action: ActionDrop,
			Reason: reason,
			Iface:  iface,
		}, err
	}

	verdict, err := g.Check(chain, policy)
	if err != nil {
		return Verdict{}, err
	}
	verdict.Iface = iface

	g.mu.Lock()
	counters := g.counters[iface]
	switch verdict.Action {
	case ActionAccept:
		counters.Accepted++
	case ActionDrop:
		counters.Dropped++
	}
	g.counters[iface] = counters
	g.mu.Unlock()

	return verdict, nil
}

// Stats returns the counters of one interface.
func (g *Ipv6ExtChainGuard) Stats(iface string) Counters {
	if strings.TrimSpace(iface) == "" {
		return Counters{}
	}
	g.mu.RLock()
	counters := g.counters[iface]
	g.mu.RUnlock()
	return counters
}

// Snapshot serialises policies and counters.
func (g *Ipv6ExtChainGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	snapshot := guardSnapshot{
		Version:  snapshotVersion,
		Default:  clonePolicy(g.defaultPolicy),
		Policies: make(map[string]Policy, len(g.policies)),
		Counters: make(map[string]Counters, len(g.counters)),
	}
	for iface, policy := range g.policies {
		snapshot.Policies[iface] = clonePolicy(policy)
	}
	for iface, counters := range g.counters {
		snapshot.Counters[iface] = counters
	}
	g.mu.RUnlock()

	return json.Marshal(snapshot)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *Ipv6ExtChainGuard) Restore(data []byte) error {
	var snapshot guardSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return ErrBadSnapshot
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrBadSnapshot
	}
	if snapshot.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	policies := make(map[string]Policy, len(snapshot.Policies))
	for iface, policy := range snapshot.Policies {
		if strings.TrimSpace(iface) == "" {
			return ErrBadSnapshot
		}
		policies[iface] = clonePolicy(policy)
	}
	counters := make(map[string]Counters, len(snapshot.Counters))
	for iface, counter := range snapshot.Counters {
		if strings.TrimSpace(iface) == "" {
			return ErrBadSnapshot
		}
		counters[iface] = counter
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cfg.MaxIfaces > 0 && len(policies) > g.cfg.MaxIfaces {
		return ErrBadSnapshot
	}
	g.defaultPolicy = clonePolicy(snapshot.Default)
	g.policies = policies
	g.counters = counters
	return nil
}

func parseExtensionHeader(pkt []byte, offset, next int) (ExtHeader, int, error) {
	remaining := len(pkt) - offset
	hdr := ExtHeader{
		Type:   headerName(next),
		Offset: offset,
	}

	switch next {
	case nextHopByHop, nextDestOpts:
		if remaining < 2 {
			return ExtHeader{}, 0, ErrTruncatedChain
		}
		hdr.Length = (int(pkt[offset+1]) + 1) * 8
		if hdr.Length > remaining {
			return ExtHeader{}, 0, ErrTruncatedChain
		}
		hdr.Next = headerName(int(pkt[offset]))
		options, hasJumbo, jumboLength, jumboInvalid, err := parseOptions(pkt[offset+2 : offset+hdr.Length])
		if err != nil {
			return ExtHeader{}, 0, err
		}
		hdr.Options = options
		hdr.HasJumbo = hasJumbo
		hdr.JumboLength = jumboLength
		hdr.jumboInvalid = jumboInvalid
	case nextRouting:
		if remaining < 2 {
			return ExtHeader{}, 0, ErrTruncatedChain
		}
		hdr.Length = (int(pkt[offset+1]) + 1) * 8
		if hdr.Length > remaining {
			return ExtHeader{}, 0, ErrTruncatedChain
		}
		hdr.Next = headerName(int(pkt[offset]))
		hdr.RoutingType = int(pkt[offset+2])
		hdr.SegmentsLeft = int(pkt[offset+3])
	case nextFragment:
		if remaining < 8 {
			return ExtHeader{}, 0, ErrTruncatedChain
		}
		hdr.Length = 8
		hdr.Next = headerName(int(pkt[offset]))
		word := binary.BigEndian.Uint16(pkt[offset+2 : offset+4])
		hdr.FragOffset = int(word >> 3)
		hdr.MoreFragments = word&1 != 0
		hdr.Identification = binary.BigEndian.Uint32(pkt[offset+4 : offset+8])
	case nextAH:
		if remaining < 2 {
			return ExtHeader{}, 0, ErrTruncatedChain
		}
		hdr.Length = (int(pkt[offset+1]) + 2) * 4
		if hdr.Length < 8 || hdr.Length > remaining {
			return ExtHeader{}, 0, ErrTruncatedChain
		}
		hdr.Next = headerName(int(pkt[offset]))
	default:
		return ExtHeader{}, 0, ErrTruncatedChain
	}

	return hdr, nextHeaderNumber(hdr.Next), nil
}

func parseOptions(area []byte) ([]Option, bool, uint32, bool, error) {
	var options []Option
	var hasJumbo bool
	var jumboLength uint32
	var jumboInvalid bool

	for offset := 0; offset < len(area); {
		typ := area[offset]
		if typ == optionPad1 {
			options = append(options, Option{Type: typ, Length: 1})
			offset++
			continue
		}
		if offset+1 >= len(area) {
			return nil, false, 0, false, ErrTruncatedChain
		}

		dataLength := int(area[offset+1])
		totalLength := 2 + dataLength
		if totalLength > len(area)-offset {
			return nil, false, 0, false, ErrTruncatedChain
		}

		option := Option{
			Type:     typ,
			Critical: typ&0xC0 != 0,
			Length:   totalLength,
		}
		options = append(options, option)

		if typ == optionJumbo {
			hasJumbo = true
			if dataLength != 4 {
				jumboInvalid = true
			} else {
				jumboLength = binary.BigEndian.Uint32(area[offset+2 : offset+6])
			}
		}
		offset += totalLength
	}

	return options, hasJumbo, jumboLength, jumboInvalid, nil
}

func upperLayer(next int) bool {
	switch next {
	case nextTCP, nextUDP, nextICMPv6:
		return true
	default:
		return false
	}
}

func knownUpper(upper string) bool {
	switch upper {
	case UpperTCP, UpperUDP, UpperICMPv6, UpperESP, UpperNoNext:
		return true
	default:
		return false
	}
}

func headerName(next int) string {
	switch next {
	case nextHopByHop:
		return HeaderHopByHop
	case nextRouting:
		return HeaderRouting
	case nextFragment:
		return HeaderFragment
	case nextDestOpts:
		return HeaderDestOpts
	case nextAH:
		return HeaderAH
	case nextTCP:
		return UpperTCP
	case nextUDP:
		return UpperUDP
	case nextICMPv6:
		return UpperICMPv6
	case nextESP:
		return UpperESP
	case nextNoNext:
		return UpperNoNext
	default:
		return UpperUnknown
	}
}

func nextHeaderNumber(name string) int {
	switch name {
	case HeaderHopByHop:
		return nextHopByHop
	case HeaderRouting:
		return nextRouting
	case HeaderFragment:
		return nextFragment
	case HeaderDestOpts:
		return nextDestOpts
	case HeaderAH:
		return nextAH
	case UpperTCP:
		return nextTCP
	case UpperUDP:
		return nextUDP
	case UpperICMPv6:
		return nextICMPv6
	case UpperESP:
		return nextESP
	case UpperNoNext:
		return nextNoNext
	default:
		return -1
	}
}

func knownOption(typ uint8) bool {
	switch typ {
	case optionPad1, optionPadN, optionJumbo:
		return true
	default:
		return false
	}
}

func containsRoutingType(types []int, routingType int) bool {
	for _, allowed := range types {
		if allowed == routingType {
			return true
		}
	}
	return false
}

func exceeds(count, limit int) bool {
	if limit < 0 {
		limit = 0
	}
	return count > limit
}

func jumboLengthMatches(chain Chain, length uint32) bool {
	if chain.TotalLen < ipv6FixedHeaderLen {
		return false
	}
	return uint64(length) == uint64(chain.TotalLen-ipv6FixedHeaderLen)
}

func clonePolicy(policy Policy) Policy {
	if policy.AllowRoutingTypes != nil {
		policy.AllowRoutingTypes = append([]int(nil), policy.AllowRoutingTypes...)
	}
	return policy
}

func describeHeader(hdr ExtHeader) string {
	switch hdr.Type {
	case HeaderRouting:
		return fmt.Sprintf("routing_type=%d segments_left=%d", hdr.RoutingType, hdr.SegmentsLeft)
	case HeaderFragment:
		return fmt.Sprintf("offset=%d more=%t id=%d", hdr.FragOffset, hdr.MoreFragments, hdr.Identification)
	case HeaderHopByHop, HeaderDestOpts:
		return fmt.Sprintf("options=%d jumbo=%t", len(hdr.Options), hdr.HasJumbo)
	default:
		return fmt.Sprintf("length=%d", hdr.Length)
	}
}
