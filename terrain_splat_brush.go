// Package task edits terrain splat weights with brush strokes that are staged
// as ordered dabs, committed per terrain block and normalised in fixed point.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
)

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

const (
	defaultBlockSize               = 16
	defaultMaxDirtyBlocksPerStroke = 32
	defaultMaxDabsPerStroke        = 256
	defaultMaxSnapshotBytes        = 1 << 20

	snapshotMagic   = "terrain-splat-brush"
	snapshotVersion = 1
)

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

type pixelKey struct {
	X int
	Y int
}

type pixelWeights [LayerCount]uint16

type strokeState uint8

const (
	strokeActive strokeState = iota
	strokeCommitted
	strokeUndone
	strokeCancelled
	strokeFailed
)

func (s strokeState) String() string {
	switch s {
	case strokeActive:
		return "active"
	case strokeCommitted:
		return "committed"
	case strokeUndone:
		return "undone"
	case strokeCancelled:
		return "cancelled"
	case strokeFailed:
		return "failed"
	default:
		return "invalid"
	}
}

func parseStrokeState(s string) (strokeState, bool) {
	switch s {
	case "active":
		return strokeActive, true
	case "committed":
		return strokeCommitted, true
	case "undone":
		return strokeUndone, true
	case "cancelled":
		return strokeCancelled, true
	case "failed":
		return strokeFailed, true
	default:
		return 0, false
	}
}

type strokeRecord struct {
	ID          StrokeID
	Start       StrokeStart
	Dabs        []Dab
	SeenSeqs    map[int]struct{}
	State       strokeState
	Blocks      []BlockCoord
	Before      map[BlockCoord][]uint32
	After       map[BlockCoord][]uint32
	Bytes       int
	CommitOrder int64
}

