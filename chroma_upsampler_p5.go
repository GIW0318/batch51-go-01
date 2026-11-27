// Package task upsamples 4:2:0 chroma to 4:4:4 with a fixed chroma siting,
// optional luma edge guidance and a per frame sample budget.
package task

import (
	"encoding/json"
	"errors"
	"hash/crc32"
	"math"
	"sync"
)

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

// maxInterpSamples caps the bilinear tap evaluations of one frame. Frames
// that would exceed the budget are processed block by block with nearest
// sample replication instead of being dropped.
const maxInterpSamples = 1 << 20

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

// settings is the tunable state that must switch atomically per frame.
type settings struct {
	siting     Siting
	matrix     ColorSpace
	bitDepth   int
	fullRange  bool
	edgeGuided bool
}

// ChromaUpsamplerP5 owns the siting, range and guidance settings.
type ChromaUpsamplerP5 struct {
	mu      sync.RWMutex
	cfg     settings
	frames  int
	samples int
}

// NewChromaUpsamplerP5 builds an upsampler with centre siting, the BT.709
// matrix, ten bit sources, full range input and edge guidance disabled.
func NewChromaUpsamplerP5() *ChromaUpsamplerP5 {
	return &ChromaUpsamplerP5{
		cfg: settings{
			siting:     SitingCenter,
			matrix:     ColorSpaceBT709,
			bitDepth:   10,
			fullRange:  true,
			edgeGuided: false,
		},
	}
}

func validSiting(s Siting) bool {
	switch s {
	case SitingCenter, SitingLeft, SitingTopLeft:
		return true
	}
	return false
}

func validMatrix(m ColorSpace) bool {
	switch m {
	case ColorSpaceBT709, ColorSpaceBT601:
		return true
	}
	return false
}

func validBitDepth(bits int) bool {
	return bits >= MinBitDepth && bits <= MaxBitDepth
}

// SetSiting fixes the chroma sample position for the whole frame.
func (u *ChromaUpsamplerP5) SetSiting(s Siting) error {
	if !validSiting(s) {
		return ErrBadSiting
	}
	u.mu.Lock()
	u.cfg.siting = s
	u.mu.Unlock()
	return nil
}

// SetMatrix selects the colour matrix used by the following conversion stage.
func (u *ChromaUpsamplerP5) SetMatrix(colorspace ColorSpace) error {
	if !validMatrix(colorspace) {
		return ErrBadMatrix
	}
	u.mu.Lock()
	u.cfg.matrix = colorspace
	u.mu.Unlock()
	return nil
}

// SetEdgeGuided toggles luma edge guided chroma interpolation.
func (u *ChromaUpsamplerP5) SetEdgeGuided(on bool) error {
	u.mu.Lock()
	u.cfg.edgeGuided = on
	u.mu.Unlock()
	return nil
}

// SetRange selects full or limited range input.
func (u *ChromaUpsamplerP5) SetRange(full bool) error {
	u.mu.Lock()
	u.cfg.fullRange = full
	u.mu.Unlock()
	return nil
}

// SetBitDepth sets the source bit depth.
func (u *ChromaUpsamplerP5) SetBitDepth(bits int) error {
	if !validBitDepth(bits) {
		return ErrBadBitDepth
	}
	u.mu.Lock()
	u.cfg.bitDepth = bits
	u.mu.Unlock()
	return nil
}

// Stats reports the settings and the processed frame counters.
func (u *ChromaUpsamplerP5) Stats() UpsampleStats {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return UpsampleStats{
		Siting:     u.cfg.siting,
		Matrix:     u.cfg.matrix,
		BitDepth:   u.cfg.bitDepth,
		FullRange:  u.cfg.fullRange,
		EdgeGuided: u.cfg.edgeGuided,
		Frames:     u.frames,
		Samples:    u.samples,
	}
}

// Reset clears the counters and keeps the settings.
func (u *ChromaUpsamplerP5) Reset() error {
	u.mu.Lock()
	u.frames = 0
	u.samples = 0
	u.mu.Unlock()
	return nil
}

// sitingOffsets returns the luma grid offset of a chroma sample for the
// horizontal and vertical axis.
func sitingOffsets(s Siting) (hOff, vOff float64) {
	switch s {
	case SitingLeft:
		return 0, 0.5
	case SitingTopLeft:
		return 0, 0
	default:
		return 0.5, 0.5
	}
}

