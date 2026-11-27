// Package task keeps the OSPFv2 neighbour table and the link state database:
// LSA instance comparison, aging, flooding decisions and DR election.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Neighbour states reported by Hello and Neighbors.
const (
	NeighborDown   = "down"
	NeighborInit   = "init"
	NeighborTwoWay = "two_way"
	NeighborFull   = "full"
)

// LSAStatus is the instance comparison result returned by RecvLSA.
type LSAStatus string

// LSA instance comparison results.
const (
	StatusInstalled = "installed"
	StatusNewer     = "newer"
	StatusDuplicate = "duplicate"
	StatusOlder     = "older"
	StatusMaxAge    = "max_age"
)

// Event kinds appended by Age.
const (
	EventNeighborDown = "neighbor_down"
	EventLSAMaxAge    = "lsa_max_age"
)

// Sequence anchors of the LSA instance space.
const (
	InitialSequence  = 0x80000001
	ReservedSequence = 0x80000000
	MaxSequence      = 0x7FFFFFFF
)

// Errors returned by the guard.
var (
	ErrBlankInterface    = errors.New("interface name is required")
	ErrInterfaceExists   = errors.New("interface already registered")
	ErrInterfaceNotFound = errors.New("interface not registered")
	ErrBlankRouterID     = errors.New("router id is required")
	ErrAreaNotFound      = errors.New("area not configured")
	ErrAreaMismatch      = errors.New("interface does not belong to this area")
	ErrBadHello          = errors.New("hello packet is invalid")
	ErrBadLSAType        = errors.New("lsa type out of the supported range")
	ErrBadSequence       = errors.New("lsa sequence number is not usable")
	ErrLSAUnknown        = errors.New("lsa is not installed in this area")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// AreaConfig is the identity of the local router and its areas.
type AreaConfig struct {
	RouterID string   `json:"router_id"`
	Areas    []string `json:"areas"`
	MaxAge   uint16   `json:"max_age"`
}

// IfaceConfig binds one interface to an area.
type IfaceConfig struct {
	Area     string `json:"area"`
	Priority uint8  `json:"priority"`
}

// HelloPacket is one received Hello.
type HelloPacket struct {
	RouterID     string   `json:"router_id"`
	Priority     uint8    `json:"priority"`
	DeadInterval uint32   `json:"dead_interval"`
	Neighbors    []string `json:"neighbors"`
}

// NeighborState is the compiled view of one neighbour.
type NeighborState struct {
	Iface     string `json:"iface"`
	RouterID  string `json:"router_id"`
	State     string `json:"state"`
	Priority  uint8  `json:"priority"`
	DeadInSec uint32 `json:"dead_in_sec"`
	Seq       uint64 `json:"seq"`
}

// LSA is one link state advertisement.
type LSA struct {
	Type              uint16 `json:"type"`
	LinkStateID       string `json:"link_state_id"`
	AdvertisingRouter string `json:"advertising_router"`
	Sequence          uint32 `json:"sequence"`
	Age               uint16 `json:"age"`
	Checksum          uint32 `json:"checksum"`
	Body              string `json:"body"`
}

// LSAEvent is one aging transition produced by Age.
type LSAEvent struct {
	Kind   string `json:"kind"`
	Area   string `json:"area"`
	Iface  string `json:"iface"`
	Router string `json:"router"`
	Key    string `json:"key"`
}

// DRResult is the elected designated router pair of one interface.
type DRResult struct {
	Area    string `json:"area"`
	Iface   string `json:"iface"`
	DR      string `json:"dr"`
	BDR     string `json:"bdr"`
	Changed bool   `json:"changed"`
}

// OspfLsaGuardP3 owns the neighbour table, the link state databases and the
// aging clock.
type OspfLsaGuardP3 struct {
	mu sync.Mutex

	cfg      AreaConfig
	maxAge   int64
	areas    map[string]*areaRecord
	ifaces   map[string]*interfaceRecord
	now      time.Time
	clockSet bool
}

// NewOspfLsaGuardP3 builds a guard for the configured router identity.
func NewOspfLsaGuardP3(cfg AreaConfig) *OspfLsaGuardP3 {
	maxAge := int64(cfg.MaxAge)
	if maxAge <= 0 {
		maxAge = 3600
	}

	cfg.RouterID = strings.TrimSpace(cfg.RouterID)
	areas := make(map[string]*areaRecord)
	areaNames := make([]string, 0, len(cfg.Areas))
	for _, raw := range cfg.Areas {
		area := strings.TrimSpace(raw)
		if area == "" {
			continue
		}
		if _, exists := areas[area]; exists {
			continue
		}
		areas[area] = newAreaRecord(area)
		areaNames = append(areaNames, area)
	}
	cfg.Areas = areaNames

	return &OspfLsaGuardP3{
		cfg:    cfg,
		maxAge: maxAge,
		areas:  areas,
		ifaces: make(map[string]*interfaceRecord),
	}
}

// AddInterface registers one interface in its area.
func (g *OspfLsaGuardP3) AddInterface(iface string, cfg IfaceConfig) error {
	name := strings.TrimSpace(iface)
	if name == "" {
		return ErrBlankInterface
	}

	area := strings.TrimSpace(cfg.Area)

	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(g.cfg.RouterID) == "" {
		return ErrBlankRouterID
	}
	if _, exists := g.areas[area]; !exists {
		return ErrAreaNotFound
	}
	if _, exists := g.ifaces[name]; exists {
		return ErrInterfaceExists
	}
	g.ifaces[name] = &interfaceRecord{
		Name:      name,
		Area:      area,
		Priority:  cfg.Priority,
		Neighbors: make(map[string]*neighborRecord),
	}
	return nil
}

// Hello applies one Hello received on the interface.
func (g *OspfLsaGuardP3) Hello(iface string, pkt HelloPacket) (NeighborState, error) {
	name := strings.TrimSpace(iface)
	routerID := strings.TrimSpace(pkt.RouterID)

	g.mu.Lock()
	defer g.mu.Unlock()

	ifaceState, exists := g.ifaces[name]
	if !exists {
		return NeighborState{}, ErrInterfaceNotFound
	}
	if routerID == "" || pkt.DeadInterval == 0 {
		return NeighborState{}, ErrBadHello
	}

	state := NeighborInit
	for _, neighbor := range pkt.Neighbors {
		if strings.TrimSpace(neighbor) == g.cfg.RouterID {
			state = NeighborFull
			break
		}
	}

	neighbor := ifaceState.Neighbors[routerID]
	if neighbor == nil {
		neighbor = &neighborRecord{RouterID: routerID}
		ifaceState.Neighbors[routerID] = neighbor
	}
	neighbor.State = state
	neighbor.Priority = pkt.Priority
	neighbor.DeadInterval = pkt.DeadInterval
	neighbor.Seq++
	if g.clockSet {
		neighbor.Deadline = g.now.Add(time.Duration(pkt.DeadInterval) * time.Second)
		neighbor.DeadlineSet = true
	} else {
		neighbor.Deadline = time.Time{}
		neighbor.DeadlineSet = false
	}

	return NeighborState{
		Iface:     ifaceState.Name,
		RouterID:  neighbor.RouterID,
		State:     neighbor.State,
		Priority:  neighbor.Priority,
		DeadInSec: pkt.DeadInterval,
		Seq:       neighbor.Seq,
	}, nil
}

// RecvLSA compares and stores one received LSA.
func (g *OspfLsaGuardP3) RecvLSA(area string, lsa LSA) (LSAStatus, error) {
	areaName := strings.TrimSpace(area)

	g.mu.Lock()
	defer g.mu.Unlock()

	areaState, exists := g.areas[areaName]
	if !exists {
		return "", ErrAreaNotFound
	}
	if lsa.Type < 1 || lsa.Type > 5 {
		return "", ErrBadLSAType
	}
	if !usableSequence(lsa.Sequence) {
		return "", ErrBadSequence
	}

	key := lsaKey(lsa)
	current, installed := areaState.LSAs[key]
	watermark := areaState.Watermarks[key]
	maxAge := int64(g.maxAge)
	maxAgeInstance := int64(lsa.Age) >= maxAge

	// A MaxSequence instance may only be replaced through an explicit MaxAge
	// flush. Never interpret a smaller, initial sequence as a direct wrap.
	if !maxAgeInstance && lsa.Sequence == InitialSequence && watermark == MaxSequence && !areaState.FlushReady[key] {
		if installed && current.Sequence == MaxSequence {
			flush := current
			flush.Age = uint16(maxAge)
			areaState.LSAs[key] = flush
			areaState.Watermarks[key] = MaxSequence
			areaState.FlushReady[key] = true
			_ = g.floodTargetsLocked(areaState)
			return StatusMaxAge, nil
		}

		// The old instance is already absent (for example after an earlier
		// MaxAge flush). Require one explicit rebuild attempt before accepting
		// the initial sequence again.
		areaState.Watermarks[key] = MaxSequence
		areaState.FlushReady[key] = true
		_ = g.floodTargetsLocked(areaState)
		return StatusMaxAge, nil
	}

	// Once a wrapped instance has been rebuilt, a delayed copy from the old
	// MaxSequence generation must not be able to replace it.
	if areaState.RetiredMax[key] && lsa.Sequence == MaxSequence && (!installed || sequenceCompare(current.Sequence, MaxSequence) < 0) {
		return StatusOlder, nil
	}

	status := LSAStatus(StatusOlder)
	accept := false
	rebuild := false

	if installed {
		cmp := compareLSAInstance(lsa, current)
		switch {
		case cmp > 0:
			accept = true
			status = StatusNewer
		case cmp < 0:
			if areaState.FlushReady[key] && current.Sequence == MaxSequence && lsa.Sequence == InitialSequence {
				accept = true
				status = StatusInstalled
				rebuild = true
			}
		case int64(current.Age) < maxAge && maxAgeInstance:
			// A MaxAge copy is accepted even when the age-independent
			// sequence and checksum are equal to the stored instance.
			accept = true
			status = StatusMaxAge
		default:
			return StatusDuplicate, nil
		}
	} else {
		switch {
		case watermark == 0:
			accept = true
			status = StatusInstalled
		case watermark == MaxSequence && areaState.FlushReady[key]:
			switch {
			case lsa.Sequence == InitialSequence:
				accept = true
				status = StatusInstalled
				rebuild = true
			case lsa.Sequence == MaxSequence && maxAgeInstance:
				accept = true
				status = StatusMaxAge
			}
		case sequenceCompare(lsa.Sequence, watermark) > 0:
			accept = true
			status = StatusNewer
		case lsa.Sequence == watermark && maxAgeInstance:
			accept = true
			status = StatusMaxAge
		}
	}

	if !accept {
		return status, nil
	}
	if maxAgeInstance {
		status = StatusMaxAge
	}

	// All validation and the flood-target calculation happen before the
	// database mutation. No error can leave a half-installed instance behind.
	_ = g.floodTargetsLocked(areaState)

	areaState.LSAs[key] = lsa
	if rebuild {
		areaState.Watermarks[key] = InitialSequence
		areaState.FlushReady[key] = false
		areaState.RetiredMax[key] = true
	} else {
		if areaState.Watermarks[key] == 0 || sequenceCompare(lsa.Sequence, areaState.Watermarks[key]) > 0 {
			areaState.Watermarks[key] = lsa.Sequence
		}
		if lsa.Sequence == MaxSequence {
			areaState.FlushReady[key] = maxAgeInstance
		}
	}

	return status, nil
}

// Flood reports the area interfaces an installed LSA must be sent out of.
func (g *OspfLsaGuardP3) Flood(area string, lsa LSA) ([]string, error) {
	areaName := strings.TrimSpace(area)

	g.mu.Lock()
	defer g.mu.Unlock()

	areaState, exists := g.areas[areaName]
	if !exists {
		return nil, ErrAreaNotFound
	}
	stored, installed := areaState.LSAs[lsaKey(lsa)]
	if !installed || stored.Sequence != lsa.Sequence || stored.Checksum != lsa.Checksum || stored.Body != lsa.Body {
		return nil, ErrLSAUnknown
	}
	return g.floodTargetsLocked(areaState), nil
}

// Age advances the clock, ages the database and expires neighbours.
func (g *OspfLsaGuardP3) Age(now time.Time) ([]LSAEvent, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.clockSet {
		g.clockSet = true
		g.now = now
		for _, iface := range g.ifaces {
			for _, neighbor := range iface.Neighbors {
				if !neighbor.DeadlineSet {
					neighbor.Deadline = now.Add(time.Duration(neighbor.DeadInterval) * time.Second)
					neighbor.DeadlineSet = true
				}
			}
		}
		return nil, nil
	}
	if now.Before(g.now) {
		return nil, nil
	}

	events := make([]LSAEvent, 0)
	for _, ifaceName := range sortedInterfaceNames(g.ifaces) {
		iface := g.ifaces[ifaceName]
		for _, routerID := range sortedNeighborIDs(iface.Neighbors) {
			neighbor := iface.Neighbors[routerID]
			if !neighbor.DeadlineSet {
				neighbor.Deadline = now.Add(time.Duration(neighbor.DeadInterval) * time.Second)
				neighbor.DeadlineSet = true
				continue
			}
			if now.Before(neighbor.Deadline) {
				continue
			}

			delete(iface.Neighbors, routerID)
			events = append(events, LSAEvent{
				Kind:   EventNeighborDown,
				Iface:  iface.Name,
				Router: routerID,
			})

			areaState := g.areas[iface.Area]
			if areaState == nil {
				continue
			}
			for key, stored := range areaState.LSAs {
				if stored.AdvertisingRouter == routerID {
					delete(areaState.LSAs, key)
				}
			}
		}
	}

	elapsed := int64(now.Sub(g.now) / time.Second)
	if elapsed <= 0 {
		return events, nil
	}
	g.now = g.now.Add(time.Duration(elapsed) * time.Second)

	for _, areaName := range sortedAreaNames(g.areas) {
		areaState := g.areas[areaName]
		for _, key := range sortedLSAKeys(areaState.LSAs) {
			stored := areaState.LSAs[key]
			age := int64(stored.Age) + elapsed
			if age >= int64(g.maxAge) {
				delete(areaState.LSAs, key)
				if areaState.Watermarks[key] == MaxSequence {
					areaState.FlushReady[key] = true
				}
				events = append(events, LSAEvent{
					Kind:   EventLSAMaxAge,
					Area:   areaName,
					Router: stored.AdvertisingRouter,
					Key:    key,
				})
				continue
			}
			stored.Age = uint16(age)
			areaState.LSAs[key] = stored
		}
	}

	return events, nil
}

// ElectDR elects the designated router of one interface.
func (g *OspfLsaGuardP3) ElectDR(area, iface string) (DRResult, error) {
	areaName := strings.TrimSpace(area)
	ifaceName := strings.TrimSpace(iface)

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.areas[areaName]; !exists {
		return DRResult{}, ErrAreaNotFound
	}
	ifaceState, exists := g.ifaces[ifaceName]
	if !exists {
		return DRResult{}, ErrInterfaceNotFound
	}
	if ifaceState.Area != areaName {
		return DRResult{}, ErrAreaMismatch
	}

	type candidate struct {
		routerID string
		priority uint8
	}
	candidates := make([]candidate, 0, len(ifaceState.Neighbors)+1)
	if g.cfg.RouterID != "" && ifaceState.Priority > 0 {
		candidates = append(candidates, candidate{routerID: g.cfg.RouterID, priority: ifaceState.Priority})
	}
	for _, neighbor := range ifaceState.Neighbors {
		if neighbor.RouterID == g.cfg.RouterID || neighbor.Priority == 0 {
			continue
		}
		if neighbor.State != NeighborTwoWay && neighbor.State != NeighborFull {
			continue
		}
		candidates = append(candidates, candidate{routerID: neighbor.RouterID, priority: neighbor.Priority})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority > candidates[j].priority
		}
		return routerIDGreater(candidates[i].routerID, candidates[j].routerID)
	})

	dr, bdr := "", ""
	if len(candidates) > 0 {
		dr = candidates[0].routerID
	}
	if len(candidates) > 1 {
		bdr = candidates[1].routerID
	}

	changed := ifaceState.Elected && (ifaceState.DR != dr || ifaceState.BDR != bdr)
	if !ifaceState.Elected {
		changed = dr != "" || bdr != ""
	}
	ifaceState.DR = dr
	ifaceState.BDR = bdr
	ifaceState.Elected = true

	return DRResult{
		Area:    areaName,
		Iface:   ifaceState.Name,
		DR:      dr,
		BDR:     bdr,
		Changed: changed,
	}, nil
}