// TerrainSplatBrush owns the splat weights, the stroke staging area and the
// per block undo images.
type TerrainSplatBrush struct {
	mu sync.Mutex

	limits BrushLimits

	nextStroke StrokeID
	nextCommit int64

	baseWeights map[pixelKey]pixelWeights
	weights     map[pixelKey]pixelWeights
	dirty       map[BlockCoord]struct{}
	derived     map[BlockCoord]struct{}
	records     map[StrokeID]*strokeRecord

	committed     int
	cancelled     int
	undone        int
	duplicateDabs int
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotCounters struct {
	Committed     int `json:"committed"`
	Cancelled     int `json:"cancelled"`
	Undone        int `json:"undone"`
	DuplicateDabs int `json:"duplicate_dabs"`
}

type snapshotPixel struct {
	X int          `json:"x"`
	Y int          `json:"y"`
	W pixelWeights `json:"w"`
}

type snapshotBlock struct {
	BX    int      `json:"bx"`
	BY    int      `json:"by"`
	Cells []uint32 `json:"cells"`
}

type snapshotStroke struct {
	ID          StrokeID        `json:"id"`
	Start       StrokeStart     `json:"start"`
	Dabs        []Dab           `json:"dabs"`
	State       string          `json:"state"`
	Blocks      []BlockCoord    `json:"blocks"`
	Before      []snapshotBlock `json:"before"`
	After       []snapshotBlock `json:"after"`
	Bytes       int             `json:"bytes"`
	CommitOrder int64           `json:"commit_order"`
}

type snapshotPayload struct {
	BlockSize  int              `json:"block_size"`
	Limits     BrushLimits      `json:"limits"`
	NextStroke StrokeID         `json:"next_stroke"`
	NextCommit int64            `json:"next_commit"`
	Base       []snapshotPixel  `json:"base"`
	Weights    []snapshotPixel  `json:"weights"`
	Dirty      []BlockCoord     `json:"dirty"`
	Derived    []BlockCoord     `json:"derived"`
	Records    []snapshotStroke `json:"records"`
	Counters   snapshotCounters `json:"counters"`
}

// NewTerrainSplatBrush builds a brush with the given caps.
func NewTerrainSplatBrush(limits BrushLimits) *TerrainSplatBrush {
	return &TerrainSplatBrush{
		limits:      effectiveLimits(limits),
		nextStroke:  1,
		nextCommit:  1,
		baseWeights: make(map[pixelKey]pixelWeights),
		weights:     make(map[pixelKey]pixelWeights),
		dirty:       make(map[BlockCoord]struct{}),
		derived:     make(map[BlockCoord]struct{}),
		records:     make(map[StrokeID]*strokeRecord),
	}
}

func effectiveLimits(limits BrushLimits) BrushLimits {
	if limits.BlockSize <= 0 {
		limits.BlockSize = defaultBlockSize
	}
	if limits.MaxDirtyBlocksPerStroke <= 0 {
		limits.MaxDirtyBlocksPerStroke = defaultMaxDirtyBlocksPerStroke
	}
	if limits.MaxDabsPerStroke <= 0 {
		limits.MaxDabsPerStroke = defaultMaxDabsPerStroke
	}
	if limits.MaxSnapshotBytes <= 0 {
		limits.MaxSnapshotBytes = defaultMaxSnapshotBytes
	}
	return limits
}

func validLayer(layer int) bool {
	return layer >= 0 && layer < LayerCount
}

func validPositiveFloat(v float32) bool {
	return v > 0 && !float32IsSpecial(v)
}

func validWeight(v float32) bool {
	return v >= 0 && !float32IsSpecial(v)
}

func float32IsSpecial(v float32) bool {
	return math.IsNaN(float64(v)) || math.IsInf(float64(v), 0)
}

func validStart(st StrokeStart) error {
	if !validLayer(st.Layer) {
		return ErrBadLayer
	}
	if st.Terrain < 0 || st.X < 0 || st.Y < 0 ||
		!validPositiveFloat(st.Radius) || !validPositiveFloat(st.Spacing) {
		return ErrBadDab
	}
	return nil
}

func validDab(d Dab) error {
	if !validLayer(d.Layer) {
		return ErrBadLayer
	}
	if d.X < 0 || d.Y < 0 || !validPositiveFloat(d.Radius) || !validWeight(d.Weight) {
		return ErrBadDab
	}
	return nil
}

// Limits reports the limits in force, defaults included.
func (b *TerrainSplatBrush) Limits() BrushLimits {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limits
}

// BeginStroke opens a stroke and hands back its identifier.
func (b *TerrainSplatBrush) BeginStroke(st StrokeStart) (StrokeID, error) {
	if err := validStart(st); err != nil {
		return 0, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextStroke
	b.nextStroke++
	b.records[id] = &strokeRecord{
		ID:       id,
		Start:    st,
		SeenSeqs: make(map[int]struct{}),
		State:    strokeActive,
	}
	return id, nil
}

// AddDab appends one stamp to an open stroke.
func (b *TerrainSplatBrush) AddDab(id StrokeID, d Dab) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	rec, ok := b.records[id]
	if !ok {
		return ErrUnknownStroke
	}
	if rec.State != strokeActive {
		return ErrStrokeClosed
	}
	if err := validDab(d); err != nil {
		return err
	}
	if _, duplicate := rec.SeenSeqs[d.Seq]; duplicate {
		b.duplicateDabs++
		return nil
	}
	if len(rec.Dabs) >= b.limits.MaxDabsPerStroke {
		return ErrDabLimit
	}

	rec.Dabs = append(rec.Dabs, d)
	rec.SeenSeqs[d.Seq] = struct{}{}
	return nil
}

// EndStroke normalises and commits the staged dabs of one stroke.
func (b *TerrainSplatBrush) EndStroke(id StrokeID) (StrokeResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	rec, ok := b.records[id]
	if !ok {
		return StrokeResult{}, ErrUnknownStroke
	}
	if rec.State != strokeActive {
		return StrokeResult{}, ErrStrokeClosed
	}

	updates := stagedUpdates(b.weights, rec.Dabs)
	blocks := blocksForUpdates(updates, b.limits.BlockSize)
	if len(blocks) > b.limits.MaxDirtyBlocksPerStroke {
		rec.State = strokeFailed
		return StrokeResult{}, ErrDirtyLimit
	}

	blockBytes := int64(b.limits.BlockSize) * int64(b.limits.BlockSize) *
		int64(LayerCount) * int64(bytesPerCell) * 2
	totalBytes := int64(len(blocks)) * blockBytes
	if totalBytes > int64(b.limits.MaxSnapshotBytes) {
		rec.State = strokeFailed
		return StrokeResult{}, ErrSnapshotLimit
	}

	before := make(map[BlockCoord][]uint32, len(blocks))
	after := make(map[BlockCoord][]uint32, len(blocks))
	for _, block := range blocks {
		before[block] = captureBlock(b.weights, nil, block, b.limits.BlockSize)
		after[block] = captureBlock(b.weights, updates, block, b.limits.BlockSize)
	}

	for pixel, values := range updates {
		b.weights[pixel] = values
	}
	for _, block := range blocks {
		b.dirty[block] = struct{}{}
		delete(b.derived, block)
	}

	rec.State = strokeCommitted
	rec.Blocks = append([]BlockCoord(nil), blocks...)
	rec.Before = before
	rec.After = after
	rec.Bytes = int(totalBytes)
	rec.CommitOrder = b.nextCommit
	b.nextCommit++
	b.committed++

	return StrokeResult{
		Stroke: id,
		Dabs:   len(rec.Dabs),
		Blocks: append([]BlockCoord(nil), blocks...),
		Bytes:  int(totalBytes),
	}, nil
}

// CancelStroke discards an open stroke or rolls back a committed one.
func (b *TerrainSplatBrush) CancelStroke(id StrokeID) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	rec, ok := b.records[id]
	if !ok {
		return ErrUnknownStroke
	}

	switch rec.State {
	case strokeActive:
		rec.State = strokeCancelled
		b.cancelled++
		return nil
	case strokeCommitted:
		for _, block := range rec.Blocks {
			delete(b.derived, block)
		}
		rec.State = strokeCancelled
		b.cancelled++
		b.rebuildLocked()
		return nil
	default:
		return ErrUnknownStroke
	}
}

