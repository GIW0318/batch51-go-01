// Package task keeps the OSPFv2 neighbour table and the link state database:
// LSA instance comparison, aging, flooding decisions and DR election.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	cfg AreaConfig
}

// NewOspfLsaGuardP3 builds a guard for the configured router identity.
func NewOspfLsaGuardP3(cfg AreaConfig) *OspfLsaGuardP3 {
	return &OspfLsaGuardP3{cfg: cfg}
}

// AddInterface registers one interface in its area.
func (g *OspfLsaGuardP3) AddInterface(iface string, cfg IfaceConfig) error {
	return errNotImplemented
}

// Hello applies one Hello received on the interface.
func (g *OspfLsaGuardP3) Hello(iface string, pkt HelloPacket) (NeighborState, error) {
	return NeighborState{}, errNotImplemented
}

// RecvLSA compares and stores one received LSA.
func (g *OspfLsaGuardP3) RecvLSA(area string, lsa LSA) (LSAStatus, error) {
	return "", errNotImplemented
}

// Flood reports the area interfaces an installed LSA must be sent out of.
func (g *OspfLsaGuardP3) Flood(area string, lsa LSA) ([]string, error) {
	return nil, errNotImplemented
}

// Age advances the clock, ages the database and expires neighbours.
func (g *OspfLsaGuardP3) Age(now time.Time) ([]LSAEvent, error) {
	return nil, errNotImplemented
}

// ElectDR elects the designated router of one interface.
func (g *OspfLsaGuardP3) ElectDR(area, iface string) (DRResult, error) {
	return DRResult{}, errNotImplemented
}

// LSDB returns a copy of the area database ordered by LSA identity.
func (g *OspfLsaGuardP3) LSDB(area string) []LSA {
	return nil
}

// SPFInput returns the database entries that still take part in the shortest
// path calculation.
func (g *OspfLsaGuardP3) SPFInput(area string) []LSA {
	return nil
}

// Neighbors lists the neighbours of one interface ordered by router id.
func (g *OspfLsaGuardP3) Neighbors(iface string) []NeighborState {
	return nil
}

// Snapshot serialises the databases, the neighbour table and the clock.
func (g *OspfLsaGuardP3) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *OspfLsaGuardP3) Restore(data []byte) error {
	return errNotImplemented
}
