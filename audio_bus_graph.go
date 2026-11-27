// Package task models the send graph of a live mixing console: buses with
// source levels, sends between buses, sidechain keys and block aligned
// rendering.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
)

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

type busState struct {
	id       string
	level    float32
	gainDB   float32
	bypassed bool
}

type sendState struct {
	src           string
	dst           string
	gainDB        float32
	latencyFrames int
	preFader      bool
}

type edgeKey struct {
	src string
	dst string
}

type sidechainState struct {
	key   string
	depth float32
}

type pendingKind string

const (
	pendingAddBus     pendingKind = "add_bus"
	pendingRemoveBus  pendingKind = "remove_bus"
	pendingConnect    pendingKind = "connect"
	pendingDisconnect pendingKind = "disconnect"
)

// pendingEdit is deliberately data-only. It can be replayed atomically at a
// block boundary and serialized without retaining pointers into live state.
type pendingEdit struct {
	Kind      pendingKind `json:"kind"`
	ID        string      `json:"id,omitempty"`
	BusConfig BusConfig   `json:"bus_config,omitempty"`
	Src       string      `json:"src,omitempty"`
	Dst       string      `json:"dst,omitempty"`
	Send      SendConfig  `json:"send_config,omitempty"`
}

// AudioBusGraph owns the staged edit set and the committed send graph.
type AudioBusGraph struct {
	mu sync.Mutex

	limits GraphLimits

	buses      map[string]*busState
	sends      map[edgeKey]*sendState
	sidechains map[string]*sidechainState
	pending    []pendingEdit

	rejectedCycles int
	renderedBlocks int
	framesRendered int
}

// NewAudioBusGraph builds an empty graph with the given caps.
func NewAudioBusGraph(limits GraphLimits) *AudioBusGraph {
	return &AudioBusGraph{
		limits:     normalizeLimits(limits),
		buses:      make(map[string]*busState),
		sends:      make(map[edgeKey]*sendState),
		sidechains: make(map[string]*sidechainState),
	}
}

func normalizeLimits(limits GraphLimits) GraphLimits {
	if limits.MaxBuses <= 0 {
		limits.MaxBuses = 32
	}
	if limits.MaxSendsPerBus <= 0 {
		limits.MaxSendsPerBus = 8
	}
	if limits.MaxBlockFrames <= 0 {
		limits.MaxBlockFrames = 512
	}
	return limits
}

func (g *AudioBusGraph) caps() GraphLimits {
	return normalizeLimits(g.limits)
}

func validLevel(v float32) bool {
	f := float64(v)
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 && f <= 1
}

func validGain(db float32) bool {
	f := float64(db)
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= GainMinDB && f <= GainMaxDB
}

func validDepth(depth float32) bool {
	f := float64(depth)
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= SidechainMinDepth && f <= SidechainMaxDepth
}

func validLatency(frames int) bool {
	return frames >= 0 && frames <= MaxSendLatencyFrames
}

func linearGain(db float32) float32 {
	return float32(math.Pow(10, float64(db)/20.0))
}

func cloneBus(b *busState) *busState {
	c := *b
	return &c
}

func cloneSend(s *sendState) *sendState {
	c := *s
	return &c
}

// effectiveGraph returns a detached view of the committed graph with every
// staged edit replayed in order. Validation always uses this view, so a staged
// edge participates in duplicate, limit and cycle checks before a block commit.
func (g *AudioBusGraph) effectiveGraph() (map[string]*busState, map[edgeKey]*sendState, map[string]*sidechainState) {
	buses := make(map[string]*busState, len(g.buses)+len(g.pending))
	for id, bus := range g.buses {
		buses[id] = cloneBus(bus)
	}
	sends := make(map[edgeKey]*sendState, len(g.sends)+len(g.pending))
	for key, send := range g.sends {
		sends[key] = cloneSend(send)
	}
	sidechains := make(map[string]*sidechainState, len(g.sidechains))
	for dst, sc := range g.sidechains {
		sidechains[dst] = &sidechainState{key: sc.key, depth: sc.depth}
	}

	for _, edit := range g.pending {
		switch edit.Kind {
		case pendingAddBus:
			buses[edit.ID] = &busState{
				id:     edit.ID,
				level:  edit.BusConfig.Level,
				gainDB: edit.BusConfig.GainDB,
			}
		case pendingRemoveBus:
			removeBusFromView(edit.ID, buses, sends, sidechains)
		case pendingConnect:
			sends[edgeKey{src: edit.Src, dst: edit.Dst}] = &sendState{
				src:           edit.Src,
				dst:           edit.Dst,
				gainDB:        edit.Send.GainDB,
				latencyFrames: edit.Send.LatencyFrames,
				preFader:      edit.Send.PreFader,
			}
		case pendingDisconnect:
			delete(sends, edgeKey{src: edit.Src, dst: edit.Dst})
		}
	}
	return buses, sends, sidechains
}