// LSDB returns a copy of the area database ordered by LSA identity.
func (g *OspfLsaGuardP3) LSDB(area string) []LSA {
	areaName := strings.TrimSpace(area)

	g.mu.Lock()
	defer g.mu.Unlock()

	areaState, exists := g.areas[areaName]
	if !exists {
		return nil
	}
	keys := sortedLSAKeys(areaState.LSAs)
	result := make([]LSA, 0, len(keys))
	for _, key := range keys {
		result = append(result, areaState.LSAs[key])
	}
	return result
}

// SPFInput returns the database entries that still take part in the shortest
// path calculation.
func (g *OspfLsaGuardP3) SPFInput(area string) []LSA {
	areaName := strings.TrimSpace(area)

	g.mu.Lock()
	defer g.mu.Unlock()

	areaState, exists := g.areas[areaName]
	if !exists {
		return nil
	}
	keys := sortedLSAKeys(areaState.LSAs)
	result := make([]LSA, 0, len(keys))
	for _, key := range keys {
		stored := areaState.LSAs[key]
		if int64(stored.Age) >= int64(g.maxAge) {
			continue
		}
		result = append(result, stored)
	}
	return result
}

// Neighbors lists the neighbours of one interface ordered by router id.
func (g *OspfLsaGuardP3) Neighbors(iface string) []NeighborState {
	ifaceName := strings.TrimSpace(iface)

	g.mu.Lock()
	defer g.mu.Unlock()

	ifaceState, exists := g.ifaces[ifaceName]
	if !exists {
		return nil
	}
	result := make([]NeighborState, 0, len(ifaceState.Neighbors))
	for _, routerID := range sortedNeighborIDs(ifaceState.Neighbors) {
		neighbor := ifaceState.Neighbors[routerID]
		result = append(result, NeighborState{
			Iface:     ifaceState.Name,
			RouterID:  neighbor.RouterID,
			State:     neighbor.State,
			Priority:  neighbor.Priority,
			DeadInSec: neighborDeadInSec(neighbor, g.now, g.clockSet),
			Seq:       neighbor.Seq,
		})
	}
	return result
}

