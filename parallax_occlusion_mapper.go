// Package task plans parallax occlusion sampling: it keeps the height atlas,
// the step, depth and fade parameters and the per LOD variants used by the
// material shaders.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

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

// ParallaxOcclusionMapper owns the height atlas, the sampling parameters and
// the variant cache.
type ParallaxOcclusionMapper struct {
}

// NewParallaxOcclusionMapper validates the configuration and creates an empty
// mapper.
func NewParallaxOcclusionMapper(cfg POMConfig) (*ParallaxOcclusionMapper, error) {
	return nil, errNotImplemented
}

// SetHeightMap installs or replaces the height atlas of one material family.
func (p *ParallaxOcclusionMapper) SetHeightMap(id string, tex HeightMap) error {
	return errNotImplemented
}

// SetSteps sets the step count used at full parallax weight.
func (p *ParallaxOcclusionMapper) SetSteps(n int) error {
	return errNotImplemented
}

// SetMaxSteps lowers or raises the hard step ceiling.
func (p *ParallaxOcclusionMapper) SetMaxSteps(n int) error {
	return errNotImplemented
}

// Steps reports the effective full weight step count.
func (p *ParallaxOcclusionMapper) Steps() int {
	return 0
}

// SetDepthScale sets the parallax depth scale.
func (p *ParallaxOcclusionMapper) SetDepthScale(s float32) error {
	return errNotImplemented
}

// DepthScale reports the current parallax depth scale.
func (p *ParallaxOcclusionMapper) DepthScale() float32 {
	return 0
}

// SetFadeRange sets the distance window over which parallax fades out.
func (p *ParallaxOcclusionMapper) SetFadeRange(near, far float32) error {
	return errNotImplemented
}

// HeightAt samples the height atlas at one material space uv.
func (p *ParallaxOcclusionMapper) HeightAt(m MaterialRef, uv Vec2) (float32, error) {
	return 0, errNotImplemented
}

// Sample resolves the displaced uv, the height and the step budget of one
// pixel.
func (p *ParallaxOcclusionMapper) Sample(m MaterialRef, uv Vec2, viewTS Vec3) (SampleResult, error) {
	return SampleResult{}, errNotImplemented
}

// BuildVariant compiles one LOD variant and merges concurrent builds.
func (p *ParallaxOcclusionMapper) BuildVariant(lod int) (POMVariant, error) {
	return POMVariant{}, errNotImplemented
}

// Variant reads a compiled LOD variant.
func (p *ParallaxOcclusionMapper) Variant(lod int) (POMVariant, bool) {
	return POMVariant{}, false
}

// Stats returns the live accounting.
func (p *ParallaxOcclusionMapper) Stats() POMStats {
	return POMStats{}
}

// Snapshot serialises the atlas, the sampling parameters and the variant
// cache.
func (p *ParallaxOcclusionMapper) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole mapper state with a snapshot, all or nothing.
func (p *ParallaxOcclusionMapper) Restore(data []byte) error {
	return errNotImplemented
}
