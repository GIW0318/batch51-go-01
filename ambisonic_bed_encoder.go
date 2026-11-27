// Package task encodes ambisonic sound beds, rotates the encoded field and
// decodes a bed to an interleaved binaural stereo pair.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// NormMode selects the spherical harmonic normalisation of the bed.
type NormMode string

// Supported normalisation modes.
const (
	NormSN3D NormMode = "sn3d"
	NormN3D  NormMode = "n3d"
)

// Contract defaults used when the configuration leaves a field unset.
const (
	MinOrder           = 1
	MaxOrder           = 3
	DefaultTaps        = 16
	DefaultBlockSize   = 128
	DefaultSourceLimit = 64
	DefaultBudget      = 4096
)

// Errors reported by the encoder.
var (
	ErrBadOrder          = errors.New("ambisonic order must be between 1 and 3")
	ErrBadNorm           = errors.New("unknown normalisation mode")
	ErrBadSource         = errors.New("source fields are out of range")
	ErrSourceLimit       = errors.New("source limit reached")
	ErrBadQuaternion     = errors.New("rotation quaternion must be unit length")
	ErrBadBedLength      = errors.New("bed length does not match the channel count")
	ErrBadHRTF           = errors.New("hrtf tap count is out of range")
	ErrConvolutionBudget = errors.New("hrtf taps exceed the per-frame convolution budget")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// AmbisonicConfig configures one bed encoder.
type AmbisonicConfig struct {
	Order             int
	Norm              NormMode
	HRTF              string
	Taps              int
	BlockSize         int
	MaxSources        int
	ConvolutionBudget int
}

// Source is one mono source placed on the unit sphere. Azimuth is measured in
// degrees from the front axis, positive towards the left. Elevation is measured
// in degrees above the horizon. Order zero means "use the encoder order".
type Source struct {
	ID        string
	Azimuth   float64
	Elevation float64
	Gain      float64
	Order     int
}

// Quaternion is a unit quaternion in w, x, y, z order.
type Quaternion struct {
	W float64
	X float64
	Y float64
	Z float64
}

// AmbisonicEncoder owns the accumulated bed, the encoder order and the applied
// rotation.
type AmbisonicEncoder struct {
	cfg AmbisonicConfig
}

// NewAmbisonicEncoder builds an encoder for the given configuration.
func NewAmbisonicEncoder(cfg AmbisonicConfig) *AmbisonicEncoder {
	return &AmbisonicEncoder{cfg: cfg}
}

// Encode adds one source to the bed and returns the whole bed after the add.
func (e *AmbisonicEncoder) Encode(source Source) ([]float64, error) {
	return nil, errNotImplemented
}

// Rotate applies a unit quaternion increment to the encoded field.
func (e *AmbisonicEncoder) Rotate(q Quaternion) error {
	return errNotImplemented
}

// DecodeBinaural convolves the bed with the active HRTF set.
func (e *AmbisonicEncoder) DecodeBinaural(bed []float64) ([]float32, error) {
	return nil, errNotImplemented
}

// SetNormalization switches the bed normalisation mode.
func (e *AmbisonicEncoder) SetNormalization(mode NormMode) error {
	return errNotImplemented
}

// SetHRTF switches the head related transfer function dataset.
func (e *AmbisonicEncoder) SetHRTF(name string, taps int) error {
	return errNotImplemented
}

// Snapshot serialises the encoder state.
func (e *AmbisonicEncoder) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the encoder state with a snapshot, all or nothing.
func (e *AmbisonicEncoder) Restore(data []byte) error {
	return errNotImplemented
}

// Bed returns a copy of the accumulated bed.
func (e *AmbisonicEncoder) Bed() []float64 {
	return nil
}

// Reset clears the bed, the counters and the applied rotation.
func (e *AmbisonicEncoder) Reset() error {
	return errNotImplemented
}

// Order reports the effective encoder order.
func (e *AmbisonicEncoder) Order() int {
	return 0
}

// Channels reports the number of bed channels.
func (e *AmbisonicEncoder) Channels() int {
	return 0
}

// Norm reports the effective normalisation mode.
func (e *AmbisonicEncoder) Norm() NormMode {
	return ""
}

// Taps reports the active HRTF tap count.
func (e *AmbisonicEncoder) Taps() int {
	return 0
}

// BlockSize reports the decode block size in frames.
func (e *AmbisonicEncoder) BlockSize() int {
	return 0
}

// SourceCount reports how many sources were accepted into the bed.
func (e *AmbisonicEncoder) SourceCount() int {
	return 0
}

// Truncated reports how many sources were cut down to the encoder order.
func (e *AmbisonicEncoder) Truncated() int {
	return 0
}

// Rotation reports the accumulated rotation quaternion.
func (e *AmbisonicEncoder) Rotation() Quaternion {
	return Quaternion{}
}

// ConvolutionCost reports channels times taps for one decoded block.
func (e *AmbisonicEncoder) ConvolutionCost() int {
	return 0
}