// axisTaps locates the two neighbouring sample indices and the blend factor
// for a luma coordinate along one axis. Coordinates outside the sample grid
// collapse onto the nearest edge sample instead of extrapolating.
func axisTaps(pos int, off float64, n int) (i0, i1 int, t float64) {
	f := (float64(pos) - off) / 2
	if f <= 0 {
		return 0, 0, 0
	}
	if f >= float64(n-1) {
		return n - 1, n - 1, 0
	}
	i0 = int(math.Floor(f))
	return i0, i0 + 1, f - float64(i0)
}

// Upsample reconstructs full resolution chroma from one 4:2:0 frame.
func (u *ChromaUpsamplerP5) Upsample(y, uPlane, vPlane Plane) (Chroma444, error) {
	u.mu.RLock()
	cfg := u.cfg
	u.mu.RUnlock()

	// Validation order: dimensions, pixel budget, empty samples, layout.
	if y.Width <= 0 || y.Height <= 0 || uPlane.Width <= 0 || uPlane.Height <= 0 ||
		vPlane.Width <= 0 || vPlane.Height <= 0 {
		return Chroma444{}, ErrEmptyPlane
	}
	w, h := y.Width, y.Height
	if w*h > MaxPixels {
		return Chroma444{}, ErrFrameTooLarge
	}
	if len(y.Data) == 0 || len(uPlane.Data) == 0 || len(vPlane.Data) == 0 {
		return Chroma444{}, ErrEmptyPlane
	}
	cw, ch := (w+1)/2, (h+1)/2
	if len(y.Data) != w*h ||
		uPlane.Width != cw || uPlane.Height != ch || len(uPlane.Data) != cw*ch ||
		vPlane.Width != cw || vPlane.Height != ch || len(vPlane.Data) != cw*ch {
		return Chroma444{}, ErrPlaneMismatch
	}

	out := Chroma444{
		Width:  w,
		Height: h,
		Y:      make([]float32, w*h),
		U:      make([]float32, w*h),
		V:      make([]float32, w*h),
	}
	copy(out.Y, y.Data)

	hOff, vOff := sitingOffsets(cfg.siting)

	// Per frame tap budget: rows are processed in blocks and a block that
	// would exceed the budget falls back to nearest sample replication
	// instead of dropping the frame.
	tapsPerRow := 4 * w
	rowsPerBlock := maxInterpSamples / tapsPerRow
	if rowsPerBlock < 1 {
		rowsPerBlock = 1
	}
	for row0 := 0; row0 < h; row0 += rowsPerBlock {
		row1 := row0 + rowsPerBlock
		if row1 > h {
			row1 = h
		}
		bilinear := tapsPerRow*(row1-row0) <= maxInterpSamples
		for row := row0; row < row1; row++ {
			y0, y1, ty := axisTaps(row, vOff, ch)
			for col := 0; col < w; col++ {
				x0, x1, tx := axisTaps(col, hOff, cw)
				idx := row*w + col
				if !bilinear {
					out.U[idx] = uPlane.Data[y0*cw+x0]
					out.V[idx] = vPlane.Data[y0*cw+x0]
					continue
				}
				out.U[idx] = blend(uPlane.Data, y.Data, cw, w, x0, x1, y0, y1, tx, ty, col, row, cfg.edgeGuided)
				out.V[idx] = blend(vPlane.Data, y.Data, cw, w, x0, x1, y0, y1, tx, ty, col, row, cfg.edgeGuided)
			}
		}
	}

	// Range normalisation runs before the matrix stage so every conversion
	// path sees the same fixed order and no colour cast creeps in.
	if !cfg.fullRange {
		normalizeLimitedChroma(out.U, cfg.bitDepth)
		normalizeLimitedChroma(out.V, cfg.bitDepth)
	}
	applyMatrix(out.U, out.V, cfg.matrix)

	u.mu.Lock()
	u.frames++
	u.samples += w * h
	u.mu.Unlock()
	return out, nil
}

