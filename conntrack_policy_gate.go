// Package task keeps a node firewall connection tracking table and decides
// per packet whether a flow may pass, expire or be evicted.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Verdict actions.
const (
	ActionAllow = "allow"
	ActionDrop  = "drop"
)

// Rule actions.
const (
	ActionDeny = "deny"
)

// Protocols understood by the gate.
const (
	ProtoTCP  = "tcp"
	ProtoUDP  = "udp"
	ProtoICMP = "icmp"
)

// Connection states kept in the table.
const (
	StateNew         = "new"
	StateEstablished = "established"
	StateRelated     = "related"
	StateClosed      = "closed"
)

// TCP flags.
const (
	FlagSYN = "SYN"
	FlagACK = "ACK"
	FlagFIN = "FIN"
	FlagRST = "RST"
)

// Reason codes attached to a verdict.
const (
	ReasonRuleAllow   = "reason_rule_allow"
	ReasonRuleDeny    = "reason_rule_deny"
	ReasonNoRule      = "reason_no_rule"
	ReasonNoSYN       = "reason_no_syn"
	ReasonHandshake   = "reason_handshake"
	ReasonEstablished = "reason_established"
	ReasonClosed      = "reason_closed"
	ReasonBadState    = "reason_bad_state"
	ReasonTableFull   = "reason_table_full"
	ReasonSourceLimit = "reason_source_limit"
	ReasonFlowQuota   = "reason_flow_quota"
	ReasonRelatedFlow = "reason_related_flow"
	ReasonBadPacket   = "reason_bad_packet"
)

// Errors returned by the gate.
var (
	ErrBadPacket   = errors.New("packet is malformed")
	ErrBadRule     = errors.New("rule is malformed")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// FlowKey is the directional five tuple of one flow.
type FlowKey struct {
	Proto   string `json:"proto"`
	SrcIP   string `json:"src_ip"`
	SrcPort int    `json:"src_port"`
	DstIP   string `json:"dst_ip"`
	DstPort int    `json:"dst_port"`
}

// Packet is one packet offered to Admit.
type Packet struct {
	Proto    string   `json:"proto"`
	SrcIP    string   `json:"src_ip"`
	SrcPort  int      `json:"src_port"`
	DstIP    string   `json:"dst_ip"`
	DstPort  int      `json:"dst_port"`
	Flags    []string `json:"flags"`
	ICMPType int      `json:"icmp_type"`
	ICMPCode int      `json:"icmp_code"`
	Length   int      `json:"length"`
	Related  *FlowKey `json:"related"`
}

// Rule is one ordered firewall rule.
type Rule struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Proto   string `json:"proto"`
	SrcCIDR string `json:"src_cidr"`
	DstPort int    `json:"dst_port"`
}

// ConnState is one table entry.
type ConnState struct {
	Key      FlowKey   `json:"key"`
	State    string    `json:"state"`
	Packets  int       `json:"packets"`
	Bytes    int       `json:"bytes"`
	LastSeen time.Time `json:"last_seen"`
	Gen      int       `json:"gen"`
}

// Verdict is the outcome of one Admit call.
type Verdict struct {
	Action string  `json:"action"`
	Reason string  `json:"reason"`
	State  string  `json:"state"`
	Key    FlowKey `json:"key"`
	Gen    int     `json:"gen"`
}

// Stats is a copy of the gate counters.
type Stats struct {
	Admitted int `json:"admitted"`
	Dropped  int `json:"dropped"`
	Expired  int `json:"expired"`
	Evicted  int `json:"evicted"`
	Flows    int `json:"flows"`
}

// GateConfig bounds the table and fixes the per protocol timeouts.
type GateConfig struct {
	MaxFlows           int
	MaxFlowsPerSource  int
	MaxPacketsPerFlow  int
	TCPTimeout         time.Duration
	UDPTimeout         time.Duration
	ICMPTimeout        time.Duration
	ReEvaluateOnReload bool
	Now                func() time.Time
}

const snapshotVersion = 1

type compiledRule struct {
	action  string
	proto   string
	network *net.IPNet
	dstPort int
}

type flowRecord struct {
	state      ConnState
	seq        uint64
	ackPending bool
}

type gateCounters struct {
	admitted int
	dropped  int
	expired  int
	evicted  int
}

