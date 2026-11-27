// Package task upsamples 4:2:0 and 4:2:2 chroma planes to 4:4:4 while keeping
// the luma grid and the chroma sampling phase aligned.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Subsampling is the chroma layout of the source frame.
type Subsampling string

// Supported chroma layouts.
const (
	Sub420 Subsampling = "420"
	Sub422 Subsampling = "422"
)

// Phase is the position of a chroma sample relative to the luma grid.
type Phase string

// Supported sampling phases.
const (
	PhaseCentered Phase = "centered"
	PhaseCosited  Phase = "cosited"
)

// EdgeMode selects the interpolation used around detected chroma edges.
type EdgeMode string

// Supported edge modes.
const (
	EdgeDirectional EdgeMode = "directional"
	EdgeBilinear    EdgeMode = "bilinear"
)

// Contract defaults used when the configuration leaves a field unset.
const (
	DefaultMaxPixels = 1 << 24
	DefaultBlockRows = 16
)

// Errors reported by the upsampler.
var (
	ErrBadFrame       = errors.New("frame dimensions or plane lengths do not match")
	ErrBadSize        = errors.New("frame size must be positive")
	ErrPixelBudget    = errors.New("frame exceeds the pixel budget")
	ErrBadSubsampling = errors.New("unknown chroma subsampling")
	ErrBadPhase       = errors.New("unknown chroma sampling phase")
	ErrBadEdge        = errors.New("unknown edge mode")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// ChromaConfig configures one upsampler.
type ChromaConfig struct {
	Subsampling Subsampling
	Phase       Phase
	Edge        EdgeMode
	MaxPixels   int
	BlockRows   int
}

// YUVFrame is one planar source frame. Y has W*H samples, U and V carry the
// subsampled chroma grid.
type YUVFrame struct {
	W          int
	H          int
	Y          []float64
	U          []float64
	V          []float64
	ColorSpace string
}

// YUV444 is a fully sampled frame with W*H samples per plane.
type YUV444 struct {
	W int
	H int
	Y []float64
	U []float64
	V []float64
}

// PadPlan describes the chroma grid and the padding needed at the right and
// bottom edges. The output size never changes.
type PadPlan struct {
	W         int
	H         int
	ChromaW   int
	ChromaH   int
	PadRight  int
	PadBottom int
	BlockRows int
	Blocks    int
}

// ChromaUpsampler owns the layout configuration and the per frame counters.
type ChromaUpsampler struct {
	cfg ChromaConfig
}

// NewChromaUpsampler builds an upsampler for the given configuration.
func NewChromaUpsampler(cfg ChromaConfig) *ChromaUpsampler {
	return &ChromaUpsampler{cfg: cfg}
}

// Upsample converts a subsampled frame to 4:4:4.
func (u *ChromaUpsampler) Upsample(frame YUVFrame) (YUV444, error) {
	return YUV444{}, errNotImplemented
}

// Sample reads one 4:4:4 sample at luma coordinates.
func (u *ChromaUpsampler) Sample(frame YUVFrame, x, y int) (float64, float64, float64, error) {
	return 0, 0, 0, errNotImplemented
}

// SetPhase switches the chroma sampling phase.
func (u *ChromaUpsampler) SetPhase(phase Phase) error {
	return errNotImplemented
}

// SetSubsampling switches between 4:2:0 and 4:2:2.
func (u *ChromaUpsampler) SetSubsampling(mode Subsampling) error {
	return errNotImplemented
}

// SetEdgeMode switches the edge interpolation mode.
func (u *ChromaUpsampler) SetEdgeMode(mode EdgeMode) error {
	return errNotImplemented
}

// HandleOddSize reports the chroma grid and padding for a frame size.
func (u *ChromaUpsampler) HandleOddSize(w, h int) (PadPlan, error) {
	return PadPlan{}, errNotImplemented
}

// Snapshot serialises the upsampler state.
func (u *ChromaUpsampler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the upsampler state with a snapshot, all or nothing.
func (u *ChromaUpsampler) Restore(data []byte) error {
	return errNotImplemented
}

// Subsampling reports the effective chroma layout.
func (u *ChromaUpsampler) Subsampling() Subsampling {
	return ""
}

// Phase reports the effective sampling phase.
func (u *ChromaUpsampler) Phase() Phase {
	return ""
}

// Edge reports the effective edge mode.
func (u *ChromaUpsampler) Edge() EdgeMode {
	return ""
}

// BlockCount reports how many blocks the last Upsample call processed.
func (u *ChromaUpsampler) BlockCount() int {
	return 0
}

// Reset clears the per frame counters.
func (u *ChromaUpsampler) Reset() error {
	return errNotImplemented
}
