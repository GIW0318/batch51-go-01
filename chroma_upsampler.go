// Package task upsamples 4:2:0 and 4:2:2 chroma planes to 4:4:4 while keeping
// the luma grid and the chroma sampling phase aligned.
package task

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
	"strings"
	"sync"
)

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
	mu     sync.RWMutex
	cfg    ChromaConfig
	blocks int
}

// NewChromaUpsampler builds an upsampler for the given configuration.
func NewChromaUpsampler(cfg ChromaConfig) *ChromaUpsampler {
	return &ChromaUpsampler{cfg: normalizeConfig(cfg)}
}

// Upsample converts a subsampled frame to 4:4:4.
func (u *ChromaUpsampler) Upsample(frame YUVFrame) (YUV444, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	cw, ch, err := validateFrame(frame, u.cfg)
	if err != nil {
		return YUV444{}, err
	}

	out := YUV444{
		W: frame.W,
		H: frame.H,
		Y: make([]float64, len(frame.Y)),
		U: make([]float64, len(frame.Y)),
		V: make([]float64, len(frame.Y)),
	}
	copy(out.Y, frame.Y)

	ctx := newSampleContext(frame, cw, ch, u.cfg)
	blocks := 0
	for y := 0; y < frame.H; {
		rows := u.cfg.BlockRows
		if rows > frame.H-y {
			rows = frame.H - y
		}
		for row := y; row < y+rows; row++ {
			dst := row * frame.W
			for x := 0; x < frame.W; x++ {
				out.U[dst+x] = ctx.sample(frame.U, x, row)
				out.V[dst+x] = ctx.sample(frame.V, x, row)
			}
		}
		y += rows
		blocks++
	}

	u.blocks = blocks
	return out, nil
}

// Sample reads one 4:4:4 sample at luma coordinates.
func (u *ChromaUpsampler) Sample(frame YUVFrame, x, y int) (float64, float64, float64, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()

	cw, ch, err := validateFrame(frame, u.cfg)
	if err != nil {
		return 0, 0, 0, err
	}
	if x < 0 || x >= frame.W || y < 0 || y >= frame.H {
		return 0, 0, 0, ErrBadFrame
	}

	ctx := newSampleContext(frame, cw, ch, u.cfg)
	return frame.Y[y*frame.W+x], ctx.sample(frame.U, x, y), ctx.sample(frame.V, x, y), nil
}

// SetPhase switches the chroma sampling phase.
func (u *ChromaUpsampler) SetPhase(phase Phase) error {
	if !validPhase(phase) {
		return ErrBadPhase
	}
	u.mu.Lock()
	u.cfg.Phase = phase
	u.mu.Unlock()
	return nil
}

// SetSubsampling switches between 4:2:0 and 4:2:2.
func (u *ChromaUpsampler) SetSubsampling(mode Subsampling) error {
	if !validSubsampling(mode) {
		return ErrBadSubsampling
	}
	u.mu.Lock()
	u.cfg.Subsampling = mode
	u.mu.Unlock()
	return nil
}

// SetEdgeMode switches the edge interpolation mode.
func (u *ChromaUpsampler) SetEdgeMode(mode EdgeMode) error {
	if !validEdge(mode) {
		return ErrBadEdge
	}
	u.mu.Lock()
	u.cfg.Edge = mode
	u.mu.Unlock()
	return nil
}

// HandleOddSize reports the chroma grid and padding for a frame size.
func (u *ChromaUpsampler) HandleOddSize(w, h int) (PadPlan, error) {
	if w <= 0 || h <= 0 {
		return PadPlan{}, ErrBadSize
	}

	u.mu.RLock()
	cfg := u.cfg
	u.mu.RUnlock()

	if exceedsBudget(w, h, cfg.MaxPixels) {
		return PadPlan{}, ErrPixelBudget
	}
	cw, ch := chromaDimensions(w, h, cfg.Subsampling)
	padBottom := 0
	if cfg.Subsampling == Sub420 {
		padBottom = h % 2
	}
	return PadPlan{
		W:         w,
		H:         h,
		ChromaW:   cw,
		ChromaH:   ch,
		PadRight:  w % 2,
		PadBottom: padBottom,
		BlockRows: cfg.BlockRows,
		Blocks:    blockCount(h, cfg.BlockRows),
	}, nil
}

// Snapshot serialises the upsampler state.
func (u *ChromaUpsampler) Snapshot() ([]byte, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()

	const size = 36
	data := make([]byte, size)
	copy(data[:4], "CHR1")
	data[4] = 1
	data[5] = byte(subsamplingCode(u.cfg.Subsampling))
	data[6] = byte(phaseCode(u.cfg.Phase))
	data[7] = byte(edgeCode(u.cfg.Edge))
	binary.BigEndian.PutUint64(data[8:16], uint64(u.cfg.MaxPixels))
	binary.BigEndian.PutUint64(data[16:24], uint64(u.cfg.BlockRows))
	binary.BigEndian.PutUint64(data[24:32], uint64(u.blocks))
	binary.BigEndian.PutUint32(data[32:36], crc32.ChecksumIEEE(data[:32]))
	return data, nil
}

