// Package task validates build placement on a snapped grid: it previews the
// footprint of one piece, commits a batch atomically, walks the support chain
// up from the ground and carries the placed set plus the undo stack across a
// reload.
package task

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// Support reasons reported for one committed piece.
const (
	ReasonGround    = "ground"
	ReasonStacked   = "stacked"
	ReasonNoSupport = "no_support"
	ReasonOverload  = "overloaded"
)

// Rotation values accepted by Preview and Commit.
const (
	RotationNone = 0
	TurnRight    = 90
	TurnBack     = 180
	TurnLeft     = 270
)

// Errors reported by the validator.
var (
	ErrBadItem           = errors.New("item definition is invalid")
	ErrInvalidRotation   = errors.New("rotation must be 0, 90, 180 or 270")
	ErrOutOfBounds       = errors.New("footprint falls outside the buildable area")
	ErrCellOccupied      = errors.New("footprint overlaps a piece on the same layer")
	ErrFootprintTooLarge = errors.New("footprint exceeds the configured cell budget")
	ErrBatchTooLarge     = errors.New("batch exceeds the configured item budget")
	ErrBadBatch          = errors.New("batch id must be non blank and shared by the whole batch")
	ErrPieceLimit        = errors.New("placed piece budget reached")
	ErrUnknownPlacement  = errors.New("unknown placement")
	ErrNotLatestBatch    = errors.New("only the latest batch can be undone")
	ErrUnknownBatch      = errors.New("batch is not on the undo stack")
	ErrUnsupported       = errors.New("placement has no support chain down to the ground")
	ErrOverloaded        = errors.New("support cannot carry the load above it")
	ErrSteepTerrain      = errors.New("terrain slope exceeds the configured limit")
	ErrBadGrid           = errors.New("grid cell must be positive")
	ErrGridLocked        = errors.New("grid cannot change while pieces are placed")
	ErrBadTerrain        = errors.New("terrain view is invalid")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

const (
	defaultTerrainRows = 32
	defaultTerrainCols = 32
	snapshotVersion    = 1
	snapshotMagic      = "BPV1\n"
)

// Vec3 is a world position in metres.
type Vec3 struct {
	X float64
	Y float64
	Z float64
}

// Cell is one grid cell index on the build plane.
type Cell struct {
	X int
	Y int
}

// ItemDef is the footprint and load profile of one buildable piece.
type ItemDef struct {
	ID         string
	Width      int
	Depth      int
	Height     int
	Mass       int
	MaxLoad    int
	Foundation bool
}

// BuildLimits caps one validator instance.
type BuildLimits struct {
	MaxBatch     int
	MaxFootprint int
	MaxPieces    int
	MaxUndoDepth int
}

// Placement is one requested or committed piece. Layer is filled by the
// validator and ignored on input.
type Placement struct {
	BatchID string
	Def     ItemDef
	Cell    Cell
	Pos     Vec3
	Rot     int
	Layer   int
	Cells   []Cell
}

// PlacedID identifies one committed piece.
type PlacedID string

// Conflict reports the first rejected entry of a batch.
type Conflict struct {
	Index  int
	ItemID string
	Cell   Cell
	With   PlacedID
}

// Error implements the error interface.
func (c *Conflict) Error() string {
	return fmt.Sprintf("placement conflict at index %d for item %s on cell %v", c.Index, c.ItemID, c.Cell)
}

// SupportReport is the answer of CheckSupport for one committed piece.
type SupportReport struct {
	ID        PlacedID
	Supported bool
	Path      []PlacedID
	MaxSlope  float64
	Reason    string
}

// TerrainView is the height field the validator builds on. Heights holds one
// value per cell, row major, and each cell also provides its upper and right
// corner by clamping at the field edge.
type TerrainView struct {
	Rows     int
	Cols     int
	Heights  []float64
	MaxSlope float64
}

// BuildStats summarises one validator.
type BuildStats struct {
	Placed    int
	Batches   int
	UndoDepth int
	Conflicts int
	Removed   int
	Cascaded  int
}

type placedPiece struct {
	id        PlacedID
	placement Placement
}

type batchRecord struct {
	BatchID string
	IDs     []PlacedID
}

type cellKey struct {
	layer int
	cell  Cell
}

type snapshotPiece struct {
	ID        PlacedID  `json:"id"`
	Placement Placement `json:"placement"`
}

type snapshotCounters struct {
	Batches   int `json:"batches"`
	Conflicts int `json:"conflicts"`
	Removed   int `json:"removed"`
	Cascaded  int `json:"cascaded"`
}

type snapshotPayload struct {
	Version  int              `json:"version"`
	Grid     float64          `json:"grid"`
	Terrain  TerrainView      `json:"terrain"`
	NextID   int64            `json:"next_id"`
	Counters snapshotCounters `json:"counters"`
	Placed   []snapshotPiece  `json:"placed"`
	Undo     []batchRecord    `json:"undo"`
}

// BuildPlacementValidator owns the placed set and the undo stack.
type BuildPlacementValidator struct {
	mu sync.RWMutex

	limits  BuildLimits
	grid    float64
	terrain TerrainView

	placed  map[PlacedID]*placedPiece
	undo    []batchRecord
	nextID  int64
	batches int

	conflicts int
	removed   int
	cascaded  int
}

// NewBuildPlacementValidator builds a validator with the given budgets and a
// flat 32 by 32 default terrain.
func NewBuildPlacementValidator(limits BuildLimits) *BuildPlacementValidator {
	return &BuildPlacementValidator{
		limits: limits,
		grid:   1,
		terrain: TerrainView{
			Rows:     defaultTerrainRows,
			Cols:     defaultTerrainCols,
			Heights:  make([]float64, defaultTerrainRows*defaultTerrainCols),
			MaxSlope: 1,
		},
		placed: make(map[PlacedID]*placedPiece),
		nextID: 1,
	}
}

// SetGrid changes the grid cell size in metres.
func (v *BuildPlacementValidator) SetGrid(cell float64) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !isFinite(cell) || cell <= 0 {
		return ErrBadGrid
	}
	if len(v.placed) != 0 {
		return ErrGridLocked
	}
	v.grid = cell
	return nil
}

