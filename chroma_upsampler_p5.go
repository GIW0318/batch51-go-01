// Package task upsamples 4:2:0 chroma to 4:4:4 with a fixed chroma siting,
// optional luma edge guidance and a per frame sample budget.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Siting is the chroma sample position relative to the luma grid.
type Siting string

// Supported chroma sitings.
const (
	SitingCenter  Siting = "center"
	SitingLeft    Siting = "left"
	SitingTopLeft Siting = "top_left"
)

// ColorSpace selects the luma and chroma coefficients of the following matrix
// stage.
type ColorSpace string

// Supported colour matrices.
const (
	ColorSpaceBT709 ColorSpace = "bt709"
	ColorSpaceBT601 ColorSpace = "bt601"
)

// Bit depth window of the source planes.
const (
	MinBitDepth = 8
	MaxBitDepth = 16
)

// MaxPixels caps one frame at 1920 by 1080 samples.
const (
	MaxPixels = 1920 * 1080
)

// Errors returned by the upsampler. A rejected call leaves settings and
// counters untouched.
var (
	ErrBadSiting     = errors.New("unsupported chroma siting")
	ErrBadMatrix     = errors.New("unsupported colour matrix")
	ErrBadBitDepth   = errors.New("bit depth out of range")
	ErrEmptyPlane    = errors.New("plane has no samples")
	ErrPlaneMismatch = errors.New("plane dimensions do not match the 4:2:0 layout")
	ErrFrameTooLarge = errors.New("frame exceeds the pixel budget")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Plane is one component plane with normalised samples in the zero to one
// range.
type Plane struct {
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Data   []float32 `json:"data"`
}

// Chroma444 is the reconstructed full resolution frame.
type Chroma444 struct {
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Y      []float32 `json:"y"`
	U      []float32 `json:"u"`
	V      []float32 `json:"v"`
}

// UpsampleStats is the observable state of the upsampler.
type UpsampleStats struct {
	Siting     Siting     `json:"siting"`
	Matrix     ColorSpace `json:"matrix"`
	BitDepth   int        `json:"bit_depth"`
	FullRange  bool       `json:"full_range"`
	EdgeGuided bool       `json:"edge_guided"`
	Frames     int        `json:"frames"`
	Samples    int        `json:"samples"`
}

// ChromaUpsamplerP5 owns the siting, range and guidance settings.
type ChromaUpsamplerP5 struct{}

// NewChromaUpsamplerP5 builds an upsampler with centre siting, the BT.709
// matrix, ten bit sources, full range input and edge guidance disabled.
func NewChromaUpsamplerP5() *ChromaUpsamplerP5 {
	return &ChromaUpsamplerP5{}
}

// SetSiting fixes the chroma sample position for the whole frame.
func (u *ChromaUpsamplerP5) SetSiting(s Siting) error {
	return errNotImplemented
}

// SetMatrix selects the colour matrix used by the following conversion stage.
func (u *ChromaUpsamplerP5) SetMatrix(colorspace ColorSpace) error {
	return errNotImplemented
}

// Upsample reconstructs full resolution chroma from one 4:2:0 frame.
func (u *ChromaUpsamplerP5) Upsample(y, uPlane, vPlane Plane) (Chroma444, error) {
	return Chroma444{}, errNotImplemented
}

// SetEdgeGuided toggles luma edge guided chroma interpolation.
func (u *ChromaUpsamplerP5) SetEdgeGuided(on bool) error {
	return errNotImplemented
}

// SetRange selects full or limited range input.
func (u *ChromaUpsamplerP5) SetRange(full bool) error {
	return errNotImplemented
}

// SetBitDepth sets the source bit depth.
func (u *ChromaUpsamplerP5) SetBitDepth(bits int) error {
	return errNotImplemented
}

// Stats reports the settings and the processed frame counters.
func (u *ChromaUpsamplerP5) Stats() UpsampleStats {
	return UpsampleStats{}
}

// Reset clears the counters and keeps the settings.
func (u *ChromaUpsamplerP5) Reset() error {
	return errNotImplemented
}

// Snapshot serialises the settings and the counters.
func (u *ChromaUpsamplerP5) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (u *ChromaUpsamplerP5) Restore(data []byte) error {
	return errNotImplemented
}
