// Package task models the send graph of a live mixing console: buses with
// source levels, sends between buses, sidechain keys and block aligned
// rendering.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Fader and send gain window, in decibels.
const (
	GainMinDB = -60.0
	GainMaxDB = 12.0
)

// Sidechain depth window.
const (
	SidechainMinDepth = 0.0
	SidechainMaxDepth = 1.0
)

// Frames accepted by one render block and by one send delay line.
const (
	MaxSendLatencyFrames = 4096
)

// Console template bus identifiers.
const (
	BusMaster = "master"
)

// Errors returned by the graph. Every rejected edit leaves the graph as it
// was before the call.
var (
	ErrBlankBusID     = errors.New("bus id is required")
	ErrDuplicateBus   = errors.New("bus already exists")
	ErrUnknownBus     = errors.New("unknown bus")
	ErrBusLimit       = errors.New("bus limit reached")
	ErrLevelRange     = errors.New("bus source level out of range")
	ErrGainRange      = errors.New("gain out of range")
	ErrSendLimit      = errors.New("send limit reached for this bus")
	ErrDuplicateSend  = errors.New("send already exists")
	ErrSelfSend       = errors.New("bus cannot send to itself")
	ErrCycle          = errors.New("send would create a cycle")
	ErrLatencyRange   = errors.New("send latency out of range")
	ErrUnknownSend    = errors.New("send does not exist")
	ErrSidechainDepth = errors.New("sidechain depth out of range")
	ErrFramesRange    = errors.New("frame count out of range")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// GraphLimits bounds the size of the graph and of one render block. A zero
// MaxBlockFrames falls back to 512, a zero MaxBuses to 32 and a zero
// MaxSendsPerBus to 8.
type GraphLimits struct {
	MaxBuses       int `json:"max_buses"`
	MaxSendsPerBus int `json:"max_sends_per_bus"`
	MaxBlockFrames int `json:"max_block_frames"`
}

// BusConfig is the initial configuration of one bus. Level is the bus own
// source contribution in the zero to one range.
type BusConfig struct {
	Level  float32 `json:"level"`
	GainDB float32 `json:"gain_db"`
}

// SendConfig describes one send edge. LatencyFrames is the delay added on the
// edge, PreFader selects whether the send taps the source before its fader.
type SendConfig struct {
	GainDB        float32 `json:"gain_db"`
	LatencyFrames int     `json:"latency_frames"`
	PreFader      bool    `json:"pre_fader"`
}

// BusStats is the observable state of the graph. Buses and Sends count the
// committed graph, PendingEdits counts the staged edits not yet committed.
type BusStats struct {
	Buses          int `json:"buses"`
	Sends          int `json:"sends"`
	Sidechains     int `json:"sidechains"`
	RejectedCycles int `json:"rejected_cycles"`
	PendingEdits   int `json:"pending_edits"`
	RenderedBlocks int `json:"rendered_blocks"`
	FramesRendered int `json:"frames_rendered"`
}

// AudioBusGraph owns the staged edit set and the committed send graph.
type AudioBusGraph struct {
	limits GraphLimits
}

// NewAudioBusGraph builds an empty graph with the given caps.
func NewAudioBusGraph(limits GraphLimits) *AudioBusGraph {
	return &AudioBusGraph{limits: limits}
}

// AddBus stages a new bus. The bus only takes part in rendering after the next
// Render call.
func (g *AudioBusGraph) AddBus(id string, cfg BusConfig) error {
	return errNotImplemented
}

// RemoveBus stages the removal of a bus together with all its incoming and
// outgoing sends.
func (g *AudioBusGraph) RemoveBus(id string) error {
	return errNotImplemented
}

// Connect stages a send from src to dst. A send that would close a cycle is
// rejected with ErrCycle and the full loop in the error text.
func (g *AudioBusGraph) Connect(src, dst string, send SendConfig) error {
	return errNotImplemented
}

// Disconnect stages the removal of the send from src to dst.
func (g *AudioBusGraph) Disconnect(src, dst string) error {
	return errNotImplemented
}

// SetGain sets the fader of one bus, in decibels.
func (g *AudioBusGraph) SetGain(id string, db float32) error {
	return errNotImplemented
}

// SetSidechain keys the destination bus from the source level of key, scaled
// by depth.
func (g *AudioBusGraph) SetSidechain(dst, key string, depth float32) error {
	return errNotImplemented
}

// Bypass mutes a bus: its own source and all of its outgoing sends go silent
// while bypassed.
func (g *AudioBusGraph) Bypass(id string, on bool) error {
	return errNotImplemented
}

// Render commits the staged edits and renders one block of frames. The block
// runs entirely on the graph committed at its start.
func (g *AudioBusGraph) Render(frames int) ([]float32, error) {
	return nil, errNotImplemented
}

// Latency returns the committed delay of one bus in frames.
func (g *AudioBusGraph) Latency(id string) (int, error) {
	return 0, errNotImplemented
}

// Stats reports the committed graph and the staged edit count.
func (g *AudioBusGraph) Stats() BusStats {
	return BusStats{}
}

// Buses lists the committed bus identifiers in ascending order.
func (g *AudioBusGraph) Buses() []string {
	return nil
}

// Snapshot serialises the committed graph, the faders, the sidechain keys, the
// bypass flags and the staged edits.
func (g *AudioBusGraph) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *AudioBusGraph) Restore(data []byte) error {
	return errNotImplemented
}