type snapshotPayload struct {
	Version     int         `json:"version"`
	Entries     []ConnState `json:"entries"`
	Rules       []Rule      `json:"rules"`
	Generation  int         `json:"generation"`
	Stats       Stats       `json:"stats"`
	PendingAcks []FlowKey   `json:"pending_acks,omitempty"`
	FlowOrder   []FlowKey   `json:"flow_order,omitempty"`
}

// ConntrackPolicyGate owns the tracking table, the rule generation and the
// counters.
type ConntrackPolicyGate struct {
	cfg GateConfig

	mu         sync.Mutex
	flows      map[FlowKey]*flowRecord
	rules      []Rule
	compiled   []compiledRule
	generation int
	nextSeq    uint64
	counters   gateCounters
}

// NewConntrackPolicyGate builds a gate with the given configuration.
func NewConntrackPolicyGate(cfg GateConfig) *ConntrackPolicyGate {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &ConntrackPolicyGate{
		cfg:   cfg,
		flows: make(map[FlowKey]*flowRecord),
	}
}

// Admit grades one packet and updates the tracking table.
func (g *ConntrackPolicyGate) Admit(pkt Packet) (Verdict, error) {
	packet, err := normalizePacket(pkt)
	if err != nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.counters.dropped++
		return Verdict{
			Action: ActionDrop,
			Reason: ReasonBadPacket,
			Key:    rawFlowKey(pkt),
			Gen:    g.generation,
		}, err
	}

	key := flowKeyOf(packet)
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.cfg.Now()
	if packet.Proto == ProtoICMP && isICMPError(packet.ICMPType) {
		return g.admitICMPErrorLocked(packet, key, now), nil
	}
	if record, forward, ok := g.findFlowLocked(key); ok {
		return g.admitExistingLocked(packet, key, record, forward, now), nil
	}
	return g.admitNewLocked(packet, key, now), nil
}

// Lookup reads one table entry by its exact key.
func (g *ConntrackPolicyGate) Lookup(flow FlowKey) (ConnState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	record, ok := g.flows[flow]
	if !ok {
		return ConnState{}, false
	}
	return record.state, true
}

// Expire removes every entry idle past its protocol timeout.
func (g *ConntrackPolicyGate) Expire(now time.Time) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	removed := 0
	for key, record := range g.flows {
		timeout := g.timeoutFor(key.Proto)
		if now.Sub(record.state.LastSeen) < timeout {
			continue
		}
		delete(g.flows, key)
		removed++
	}
	g.counters.expired += removed
	return removed
}

// Reload replaces the whole rule generation atomically.
func (g *ConntrackPolicyGate) Reload(rules []Rule) error {
	compiled, err := compileRules(rules)
	if err != nil {
		return err
	}
	ruleCopy := cloneRules(rules)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.generation++
	g.rules = ruleCopy
	g.compiled = compiled
	if g.cfg.ReEvaluateOnReload {
		for key := range g.flows {
			action, matched := g.matchRuleLocked(key)
			if !matched || action != ActionAllow {
				delete(g.flows, key)
			}
		}
	}
	return nil
}

// Delete removes one entry by its exact key.
func (g *ConntrackPolicyGate) Delete(flow FlowKey) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.flows[flow]; !ok {
		return false
	}
	delete(g.flows, flow)
	return true
}

// FlowCount reports how many entries the table holds.
func (g *ConntrackPolicyGate) FlowCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.flows)
}

// Rules returns a copy of the active rule generation.
func (g *ConntrackPolicyGate) Rules() []Rule {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cloneRules(g.rules)
}

// Stats returns the live counters.
func (g *ConntrackPolicyGate) Stats() Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Stats{
		Admitted: g.counters.admitted,
		Dropped:  g.counters.dropped,
		Expired:  g.counters.expired,
		Evicted:  g.counters.evicted,
		Flows:    len(g.flows),
	}
}