// Restore replaces the upsampler state with a snapshot, all or nothing.
func (u *ChromaUpsampler) Restore(data []byte) error {
	if len(data) != 36 || string(data[:4]) != "CHR1" || data[4] != 1 {
		return ErrBadSnapshot
	}
	if binary.BigEndian.Uint32(data[32:36]) != crc32.ChecksumIEEE(data[:32]) {
		return ErrBadSnapshot
	}

	sub, ok := subsamplingFromCode(data[5])
	if !ok {
		return ErrBadSnapshot
	}
	phase, ok := phaseFromCode(data[6])
	if !ok {
		return ErrBadSnapshot
	}
	edge, ok := edgeFromCode(data[7])
	if !ok {
		return ErrBadSnapshot
	}
	maxPixels, ok := snapshotInt(binary.BigEndian.Uint64(data[8:16]))
	if !ok || maxPixels <= 0 {
		return ErrBadSnapshot
	}
	blockRows, ok := snapshotInt(binary.BigEndian.Uint64(data[16:24]))
	if !ok || blockRows <= 0 {
		return ErrBadSnapshot
	}
	blocks, ok := snapshotInt(binary.BigEndian.Uint64(data[24:32]))
	if !ok || blocks < 0 {
		return ErrBadSnapshot
	}

	u.mu.Lock()
	u.cfg = ChromaConfig{
		Subsampling: sub,
		Phase:       phase,
		Edge:        edge,
		MaxPixels:   maxPixels,
		BlockRows:   blockRows,
	}
	u.blocks = blocks
	u.mu.Unlock()
	return nil
}

// Subsampling reports the effective chroma layout.
func (u *ChromaUpsampler) Subsampling() Subsampling {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.cfg.Subsampling
}

// Phase reports the effective sampling phase.
func (u *ChromaUpsampler) Phase() Phase {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.cfg.Phase
}

// Edge reports the effective edge mode.
func (u *ChromaUpsampler) Edge() EdgeMode {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.cfg.Edge
}

// BlockCount reports how many blocks the last Upsample call processed.
func (u *ChromaUpsampler) BlockCount() int {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.blocks
}

// Reset clears the per frame counters.
func (u *ChromaUpsampler) Reset() error {
	u.mu.Lock()
	u.blocks = 0
	u.mu.Unlock()
	return nil
}

func normalizeConfig(cfg ChromaConfig) ChromaConfig {
	if !validSubsampling(cfg.Subsampling) {
		cfg.Subsampling = Sub420
	}
	if !validPhase(cfg.Phase) {
		cfg.Phase = PhaseCentered
	}
	if !validEdge(cfg.Edge) {
		cfg.Edge = EdgeDirectional
	}
	if cfg.MaxPixels <= 0 {
		cfg.MaxPixels = DefaultMaxPixels
	}
	if cfg.BlockRows <= 0 {
		cfg.BlockRows = DefaultBlockRows
	}
	return cfg
}

func validSubsampling(mode Subsampling) bool {
	return mode == Sub420 || mode == Sub422
}

func validPhase(phase Phase) bool {
	return phase == PhaseCentered || phase == PhaseCosited
}

func validEdge(mode EdgeMode) bool {
	return mode == EdgeDirectional || mode == EdgeBilinear
}

func chromaDimensions(w, h int, mode Subsampling) (int, int) {
	cw := w/2 + w%2
	ch := h/2 + h%2
	if mode == Sub422 {
		ch = h
	}
	return cw, ch
}

func exceedsBudget(w, h, maxPixels int) bool {
	if w <= 0 || h <= 0 || maxPixels <= 0 {
		return true
	}
	return w > maxPixels/h
}

func validateFrame(frame YUVFrame, cfg ChromaConfig) (int, int, error) {
	if frame.W <= 0 || frame.H <= 0 {
		return 0, 0, ErrBadFrame
	}
	if exceedsBudget(frame.W, frame.H, cfg.MaxPixels) {
		return 0, 0, ErrPixelBudget
	}
	cw, ch := chromaDimensions(frame.W, frame.H, cfg.Subsampling)
	if len(frame.Y) != frame.W*frame.H || len(frame.U) != cw*ch || len(frame.V) != cw*ch {
		return 0, 0, ErrBadFrame
	}
	return cw, ch, nil
}

func blockCount(h, blockRows int) int {
	if h <= 0 || blockRows <= 0 {
		return 0
	}
	return 1 + (h-1)/blockRows
}

type sampleContext struct {
	frame YUVFrame
	cw    int
	ch    int
	cfg   ChromaConfig
	rgb   bool
}

func newSampleContext(frame YUVFrame, cw, ch int, cfg ChromaConfig) sampleContext {
	return sampleContext{
		frame: frame,
		cw:    cw,
		ch:    ch,
		cfg:   cfg,
		rgb:   strings.EqualFold(strings.TrimSpace(frame.ColorSpace), "rgb"),
	}
}