// blend evaluates the bilinear chroma sample at one luma position. With edge
// guidance enabled each tap is reweighted by the luma similarity between the
// tap position and the target pixel so chroma does not bleed across edges.
func blend(chroma, luma []float32, cw, lw, x0, x1, y0, y1 int, tx, ty float64, lx, ly int, guided bool) float32 {
	w00 := (1 - tx) * (1 - ty)
	w10 := tx * (1 - ty)
	w01 := (1 - tx) * ty
	w11 := tx * ty
	c00 := chroma[y0*cw+x0]
	c10 := chroma[y0*cw+x1]
	c01 := chroma[y1*cw+x0]
	c11 := chroma[y1*cw+x1]
	if guided {
		target := luma[ly*lw+lx]
		w00 *= lumaWeight(luma, lw, 2*x0, 2*y0, target)
		w10 *= lumaWeight(luma, lw, 2*x1, 2*y0, target)
		w01 *= lumaWeight(luma, lw, 2*x0, 2*y1, target)
		w11 *= lumaWeight(luma, lw, 2*x1, 2*y1, target)
	}
	sum := w00 + w10 + w01 + w11
	if sum <= 0 {
		return c00
	}
	v := (w00*float64(c00) + w10*float64(c10) + w01*float64(c01) + w11*float64(c11)) / sum
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return float32(v)
}

// lumaWeight dampens a tap whose luma neighbourhood differs from the target
// pixel, limiting interpolation across luma edges.
func lumaWeight(luma []float32, lw, x, y int, target float32) float64 {
	lh := len(luma) / lw
	if x < 0 {
		x = 0
	}
	if x >= lw {
		x = lw - 1
	}
	if y < 0 {
		y = 0
	}
	if y >= lh {
		y = lh - 1
	}
	d := math.Abs(float64(luma[y*lw+x] - target))
	return 1 / (1 + 16*d)
}

// normalizeLimitedChroma expands limited range chroma code values to the
// full normalised range. It always runs before any matrix coefficient is
// applied.
func normalizeLimitedChroma(data []float32, bits int) {
	scale := float64(uint64(1)<<bits - 1)
	lo := 16 * scale / 255
	hi := 240 * scale / 255
	span := hi - lo
	for i, v := range data {
		n := (float64(v) - lo) / span
		if n < 0 {
			n = 0
		}
		if n > 1 {
			n = 1
		}
		data[i] = float32(n)
	}
}

// applyMatrix is the coefficient stage of the pipeline. The output stays in
// YUV so the stage is an identity on the planes themselves; keeping it as an
// explicit step pins the range normalise then matrix order for every path.
func applyMatrix(uPlane, vPlane []float32, m ColorSpace) {
	_ = uPlane
	_ = vPlane
	_ = m
}

// snapshotPayload is the serialised state of the upsampler.
type snapshotPayload struct {
	Siting     Siting     `json:"siting"`
	Matrix     ColorSpace `json:"matrix"`
	BitDepth   int        `json:"bit_depth"`
	FullRange  bool       `json:"full_range"`
	EdgeGuided bool       `json:"edge_guided"`
	Frames     int        `json:"frames"`
	Samples    int        `json:"samples"`
}

// snapshotEnvelope wraps the payload with a version tag and a checksum so a
// corrupt snapshot is rejected instead of half applied.
type snapshotEnvelope struct {
	Version int    `json:"version"`
	Sum     uint32 `json:"sum"`
	Payload []byte `json:"payload"`
}

const snapshotVersion = 1

// Snapshot serialises the settings and the counters.
func (u *ChromaUpsamplerP5) Snapshot() ([]byte, error) {
	u.mu.RLock()
	payload, err := json.Marshal(snapshotPayload{
		Siting:     u.cfg.siting,
		Matrix:     u.cfg.matrix,
		BitDepth:   u.cfg.bitDepth,
		FullRange:  u.cfg.fullRange,
		EdgeGuided: u.cfg.edgeGuided,
		Frames:     u.frames,
		Samples:    u.samples,
	})
	u.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	return json.Marshal(snapshotEnvelope{
		Version: snapshotVersion,
		Sum:     crc32.ChecksumIEEE(payload),
		Payload: payload,
	})
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (u *ChromaUpsamplerP5) Restore(data []byte) error {
	var env snapshotEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return ErrBadSnapshot
	}
	if env.Version != snapshotVersion || crc32.ChecksumIEEE(env.Payload) != env.Sum {
		return ErrBadSnapshot
	}
	var p snapshotPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return ErrBadSnapshot
	}
	if !validSiting(p.Siting) || !validMatrix(p.Matrix) || !validBitDepth(p.BitDepth) ||
		p.Frames < 0 || p.Samples < 0 {
		return ErrBadSnapshot
	}
	u.mu.Lock()
	u.cfg = settings{
		siting:     p.Siting,
		matrix:     p.Matrix,
		bitDepth:   p.BitDepth,
		fullRange:  p.FullRange,
		edgeGuided: p.EdgeGuided,
	}
	u.frames = p.Frames
	u.samples = p.Samples
	u.mu.Unlock()
	return nil
}
