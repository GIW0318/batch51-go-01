// Package task plans parallax occlusion sampling: it keeps the height atlas,
// the step, depth and fade parameters and the per LOD variants used by the
// material shaders.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Errors reported by the mapper.
var (
	ErrBadConfig       = errors.New("pom configuration is invalid")
	ErrBadMaterial     = errors.New("material reference is invalid")
	ErrUnknownMaterial = errors.New("height map is not registered")
	ErrBadHeightMap    = errors.New("height map payload is invalid")
	ErrBadSteps        = errors.New("step count is invalid")
	ErrBadScale        = errors.New("depth scale is invalid")
	ErrBadFade         = errors.New("fade range is invalid")
	ErrBadView         = errors.New("tangent space view vector is invalid")
	ErrBadLOD          = errors.New("lod level is invalid")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Vec2 is a texture coordinate pair.
type Vec2 struct {
	U float32 `json:"u"`
	V float32 `json:"v"`
}

// Vec3 is a tangent space direction.
type Vec3 struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// HeightMap is a one channel height atlas laid out as a cell grid.
type HeightMap struct {
	ID     string    `json:"id"`
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Cols   int       `json:"cols"`
	Rows   int       `json:"rows"`
	Values []float32 `json:"values"`
}

// MaterialRef selects one material cell of the atlas.
type MaterialRef struct {
	ID       string  `json:"id"`
	Cell     int     `json:"cell"`
	Distance float32 `json:"distance"`
}

// SampleResult is the outcome of one pixel sample.
type SampleResult struct {
	UV      Vec2    `json:"uv"`
	Height  float32 `json:"height"`
	Fade    float32 `json:"fade"`
	Steps   int     `json:"steps"`
	Clipped bool    `json:"clipped"`
}

// POMVariant is the compiled parameter set of one LOD.
type POMVariant struct {
	LOD        int     `json:"lod"`
	Steps      int     `json:"steps"`
	DepthScale float32 `json:"depth_scale"`
}

// POMStats is the live accounting of the mapper.
type POMStats struct {
	Materials int `json:"materials"`
	Variants  int `json:"variants"`
	Samples   int `json:"samples"`
	StepTotal int `json:"step_total"`
	Merged    int `json:"merged"`
	Clipped   int `json:"clipped"`
	Builds    int `json:"builds"`
}

// POMConfig bounds the mapper.
type POMConfig struct {
	MaxSteps      int     `json:"max_steps"`
	MaxDepthScale float32 `json:"max_depth_scale"`
	MaxTexSize    int     `json:"max_tex_size"`
	MaxVariants   int     `json:"max_variants"`
	MaxLOD        int     `json:"max_lod"`
	FadeNear      float32 `json:"fade_near"`
	FadeFar       float32 `json:"fade_far"`
	ParallelZ     float32 `json:"parallel_z"`
}

// mapperState is immutable after publication. Every writer publishes a new
// complete state, so a concurrent reader only ever observes a whole table.
type mapperState struct {
	cfg        POMConfig
	steps      int
	maxSteps   int
	depthScale float32
	fadeNear   float32
	fadeFar    float32
	parallelZ  float32
	materials  map[string]HeightMap
	variants   map[int]POMVariant
}

type pomCounters struct {
	samples   atomic.Int64
	stepTotal atomic.Int64
	merged    atomic.Int64
	clipped   atomic.Int64
	builds    atomic.Int64
}

type variantBuild struct {
	done    chan struct{}
	variant POMVariant
	err     error
}

// ParallaxOcclusionMapper owns the height atlas, the sampling parameters and
// the variant cache.
type ParallaxOcclusionMapper struct {
	state atomic.Pointer[mapperState]

	stateMu  sync.Mutex
	buildMu  sync.Mutex
	inflight map[int]*variantBuild
	counters pomCounters
}

// NewParallaxOcclusionMapper validates the configuration and creates an empty
// mapper.
func NewParallaxOcclusionMapper(cfg POMConfig) (*ParallaxOcclusionMapper, error) {
	if !validConfig(cfg) {
		return nil, ErrBadConfig
	}
	m := &ParallaxOcclusionMapper{
		inflight: make(map[int]*variantBuild),
	}
	m.state.Store(&mapperState{
		cfg:        cfg,
		steps:      cfg.MaxSteps,
		maxSteps:   cfg.MaxSteps,
		depthScale: cfg.MaxDepthScale,
		fadeNear:   cfg.FadeNear,
		fadeFar:    cfg.FadeFar,
		parallelZ:  cfg.ParallelZ,
		materials:  make(map[string]HeightMap),
		variants:   make(map[int]POMVariant),
	})
	return m, nil
}

func validConfig(cfg POMConfig) bool {
	if cfg.MaxSteps <= 0 || cfg.MaxTexSize <= 0 || cfg.MaxVariants <= 0 || cfg.MaxLOD <= 0 {
		return false
	}
	if !finite32(cfg.MaxDepthScale) || cfg.MaxDepthScale <= 0 || cfg.MaxDepthScale > 1 {
		return false
	}
	if !finite32(cfg.FadeNear) || cfg.FadeNear < 0 {
		return false
	}
	if !finite32(cfg.FadeFar) || cfg.FadeFar <= cfg.FadeNear {
		return false
	}
	if !finite32(cfg.ParallelZ) || cfg.ParallelZ <= 0 {
		return false
	}
	return true
}

func finite32(v float32) bool {
	f := float64(v)
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

func (p *ParallaxOcclusionMapper) current() *mapperState {
	return p.state.Load()
}

// SetHeightMap installs or replaces the height atlas of one material family.
func (p *ParallaxOcclusionMapper) SetHeightMap(id string, tex HeightMap) error {
	if strings.TrimSpace(id) == "" {
		return ErrBadMaterial
	}
	state := p.current()
	if state == nil {
		return ErrBadConfig
	}
	if err := validateHeightMap(state.cfg, tex); err != nil {
		return err
	}

	tex.ID = id
	tex.Values = append([]float32(nil), tex.Values...)

	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	state = p.current()
	if state == nil {
		return ErrBadConfig
	}
	next := *state
	next.materials = cloneMaterials(state.materials)
	next.materials[id] = tex
	p.state.Store(&next)
	return nil
}

func validateHeightMap(cfg POMConfig, tex HeightMap) error {
	if tex.Width <= 0 || tex.Height <= 0 || tex.Cols <= 0 || tex.Rows <= 0 {
		return ErrBadHeightMap
	}
	if tex.Width > cfg.MaxTexSize || tex.Height > cfg.MaxTexSize {
		return ErrBadHeightMap
	}
	// A cell must contain at least one source texel. This also keeps the
	// atlas-cell bounds below free of zero-width or zero-height ranges.
	if tex.Cols > tex.Width || tex.Rows > tex.Height {
		return ErrBadHeightMap
	}
	maxInt := int(^uint(0) >> 1)
	if tex.Width > maxInt/tex.Height {
		return ErrBadHeightMap
	}
	if tex.Width*tex.Height != len(tex.Values) {
		return ErrBadHeightMap
	}
	for _, v := range tex.Values {
		if !(v >= 0 && v <= 1) {
			return ErrBadHeightMap
		}
	}
	return nil
}

func cloneMaterials(src map[string]HeightMap) map[string]HeightMap {
	dst := make(map[string]HeightMap, len(src)+1)
	for id, tex := range src {
		dst[id] = tex
	}
	return dst
}

func cloneVariants(src map[int]POMVariant) map[int]POMVariant {
	dst := make(map[int]POMVariant, len(src)+1)
	for lod, variant := range src {
		dst[lod] = variant
	}
	return dst
}

// SetSteps sets the step count used at full parallax weight.
func (p *ParallaxOcclusionMapper) SetSteps(n int) error {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	state := p.current()
	if state == nil || n < 1 || n > state.maxSteps {
		return ErrBadSteps
	}
	next := *state
	next.steps = n
	p.state.Store(&next)
	return nil
}

// SetMaxSteps lowers or raises the hard step ceiling.
func (p *ParallaxOcclusionMapper) SetMaxSteps(n int) error {
	if n <= 0 {
		return ErrBadSteps
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	state := p.current()
	if state == nil {
		return ErrBadSteps
	}
	next := *state
	next.maxSteps = n
	if next.steps > n {
		next.steps = n
	}
	p.state.Store(&next)
	return nil
}

// Steps reports the effective full weight step count.
func (p *ParallaxOcclusionMapper) Steps() int {
	state := p.current()
	if state == nil {
		return 0
	}
	return state.steps
}

// SetDepthScale sets the parallax depth scale.
func (p *ParallaxOcclusionMapper) SetDepthScale(s float32) error {
	if !finite32(s) || s <= 0 {
		return ErrBadScale
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	state := p.current()
	if state == nil || s > state.cfg.MaxDepthScale {
		return ErrBadScale
	}
	next := *state
	next.depthScale = s
	p.state.Store(&next)
	return nil
}

// DepthScale reports the current parallax depth scale.
func (p *ParallaxOcclusionMapper) DepthScale() float32 {
	state := p.current()
	if state == nil {
		return 0
	}
	return state.depthScale
}

// SetFadeRange sets the distance window over which parallax fades out.
func (p *ParallaxOcclusionMapper) SetFadeRange(near, far float32) error {
	if !finite32(near) || near < 0 || !finite32(far) || far <= near {
		return ErrBadFade
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	state := p.current()
	if state == nil {
		return ErrBadFade
	}
	next := *state
	next.fadeNear = near
	next.fadeFar = far
	p.state.Store(&next)
	return nil
}

// HeightAt samples the height atlas at one material space uv.
func (p *ParallaxOcclusionMapper) HeightAt(m MaterialRef, uv Vec2) (float32, error) {
	state := p.current()
	if state == nil {
		return 0, ErrBadConfig
	}
	tex, cell, err := lookupMaterial(state, m)
	if err != nil {
		return 0, err
	}
	return sampleCellHeight(tex, cell, uv), nil
}

func lookupMaterial(state *mapperState, m MaterialRef) (HeightMap, int, error) {
	tex, ok := state.materials[m.ID]
	if !ok {
		return HeightMap{}, 0, ErrUnknownMaterial
	}
	cellCount := int64(tex.Cols) * int64(tex.Rows)
	if m.Cell < 0 || int64(m.Cell) >= cellCount {
		return HeightMap{}, 0, ErrBadMaterial
	}
	return tex, m.Cell, nil
}

// cellPixelBounds returns the half-open source-texel range belonging to a
// material cell. Sampling is restricted to this range so bilinear taps can
// never read a neighbouring atlas cell.
func cellPixelBounds(tex HeightMap, cell int) (x0, x1, y0, y1 int) {
	col := cell % tex.Cols
	row := cell / tex.Cols
	x0 = int(int64(col) * int64(tex.Width) / int64(tex.Cols))
	x1 = int(int64(col+1) * int64(tex.Width) / int64(tex.Cols))
	y0 = int(int64(row) * int64(tex.Height) / int64(tex.Rows))
	y1 = int(int64(row+1) * int64(tex.Height) / int64(tex.Rows))
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	return x0, x1, y0, y1
}

func sampleCellHeight(tex HeightMap, cell int, uv Vec2) float32 {
	x0, x1, y0, y1 := cellPixelBounds(tex, cell)
	u, _ := clampUV(uv.U)
	v, _ := clampUV(uv.V)

	x := float64(x0) + float64(u)*float64(x1-x0) - 0.5
	y := float64(y0) + float64(v)*float64(y1-y0) - 0.5
	if x < float64(x0) {
		x = float64(x0)
	} else if x > float64(x1-1) {
		x = float64(x1 - 1)
	}
	if y < float64(y0) {
		y = float64(y0)
	} else if y > float64(y1-1) {
		y = float64(y1 - 1)
	}

	ix := int(math.Floor(x))
	iy := int(math.Floor(y))
	fx := x - float64(ix)
	fy := y - float64(iy)
	ix1 := ix + 1
	iy1 := iy + 1
	if ix1 >= x1 {
		ix1 = ix
		fx = 0
	}
	if iy1 >= y1 {
		iy1 = iy
		fy = 0
	}

	h00 := float64(tex.Values[iy*tex.Width+ix])
	h10 := float64(tex.Values[iy*tex.Width+ix1])
	h01 := float64(tex.Values[iy1*tex.Width+ix])
	h11 := float64(tex.Values[iy1*tex.Width+ix1])
	top := h00 + (h10-h00)*fx
	bottom := h01 + (h11-h01)*fx
	return float32(top + (bottom-top)*fy)
}

func clampUV(v float32) (float32, bool) {
	if math.IsNaN(float64(v)) {
		return 0, true
	}
	if v < 0 {
		return 0, true
	}
	if v > 1 {
		return 1, true
	}
	return v, false
}

// Sample resolves the displaced uv, the height and the step budget of one
// pixel.
func (p *ParallaxOcclusionMapper) Sample(m MaterialRef, uv Vec2, viewTS Vec3) (SampleResult, error) {
	state := p.current()
	if state == nil {
		return SampleResult{}, ErrBadConfig
	}
	if !validView(viewTS) {
		return SampleResult{}, ErrBadView
	}
	tex, cell, err := lookupMaterial(state, m)
	if err != nil {
		return SampleResult{}, err
	}

	baseU, uClipped := clampUV(uv.U)
	baseV, vClipped := clampUV(uv.V)
	clipped := uClipped || vClipped
	height := sampleCellHeight(tex, cell, Vec2{U: baseU, V: baseV})
	fade := fadeAt(state, m.Distance)
	steps := adaptiveSteps(state.steps, fade)

	// A view nearly parallel to the surface has no stable projection to step
	// along. Return the base sample instead of letting the division produce an
	// ever-growing offset.
	if math.Abs(float64(viewTS.Z)) < float64(state.parallelZ) {
		res := SampleResult{
			UV:      Vec2{U: baseU, V: baseV},
			Height:  height,
			Fade:    fade,
			Steps:   1,
			Clipped: clipped,
		}
		p.account(res)
		return res, nil
	}

	weight := float64(state.depthScale) * float64(fade) * float64(height)
	offsetU := float64(viewTS.X) / float64(viewTS.Z) * weight
	offsetV := float64(viewTS.Y) / float64(viewTS.Z) * weight
	rawU := float64(baseU) - offsetU
	rawV := float64(baseV) - offsetV

	resU, clippedU := clampUnitFloat64(rawU)
	resV, clippedV := clampUnitFloat64(rawV)
	clipped = clipped || clippedU || clippedV
	res := SampleResult{
		UV:      Vec2{U: resU, V: resV},
		Height:  height,
		Fade:    fade,
		Steps:   steps,
		Clipped: clipped,
	}
	p.account(res)
	return res, nil
}

func validView(v Vec3) bool {
	if !finite32(v.X) || !finite32(v.Y) || !finite32(v.Z) {
		return false
	}
	x, y, z := float64(v.X), float64(v.Y), float64(v.Z)
	return x*x+y*y+z*z > 0
}

func fadeAt(state *mapperState, distance float32) float32 {
	if math.IsNaN(float64(distance)) {
		return 0
	}
	if distance <= state.fadeNear {
		return 1
	}
	if distance >= state.fadeFar {
		return 0
	}
	return (state.fadeFar - distance) / (state.fadeFar - state.fadeNear)
}

func adaptiveSteps(fullSteps int, fade float32) int {
	steps := int(math.Round(float64(fullSteps) * float64(fade)))
	if steps < 1 {
		return 1
	}
	if steps > fullSteps {
		return fullSteps
	}
	return steps
}

func clampUnitFloat64(v float64) (float32, bool) {
	if math.IsNaN(v) {
		return 0, true
	}
	if v < 0 {
		return 0, true
	}
	if v > 1 {
		return 1, true
	}
	return float32(v), false
}

func (p *ParallaxOcclusionMapper) account(res SampleResult) {
	p.counters.samples.Add(1)
	p.counters.stepTotal.Add(int64(res.Steps))
	if res.Clipped {
		p.counters.clipped.Add(1)
	}
}

// BuildVariant compiles one LOD variant and merges concurrent builds.
func (p *ParallaxOcclusionMapper) BuildVariant(lod int) (POMVariant, error) {
	state := p.current()
	if state == nil {
		return POMVariant{}, ErrBadConfig
	}
	if lod < 0 || lod > state.cfg.MaxLOD {
		return POMVariant{}, ErrBadLOD
	}
	if len(state.materials) == 0 {
		return POMVariant{}, ErrBadMaterial
	}

	p.buildMu.Lock()
	state = p.current()
	if state == nil || lod < 0 || lod > state.cfg.MaxLOD {
		p.buildMu.Unlock()
		return POMVariant{}, ErrBadLOD
	}
	if len(state.materials) == 0 {
		p.buildMu.Unlock()
		return POMVariant{}, ErrBadMaterial
	}
	if cached, ok := state.variants[lod]; ok && variantCurrent(cached, state, lod) {
		p.buildMu.Unlock()
		p.counters.merged.Add(1)
		return cached, nil
	}
	if call, ok := p.inflight[lod]; ok {
		p.counters.merged.Add(1)
		p.buildMu.Unlock()
		<-call.done
		return call.variant, call.err
	}
	if _, exists := state.variants[lod]; !exists && len(state.variants) >= state.cfg.MaxVariants {
		p.buildMu.Unlock()
		return POMVariant{}, ErrBadLOD
	}
	call := &variantBuild{done: make(chan struct{})}
	p.inflight[lod] = call
	p.buildMu.Unlock()

	p.counters.builds.Add(1)
	variant, err := p.publishVariant(lod)

	p.buildMu.Lock()
	call.variant = variant
	call.err = err
	delete(p.inflight, lod)
	close(call.done)
	p.buildMu.Unlock()
	return variant, err
}

func variantCurrent(variant POMVariant, state *mapperState, lod int) bool {
	want := compileVariant(state, lod)
	return variant.Steps == want.Steps && variant.DepthScale == want.DepthScale
}

func compileVariant(state *mapperState, lod int) POMVariant {
	scale := float64(state.depthScale)
	if lod > 0 {
		scale = math.Ldexp(scale, -lod)
	}
	if scale <= 0 || math.IsInf(scale, 0) || math.IsNaN(scale) {
		scale = float64(math.SmallestNonzeroFloat32)
	}
	return POMVariant{
		LOD:        lod,
		Steps:      state.steps,
		DepthScale: float32(scale),
	}
}

func (p *ParallaxOcclusionMapper) publishVariant(lod int) (POMVariant, error) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	state := p.current()
	if state == nil {
		return POMVariant{}, ErrBadConfig
	}
	if len(state.materials) == 0 {
		return POMVariant{}, ErrBadMaterial
	}

	if _, exists := state.variants[lod]; !exists && len(state.variants) >= state.cfg.MaxVariants {
		return POMVariant{}, ErrBadLOD
	}
	variant := compileVariant(state, lod)
	next := *state
	next.variants = cloneVariants(state.variants)
	next.variants[lod] = variant
	p.state.Store(&next)
	return variant, nil
}

// Variant reads a compiled LOD variant.
func (p *ParallaxOcclusionMapper) Variant(lod int) (POMVariant, bool) {
	state := p.current()
	if state == nil || lod < 0 || lod > state.cfg.MaxLOD {
		return POMVariant{}, false
	}
	variant, ok := state.variants[lod]
	return variant, ok
}

// Stats returns the live accounting.
func (p *ParallaxOcclusionMapper) Stats() POMStats {
	state := p.current()
	stats := POMStats{
		Samples:   int(p.counters.samples.Load()),
		StepTotal: int(p.counters.stepTotal.Load()),
		Merged:    int(p.counters.merged.Load()),
		Clipped:   int(p.counters.clipped.Load()),
		Builds:    int(p.counters.builds.Load()),
	}
	if state != nil {
		stats.Materials = len(state.materials)
		stats.Variants = len(state.variants)
	}
	return stats
}

const pomSnapshotVersion = 1

type pomSnapshot struct {
	Version    int          `json:"version"`
	Materials  []HeightMap  `json:"materials"`
	Steps      int          `json:"steps"`
	MaxSteps   int          `json:"max_steps"`
	DepthScale float32      `json:"depth_scale"`
	FadeNear   float32      `json:"fade_near"`
	FadeFar    float32      `json:"fade_far"`
	ParallelZ  float32      `json:"parallel_z"`
	Variants   []POMVariant `json:"variants"`
}

// Snapshot serialises the atlas, the sampling parameters and the variant
// cache.
func (p *ParallaxOcclusionMapper) Snapshot() ([]byte, error) {
	state := p.current()
	if state == nil {
		return nil, ErrBadConfig
	}

	ids := make([]string, 0, len(state.materials))
	for id := range state.materials {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	materials := make([]HeightMap, 0, len(ids))
	for _, id := range ids {
		materials = append(materials, state.materials[id])
	}

	lods := make([]int, 0, len(state.variants))
	for lod := range state.variants {
		lods = append(lods, lod)
	}
	sort.Ints(lods)
	variants := make([]POMVariant, 0, len(lods))
	for _, lod := range lods {
		variants = append(variants, state.variants[lod])
	}

	return json.Marshal(pomSnapshot{
		Version:    pomSnapshotVersion,
		Materials:  materials,
		Steps:      state.steps,
		MaxSteps:   state.maxSteps,
		DepthScale: state.depthScale,
		FadeNear:   state.fadeNear,
		FadeFar:    state.fadeFar,
		ParallelZ:  state.parallelZ,
		Variants:   variants,
	})
}

// Restore replaces the whole mapper state with a snapshot, all or nothing.
func (p *ParallaxOcclusionMapper) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}
	state := p.current()
	if state == nil {
		return ErrBadSnapshot
	}
	if !validSnapshotFields(data) {
		return ErrBadSnapshot
	}

	var snap pomSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != pomSnapshotVersion || snap.MaxSteps <= 0 || snap.Steps < 1 || snap.Steps > snap.MaxSteps {
		return ErrBadSnapshot
	}
	if !finite32(snap.DepthScale) || snap.DepthScale <= 0 || snap.DepthScale > state.cfg.MaxDepthScale {
		return ErrBadSnapshot
	}
	if !finite32(snap.FadeNear) || snap.FadeNear < 0 || !finite32(snap.FadeFar) || snap.FadeFar <= snap.FadeNear {
		return ErrBadSnapshot
	}
	if !finite32(snap.ParallelZ) || snap.ParallelZ <= 0 {
		return ErrBadSnapshot
	}

	materials := make(map[string]HeightMap, len(snap.Materials))
	for _, tex := range snap.Materials {
		if strings.TrimSpace(tex.ID) == "" {
			return ErrBadSnapshot
		}
		if _, exists := materials[tex.ID]; exists {
			return ErrBadSnapshot
		}
		if err := validateHeightMap(state.cfg, tex); err != nil {
			return ErrBadSnapshot
		}
		tex.Values = append([]float32(nil), tex.Values...)
		materials[tex.ID] = tex
	}
	if len(snap.Variants) > state.cfg.MaxVariants {
		return ErrBadSnapshot
	}
	if len(snap.Variants) > 0 && len(materials) == 0 {
		return ErrBadSnapshot
	}
	variants := make(map[int]POMVariant, len(snap.Variants))
	for _, variant := range snap.Variants {
		if variant.LOD < 0 || variant.LOD > state.cfg.MaxLOD || variant.Steps < 1 {
			return ErrBadSnapshot
		}
		if !finite32(variant.DepthScale) || variant.DepthScale <= 0 || variant.DepthScale > state.cfg.MaxDepthScale {
			return ErrBadSnapshot
		}
		if _, exists := variants[variant.LOD]; exists {
			return ErrBadSnapshot
		}
		variants[variant.LOD] = variant
	}

	next := &mapperState{
		cfg:        state.cfg,
		steps:      snap.Steps,
		maxSteps:   snap.MaxSteps,
		depthScale: snap.DepthScale,
		fadeNear:   snap.FadeNear,
		fadeFar:    snap.FadeFar,
		parallelZ:  snap.ParallelZ,
		materials:  materials,
		variants:   variants,
	}

	p.stateMu.Lock()
	p.state.Store(next)
	p.counters.samples.Store(0)
	p.counters.stepTotal.Store(0)
	p.counters.merged.Store(0)
	p.counters.clipped.Store(0)
	p.counters.builds.Store(0)
	p.stateMu.Unlock()
	return nil
}

func validSnapshotFields(data []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return false
	}
	required := [...]string{
		"version",
		"materials",
		"steps",
		"max_steps",
		"depth_scale",
		"fade_near",
		"fade_far",
		"parallel_z",
		"variants",
	}
	for _, name := range required {
		raw, ok := fields[name]
		if !ok || strings.TrimSpace(string(raw)) == "null" {
			return false
		}
	}
	for name := range fields {
		known := false
		for _, requiredName := range required {
			if name == requiredName {
				known = true
				break
			}
		}
		if !known {
			return false
		}
	}
	return true
}
