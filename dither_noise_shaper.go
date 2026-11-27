// Package task quantises high resolution audio down to a target bit depth with
// triangular dither, error feedback noise shaping and per channel
// decorrelation.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Bit depth window of the quantiser.
const (
	MinBits = 4
	MaxBits = 24
)

// Channel and block windows.
const (
	MaxChannels    = 8
	MaxBlockFrames = 4096
)

// Shaping curve window: coefficient count and absolute coefficient limit.
const (
	MaxCurveLen  = 32
	MaxCurveCoef = 1.0
)

// Errors returned by the shaper. A rejected call leaves the quantiser state
// untouched.
var (
	ErrBitsRange     = errors.New("target bit depth out of range")
	ErrCurveLength   = errors.New("shaping curve too long")
	ErrCurveCoeff    = errors.New("shaping coefficient out of range")
	ErrChannelCount  = errors.New("channel count out of range")
	ErrRaggedInput   = errors.New("sample count is not a multiple of the channel count")
	ErrFramesRange   = errors.New("frame count out of range")
	ErrUnknownChannel = errors.New("channel index out of range")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// DitherStats is the observable state of the shaper.
type DitherStats struct {
	Bits            int    `json:"bits"`
	Channels        int    `json:"channels"`
	CurveLen        int    `json:"curve_len"`
	ClipGuard       bool   `json:"clip_guard"`
	ProcessedFrames int    `json:"processed_frames"`
	Clipped         int    `json:"clipped"`
	Seed            uint64 `json:"seed"`
}

// DitherNoiseShaper owns the per channel error history and random substreams.
type DitherNoiseShaper struct {
	bits int
}

// NewDitherNoiseShaper builds a shaper for the given bit depth; a depth
// outside the window falls back to sixteen bits. The clip guard starts
// enabled and the seed starts at zero.
func NewDitherNoiseShaper(bits int) *DitherNoiseShaper {
	return &DitherNoiseShaper{bits: bits}
}

// SetTargetBits changes the target bit depth.
func (s *DitherNoiseShaper) SetTargetBits(bits int) error {
	return errNotImplemented
}

// SetShapeCurve installs the error feedback coefficients, most recent error
// first. An empty curve disables shaping.
func (s *DitherNoiseShaper) SetShapeCurve(curve []float64) error {
	return errNotImplemented
}

// SetSeed sets the master seed the per channel dither substreams derive from.
func (s *DitherNoiseShaper) SetSeed(seed uint64) error {
	return errNotImplemented
}

// Process quantises one interleaved block and returns the same number of
// samples.
func (s *DitherNoiseShaper) Process(in []float32, channels int) ([]int16, error) {
	return nil, errNotImplemented
}

// ResetChannel clears the error history of one channel and restarts its dither
// substream from the initial state.
func (s *DitherNoiseShaper) ResetChannel(ch int) error {
	return errNotImplemented
}

// ErrorSpectrum returns the retained error feedback values of one channel,
// oldest first.
func (s *DitherNoiseShaper) ErrorSpectrum(ch int) ([]float64, error) {
	return nil, errNotImplemented
}

// SetClipGuard toggles the pre quantisation limiter and its counter.
func (s *DitherNoiseShaper) SetClipGuard(on bool) error {
	return errNotImplemented
}

// Stats reports the configured depth, the curve length and the counters.
func (s *DitherNoiseShaper) Stats() DitherStats {
	return DitherStats{}
}

// Snapshot serialises the configuration, the per channel error history, the
// random substream positions and the counters.
func (s *DitherNoiseShaper) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *DitherNoiseShaper) Restore(data []byte) error {
	return errNotImplemented
}