// SetTerrain replaces the height field.
func (v *BuildPlacementValidator) SetTerrain(t TerrainView) error {
	if err := validateTerrain(t); err != nil {
		return err
	}
	copied := cloneTerrain(t)

	v.mu.Lock()
	defer v.mu.Unlock()
	v.terrain = copied
	return nil
}

// Preview snaps one piece onto the grid and checks its footprint.
func (v *BuildPlacementValidator) Preview(def ItemDef, pos Vec3, rot int) (Placement, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if err := validateItem(def, v.limits.MaxFootprint); err != nil {
		return Placement{}, err
	}
	if !validRotation(rot) {
		return Placement{}, ErrInvalidRotation
	}

	cell, err := v.snapPosition(pos)
	if err != nil {
		return Placement{}, err
	}
	width, depth := rotatedDims(def, rot)
	if !v.inBounds(cell, width, depth) {
		return Placement{}, ErrOutOfBounds
	}

	cells := makeCells(cell, width, depth)
	for _, c := range cells {
		if _, occupied := v.occupantLocked(c, 0); occupied {
			return Placement{}, ErrCellOccupied
		}
	}

	return Placement{
		Def:   def,
		Cell:  cell,
		Pos:   v.snappedPosition(cell),
		Rot:   rot,
		Layer: 0,
		Cells: cells,
	}, nil
}