func (c sampleContext) sample(plane []float64, x, y int) float64 {
	if c.cfg.Edge != EdgeDirectional || c.cfg.Subsampling == Sub422 {
		return c.bilinear(plane, x, y)
	}

	dx := math.Abs(c.edgeLuma(x-1, y) - c.edgeLuma(x+1, y))
	dy := math.Abs(c.edgeLuma(x, y-1) - c.edgeLuma(x, y+1))
	if dy > dx*2 {
		return c.horizontal(plane, x, y)
	}
	if dx > dy*2 {
		return c.vertical(plane, x, y)
	}
	return c.bilinear(plane, x, y)
}

func (c sampleContext) bilinear(plane []float64, x, y int) float64 {
	fx := c.clampCoord(c.horizontalCoord(x), c.cw-1)
	fy := c.clampCoord(c.verticalCoord(y), c.ch-1)
	x0, x1, tx := bounds(fx, c.cw)
	y0, y1, ty := bounds(fy, c.ch)
	top := lerp(c.at(plane, x0, y0), c.at(plane, x1, y0), tx)
	bottom := lerp(c.at(plane, x0, y1), c.at(plane, x1, y1), tx)
	return lerp(top, bottom, ty)
}

func (c sampleContext) horizontal(plane []float64, x, y int) float64 {
	fx := c.clampCoord(c.horizontalCoord(x), c.cw-1)
	x0, x1, tx := bounds(fx, c.cw)
	row := int(math.Round(c.clampCoord(c.verticalCoord(y), c.ch-1)))
	return lerp(c.at(plane, x0, row), c.at(plane, x1, row), tx)
}

func (c sampleContext) vertical(plane []float64, x, y int) float64 {
	fy := c.clampCoord(c.verticalCoord(y), c.ch-1)
	y0, y1, ty := bounds(fy, c.ch)
	col := int(math.Round(c.clampCoord(c.horizontalCoord(x), c.cw-1)))
	return lerp(c.at(plane, col, y0), c.at(plane, col, y1), ty)
}

func (c sampleContext) horizontalCoord(x int) float64 {
	if c.cfg.Phase == PhaseCosited {
		return float64(x) / 2
	}
	return (float64(x) - 0.5) / 2
}

func (c sampleContext) verticalCoord(y int) float64 {
	if c.cfg.Subsampling == Sub422 {
		return float64(y)
	}
	if c.cfg.Phase == PhaseCosited {
		return float64(y) / 2
	}
	return (float64(y) - 0.5) / 2
}

func (c sampleContext) clampCoord(value float64, last int) float64 {
	if value < 0 {
		return 0
	}
	if value > float64(last) {
		return float64(last)
	}
	return value
}

func (c sampleContext) at(plane []float64, x, y int) float64 {
	return plane[y*c.cw+x]
}

func (c sampleContext) edgeLuma(x, y int) float64 {
	x = integerClamp(x, 0, c.frame.W-1)
	y = integerClamp(y, 0, c.frame.H-1)
	if !c.rgb {
		return c.frame.Y[y*c.frame.W+x]
	}
	r := c.frame.Y[y*c.frame.W+x]
	g := c.bilinear(c.frame.U, x, y)
	b := c.bilinear(c.frame.V, x, y)
	return 0.299*r + 0.587*g + 0.114*b
}

func bounds(value float64, n int) (int, int, float64) {
	if n <= 1 {
		return 0, 0, 0
	}
	lo := int(math.Floor(value))
	if lo >= n-1 {
		return n - 1, n - 1, 0
	}
	return lo, lo + 1, value - float64(lo)
}

func lerp(a, b, t float64) float64 {
	return a + (b-a)*t
}

func integerClamp(value, lo, hi int) int {
	if value < lo {
		return lo
	}
	if value > hi {
		return hi
	}
	return value
}

func subsamplingCode(mode Subsampling) int {
	if mode == Sub422 {
		return 1
	}
	return 0
}

func phaseCode(phase Phase) int {
	if phase == PhaseCosited {
		return 1
	}
	return 0
}

func edgeCode(mode EdgeMode) int {
	if mode == EdgeBilinear {
		return 1
	}
	return 0
}

func subsamplingFromCode(code byte) (Subsampling, bool) {
	switch code {
	case 0:
		return Sub420, true
	case 1:
		return Sub422, true
	default:
		return "", false
	}
}

func phaseFromCode(code byte) (Phase, bool) {
	switch code {
	case 0:
		return PhaseCentered, true
	case 1:
		return PhaseCosited, true
	default:
		return "", false
	}
}

func edgeFromCode(code byte) (EdgeMode, bool) {
	switch code {
	case 0:
		return EdgeDirectional, true
	case 1:
		return EdgeBilinear, true
	default:
		return "", false
	}
}

func snapshotInt(value uint64) (int, bool) {
	maxInt := uint64(^uint(0) >> 1)
	if value > maxInt {
		return 0, false
	}
	return int(value), true
}