// SampleWeight reads the committed normalised weight of one pixel layer.
func (b *TerrainSplatBrush) SampleWeight(x, y, layer int) (float32, error) {
	if !validLayer(layer) {
		return 0, ErrBadLayer
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if x < 0 || y < 0 {
		return 0, nil
	}
	values := b.weights[pixelKey{X: x, Y: y}]
	return float32(values[layer]) / float32(WeightScale), nil
}

// DirtyBlocks lists the blocks that carry uncommitted undo images.
func (b *TerrainSplatBrush) DirtyBlocks() []BlockCoord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return sortedBlockSet(b.dirty)
}

// FlushDerived marks blocks whose normals and grass layer have been derived.
func (b *TerrainSplatBrush) FlushDerived(blocks []BlockCoord) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, block := range blocks {
		if _, ok := b.dirty[block]; !ok {
			return ErrUnknownBlock
		}
	}
	for _, block := range blocks {
		b.derived[block] = struct{}{}
	}
	return nil
}

// Undo rolls a committed stroke back to its before image.
func (b *TerrainSplatBrush) Undo(id StrokeID) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	rec, ok := b.records[id]
	if !ok {
		return ErrUnknownStroke
	}
	if rec.State != strokeCommitted {
		if rec.State == strokeActive {
			return ErrStrokeClosed
		}
		return ErrUnknownStroke
	}

	for _, block := range rec.Blocks {
		delete(b.derived, block)
	}
	rec.State = strokeUndone
	b.undone++
	b.rebuildLocked()
	return nil
}