// Commit places a whole batch or nothing at all.
func (v *BuildPlacementValidator) Commit(batch []Placement) ([]PlacedID, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	reject := func(err error) ([]PlacedID, error) {
		return nil, err
	}

	if len(batch) == 0 {
		return reject(ErrBadBatch)
	}
	if v.limits.MaxBatch < 0 || len(batch) > v.limits.MaxBatch {
		return reject(ErrBatchTooLarge)
	}

	batchID := batch[0].BatchID
	if strings.TrimSpace(batchID) == "" {
		return reject(ErrBadBatch)
	}
	for _, requested := range batch[1:] {
		if requested.BatchID != batchID {
			return reject(ErrBadBatch)
		}
	}
	if v.limits.MaxPieces < 0 || len(batch) > v.limits.MaxPieces-len(v.placed) {
		return reject(ErrPieceLimit)
	}

	tentative := make(map[PlacedID]*placedPiece, len(v.placed)+len(batch))
	for id, piece := range v.placed {
		tentative[id] = piece
	}
	occupancy := buildOccupancy(tentative)
	ids := make([]PlacedID, 0, len(batch))
	nextID := v.nextID

	for index, requested := range batch {
		fail := func(with PlacedID) ([]PlacedID, error) {
			v.conflicts++
			return nil, &Conflict{Index: index, ItemID: requested.Def.ID, Cell: requested.Cell, With: with}
		}

		if err := validateItem(requested.Def, v.limits.MaxFootprint); err != nil {
			return fail("")
		}
		if !validRotation(requested.Rot) {
			return fail("")
		}
		cell, anchorErr := v.commitCell(requested)
		if anchorErr != nil {
			v.conflicts++
			return nil, &Conflict{Index: index, ItemID: requested.Def.ID, Cell: requested.Cell}
		}
		width, depth := rotatedDims(requested.Def, requested.Rot)
		if !v.inBounds(cell, width, depth) {
			v.conflicts++
			return nil, &Conflict{Index: index, ItemID: requested.Def.ID, Cell: cell}
		}
		fail = func(with PlacedID) ([]PlacedID, error) {
			v.conflicts++
			return nil, &Conflict{Index: index, ItemID: requested.Def.ID, Cell: cell, With: with}
		}
		cells := makeCells(cell, width, depth)

		layer := 0
		if !requested.Def.Foundation {
			supportLayer, supported := highestOverlappingLayer(cells, tentative)
			if !supported {
				return fail("")
			}
			layer = supportLayer + 1
		}

		for _, c := range cells {
			if occupiedBy, occupied := occupancy[cellKey{layer: layer, cell: c}]; occupied {
				return fail(occupiedBy)
			}
		}
		if requested.Def.Foundation {
			if maxSlopeForCells(cells, v.terrain, v.grid) > v.terrain.MaxSlope {
				return fail("")
			}
		}

		id := PlacedID(fmt.Sprintf("p%d", nextID))
		nextID++
		piece := &placedPiece{
			id: id,
			placement: Placement{
				BatchID: batchID,
				Def:     requested.Def,
				Cell:    cell,
				Pos:     v.snappedPosition(cell),
				Rot:     requested.Rot,
				Layer:   layer,
				Cells:   append([]Cell(nil), cells...),
			},
		}
		tentative[id] = piece
		for _, c := range cells {
			occupancy[cellKey{layer: layer, cell: c}] = id
		}
		ids = append(ids, id)

		if overloadedID, overloaded := firstOverloaded(tentative); overloaded {
			return fail(overloadedID)
		}
	}

	v.placed = tentative
	v.nextID = nextID
	v.batches++
	v.undo = append(v.undo, batchRecord{BatchID: batchID, IDs: append([]PlacedID(nil), ids...)})
	v.trimUndoLocked()
	return ids, nil
}

// Remove deletes one committed piece and any pieces it leaves unsupported.
func (v *BuildPlacementValidator) Remove(id PlacedID) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.placed[id]; !ok {
		return ErrUnknownPlacement
	}
	delete(v.placed, id)
	v.removed++
	v.cascaded += v.removeUnsupportedLocked()
	return nil
}

