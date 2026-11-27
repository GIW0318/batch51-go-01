// Package task plans screen space lens flares for a set of scene light
// sources, using asynchronously reported occlusion results and per frame fade.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Defaults applied by NewLensFlareScheduler for limits left at zero.
const (
	DefaultFadeIn       float32 = 0.25
	DefaultFadeOut      float32 = 0.2
	DefaultMaxSources           = 256
	DefaultMaxInstances         = 32
	DefaultMaxQueries           = 8
)

// Vec3 is a point or direction in world space.
type Vec3 struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// Camera is the view the flares are planned against.
type Camera struct {
	Position  Vec3    `json:"position"`
	Forward   Vec3    `json:"forward"`
	Up        Vec3    `json:"up"`
	FovDeg    float32 `json:"fov_deg"`
	ViewportW int     `json:"viewport_w"`
	ViewportH int     `json:"viewport_h"`
}

// FlareSource is one scene light that may produce a flare.
type FlareSource struct {
	Position   Vec3    `json:"position"`
	Brightness float32 `json:"brightness"`
	Radius     float32 `json:"radius"`
	Group      string  `json:"group"`
}

// FlareInstance is one flare the renderer should draw this frame.
type FlareInstance struct {
	ID        string  `json:"id"`
	NdcX      float32 `json:"ndc_x"`
	NdcY      float32 `json:"ndc_y"`
	Intensity float32 `json:"intensity"`
	Scale     float32 `json:"scale"`
	Order     int     `json:"order"`
}

// FlareLimits bounds one scheduler. Zero values mean "use the default".
type FlareLimits struct {
	MaxSources         int `json:"max_sources"`
	MaxInstances       int `json:"max_instances"`
	MaxQueriesPerFrame int `json:"max_queries_per_frame"`
}

// FlareStats is the per scheduler counter block.
type FlareStats struct {
	Frame     int `json:"frame"`
	Sources   int `json:"sources"`
	Visible   int `json:"visible"`
	Occluded  int `json:"occluded"`
	Behind    int `json:"behind"`
	Offscreen int `json:"offscreen"`
	Deferred  int `json:"deferred"`
	Queries   int `json:"queries"`
	Instances int `json:"instances"`
}

// Errors returned by the scheduler.
var (
	ErrBadSource    = errors.New("flare source is not usable")
	ErrSourceExists = errors.New("flare source id is already registered")
	ErrSourceLimit  = errors.New("flare source limit reached")
	ErrUnknownSource = errors.New("flare source id is not registered")
	ErrBadOcclusion = errors.New("occlusion visibility must be between zero and one")
	ErrBadLimit     = errors.New("instance limit must be positive")
	ErrBadFade      = errors.New("fade rate must be inside the open unit interval")
	ErrBadCamera    = errors.New("camera is not usable")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// LensFlareScheduler owns the source table, the fade progress and the per
// frame query and instance budgets.
type LensFlareScheduler struct {
	limits FlareLimits
}

// NewLensFlareScheduler builds a scheduler with the given caps.
func NewLensFlareScheduler(limits FlareLimits) *LensFlareScheduler {
	return &LensFlareScheduler{limits: limits}
}

// RegisterSource adds one light source.
func (s *LensFlareScheduler) RegisterSource(id string, src FlareSource) error {
	return errNotImplemented
}

// UpdateSource replaces the geometry of a registered light source.
func (s *LensFlareScheduler) UpdateSource(id string, src FlareSource) error {
	return errNotImplemented
}

// SetOcclusionResult stores the visibility reported by the occlusion backend.
func (s *LensFlareScheduler) SetOcclusionResult(id string, visible float32) error {
	return errNotImplemented
}

// Plan advances one frame and returns the flares to draw.
func (s *LensFlareScheduler) Plan(cam Camera) ([]FlareInstance, error) {
	return nil, errNotImplemented
}

// SetMaxInstances replaces the per frame instance cap.
func (s *LensFlareScheduler) SetMaxInstances(n int) error {
	return errNotImplemented
}

// SetFadeRates replaces the per frame fade in and fade out steps.
func (s *LensFlareScheduler) SetFadeRates(in, out float32) error {
	return errNotImplemented
}

// Priority reports the position of a source in the most recent plan.
func (s *LensFlareScheduler) Priority(id string) int {
	return -1
}

// Intensity reports the current fade progress of a source.
func (s *LensFlareScheduler) Intensity(id string) float32 {
	return 0
}

// SourceCount reports how many sources are registered.
func (s *LensFlareScheduler) SourceCount() int {
	return 0
}

// Stats reports the counters of the last plan.
func (s *LensFlareScheduler) Stats() FlareStats {
	return FlareStats{}
}

// Snapshot serialises the source table, the fade progress and the budgets.
func (s *LensFlareScheduler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *LensFlareScheduler) Restore(data []byte) error {
	return errNotImplemented
}