// Snapshot serialises table entries, rules, generation and counters.
func (g *ConntrackPolicyGate) Snapshot() ([]byte, error) {
	g.mu.Lock()
	type orderedFlow struct {
		key FlowKey
		seq uint64
	}
	entries := make([]ConnState, 0, len(g.flows))
	pendingAcks := make([]FlowKey, 0)
	ordered := make([]orderedFlow, 0, len(g.flows))
	for _, record := range g.flows {
		entries = append(entries, record.state)
		ordered = append(ordered, orderedFlow{key: record.state.Key, seq: record.seq})
		if record.ackPending && record.state.State == StateEstablished {
			pendingAcks = append(pendingAcks, record.state.Key)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return flowKeyLess(entries[i].Key, entries[j].Key)
	})
	sort.Slice(pendingAcks, func(i, j int) bool {
		return flowKeyLess(pendingAcks[i], pendingAcks[j])
	})
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].seq != ordered[j].seq {
			return ordered[i].seq < ordered[j].seq
		}
		return flowKeyLess(ordered[i].key, ordered[j].key)
	})
	flowOrder := make([]FlowKey, len(ordered))
	for i, item := range ordered {
		flowOrder[i] = item.key
	}
	payload := snapshotPayload{
		Version:     snapshotVersion,
		Entries:     entries,
		Rules:       cloneRules(g.rules),
		Generation:  g.generation,
		PendingAcks: pendingAcks,
		FlowOrder:   flowOrder,
		Stats: Stats{
			Admitted: g.counters.admitted,
			Dropped:  g.counters.dropped,
			Expired:  g.counters.expired,
			Evicted:  g.counters.evicted,
			Flows:    len(g.flows),
		},
	}
	g.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *ConntrackPolicyGate) Restore(data []byte) error {
	payload, err := decodeSnapshot(data)
	if err != nil {
		return err
	}
	compiled, err := compileRules(payload.Rules)
	if err != nil {
		return badSnapshot("invalid rules: %v", err)
	}
	flows, counters, err := g.validateSnapshotPayload(payload)
	if err != nil {
		return err
	}

	g.mu.Lock()
	g.flows = flows
	g.rules = cloneRules(payload.Rules)
	g.compiled = compiled
	g.generation = payload.Generation
	g.nextSeq = uint64(len(flows))
	g.counters = counters
	g.mu.Unlock()
	return nil
}

func (g *ConntrackPolicyGate) admitNewLocked(packet Packet, key FlowKey, now time.Time) Verdict {
	if packet.Proto == ProtoTCP && !isBareSYN(packet.Flags) {
		return g.dropVerdict(key, "", ReasonNoSYN, g.generation)
	}

	action, matched := g.matchRuleLocked(key)
	if !matched {
		return g.dropVerdict(key, "", ReasonNoRule, g.generation)
	}
	if action != ActionAllow {
		return g.dropVerdict(key, "", ReasonRuleDeny, g.generation)
	}

	if g.cfg.MaxPacketsPerFlow <= 0 {
		return g.dropVerdict(key, "", ReasonFlowQuota, g.generation)
	}
	if !g.sourceHasCapacityLocked(packet.SrcIP) {
		return g.dropVerdict(key, "", ReasonSourceLimit, g.generation)
	}
	if !g.makeTableRoomLocked() {
		return g.dropVerdict(key, "", ReasonTableFull, g.generation)
	}

	g.nextSeq++
	record := &flowRecord{
		state: ConnState{
			Key:      key,
			State:    StateNew,
			Packets:  1,
			Bytes:    packet.Length,
			LastSeen: now,
			Gen:      g.generation,
		},
		seq: g.nextSeq,
	}
	g.flows[key] = record
	g.counters.admitted++
	return Verdict{
		Action: ActionAllow,
		Reason: ReasonRuleAllow,
		State:  StateNew,
		Key:    key,
		Gen:    record.state.Gen,
	}
}

func (g *ConntrackPolicyGate) admitExistingLocked(packet Packet, key FlowKey, record *flowRecord, forward bool, now time.Time) Verdict {
	if g.cfg.MaxPacketsPerFlow <= 0 || record.state.Packets >= g.cfg.MaxPacketsPerFlow {
		return g.dropVerdict(key, record.state.State, ReasonFlowQuota, record.state.Gen)
	}

	switch packet.Proto {
	case ProtoTCP:
		return g.admitTCPLocked(packet, key, record, forward, now)
	case ProtoUDP, ProtoICMP:
		return g.admitDatagramLocked(packet, key, record, now)
	default:
		return g.dropVerdict(key, record.state.State, ReasonBadPacket, record.state.Gen)
	}
}