// Snapshot serialises the databases, the neighbour table and the clock.
func (g *OspfLsaGuardP3) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	areaNames := sortedAreaNames(g.areas)
	snapshot := snapshotState{
		Version:  snapshotVersion,
		RouterID: g.cfg.RouterID,
		Areas:    areaNames,
		MaxAge:   uint16(g.maxAge),
		Clock:    g.now,
		ClockSet: g.clockSet,
	}

	for _, ifaceName := range sortedInterfaceNames(g.ifaces) {
		iface := g.ifaces[ifaceName]
		entry := snapshotInterface{
			Name:     iface.Name,
			Area:     iface.Area,
			Priority: iface.Priority,
			DR:       iface.DR,
			BDR:      iface.BDR,
			Elected:  iface.Elected,
		}
		for _, routerID := range sortedNeighborIDs(iface.Neighbors) {
			neighbor := iface.Neighbors[routerID]
			entry.Neighbors = append(entry.Neighbors, snapshotNeighbor{
				RouterID:     neighbor.RouterID,
				State:        neighbor.State,
				Priority:     neighbor.Priority,
				DeadInterval: neighbor.DeadInterval,
				Deadline:     neighbor.Deadline,
				DeadlineSet:  neighbor.DeadlineSet,
				Seq:          neighbor.Seq,
			})
		}
		snapshot.Interfaces = append(snapshot.Interfaces, entry)
	}

	for _, areaName := range areaNames {
		areaState := g.areas[areaName]
		entry := snapshotArea{
			Area:       areaName,
			Watermarks: copyUint32Map(areaState.Watermarks),
			FlushReady: copyBoolMap(areaState.FlushReady),
			RetiredMax: copyBoolMap(areaState.RetiredMax),
		}
		for _, key := range sortedLSAKeys(areaState.LSAs) {
			entry.LSAs = append(entry.LSAs, areaState.LSAs[key])
		}
		snapshot.Databases = append(snapshot.Databases, entry)
	}

	return json.Marshal(snapshot)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *OspfLsaGuardP3) Restore(data []byte) error {
	var snapshot snapshotState
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}

	areas, areaNames, err := restoreAreas(snapshot.Areas)
	if err != nil || snapshot.Version != snapshotVersion || snapshot.MaxAge == 0 {
		return ErrBadSnapshot
	}

	if snapshot.ClockSet && snapshot.Clock.IsZero() {
		return ErrBadSnapshot
	}

	seenDatabases := make(map[string]bool)
	for _, entry := range snapshot.Databases {
		areaName := strings.TrimSpace(entry.Area)
		areaState := areas[areaName]
		if areaState == nil || seenDatabases[areaName] {
			return ErrBadSnapshot
		}
		seenDatabases[areaName] = true

		for _, stored := range entry.LSAs {
			if stored.Type < 1 || stored.Type > 5 || !usableSequence(stored.Sequence) {
				return ErrBadSnapshot
			}
			key := lsaKey(stored)
			if _, duplicate := areaState.LSAs[key]; duplicate {
				return ErrBadSnapshot
			}
			areaState.LSAs[key] = stored
		}

		for key, watermark := range entry.Watermarks {
			if !validLSAKey(key) || !usableSequence(watermark) {
				return ErrBadSnapshot
			}
			areaState.Watermarks[key] = watermark
		}
		for key, ready := range entry.FlushReady {
			if !validLSAKey(key) || (ready && areaState.Watermarks[key] != MaxSequence) {
				return ErrBadSnapshot
			}
			areaState.FlushReady[key] = ready
		}
		for key, retired := range entry.RetiredMax {
			if !validLSAKey(key) || (retired && areaState.Watermarks[key] == 0) {
				return ErrBadSnapshot
			}
			areaState.RetiredMax[key] = retired
		}
		for key, stored := range areaState.LSAs {
			watermark := areaState.Watermarks[key]
			if watermark == 0 || sequenceCompare(stored.Sequence, watermark) > 0 {
				return ErrBadSnapshot
			}
		}
	}

	ifaces := make(map[string]*interfaceRecord)
	for _, entry := range snapshot.Interfaces {
		name := strings.TrimSpace(entry.Name)
		areaName := strings.TrimSpace(entry.Area)
		if name == "" || areas[areaName] == nil {
			return ErrBadSnapshot
		}
		if _, duplicate := ifaces[name]; duplicate {
			return ErrBadSnapshot
		}
		iface := &interfaceRecord{
			Name:      name,
			Area:      areaName,
			Priority:  entry.Priority,
			Neighbors: make(map[string]*neighborRecord),
			DR:        entry.DR,
			BDR:       entry.BDR,
			Elected:   entry.Elected,
		}
		for _, persisted := range entry.Neighbors {
			routerID := strings.TrimSpace(persisted.RouterID)
			if routerID == "" || persisted.DeadInterval == 0 || !validNeighborState(persisted.State) {
				return ErrBadSnapshot
			}
			if persisted.DeadlineSet && persisted.Deadline.IsZero() {
				return ErrBadSnapshot
			}
			if snapshot.ClockSet != persisted.DeadlineSet {
				return ErrBadSnapshot
			}
			if _, duplicate := iface.Neighbors[routerID]; duplicate {
				return ErrBadSnapshot
			}
			iface.Neighbors[routerID] = &neighborRecord{
				RouterID:     routerID,
				State:        persisted.State,
				Priority:     persisted.Priority,
				DeadInterval: persisted.DeadInterval,
				Deadline:     persisted.Deadline,
				DeadlineSet:  persisted.DeadlineSet,
				Seq:          persisted.Seq,
			}
		}
		ifaces[name] = iface
	}

	restoredCfg := AreaConfig{
		RouterID: strings.TrimSpace(snapshot.RouterID),
		Areas:    areaNames,
		MaxAge:   snapshot.MaxAge,
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg = restoredCfg
	g.maxAge = int64(snapshot.MaxAge)
	g.areas = areas
	g.ifaces = ifaces
	g.now = snapshot.Clock
	g.clockSet = snapshot.ClockSet
	return nil
}

