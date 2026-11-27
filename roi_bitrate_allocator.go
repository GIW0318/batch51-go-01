// Package task allocates per block QP offsets for region of interest encoding
// and keeps the total bitrate inside the configured target.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"sort"
	"sync"
)

// Contract defaults applied when Limits or the configuration leaves a field
// unset.
const (
	DefaultBlockSize  = 16
	DefaultQPMin      = 10
	DefaultQPMax      = 45
	DefaultSmooth     = 4
	DefaultMaxBlocks  = 4096
	DefaultMaxSamples = 65536
	MaxRoiWeight      = 4.0
)

const snapshotVersion = 1

var snapshotMagic = [4]byte{'R', 'O', 'I', '1'}

// Errors reported by the allocator.
var (
	ErrBadRect       = errors.New("roi rectangle is empty or has a negative origin")
	ErrBadWeight     = errors.New("roi weight is outside the accepted range")
	ErrBadFrame      = errors.New("frame dimensions are invalid")
	ErrBlockBudget   = errors.New("frame block count exceeds the budget")
	ErrBadSaliency   = errors.New("saliency map dimensions do not match its samples")
	ErrSampleBudget  = errors.New("saliency sample count exceeds the budget")
	ErrBadQPRange    = errors.New("qp range is out of bounds or inverted")
	ErrBadWindow     = errors.New("smoothing window is outside the accepted range")
	ErrBadBits       = errors.New("target bits must be positive")
	ErrRoiOutOfFrame = errors.New("roi rectangle is not fully inside the frame")
	ErrUnknownPlan   = errors.New("no pending plan for this frame")
	ErrStaleFrame    = errors.New("frame was already committed")
	ErrBadTable      = errors.New("qp table does not match the planned grid")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Rect is a pixel aligned region of interest.
type Rect struct {
	X int
	Y int
	W int
	H int
}

// Frame describes the picture the QP table is planned for.
type Frame struct {
	ID        int
	Width     int
	Height    int
	BlockSize int
}

// SaliencyMap is a coarse per sample importance map in the 0..255 range.
type SaliencyMap struct {
	Width  int
	Height int
	Values []uint8
}

// QPTable is the per block quantiser decision for one frame.
type QPTable struct {
	FrameID   int
	BlockCols int
	BlockRows int
	Base      int
	QP        []int
	ROIBlocks int
}

// RoiStats is the running counter set of one allocator.
type RoiStats struct {
	Frames      int
	Committed   int
	Clamped     int
	ROIFrames   int
	Rejected    int
	LastFrameID int
	LastBase    int
}

// Limits bounds the per frame work of the allocator.
type Limits struct {
	MaxBlocks  int
	MaxSamples int
}

type blockKey struct {
	x    int
	y    int
	size int
}

type pendingPlan struct {
	cols      int
	rows      int
	blockSize int
	weights   []float32
}

type historyEntry struct {
	qp     int
	weight float32
}

type snapshotHistory struct {
	X         int
	Y         int
	BlockSize int
	QP        int
	Weight    float32
}

type snapshotData struct {
	Version        int
	HasROI         bool
	ROI            Rect
	ROIWeight      float32
	HasSaliency    bool
	SaliencyWidth  int
	SaliencyHeight int
	SaliencyValues []uint8
	QPMin          int
	QPMax          int
	SmoothWindow   int
	History        []snapshotHistory
	Stats          RoiStats
}

// RoiBitrateAllocator owns the region of interest, the saliency map, the QP
// bounds and the per block smoothing history.
type RoiBitrateAllocator struct {
	mu sync.Mutex

	limits       Limits
	roi          Rect
	roiWeight    float32
	hasROI       bool
	saliency     *SaliencyMap
	qpMin        int
	qpMax        int
	smoothWindow int
	history      map[blockKey]historyEntry
	stats        RoiStats
	pending      map[int]pendingPlan
}

// NewRoiBitrateAllocator builds an allocator for the given caps.
func NewRoiBitrateAllocator(limits Limits) *RoiBitrateAllocator {
	if limits.MaxBlocks <= 0 {
		limits.MaxBlocks = DefaultMaxBlocks
	}
	if limits.MaxSamples <= 0 {
		limits.MaxSamples = DefaultMaxSamples
	}
	return &RoiBitrateAllocator{
		limits:       limits,
		qpMin:        DefaultQPMin,
		qpMax:        DefaultQPMax,
		smoothWindow: DefaultSmooth,
		history:      make(map[blockKey]historyEntry),
		pending:      make(map[int]pendingPlan),
	}
}

// SetRoi installs one region of interest with its weight.
func (a *RoiBitrateAllocator) SetRoi(rect Rect, weight float32) error {
	if !validRect(rect) {
		return ErrBadRect
	}
	if !validWeight(weight) {
		return ErrBadWeight
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.roi = rect
	a.roiWeight = weight
	a.hasROI = true
	return nil
}

// ClearRoi removes the region of interest.
func (a *RoiBitrateAllocator) ClearRoi() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roi = Rect{}
	a.roiWeight = 0
	a.hasROI = false
	return nil
}

// SetSaliencyMap installs the saliency map used to weight blocks.
func (a *RoiBitrateAllocator) SetSaliencyMap(m SaliencyMap) error {
	count, ok := saliencySampleCount(m.Width, m.Height)
	if !ok || count != len(m.Values) {
		return ErrBadSaliency
	}
	if count > a.limits.MaxSamples {
		return ErrSampleBudget
	}

	values := append([]uint8(nil), m.Values...)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.saliency = &SaliencyMap{Width: m.Width, Height: m.Height, Values: values}
	return nil
}

// PlanQP plans the per block QP table for one frame.
func (a *RoiBitrateAllocator) PlanQP(frame Frame, targetBits int) (QPTable, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if frame.Width <= 0 || frame.Height <= 0 || frame.BlockSize < 0 {
		a.stats.Rejected++
		return QPTable{}, ErrBadFrame
	}
	if targetBits <= 0 {
		a.stats.Rejected++
		return QPTable{}, ErrBadBits
	}

	blockSize := frame.BlockSize
	if blockSize == 0 {
		blockSize = DefaultBlockSize
	}
	cols, rows := blockGrid(frame.Width, frame.Height, blockSize)
	if cols > a.limits.MaxBlocks/rows {
		a.stats.Rejected++
		return QPTable{}, ErrBlockBudget
	}
	blockCount := cols * rows

	if a.hasROI && !roiInsideFrame(a.roi, frame.Width, frame.Height) {
		a.stats.Rejected++
		return QPTable{}, ErrRoiOutOfFrame
	}

	base := baseQP(targetBits / blockCount)
	base = clamp(base, a.qpMin, a.qpMax)

	table := QPTable{
		FrameID:   frame.ID,
		BlockCols: cols,
		BlockRows: rows,
		Base:      base,
		QP:        make([]int, blockCount),
	}
	weights := make([]float32, blockCount)

	stepLimit := 0
	if a.smoothWindow > 0 {
		stepLimit = (8 + a.smoothWindow - 1) / a.smoothWindow
	}
	clamped := 0

	roiRight, roiBottom := 0, 0
	if a.hasROI {
		roiRight = a.roi.X + a.roi.W
		roiBottom = a.roi.Y + a.roi.H
	}

	for y := 0; y < rows; y++ {
		top := y * blockSize
		bottom := blockEnd(top, blockSize, frame.Height)
		for x := 0; x < cols; x++ {
			left := x * blockSize
			right := blockEnd(left, blockSize, frame.Width)

			var blockWeight float32
			if a.hasROI &&
				left < roiRight && a.roi.X < right &&
				top < roiBottom && a.roi.Y < bottom {
				table.ROIBlocks++
				blockWeight = a.roiWeight
			}
			if a.saliency != nil {
				sample := saliencySample(a.saliency, x, y, cols, rows)
				saliencyWeight := float32(sample) / 64
				if saliencyWeight > blockWeight {
					blockWeight = saliencyWeight
				}
			}
			weights[y*cols+x] = blockWeight

			qp := base + 2
			if blockWeight > 0 {
				qp = base - int(float64(blockWeight)*4)
			}
			qp = clamp(qp, a.qpMin, a.qpMax)

			if stepLimit > 0 {
				key := blockKey{x: x, y: y, size: blockSize}
				if previous, ok := a.history[key]; ok && previous.weight == blockWeight {
					if qp > previous.qp+stepLimit {
						qp = previous.qp + stepLimit
						clamped++
					} else if qp < previous.qp-stepLimit {
						qp = previous.qp - stepLimit
						clamped++
					}
					qp = clamp(qp, a.qpMin, a.qpMax)
				}
			}

			table.QP[y*cols+x] = qp
		}
	}

	a.stats.Frames++
	if a.hasROI {
		a.stats.ROIFrames++
	}
	a.stats.Clamped += clamped
	a.stats.LastBase = base
	a.pending[frame.ID] = pendingPlan{
		cols:      cols,
		rows:      rows,
		blockSize: blockSize,
		weights:   weights,
	}
	return table, nil
}

// SetQPRange replaces the inclusive QP bounds.
func (a *RoiBitrateAllocator) SetQPRange(min, max int) error {
	if min < 0 || max > 51 || min >= max {
		return ErrBadQPRange
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.qpMin = min
	a.qpMax = max
	return nil
}

// SetSmoothWindow replaces the number of frames the per block step limit is
// derived from.
func (a *RoiBitrateAllocator) SetSmoothWindow(frames int) error {
	if frames < 0 || frames > 64 {
		return ErrBadWindow
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.smoothWindow = frames
	return nil
}

// MarkSceneCut drops the smoothing history at a scene boundary.
func (a *RoiBitrateAllocator) MarkSceneCut() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = make(map[blockKey]historyEntry)
	return nil
}

// Commit records one planned table as the new smoothing history.
func (a *RoiBitrateAllocator) Commit(qp QPTable) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stats.Committed > 0 && qp.FrameID <= a.stats.LastFrameID {
		delete(a.pending, qp.FrameID)
		return ErrStaleFrame
	}

	plan, ok := a.pending[qp.FrameID]
	if !ok {
		return ErrUnknownPlan
	}
	if qp.BlockCols != plan.cols || qp.BlockRows != plan.rows ||
		len(qp.QP) != plan.cols*plan.rows {
		return ErrBadTable
	}

	history := make(map[blockKey]historyEntry, len(qp.QP))
	for i, value := range qp.QP {
		history[blockKey{
			x:    i % plan.cols,
			y:    i / plan.cols,
			size: plan.blockSize,
		}] = historyEntry{qp: value, weight: plan.weights[i]}
	}
	a.history = history
	delete(a.pending, qp.FrameID)
	a.stats.Committed++
	a.stats.LastFrameID = qp.FrameID
	return nil
}

// Stats returns a copy of the running counters.
func (a *RoiBitrateAllocator) Stats() RoiStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

// Roi reports the installed region of interest.
func (a *RoiBitrateAllocator) Roi() (Rect, float32, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.roi, a.roiWeight, a.hasROI
}

// QPRange reports the inclusive QP bounds in force.
func (a *RoiBitrateAllocator) QPRange() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.qpMin, a.qpMax
}