// ActiveStrokes lists the stroke identifiers that are still open, ascending.
func (b *TerrainSplatBrush) ActiveStrokes() []StrokeID {
	b.mu.Lock()
	defer b.mu.Unlock()

	ids := make([]StrokeID, 0)
	for id, rec := range b.records {
		if rec.State == strokeActive {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Stats reports the counters of the brush.
func (b *TerrainSplatBrush) Stats() BrushStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	active := 0
	for _, rec := range b.records {
		if rec.State == strokeActive {
			active++
		}
	}
	return BrushStats{
		Active:        active,
		Committed:     b.committed,
		Cancelled:     b.cancelled,
		Undone:        b.undone,
		DuplicateDabs: b.duplicateDabs,
		DerivedBlocks: len(b.derived),
		DirtyBlocks:   len(b.dirty),
		SnapshotBytes: b.snapshotBytesLocked(),
	}
}

// Snapshot serialises weights, dirty blocks and stroke records.
func (b *TerrainSplatBrush) Snapshot() ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	payload := b.snapshotPayloadLocked()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Payload:  payloadBytes,
		Checksum: snapshotChecksum(snapshotVersion, payloadBytes),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (b *TerrainSplatBrush) Restore(data []byte) error {
	loaded, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.limits = loaded.limits
	b.nextStroke = loaded.nextStroke
	b.nextCommit = loaded.nextCommit
	b.baseWeights = loaded.baseWeights
	b.weights = loaded.weights
	b.dirty = loaded.dirty
	b.derived = loaded.derived
	b.records = loaded.records
	b.committed = loaded.committed
	b.cancelled = loaded.cancelled
	b.undone = loaded.undone
	b.duplicateDabs = loaded.duplicateDabs
	return nil
}

func (b *TerrainSplatBrush) snapshotBytesLocked() int {
	total := 0
	for _, rec := range b.records {
		if rec.State == strokeCommitted {
			total += rec.Bytes
		}
	}
	return total
}

func (b *TerrainSplatBrush) rebuildLocked() {
	weights := cloneWeights(b.baseWeights)
	dirty := make(map[BlockCoord]struct{})

	records := make([]*strokeRecord, 0, len(b.records))
	for _, rec := range b.records {
		if rec.State == strokeCommitted {
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CommitOrder < records[j].CommitOrder })
	for _, rec := range records {
		applyDabs(weights, rec.Dabs)
		for _, block := range rec.Blocks {
			dirty[block] = struct{}{}
		}
	}

	b.weights = weights
	b.dirty = dirty
}

func stagedUpdates(current map[pixelKey]pixelWeights, dabs []Dab) map[pixelKey]pixelWeights {
	deltas := make(map[pixelKey][LayerCount]float64)
	effective := make([]Dab, 0, len(dabs))
	for _, dab := range dabs {
		if dab.Weight > 0 {
			effective = append(effective, dab)
		}
	}
	footprints := make([]map[pixelKey]struct{}, len(effective))
	for i, dab := range effective {
		footprints[i] = dabFootprint(dab)
	}

	used := make([]bool, len(effective))
	for start := range effective {
		if used[start] {
			continue
		}
		used[start] = true
		group := []int{start}
		pixels := clonePixelSet(footprints[start])
		for changed := true; changed; {
			changed = false
			for i := range effective {
				if used[i] || !pixelSetsIntersect(pixels, footprints[i]) {
					continue
				}
				used[i] = true
				group = append(group, i)
				addPixelSet(pixels, footprints[i])
				changed = true
			}
		}
		for key := range pixels {
			var values [LayerCount]float64
			for _, index := range group {
				dab := effective[index]
				values[dab.Layer] += float64(dab.Weight)
			}
			deltas[key] = values
		}
	}

	updates := make(map[pixelKey]pixelWeights, len(deltas))
	for key, delta := range deltas {
		before := current[key]
		var raw [LayerCount]float64
		for layer := 0; layer < LayerCount; layer++ {
			raw[layer] = float64(before[layer])/WeightScale + delta[layer]
		}
		updates[key] = normaliseRaw(raw)
	}
	return updates
}

func applyDabs(weights map[pixelKey]pixelWeights, dabs []Dab) {
	for key, values := range stagedUpdates(weights, dabs) {
		weights[key] = values
	}
}

func dabFootprint(dab Dab) map[pixelKey]struct{} {
	radius := float64(dab.Radius)
	radiusSquared := radius * radius
	extent := int(math.Ceil(radius))
	minX := dab.X - extent
	maxX := dab.X + extent
	minY := dab.Y - extent
	maxY := dab.Y + extent
	pixels := make(map[pixelKey]struct{})

	for y := minY; y <= maxY; y++ {
		if y < 0 {
			continue
		}
		dy := float64(y - dab.Y)
		for x := minX; x <= maxX; x++ {
			if x < 0 {
				continue
			}
			dx := float64(x - dab.X)
			if dx*dx+dy*dy > radiusSquared {
				continue
			}
			pixels[pixelKey{X: x, Y: y}] = struct{}{}
		}
	}
	return pixels
}

func clonePixelSet(source map[pixelKey]struct{}) map[pixelKey]struct{} {
	clone := make(map[pixelKey]struct{}, len(source))
	for key := range source {
		clone[key] = struct{}{}
	}
	return clone
}

func addPixelSet(target, source map[pixelKey]struct{}) {
	for key := range source {
		target[key] = struct{}{}
	}
}

func pixelSetsIntersect(a, b map[pixelKey]struct{}) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	for key := range a {
		if _, ok := b[key]; ok {
			return true
		}
	}
	return false
}

func normaliseRaw(raw [LayerCount]float64) pixelWeights {
	var total float64
	maxLayer := 0
	for layer := 0; layer < LayerCount; layer++ {
		if raw[layer] < 0 {
			raw[layer] = 0
		}
		total += raw[layer]
		if raw[layer] > raw[maxLayer] {
			maxLayer = layer
		}
	}
	if total <= 0 {
		return pixelWeights{}
	}

	var quantised [LayerCount]int64
	var sum int64
	for layer := 0; layer < LayerCount; layer++ {
		q := int64(math.Round(raw[layer] * WeightScale / total))
		quantised[layer] = q
		sum += q
	}
	quantised[maxLayer] += int64(WeightScale) - sum

	var result pixelWeights
	for layer := 0; layer < LayerCount; layer++ {
		result[layer] = uint16(quantised[layer])
	}
	return result
}

func blocksForUpdates(updates map[pixelKey]pixelWeights, blockSize int) []BlockCoord {
	set := make(map[BlockCoord]struct{})
	for key := range updates {
		set[BlockCoord{BX: key.X / blockSize, BY: key.Y / blockSize}] = struct{}{}
	}
	return sortedBlockSet(set)
}

func sortedBlockSet(set map[BlockCoord]struct{}) []BlockCoord {
	blocks := make([]BlockCoord, 0, len(set))
	for block := range set {
		blocks = append(blocks, block)
	}
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].BX != blocks[j].BX {
			return blocks[i].BX < blocks[j].BX
		}
		return blocks[i].BY < blocks[j].BY
	})
	return blocks
}