const snapshotVersion = 1

type areaRecord struct {
	Name       string
	LSAs       map[string]LSA
	Watermarks map[string]uint32
	FlushReady map[string]bool
	RetiredMax map[string]bool
}

func newAreaRecord(name string) *areaRecord {
	return &areaRecord{
		Name:       name,
		LSAs:       make(map[string]LSA),
		Watermarks: make(map[string]uint32),
		FlushReady: make(map[string]bool),
		RetiredMax: make(map[string]bool),
	}
}

type neighborRecord struct {
	RouterID     string
	State        string
	Priority     uint8
	DeadInterval uint32
	Deadline     time.Time
	DeadlineSet  bool
	Seq          uint64
}

type interfaceRecord struct {
	Name      string
	Area      string
	Priority  uint8
	Neighbors map[string]*neighborRecord
	DR        string
	BDR       string
	Elected   bool
}

type snapshotState struct {
	Version    int                 `json:"version"`
	RouterID   string              `json:"router_id"`
	Areas      []string            `json:"areas"`
	MaxAge     uint16              `json:"max_age"`
	Clock      time.Time           `json:"clock"`
	ClockSet   bool                `json:"clock_set"`
	Interfaces []snapshotInterface `json:"interfaces"`
	Databases  []snapshotArea      `json:"databases"`
}

