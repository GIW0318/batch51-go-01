// Package task admits custom ACPI tables (DSDT and SSDT) during image build
// and early boot: every table header is checked for length, signature, OEM ids
// and checksum, overlapping overrides resolve by origin priority, and a batch
// of tables is applied or rolled back as one unit.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Origins accepted by Register, ordered from lowest to highest priority.
const (
	OriginBuiltin     = "builtin"
	OriginInitrdEarly = "initrd_early"
	OriginInitrdLate  = "initrd_late"
)

// Origin priorities.
const (
	PriorityBuiltin     = 10
	PriorityInitrdEarly = 20
	PriorityInitrdLate  = 30
)

// ACPI table header size.
const TableHeaderSize = 36

// Errors returned by the guard.
var (
	ErrShortTable       = errors.New("acpi table is shorter than its header")
	ErrLengthMismatch   = errors.New("acpi table length field does not match the payload")
	ErrChecksumMismatch = errors.New("acpi table checksum does not add up to zero")
	ErrBadSignature     = errors.New("acpi table signature must be four uppercase letters")
	ErrTableTooLarge    = errors.New("acpi table exceeds the configured size cap")
	ErrBadOrigin        = errors.New("acpi table origin is unknown")
	ErrTableLimit       = errors.New("acpi table limit reached")
	ErrUnknownTable     = errors.New("acpi table is not registered")
	ErrUnknownSignature = errors.New("no table with that signature is registered")
	ErrEmptyOrder       = errors.New("apply order is empty")
	ErrDsdtNeedsSsdt    = errors.New("dsdt override needs a companion ssdt")
	ErrLayerLimit       = errors.New("too many override layers for one signature")
	ErrActiveTable      = errors.New("table is active and cannot be removed")
	ErrNoRollback       = errors.New("no applied batch to roll back")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// OverridePolicy bounds the registered tables and their sizes.
type OverridePolicy struct {
	MaxTables            int
	MaxTableBytes        int
	MaxLayers            int
	RequireDsdtCompanion bool
}

// TableID identifies one registered table.
type TableID string

// Table is the metadata parsed from one ACPI table header.
type Table struct {
	ID            TableID
	Signature     string
	OEMID         string
	OEMTableID    string
	Origin        string
	Priority      int
	Length        uint32
	Revision      uint8
	SelfContained bool
	Seq           int64
}

// OverrideStats counts the registered and active tables.
type OverrideStats struct {
	Tables    int
	Active    int
	Applies   int64
	Rollbacks int64
	Rejected  int64
}

type storedTable struct {
	meta Table
	data []byte
}

type activeState map[string][]TableID

// AcpiOverrideGuard owns registered tables and the active override set.
type AcpiOverrideGuard struct {
	mu      sync.RWMutex
	cfg     OverridePolicy
	tables  map[TableID]*storedTable
	order   []TableID
	active  activeState
	undo    *activeState
	nextSeq int64
	stats   OverrideStats
}

// NewAcpiOverrideGuard builds a guard with the given policy.
func NewAcpiOverrideGuard(cfg OverridePolicy) *AcpiOverrideGuard {
	return &AcpiOverrideGuard{
		cfg:     cfg,
		tables:  make(map[TableID]*storedTable),
		active:  make(activeState),
		nextSeq: 1,
	}
}

func originPriority(origin string) (int, bool) {
	switch origin {
	case OriginBuiltin:
		return PriorityBuiltin, true
	case OriginInitrdEarly:
		return PriorityInitrdEarly, true
	case OriginInitrdLate:
		return PriorityInitrdLate, true
	default:
		return 0, false
	}
}

func trimmedACPIID(b []byte) string {
	return strings.TrimRight(string(b), "\x00 ")
}

func validSignature(b []byte) bool {
	if len(b) != 4 {
		return false
	}
	for _, c := range b {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

type parsedTable struct {
	signature  string
	oemID      string
	oemTableID string
	revision   uint8
	length     uint32
}

func parseTable(table []byte) (parsedTable, error) {
	if len(table) < TableHeaderSize {
		return parsedTable{}, ErrShortTable
	}

	length := binary.LittleEndian.Uint32(table[4:8])
	if uint64(length) != uint64(len(table)) {
		return parsedTable{}, ErrLengthMismatch
	}

	var checksum uint8
	for _, b := range table {
		checksum += b
	}
	if checksum != 0 {
		return parsedTable{}, ErrChecksumMismatch
	}

	if !validSignature(table[0:4]) {
		return parsedTable{}, ErrBadSignature
	}

	return parsedTable{
		signature:  string(table[0:4]),
		oemID:      trimmedACPIID(table[10:16]),
		oemTableID: trimmedACPIID(table[16:24]),
		revision:   table[8],
		length:     length,
	}, nil
}

func (g *AcpiOverrideGuard) validateTable(table []byte) error {
	if _, err := parseTable(table); err != nil {
		return err
	}
	if g.cfg.MaxTableBytes > 0 && len(table) > g.cfg.MaxTableBytes {
		return ErrTableTooLarge
	}
	return nil
}

func (g *AcpiOverrideGuard) rejectRegister(err error) (TableID, error) {
	g.mu.Lock()
	g.stats.Rejected++
	g.mu.Unlock()
	return "", err
}

// Register validates one table and keeps it for later resolution.
func (g *AcpiOverrideGuard) Register(table []byte, origin string) (TableID, error) {
	priority, ok := originPriority(origin)
	if !ok {
		return g.rejectRegister(ErrBadOrigin)
	}

	parsed, err := parseTable(table)
	if err != nil {
		return g.rejectRegister(err)
	}
	if g.cfg.MaxTableBytes > 0 && len(table) > g.cfg.MaxTableBytes {
		return g.rejectRegister(ErrTableTooLarge)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cfg.MaxTables > 0 && len(g.tables) >= g.cfg.MaxTables {
		g.stats.Rejected++
		return "", ErrTableLimit
	}

	seq := g.nextSeq
	g.nextSeq++
	id := TableID(fmt.Sprintf("table-%d", seq))
	meta := Table{
		ID:         id,
		Signature:  parsed.signature,
		OEMID:      parsed.oemID,
		OEMTableID: parsed.oemTableID,
		Origin:     origin,
		Priority:   priority,
		Length:     parsed.length,
		Revision:   parsed.revision,
		Seq:        seq,
	}
	data := append([]byte(nil), table...)
	g.tables[id] = &storedTable{meta: meta, data: data}
	g.order = append(g.order, id)
	return id, nil
}

// Validate checks the header, length, signature and checksum of one table.
func (g *AcpiOverrideGuard) Validate(table []byte) error {
	return g.validateTable(table)
}

func sortResolutionOrder(tables []Table) {
	sort.SliceStable(tables, func(i, j int) bool {
		if tables[i].Priority != tables[j].Priority {
			return tables[i].Priority > tables[j].Priority
		}
		if tables[i].Seq != tables[j].Seq {
			return tables[i].Seq > tables[j].Seq
		}
		return tables[i].ID < tables[j].ID
	})
}

// Resolve lists the registered tables of one signature, highest priority first.
func (g *AcpiOverrideGuard) Resolve(sig string) ([]Table, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var tables []Table
	for _, id := range g.order {
		stored := g.tables[id]
		if stored != nil && stored.meta.Signature == sig {
			tables = append(tables, stored.meta)
		}
	}
	if len(tables) == 0 {
		return nil, ErrUnknownSignature
	}
	sortResolutionOrder(tables)
	return tables, nil
}

func cloneActive(src activeState) activeState {
	dst := make(activeState, len(src))
	for sig, ids := range src {
		dst[sig] = append([]TableID(nil), ids...)
	}
	return dst
}

func sortLayerIDs(ids []TableID, tables map[TableID]*storedTable) {
	sort.SliceStable(ids, func(i, j int) bool {
		a := tables[ids[i]].meta
		b := tables[ids[j]].meta
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.Seq != b.Seq {
			return a.Seq > b.Seq
		}
		return a.ID < b.ID
	})
}

func activeContains(active activeState, id TableID) bool {
	for _, ids := range active {
		for _, activeID := range ids {
			if activeID == id {
				return true
			}
		}
	}
	return false
}

// Apply activates one batch of tables atomically.
func (g *AcpiOverrideGuard) Apply(order []TableID) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(order) == 0 {
		g.stats.Rejected++
		return ErrEmptyOrder
	}

	seen := make(map[TableID]struct{}, len(order))
	candidate := make(activeState)
	hasSSDT := false
	needsSSDT := false

	for _, id := range order {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		stored, ok := g.tables[id]
		if !ok {
			g.stats.Rejected++
			return ErrUnknownTable
		}
		if err := g.validateTable(stored.data); err != nil {
			g.stats.Rejected++
			return err
		}

		seen[id] = struct{}{}
		sig := stored.meta.Signature
		candidate[sig] = append(candidate[sig], id)
		if sig == "SSDT" {
			hasSSDT = true
		}
		if sig == "DSDT" && !stored.meta.SelfContained {
			needsSSDT = true
		}
	}

	if g.cfg.RequireDsdtCompanion && needsSSDT && !hasSSDT {
		g.stats.Rejected++
		return ErrDsdtNeedsSsdt
	}
	if g.cfg.MaxLayers > 0 {
		for _, layers := range candidate {
			if len(layers) > g.cfg.MaxLayers {
				g.stats.Rejected++
				return ErrLayerLimit
			}
		}
	}

	for sig := range candidate {
		sortLayerIDs(candidate[sig], g.tables)
	}
	previous := cloneActive(g.active)
	g.undo = &previous
	g.active = candidate
	g.stats.Applies++
	return nil
}

// Rollback restores the override set that was active before the last Apply.
func (g *AcpiOverrideGuard) Rollback() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.undo == nil {
		return ErrNoRollback
	}

	previous := cloneActive(*g.undo)
	for _, ids := range previous {
		for _, id := range ids {
			stored, ok := g.tables[id]
			if !ok {
				return ErrUnknownTable
			}
			if err := g.validateTable(stored.data); err != nil {
				return err
			}
		}
	}

	g.active = previous
	g.undo = nil
	g.stats.Rollbacks++
	return nil
}

// Active reports the currently effective table of one signature.
func (g *AcpiOverrideGuard) Active(sig string) (Table, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	ids := g.active[sig]
	if len(ids) == 0 {
		return Table{}, false
	}
	stored := g.tables[ids[0]]
	if stored == nil {
		return Table{}, false
	}
	return stored.meta, true
}

// Tables lists every registered table, oldest first.
func (g *AcpiOverrideGuard) Tables() []Table {
	g.mu.RLock()
	defer g.mu.RUnlock()

	tables := make([]Table, 0, len(g.order))
	for _, id := range g.order {
		if stored := g.tables[id]; stored != nil {
			tables = append(tables, stored.meta)
		}
	}
	return tables
}

// Deregister removes one inactive table.
func (g *AcpiOverrideGuard) Deregister(id TableID) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.tables[id]; !ok {
		return ErrUnknownTable
	}
	if activeContains(g.active, id) {
		return ErrActiveTable
	}
	if g.undo != nil && activeContains(*g.undo, id) {
		// The previous batch can no longer be restored completely. Drop it
		// rather than leaving a snapshot that references an unregistered table.
		g.undo = nil
	}

	delete(g.tables, id)
	for i, registered := range g.order {
		if registered == id {
			g.order = append(g.order[:i], g.order[i+1:]...)
			break
		}
	}
	return nil
}

// Layers reports how many active tables carry one signature.
func (g *AcpiOverrideGuard) Layers(sig string) int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.active[sig])
}

