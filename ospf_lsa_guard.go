// Package task guards OSPF neighbour admission and the link state database.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Neighbour states.
const (
	NeighborDown   = "down"
	NeighborInit   = "init"
	NeighborTwoWay = "two_way"
	NeighborFull   = "full"
)

// Verdict actions for an incoming LSA.
const (
	ActionAccept = "accept"
	ActionReject = "reject"
)

// Reason codes for LSA admission and neighbour handling.
const (
	ReasonNewLSA       = "new_lsa"
	ReasonNewerLSA     = "newer_lsa"
	ReasonRefreshLSA   = "refresh_lsa"
	ReasonStaleLSA     = "stale_lsa"
	ReasonSeqConflict  = "seq_conflict"
	ReasonMaxAgeLSA    = "max_age_lsa"
	ReasonLSDBLimit    = "lsdb_limit"
	ReasonAreaMismatch = "area_mismatch"
)

// Errors returned by the guard.
var (
	ErrAreaMismatch     = errors.New("hello or lsa area does not match the configured area")
	ErrAuthFailed       = errors.New("hello authentication failed")
	ErrIntervalMismatch = errors.New("hello or dead interval does not match the policy")
	ErrNeighborLimit    = errors.New("neighbour limit reached for the area")
	ErrFloodQueueFull   = errors.New("flood queue is full and the lsa has lower priority")
	ErrNoLSA            = errors.New("lsa is not present in the database")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// AreaPolicy holds the local router identity and the table bounds.
type AreaPolicy struct {
	RouterID            string
	AreaID              string
	BackboneArea        string
	AuthKey             string
	HelloInterval       int
	DeadInterval        int
	MaxLSAsPerArea      int
	MaxNeighborsPerArea int
	MaxFloodQueue       int
	MaxAge              time.Duration
	IsABR               bool
}

// HelloPacket is one hello received on an interface.
type HelloPacket struct {
	AreaID        string
	RouterID      string
	Auth          string
	HelloInterval int
	DeadInterval  int
	Neighbors     []string
	Priority      int
}

// NeighborState is the per interface per router adjacency record.
type NeighborState struct {
	Iface         string    `json:"iface"`
	Area          string    `json:"area"`
	RouterID      string    `json:"router_id"`
	State         string    `json:"state"`
	TwoWay        bool      `json:"two_way"`
	HelloInterval int       `json:"hello_interval"`
	DeadInterval  int       `json:"dead_interval"`
	LastSeen      time.Time `json:"last_seen"`
	Seq           int64     `json:"seq"`
}

// LSA is one link state advertisement.
type LSA struct {
	Type              int           `json:"type"`
	LinkStateID       string        `json:"link_state_id"`
	AdvertisingRouter string        `json:"advertising_router"`
	Seq               uint32        `json:"seq"`
	Age               time.Duration `json:"age"`
	Body              string        `json:"body"`
	Checksum          string        `json:"checksum"`
	Priority          int           `json:"priority"`
}

// Verdict is the outcome of one received LSA.
type Verdict struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	Key    string `json:"key"`
	Stored bool   `json:"stored"`
	Seq    uint32 `json:"seq"`
}

// Expired reports one LSA removed by the aging scan.
type Expired struct {
	Area   string `json:"area"`
	Key    string `json:"key"`
	Reason string `json:"reason"`
	Seq    uint32 `json:"seq"`
}

// OspfStats exposes the counters of the guard.
type OspfStats struct {
	LSAs        int `json:"lsas"`
	Neighbors   int `json:"neighbors"`
	Dropped     int `json:"dropped"`
	Floods      int `json:"floods"`
	Regenerated int `json:"regenerated"`
	Purged      int `json:"purged"`
}

type neighborKey struct {
	iface    string
	routerID string
}

type lsaEntry struct {
	lsa        LSA
	receivedAt time.Time
}

