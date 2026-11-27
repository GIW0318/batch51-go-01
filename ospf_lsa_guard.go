// Package task guards OSPF neighbour admission and the link state database.
package task

import (
	"errors"
	"sync"
	"time"
)

// errNotImplemented marks behaviour that has not been wired up yet.
var errNotImplemented = errors.New("not implemented")

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

// OspfLsaGuard owns the neighbour table, the LSDB and the flood queues.
type OspfLsaGuard struct {
	mu  sync.Mutex
	cfg AreaPolicy
}

// NewOspfLsaGuard builds a guard for one OSPF area.
func NewOspfLsaGuard(cfg AreaPolicy) *OspfLsaGuard {
	return &OspfLsaGuard{cfg: cfg}
}

// LSAKey returns the unique key of an LSA: type, link state id and originator.
func LSAKey(lsa LSA) string {
	return ""
}

// Hello admits a hello packet and advances the adjacency state machine.
func (g *OspfLsaGuard) Hello(iface string, pkt HelloPacket) (NeighborState, error) {
	return NeighborState{}, errNotImplemented
}

// RecvLSA admits one LSA into the area database.
func (g *OspfLsaGuard) RecvLSA(area string, lsa LSA) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Age removes LSAs that reached MaxAge before now.
func (g *OspfLsaGuard) Age(now time.Time) []Expired {
	return nil
}

// Flood appends an LSA to the area flood queue under the queue policy.
func (g *OspfLsaGuard) Flood(area string, lsa LSA) error {
	return errNotImplemented
}

// Neighbor reads one adjacency record.
func (g *OspfLsaGuard) Neighbor(iface, routerID string) (NeighborState, bool) {
	return NeighborState{}, false
}

// Neighbors lists the adjacency records of one area ordered by interface and
// router id.
func (g *OspfLsaGuard) Neighbors(area string) []NeighborState {
	return nil
}

// LSDB lists the database of one area ordered by LSA key.
func (g *OspfLsaGuard) LSDB(area string) []LSA {
	return nil
}

// FloodQueue lists the pending flood entries of one area in queue order.
func (g *OspfLsaGuard) FloodQueue(area string) []LSA {
	return nil
}

// Purge removes one LSA from the database.
func (g *OspfLsaGuard) Purge(area, key string) error {
	return errNotImplemented
}

// Stats reports the current counters.
func (g *OspfLsaGuard) Stats() OspfStats {
	return OspfStats{}
}

// Snapshot serialises neighbours, LSDB and flood queues.
func (g *OspfLsaGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the state with a snapshot, all or nothing.
func (g *OspfLsaGuard) Restore(data []byte) error {
	return errNotImplemented
}