// SetSelfContained marks one DSDT as carrying its own companion tables.
func (g *AcpiOverrideGuard) SetSelfContained(id TableID, selfContained bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	stored, ok := g.tables[id]
	if !ok {
		return ErrUnknownTable
	}
	stored.meta.SelfContained = selfContained
	return nil
}

// Stats reads the guard counters.
func (g *AcpiOverrideGuard) Stats() OverrideStats {
	g.mu.RLock()
	defer g.mu.RUnlock()

	stats := g.stats
	stats.Tables = len(g.tables)
	stats.Active = 0
	for _, ids := range g.active {
		stats.Active += len(ids)
	}
	return stats
}

const (
	snapshotMagic   = "ACPI_OVERRIDE_GUARD_SNAPSHOT\x00"
	snapshotVersion = uint32(1)
)

type snapshotTable struct {
	Meta Table  `json:"meta"`
	Data []byte `json:"data"`
}

type snapshotState struct {
	Version int                  `json:"version"`
	NextSeq int64                `json:"next_seq"`
	Tables  []snapshotTable      `json:"tables"`
	Active  map[string][]TableID `json:"active"`
	HasUndo bool                 `json:"has_undo"`
	Undo    map[string][]TableID `json:"undo,omitempty"`
	Stats   OverrideStats        `json:"stats"`
}