func captureBlock(weights map[pixelKey]pixelWeights, updates map[pixelKey]pixelWeights, block BlockCoord, blockSize int) []uint32 {
	cells := make([]uint32, blockSize*blockSize*LayerCount)
	baseX := block.BX * blockSize
	baseY := block.BY * blockSize
	for localY := 0; localY < blockSize; localY++ {
		for localX := 0; localX < blockSize; localX++ {
			key := pixelKey{X: baseX + localX, Y: baseY + localY}
			values := weights[key]
			if updates != nil {
				if updated, ok := updates[key]; ok {
					values = updated
				}
			}
			for layer := 0; layer < LayerCount; layer++ {
				index := ((localY*blockSize)+localX)*LayerCount + layer
				cells[index] = uint32(values[layer])
			}
		}
	}
	return cells
}

func cloneWeights(source map[pixelKey]pixelWeights) map[pixelKey]pixelWeights {
	clone := make(map[pixelKey]pixelWeights, len(source))
	for key, values := range source {
		clone[key] = values
	}
	return clone
}

func snapshotChecksum(version int, payload []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(snapshotMagic))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(fmt.Sprintf("%d", version)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return hex.EncodeToString(hash.Sum(nil))
}

func (b *TerrainSplatBrush) snapshotPayloadLocked() snapshotPayload {
	payload := snapshotPayload{
		BlockSize:  b.limits.BlockSize,
		Limits:     b.limits,
		NextStroke: b.nextStroke,
		NextCommit: b.nextCommit,
		Base:       snapshotPixels(b.baseWeights),
		Weights:    snapshotPixels(b.weights),
		Dirty:      sortedBlockSet(b.dirty),
		Derived:    sortedBlockSet(b.derived),
		Counters: snapshotCounters{
			Committed:     b.committed,
			Cancelled:     b.cancelled,
			Undone:        b.undone,
			DuplicateDabs: b.duplicateDabs,
		},
	}

	ids := make([]StrokeID, 0, len(b.records))
	for id := range b.records {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		payload.Records = append(payload.Records, snapshotStrokeRecord(b.records[id]))
	}
	return payload
}

