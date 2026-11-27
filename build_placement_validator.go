// Package task validates build placement on a snapped grid: it previews the
// footprint of one piece, commits a batch atomically, walks the support chain
// up from the ground and carries the placed set plus the undo stack across a
// reload.
package task

import (
	"errors"
	"fmt"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

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

// BuildPlacementValidator owns the placed set and the undo stack.
type BuildPlacementValidator struct {
	limits  BuildLimits
	grid    float64
	terrain TerrainView
}

// NewBuildPlacementValidator builds a validator with the given budgets and a
// flat 32 by 32 default terrain.
func NewBuildPlacementValidator(limits BuildLimits) *BuildPlacementValidator {
	return &BuildPlacementValidator{limits: limits, grid: 1}
}

// SetGrid changes the grid cell size in metres.
func (v *BuildPlacementValidator) SetGrid(cell float64) error {
	return errNotImplemented
}

// SetTerrain replaces the height field.
func (v *BuildPlacementValidator) SetTerrain(t TerrainView) error {
	return errNotImplemented
}

// Preview snaps one piece onto the grid and checks its footprint.
func (v *BuildPlacementValidator) Preview(def ItemDef, pos Vec3, rot int) (Placement, error) {
	return Placement{}, errNotImplemented
}

// Commit places a whole batch or nothing at all.
func (v *BuildPlacementValidator) Commit(batch []Placement) ([]PlacedID, error) {
	return nil, errNotImplemented
}

// Remove deletes one committed piece.
func (v *BuildPlacementValidator) Remove(id PlacedID) error {
	return errNotImplemented
}

// CheckSupport walks the support chain of one committed piece.
func (v *BuildPlacementValidator) CheckSupport(id PlacedID) (SupportReport, error) {
	return SupportReport{}, errNotImplemented
}

// Undo removes the pieces of the latest batch.
func (v *BuildPlacementValidator) Undo(batchID string) error {
	return errNotImplemented
}

// Stats reads the counters of the validator.
func (v *BuildPlacementValidator) Stats() BuildStats {
	return BuildStats{}
}

// Snapshot serialises the placed set and the undo stack.
func (v *BuildPlacementValidator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (v *BuildPlacementValidator) Restore(data []byte) error {
	return errNotImplemented
}