// CheckSupport walks the support chain of one committed piece.
func (v *BuildPlacementValidator) CheckSupport(id PlacedID) (SupportReport, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	piece, ok := v.placed[id]
	if !ok {
		return SupportReport{}, ErrUnknownPlacement
	}

	report := SupportReport{
		ID:       id,
		Path:     []PlacedID{id},
		MaxSlope: maxSlopeForCells(piece.placement.Cells, v.terrain, v.grid),
	}
	if piece.placement.Def.Foundation {
		report.Supported = true
		report.Reason = ReasonGround
		return report, nil
	}

	visited := make(map[PlacedID]bool, len(v.placed))
	visited[id] = true
	current := piece
	for current.placement.Layer > 0 {
		supporterID, supporter, found := v.supportBelowLocked(current, visited)
		if !found {
			report.Supported = false
			report.Reason = ReasonNoSupport
			return report, nil
		}
		report.Path = append(report.Path, supporterID)
		if supporter.placement.Def.Foundation {
			report.Supported = true
			report.Reason = ReasonStacked
			for _, pathID := range report.Path[1:] {
				support := v.placed[pathID]
				if support != nil && loadOn(pathID, v.placed) > support.placement.Def.MaxLoad {
					report.Supported = false
					report.Reason = ReasonOverload
					return report, nil
				}
			}
			return report, nil
		}
		if visited[supporterID] {
			break
		}
		visited[supporterID] = true
		current = supporter
	}

	report.Supported = false
	report.Reason = ReasonNoSupport
	return report, nil
}

// Undo removes the pieces of the latest batch.
func (v *BuildPlacementValidator) Undo(batchID string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if len(v.undo) == 0 {
		return ErrUnknownBatch
	}
	latest := v.undo[len(v.undo)-1]
	if latest.BatchID != batchID {
		for _, record := range v.undo[:len(v.undo)-1] {
			if record.BatchID == batchID {
				return ErrNotLatestBatch
			}
		}
		return ErrUnknownBatch
	}

	for _, id := range latest.IDs {
		delete(v.placed, id)
	}
	v.undo = append([]batchRecord(nil), v.undo[:len(v.undo)-1]...)
	if v.batches > 0 {
		v.batches--
	}
	v.cascaded += v.removeUnsupportedLocked()
	return nil
}

// Stats reads the counters of the validator.
func (v *BuildPlacementValidator) Stats() BuildStats {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return BuildStats{
		Placed:    len(v.placed),
		Batches:   v.batches,
		UndoDepth: len(v.undo),
		Conflicts: v.conflicts,
		Removed:   v.removed,
		Cascaded:  v.cascaded,
	}
}