func (g *ConntrackPolicyGate) admitTCPLocked(packet Packet, key FlowKey, record *flowRecord, forward bool, now time.Time) Verdict {
	flags := flagSet(packet.Flags)
	if record.state.State == StateClosed {
		if flags[FlagRST] {
			return g.acceptLocked(packet, key, record, now, StateClosed, ReasonClosed)
		}
		return g.dropVerdict(key, record.state.State, ReasonBadState, record.state.Gen)
	}
	if flags[FlagFIN] || flags[FlagRST] {
		return g.acceptLocked(packet, key, record, now, StateClosed, ReasonClosed)
	}

	switch record.state.State {
	case StateNew:
		if !forward && flags[FlagSYN] && flags[FlagACK] {
			return g.acceptLocked(packet, key, record, now, StateEstablished, ReasonHandshake)
		}
		if forward && isBareSYN(packet.Flags) {
			return g.acceptLocked(packet, key, record, now, StateNew, ReasonRuleAllow)
		}
		return g.dropVerdict(key, record.state.State, ReasonBadState, record.state.Gen)
	case StateEstablished, StateRelated:
		if record.ackPending && forward && flags[FlagACK] {
			return g.acceptLocked(packet, key, record, now, StateEstablished, ReasonHandshake)
		}
		return g.acceptLocked(packet, key, record, now, "", ReasonEstablished)
	default:
		return g.dropVerdict(key, record.state.State, ReasonBadState, record.state.Gen)
	}
}

func (g *ConntrackPolicyGate) admitDatagramLocked(packet Packet, key FlowKey, record *flowRecord, now time.Time) Verdict {
	switch record.state.State {
	case StateNew:
		return g.acceptLocked(packet, key, record, now, StateEstablished, ReasonEstablished)
	case StateEstablished, StateRelated:
		return g.acceptLocked(packet, key, record, now, "", ReasonEstablished)
	case StateClosed:
		return g.dropVerdict(key, record.state.State, ReasonBadState, record.state.Gen)
	default:
		return g.dropVerdict(key, record.state.State, ReasonBadState, record.state.Gen)
	}
}

func (g *ConntrackPolicyGate) admitICMPErrorLocked(packet Packet, key FlowKey, now time.Time) Verdict {
	if packet.Related == nil {
		return g.dropVerdict(key, "", ReasonRelatedFlow, g.generation)
	}
	related, _, ok := g.findFlowLocked(*packet.Related)
	if !ok {
		return g.dropVerdict(key, "", ReasonRelatedFlow, g.generation)
	}
	if g.cfg.MaxPacketsPerFlow <= 0 || related.state.Packets >= g.cfg.MaxPacketsPerFlow {
		return g.dropVerdict(key, StateRelated, ReasonFlowQuota, related.state.Gen)
	}
	related.state.Packets++
	related.state.Bytes += packet.Length
	related.state.LastSeen = now
	g.counters.admitted++
	return Verdict{
		Action: ActionAllow,
		Reason: ReasonEstablished,
		State:  StateRelated,
		Key:    key,
		Gen:    related.state.Gen,
	}
}

func (g *ConntrackPolicyGate) acceptLocked(packet Packet, key FlowKey, record *flowRecord, now time.Time, state, reason string) Verdict {
	if state != "" {
		if state == StateClosed {
			record.ackPending = false
		} else if packet.Proto == ProtoTCP && record.state.State == StateNew && state == StateEstablished {
			record.ackPending = true
		} else if state == StateEstablished && reason == ReasonHandshake {
			record.ackPending = false
		}
		record.state.State = state
	}
	record.state.Packets++
	record.state.Bytes += packet.Length
	record.state.LastSeen = now
	g.counters.admitted++
	return Verdict{
		Action: ActionAllow,
		Reason: reason,
		State:  record.state.State,
		Key:    key,
		Gen:    record.state.Gen,
	}
}

func (g *ConntrackPolicyGate) dropVerdict(key FlowKey, state, reason string, gen int) Verdict {
	g.counters.dropped++
	return Verdict{
		Action: ActionDrop,
		Reason: reason,
		State:  state,
		Key:    key,
		Gen:    gen,
	}
}

func (g *ConntrackPolicyGate) findFlowLocked(key FlowKey) (*flowRecord, bool, bool) {
	if record, ok := g.flows[key]; ok {
		return record, true, true
	}
	if record, ok := g.flows[reverseFlowKey(key)]; ok {
		return record, false, true
	}
	return nil, false, false
}