func snapshotPixels(weights map[pixelKey]pixelWeights) []snapshotPixel {
	pixels := make([]snapshotPixel, 0, len(weights))
	for key, values := range weights {
		pixels = append(pixels, snapshotPixel{X: key.X, Y: key.Y, W: values})
	}
	sort.Slice(pixels, func(i, j int) bool {
		if pixels[i].X != pixels[j].X {
			return pixels[i].X < pixels[j].X
		}
		return pixels[i].Y < pixels[j].Y
	})
	return pixels
}

func snapshotStrokeRecord(rec *strokeRecord) snapshotStroke {
	result := snapshotStroke{
		ID:          rec.ID,
		Start:       rec.Start,
		Dabs:        append([]Dab(nil), rec.Dabs...),
		State:       rec.State.String(),
		Blocks:      append([]BlockCoord(nil), rec.Blocks...),
		Bytes:       rec.Bytes,
		CommitOrder: rec.CommitOrder,
	}
	result.Before = snapshotBlockImages(rec.Blocks, rec.Before)
	result.After = snapshotBlockImages(rec.Blocks, rec.After)
	return result
}

func snapshotBlockImages(blocks []BlockCoord, images map[BlockCoord][]uint32) []snapshotBlock {
	if len(blocks) == 0 {
		return nil
	}
	result := make([]snapshotBlock, 0, len(blocks))
	for _, block := range blocks {
		result = append(result, snapshotBlock{
			BX:    block.BX,
			BY:    block.BY,
			Cells: append([]uint32(nil), images[block]...),
		})
	}
	return result
}

