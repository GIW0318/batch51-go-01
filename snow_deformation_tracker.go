// Package task keeps a tiled snow height field: brushes press the surface
// down, shove part of the displaced snow to the rim of the footprint and mark
// the touched tiles dirty so the renderer uploads them within a byte budget.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Errors returned by the tracker.
var (
	ErrBadConfig      = errors.New("snow config is invalid")
	ErrBadHeightField = errors.New("base height field does not match the configured grid")
	ErrNotLoaded      = errors.New("base height field is not loaded")
	ErrOutOfBounds    = errors.New("position is outside the height field")
	ErrBadPressure    = errors.New("pressure must be inside (0,1]")
	ErrBadBrush       = errors.New("brush parameters are invalid")
	ErrBadDuration    = errors.New("relax duration must not be negative")
	ErrBadLimit       = errors.New("flush limit must be positive")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Vec2 is a position on the snow plane in metres.
type Vec2 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Vec3 is a world position; the components are plain floats and X and Y are
// the ones read by the brush routines.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Brush describes the footprint of one footprint stamp.
type Brush struct {
	Radius    float64 `json:"radius"`
	RimRatio  float64 `json:"rim_ratio"`
	RimSpread float64 `json:"rim_spread"`
}

// SnowConfig fixes the grid, the depth and rim ceilings and the upload budget.
type SnowConfig struct {
	TilesX       int     `json:"tiles_x"`
	TilesY       int     `json:"tiles_y"`
	TileCells    int     `json:"tile_cells"`
	CellSize     float64 `json:"cell_size"`
	MaxDepth     float64 `json:"max_depth"`
	MaxRim       float64 `json:"max_rim"`
	RecoverRate  float64 `json:"recover_rate"`
	UploadBudget int     `json:"upload_budget"`
}

// HeightField is the undeformed terrain handed in by the level loader.
type HeightField struct {
	Version uint64    `json:"version"`
	Width   int       `json:"width"`
	Height  int       `json:"height"`
	Heights []float64 `json:"heights"`
}

// TileDelta describes one dirty tile that is ready to be uploaded.
type TileDelta struct {
	TileX   int    `json:"tile_x"`
	TileY   int    `json:"tile_y"`
	Version uint64 `json:"version"`
	Bytes   int    `json:"bytes"`
}

// SnowDeformation owns the base field, the dent map, the rim map and the
// dirty tile queue.
type SnowDeformation struct {
	cfg SnowConfig
}

// NewSnowDeformation builds a tracker over the configured tile grid.
func NewSnowDeformation(cfg SnowConfig) *SnowDeformation {
	return &SnowDeformation{cfg: cfg}
}

// Load installs a base height field and clears every dent, rim and dirty tile.
func (d *SnowDeformation) Load(base HeightField) error {
	return errNotImplemented
}

// Stamp presses one brush footprint into the snow at the given position.
func (d *SnowDeformation) Stamp(brush Brush, at Vec3, pressure float64) error {
	return errNotImplemented
}

// Stroke walks from one sample point to another and stamps the brush at every
// intermediate step so a fast moving vehicle leaves a continuous groove.
func (d *SnowDeformation) Stroke(from, to Vec3, pressure float64, steps int) error {
	return errNotImplemented
}

// SampleHeight reads the current surface height, base plus rim minus dent.
func (d *SnowDeformation) SampleHeight(pos Vec2) (float64, error) {
	return 0, errNotImplemented
}

// DepthAt reads the accumulated dent depth of one cell.
func (d *SnowDeformation) DepthAt(pos Vec2) (float64, error) {
	return 0, errNotImplemented
}

// RimHeight reads the accumulated rim snow of one cell.
func (d *SnowDeformation) RimHeight(pos Vec2) (float64, error) {
	return 0, errNotImplemented
}

// Totals reports the summed dent depth and the summed rim snow of the field.
func (d *SnowDeformation) Totals() (float64, float64) {
	return 0, 0
}

// Relax lets the snow creep back and removes dent depth.
func (d *SnowDeformation) Relax(dt time.Duration) error {
	return errNotImplemented
}

// FlushTiles returns up to limit dirty tiles, oldest dirty tile first.
func (d *SnowDeformation) FlushTiles(limit int) ([]TileDelta, error) {
	return nil, errNotImplemented
}

// PendingTiles counts the tiles that are still waiting for an upload.
func (d *SnowDeformation) PendingTiles() int {
	return 0
}

// TileVersion reads the current version counter of one tile.
func (d *SnowDeformation) TileVersion(tx, ty int) (uint64, error) {
	return 0, errNotImplemented
}

// Snapshot serialises the base version, the dent and rim maps and the dirty
// tile queue.
func (d *SnowDeformation) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (d *SnowDeformation) Restore(data []byte) error {
	return errNotImplemented
}