func (g *ConntrackPolicyGate) sourceHasCapacityLocked(source string) bool {
	if g.cfg.MaxFlowsPerSource <= 0 {
		return false
	}
	used := 0
	for _, record := range g.flows {
		if record.state.Key.SrcIP == source {
			used++
		}
	}
	return used < g.cfg.MaxFlowsPerSource
}

func (g *ConntrackPolicyGate) makeTableRoomLocked() bool {
	if g.cfg.MaxFlows <= 0 {
		return false
	}
	if len(g.flows) < g.cfg.MaxFlows {
		return true
	}

	var victimKey FlowKey
	var victim *flowRecord
	found := false
	for key, record := range g.flows {
		if record.state.State == StateEstablished {
			continue
		}
		if !found ||
			record.state.LastSeen.Before(victim.state.LastSeen) ||
			(record.state.LastSeen.Equal(victim.state.LastSeen) && record.seq < victim.seq) ||
			(record.state.LastSeen.Equal(victim.state.LastSeen) && record.seq == victim.seq && flowKeyLess(key, victimKey)) {
			found = true
			victimKey = key
			victim = record
		}
	}
	if !found {
		return false
	}
	delete(g.flows, victimKey)
	g.counters.evicted++
	return true
}

func (g *ConntrackPolicyGate) matchRuleLocked(flow FlowKey) (string, bool) {
	ip := net.ParseIP(flow.SrcIP)
	for _, rule := range g.compiled {
		if rule.proto != flow.Proto {
			continue
		}
		if rule.network != nil && (ip == nil || !rule.network.Contains(ip)) {
			continue
		}
		if rule.dstPort != 0 && rule.dstPort != flow.DstPort {
			continue
		}
		return rule.action, true
	}
	return "", false
}

func (g *ConntrackPolicyGate) timeoutFor(proto string) time.Duration {
	switch proto {
	case ProtoTCP:
		return g.cfg.TCPTimeout
	case ProtoUDP:
		return g.cfg.UDPTimeout
	case ProtoICMP:
		return g.cfg.ICMPTimeout
	default:
		return 0
	}
}

func compileRules(rules []Rule) ([]compiledRule, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for i, rule := range rules {
		action := strings.ToLower(rule.Action)
		if action != ActionAllow && action != ActionDeny {
			return nil, fmt.Errorf("%w: rule %d has unknown action %q", ErrBadRule, i, rule.Action)
		}
		proto := strings.ToLower(rule.Proto)
		switch proto {
		case ProtoTCP, ProtoUDP, ProtoICMP:
		default:
			return nil, fmt.Errorf("%w: rule %d has unknown protocol %q", ErrBadRule, i, rule.Proto)
		}
		if rule.DstPort < 0 || rule.DstPort > 65535 {
			return nil, fmt.Errorf("%w: rule %d has invalid destination port %d", ErrBadRule, i, rule.DstPort)
		}
		var network *net.IPNet
		if rule.SrcCIDR != "" {
			_, parsed, err := net.ParseCIDR(rule.SrcCIDR)
			if err != nil {
				return nil, fmt.Errorf("%w: rule %d has invalid source CIDR %q", ErrBadRule, i, rule.SrcCIDR)
			}
			network = parsed
		}
		compiled = append(compiled, compiledRule{
			action:  action,
			proto:   proto,
			network: network,
			dstPort: rule.DstPort,
		})
	}
	return compiled, nil
}

