// Package task edits terrain splat weights with brush strokes that are staged
// as ordered dabs, committed per terrain block and normalised in fixed point.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// StrokeID identifies one brush stroke for the lifetime of a brush.
type StrokeID int64

// BlockCoord addresses one terrain splat block.
type BlockCoord struct {
	BX int `json:"bx"`
	BY int `json:"by"`
}

// Splat layer indices. Every pixel carries one weight per layer.
const (
	LayerRock  = 0
	LayerGrass = 1
	LayerSand  = 2
	LayerSnow  = 3
	LayerCount = 4
)

// WeightScale is the fixed point denominator used when a stroke is committed.
const WeightScale = 65535

// bytesPerCell is the size of one stored weight cell; a dirty block keeps a
// before image and an after image of BlockSize*BlockSize*LayerCount cells.
const bytesPerCell = 4

// StrokeStart is what the input layer sends before the first dab of a stroke.
type StrokeStart struct {
	Terrain int     `json:"terrain"`
	X       int     `json:"x"`
	Y       int     `json:"y"`
	Layer   int     `json:"layer"`
	Radius  float32 `json:"radius"`
	Spacing float32 `json:"spacing"`
	Preset  string  `json:"preset"`
}

// Dab is one stamp of the brush, delivered in stroke order.
type Dab struct {
	Seq    int     `json:"seq"`
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Layer  int     `json:"layer"`
	Weight float32 `json:"weight"`
	Radius float32 `json:"radius"`
}

// StrokeResult summarises one committed stroke.
type StrokeResult struct {
	Stroke StrokeID     `json:"stroke"`
	Dabs   int          `json:"dabs"`
	Blocks []BlockCoord `json:"blocks"`
	Bytes  int          `json:"bytes"`
}

// BrushLimits bounds one brush. Zero values mean "use the default".
type BrushLimits struct {
	BlockSize               int `json:"block_size"`
	MaxDirtyBlocksPerStroke int `json:"max_dirty_blocks_per_stroke"`
	MaxDabsPerStroke        int `json:"max_dabs_per_stroke"`
	MaxSnapshotBytes        int `json:"max_snapshot_bytes"`
}

// BrushStats is the per brush counter block.
type BrushStats struct {
	Active        int `json:"active"`
	Committed     int `json:"committed"`
	Cancelled     int `json:"cancelled"`
	Undone        int `json:"undone"`
	DuplicateDabs int `json:"duplicate_dabs"`
	DerivedBlocks int `json:"derived_blocks"`
	DirtyBlocks   int `json:"dirty_blocks"`
	SnapshotBytes int `json:"snapshot_bytes"`
}

// Errors returned by the brush.
var (
	ErrUnknownStroke = errors.New("stroke id is not known to this brush")
	ErrStrokeClosed  = errors.New("stroke is not in the required state")
	ErrBadDab        = errors.New("dab is not usable")
	ErrBadLayer      = errors.New("layer index is outside the splat set")
	ErrDabLimit      = errors.New("dab limit reached for this stroke")
	ErrDirtyLimit    = errors.New("dirty block limit reached for this stroke")
	ErrSnapshotLimit = errors.New("undo snapshot budget exceeded for this stroke")
	ErrUnknownBlock  = errors.New("block is not dirty")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// TerrainSplatBrush owns the splat weights, the stroke staging area and the
// per block undo images.
type TerrainSplatBrush struct {
	limits BrushLimits
}

// NewTerrainSplatBrush builds a brush with the given caps.
func NewTerrainSplatBrush(limits BrushLimits) *TerrainSplatBrush {
	return &TerrainSplatBrush{limits: limits}
}

// Limits reports the limits in force, defaults included.
func (b *TerrainSplatBrush) Limits() BrushLimits {
	return b.limits
}

// BeginStroke opens a stroke and hands back its identifier.
func (b *TerrainSplatBrush) BeginStroke(st StrokeStart) (StrokeID, error) {
	return 0, errNotImplemented
}

// AddDab appends one stamp to an open stroke.
func (b *TerrainSplatBrush) AddDab(id StrokeID, d Dab) error {
	return errNotImplemented
}

// EndStroke normalises and commits the staged dabs of one stroke.
func (b *TerrainSplatBrush) EndStroke(id StrokeID) (StrokeResult, error) {
	return StrokeResult{}, errNotImplemented
}

// CancelStroke discards an open stroke or rolls back a committed one.
func (b *TerrainSplatBrush) CancelStroke(id StrokeID) error {
	return errNotImplemented
}

// SampleWeight reads the committed normalised weight of one pixel layer.
func (b *TerrainSplatBrush) SampleWeight(x, y, layer int) (float32, error) {
	return 0, errNotImplemented
}

// DirtyBlocks lists the blocks that carry uncommitted undo images.
func (b *TerrainSplatBrush) DirtyBlocks() []BlockCoord {
	return nil
}

// FlushDerived marks blocks whose normals and grass layer have been derived.
func (b *TerrainSplatBrush) FlushDerived(blocks []BlockCoord) error {
	return errNotImplemented
}

// Undo rolls a committed stroke back to its before image.
func (b *TerrainSplatBrush) Undo(id StrokeID) error {
	return errNotImplemented
}

// ActiveStrokes lists the stroke identifiers that are still open, ascending.
func (b *TerrainSplatBrush) ActiveStrokes() []StrokeID {
	return nil
}

// Stats reports the counters of the brush.
func (b *TerrainSplatBrush) Stats() BrushStats {
	return BrushStats{}
}

// Snapshot serialises weights, dirty blocks and stroke records.
func (b *TerrainSplatBrush) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (b *TerrainSplatBrush) Restore(data []byte) error {
	return errNotImplemented
}