// OspfLsaGuard owns the neighbour table, the LSDB and the flood queues.
type OspfLsaGuard struct {
	mu          sync.Mutex
	cfg         AreaPolicy
	neighbors   map[neighborKey]NeighborState
	lsdb        map[string]map[string]lsaEntry
	floodQueues map[string][]LSA
	stats       OspfStats
}

const (
	snapshotVersion = 1
	maxSequence     = uint32(0x7fffffff)
	initialSequence = uint32(0x80000001)
)

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotLSA struct {
	Area       string    `json:"area"`
	LSA        LSA       `json:"lsa"`
	ReceivedAt time.Time `json:"received_at"`
}

type snapshotPayload struct {
	Neighbors   []NeighborState  `json:"neighbors"`
	LSAs        []snapshotLSA    `json:"lsas"`
	FloodQueues map[string][]LSA `json:"flood_queues"`
	Counters    OspfStats        `json:"counters"`
}

// NewOspfLsaGuard builds a guard for one OSPF area.
func NewOspfLsaGuard(cfg AreaPolicy) *OspfLsaGuard {
	return &OspfLsaGuard{
		cfg:         cfg,
		neighbors:   make(map[neighborKey]NeighborState),
		lsdb:        make(map[string]map[string]lsaEntry),
		floodQueues: make(map[string][]LSA),
	}
}

// LSAKey returns the unique key of an LSA: type, link state id and originator.
func LSAKey(lsa LSA) string {
	return fmt.Sprintf("%d:%s:%s", lsa.Type, lsa.LinkStateID, lsa.AdvertisingRouter)
}

// Hello admits a hello packet and advances the adjacency state machine.
func (g *OspfLsaGuard) Hello(iface string, pkt HelloPacket) (NeighborState, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if pkt.AreaID != g.cfg.AreaID {
		g.stats.Dropped++
		return NeighborState{}, ErrAreaMismatch
	}
	if pkt.Auth != g.cfg.AuthKey {
		g.stats.Dropped++
		return NeighborState{}, ErrAuthFailed
	}
	if pkt.HelloInterval != g.cfg.HelloInterval || pkt.DeadInterval != g.cfg.DeadInterval {
		g.stats.Dropped++
		return NeighborState{}, ErrIntervalMismatch
	}

	key := neighborKey{iface: iface, routerID: pkt.RouterID}
	now := time.Now()
	neighbor, exists := g.neighbors[key]
	if !exists {
		if g.cfg.MaxNeighborsPerArea > 0 && g.neighborCountLocked(pkt.AreaID) >= g.cfg.MaxNeighborsPerArea {
			g.stats.Dropped++
			return NeighborState{}, ErrNeighborLimit
		}
		neighbor = NeighborState{
			Iface:    iface,
			Area:     pkt.AreaID,
			RouterID: pkt.RouterID,
			State:    NeighborInit,
		}
		neighbor.HelloInterval = pkt.HelloInterval
		neighbor.DeadInterval = pkt.DeadInterval
		neighbor.LastSeen = now
		neighbor.Seq = 1
		g.neighbors[key] = neighbor
		return neighbor, nil
	}

	neighbor.HelloInterval = pkt.HelloInterval
	neighbor.DeadInterval = pkt.DeadInterval
	neighbor.LastSeen = now
	neighbor.Seq++

	switch {
	case neighbor.State == NeighborFull:
		neighbor.State = NeighborFull
		neighbor.TwoWay = true
	case neighbor.State == NeighborTwoWay:
		neighbor.State = NeighborFull
		neighbor.TwoWay = true
	case pktHasRouter(pkt.Neighbors, g.cfg.RouterID):
		neighbor.State = NeighborTwoWay
		neighbor.TwoWay = true
	default:
		neighbor.State = NeighborInit
	}

	g.neighbors[key] = neighbor
	return neighbor, nil
}