func normalizePacket(packet Packet) (Packet, error) {
	packet.Proto = strings.ToLower(packet.Proto)
	switch packet.Proto {
	case ProtoTCP, ProtoUDP, ProtoICMP:
	default:
		return Packet{}, fmt.Errorf("%w: unknown protocol %q", ErrBadPacket, packet.Proto)
	}
	if net.ParseIP(packet.SrcIP) == nil {
		return Packet{}, fmt.Errorf("%w: invalid source IP %q", ErrBadPacket, packet.SrcIP)
	}
	if net.ParseIP(packet.DstIP) == nil {
		return Packet{}, fmt.Errorf("%w: invalid destination IP %q", ErrBadPacket, packet.DstIP)
	}
	if packet.SrcPort < 0 || packet.SrcPort > 65535 || packet.DstPort < 0 || packet.DstPort > 65535 {
		return Packet{}, fmt.Errorf("%w: port out of range", ErrBadPacket)
	}
	if packet.Length < 0 {
		return Packet{}, fmt.Errorf("%w: negative packet length", ErrBadPacket)
	}
	if packet.Proto == ProtoTCP {
		normalized := make([]string, len(packet.Flags))
		for i, flag := range packet.Flags {
			flag = strings.ToUpper(flag)
			switch flag {
			case FlagSYN, FlagACK, FlagFIN, FlagRST:
				normalized[i] = flag
			default:
				return Packet{}, fmt.Errorf("%w: unknown TCP flag %q", ErrBadPacket, flag)
			}
		}
		packet.Flags = normalized
	}
	if packet.Proto == ProtoICMP {
		if packet.ICMPType < 0 || packet.ICMPType > 255 || packet.ICMPCode < 0 || packet.ICMPCode > 255 {
			return Packet{}, fmt.Errorf("%w: ICMP type or code out of range", ErrBadPacket)
		}
	}
	if packet.Related != nil {
		related := *packet.Related
		packet.Related = &related
	}
	return packet, nil
}

func rawFlowKey(packet Packet) FlowKey {
	return FlowKey{
		Proto:   strings.ToLower(packet.Proto),
		SrcIP:   packet.SrcIP,
		SrcPort: packet.SrcPort,
		DstIP:   packet.DstIP,
		DstPort: packet.DstPort,
	}
}

func flowKeyOf(packet Packet) FlowKey {
	return rawFlowKey(packet)
}

func reverseFlowKey(key FlowKey) FlowKey {
	return FlowKey{
		Proto:   key.Proto,
		SrcIP:   key.DstIP,
		SrcPort: key.DstPort,
		DstIP:   key.SrcIP,
		DstPort: key.SrcPort,
	}
}

func flagSet(flags []string) map[string]bool {
	set := make(map[string]bool, len(flags))
	for _, flag := range flags {
		set[strings.ToUpper(flag)] = true
	}
	return set
}

func isBareSYN(flags []string) bool {
	set := flagSet(flags)
	return set[FlagSYN] && !set[FlagACK] && !set[FlagFIN] && !set[FlagRST]
}

func isICMPError(icmpType int) bool {
	switch icmpType {
	case 3, 4, 5, 11, 12:
		return true
	default:
		return false
	}
}

func cloneRules(rules []Rule) []Rule {
	if rules == nil {
		return nil
	}
	copied := make([]Rule, len(rules))
	copy(copied, rules)
	return copied
}

func flowKeyLess(a, b FlowKey) bool {
	if a.Proto != b.Proto {
		return a.Proto < b.Proto
	}
	if a.SrcIP != b.SrcIP {
		return a.SrcIP < b.SrcIP
	}
	if a.SrcPort != b.SrcPort {
		return a.SrcPort < b.SrcPort
	}
	if a.DstIP != b.DstIP {
		return a.DstIP < b.DstIP
	}
	return a.DstPort < b.DstPort
}