type snapshotInterface struct {
	Name      string             `json:"name"`
	Area      string             `json:"area"`
	Priority  uint8              `json:"priority"`
	Neighbors []snapshotNeighbor `json:"neighbors"`
	DR        string             `json:"dr"`
	BDR       string             `json:"bdr"`
	Elected   bool               `json:"elected"`
}

type snapshotNeighbor struct {
	RouterID     string    `json:"router_id"`
	State        string    `json:"state"`
	Priority     uint8     `json:"priority"`
	DeadInterval uint32    `json:"dead_interval"`
	Deadline     time.Time `json:"deadline"`
	DeadlineSet  bool      `json:"deadline_set"`
	Seq          uint64    `json:"seq"`
}

type snapshotArea struct {
	Area       string            `json:"area"`
	LSAs       []LSA             `json:"lsas"`
	Watermarks map[string]uint32 `json:"watermarks"`
	FlushReady map[string]bool   `json:"flush_ready"`
	RetiredMax map[string]bool   `json:"retired_max"`
}

func usableSequence(sequence uint32) bool {
	return sequence != 0 && sequence != ReservedSequence
}

func lsaKey(lsa LSA) string {
	return fmt.Sprintf("%d|%s|%s", lsa.Type, lsa.LinkStateID, lsa.AdvertisingRouter)
}