// Snapshot serialises the placed set and the undo stack.
func (v *BuildPlacementValidator) Snapshot() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	ids := make([]PlacedID, 0, len(v.placed))
	for id := range v.placed {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	payload := snapshotPayload{
		Version: snapshotVersion,
		Grid:    v.grid,
		Terrain: cloneTerrain(v.terrain),
		NextID:  v.nextID,
		Counters: snapshotCounters{
			Batches:   v.batches,
			Conflicts: v.conflicts,
			Removed:   v.removed,
			Cascaded:  v.cascaded,
		},
		Placed: make([]snapshotPiece, 0, len(ids)),
		Undo:   make([]batchRecord, 0, len(v.undo)),
	}
	for _, id := range ids {
		piece := v.placed[id]
		payload.Placed = append(payload.Placed, snapshotPiece{
			ID:        id,
			Placement: clonePlacement(piece.placement),
		})
	}
	for _, record := range v.undo {
		payload.Undo = append(payload.Undo, batchRecord{
			BatchID: record.BatchID,
			IDs:     append([]PlacedID(nil), record.IDs...),
		})
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	header := make([]byte, 0, len(snapshotMagic)+hex.EncodedLen(len(sum))+1)
	header = append(header, snapshotMagic...)
	header = append(header, []byte(hex.EncodeToString(sum[:]))...)
	header = append(header, '\n')
	return append(header, raw...), nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (v *BuildPlacementValidator) Restore(data []byte) error {
	payload, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	placed, undo, err := v.validateSnapshotLocked(payload)
	if err != nil {
		return ErrBadSnapshot
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.grid = payload.Grid
	v.terrain = cloneTerrain(payload.Terrain)
	v.placed = placed
	v.undo = undo
	v.nextID = payload.NextID
	v.batches = payload.Counters.Batches
	v.conflicts = payload.Counters.Conflicts
	v.removed = payload.Counters.Removed
	v.cascaded = payload.Counters.Cascaded
	return nil
}

func validateItem(def ItemDef, maxFootprint int) error {
	if strings.TrimSpace(def.ID) == "" || def.Width <= 0 || def.Depth <= 0 || def.Height <= 0 {
		return ErrBadItem
	}
	if maxFootprint < 0 || def.Width > maxFootprint/def.Depth {
		return ErrFootprintTooLarge
	}
	return nil
}

func validRotation(rot int) bool {
	switch rot {
	case RotationNone, TurnRight, TurnBack, TurnLeft:
		return true
	default:
		return false
	}
}

func rotatedDims(def ItemDef, rot int) (int, int) {
	if rot == TurnRight || rot == TurnLeft {
		return def.Depth, def.Width
	}
	return def.Width, def.Depth
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateTerrain(t TerrainView) error {
	if t.Rows <= 0 || t.Cols <= 0 || !isFinite(t.MaxSlope) || t.MaxSlope < 0 {
		return ErrBadTerrain
	}
	if t.Rows > int(^uint(0)>>1)/t.Cols || len(t.Heights) != t.Rows*t.Cols {
		return ErrBadTerrain
	}
	for _, height := range t.Heights {
		if !isFinite(height) {
			return ErrBadTerrain
		}
	}
	return nil
}

func cloneTerrain(t TerrainView) TerrainView {
	t.Heights = append([]float64(nil), t.Heights...)
	return t
}

func clonePlacement(p Placement) Placement {
	p.Cells = append([]Cell(nil), p.Cells...)
	return p
}

func (v *BuildPlacementValidator) snapPosition(pos Vec3) (Cell, error) {
	if !isFinite(pos.X) || !isFinite(pos.Y) {
		return Cell{}, ErrOutOfBounds
	}
	x := math.Floor(pos.X / v.grid)
	y := math.Floor(pos.Y / v.grid)
	if x < 0 || y < 0 || x >= float64(v.terrain.Cols) || y >= float64(v.terrain.Rows) {
		return Cell{}, ErrOutOfBounds
	}
	return Cell{X: int(x), Y: int(y)}, nil
}

func (v *BuildPlacementValidator) commitCell(requested Placement) (Cell, error) {
	if requested.Cell == (Cell{}) && (requested.Pos.X != 0 || requested.Pos.Y != 0) {
		return v.snapPosition(requested.Pos)
	}
	if requested.Cell.X < 0 || requested.Cell.Y < 0 || requested.Cell.X >= v.terrain.Cols || requested.Cell.Y >= v.terrain.Rows {
		return requested.Cell, ErrOutOfBounds
	}
	return requested.Cell, nil
}

func (v *BuildPlacementValidator) inBounds(cell Cell, width, depth int) bool {
	return inTerrainBounds(cell, width, depth, v.terrain)
}

func inTerrainBounds(cell Cell, width, depth int, terrain TerrainView) bool {
	if cell.X < 0 || cell.Y < 0 || cell.X >= terrain.Cols || cell.Y >= terrain.Rows {
		return false
	}
	return width <= terrain.Cols-cell.X && depth <= terrain.Rows-cell.Y
}

func makeCells(anchor Cell, width, depth int) []Cell {
	cells := make([]Cell, 0, width*depth)
	for y := anchor.Y; y < anchor.Y+depth; y++ {
		for x := anchor.X; x < anchor.X+width; x++ {
			cells = append(cells, Cell{X: x, Y: y})
		}
	}
	return cells
}

func (v *BuildPlacementValidator) snappedPosition(cell Cell) Vec3 {
	return Vec3{
		X: float64(cell.X) * v.grid,
		Y: float64(cell.Y) * v.grid,
		Z: v.terrainHeightClamped(cell.X, cell.Y),
	}
}

func (v *BuildPlacementValidator) terrainHeightClamped(x, y int) float64 {
	if x < 0 {
		x = 0
	} else if x >= v.terrain.Cols {
		x = v.terrain.Cols - 1
	}
	if y < 0 {
		y = 0
	} else if y >= v.terrain.Rows {
		y = v.terrain.Rows - 1
	}
	return v.terrain.Heights[y*v.terrain.Cols+x]
}

func maxSlopeForCells(cells []Cell, terrain TerrainView, grid float64) float64 {
	if len(cells) == 0 {
		return 0
	}
	minX, maxX := cells[0].X, cells[0].X
	minY, maxY := cells[0].Y, cells[0].Y
	for _, cell := range cells[1:] {
		if cell.X < minX {
			minX = cell.X
		}
		if cell.X > maxX {
			maxX = cell.X
		}
		if cell.Y < minY {
			minY = cell.Y
		}
		if cell.Y > maxY {
			maxY = cell.Y
		}
	}

	height := func(x, y int) float64 {
		if x < 0 {
			x = 0
		} else if x >= terrain.Cols {
			x = terrain.Cols - 1
		}
		if y < 0 {
			y = 0
		} else if y >= terrain.Rows {
			y = terrain.Rows - 1
		}
		return terrain.Heights[y*terrain.Cols+x]
	}

	corners := [][2]int{{minX, minY}, {maxX, minY}, {minX, maxY}, {maxX, maxY}}
	steepest := 0.0
	for _, corner := range corners {
		h := height(corner[0], corner[1])
		dx := math.Abs(height(corner[0]+1, corner[1])-h) / grid
		dy := math.Abs(height(corner[0], corner[1]+1)-h) / grid
		if dx > steepest {
			steepest = dx
		}
		if dy > steepest {
			steepest = dy
		}
	}
	return steepest
}

func (v *BuildPlacementValidator) occupantLocked(cell Cell, layer int) (PlacedID, bool) {
	for id, piece := range v.placed {
		if piece.placement.Layer != layer {
			continue
		}
		for _, occupied := range piece.placement.Cells {
			if occupied == cell {
				return id, true
			}
		}
	}
	return "", false
}

func buildOccupancy(placed map[PlacedID]*placedPiece) map[cellKey]PlacedID {
	occupancy := make(map[cellKey]PlacedID)
	for id, piece := range placed {
		for _, cell := range piece.placement.Cells {
			occupancy[cellKey{layer: piece.placement.Layer, cell: cell}] = id
		}
	}
	return occupancy
}

func cellsIntersect(a, b []Cell) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	for _, left := range a {
		for _, right := range b {
			if left == right {
				return true
			}
		}
	}
	return false
}

func highestOverlappingLayer(cells []Cell, placed map[PlacedID]*placedPiece) (int, bool) {
	highest := -1
	for _, piece := range placed {
		if cellsIntersect(cells, piece.placement.Cells) && piece.placement.Layer > highest {
			highest = piece.placement.Layer
		}
	}
	return highest, highest >= 0
}

func isSupported(id PlacedID, placed map[PlacedID]*placedPiece) bool {
	return isSupportedVisited(id, placed, make(map[PlacedID]bool))
}

func isSupportedVisited(id PlacedID, placed map[PlacedID]*placedPiece, visiting map[PlacedID]bool) bool {
	piece, ok := placed[id]
	if !ok || visiting[id] {
		return false
	}
	if piece.placement.Def.Foundation {
		return piece.placement.Layer == 0
	}
	if piece.placement.Layer <= 0 {
		return false
	}
	visiting[id] = true
	defer delete(visiting, id)
	for otherID, other := range placed {
		if other.placement.Layer != piece.placement.Layer-1 || !cellsIntersect(piece.placement.Cells, other.placement.Cells) {
			continue
		}
		if isSupportedVisited(otherID, placed, visiting) {
			return true
		}
	}
	return false
}

func loadOn(id PlacedID, placed map[PlacedID]*placedPiece) int {
	support := placed[id]
	if support == nil {
		return 0
	}
	load := 0
	for _, other := range placed {
		if other.placement.Layer <= support.placement.Layer {
			continue
		}
		if cellsIntersect(support.placement.Cells, other.placement.Cells) {
			load += other.placement.Def.Mass
		}
	}
	return load
}

func firstOverloaded(placed map[PlacedID]*placedPiece) (PlacedID, bool) {
	ids := make([]PlacedID, 0, len(placed))
	for id := range placed {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		piece := placed[id]
		if loadOn(id, placed) > piece.placement.Def.MaxLoad {
			return id, true
		}
	}
	return "", false
}

func (v *BuildPlacementValidator) supportBelowLocked(piece *placedPiece, visited map[PlacedID]bool) (PlacedID, *placedPiece, bool) {
	candidates := make([]PlacedID, 0)
	for id, candidate := range v.placed {
		if visited[id] || candidate.placement.Layer != piece.placement.Layer-1 {
			continue
		}
		if cellsIntersect(piece.placement.Cells, candidate.placement.Cells) && isSupported(id, v.placed) {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return "", nil, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	id := candidates[0]
	return id, v.placed[id], true
}

func (v *BuildPlacementValidator) removeUnsupportedLocked() int {
	removed := 0
	for {
		unsupported := make([]PlacedID, 0)
		for id := range v.placed {
			if !isSupported(id, v.placed) {
				unsupported = append(unsupported, id)
			}
		}
		if len(unsupported) == 0 {
			return removed
		}
		sort.Slice(unsupported, func(i, j int) bool { return unsupported[i] < unsupported[j] })
		for _, id := range unsupported {
			delete(v.placed, id)
			removed++
		}
	}
}

func (v *BuildPlacementValidator) trimUndoLocked() {
	if v.limits.MaxUndoDepth <= 0 {
		v.undo = nil
		return
	}
	if len(v.undo) <= v.limits.MaxUndoDepth {
		return
	}
	keepFrom := len(v.undo) - v.limits.MaxUndoDepth
	v.undo = append([]batchRecord(nil), v.undo[keepFrom:]...)
}

func decodeSnapshot(data []byte) (snapshotPayload, error) {
	var payload snapshotPayload
	if len(data) < len(snapshotMagic)+64+1 || string(data[:len(snapshotMagic)]) != snapshotMagic {
		return payload, ErrBadSnapshot
	}
	rest := data[len(snapshotMagic):]
	if len(rest) < 65 || rest[64] != '\n' {
		return payload, ErrBadSnapshot
	}
	want, err := hex.DecodeString(string(rest[:64]))
	if err != nil || len(want) != sha256.Size {
		return payload, ErrBadSnapshot
	}
	raw := rest[65:]
	got := sha256.Sum256(raw)
	if subtle.ConstantTimeCompare(want, got[:]) != 1 {
		return payload, ErrBadSnapshot
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, ErrBadSnapshot
	}
	if payload.Version != snapshotVersion {
		return payload, ErrBadSnapshot
	}
	return payload, nil
}

func (v *BuildPlacementValidator) validateSnapshotLocked(payload snapshotPayload) (map[PlacedID]*placedPiece, []batchRecord, error) {
	if !isFinite(payload.Grid) || payload.Grid <= 0 || payload.NextID <= 0 {
		return nil, nil, ErrBadSnapshot
	}
	if err := validateTerrain(payload.Terrain); err != nil {
		return nil, nil, ErrBadSnapshot
	}
	if payload.Counters.Batches < 0 || payload.Counters.Conflicts < 0 || payload.Counters.Removed < 0 || payload.Counters.Cascaded < 0 {
		return nil, nil, ErrBadSnapshot
	}
	pieceCap := v.limits.MaxPieces
	if pieceCap < 0 {
		pieceCap = 0
	}
	if len(payload.Placed) > pieceCap {
		return nil, nil, ErrBadSnapshot
	}
	undoCap := v.limits.MaxUndoDepth
	if undoCap < 0 {
		undoCap = 0
	}
	if len(payload.Undo) > undoCap {
		return nil, nil, ErrBadSnapshot
	}

	placed := make(map[PlacedID]*placedPiece, len(payload.Placed))
	occupancy := make(map[cellKey]struct{})
	for _, saved := range payload.Placed {
		if saved.ID == "" || strings.TrimSpace(saved.Placement.BatchID) == "" {
			return nil, nil, ErrBadSnapshot
		}
		if _, exists := placed[saved.ID]; exists {
			return nil, nil, ErrBadSnapshot
		}
		if err := validateItem(saved.Placement.Def, v.limits.MaxFootprint); err != nil {
			return nil, nil, ErrBadSnapshot
		}
		if !validRotation(saved.Placement.Rot) {
			return nil, nil, ErrBadSnapshot
		}
		width, depth := rotatedDims(saved.Placement.Def, saved.Placement.Rot)
		cell := saved.Placement.Cell
		if !inTerrainBounds(cell, width, depth, payload.Terrain) {
			return nil, nil, ErrBadSnapshot
		}
		cells := makeCells(cell, width, depth)
		if saved.Placement.Def.Foundation {
			if saved.Placement.Layer != 0 {
				return nil, nil, ErrBadSnapshot
			}
		} else if saved.Placement.Layer <= 0 {
			return nil, nil, ErrBadSnapshot
		}
		if !sameCellSet(cells, saved.Placement.Cells) {
			return nil, nil, ErrBadSnapshot
		}
		for _, footprintCell := range cells {
			key := cellKey{layer: saved.Placement.Layer, cell: footprintCell}
			if _, exists := occupancy[key]; exists {
				return nil, nil, ErrBadSnapshot
			}
			occupancy[key] = struct{}{}
		}
		placement := clonePlacement(saved.Placement)
		placement.Pos = v.snappedPositionFor(cell, payload.Grid, payload.Terrain)
		placed[saved.ID] = &placedPiece{id: saved.ID, placement: placement}
	}

	for id := range placed {
		if !isSupported(id, placed) {
			return nil, nil, ErrBadSnapshot
		}
	}
	if _, overloaded := firstOverloaded(placed); overloaded {
		return nil, nil, ErrBadSnapshot
	}

	seenUndoIDs := make(map[PlacedID]struct{})
	undo := make([]batchRecord, 0, len(payload.Undo))
	for _, saved := range payload.Undo {
		if strings.TrimSpace(saved.BatchID) == "" || len(saved.IDs) == 0 {
			return nil, nil, ErrBadSnapshot
		}
		batchCap := v.limits.MaxBatch
		if batchCap < 0 {
			batchCap = 0
		}
		if len(saved.IDs) > batchCap {
			return nil, nil, ErrBadSnapshot
		}
		ids := append([]PlacedID(nil), saved.IDs...)
		for _, id := range ids {
			if id == "" {
				return nil, nil, ErrBadSnapshot
			}
			if _, exists := seenUndoIDs[id]; exists {
				return nil, nil, ErrBadSnapshot
			}
			seenUndoIDs[id] = struct{}{}
		}
		undo = append(undo, batchRecord{BatchID: saved.BatchID, IDs: ids})
	}
	return placed, undo, nil
}

func sameCellSet(left, right []Cell) bool {
	if len(left) != len(right) {
		return false
	}
	want := make(map[Cell]int, len(left))
	for _, cell := range left {
		want[cell]++
	}
	for _, cell := range right {
		if want[cell] == 0 {
			return false
		}
		want[cell]--
	}
	return true
}

func (v *BuildPlacementValidator) snappedPositionFor(cell Cell, grid float64, terrain TerrainView) Vec3 {
	x := cell.X
	y := cell.Y
	if x < 0 {
		x = 0
	} else if x >= terrain.Cols {
		x = terrain.Cols - 1
	}
	if y < 0 {
		y = 0
	} else if y >= terrain.Rows {
		y = terrain.Rows - 1
	}
	return Vec3{
		X: float64(cell.X) * grid,
		Y: float64(cell.Y) * grid,
		Z: terrain.Heights[y*terrain.Cols+x],
	}
}
