// Package task denoises a luma frame sequence in the time domain: motion
// compensated history accumulation, scene cut resets and a per frame budget.
package task

import (
	"encoding/json"
	"errors"
	"hash/crc32"
	"sync"
)

// Denoising strength window.
const (
	MinStrength = 1
	MaxStrength = 5
)

// History depth window, in frames.
const (
	MinHistoryDepth = 1
	MaxHistoryDepth = 8
)

// Supported block sizes, in pixels.
const (
	BlockSmall  = 8
	BlockMedium = 16
	BlockLarge  = 32
)

// MaxPixels caps one frame at 4096 by 2160 luma samples.
const (
	MaxPixels = 4096 * 2160
)

// MaxMotion caps the absolute motion vector component, in pixels.
const (
	MaxMotion = 64
)

// Errors returned by the denoiser. A rejected call leaves the history and the
// counters untouched.
var (
	ErrStrengthRange  = errors.New("denoise strength out of range")
	ErrBlockSize      = errors.New("block size is not supported")
	ErrHistoryDepth   = errors.New("history depth out of range")
	ErrBadFrame       = errors.New("frame dimensions or sample count are invalid")
	ErrFrameTooLarge  = errors.New("frame exceeds the pixel budget")
	ErrBadMotionField = errors.New("motion field does not match the block grid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

const (
	// maxBlockBudget limits the number of block records visited for one frame.
	// Frames above this budget are processed with coarser blocks, never dropped.
	maxBlockBudget  = 65536
	snapshotVersion = 1
)

// Frame is one luma plane.
type Frame struct {
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Y      []float32 `json:"y"`
}

// MotionField is the per block motion grid from the current frame into the
// previous one.
type MotionField struct {
	BlockSize int   `json:"block_size"`
	Cols      int   `json:"cols"`
	Rows      int   `json:"rows"`
	VX        []int `json:"vx"`
	VY        []int `json:"vy"`
}

// DenoiseStats is the observable state of the denoiser.
type DenoiseStats struct {
	Strength     int `json:"strength"`
	BlockSize    int `json:"block_size"`
	HistoryDepth int `json:"history_depth"`
	Frames       int `json:"frames"`
	SceneCuts    int `json:"scene_cuts"`
	Blocks       int `json:"blocks"`
	Resets       int `json:"resets"`
}

// VideoTemporalDenoiser owns the accumulated history frames and the motion
// field. The mutex protects both the configuration and the frame history so a
// reader can only observe a complete history update.
type VideoTemporalDenoiser struct {
	mu sync.RWMutex

	strength     int
	blockSize    int
	historyDepth int

	history   []Frame
	motion    MotionField
	hasMotion bool

	frames    int
	sceneCuts int
	blocks    int
	resets    int
}

// NewVideoTemporalDenoiser builds a denoiser with strength three, sixteen
// pixel blocks and a history depth of four frames.
func NewVideoTemporalDenoiser() *VideoTemporalDenoiser {
	return &VideoTemporalDenoiser{
		strength:     3,
		blockSize:    BlockMedium,
		historyDepth: 4,
	}
}

// SetStrength selects the history weight: level one keeps a fifth of the
// history, level five keeps four fifths.
func (d *VideoTemporalDenoiser) SetStrength(level int) error {
	if level < MinStrength || level > MaxStrength {
		return ErrStrengthRange
	}
	d.mu.Lock()
	d.strength = level
	d.mu.Unlock()
	return nil
}

// SetMotionVectors installs the motion grid used by the next frames.
func (d *VideoTemporalDenoiser) SetMotionVectors(mv MotionField) error {
	mv, err := normalizeMotionField(mv)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.motion = mv
	d.hasMotion = true
	d.mu.Unlock()
	return nil
}

// Denoise blends one frame with the motion compensated history and returns a
// new frame.
func (d *VideoTemporalDenoiser) Denoise(frame Frame) (Frame, error) {
	sampleCount, err := validateFrame(frame)
	if err != nil {
		return Frame{}, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	cols, rows := gridDimensions(frame.Width, frame.Height, d.blockSize)
	if d.hasMotion {
		if d.motion.BlockSize != d.blockSize || d.motion.Cols != cols || d.motion.Rows != rows {
			return Frame{}, ErrBadMotionField
		}
	}

	if len(d.history) > 0 && !historyMatchesFrame(d.history[0], frame) {
		d.history = nil
	}

	processingBlock := processingBlockSize(frame.Width, frame.Height, d.blockSize)
	blockCount := blockGridCount(frame.Width, frame.Height, processingBlock)
	out := Frame{
		Width:  frame.Width,
		Height: frame.Height,
		Y:      make([]float32, sampleCount),
	}

	if len(d.history) == 0 {
		copy(out.Y, frame.Y)
	} else {
		d.blendWithHistory(out.Y, frame, processingBlock)
	}

	d.appendHistoryLocked(frame)
	d.frames++
	d.blocks = blockCount
	return out, nil
}

// NotifySceneCut drops the accumulated history so the next frame starts a new
// shot.
func (d *VideoTemporalDenoiser) NotifySceneCut() error {
	d.mu.Lock()
	d.history = nil
	d.sceneCuts++
	d.mu.Unlock()
	return nil
}

// SetHistoryDepth changes how many past frames feed the running history mean.
func (d *VideoTemporalDenoiser) SetHistoryDepth(n int) error {
	if n < MinHistoryDepth || n > MaxHistoryDepth {
		return ErrHistoryDepth
	}
	d.mu.Lock()
	d.historyDepth = n
	d.trimHistoryLocked()
	d.mu.Unlock()
	return nil
}

// Reset clears the history and the counters.
func (d *VideoTemporalDenoiser) Reset() error {
	d.mu.Lock()
	d.history = nil
	d.frames = 0
	d.sceneCuts = 0
	d.blocks = 0
	d.resets++
	d.mu.Unlock()
	return nil
}

// SetBlockSize changes the block grid used for the motion field.
func (d *VideoTemporalDenoiser) SetBlockSize(px int) error {
	if !supportedBlockSize(px) {
		return ErrBlockSize
	}
	d.mu.Lock()
	d.blockSize = px
	d.mu.Unlock()
	return nil
}

// Stats reports the configuration and the processed frame counters.
func (d *VideoTemporalDenoiser) Stats() DenoiseStats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return DenoiseStats{
		Strength:     d.strength,
		BlockSize:    d.blockSize,
		HistoryDepth: d.historyDepth,
		Frames:       d.frames,
		SceneCuts:    d.sceneCuts,
		Blocks:       d.blocks,
		Resets:       d.resets,
	}
}

// Snapshot serialises the configuration, the history frame and the counters.
func (d *VideoTemporalDenoiser) Snapshot() ([]byte, error) {
	d.mu.RLock()
	state := snapshotState{
		Strength:     d.strength,
		BlockSize:    d.blockSize,
		HistoryDepth: d.historyDepth,
		History:      cloneFrames(d.history),
		Frames:       d.frames,
		SceneCuts:    d.sceneCuts,
		Blocks:       d.blocks,
		Resets:       d.resets,
	}
	if d.hasMotion {
		motion := cloneMotionField(d.motion)
		state.Motion = &motion
	}
	d.mu.RUnlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: crc32.ChecksumIEEE(payload),
		State:    json.RawMessage(payload),
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (d *VideoTemporalDenoiser) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || len(envelope.State) == 0 ||
		crc32.ChecksumIEEE(envelope.State) != envelope.Checksum {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.State, &state); err != nil {
		return ErrBadSnapshot
	}
	if !validSnapshotState(state) {
		return ErrBadSnapshot
	}

	var motion MotionField
	hasMotion := state.Motion != nil
	if hasMotion {
		motion = cloneMotionField(*state.Motion)
	}

	d.mu.Lock()
	d.strength = state.Strength
	d.blockSize = state.BlockSize
	d.historyDepth = state.HistoryDepth
	d.history = state.History
	d.motion = motion
	d.hasMotion = hasMotion
	d.frames = state.Frames
	d.sceneCuts = state.SceneCuts
	d.blocks = state.Blocks
	d.resets = state.Resets
	d.mu.Unlock()
	return nil
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum uint32          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

type snapshotState struct {
	Strength     int          `json:"strength"`
	BlockSize    int          `json:"block_size"`
	HistoryDepth int          `json:"history_depth"`
	History      []Frame      `json:"history"`
	Motion       *MotionField `json:"motion,omitempty"`
	Frames       int          `json:"frames"`
	SceneCuts    int          `json:"scene_cuts"`
	Blocks       int          `json:"blocks"`
	Resets       int          `json:"resets"`
}

func validateFrame(frame Frame) (int, error) {
	if frame.Width <= 0 || frame.Height <= 0 {
		return 0, ErrBadFrame
	}
	if frame.Width > MaxPixels/frame.Height {
		return 0, ErrFrameTooLarge
	}
	sampleCount := frame.Width * frame.Height
	if len(frame.Y) != sampleCount {
		return 0, ErrBadFrame
	}
	return sampleCount, nil
}

func supportedBlockSize(px int) bool {
	return px == BlockSmall || px == BlockMedium || px == BlockLarge
}

func normalizeMotionField(mv MotionField) (MotionField, error) {
	if !supportedBlockSize(mv.BlockSize) || mv.Cols <= 0 || mv.Rows <= 0 {
		return MotionField{}, ErrBadMotionField
	}
	maxInt := int(^uint(0) >> 1)
	if mv.Cols > maxInt/mv.Rows {
		return MotionField{}, ErrBadMotionField
	}
	count := mv.Cols * mv.Rows
	if len(mv.VX) != count || len(mv.VY) != count {
		return MotionField{}, ErrBadMotionField
	}
	for i := 0; i < count; i++ {
		if mv.VX[i] < -MaxMotion || mv.VX[i] > MaxMotion ||
			mv.VY[i] < -MaxMotion || mv.VY[i] > MaxMotion {
			return MotionField{}, ErrBadMotionField
		}
	}
	return cloneMotionField(mv), nil
}

func cloneMotionField(mv MotionField) MotionField {
	cloned := mv
	cloned.VX = append([]int(nil), mv.VX...)
	cloned.VY = append([]int(nil), mv.VY...)
	return cloned
}

func cloneFrame(frame Frame) Frame {
	cloned := frame
	cloned.Y = append([]float32(nil), frame.Y...)
	return cloned
}

func cloneFrames(frames []Frame) []Frame {
	if len(frames) == 0 {
		return nil
	}
	cloned := make([]Frame, len(frames))
	for i := range frames {
		cloned[i] = cloneFrame(frames[i])
	}
	return cloned
}

func gridDimensions(width, height, blockSize int) (int, int) {
	return (width + blockSize - 1) / blockSize, (height + blockSize - 1) / blockSize
}

func blockGridCount(width, height, blockSize int) int {
	cols, rows := gridDimensions(width, height, blockSize)
	return cols * rows
}

func processingBlockSize(width, height, configured int) int {
	blockSize := configured
	for blockGridCount(width, height, blockSize) > maxBlockBudget {
		blockSize *= 2
	}
	return blockSize
}

func historyMatchesFrame(history Frame, frame Frame) bool {
	return history.Width == frame.Width && history.Height == frame.Height &&
		len(history.Y) == len(frame.Y)
}

func historyWeight(level int) float32 {
	switch level {
	case 1:
		return 0.2
	case 2:
		return 0.35
	case 3:
		return 0.5
	case 4:
		return 0.65
	default:
		return 0.8
	}
}

func (d *VideoTemporalDenoiser) blendWithHistory(dst []float32, frame Frame, processingBlock int) {
	width, height := frame.Width, frame.Height
	weight := historyWeight(d.strength)
	currentWeight := 1 - weight
	historyCount := float32(len(d.history))
	baseBlock := d.blockSize

	for y0 := 0; y0 < height; y0 += processingBlock {
		y1 := min(y0+processingBlock, height)
		for x0 := 0; x0 < width; x0 += processingBlock {
			x1 := min(x0+processingBlock, width)
			vx, vy := 0, 0
			if d.hasMotion {
				gridX := min(x0/baseBlock, d.motion.Cols-1)
				gridY := min(y0/baseBlock, d.motion.Rows-1)
				index := gridY*d.motion.Cols + gridX
				vx = d.motion.VX[index]
				vy = d.motion.VY[index]
			}

			if !targetBlockInBounds(x0, y0, x1, y1, vx, vy, width, height) {
				spatialDenoiseRect(dst, frame.Y, width, height, x0, y0, x1, y1)
				continue
			}

			for y := y0; y < y1; y++ {
				sourceRow := (y + vy) * width
				targetRow := y * width
				for x := x0; x < x1; x++ {
					var historySum float32
					for _, historical := range d.history {
						historySum += historical.Y[sourceRow+x+vx]
					}
					index := targetRow + x
					dst[index] = frame.Y[index]*currentWeight + (historySum/historyCount)*weight
				}
			}
		}
	}
}

func targetBlockInBounds(x0, y0, x1, y1, vx, vy, width, height int) bool {
	return x0+vx >= 0 && x1-1+vx < width && y0+vy >= 0 && y1-1+vy < height
}

func spatialDenoiseRect(dst, src []float32, width, height, x0, y0, x1, y1 int) {
	for y := y0; y < y1; y++ {
		top := max(0, y-1)
		bottom := min(height-1, y+1)
		for x := x0; x < x1; x++ {
			left := max(0, x-1)
			right := min(width-1, x+1)
			var sum float32
			var count int
			for yy := top; yy <= bottom; yy++ {
				row := yy * width
				for xx := left; xx <= right; xx++ {
					sum += src[row+xx]
					count++
				}
			}
			dst[y*width+x] = sum / float32(count)
		}
	}
}

func (d *VideoTemporalDenoiser) appendHistoryLocked(frame Frame) {
	d.history = append(d.history, cloneFrame(frame))
	d.trimHistoryLocked()
}

func (d *VideoTemporalDenoiser) trimHistoryLocked() {
	if len(d.history) <= d.historyDepth {
		return
	}
	start := len(d.history) - d.historyDepth
	trimmed := make([]Frame, d.historyDepth)
	copy(trimmed, d.history[start:])
	d.history = trimmed
}

func validSnapshotState(state snapshotState) bool {
	if state.Strength < MinStrength || state.Strength > MaxStrength ||
		!supportedBlockSize(state.BlockSize) ||
		state.HistoryDepth < MinHistoryDepth || state.HistoryDepth > MaxHistoryDepth ||
		state.Frames < 0 || state.SceneCuts < 0 || state.Blocks < 0 || state.Resets < 0 ||
		len(state.History) > state.HistoryDepth {
		return false
	}
	if len(state.History) > 0 && state.Frames == 0 {
		return false
	}

	var width, height int
	for i := range state.History {
		if _, err := validateFrame(state.History[i]); err != nil {
			return false
		}
		if i == 0 {
			width, height = state.History[i].Width, state.History[i].Height
		} else if state.History[i].Width != width || state.History[i].Height != height {
			return false
		}
	}
	if state.Motion != nil {
		if _, err := normalizeMotionField(*state.Motion); err != nil {
			return false
		}
	}
	return true
}