func removeBusFromView(id string, buses map[string]*busState, sends map[edgeKey]*sendState, sidechains map[string]*sidechainState) {
	delete(buses, id)
	for key := range sends {
		if key.src == id || key.dst == id {
			delete(sends, key)
		}
	}
	for dst, sc := range sidechains {
		if dst == id || sc.key == id {
			delete(sidechains, dst)
		}
	}
}

func outgoingCount(sends map[edgeKey]*sendState, src string) int {
	count := 0
	for key := range sends {
		if key.src == src {
			count++
		}
	}
	return count
}

func sendExists(sends map[edgeKey]*sendState, src, dst string) bool {
	_, ok := sends[edgeKey{src: src, dst: dst}]
	return ok
}

// findPath returns the directed path from src to dst inclusive, or nil.
func findPath(sends map[edgeKey]*sendState, src, dst string) []string {
	adj := make(map[string][]string)
	for key := range sends {
		adj[key.src] = append(adj[key.src], key.dst)
	}
	for node := range adj {
		sort.Strings(adj[node])
	}
	visited := make(map[string]bool)

	var walk func(string, []string) []string
	walk = func(node string, trail []string) []string {
		if node == dst {
			return append(trail, node)
		}
		if visited[node] {
			return nil
		}
		visited[node] = true
		for _, next := range adj[node] {
			if path := walk(next, append(trail, node)); path != nil {
				return path
			}
		}
		delete(visited, node)
		return nil
	}
	return walk(src, nil)
}

func formatCycle(vertices []string) string {
	if len(vertices) == 0 {
		return ""
	}
	minIndex := 0
	for i := 1; i < len(vertices); i++ {
		if vertices[i] < vertices[minIndex] {
			minIndex = i
		}
	}
	rotated := make([]string, 0, len(vertices)+1)
	rotated = append(rotated, vertices[minIndex:]...)
	rotated = append(rotated, vertices[:minIndex]...)
	rotated = append(rotated, rotated[0])
	return strings.Join(rotated, " -> ")
}

func cycleForNewSend(sends map[edgeKey]*sendState, src, dst string) string {
	path := findPath(sends, dst, src)
	if len(path) == 0 {
		return ""
	}
	// path starts at dst and ends at src. Prepending src makes a closed walk;
	// drop the duplicated closing vertex before rotating it canonically.
	vertices := make([]string, 0, len(path)+1)
	vertices = append(vertices, src)
	vertices = append(vertices, path...)
	vertices = vertices[:len(vertices)-1]
	return formatCycle(vertices)
}

// layout validates the graph is a DAG and computes the longest cumulative
// delay of each bus. The returned order is deterministic and topological.
func layout(buses map[string]*busState, sends map[edgeKey]*sendState) ([]string, map[string]int, error) {
	latency := make(map[string]int, len(buses))
	indegree := make(map[string]int, len(buses))
	for id := range buses {
		latency[id] = 0
		indegree[id] = 0
	}

	type edge struct {
		key   edgeKey
		delay int
	}
	adj := make(map[string][]edge)
	for key, send := range sends {
		if _, ok := buses[key.src]; !ok {
			return nil, nil, ErrUnknownBus
		}
		if _, ok := buses[key.dst]; !ok {
			return nil, nil, ErrUnknownBus
		}
		adj[key.src] = append(adj[key.src], edge{key: key, delay: send.latencyFrames})
		indegree[key.dst]++
	}
	for node := range adj {
		sort.Slice(adj[node], func(i, j int) bool {
			if adj[node][i].key.src != adj[node][j].key.src {
				return adj[node][i].key.src < adj[node][j].key.src
			}
			return adj[node][i].key.dst < adj[node][j].key.dst
		})
	}

	queue := make([]string, 0, len(buses))
	for id := range buses {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)

	order := make([]string, 0, len(buses))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, e := range adj[id] {
			candidate := latency[id] + e.delay
			if candidate > latency[e.key.dst] {
				latency[e.key.dst] = candidate
			}
			indegree[e.key.dst]--
			if indegree[e.key.dst] == 0 {
				queue = append(queue, e.key.dst)
				sort.Strings(queue)
			}
		}
	}
	if len(order) != len(buses) {
		return nil, nil, ErrCycle
	}
	return order, latency, nil
}