func decodeSnapshot(data []byte) (*TerrainSplatBrush, error) {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion ||
		envelope.Checksum == "" || len(envelope.Payload) == 0 {
		return nil, ErrBadSnapshot
	}
	if envelope.Checksum != snapshotChecksum(envelope.Version, envelope.Payload) {
		return nil, ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	limits := effectiveLimits(payload.Limits)
	if payload.BlockSize != limits.BlockSize || payload.Limits != limits {
		return nil, ErrBadSnapshot
	}

	baseWeights, err := decodePixels(payload.Base)
	if err != nil {
		return nil, err
	}
	weights, err := decodePixels(payload.Weights)
	if err != nil {
		return nil, err
	}
	dirty, err := decodeBlockSet(payload.Dirty)
	if err != nil {
		return nil, err
	}
	derived, err := decodeBlockSet(payload.Derived)
	if err != nil {
		return nil, err
	}
	for block := range derived {
		if _, ok := dirty[block]; !ok {
			return nil, ErrBadSnapshot
		}
	}

	loaded := &TerrainSplatBrush{
		limits:        limits,
		baseWeights:   baseWeights,
		weights:       weights,
		dirty:         dirty,
		derived:       derived,
		records:       make(map[StrokeID]*strokeRecord),
		committed:     payload.Counters.Committed,
		cancelled:     payload.Counters.Cancelled,
		undone:        payload.Counters.Undone,
		duplicateDabs: payload.Counters.DuplicateDabs,
	}
	if loaded.committed < 0 || loaded.cancelled < 0 || loaded.undone < 0 || loaded.duplicateDabs < 0 {
		return nil, ErrBadSnapshot
	}

	var maxID StrokeID
	var maxCommit int64
	for _, record := range payload.Records {
		rec, err := decodeStrokeRecord(record, limits)
		if err != nil {
			return nil, err
		}
		if _, exists := loaded.records[rec.ID]; exists {
			return nil, ErrBadSnapshot
		}
		loaded.records[rec.ID] = rec
		if rec.ID > maxID {
			maxID = rec.ID
		}
		if rec.CommitOrder > maxCommit {
			maxCommit = rec.CommitOrder
		}
	}
	if payload.NextStroke != maxID+1 || payload.NextCommit != maxCommit+1 {
		return nil, ErrBadSnapshot
	}
	loaded.nextStroke = payload.NextStroke
	loaded.nextCommit = payload.NextCommit

	expectedCommitted := 0
	expectedCancelled := 0
	expectedUndone := 0
	for _, rec := range loaded.records {
		if rec.CommitOrder > 0 {
			expectedCommitted++
		}
		switch rec.State {
		case strokeCancelled:
			expectedCancelled++
		case strokeUndone:
			expectedUndone++
		}
	}
	if loaded.committed != expectedCommitted || loaded.cancelled != expectedCancelled || loaded.undone != expectedUndone {
		return nil, ErrBadSnapshot
	}

	replayed := cloneWeights(loaded.baseWeights)
	replayedDirty := make(map[BlockCoord]struct{})
	records := make([]*strokeRecord, 0, len(loaded.records))
	for _, rec := range loaded.records {
		if rec.State == strokeCommitted {
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CommitOrder < records[j].CommitOrder })
	for _, rec := range records {
		applyDabs(replayed, rec.Dabs)
		for _, block := range rec.Blocks {
			replayedDirty[block] = struct{}{}
		}
	}
	if !equalWeights(loaded.weights, replayed) || !equalBlockSets(loaded.dirty, replayedDirty) {
		return nil, ErrBadSnapshot
	}
	return loaded, nil
}

func decodePixels(pixels []snapshotPixel) (map[pixelKey]pixelWeights, error) {
	result := make(map[pixelKey]pixelWeights, len(pixels))
	var previous *snapshotPixel
	for i := range pixels {
		pixel := pixels[i]
		if pixel.X < 0 || pixel.Y < 0 {
			return nil, ErrBadSnapshot
		}
		if previous != nil {
			if pixel.X < previous.X || (pixel.X == previous.X && pixel.Y <= previous.Y) {
				return nil, ErrBadSnapshot
			}
		}
		var total int
		for _, value := range pixel.W {
			total += int(value)
		}
		if total != WeightScale {
			return nil, ErrBadSnapshot
		}
		result[pixelKey{X: pixel.X, Y: pixel.Y}] = pixel.W
		previous = &pixels[i]
	}
	return result, nil
}

func decodeBlockSet(blocks []BlockCoord) (map[BlockCoord]struct{}, error) {
	result := make(map[BlockCoord]struct{}, len(blocks))
	for i, block := range blocks {
		if block.BX < 0 || block.BY < 0 {
			return nil, ErrBadSnapshot
		}
		if i > 0 {
			previous := blocks[i-1]
			if block.BX < previous.BX || (block.BX == previous.BX && block.BY <= previous.BY) {
				return nil, ErrBadSnapshot
			}
		}
		result[block] = struct{}{}
	}
	return result, nil
}

func decodeStrokeRecord(record snapshotStroke, limits BrushLimits) (*strokeRecord, error) {
	if record.ID <= 0 {
		return nil, ErrBadSnapshot
	}
	if err := validStart(record.Start); err != nil {
		return nil, ErrBadSnapshot
	}
	if len(record.Dabs) > limits.MaxDabsPerStroke {
		return nil, ErrBadSnapshot
	}
	seen := make(map[int]struct{}, len(record.Dabs))
	for _, dab := range record.Dabs {
		if err := validDab(dab); err != nil {
			return nil, ErrBadSnapshot
		}
		if _, duplicate := seen[dab.Seq]; duplicate {
			return nil, ErrBadSnapshot
		}
		seen[dab.Seq] = struct{}{}
	}

	state, ok := parseStrokeState(record.State)
	if !ok {
		return nil, ErrBadSnapshot
	}
	if state == strokeActive || state == strokeFailed {
		if len(record.Blocks) != 0 || len(record.Before) != 0 || len(record.After) != 0 ||
			record.Bytes != 0 || record.CommitOrder != 0 {
			return nil, ErrBadSnapshot
		}
	}

	blocks := make([]BlockCoord, 0, len(record.Blocks))
	seenBlocks := make(map[BlockCoord]struct{}, len(record.Blocks))
	for i, block := range record.Blocks {
		if block.BX < 0 || block.BY < 0 {
			return nil, ErrBadSnapshot
		}
		if i > 0 {
			previous := record.Blocks[i-1]
			if block.BX < previous.BX || (block.BX == previous.BX && block.BY <= previous.BY) {
				return nil, ErrBadSnapshot
			}
		}
		seenBlocks[block] = struct{}{}
		blocks = append(blocks, block)
	}

	before, err := decodeBlockImages(record.Before, blocks, limits.BlockSize)
	if err != nil {
		return nil, err
	}
	after, err := decodeBlockImages(record.After, blocks, limits.BlockSize)
	if err != nil {
		return nil, err
	}

	blockBytes := int64(limits.BlockSize) * int64(limits.BlockSize) *
		int64(LayerCount) * int64(bytesPerCell) * 2
	expectedBytes := int64(len(blocks)) * blockBytes
	if record.Bytes != int(expectedBytes) {
		return nil, ErrBadSnapshot
	}
	if state == strokeCancelled && record.CommitOrder == 0 && len(blocks) != 0 {
		return nil, ErrBadSnapshot
	}
	if record.CommitOrder < 0 {
		return nil, ErrBadSnapshot
	}
	if (state == strokeCommitted || state == strokeUndone || (state == strokeCancelled && record.CommitOrder > 0)) && record.CommitOrder <= 0 {
		return nil, ErrBadSnapshot
	}
	if len(before) != len(blocks) || len(after) != len(blocks) {
		return nil, ErrBadSnapshot
	}
	for block := range seenBlocks {
		if _, ok := before[block]; !ok {
			return nil, ErrBadSnapshot
		}
		if _, ok := after[block]; !ok {
			return nil, ErrBadSnapshot
		}
	}

	return &strokeRecord{
		ID:          record.ID,
		Start:       record.Start,
		Dabs:        append([]Dab(nil), record.Dabs...),
		SeenSeqs:    seen,
		State:       state,
		Blocks:      blocks,
		Before:      before,
		After:       after,
		Bytes:       record.Bytes,
		CommitOrder: record.CommitOrder,
	}, nil
}

func decodeBlockImages(images []snapshotBlock, blocks []BlockCoord, blockSize int) (map[BlockCoord][]uint32, error) {
	if len(images) != len(blocks) {
		return nil, ErrBadSnapshot
	}
	result := make(map[BlockCoord][]uint32, len(images))
	cellsPerBlock := blockSize * blockSize * LayerCount
	for i, image := range images {
		block := BlockCoord{BX: image.BX, BY: image.BY}
		if block != blocks[i] || len(image.Cells) != cellsPerBlock {
			return nil, ErrBadSnapshot
		}
		for pixel := 0; pixel < blockSize*blockSize; pixel++ {
			var total uint32
			for layer := 0; layer < LayerCount; layer++ {
				value := image.Cells[pixel*LayerCount+layer]
				if value > WeightScale {
					return nil, ErrBadSnapshot
				}
				total += value
			}
			if total != 0 && total != WeightScale {
				return nil, ErrBadSnapshot
			}
		}
		result[block] = append([]uint32(nil), image.Cells...)
	}
	return result, nil
}

func equalWeights(a, b map[pixelKey]pixelWeights) bool {
	if len(a) != len(b) {
		return false
	}
	for key, values := range a {
		if b[key] != values {
			return false
		}
	}
	return true
}

func equalBlockSets(a, b map[BlockCoord]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for block := range a {
		if _, ok := b[block]; !ok {
			return false
		}
	}
	return true
}