func decodeSnapshot(data []byte) (snapshotPayload, error) {
	if len(data) == 0 {
		return snapshotPayload{}, badSnapshot("empty payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var payload snapshotPayload
	if err := decoder.Decode(&payload); err != nil {
		return snapshotPayload{}, badSnapshot("decode: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return snapshotPayload{}, badSnapshot("trailing JSON value")
		}
		return snapshotPayload{}, badSnapshot("trailing data: %v", err)
	}
	if payload.Version != snapshotVersion {
		return snapshotPayload{}, badSnapshot("unsupported version %d", payload.Version)
	}
	if payload.Generation < 0 {
		return snapshotPayload{}, badSnapshot("negative generation")
	}
	if payload.Generation == 0 && len(payload.Rules) != 0 {
		return snapshotPayload{}, badSnapshot("rules exist without a loaded generation")
	}
	if payload.Stats.Admitted < 0 || payload.Stats.Dropped < 0 || payload.Stats.Expired < 0 || payload.Stats.Evicted < 0 || payload.Stats.Flows < 0 {
		return snapshotPayload{}, badSnapshot("negative counter")
	}
	if payload.Stats.Flows != len(payload.Entries) {
		return snapshotPayload{}, badSnapshot("flow count does not match entries")
	}
	return payload, nil
}

func (g *ConntrackPolicyGate) validateSnapshotPayload(payload snapshotPayload) (map[FlowKey]*flowRecord, gateCounters, error) {
	if g.cfg.MaxFlows <= 0 && len(payload.Entries) != 0 {
		return nil, gateCounters{}, badSnapshot("entry count exceeds table limit")
	}
	if g.cfg.MaxFlows > 0 && len(payload.Entries) > g.cfg.MaxFlows {
		return nil, gateCounters{}, badSnapshot("entry count exceeds table limit")
	}

	flows := make(map[FlowKey]*flowRecord, len(payload.Entries))
	sourceCounts := make(map[string]int)
	for i, entry := range payload.Entries {
		if _, ok := flows[entry.Key]; ok {
			return nil, gateCounters{}, badSnapshot("duplicate flow key at entry %d", i)
		}
		if err := validateFlowKey(entry.Key); err != nil {
			return nil, gateCounters{}, badSnapshot("entry %d: %v", i, err)
		}
		if !validConnState(entry.State) {
			return nil, gateCounters{}, badSnapshot("entry %d has invalid state %q", i, entry.State)
		}
		if entry.Packets <= 0 {
			return nil, gateCounters{}, badSnapshot("entry %d has invalid packet count", i)
		}
		if entry.Bytes < 0 {
			return nil, gateCounters{}, badSnapshot("entry %d has invalid byte count", i)
		}
		if entry.Gen <= 0 || entry.Gen > payload.Generation {
			return nil, gateCounters{}, badSnapshot("entry %d has invalid generation", i)
		}
		if g.cfg.MaxPacketsPerFlow <= 0 || entry.Packets > g.cfg.MaxPacketsPerFlow {
			return nil, gateCounters{}, badSnapshot("entry %d exceeds packet quota", i)
		}
		sourceCounts[entry.Key.SrcIP]++
		if g.cfg.MaxFlowsPerSource <= 0 || sourceCounts[entry.Key.SrcIP] > g.cfg.MaxFlowsPerSource {
			return nil, gateCounters{}, badSnapshot("entry %d exceeds source flow limit", i)
		}
		flows[entry.Key] = &flowRecord{
			state: entry,
			seq:   uint64(i + 1),
		}
	}
	seenPending := make(map[FlowKey]bool, len(payload.PendingAcks))
	for i, key := range payload.PendingAcks {
		if seenPending[key] {
			return nil, gateCounters{}, badSnapshot("duplicate pending ACK key at entry %d", i)
		}
		seenPending[key] = true
		record, ok := flows[key]
		if !ok || record.state.State != StateEstablished || record.state.Key.Proto != ProtoTCP {
			return nil, gateCounters{}, badSnapshot("pending ACK entry %d does not name an established TCP flow", i)
		}
		record.ackPending = true
	}
	if payload.FlowOrder != nil {
		if len(payload.FlowOrder) != len(flows) {
			return nil, gateCounters{}, badSnapshot("flow order does not match entries")
		}
		seenOrder := make(map[FlowKey]bool, len(payload.FlowOrder))
		for i, key := range payload.FlowOrder {
			record, ok := flows[key]
			if !ok || seenOrder[key] {
				return nil, gateCounters{}, badSnapshot("invalid flow order at entry %d", i)
			}
			seenOrder[key] = true
			record.seq = uint64(i + 1)
		}
	}

	counters := gateCounters{
		admitted: payload.Stats.Admitted,
		dropped:  payload.Stats.Dropped,
		expired:  payload.Stats.Expired,
		evicted:  payload.Stats.Evicted,
	}
	return flows, counters, nil
}

func validateFlowKey(key FlowKey) error {
	switch key.Proto {
	case ProtoTCP, ProtoUDP, ProtoICMP:
	default:
		return fmt.Errorf("unknown protocol %q", key.Proto)
	}
	if net.ParseIP(key.SrcIP) == nil || net.ParseIP(key.DstIP) == nil {
		return fmt.Errorf("invalid IP address")
	}
	if key.SrcPort < 0 || key.SrcPort > 65535 || key.DstPort < 0 || key.DstPort > 65535 {
		return fmt.Errorf("port out of range")
	}
	return nil
}

func validConnState(state string) bool {
	switch state {
	case StateNew, StateEstablished, StateRelated, StateClosed:
		return true
	default:
		return false
	}
}

func badSnapshot(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadSnapshot, fmt.Sprintf(format, args...))
}