// AddBus stages a new bus. The bus only takes part in rendering after the next
// Render call.
func (g *AudioBusGraph) AddBus(id string, cfg BusConfig) error {
	if strings.TrimSpace(id) == "" {
		return ErrBlankBusID
	}
	if !validLevel(cfg.Level) {
		return ErrLevelRange
	}
	if !validGain(cfg.GainDB) {
		return ErrGainRange
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	buses, _, _ := g.effectiveGraph()
	if _, exists := buses[id]; exists {
		return ErrDuplicateBus
	}
	if len(buses) >= g.caps().MaxBuses {
		return ErrBusLimit
	}
	g.pending = append(g.pending, pendingEdit{
		Kind:      pendingAddBus,
		ID:        id,
		BusConfig: cfg,
	})
	return nil
}

// RemoveBus stages the removal of a bus together with all its incoming and
// outgoing sends.
func (g *AudioBusGraph) RemoveBus(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	buses, _, _ := g.effectiveGraph()
	if _, exists := buses[id]; !exists {
		return ErrUnknownBus
	}
	g.pending = append(g.pending, pendingEdit{Kind: pendingRemoveBus, ID: id})
	return nil
}

// Connect stages a send from src to dst. A send that would close a cycle is
// rejected with ErrCycle and the full loop in the error text.
func (g *AudioBusGraph) Connect(src, dst string, send SendConfig) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	buses, sends, _ := g.effectiveGraph()
	if _, ok := buses[src]; !ok {
		return ErrUnknownBus
	}
	if _, ok := buses[dst]; !ok {
		return ErrUnknownBus
	}
	if src == dst {
		return ErrSelfSend
	}
	if !validGain(send.GainDB) {
		return ErrGainRange
	}
	if !validLatency(send.LatencyFrames) {
		return ErrLatencyRange
	}
	if sendExists(sends, src, dst) {
		return ErrDuplicateSend
	}
	if outgoingCount(sends, src) >= g.caps().MaxSendsPerBus {
		return ErrSendLimit
	}
	if cycle := cycleForNewSend(sends, src, dst); cycle != "" {
		g.rejectedCycles++
		return fmt.Errorf("%w: %s", ErrCycle, cycle)
	}

	g.pending = append(g.pending, pendingEdit{
		Kind: pendingConnect,
		Src:  src,
		Dst:  dst,
		Send: send,
	})
	return nil
}

// Disconnect stages the removal of the send from src to dst.
func (g *AudioBusGraph) Disconnect(src, dst string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	buses, sends, _ := g.effectiveGraph()
	if _, ok := buses[src]; !ok {
		return ErrUnknownBus
	}
	if _, ok := buses[dst]; !ok {
		return ErrUnknownBus
	}
	if !sendExists(sends, src, dst) {
		return ErrUnknownSend
	}
	g.pending = append(g.pending, pendingEdit{Kind: pendingDisconnect, Src: src, Dst: dst})
	return nil
}

// SetGain sets the fader of one bus, in decibels.
func (g *AudioBusGraph) SetGain(id string, db float32) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	bus, ok := g.buses[id]
	if !ok {
		return ErrUnknownBus
	}
	if !validGain(db) {
		return ErrGainRange
	}
	bus.gainDB = db
	return nil
}

// SetSidechain keys the destination bus from the source level of key, scaled
// by depth.
func (g *AudioBusGraph) SetSidechain(dst, key string, depth float32) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !validDepth(depth) {
		return ErrSidechainDepth
	}
	if _, ok := g.buses[dst]; !ok {
		return ErrUnknownBus
	}
	if _, ok := g.buses[key]; !ok {
		return ErrUnknownBus
	}
	g.sidechains[dst] = &sidechainState{key: key, depth: depth}
	return nil
}

// Bypass mutes a bus: its own source and all of its outgoing sends go silent
// while bypassed.
func (g *AudioBusGraph) Bypass(id string, on bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	bus, ok := g.buses[id]
	if !ok {
		return ErrUnknownBus
	}
	bus.bypassed = on
	return nil
}