// RecvLSA admits one LSA into the area database.
func (g *OspfLsaGuard) RecvLSA(area string, lsa LSA) (Verdict, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	key := LSAKey(lsa)
	verdict := Verdict{Key: key, Seq: lsa.Seq}
	if area != g.cfg.AreaID {
		verdict.Action = ActionReject
		verdict.Reason = ReasonAreaMismatch
		g.stats.Dropped++
		return verdict, ErrAreaMismatch
	}

	db := g.lsdbForAreaLocked(area)
	storageKey := storageLSAKey(lsa)
	currentKey, current, exists := highestCanonicalLSA(db, key)
	if !exists {
		return g.storeNewLSALocked(db, key, storageKey, lsa), nil
	}

	order := compareSequence(lsa.Seq, current.lsa.Seq)
	if order < 0 {
		g.stats.Dropped++
		verdict.Action = ActionReject
		verdict.Reason = ReasonStaleLSA
		return verdict, nil
	}
	if order == 0 {
		if lsa.Body != current.lsa.Body || lsa.Checksum != current.lsa.Checksum {
			g.stats.Dropped++
			verdict.Action = ActionReject
			verdict.Reason = ReasonSeqConflict
			return verdict, nil
		}
		verdict.Action = ActionAccept
		verdict.Reason = ReasonRefreshLSA
		verdict.Stored = true
		if exact, ok := db[storageKey]; ok && LSAKey(exact.lsa) == key {
			db[storageKey] = lsaEntry{lsa: lsa, receivedAt: time.Now()}
		}
		return verdict, nil
	}

	if current.lsa.AdvertisingRouter == g.cfg.RouterID {
		regenerated := current.lsa
		regenerated.Seq = nextSequence(lsa.Seq)
		regenerated.Age = 0
		db[currentKey] = lsaEntry{lsa: regenerated, receivedAt: time.Now()}
		g.stats.Regenerated++
		verdict.Action = ActionAccept
		verdict.Reason = ReasonNewerLSA
		verdict.Stored = true
		verdict.Seq = regenerated.Seq
		return verdict, nil
	}

	if exact, ok := db[storageKey]; ok && LSAKey(exact.lsa) == key {
		db[storageKey] = lsaEntry{lsa: lsa, receivedAt: time.Now()}
		verdict.Action = ActionAccept
		verdict.Reason = ReasonNewerLSA
		verdict.Stored = true
		return verdict, nil
	}

	verdict = g.storeNewLSALocked(db, key, storageKey, lsa)
	verdict.Reason = ReasonNewerLSA
	return verdict, nil
}

// Age removes LSAs that reached MaxAge before now.
func (g *OspfLsaGuard) Age(now time.Time) []Expired {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cfg.MaxAge <= 0 {
		return []Expired{}
	}

	expired := make([]Expired, 0)
	for area, db := range g.lsdb {
		for storageKey, entry := range db {
			age := entry.lsa.Age + now.Sub(entry.receivedAt)
			if age < g.cfg.MaxAge {
				continue
			}
			delete(db, storageKey)
			g.stats.Purged++
			expired = append(expired, Expired{
				Area:   area,
				Key:    LSAKey(entry.lsa),
				Reason: ReasonMaxAgeLSA,
				Seq:    entry.lsa.Seq,
			})
		}
	}

	sort.Slice(expired, func(i, j int) bool {
		if expired[i].Area != expired[j].Area {
			return expired[i].Area < expired[j].Area
		}
		if expired[i].Key != expired[j].Key {
			return expired[i].Key < expired[j].Key
		}
		return compareSequence(expired[i].Seq, expired[j].Seq) < 0
	})
	return expired
}

// Flood appends an LSA to the area flood queue under the queue policy.
func (g *OspfLsaGuard) Flood(area string, lsa LSA) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if area != g.cfg.AreaID {
		g.stats.Dropped++
		return ErrAreaMismatch
	}

	queue := g.floodQueues[area]
	if g.cfg.MaxFloodQueue <= 0 || len(queue) < g.cfg.MaxFloodQueue {
		g.floodQueues[area] = append(queue, lsa)
		g.stats.Floods++
		return nil
	}

	victim := lowestPriorityFloodIndex(queue)
	if victim < 0 || lsa.Priority <= queue[victim].Priority {
		g.stats.Dropped++
		return ErrFloodQueueFull
	}

	queue = append(queue[:victim], queue[victim+1:]...)
	queue = append(queue, lsa)
	g.floodQueues[area] = queue
	g.stats.Floods++
	return nil
}