func compareLSAInstance(left, right LSA) int {
	if comparison := sequenceCompare(left.Sequence, right.Sequence); comparison != 0 {
		return comparison
	}
	if left.Checksum > right.Checksum {
		return 1
	}
	if left.Checksum < right.Checksum {
		return -1
	}
	return 0
}

func sequenceCompare(left, right uint32) int {
	leftSigned := int32(left)
	rightSigned := int32(right)
	if leftSigned > rightSigned {
		return 1
	}
	if leftSigned < rightSigned {
		return -1
	}
	return 0
}

func (g *OspfLsaGuardP3) floodTargetsLocked(area *areaRecord) []string {
	result := make([]string, 0)
	for _, ifaceName := range sortedInterfaceNames(g.ifaces) {
		iface := g.ifaces[ifaceName]
		if iface.Area != area.Name {
			continue
		}
		for _, neighbor := range iface.Neighbors {
			if neighbor.State == NeighborFull {
				result = append(result, iface.Name)
				break
			}
		}
	}
	return result
}

func sortedAreaNames(areas map[string]*areaRecord) []string {
	names := make([]string, 0, len(areas))
	for name := range areas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedInterfaceNames(ifaces map[string]*interfaceRecord) []string {
	names := make([]string, 0, len(ifaces))
	for name := range ifaces {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedNeighborIDs(neighbors map[string]*neighborRecord) []string {
	ids := make([]string, 0, len(neighbors))
	for routerID := range neighbors {
		ids = append(ids, routerID)
	}
	sort.Slice(ids, func(i, j int) bool {
		return routerIDLess(ids[i], ids[j])
	})
	return ids
}

func sortedLSAKeys(lsas map[string]LSA) []string {
	keys := make([]string, 0, len(lsas))
	for key := range lsas {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func routerIDLess(left, right string) bool {
	leftIP := net.ParseIP(left)
	rightIP := net.ParseIP(right)
	if leftIP != nil && rightIP != nil {
		left16 := leftIP.To16()
		right16 := rightIP.To16()
		if left16 != nil && right16 != nil {
			return bytes.Compare(left16, right16) < 0
		}
	}
	return left < right
}

func routerIDGreater(left, right string) bool {
	return routerIDLess(right, left)
}

func neighborDeadInSec(neighbor *neighborRecord, now time.Time, clockSet bool) uint32 {
	if !clockSet || !neighbor.DeadlineSet {
		return neighbor.DeadInterval
	}
	if !now.Before(neighbor.Deadline) {
		return 0
	}
	remaining := neighbor.Deadline.Sub(now)
	return uint32((remaining + time.Second - 1) / time.Second)
}

func validNeighborState(state string) bool {
	switch state {
	case NeighborDown, NeighborInit, NeighborTwoWay, NeighborFull:
		return true
	default:
		return false
	}
}

func validLSAKey(key string) bool {
	parts := strings.SplitN(key, "|", 3)
	if len(parts) != 3 {
		return false
	}
	var lsaType uint16
	if _, err := fmt.Sscanf(parts[0], "%d", &lsaType); err != nil {
		return false
	}
	return lsaType >= 1 && lsaType <= 5 && parts[0] == fmt.Sprintf("%d", lsaType)
}

func copyUint32Map(source map[string]uint32) map[string]uint32 {
	result := make(map[string]uint32, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func copyBoolMap(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func restoreAreas(names []string) (map[string]*areaRecord, []string, error) {
	areas := make(map[string]*areaRecord)
	areaNames := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, nil, ErrBadSnapshot
		}
		if _, duplicate := areas[name]; duplicate {
			return nil, nil, ErrBadSnapshot
		}
		areas[name] = newAreaRecord(name)
		areaNames = append(areaNames, name)
	}
	return areas, areaNames, nil
}