func (g *AcpiOverrideGuard) snapshotStateLocked() snapshotState {
	state := snapshotState{
		Version: int(snapshotVersion),
		NextSeq: g.nextSeq,
		Tables:  make([]snapshotTable, 0, len(g.order)),
		Active:  make(map[string][]TableID, len(g.active)),
		HasUndo: g.undo != nil,
		Stats:   g.stats,
	}
	for _, id := range g.order {
		stored := g.tables[id]
		if stored == nil {
			continue
		}
		state.Tables = append(state.Tables, snapshotTable{
			Meta: stored.meta,
			Data: append([]byte(nil), stored.data...),
		})
	}
	for sig, ids := range g.active {
		state.Active[sig] = append([]TableID(nil), ids...)
	}
	if g.undo != nil {
		state.Undo = make(map[string][]TableID, len(*g.undo))
		for sig, ids := range *g.undo {
			state.Undo[sig] = append([]TableID(nil), ids...)
		}
	}
	return state
}

func encodeSnapshot(state snapshotState) ([]byte, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) > uint64(^uint32(0)) {
		return nil, ErrBadSnapshot
	}

	headerLen := len(snapshotMagic) + 8
	out := make([]byte, headerLen, headerLen+len(payload)+sha256.Size)
	copy(out, snapshotMagic)
	binary.BigEndian.PutUint32(out[len(snapshotMagic):len(snapshotMagic)+4], snapshotVersion)
	binary.BigEndian.PutUint32(out[len(snapshotMagic)+4:headerLen], uint32(len(payload)))
	out = append(out, payload...)
	sum := sha256.Sum256(out)
	out = append(out, sum[:]...)
	return out, nil
}