// SmoothWindow reports the smoothing window in force.
func (a *RoiBitrateAllocator) SmoothWindow() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.smoothWindow
}

// Snapshot serialises the roi, the saliency map, the bounds, the smoothing
// history and the counters.
func (a *RoiBitrateAllocator) Snapshot() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	history := make([]snapshotHistory, 0, len(a.history))
	for key, value := range a.history {
		history = append(history, snapshotHistory{
			X:         key.x,
			Y:         key.y,
			BlockSize: key.size,
			QP:        value.qp,
			Weight:    value.weight,
		})
	}
	sort.Slice(history, func(i, j int) bool {
		if history[i].Y != history[j].Y {
			return history[i].Y < history[j].Y
		}
		if history[i].X != history[j].X {
			return history[i].X < history[j].X
		}
		return history[i].BlockSize < history[j].BlockSize
	})

	data := snapshotData{
		Version:      snapshotVersion,
		HasROI:       a.hasROI,
		ROI:          a.roi,
		ROIWeight:    a.roiWeight,
		QPMin:        a.qpMin,
		QPMax:        a.qpMax,
		SmoothWindow: a.smoothWindow,
		History:      history,
		Stats:        a.stats,
	}
	if a.saliency != nil {
		data.HasSaliency = true
		data.SaliencyWidth = a.saliency.Width
		data.SaliencyHeight = a.saliency.Height
		data.SaliencyValues = append([]uint8(nil), a.saliency.Values...)
	}

	var payload bytes.Buffer
	if err := gob.NewEncoder(&payload).Encode(&data); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(payload.Bytes())
	out := make([]byte, 0, len(snapshotMagic)+len(sum)+payload.Len())
	out = append(out, snapshotMagic[:]...)
	out = append(out, sum[:]...)
	out = append(out, payload.Bytes()...)
	return out, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *RoiBitrateAllocator) Restore(data []byte) error {
	headerLen := len(snapshotMagic) + sha256.Size
	if len(data) < headerLen || !bytes.Equal(data[:len(snapshotMagic)], snapshotMagic[:]) {
		return ErrBadSnapshot
	}

	sum := data[len(snapshotMagic):headerLen]
	payload := data[headerLen:]
	want := sha256.Sum256(payload)
	if !bytes.Equal(sum, want[:]) {
		return ErrBadSnapshot
	}

	var snapshot snapshotData
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&snapshot); err != nil {
		return ErrBadSnapshot
	}
	if !validSnapshot(snapshot, a.limits) {
		return ErrBadSnapshot
	}

	history := make(map[blockKey]historyEntry, len(snapshot.History))
	for _, entry := range snapshot.History {
		history[blockKey{x: entry.X, y: entry.Y, size: entry.BlockSize}] = historyEntry{
			qp:     entry.QP,
			weight: entry.Weight,
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.hasROI = snapshot.HasROI
	if snapshot.HasROI {
		a.roi = snapshot.ROI
		a.roiWeight = snapshot.ROIWeight
	} else {
		a.roi = Rect{}
		a.roiWeight = 0
	}

	if snapshot.HasSaliency {
		a.saliency = &SaliencyMap{
			Width:  snapshot.SaliencyWidth,
			Height: snapshot.SaliencyHeight,
			Values: append([]uint8(nil), snapshot.SaliencyValues...),
		}
	} else {
		a.saliency = nil
	}

	a.qpMin = snapshot.QPMin
	a.qpMax = snapshot.QPMax
	a.smoothWindow = snapshot.SmoothWindow
	a.history = history
	a.stats = snapshot.Stats
	a.pending = make(map[int]pendingPlan)
	return nil
}

func validRect(rect Rect) bool {
	return rect.X >= 0 && rect.Y >= 0 && rect.W > 0 && rect.H > 0
}

func validWeight(weight float32) bool {
	return weight > 0 && weight <= MaxRoiWeight
}

func roiInsideFrame(rect Rect, width, height int) bool {
	if !validRect(rect) || rect.X > width || rect.Y > height {
		return false
	}
	return rect.W <= width-rect.X && rect.H <= height-rect.Y
}

func saliencySampleCount(width, height int) (int, bool) {
	if width <= 0 || height <= 0 {
		return 0, false
	}
	maxInt := int(^uint(0) >> 1)
	if width > maxInt/height {
		return 0, false
	}
	return width * height, true
}

func blockGrid(width, height, blockSize int) (int, int) {
	cols := width / blockSize
	if width%blockSize != 0 {
		cols++
	}
	rows := height / blockSize
	if height%blockSize != 0 {
		rows++
	}
	return cols, rows
}

func blockEnd(start, size, limit int) int {
	end := start + size
	if end < start || end > limit {
		return limit
	}
	return end
}

func baseQP(bitsPerBlock int) int {
	switch {
	case bitsPerBlock >= 1024:
		return 20
	case bitsPerBlock >= 512:
		return 26
	case bitsPerBlock >= 256:
		return 32
	case bitsPerBlock >= 128:
		return 38
	default:
		return 45
	}
}

func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func saliencySample(m *SaliencyMap, x, y, cols, rows int) uint8 {
	sx := int((int64(2*x) + 1) * int64(m.Width) / (2 * int64(cols)))
	sy := int((int64(2*y) + 1) * int64(m.Height) / (2 * int64(rows)))
	if sx >= m.Width {
		sx = m.Width - 1
	}
	if sy >= m.Height {
		sy = m.Height - 1
	}
	return m.Values[sy*m.Width+sx]
}

func validSnapshot(snapshot snapshotData, limits Limits) bool {
	if snapshot.Version != snapshotVersion {
		return false
	}
	if snapshot.QPMin < 0 || snapshot.QPMax > 51 || snapshot.QPMin >= snapshot.QPMax {
		return false
	}
	if snapshot.SmoothWindow < 0 || snapshot.SmoothWindow > 64 {
		return false
	}

	if snapshot.HasROI {
		if !validRect(snapshot.ROI) || !validWeight(snapshot.ROIWeight) {
			return false
		}
	}

	if snapshot.HasSaliency {
		count, ok := saliencySampleCount(snapshot.SaliencyWidth, snapshot.SaliencyHeight)
		if !ok || count != len(snapshot.SaliencyValues) || count > limits.MaxSamples {
			return false
		}
	} else if len(snapshot.SaliencyValues) != 0 {
		return false
	}

	stats := snapshot.Stats
	if stats.Frames < 0 || stats.Committed < 0 || stats.Clamped < 0 ||
		stats.ROIFrames < 0 || stats.Rejected < 0 {
		return false
	}
	if stats.Committed > stats.Frames || stats.ROIFrames > stats.Frames {
		return false
	}
	if stats.Committed == 0 && stats.LastFrameID != 0 {
		return false
	}
	if stats.LastBase < 0 || stats.LastBase > 51 {
		return false
	}

	for _, entry := range snapshot.History {
		if entry.X < 0 || entry.Y < 0 || entry.BlockSize <= 0 ||
			!(entry.Weight >= 0 && entry.Weight <= MaxRoiWeight) {
			return false
		}
	}
	return true
}
