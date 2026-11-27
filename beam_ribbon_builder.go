// Package task expands a beam centre line into a camera facing ribbon: it
// splits the path where the tangent turns too hard, samples the width curve,
// keeps a pixel floor on the width and culls whole ribbons against a view.
package task

import "errors"

// Errors returned by the builder.
var (
	ErrBadLimits       = errors.New("ribbon limits are invalid")
	ErrBadCamera       = errors.New("camera basis is degenerate")
	ErrBadBeam         = errors.New("beam id is empty")
	ErrBadPriority     = errors.New("beam priority must not be negative")
	ErrBadCurve        = errors.New("width curve is invalid")
	ErrUnknownBeam     = errors.New("beam is not registered")
	ErrShortPath       = errors.New("path needs at least two points")
	ErrDegeneratePath  = errors.New("path has a zero length segment")
	ErrTooManySegments = errors.New("path needs more segments than allowed")
	ErrVertexLimit     = errors.New("ribbon exceeds the vertex budget")
	ErrBadBudget       = errors.New("vertex budget must be positive")
	ErrNoActiveBeam    = errors.New("no active beam is selected")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// DefaultBeam is the beam used before SetActiveBeam is called.
const DefaultBeam = "default"

// Vec3 is a point in world space.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Camera is the view the ribbon is expanded against.
type Camera struct {
	Pos     Vec3    `json:"pos"`
	Forward Vec3    `json:"forward"`
	Up      Vec3    `json:"up"`
	FovY    float64 `json:"fov_y"`
}

// Key is one sample of a piecewise linear width curve.
type Key struct {
	T     float64 `json:"t"`
	Width float64 `json:"width"`
}

// Plane bounds a view; a point is inside when dot(Normal, p) plus Offset is
// not positive.
type Plane struct {
	Normal Vec3    `json:"normal"`
	Offset float64 `json:"offset"`
}

// Frustum is the set of planes the culler tests against.
type Frustum struct {
	Planes []Plane `json:"planes"`
}

// RibbonLimits fixes the segment, vertex and pixel budgets.
type RibbonLimits struct {
	MaxSegments       int     `json:"max_segments"`
	MaxVertices       int     `json:"max_vertices"`
	MaxBeams          int     `json:"max_beams"`
	MinPixelWidth     float64 `json:"min_pixel_width"`
	PixelsPerUnit     float64 `json:"pixels_per_unit"`
	DefaultWidth      float64 `json:"default_width"`
	BreakAngleDeg     float64 `json:"break_angle_deg"`
	ParallelThreshold float64 `json:"parallel_threshold"`
}

// BeamID names one ribbon.
type BeamID string

// Ribbon is the expanded strip of one beam.
type Ribbon struct {
	BeamID   BeamID    `json:"beam_id"`
	Segments int       `json:"segments"`
	Widths   []float64 `json:"widths"`
	Vertices []Vec3    `json:"vertices"`
	Degraded bool      `json:"degraded"`
	LineEnds []Vec3    `json:"line_ends"`
}

// RibbonStats counts the work done since the builder was created.
type RibbonStats struct {
	Builds   int `json:"builds"`
	Degraded int `json:"degraded"`
	Culled   int `json:"culled"`
	Released int `json:"released"`
}

// BeamRibbon owns the width curves, the registered ribbons and the budgets.
type BeamRibbon struct {
	limits RibbonLimits
}

// NewBeamRibbon builds a ribbon builder with the given limits.
func NewBeamRibbon(limits RibbonLimits) *BeamRibbon {
	return &BeamRibbon{limits: limits}
}

// SetActiveBeam selects the beam the next Build call expands.
func (b *BeamRibbon) SetActiveBeam(beamID string, priority int) error {
	return errors.New("not implemented")
}

// Build expands one centre line against the camera into a ribbon.
func (b *BeamRibbon) Build(path []Vec3, camera Camera) (Ribbon, error) {
	return Ribbon{}, errors.New("not implemented")
}

// SetWidthCurve installs the width keys of one beam.
func (b *BeamRibbon) SetWidthCurve(beamID string, keys []Key) error {
	return errors.New("not implemented")
}

// WidthAt samples the width curve of one beam, clamping t to zero and one.
func (b *BeamRibbon) WidthAt(beamID string, t float64) (float64, error) {
	return 0, errors.New("not implemented")
}

// Cull returns the registered beams that survive the view, ascending.
func (b *BeamRibbon) Cull(view Frustum) []BeamID {
	return nil
}

// SetBudget sets the global vertex budget.
func (b *BeamRibbon) SetBudget(vertices int) error {
	return errors.New("not implemented")
}

// Budget reports the vertices used by the last build and the total budget.
func (b *BeamRibbon) Budget() (int, int) {
	return 0, 0
}

// Stats reports the counters of the builder.
func (b *BeamRibbon) Stats() RibbonStats {
	return RibbonStats{}
}

// Release unregisters one beam.
func (b *BeamRibbon) Release(beamID string) error {
	return errors.New("not implemented")
}

// Snapshot serialises curves, ribbons, budgets and counters.
func (b *BeamRibbon) Snapshot() ([]byte, error) {
	return nil, errors.New("not implemented")
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (b *BeamRibbon) Restore(data []byte) error {
	return errors.New("not implemented")
}
