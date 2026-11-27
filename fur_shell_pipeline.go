// Package task plans fur shell layers for one frame: it maps camera distance
// and fur length onto a shell count, shares the read only density atlas across
// viewports and trims the shell stack when the frame budget runs out.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Errors reported by the shell pipeline.
var (
	ErrNotConfigured = errors.New("fur pipeline is not configured")
	ErrBadConfig     = errors.New("fur configuration is invalid")
	ErrBadGroom      = errors.New("groom payload is invalid")
	ErrUnknownGroom  = errors.New("groom is not registered")
	ErrBadView       = errors.New("view parameters are invalid")
	ErrBadDensity    = errors.New("density map is invalid")
	ErrBadPlan       = errors.New("shell layer plan is invalid")
	ErrStalePlan     = errors.New("shell layer plan belongs to an older groom generation")
	ErrBadFrame      = errors.New("frame number must strictly increase")
	ErrNoShell       = errors.New("shell index is out of range")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Vec2 is a texture coordinate pair in the unit square.
type Vec2 struct {
	U float32 `json:"u"`
	V float32 `json:"v"`
}

// FurConfig is the immutable pipeline configuration.
type FurConfig struct {
	// MaxShells caps the number of shells any single groom may plan.
	MaxShells int `json:"max_shells"`
	// ShellBudget caps the shell instances built during one frame, all
	// viewports and all grooms together.
	ShellBudget int `json:"shell_budget"`
	// SampleBudget caps the density samples taken during one frame.
	SampleBudget int `json:"sample_budget"`
	// LODDistances are ascending camera distances that close a LOD band.
	LODDistances []float32 `json:"lod_distances"`
	// LODShells holds the shell count of every band; it must be one longer
	// than LODDistances and must not grow with distance.
	LODShells []int `json:"lod_shells"`
	// HysteresisBand is the half width of the band in which the previous
	// LOD band keeps winning.
	HysteresisBand float32 `json:"hysteresis_band"`
	// FurLengthMax is the longest groom the pipeline accepts, in metres.
	FurLengthMax float32 `json:"fur_length_max"`
	// MinStep is the smallest accepted shell spacing, in metres.
	MinStep float32 `json:"min_step"`
}

// GroomData is the per groom payload pushed by the grooming tool.
type GroomData struct {
	ID        string  `json:"id"`
	FurLength float32 `json:"fur_length"`
	BaseAlpha float32 `json:"base_alpha"`
}

// DensityMap is a single channel coverage texture laid over the groom.
type DensityMap struct {
	ID     string    `json:"id"`
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Values []float32 `json:"values"`
}

// ViewParams describes one viewport request for one groom.
type ViewParams struct {
	ViewportID string  `json:"viewport_id"`
	GroomID    string  `json:"groom_id"`
	CameraDist float32 `json:"camera_dist"`
}

// LayerPlan is the read only product of PlanLayers. Render threads share the
// same plan and only build shells from it.
type LayerPlan struct {
	ViewportID string  `json:"viewport_id"`
	GroomID    string  `json:"groom_id"`
	Generation int64   `json:"generation"`
	Band       int     `json:"band"`
	FullShells int     `json:"full_shells"`
	Shells     int     `json:"shells"`
	Trimmed    int     `json:"trimmed"`
	Step       float32 `json:"step"`
}

// ShellInstance is one shell of the stack, ready to be drawn.
type ShellInstance struct {
	GroomID    string  `json:"groom_id"`
	ViewportID string  `json:"viewport_id"`
	Index      int     `json:"index"`
	Offset     float32 `json:"offset"`
	Alpha      float32 `json:"alpha"`
	Culled     bool    `json:"culled"`
}

// FurStats is the frame snapshot of the pipeline counters.
type FurStats struct {
	Grooms     int   `json:"grooms"`
	Plans      int   `json:"plans"`
	Frame      int64 `json:"frame"`
	Shells     int   `json:"shells"`
	Samples    int   `json:"samples"`
	Generation int64 `json:"generation"`
}

// FurShellPipeline owns the groom table, the density atlas, the per viewport
// LOD memory and the frame counters.
type FurShellPipeline struct {
}

// NewFurShellPipeline builds an unconfigured pipeline.
func NewFurShellPipeline() *FurShellPipeline {
	return &FurShellPipeline{}
}

// Configure installs a validated configuration.
func (p *FurShellPipeline) Configure(cfg FurConfig) error {
	return errNotImplemented
}

// BeginFrame opens a new frame and resets the per frame counters.
func (p *FurShellPipeline) BeginFrame(frame int64) error {
	return errNotImplemented
}

// SetGroomer installs or replaces one groom and advances its generation.
func (p *FurShellPipeline) SetGroomer(id string, groom GroomData) error {
	return errNotImplemented
}

// SetDensity installs the shared read only density map of one groom.
func (p *FurShellPipeline) SetDensity(id string, tex DensityMap) error {
	return errNotImplemented
}

// PlanLayers resolves the LOD band of one viewport and trims the stack against
// the remaining frame budget.
func (p *FurShellPipeline) PlanLayers(view ViewParams) (LayerPlan, error) {
	return LayerPlan{}, errNotImplemented
}

// BuildShells expands a plan into drawable shell instances.
func (p *FurShellPipeline) BuildShells(plan LayerPlan) ([]ShellInstance, error) {
	return nil, errNotImplemented
}

// ResolveAlpha samples the density atlas and returns the shaded alpha of one
// built shell.
func (p *FurShellPipeline) ResolveAlpha(shell int, uv Vec2) (float32, error) {
	return 0, errNotImplemented
}

// Generation reports the current generation of one groom.
func (p *FurShellPipeline) Generation(id string) (int64, error) {
	return 0, errNotImplemented
}

// DropGroomer removes a groom together with its density map and its plans.
func (p *FurShellPipeline) DropGroomer(id string) error {
	return errNotImplemented
}

// Stats returns the counters of the current frame.
func (p *FurShellPipeline) Stats() FurStats {
	return FurStats{}
}

// Snapshot serialises configuration, groom generations, density maps, the per
// viewport LOD memory and the frame counters.
func (p *FurShellPipeline) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole pipeline state with a snapshot, all or nothing.
func (p *FurShellPipeline) Restore(data []byte) error {
	return errNotImplemented
}
