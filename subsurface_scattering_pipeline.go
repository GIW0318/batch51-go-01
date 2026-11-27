// Package task runs the subsurface scattering pass: diffusion profiles are
// grouped per material, thickness drives the kernel and depth gates the taps.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// QualityLevels is how many quality steps the pass supports.
const QualityLevels = 4

// MaxDepthDelta is the depth difference above which a tap is not sampled.
const MaxDepthDelta = 0.01

// KernelRadii is the box kernel radius used by each quality level.
var KernelRadii = [QualityLevels]int{4, 3, 2, 1}

// RGB is one linear colour sample.
type RGB struct {
	R float32 `json:"r"`
	G float32 `json:"g"`
	B float32 `json:"b"`
}

// DiffusionProfile describes how far light travels inside one material.
type DiffusionProfile struct {
	MaterialID uint16  `json:"material_id"`
	Name       string  `json:"name"`
	Radius     float32 `json:"radius"`
	Falloff    float32 `json:"falloff"`
	Area       float32 `json:"area"`
}

// MaterialSlot is the material coverage of one pixel; the secondary material
// takes the rest of the weight.
type MaterialSlot struct {
	Primary       uint16  `json:"primary"`
	Secondary     uint16  `json:"secondary"`
	PrimaryWeight float32 `json:"primary_weight"`
}

// MaterialIDMap is the per pixel material coverage of the frame.
type MaterialIDMap struct {
	W     int            `json:"w"`
	H     int            `json:"h"`
	Slots []MaterialSlot `json:"slots"`
}

// ThicknessMap is the per pixel scattering thickness of the frame.
type ThicknessMap struct {
	W         int       `json:"w"`
	H         int       `json:"h"`
	Thickness []float32 `json:"thickness"`
}

// DepthTex is the depth buffer handed to the blur.
type DepthTex struct {
	W     int       `json:"w"`
	H     int       `json:"h"`
	Depth []float32 `json:"depth"`
}

// Image is one colour buffer of the pass.
type Image struct {
	W      int   `json:"w"`
	H      int   `json:"h"`
	Pixels []RGB `json:"pixels"`
}

// SssLimits bounds one pipeline. Zero values mean "use the default".
type SssLimits struct {
	MaxProfiles     int `json:"max_profiles"`
	MaxGroups       int `json:"max_groups"`
	MaxKernelBudget int `json:"max_kernel_budget"`
	MaxTempBytes    int `json:"max_temp_bytes"`
}

// SSSStats is the per pipeline counter block.
type SSSStats struct {
	Frames       int `json:"frames"`
	Profiles     int `json:"profiles"`
	Groups       int `json:"groups"`
	Quality      int `json:"quality"`
	KernelRadius int `json:"kernel_radius"`
	Samples      int `json:"samples"`
	Degrades     int `json:"degrades"`
	TempBytes    int `json:"temp_bytes"`
}

// Errors returned by the pipeline.
var (
	ErrBadProfile      = errors.New("diffusion profile is not usable")
	ErrProfileExists   = errors.New("diffusion profile id or material is already registered")
	ErrProfileLimit    = errors.New("diffusion profile limit reached")
	ErrNoMaterial      = errors.New("material id zero means no material")
	ErrUnknownMaterial = errors.New("material id has no diffusion profile")
	ErrBadMaterialMap  = errors.New("material id map is not usable")
	ErrBadThickness    = errors.New("thickness map is not usable")
	ErrBadQuality      = errors.New("quality level is outside the supported range")
	ErrBadBudget       = errors.New("kernel budget is not usable")
	ErrNoProfile       = errors.New("no diffusion profile is registered")
	ErrNoMaterialMap   = errors.New("no material id map was set")
	ErrNoThicknessMap  = errors.New("no thickness map was set")
	ErrBadImage        = errors.New("image, material or depth buffer sizes disagree")
	ErrFrameBudget     = errors.New("kernel budget cannot be met even at the lowest quality")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// SubsurfaceScatteringPipeline owns the profile registry, the per frame maps
// and the quality and budget state.
type SubsurfaceScatteringPipeline struct {
	limits SssLimits
}

// NewSubsurfaceScatteringPipeline builds a pipeline with the given caps.
func NewSubsurfaceScatteringPipeline(limits SssLimits) *SubsurfaceScatteringPipeline {
	return &SubsurfaceScatteringPipeline{limits: limits}
}

// RegisterProfile adds one diffusion profile under the given identifier.
func (p *SubsurfaceScatteringPipeline) RegisterProfile(id string, profile DiffusionProfile) error {
	return errNotImplemented
}

// SampleProfile looks a diffusion profile up by material id.
func (p *SubsurfaceScatteringPipeline) SampleProfile(materialID uint16) (DiffusionProfile, error) {
	return DiffusionProfile{}, errNotImplemented
}

// ProfileCount reports how many profiles are registered.
func (p *SubsurfaceScatteringPipeline) ProfileCount() int {
	return 0
}

// SetMaterialMap installs the per pixel material coverage of the frame.
func (p *SubsurfaceScatteringPipeline) SetMaterialMap(tex MaterialIDMap) error {
	return errNotImplemented
}

// SetThicknessMap installs the per pixel thickness of the frame.
func (p *SubsurfaceScatteringPipeline) SetThicknessMap(tex ThicknessMap) error {
	return errNotImplemented
}

// SetQuality selects the quality level of the next blur.
func (p *SubsurfaceScatteringPipeline) SetQuality(level int) error {
	return errNotImplemented
}

// SetKernelBudget replaces the per frame sample budget.
func (p *SubsurfaceScatteringPipeline) SetKernelBudget(n int) error {
	return errNotImplemented
}

// Blur runs the depth aware diffusion filter over one frame.
func (p *SubsurfaceScatteringPipeline) Blur(src Image, depth DepthTex) (Image, error) {
	return Image{}, errNotImplemented
}

// Stats reports the counters of the pipeline.
func (p *SubsurfaceScatteringPipeline) Stats() SSSStats {
	return SSSStats{}
}

// Snapshot serialises the profiles, the maps, the quality and the budget.
func (p *SubsurfaceScatteringPipeline) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *SubsurfaceScatteringPipeline) Restore(data []byte) error {
	return errNotImplemented
}