func (g *AudioBusGraph) commitPendingLocked() error {
	buses, sends, sidechains := g.effectiveGraph()
	if _, _, err := layout(buses, sends); err != nil {
		return err
	}
	g.buses = buses
	g.sends = sends
	g.sidechains = sidechains
	g.pending = nil
	return nil
}

func delayedConstant(value float32, delay, frames int) []float32 {
	out := make([]float32, frames)
	if delay < 0 {
		delay = 0
	}
	if delay >= frames {
		return out
	}
	for i := delay; i < frames; i++ {
		out[i] = value
	}
	return out
}

// Render commits the staged edits and renders one block of frames. The block
// runs entirely on the graph committed at its start.
func (g *AudioBusGraph) Render(frames int) ([]float32, error) {
	if frames < 1 || frames > g.caps().MaxBlockFrames {
		return nil, ErrFramesRange
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.pending) > 0 {
		if err := g.commitPendingLocked(); err != nil {
			return nil, err
		}
	}
	order, latency, err := layout(g.buses, g.sends)
	if err != nil {
		return nil, err
	}
	out := g.renderBlockLocked(frames, order, latency)
	g.renderedBlocks++
	g.framesRendered += frames
	return out, nil
}

func (g *AudioBusGraph) renderBlockLocked(frames int, order []string, latency map[string]int) []float32 {
	busOut := make(map[string][]float32, len(order))
	preFader := make(map[string][]float32, len(order))

	edgesByDst := make(map[string][]edgeKey, len(order))
	for key := range g.sends {
		edgesByDst[key.dst] = append(edgesByDst[key.dst], key)
	}
	for dst := range edgesByDst {
		sort.Slice(edgesByDst[dst], func(i, j int) bool {
			if edgesByDst[dst][i].src != edgesByDst[dst][j].src {
				return edgesByDst[dst][i].src < edgesByDst[dst][j].src
			}
			return edgesByDst[dst][i].dst < edgesByDst[dst][j].dst
		})
	}

	for _, id := range order {
		bus := g.buses[id]
		level := bus.level
		if bus.bypassed {
			level = 0
		}

		// A bus's local signal is aligned to the longest cumulative path that
		// reaches it. This makes local and returned signal phase-coherent when
		// the same bus is also a destination.
		pre := delayedConstant(level, latency[id], frames)
		out := make([]float32, frames)
		gain := linearGain(bus.gainDB)
		for i := range out {
			out[i] = pre[i] * gain
		}

		for _, key := range edgesByDst[id] {
			send := g.sends[key]
			source := busOut[key.src]
			if send.preFader {
				source = preFader[key.src]
			}
			if source == nil {
				continue
			}
			sendGain := linearGain(send.gainDB)
			delay := latency[id] - latency[key.src]
			if delay < 0 {
				delay = 0
			}
			if delay >= frames {
				continue
			}
			// The source block is already aligned at latency[src]. The
			// difference to the destination's longest path is the per-edge
			// compensation, so all paths land on the same sample.
			for i := delay; i < frames; i++ {
				out[i] += source[i-delay] * sendGain
			}
		}

		if sc := g.sidechains[id]; sc != nil {
			keyLevel := float32(0)
			if keyBus, ok := g.buses[sc.key]; ok && !keyBus.bypassed {
				keyLevel = keyBus.level
			}
			duck := float32(1) - sc.depth*keyLevel
			if duck < 0 {
				duck = 0
			}
			if duck != 1 {
				for i := range out {
					out[i] *= duck
				}
			}
		}

		if bus.bypassed {
			for i := range out {
				out[i] = 0
			}
			for i := range pre {
				pre[i] = 0
			}
		}
		preFader[id] = pre
		busOut[id] = out
	}

	if master, ok := busOut[BusMaster]; ok {
		rendered := make([]float32, frames)
		copy(rendered, master)
		return rendered
	}

	// Without an explicit master, terminal buses are the audible outputs.
	hasOutgoing := make(map[string]bool, len(g.buses))
	for key := range g.sends {
		hasOutgoing[key.src] = true
	}
	result := make([]float32, frames)
	ids := make([]string, 0, len(g.buses))
	for id := range g.buses {
		if !hasOutgoing[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		for i := range result {
			result[i] += busOut[id][i]
		}
	}
	return result
}

// Latency returns the committed delay of one bus in frames.
func (g *AudioBusGraph) Latency(id string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.buses[id]; !ok {
		return 0, ErrUnknownBus
	}
	_, latency, err := layout(g.buses, g.sends)
	if err != nil {
		return 0, err
	}
	return latency[id], nil
}

// Stats reports the committed graph and the staged edit count.
func (g *AudioBusGraph) Stats() BusStats {
	g.mu.Lock()
	defer g.mu.Unlock()

	return BusStats{
		Buses:          len(g.buses),
		Sends:          len(g.sends),
		Sidechains:     len(g.sidechains),
		RejectedCycles: g.rejectedCycles,
		PendingEdits:   len(g.pending),
		RenderedBlocks: g.renderedBlocks,
		FramesRendered: g.framesRendered,
	}
}

// Buses lists the committed bus identifiers in ascending order.
func (g *AudioBusGraph) Buses() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	ids := make([]string, 0, len(g.buses))
	for id := range g.buses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

const snapshotVersion = 1

type snapshotSidechain struct {
	Key   string  `json:"key"`
	Depth float32 `json:"depth"`
}

type snapshotBus struct {
	ID        string             `json:"id"`
	Level     float32            `json:"level"`
	GainDB    float32            `json:"gain_db"`
	Bypassed  bool               `json:"bypassed"`
	Sidechain *snapshotSidechain `json:"sidechain,omitempty"`
}

type snapshotSend struct {
	Src           string  `json:"src"`
	Dst           string  `json:"dst"`
	GainDB        float32 `json:"gain_db"`
	LatencyFrames int     `json:"latency_frames"`
	PreFader      bool    `json:"pre_fader"`
}

type snapshotPayload struct {
	Version        int            `json:"version"`
	Limits         GraphLimits    `json:"limits"`
	Buses          []snapshotBus  `json:"buses"`
	Sends          []snapshotSend `json:"sends"`
	Pending        []pendingEdit  `json:"pending"`
	RejectedCycles int            `json:"rejected_cycles"`
	RenderedBlocks int            `json:"rendered_blocks"`
	FramesRendered int            `json:"frames_rendered"`
}

// Snapshot serialises the committed graph, the faders, the sidechain keys, the
// bypass flags and the staged edits.
func (g *AudioBusGraph) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	payload := snapshotPayload{
		Version:        snapshotVersion,
		Limits:         g.caps(),
		Pending:        append([]pendingEdit(nil), g.pending...),
		RejectedCycles: g.rejectedCycles,
		RenderedBlocks: g.renderedBlocks,
		FramesRendered: g.framesRendered,
	}

	ids := make([]string, 0, len(g.buses))
	for id := range g.buses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		bus := g.buses[id]
		entry := snapshotBus{
			ID:       id,
			Level:    bus.level,
			GainDB:   bus.gainDB,
			Bypassed: bus.bypassed,
		}
		if sc := g.sidechains[id]; sc != nil {
			entry.Sidechain = &snapshotSidechain{Key: sc.key, Depth: sc.depth}
		}
		payload.Buses = append(payload.Buses, entry)
	}

	sendKeys := make([]edgeKey, 0, len(g.sends))
	for key := range g.sends {
		sendKeys = append(sendKeys, key)
	}
	sort.Slice(sendKeys, func(i, j int) bool {
		if sendKeys[i].src != sendKeys[j].src {
			return sendKeys[i].src < sendKeys[j].src
		}
		return sendKeys[i].dst < sendKeys[j].dst
	})
	for _, key := range sendKeys {
		send := g.sends[key]
		payload.Sends = append(payload.Sends, snapshotSend{
			Src:           key.src,
			Dst:           key.dst,
			GainDB:        send.gainDB,
			LatencyFrames: send.latencyFrames,
			PreFader:      send.preFader,
		})
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func decodeSnapshot(data []byte) (*snapshotPayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var payload snapshotPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, ErrBadSnapshot
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, ErrBadSnapshot
	}
	return &payload, nil
}