// Neighbor reads one adjacency record.
func (g *OspfLsaGuard) Neighbor(iface, routerID string) (NeighborState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	neighbor, ok := g.neighbors[neighborKey{iface: iface, routerID: routerID}]
	return neighbor, ok
}

// Neighbors lists the adjacency records of one area ordered by interface and
// router id.
func (g *OspfLsaGuard) Neighbors(area string) []NeighborState {
	g.mu.Lock()
	defer g.mu.Unlock()

	neighbors := make([]NeighborState, 0)
	for _, neighbor := range g.neighbors {
		if neighbor.Area == area {
			neighbors = append(neighbors, neighbor)
		}
	}
	sort.Slice(neighbors, func(i, j int) bool {
		if neighbors[i].Iface != neighbors[j].Iface {
			return neighbors[i].Iface < neighbors[j].Iface
		}
		return neighbors[i].RouterID < neighbors[j].RouterID
	})
	return neighbors
}

// LSDB lists the database of one area ordered by LSA key.
func (g *OspfLsaGuard) LSDB(area string) []LSA {
	g.mu.Lock()
	defer g.mu.Unlock()

	db := g.lsdb[area]
	keys := make([]string, 0, len(db))
	for key := range db {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := db[keys[i]].lsa, db[keys[j]].lsa
		leftKey, rightKey := LSAKey(left), LSAKey(right)
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return keys[i] < keys[j]
	})

	lsas := make([]LSA, 0, len(keys))
	for _, key := range keys {
		lsas = append(lsas, db[key].lsa)
	}
	return lsas
}

// FloodQueue lists the pending flood entries of one area in queue order.
func (g *OspfLsaGuard) FloodQueue(area string) []LSA {
	g.mu.Lock()
	defer g.mu.Unlock()

	queue := g.floodQueues[area]
	result := make([]LSA, len(queue))
	copy(result, queue)
	return result
}

// Purge removes one LSA from the database.
func (g *OspfLsaGuard) Purge(area, key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	db := g.lsdb[area]
	found := false
	for storageKey, entry := range db {
		if LSAKey(entry.lsa) == key {
			delete(db, storageKey)
			found = true
		}
	}
	if !found {
		return ErrNoLSA
	}
	g.stats.Purged++
	return nil
}

// Stats reports the current counters.
func (g *OspfLsaGuard) Stats() OspfStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.statsLocked()
}

// Snapshot serialises neighbours, LSDB and flood queues.
func (g *OspfLsaGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	payload := snapshotPayload{
		Neighbors:   make([]NeighborState, 0, len(g.neighbors)),
		LSAs:        make([]snapshotLSA, 0),
		FloodQueues: make(map[string][]LSA, len(g.floodQueues)),
		Counters:    g.statsLocked(),
	}
	for _, neighbor := range g.neighbors {
		payload.Neighbors = append(payload.Neighbors, neighbor)
	}
	sort.Slice(payload.Neighbors, func(i, j int) bool {
		if payload.Neighbors[i].Area != payload.Neighbors[j].Area {
			return payload.Neighbors[i].Area < payload.Neighbors[j].Area
		}
		if payload.Neighbors[i].Iface != payload.Neighbors[j].Iface {
			return payload.Neighbors[i].Iface < payload.Neighbors[j].Iface
		}
		return payload.Neighbors[i].RouterID < payload.Neighbors[j].RouterID
	})

	areas := make([]string, 0, len(g.lsdb))
	for area := range g.lsdb {
		areas = append(areas, area)
	}
	sort.Strings(areas)
	for _, area := range areas {
		keys := make([]string, 0, len(g.lsdb[area]))
		for key := range g.lsdb[area] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			entry := g.lsdb[area][key]
			payload.LSAs = append(payload.LSAs, snapshotLSA{
				Area:       area,
				LSA:        entry.lsa,
				ReceivedAt: entry.receivedAt,
			})
		}
	}

	for area, queue := range g.floodQueues {
		payload.FloodQueues[area] = append([]LSA(nil), queue...)
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payloadBytes)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Payload:  payloadBytes,
		Checksum: hex.EncodeToString(sum[:]),
	}
	return json.Marshal(envelope)
}

