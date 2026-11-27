// Package task keeps the DHCP snooping binding table of an access gateway.
package task

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// DHCP message roles understood by the guard.
const (
	MsgDiscover = "discover"
	MsgOffer    = "offer"
	MsgRequest  = "request"
	MsgAck      = "ack"
	MsgNak      = "nak"
	MsgDecline  = "decline"
	MsgRelease  = "release"
)

// Verdict actions.
const (
	ActionAllow = "allow"
	ActionDrop  = "drop"
)

// Reason codes attached to a verdict and to the audit log.
const (
	ReasonTrustedServer   = "trusted_server"
	ReasonTrustedPort     = "trusted_port"
	ReasonClientBound     = "client_bound"
	ReasonUntrustedServer = "untrusted_server"
	ReasonUnknownMessage  = "unknown_message"
	ReasonRequestMismatch = "request_mismatch"
	ReasonUnknownBinding  = "unknown_binding"
	ReasonDeclined        = "declined"
	ReasonReleased        = "released"
	ReasonCapacityEvict   = "capacity_evict"
	ReasonPortLimitEvict  = "port_limit_evict"
)

// Errors returned by the guard.
var (
	ErrNoBinding      = errors.New("no binding for this mac and vlan")
	ErrSourcePort     = errors.New("source port does not match the binding")
	ErrSourceIP       = errors.New("source address does not match the binding")
	ErrBindingExpired = errors.New("binding lease has expired")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

const (
	defaultLeaseTTL     = time.Hour
	defaultConflictHold = 30 * time.Second
	snapshotVersion     = 1
)

// SnoopConfig bounds the binding table and the audit log.
type SnoopConfig struct {
	TrustedPorts   []string
	MaxBindings    int
	MaxMACsPerPort int
	LeaseTTL       time.Duration
	ConflictHold   time.Duration
	MaxAudit       int
}

// DhcpMessage is one frame observed on an access port.
type DhcpMessage struct {
	Type        string
	MAC         string
	VLAN        int
	RequestedIP netip.Addr
	ServerID    netip.Addr
	ClientIP    netip.Addr
	Lease       time.Duration
	XID         uint32
}

// Binding is the per (mac, vlan) lease record.
type Binding struct {
	MAC        string     `json:"mac"`
	VLAN       int        `json:"vlan"`
	Port       string     `json:"port"`
	IP         netip.Addr `json:"ip"`
	ServerID   netip.Addr `json:"server_id"`
	LeaseUntil time.Time  `json:"lease_until"`
	Seq        int64      `json:"seq"`
	Active     bool       `json:"active"`
}

// Conflict records an address held in quarantine after a DECLINE.
type Conflict struct {
	IP    netip.Addr `json:"ip"`
	MAC   string     `json:"mac"`
	VLAN  int        `json:"vlan"`
	Until time.Time  `json:"until"`
}

// AuditEvent is one decision appended to the audit log.
type AuditEvent struct {
	Seq    int64     `json:"seq"`
	Port   string    `json:"port"`
	MAC    string    `json:"mac"`
	Action string    `json:"action"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// SnoopStats exposes the counters of the guard.
type SnoopStats struct {
	Bindings  int `json:"bindings"`
	Conflicts int `json:"conflicts"`
	Dropped   int `json:"dropped"`
	Expired   int `json:"expired"`
	Alerts    int `json:"alerts"`
}

// Verdict is the outcome of one observed message.
type Verdict struct {
	Action   string  `json:"action"`
	Reason   string  `json:"reason"`
	Binding  Binding `json:"binding"`
	Conflict bool    `json:"conflict"`
	Seq      int64   `json:"seq"`
}

type bindingKey struct {
	mac  string
	vlan int
}

type conflictKey struct {
	ip   netip.Addr
	vlan int
}

type bindingRecord struct {
	binding  Binding
	lastSeen time.Time
}

// DhcpSnoopingGuard owns the binding table, the conflict list and the audit log.
type DhcpSnoopingGuard struct {
	mu sync.Mutex

	cfg          SnoopConfig
	bindings     map[bindingKey]bindingRecord
	conflicts    map[conflictKey]Conflict
	audit        []AuditEvent
	trustedPorts map[string]bool

	nextBindingSeq int64
	nextAuditSeq   int64
	dropped        int
	expired        int
	alerts         int
}

// NewDhcpSnoopingGuard builds a guard for one access switch.
func NewDhcpSnoopingGuard(cfg SnoopConfig) *DhcpSnoopingGuard {
	cfg.TrustedPorts = append([]string(nil), cfg.TrustedPorts...)
	g := &DhcpSnoopingGuard{
		cfg:            cfg,
		bindings:       make(map[bindingKey]bindingRecord),
		conflicts:      make(map[conflictKey]Conflict),
		trustedPorts:   make(map[string]bool),
		nextBindingSeq: 1,
		nextAuditSeq:   1,
	}
	for _, port := range cfg.TrustedPorts {
		g.trustedPorts[port] = true
	}
	return g
}

// Ingest admits one message arriving on a port and updates the binding table.
func (g *DhcpSnoopingGuard) Ingest(port string, trusted bool, msg DhcpMessage) (Verdict, error) {
	mac := normalizeMAC(msg.MAC)
	msgType := strings.ToLower(msg.Type)

	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()

	trustedPort := trusted || g.trustedPorts[port]

	switch msgType {
	case MsgOffer, MsgAck, MsgNak:
		if !trustedPort {
			g.dropped++
			g.alerts++
			return g.recordVerdictLocked(port, mac, ActionDrop, ReasonUntrustedServer, now, Verdict{}, false), nil
		}

		if msgType == MsgOffer {
			ip := messageAddress(msg)
			if g.conflictActiveLocked(ip, msg.VLAN, now) {
				g.dropped++
				return g.recordVerdictLocked(port, mac, ActionDrop, ReasonDeclined, now, Verdict{}, true), nil
			}
		}
		return g.recordVerdictLocked(port, mac, ActionAllow, ReasonTrustedServer, now, Verdict{}, false), nil

	case MsgDiscover, MsgRequest, MsgDecline, MsgRelease:
		if trustedPort {
			return g.recordVerdictLocked(port, mac, ActionAllow, ReasonTrustedPort, now, Verdict{}, false), nil
		}
	}

	switch msgType {
	case MsgDiscover:
		return g.ingestDiscoverLocked(port, mac, msg, now), nil
	case MsgRequest:
		return g.ingestRequestLocked(port, mac, msg, now), nil
	case MsgDecline:
		return g.ingestDeclineLocked(port, mac, msg, now), nil
	case MsgRelease:
		return g.ingestReleaseLocked(port, mac, msg, now), nil
	default:
		g.dropped++
		return g.recordVerdictLocked(port, mac, ActionDrop, ReasonUnknownMessage, now, Verdict{}, false), nil
	}
}

func (g *DhcpSnoopingGuard) ingestDiscoverLocked(port, mac string, msg DhcpMessage, now time.Time) Verdict {
	key := bindingKey{mac: mac, vlan: msg.VLAN}
	_, exists := g.bindings[key]

	if exists && g.bindings[key].binding.Port != port {
		delete(g.bindings, key)
		exists = false
	}

	if !exists {
		g.makeRoomLocked(port, mac, now)
	}
	record := bindingRecord{
		binding: Binding{
			MAC:    mac,
			VLAN:   msg.VLAN,
			Port:   port,
			Seq:    g.takeBindingSeqLocked(),
			Active: true,
		},
		lastSeen: now,
	}
	g.bindings[key] = record

	verdict := Verdict{Binding: record.binding}
	return g.recordVerdictLocked(port, mac, ActionAllow, ReasonClientBound, now, verdict, false)
}

func (g *DhcpSnoopingGuard) ingestRequestLocked(port, mac string, msg DhcpMessage, now time.Time) Verdict {
	key := bindingKey{mac: mac, vlan: msg.VLAN}
	record, exists := g.bindings[key]
	if !exists || !record.binding.Active {
		g.dropped++
		return g.recordVerdictLocked(port, mac, ActionDrop, ReasonUnknownBinding, now, Verdict{}, false)
	}

	requestedIP := messageAddress(msg)
	if g.conflictActiveLocked(requestedIP, msg.VLAN, now) {
		g.dropped++
		return g.recordVerdictLocked(port, mac, ActionDrop, ReasonDeclined, now, Verdict{Binding: record.binding}, true)
	}
	if !requestedIP.IsValid() {
		g.dropped++
		return g.recordVerdictLocked(port, mac, ActionDrop, ReasonRequestMismatch, now, Verdict{Binding: record.binding}, false)
	}

	if record.binding.Port != port {
		// A request from a different port starts a new binding. The old
		// binding is removed before the new one is admitted so there is no
		// interval in which both ports are usable for this key.
		delete(g.bindings, key)
		g.makeRoomLocked(port, mac, now)
		record = bindingRecord{
			binding: Binding{
				MAC:      mac,
				VLAN:     msg.VLAN,
				Port:     port,
				IP:       requestedIP,
				ServerID: normalizeAddr(msg.ServerID),
				Seq:      g.takeBindingSeqLocked(),
				Active:   true,
			},
			lastSeen: now,
		}
		record.binding.LeaseUntil = now.Add(g.leaseTTL())
		g.bindings[key] = record
		verdict := Verdict{Binding: record.binding}
		return g.recordVerdictLocked(port, mac, ActionAllow, ReasonClientBound, now, verdict, false)
	}

	if !record.binding.IP.IsValid() {
		record.binding.IP = requestedIP
		record.binding.ServerID = normalizeAddr(msg.ServerID)
	} else if !sameAddr(record.binding.IP, requestedIP) || !sameAddr(record.binding.ServerID, normalizeAddr(msg.ServerID)) {
		g.dropped++
		return g.recordVerdictLocked(port, mac, ActionDrop, ReasonRequestMismatch, now, Verdict{Binding: record.binding}, false)
	}

	record.binding.Port = port
	record.binding.Active = true
	record.binding.LeaseUntil = now.Add(g.leaseTTL())
	record.binding.Seq = g.takeBindingSeqLocked()
	record.lastSeen = now
	g.bindings[key] = record

	verdict := Verdict{Binding: record.binding}
	return g.recordVerdictLocked(port, mac, ActionAllow, ReasonClientBound, now, verdict, false)
}

func (g *DhcpSnoopingGuard) ingestDeclineLocked(port, mac string, msg DhcpMessage, now time.Time) Verdict {
	key := bindingKey{mac: mac, vlan: msg.VLAN}
	record, exists := g.bindings[key]
	ip := messageAddress(msg)
	if !ip.IsValid() && exists {
		ip = record.binding.IP
	}

	if exists {
		delete(g.bindings, key)
	}

	conflict := false
	if ip.IsValid() {
		ip = normalizeAddr(ip)
		g.conflicts[conflictKey{ip: ip, vlan: msg.VLAN}] = Conflict{
			IP:    ip,
			MAC:   mac,
			VLAN:  msg.VLAN,
			Until: now.Add(g.conflictHold()),
		}
		conflict = true
	}

	return g.recordVerdictLocked(port, mac, ActionAllow, ReasonDeclined, now, Verdict{Conflict: conflict}, conflict)
}

func (g *DhcpSnoopingGuard) ingestReleaseLocked(port, mac string, msg DhcpMessage, now time.Time) Verdict {
	delete(g.bindings, bindingKey{mac: mac, vlan: msg.VLAN})
	return g.recordVerdictLocked(port, mac, ActionAllow, ReasonReleased, now, Verdict{}, false)
}

func (g *DhcpSnoopingGuard) recordVerdictLocked(port, mac, action, reason string, now time.Time, verdict Verdict, conflict bool) Verdict {
	verdict.Action = action
	verdict.Reason = reason
	verdict.Conflict = conflict
	verdict.Seq = g.appendAuditLocked(port, mac, action, reason, now)
	return verdict
}

func (g *DhcpSnoopingGuard) appendAuditLocked(port, mac, action, reason string, now time.Time) int64 {
	seq := g.nextAuditSeq
	g.nextAuditSeq++
	g.audit = append(g.audit, AuditEvent{
		Seq:    seq,
		Port:   port,
		MAC:    mac,
		Action: action,
		Reason: reason,
		At:     now,
	})
	if g.cfg.MaxAudit > 0 && len(g.audit) > g.cfg.MaxAudit {
		g.audit = append([]AuditEvent(nil), g.audit[len(g.audit)-g.cfg.MaxAudit:]...)
	}
	return seq
}

func (g *DhcpSnoopingGuard) makeRoomLocked(port, mac string, now time.Time) {
	for g.cfg.MaxBindings > 0 && len(g.bindings) >= g.cfg.MaxBindings {
		key, record, ok := g.leastRecentBindingLocked("")
		if !ok {
			break
		}
		delete(g.bindings, key)
		g.appendAuditLocked(record.binding.Port, record.binding.MAC, ActionDrop, ReasonCapacityEvict, now)
	}

	if g.cfg.MaxMACsPerPort <= 0 || g.portHasMACLocked(port, mac) {
		return
	}
	for g.uniqueMACsLocked(port) >= g.cfg.MaxMACsPerPort {
		victimMAC, ok := g.leastRecentMACLocked(port)
		if !ok {
			break
		}
		for key, record := range g.bindings {
			if record.binding.Port == port && record.binding.MAC == victimMAC {
				delete(g.bindings, key)
				g.appendAuditLocked(record.binding.Port, record.binding.MAC, ActionDrop, ReasonPortLimitEvict, now)
			}
		}
	}
}

func (g *DhcpSnoopingGuard) leastRecentBindingLocked(port string) (bindingKey, bindingRecord, bool) {
	var bestKey bindingKey
	var best bindingRecord
	found := false
	for key, record := range g.bindings {
		if port != "" && record.binding.Port != port {
			continue
		}
		if !found || lessActive(record, best) || (sameActivity(record, best) && lessBindingKey(key, bestKey)) {
			bestKey, best, found = key, record, true
		}
	}
	return bestKey, best, found
}

func (g *DhcpSnoopingGuard) leastRecentMACLocked(port string) (string, bool) {
	latest := make(map[string]bindingRecord)
	for _, record := range g.bindings {
		if record.binding.Port != port {
			continue
		}
		previous, ok := latest[record.binding.MAC]
		if !ok || lessActive(previous, record) {
			latest[record.binding.MAC] = record
		}
	}

	victim := ""
	var victimRecord bindingRecord
	for mac, record := range latest {
		if victim == "" || lessActive(record, victimRecord) || (sameActivity(record, victimRecord) && mac < victim) {
			victim, victimRecord = mac, record
		}
	}
	return victim, victim != ""
}

func lessActive(left, right bindingRecord) bool {
	if left.lastSeen.Equal(right.lastSeen) {
		return left.binding.Seq < right.binding.Seq
	}
	return left.lastSeen.Before(right.lastSeen)
}

func sameActivity(left, right bindingRecord) bool {
	return left.lastSeen.Equal(right.lastSeen) && left.binding.Seq == right.binding.Seq
}

func lessBindingKey(left, right bindingKey) bool {
	if left.mac != right.mac {
		return left.mac < right.mac
	}
	return left.vlan < right.vlan
}

func (g *DhcpSnoopingGuard) portHasMACLocked(port, mac string) bool {
	for _, record := range g.bindings {
		if record.binding.Port == port && record.binding.MAC == mac {
			return true
		}
	}
	return false
}

func (g *DhcpSnoopingGuard) uniqueMACsLocked(port string) int {
	macs := make(map[string]struct{})
	for _, record := range g.bindings {
		if record.binding.Port == port {
			macs[record.binding.MAC] = struct{}{}
		}
	}
	return len(macs)
}

func (g *DhcpSnoopingGuard) conflictActiveLocked(ip netip.Addr, vlan int, now time.Time) bool {
	if !ip.IsValid() {
		return false
	}
	key := conflictKey{ip: normalizeAddr(ip), vlan: vlan}
	conflict, ok := g.conflicts[key]
	if !ok {
		return false
	}
	if !conflict.Until.After(now) {
		delete(g.conflicts, key)
		return false
	}
	return true
}

func (g *DhcpSnoopingGuard) takeBindingSeqLocked() int64 {
	seq := g.nextBindingSeq
	g.nextBindingSeq++
	return seq
}

func (g *DhcpSnoopingGuard) leaseTTL() time.Duration {
	if g.cfg.LeaseTTL > 0 {
		return g.cfg.LeaseTTL
	}
	return defaultLeaseTTL
}

func (g *DhcpSnoopingGuard) conflictHold() time.Duration {
	if g.cfg.ConflictHold > 0 {
		return g.cfg.ConflictHold
	}
	return defaultConflictHold
}

// Binding reads the live record for one mac and vlan.
func (g *DhcpSnoopingGuard) Binding(mac string, vlan int) (Binding, bool) {
	key := bindingKey{mac: normalizeMAC(mac), vlan: vlan}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	record, ok := g.bindings[key]
	if !ok || !record.binding.Active || bindingExpired(record.binding, now) {
		return Binding{}, false
	}
	return record.binding, true
}

// CheckSource validates a data plane packet against the binding table.
func (g *DhcpSnoopingGuard) CheckSource(port, mac string, ip netip.Addr) error {
	mac = normalizeMAC(mac)

	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()

	found := false
	portMatched := false
	livePortMatched := false
	expiredPortMatched := false
	for key, record := range g.bindings {
		if key.mac != mac || !record.binding.Active {
			continue
		}
		found = true
		if record.binding.Port != port {
			continue
		}
		portMatched = true
		if bindingExpired(record.binding, now) {
			expiredPortMatched = true
			continue
		}
		livePortMatched = true
		if record.binding.IP.IsValid() && sameAddr(record.binding.IP, ip) {
			return nil
		}
	}

	if !found {
		return ErrNoBinding
	}
	if !portMatched {
		return ErrSourcePort
	}
	if !livePortMatched && expiredPortMatched {
		return ErrBindingExpired
	}
	return ErrSourceIP
}

// Expire drops leases and conflicts that ended before now and reports how many
// bindings were removed.
func (g *DhcpSnoopingGuard) Expire(now time.Time) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	removed := 0
	for key, record := range g.bindings {
		if bindingExpired(record.binding, now) {
			delete(g.bindings, key)
			removed++
		}
	}
	for key, conflict := range g.conflicts {
		if !conflict.Until.After(now) {
			delete(g.conflicts, key)
		}
	}
	g.expired += removed
	return removed
}

// PortBindings lists the live bindings of one port ordered by mac.
func (g *DhcpSnoopingGuard) PortBindings(port string) []Binding {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()

	bindings := make([]Binding, 0)
	for _, record := range g.bindings {
		if record.binding.Port != port || !record.binding.Active || bindingExpired(record.binding, now) {
			continue
		}
		bindings = append(bindings, record.binding)
	}
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].MAC != bindings[j].MAC {
			return bindings[i].MAC < bindings[j].MAC
		}
		return bindings[i].VLAN < bindings[j].VLAN
	})
	return bindings
}

// Conflicts lists the addresses still in quarantine, oldest deadline first.
func (g *DhcpSnoopingGuard) Conflicts() []Conflict {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()

	conflicts := make([]Conflict, 0)
	for _, conflict := range g.conflicts {
		if conflict.Until.After(now) {
			conflicts = append(conflicts, conflict)
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if !conflicts[i].Until.Equal(conflicts[j].Until) {
			return conflicts[i].Until.Before(conflicts[j].Until)
		}
		if conflicts[i].IP != conflicts[j].IP {
			return conflicts[i].IP.Compare(conflicts[j].IP) < 0
		}
		return conflicts[i].VLAN < conflicts[j].VLAN
	})
	return conflicts
}

// Audit returns a copy of the audit log, oldest first.
func (g *DhcpSnoopingGuard) Audit() []AuditEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]AuditEvent(nil), g.audit...)
}

// Stats reports the current counters.
func (g *DhcpSnoopingGuard) Stats() SnoopStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()

	bindings := 0
	for _, record := range g.bindings {
		if record.binding.Active && !bindingExpired(record.binding, now) {
			bindings++
		}
	}
	conflicts := 0
	for _, conflict := range g.conflicts {
		if conflict.Until.After(now) {
			conflicts++
		}
	}
	return SnoopStats{
		Bindings:  bindings,
		Conflicts: conflicts,
		Dropped:   g.dropped,
		Expired:   g.expired,
		Alerts:    g.alerts,
	}
}

// SetPortTrusted changes the trust role of a port at runtime.
func (g *DhcpSnoopingGuard) SetPortTrusted(port string, trusted bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if trusted {
		g.trustedPorts[port] = true
		return
	}
	delete(g.trustedPorts, port)
}

type snapshotBinding struct {
	Binding
	LastSeen time.Time `json:"last_seen"`
}

type snapshotState struct {
	Bindings       []snapshotBinding `json:"bindings"`
	Conflicts      []Conflict        `json:"conflicts"`
	Audit          []AuditEvent      `json:"audit"`
	TrustedPorts   []string          `json:"trusted_ports"`
	NextBindingSeq int64             `json:"next_binding_seq"`
	NextAuditSeq   int64             `json:"next_audit_seq"`
	Dropped        int               `json:"dropped"`
	Expired        int               `json:"expired"`
	Alerts         int               `json:"alerts"`
}

type snapshotEnvelope struct {
	Version  int    `json:"version"`
	Checksum string `json:"checksum"`
	State    string `json:"state"`
}

// Snapshot serialises the whole binding table and audit log.
func (g *DhcpSnoopingGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	state := snapshotState{
		Bindings:       make([]snapshotBinding, 0, len(g.bindings)),
		Conflicts:      make([]Conflict, 0, len(g.conflicts)),
		Audit:          append([]AuditEvent(nil), g.audit...),
		TrustedPorts:   make([]string, 0, len(g.trustedPorts)),
		NextBindingSeq: g.nextBindingSeq,
		NextAuditSeq:   g.nextAuditSeq,
		Dropped:        g.dropped,
		Expired:        g.expired,
		Alerts:         g.alerts,
	}
	for _, record := range g.bindings {
		state.Bindings = append(state.Bindings, snapshotBinding{Binding: record.binding, LastSeen: record.lastSeen})
	}
	sort.Slice(state.Bindings, func(i, j int) bool {
		if state.Bindings[i].MAC != state.Bindings[j].MAC {
			return state.Bindings[i].MAC < state.Bindings[j].MAC
		}
		return state.Bindings[i].VLAN < state.Bindings[j].VLAN
	})
	for _, conflict := range g.conflicts {
		state.Conflicts = append(state.Conflicts, conflict)
	}
	sort.Slice(state.Conflicts, func(i, j int) bool {
		if state.Conflicts[i].VLAN != state.Conflicts[j].VLAN {
			return state.Conflicts[i].VLAN < state.Conflicts[j].VLAN
		}
		return state.Conflicts[i].IP.Compare(state.Conflicts[j].IP) < 0
	})
	for port := range g.trustedPorts {
		state.TrustedPorts = append(state.TrustedPorts, port)
	}
	sort.Strings(state.TrustedPorts)

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot state: %w", err)
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		State:    base64.StdEncoding.EncodeToString(payload),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot envelope: %w", err)
	}
	return data, nil
}

// Restore replaces the state with a snapshot, all or nothing.
func (g *DhcpSnoopingGuard) Restore(data []byte) error {
	state, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	bindings := make(map[bindingKey]bindingRecord, len(state.Bindings))
	for _, item := range state.Bindings {
		key := bindingKey{mac: item.MAC, vlan: item.VLAN}
		bindings[key] = bindingRecord{binding: item.Binding, lastSeen: item.LastSeen}
	}
	conflicts := make(map[conflictKey]Conflict, len(state.Conflicts))
	for _, conflict := range state.Conflicts {
		conflicts[conflictKey{ip: conflict.IP, vlan: conflict.VLAN}] = conflict
	}
	trustedPorts := make(map[string]bool, len(state.TrustedPorts))
	for _, port := range state.TrustedPorts {
		trustedPorts[port] = true
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.bindings = bindings
	g.conflicts = conflicts
	g.audit = append([]AuditEvent(nil), state.Audit...)
	g.trustedPorts = trustedPorts
	g.nextBindingSeq = state.NextBindingSeq
	g.nextAuditSeq = state.NextAuditSeq
	g.dropped = state.Dropped
	g.expired = state.Expired
	g.alerts = state.Alerts
	return nil
}

func decodeSnapshot(data []byte) (snapshotState, error) {
	var empty snapshotState
	var envelope snapshotEnvelope
	if err := decodeJSONStrict(data, &envelope); err != nil {
		return empty, badSnapshot(err)
	}
	if envelope.Version != snapshotVersion || envelope.Checksum == "" || envelope.State == "" {
		return empty, badSnapshot(errors.New("snapshot envelope is incomplete"))
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(envelope.State)
	if err != nil {
		return empty, badSnapshot(err)
	}
	sum := sha256.Sum256(payload)
	expected, err := hex.DecodeString(envelope.Checksum)
	if err != nil || len(expected) != len(sum) {
		return empty, badSnapshot(errors.New("snapshot checksum is invalid"))
	}
	if subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return empty, badSnapshot(errors.New("snapshot checksum mismatch"))
	}

	var state snapshotState
	if err := decodeJSONStrict(payload, &state); err != nil {
		return empty, badSnapshot(err)
	}
	if err := validateSnapshotState(&state); err != nil {
		return empty, badSnapshot(err)
	}
	return state, nil
}

func validateSnapshotState(state *snapshotState) error {
	if state.NextBindingSeq <= 0 || state.NextAuditSeq <= 0 {
		return errors.New("snapshot sequence bounds are invalid")
	}
	if state.Dropped < 0 || state.Expired < 0 || state.Alerts < 0 {
		return errors.New("snapshot counters are invalid")
	}

	keys := make(map[bindingKey]struct{}, len(state.Bindings))
	var maxBindingSeq int64
	for index := range state.Bindings {
		item := &state.Bindings[index]
		item.MAC = normalizeMAC(item.MAC)
		if item.Seq <= 0 || !item.Active {
			return errors.New("snapshot binding is invalid")
		}
		key := bindingKey{mac: item.MAC, vlan: item.VLAN}
		if _, exists := keys[key]; exists {
			return errors.New("snapshot contains duplicate bindings")
		}
		keys[key] = struct{}{}
		if item.Seq > maxBindingSeq {
			maxBindingSeq = item.Seq
		}
		if item.LastSeen.IsZero() {
			item.LastSeen = item.LeaseUntil
		}
	}
	if state.NextBindingSeq <= maxBindingSeq {
		return errors.New("snapshot binding sequence would move backwards")
	}

	conflictKeys := make(map[conflictKey]struct{}, len(state.Conflicts))
	for index := range state.Conflicts {
		conflict := &state.Conflicts[index]
		conflict.MAC = normalizeMAC(conflict.MAC)
		conflict.IP = normalizeAddr(conflict.IP)
		if !conflict.IP.IsValid() || conflict.Until.IsZero() {
			return errors.New("snapshot conflict is invalid")
		}
		key := conflictKey{ip: conflict.IP, vlan: conflict.VLAN}
		if _, exists := conflictKeys[key]; exists {
			return errors.New("snapshot contains duplicate conflicts")
		}
		conflictKeys[key] = struct{}{}
	}

	var previousAuditSeq int64
	for index := range state.Audit {
		event := &state.Audit[index]
		event.MAC = normalizeMAC(event.MAC)
		if event.Seq <= previousAuditSeq || event.Seq <= 0 || event.At.IsZero() {
			return errors.New("snapshot audit sequence is invalid")
		}
		if event.Action != ActionAllow && event.Action != ActionDrop {
			return errors.New("snapshot audit action is invalid")
		}
		previousAuditSeq = event.Seq
	}
	if state.NextAuditSeq <= previousAuditSeq {
		return errors.New("snapshot audit sequence would move backwards")
	}

	trusted := make(map[string]struct{}, len(state.TrustedPorts))
	for _, port := range state.TrustedPorts {
		if _, exists := trusted[port]; exists {
			return errors.New("snapshot contains duplicate trusted ports")
		}
		trusted[port] = struct{}{}
	}
	return nil
}

func decodeJSONStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("snapshot has trailing JSON")
		}
		return err
	}
	return nil
}

func badSnapshot(err error) error {
	return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
}

func bindingExpired(binding Binding, now time.Time) bool {
	return !binding.LeaseUntil.IsZero() && !binding.LeaseUntil.After(now)
}

func normalizeMAC(mac string) string {
	return strings.ToLower(strings.TrimSpace(mac))
}

func normalizeAddr(addr netip.Addr) netip.Addr {
	if !addr.IsValid() {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func sameAddr(left, right netip.Addr) bool {
	if !left.IsValid() || !right.IsValid() {
		return !left.IsValid() && !right.IsValid()
	}
	return left.Unmap() == right.Unmap()
}

func messageAddress(msg DhcpMessage) netip.Addr {
	if msg.RequestedIP.IsValid() {
		return normalizeAddr(msg.RequestedIP)
	}
	return normalizeAddr(msg.ClientIP)
}