func decodeSnapshot(data []byte) (snapshotState, error) {
	headerLen := len(snapshotMagic) + 8
	minimum := headerLen + sha256.Size
	if len(data) < minimum || !bytes.HasPrefix(data, []byte(snapshotMagic)) {
		return snapshotState{}, ErrBadSnapshot
	}
	if binary.BigEndian.Uint32(data[len(snapshotMagic):len(snapshotMagic)+4]) != snapshotVersion {
		return snapshotState{}, ErrBadSnapshot
	}
	payloadLen := uint64(binary.BigEndian.Uint32(data[len(snapshotMagic)+4 : headerLen]))
	total := uint64(headerLen) + payloadLen + sha256.Size
	if total != uint64(len(data)) {
		return snapshotState{}, ErrBadSnapshot
	}

	payloadEnd := headerLen + int(payloadLen)
	expected := sha256.Sum256(data[:payloadEnd])
	if !bytes.Equal(expected[:], data[payloadEnd:]) {
		return snapshotState{}, ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(data[headerLen:payloadEnd], &state); err != nil {
		return snapshotState{}, ErrBadSnapshot
	}
	if state.Version != int(snapshotVersion) {
		return snapshotState{}, ErrBadSnapshot
	}
	return state, nil
}

func activeStateFromSnapshot(raw map[string][]TableID, tables map[TableID]*storedTable, cfg OverridePolicy) (activeState, error) {
	active := make(activeState)
	seen := make(map[TableID]struct{})
	for sig, ids := range raw {
		if len(ids) == 0 {
			return nil, ErrBadSnapshot
		}
		if cfg.MaxLayers > 0 && len(ids) > cfg.MaxLayers {
			return nil, ErrLayerLimit
		}
		for _, id := range ids {
			if _, duplicate := seen[id]; duplicate {
				return nil, ErrBadSnapshot
			}
			stored, ok := tables[id]
			if !ok || stored.meta.Signature != sig {
				return nil, ErrBadSnapshot
			}
			seen[id] = struct{}{}
			active[sig] = append(active[sig], id)
		}
		sortLayerIDs(active[sig], tables)
	}

	if cfg.RequireDsdtCompanion {
		hasSSDT := len(active["SSDT"]) > 0
		for _, id := range active["DSDT"] {
			if !tables[id].meta.SelfContained && !hasSSDT {
				return nil, ErrDsdtNeedsSsdt
			}
		}
	}
	return active, nil
}

func (g *AcpiOverrideGuard) restoreState(state snapshotState) (map[TableID]*storedTable, []TableID, activeState, *activeState, int64, error) {
	if g.cfg.MaxTables > 0 && len(state.Tables) > g.cfg.MaxTables {
		return nil, nil, nil, nil, 0, ErrTableLimit
	}

	tables := make(map[TableID]*storedTable, len(state.Tables))
	order := make([]TableID, 0, len(state.Tables))
	seqs := make(map[int64]struct{}, len(state.Tables))
	var maxSeq int64
	for _, snapshotTable := range state.Tables {
		meta := snapshotTable.Meta
		if meta.ID == "" {
			return nil, nil, nil, nil, 0, ErrBadSnapshot
		}
		if _, duplicate := tables[meta.ID]; duplicate {
			return nil, nil, nil, nil, 0, ErrBadSnapshot
		}
		if _, duplicate := seqs[meta.Seq]; duplicate || meta.Seq <= 0 {
			return nil, nil, nil, nil, 0, ErrBadSnapshot
		}
		priority, ok := originPriority(meta.Origin)
		if !ok || priority != meta.Priority || meta.Length != uint32(len(snapshotTable.Data)) {
			return nil, nil, nil, nil, 0, ErrBadSnapshot
		}
		if g.cfg.MaxTableBytes > 0 && len(snapshotTable.Data) > g.cfg.MaxTableBytes {
			return nil, nil, nil, nil, 0, ErrTableTooLarge
		}
		parsed, err := parseTable(snapshotTable.Data)
		if err != nil || parsed.signature != meta.Signature || parsed.length != meta.Length || parsed.revision != meta.Revision {
			return nil, nil, nil, nil, 0, ErrBadSnapshot
		}

		meta.OEMID = parsed.oemID
		meta.OEMTableID = parsed.oemTableID
		data := append([]byte(nil), snapshotTable.Data...)
		tables[meta.ID] = &storedTable{meta: meta, data: data}
		order = append(order, meta.ID)
		seqs[meta.Seq] = struct{}{}
		if meta.Seq > maxSeq {
			maxSeq = meta.Seq
		}
	}

	active, err := activeStateFromSnapshot(state.Active, tables, g.cfg)
	if err != nil {
		return nil, nil, nil, nil, 0, err
	}

	var undo *activeState
	if state.HasUndo {
		previous, err := activeStateFromSnapshot(state.Undo, tables, g.cfg)
		if err != nil {
			return nil, nil, nil, nil, 0, err
		}
		undo = &previous
	} else if len(state.Undo) != 0 {
		return nil, nil, nil, nil, 0, ErrBadSnapshot
	}

	nextSeq := state.NextSeq
	if nextSeq <= maxSeq {
		nextSeq = maxSeq + 1
	}
	if nextSeq <= 0 {
		nextSeq = 1
	}
	if state.Stats.Applies < 0 || state.Stats.Rollbacks < 0 || state.Stats.Rejected < 0 {
		return nil, nil, nil, nil, 0, ErrBadSnapshot
	}
	return tables, order, active, undo, nextSeq, nil
}

// Snapshot serialises the registered tables and the active set.
func (g *AcpiOverrideGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	state := g.snapshotStateLocked()
	g.mu.RUnlock()
	return encodeSnapshot(state)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *AcpiOverrideGuard) Restore(data []byte) error {
	state, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}
	tables, order, active, undo, nextSeq, err := g.restoreState(state)
	if err != nil {
		return err
	}

	g.mu.Lock()
	g.tables = tables
	g.order = order
	g.active = active
	g.undo = undo
	g.nextSeq = nextSeq
	g.stats = state.Stats
	g.mu.Unlock()
	return nil
}