// Restore replaces the state with a snapshot, all or nothing.
func (g *OspfLsaGuard) Restore(data []byte) error {
	neighbors, lsdb, floodQueues, counters, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if counters.Neighbors != len(neighbors) || counters.LSAs != countLSAs(lsdb) {
		return ErrBadSnapshot
	}
	g.neighbors = neighbors
	g.lsdb = lsdb
	g.floodQueues = floodQueues
	g.stats = counters
	return nil
}

func decodeSnapshot(data []byte) (map[neighborKey]NeighborState, map[string]map[string]lsaEntry, map[string][]LSA, OspfStats, error) {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, nil, nil, OspfStats{}, err
	}
	if envelope.Version != snapshotVersion || len(envelope.Payload) == 0 || envelope.Checksum == "" {
		return nil, nil, nil, OspfStats{}, ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return nil, nil, nil, OspfStats{}, ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return nil, nil, nil, OspfStats{}, err
	}
	if !validCounters(payload.Counters) {
		return nil, nil, nil, OspfStats{}, ErrBadSnapshot
	}

	neighbors := make(map[neighborKey]NeighborState, len(payload.Neighbors))
	for _, neighbor := range payload.Neighbors {
		if !validNeighbor(neighbor) {
			return nil, nil, nil, OspfStats{}, ErrBadSnapshot
		}
		key := neighborKey{iface: neighbor.Iface, routerID: neighbor.RouterID}
		if _, exists := neighbors[key]; exists {
			return nil, nil, nil, OspfStats{}, ErrBadSnapshot
		}
		neighbors[key] = neighbor
	}

	lsdb := make(map[string]map[string]lsaEntry)
	for _, snapshotEntry := range payload.LSAs {
		if snapshotEntry.ReceivedAt.IsZero() {
			return nil, nil, nil, OspfStats{}, ErrBadSnapshot
		}
		if _, exists := lsdb[snapshotEntry.Area]; !exists {
			lsdb[snapshotEntry.Area] = make(map[string]lsaEntry)
		}
		key := storageLSAKey(snapshotEntry.LSA)
		if _, exists := lsdb[snapshotEntry.Area][key]; exists {
			return nil, nil, nil, OspfStats{}, ErrBadSnapshot
		}
		lsdb[snapshotEntry.Area][key] = lsaEntry{
			lsa:        snapshotEntry.LSA,
			receivedAt: snapshotEntry.ReceivedAt,
		}
	}

	floodQueues := make(map[string][]LSA, len(payload.FloodQueues))
	for area, queue := range payload.FloodQueues {
		floodQueues[area] = append([]LSA(nil), queue...)
	}

	return neighbors, lsdb, floodQueues, payload.Counters, nil
}

func (g *OspfLsaGuard) lsdbForAreaLocked(area string) map[string]lsaEntry {
	db, ok := g.lsdb[area]
	if !ok {
		db = make(map[string]lsaEntry)
		g.lsdb[area] = db
	}
	return db
}

