// Package task keeps a node firewall connection tracking table and decides
// per packet whether a flow may pass, expire or be evicted.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// ConntrackPolicyGate owns the tracking table, the rule generation and the
// counters.
type ConntrackPolicyGate struct {
	cfg GateConfig
}

// NewConntrackPolicyGate builds a gate with the given configuration.
func NewConntrackPolicyGate(cfg GateConfig) *ConntrackPolicyGate {
	return &ConntrackPolicyGate{cfg: cfg}
}

// Admit grades one packet and updates the tracking table.
func (g *ConntrackPolicyGate) Admit(pkt Packet) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Lookup reads one table entry by its exact key.
func (g *ConntrackPolicyGate) Lookup(flow FlowKey) (ConnState, bool) {
	return ConnState{}, false
}

// Expire removes every entry idle past its protocol timeout.
func (g *ConntrackPolicyGate) Expire(now time.Time) int {
	return 0
}

// Reload replaces the whole rule generation atomically.
func (g *ConntrackPolicyGate) Reload(rules []Rule) error {
	return errNotImplemented
}

// Delete removes one entry by its exact key.
func (g *ConntrackPolicyGate) Delete(flow FlowKey) bool {
	return false
}

// FlowCount reports how many entries the table holds.
func (g *ConntrackPolicyGate) FlowCount() int {
	return 0
}

// Rules returns a copy of the active rule generation.
func (g *ConntrackPolicyGate) Rules() []Rule {
	return nil
}

// Stats returns the live counters.
func (g *ConntrackPolicyGate) Stats() Stats {
	return Stats{}
}

// Snapshot serialises table entries, rules, generation and counters.
func (g *ConntrackPolicyGate) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *ConntrackPolicyGate) Restore(data []byte) error {
	return errNotImplemented
}
