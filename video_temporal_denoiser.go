// Package task denoises a luma frame sequence in the time domain: motion
// compensated history accumulation, scene cut resets and a per frame budget.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

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
	ErrStrengthRange = errors.New("denoise strength out of range")
	ErrBlockSize     = errors.New("block size is not supported")
	ErrHistoryDepth  = errors.New("history depth out of range")
	ErrBadFrame      = errors.New("frame dimensions or sample count are invalid")
	ErrFrameTooLarge = errors.New("frame exceeds the pixel budget")
	ErrBadMotionField = errors.New("motion field does not match the block grid")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
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

// VideoTemporalDenoiser owns the accumulated history frame and the motion
// field.
type VideoTemporalDenoiser struct{}

// NewVideoTemporalDenoiser builds a denoiser with strength three, sixteen
// pixel blocks and a history depth of four frames.
func NewVideoTemporalDenoiser() *VideoTemporalDenoiser {
	return &VideoTemporalDenoiser{}
}

// SetStrength selects the history weight: level one keeps a fifth of the
// history, level five keeps four fifths.
func (d *VideoTemporalDenoiser) SetStrength(level int) error {
	return errNotImplemented
}

// SetMotionVectors installs the motion grid used by the next frames.
func (d *VideoTemporalDenoiser) SetMotionVectors(mv MotionField) error {
	return errNotImplemented
}

// Denoise blends one frame with the motion compensated history and returns a
// new frame.
func (d *VideoTemporalDenoiser) Denoise(frame Frame) (Frame, error) {
	return Frame{}, errNotImplemented
}

// NotifySceneCut drops the accumulated history so the next frame starts a new
// shot.
func (d *VideoTemporalDenoiser) NotifySceneCut() error {
	return errNotImplemented
}

// SetHistoryDepth changes how many past frames feed the running history mean.
func (d *VideoTemporalDenoiser) SetHistoryDepth(n int) error {
	return errNotImplemented
}

// Reset clears the history and the counters.
func (d *VideoTemporalDenoiser) Reset() error {
	return errNotImplemented
}

// SetBlockSize changes the block grid used for the motion field.
func (d *VideoTemporalDenoiser) SetBlockSize(px int) error {
	return errNotImplemented
}

// Stats reports the configuration and the processed frame counters.
func (d *VideoTemporalDenoiser) Stats() DenoiseStats {
	return DenoiseStats{}
}

// Snapshot serialises the configuration, the history frame and the counters.
func (d *VideoTemporalDenoiser) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (d *VideoTemporalDenoiser) Restore(data []byte) error {
	return errNotImplemented
}