func (g *OspfLsaGuard) storeNewLSALocked(db map[string]lsaEntry, verdictKey, storageKey string, lsa LSA) Verdict {
	verdict := Verdict{
		Action: ActionAccept,
		Reason: ReasonNewLSA,
		Key:    verdictKey,
		Stored: true,
		Seq:    lsa.Seq,
	}

	if g.cfg.MaxLSAsPerArea > 0 && len(db) >= g.cfg.MaxLSAsPerArea {
		victimKey, victim := lowestPriorityLSA(db)
		if victimKey == "" || lsa.Priority <= victim.lsa.Priority {
			g.stats.Dropped++
			verdict.Action = ActionReject
			verdict.Reason = ReasonLSDBLimit
			verdict.Stored = false
			return verdict
		}
		delete(db, victimKey)
	}

	db[storageKey] = lsaEntry{lsa: lsa, receivedAt: time.Now()}
	return verdict
}

func highestCanonicalLSA(db map[string]lsaEntry, key string) (string, lsaEntry, bool) {
	var bestKey string
	var best lsaEntry
	found := false
	for storageKey, entry := range db {
		if LSAKey(entry.lsa) != key {
			continue
		}
		if !found || compareSequence(entry.lsa.Seq, best.lsa.Seq) > 0 ||
			(entry.lsa.Seq == best.lsa.Seq && storageKey < bestKey) {
			bestKey = storageKey
			best = entry
			found = true
		}
	}
	return bestKey, best, found
}

func (g *OspfLsaGuard) neighborCountLocked(area string) int {
	count := 0
	for _, neighbor := range g.neighbors {
		if neighbor.Area == area {
			count++
		}
	}
	return count
}

func (g *OspfLsaGuard) statsLocked() OspfStats {
	stats := g.stats
	stats.LSAs = countLSAs(g.lsdb)
	stats.Neighbors = len(g.neighbors)
	return stats
}

func countLSAs(lsdb map[string]map[string]lsaEntry) int {
	count := 0
	for _, db := range lsdb {
		count += len(db)
	}
	return count
}

func pktHasRouter(neighbors []string, routerID string) bool {
	for _, neighbor := range neighbors {
		if neighbor == routerID {
			return true
		}
	}
	return false
}

func storageLSAKey(lsa LSA) string {
	return LSAKey(lsa) + "\x00" + strconv.Itoa(lsa.Priority)
}

// compareSequence compares OSPF sequence numbers using signed 32-bit serial
// number arithmetic, so 0x80000001 follows 0x7fffffff after wraparound.
func compareSequence(a, b uint32) int {
	diff := int32(a - b)
	switch {
	case diff > 0:
		return 1
	case diff < 0:
		return -1
	default:
		return 0
	}
}

func nextSequence(seq uint32) uint32 {
	if seq == maxSequence {
		return initialSequence
	}
	return seq + 1
}

func lowestPriorityLSA(db map[string]lsaEntry) (string, lsaEntry) {
	var victimKey string
	var victim lsaEntry
	for key, entry := range db {
		if victimKey == "" ||
			entry.lsa.Priority < victim.lsa.Priority ||
			(entry.lsa.Priority == victim.lsa.Priority && entry.receivedAt.Before(victim.receivedAt)) ||
			(entry.lsa.Priority == victim.lsa.Priority && entry.receivedAt.Equal(victim.receivedAt) && key < victimKey) {
			victimKey = key
			victim = entry
		}
	}
	return victimKey, victim
}

func lowestPriorityFloodIndex(queue []LSA) int {
	if len(queue) == 0 {
		return -1
	}
	index := 0
	for i := 1; i < len(queue); i++ {
		if queue[i].Priority < queue[index].Priority {
			index = i
		}
	}
	return index
}

func validNeighbor(neighbor NeighborState) bool {
	switch neighbor.State {
	case NeighborDown, NeighborInit, NeighborTwoWay, NeighborFull:
		return true
	default:
		return false
	}
}

func validCounters(counters OspfStats) bool {
	return counters.LSAs >= 0 &&
		counters.Neighbors >= 0 &&
		counters.Dropped >= 0 &&
		counters.Floods >= 0 &&
		counters.Regenerated >= 0 &&
		counters.Purged >= 0
}