func stateFromSnapshot(payload *snapshotPayload) (map[string]*busState, map[edgeKey]*sendState, map[string]*sidechainState, error) {
	if payload.Version != snapshotVersion {
		return nil, nil, nil, ErrBadSnapshot
	}
	limits := payload.Limits
	if limits.MaxBuses <= 0 || limits.MaxSendsPerBus <= 0 || limits.MaxBlockFrames <= 0 {
		return nil, nil, nil, ErrBadSnapshot
	}
	if payload.RejectedCycles < 0 || payload.RenderedBlocks < 0 || payload.FramesRendered < 0 {
		return nil, nil, nil, ErrBadSnapshot
	}
	if len(payload.Buses) > limits.MaxBuses {
		return nil, nil, nil, ErrBadSnapshot
	}

	buses := make(map[string]*busState, len(payload.Buses))
	sidechains := make(map[string]*sidechainState)
	for _, entry := range payload.Buses {
		if strings.TrimSpace(entry.ID) == "" {
			return nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := buses[entry.ID]; exists {
			return nil, nil, nil, ErrBadSnapshot
		}
		if !validLevel(entry.Level) || !validGain(entry.GainDB) {
			return nil, nil, nil, ErrBadSnapshot
		}
		buses[entry.ID] = &busState{
			id:       entry.ID,
			level:    entry.Level,
			gainDB:   entry.GainDB,
			bypassed: entry.Bypassed,
		}
	}
	for _, entry := range payload.Buses {
		if entry.Sidechain == nil {
			continue
		}
		if _, ok := buses[entry.Sidechain.Key]; !ok {
			return nil, nil, nil, ErrBadSnapshot
		}
		if !validDepth(entry.Sidechain.Depth) {
			return nil, nil, nil, ErrBadSnapshot
		}
		sidechains[entry.ID] = &sidechainState{key: entry.Sidechain.Key, depth: entry.Sidechain.Depth}
	}

	sends := make(map[edgeKey]*sendState, len(payload.Sends))
	outgoing := make(map[string]int)
	for _, entry := range payload.Sends {
		key := edgeKey{src: entry.Src, dst: entry.Dst}
		if _, exists := sends[key]; exists {
			return nil, nil, nil, ErrBadSnapshot
		}
		if _, ok := buses[entry.Src]; !ok {
			return nil, nil, nil, ErrBadSnapshot
		}
		if _, ok := buses[entry.Dst]; !ok {
			return nil, nil, nil, ErrBadSnapshot
		}
		if entry.Src == entry.Dst {
			return nil, nil, nil, ErrBadSnapshot
		}
		if !validGain(entry.GainDB) || !validLatency(entry.LatencyFrames) {
			return nil, nil, nil, ErrBadSnapshot
		}
		if outgoing[entry.Src] >= limits.MaxSendsPerBus {
			return nil, nil, nil, ErrBadSnapshot
		}
		outgoing[entry.Src]++
		sends[key] = &sendState{
			src:           entry.Src,
			dst:           entry.Dst,
			gainDB:        entry.GainDB,
			latencyFrames: entry.LatencyFrames,
			preFader:      entry.PreFader,
		}
	}

	if _, _, err := layout(buses, sends); err != nil {
		return nil, nil, nil, ErrBadSnapshot
	}

	temp := &AudioBusGraph{
		limits:     limits,
		buses:      buses,
		sends:      sends,
		sidechains: sidechains,
	}
	for _, edit := range payload.Pending {
		switch edit.Kind {
		case pendingAddBus:
			if err := temp.AddBus(edit.ID, edit.BusConfig); err != nil {
				return nil, nil, nil, ErrBadSnapshot
			}
		case pendingRemoveBus:
			if err := temp.RemoveBus(edit.ID); err != nil {
				return nil, nil, nil, ErrBadSnapshot
			}
		case pendingConnect:
			if err := temp.Connect(edit.Src, edit.Dst, edit.Send); err != nil {
				return nil, nil, nil, ErrBadSnapshot
			}
		case pendingDisconnect:
			if err := temp.Disconnect(edit.Src, edit.Dst); err != nil {
				return nil, nil, nil, ErrBadSnapshot
			}
		default:
			return nil, nil, nil, ErrBadSnapshot
		}
	}

	return buses, sends, sidechains, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *AudioBusGraph) Restore(data []byte) error {
	payload, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}
	buses, sends, sidechains, err := stateFromSnapshot(payload)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.limits = payload.Limits
	g.buses = buses
	g.sends = sends
	g.sidechains = sidechains
	g.pending = append([]pendingEdit(nil), payload.Pending...)
	g.rejectedCycles = payload.RejectedCycles
	g.renderedBlocks = payload.RenderedBlocks
	g.framesRendered = payload.FramesRendered
	return nil
}
