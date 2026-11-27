// Package task expands a beam centre line into a camera facing ribbon: it
// splits the path where the tangent turns too hard, samples the width curve,
// keeps a pixel floor on the width and culls whole ribbons against a view.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
)

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

const (
	epsilon            = 1e-12
	pixelWorldScale    = 4.0
	snapshotVersion    = 1
	defaultMaxSegments = 8
	defaultMaxVertices = 256
	defaultMaxBeams    = 8
	defaultMinPixels   = 0.5
	defaultPixelsUnit  = 100
	defaultWidth       = 0.5
	defaultBreakAngle  = 45
	defaultParallel    = 0.98
)

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

type sphere struct {
	center Vec3
	radius float64
}

type beamState struct {
	priority int
	ribbon   Ribbon
	parallel bool
	lastAxis Vec3
	bounds   sphere
}

// BeamRibbon owns the width curves, the registered ribbons and the budgets.
type BeamRibbon struct {
	mu             sync.RWMutex
	limits         RibbonLimits
	curves         map[string][]Key
	beams          map[BeamID]*beamState
	active         BeamID
	activePriority int
	budget         int
	lastBuildUsed  int
	stats          RibbonStats
}

// NewBeamRibbon builds a ribbon builder with the given limits.
func NewBeamRibbon(limits RibbonLimits) *BeamRibbon {
	limits = normalizeLimits(limits)
	return &BeamRibbon{
		limits: limits,
		curves: make(map[string][]Key),
		beams:  make(map[BeamID]*beamState),
		active: BeamID(DefaultBeam),
		budget: limits.MaxVertices,
	}
}

// SetActiveBeam selects the beam the next Build call expands.
func (b *BeamRibbon) SetActiveBeam(beamID string, priority int) error {
	if strings.TrimSpace(beamID) == "" {
		return ErrBadBeam
	}
	if priority < 0 {
		return ErrBadPriority
	}
	b.mu.Lock()
	b.active = BeamID(beamID)
	b.activePriority = priority
	b.mu.Unlock()
	return nil
}

// Build expands one centre line against the camera into a ribbon.
func (b *BeamRibbon) Build(path []Vec3, camera Camera) (Ribbon, error) {
	if err := validatePath(path); err != nil {
		return Ribbon{}, err
	}
	normalizedForward, fallbackAxis, err := cameraBasis(camera)
	if err != nil {
		return Ribbon{}, err
	}

	b.mu.RLock()
	beamID := b.active
	priority := b.activePriority
	curve := cloneKeys(b.curves[string(beamID)])
	previous := b.beams[beamID]
	previousParallel := false
	var previousAxis Vec3
	if previous != nil {
		previousParallel = previous.parallel
		previousAxis = previous.lastAxis
	}
	limits := b.limits
	b.mu.RUnlock()

	breaks := findBreaks(path, limits.BreakAngleDeg)
	segments := len(breaks) + 1
	if segments > limits.MaxSegments {
		return Ribbon{}, ErrTooManySegments
	}
	vertexCount := 2*len(path) + 2*len(breaks)
	if vertexCount > limits.MaxVertices {
		return Ribbon{}, ErrVertexLimit
	}

	widths := make([]float64, len(path))
	for i, point := range path {
		t := 0.0
		if len(path) > 1 {
			t = float64(i) / float64(len(path)-1)
		}
		width := limits.DefaultWidth
		if curve != nil {
			width = sampleCurve(curve, t)
		}
		distance := length(sub(point, camera.Pos))
		minimum := minimumWorldWidth(distance, limits)
		if width < minimum {
			width = minimum
		}
		if width <= 0 || !finite(width) {
			width = limits.DefaultWidth
		}
		widths[i] = width
	}

	parallel := parallelWithHysteresis(path, normalizedForward, previousParallel, limits.ParallelThreshold)
	axes := segmentAxes(path, breaks, normalizedForward, fallbackAxis, parallel, previousAxis)
	full := buildSegments(beamID, path, widths, breaks, axes)
	degraded := buildDegraded(beamID, path)

	b.mu.Lock()
	final, err := b.commitBuildLocked(beamID, priority, full, degraded, parallel, axes[len(axes)-1])
	if err != nil {
		b.mu.Unlock()
		return Ribbon{}, err
	}
	b.stats.Builds++
	b.lastBuildUsed = len(final.Vertices)
	b.mu.Unlock()
	return final, nil
}

// SetWidthCurve installs the width keys of one beam.
func (b *BeamRibbon) SetWidthCurve(beamID string, keys []Key) error {
	if strings.TrimSpace(beamID) == "" {
		return ErrBadBeam
	}
	if err := validateCurve(keys); err != nil {
		return err
	}
	copied := cloneKeys(keys)
	b.mu.Lock()
	b.curves[beamID] = copied
	b.mu.Unlock()
	return nil
}

// WidthAt samples the width curve of one beam, clamping t to zero and one.
func (b *BeamRibbon) WidthAt(beamID string, t float64) (float64, error) {
	b.mu.RLock()
	keys, ok := b.curves[beamID]
	b.mu.RUnlock()
	if !ok || len(keys) == 0 {
		return 0, ErrUnknownBeam
	}
	if math.IsNaN(t) {
		t = 0
	}
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return sampleCurve(keys, t), nil
}

// Cull returns the registered beams that survive the view, ascending.
func (b *BeamRibbon) Cull(view Frustum) []BeamID {
	b.mu.RLock()
	states := make([]*beamState, 0, len(b.beams))
	ids := make([]BeamID, 0, len(b.beams))
	for id, state := range b.beams {
		ids = append(ids, id)
		states = append(states, state)
	}
	b.mu.RUnlock()

	visible := make([]BeamID, 0, len(ids))
	culled := 0
	for i, id := range ids {
		if !sphereInsideFrustum(states[i].bounds, view) {
			culled++
			continue
		}
		visible = append(visible, id)
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i] < visible[j] })
	if culled != 0 {
		b.mu.Lock()
		b.stats.Culled += culled
		b.mu.Unlock()
	}
	return visible
}

// SetBudget sets the global vertex budget.
func (b *BeamRibbon) SetBudget(vertices int) error {
	if vertices <= 0 {
		return ErrBadBudget
	}
	b.mu.Lock()
	b.budget = vertices
	b.rebalanceBudgetLocked()
	b.mu.Unlock()
	return nil
}

// Budget reports the vertices used by the last build and the total budget.
func (b *BeamRibbon) Budget() (int, int) {
	b.mu.RLock()
	used, budget := b.lastBuildUsed, b.budget
	b.mu.RUnlock()
	return used, budget
}

// Stats reports the counters of the builder.
func (b *BeamRibbon) Stats() RibbonStats {
	b.mu.RLock()
	stats := b.stats
	b.mu.RUnlock()
	return stats
}

// Release unregisters one beam.
func (b *BeamRibbon) Release(beamID string) error {
	b.mu.Lock()
	id := BeamID(beamID)
	if _, ok := b.beams[id]; !ok {
		b.mu.Unlock()
		return ErrUnknownBeam
	}
	delete(b.beams, id)
	delete(b.curves, beamID)
	if id == b.active {
		b.active = BeamID(DefaultBeam)
		b.activePriority = 0
	}
	b.stats.Released++
	b.mu.Unlock()
	return nil
}

type snapshotBeam struct {
	Priority int    `json:"priority"`
	Parallel bool   `json:"parallel"`
	LastAxis Vec3   `json:"last_axis"`
	Ribbon   Ribbon `json:"ribbon"`
}

type snapshotState struct {
	Version        int                     `json:"version"`
	Curves         map[string][]Key        `json:"curves"`
	Beams          map[string]snapshotBeam `json:"beams"`
	Active         string                  `json:"active_beam"`
	ActivePriority int                     `json:"active_priority"`
	Budget         int                     `json:"budget"`
	LastBuildUsed  int                     `json:"last_build_used"`
	Stats          RibbonStats             `json:"stats"`
}

// Snapshot serialises curves, ribbons, budgets and counters.
func (b *BeamRibbon) Snapshot() ([]byte, error) {
	b.mu.RLock()
	state := snapshotState{
		Version:        snapshotVersion,
		Curves:         make(map[string][]Key, len(b.curves)),
		Beams:          make(map[string]snapshotBeam, len(b.beams)),
		Active:         string(b.active),
		ActivePriority: b.activePriority,
		Budget:         b.budget,
		LastBuildUsed:  b.lastBuildUsed,
		Stats:          b.stats,
	}
	for id, keys := range b.curves {
		state.Curves[id] = cloneKeys(keys)
	}
	for id, beam := range b.beams {
		state.Beams[string(id)] = snapshotBeam{
			Priority: beam.priority,
			Parallel: beam.parallel,
			LastAxis: beam.lastAxis,
			Ribbon:   cloneRibbon(beam.ribbon),
		}
	}
	b.mu.RUnlock()

	data, err := json.Marshal(state)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (b *BeamRibbon) Restore(data []byte) error {
	var state snapshotState
	if len(data) == 0 || json.Unmarshal(data, &state) != nil {
		return ErrBadSnapshot
	}
	if state.Version != snapshotVersion || state.Budget <= 0 || state.Curves == nil || state.Beams == nil || strings.TrimSpace(state.Active) == "" {
		return ErrBadSnapshot
	}
	if state.Stats.Builds < 0 || state.Stats.Degraded < 0 || state.Stats.Culled < 0 || state.Stats.Released < 0 {
		return ErrBadSnapshot
	}
	if state.ActivePriority < 0 || state.LastBuildUsed < 0 {
		return ErrBadSnapshot
	}

	curves := make(map[string][]Key, len(state.Curves))
	for id, keys := range state.Curves {
		if strings.TrimSpace(id) == "" || validateCurve(keys) != nil {
			return ErrBadSnapshot
		}
		curves[id] = cloneKeys(keys)
	}
	beams := make(map[BeamID]*beamState, len(state.Beams))
	for id, item := range state.Beams {
		if strings.TrimSpace(id) == "" || item.Priority < 0 || item.Ribbon.BeamID != BeamID(id) {
			return ErrBadSnapshot
		}
		if !validRibbonState(item.Ribbon) || !finiteVec(item.LastAxis) {
			return ErrBadSnapshot
		}
		beams[BeamID(id)] = &beamState{
			priority: item.Priority,
			ribbon:   cloneRibbon(item.Ribbon),
			parallel: item.Parallel,
			lastAxis: item.LastAxis,
			bounds:   boundsForRibbon(item.Ribbon),
		}
	}

	b.mu.Lock()
	b.curves = curves
	b.beams = beams
	b.active = BeamID(state.Active)
	b.activePriority = state.ActivePriority
	b.budget = state.Budget
	b.lastBuildUsed = state.LastBuildUsed
	b.stats = state.Stats
	b.mu.Unlock()
	return nil
}

func normalizeLimits(limits RibbonLimits) RibbonLimits {
	if limits.MaxSegments <= 0 {
		limits.MaxSegments = defaultMaxSegments
	}
	if limits.MaxVertices <= 0 {
		limits.MaxVertices = defaultMaxVertices
	}
	if limits.MaxBeams <= 0 {
		limits.MaxBeams = defaultMaxBeams
	}
	if limits.MinPixelWidth <= 0 {
		limits.MinPixelWidth = defaultMinPixels
	}
	if limits.PixelsPerUnit <= 0 {
		limits.PixelsPerUnit = defaultPixelsUnit
	}
	if limits.DefaultWidth <= 0 {
		limits.DefaultWidth = defaultWidth
	}
	if limits.BreakAngleDeg <= 0 || limits.BreakAngleDeg > 180 {
		limits.BreakAngleDeg = defaultBreakAngle
	}
	if limits.ParallelThreshold <= 0 || limits.ParallelThreshold > 1 {
		limits.ParallelThreshold = defaultParallel
	}
	return limits
}

func validateCurve(keys []Key) error {
	if len(keys) == 0 {
		return ErrBadCurve
	}
	previousT := -1.0
	for _, key := range keys {
		if !finite(key.T) || !finite(key.Width) || key.T < 0 || key.T > 1 || key.Width <= 0 {
			return ErrBadCurve
		}
		if key.T <= previousT {
			return ErrBadCurve
		}
		previousT = key.T
	}
	return nil
}

func sampleCurve(keys []Key, t float64) float64 {
	if t <= keys[0].T {
		return keys[0].Width
	}
	last := len(keys) - 1
	if t >= keys[last].T {
		return keys[last].Width
	}
	lo, hi := 0, last
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if keys[mid].T <= t {
			lo = mid
		} else {
			hi = mid
		}
	}
	a, b := keys[lo], keys[hi]
	fraction := (t - a.T) / (b.T - a.T)
	return a.Width + (b.Width-a.Width)*fraction
}

func validatePath(path []Vec3) error {
	if len(path) < 2 {
		return ErrShortPath
	}
	for _, point := range path {
		if !finiteVec(point) {
			return ErrDegeneratePath
		}
	}
	for i := 1; i < len(path); i++ {
		d := sub(path[i], path[i-1])
		if dot(d, d) <= epsilon*epsilon {
			return ErrDegeneratePath
		}
	}
	return nil
}

func cameraBasis(camera Camera) (Vec3, Vec3, error) {
	if !finiteVec(camera.Pos) || !finiteVec(camera.Forward) || !finiteVec(camera.Up) || !finite(camera.FovY) {
		return Vec3{}, Vec3{}, ErrBadCamera
	}
	lengthForward := length(camera.Forward)
	if lengthForward <= epsilon {
		return Vec3{}, Vec3{}, ErrBadCamera
	}
	normalizedForward := mul(camera.Forward, 1/lengthForward)
	up := sub(camera.Up, mul(normalizedForward, dot(camera.Up, normalizedForward)))
	if length(up) > epsilon {
		return normalizedForward, normalize(up), nil
	}

	var candidate Vec3
	ax, ay, az := math.Abs(normalizedForward.X), math.Abs(normalizedForward.Y), math.Abs(normalizedForward.Z)
	switch {
	case ax <= ay && ax <= az:
		candidate = Vec3{X: 1}
	case ay <= az:
		candidate = Vec3{Y: 1}
	default:
		candidate = Vec3{Z: 1}
	}
	fallback := sub(candidate, mul(normalizedForward, dot(candidate, normalizedForward)))
	return normalizedForward, normalize(fallback), nil
}

func findBreaks(path []Vec3, breakAngleDeg float64) []int {
	threshold := breakAngleDeg * math.Pi / 180
	breaks := make([]int, 0)
	for i := 1; i+1 < len(path); i++ {
		incoming := normalize(sub(path[i], path[i-1]))
		outgoing := normalize(sub(path[i+1], path[i]))
		angle := math.Acos(clamp(dot(incoming, outgoing), -1, 1))
		if angle > threshold+epsilon {
			breaks = append(breaks, i)
		}
	}
	return breaks
}

func parallelWithHysteresis(path []Vec3, forward Vec3, previous bool, threshold float64) bool {
	maxAlignment := 0.0
	for i := 1; i < len(path); i++ {
		tangent := normalize(sub(path[i], path[i-1]))
		if alignment := math.Abs(dot(tangent, forward)); alignment > maxAlignment {
			maxAlignment = alignment
		}
	}
	if previous {
		return maxAlignment >= threshold*0.9
	}
	return maxAlignment >= threshold
}

func segmentAxes(path []Vec3, breaks []int, forward, fallback Vec3, parallel bool, previousAxis Vec3) []Vec3 {
	axes := make([]Vec3, len(breaks)+1)
	firstEnd := len(path) - 1
	if len(breaks) != 0 {
		firstEnd = breaks[0]
	}
	axes[0] = segmentAxis(path[0], path[firstEnd], forward, fallback, parallel)
	if dot(axes[0], previousAxis) < 0 {
		axes[0] = mul(axes[0], -1)
	}
	start := firstEnd
	for i := 1; i <= len(breaks); i++ {
		end := len(path) - 1
		if i < len(breaks) {
			end = breaks[i]
		}
		axes[i] = segmentAxis(path[start], path[end], forward, fallback, parallel)
		start = end
	}
	return axes
}

func segmentAxis(start, end, forward, fallback Vec3, parallel bool) Vec3 {
	if parallel {
		return fallback
	}
	tangent := normalize(sub(end, start))
	axis := cross(tangent, forward)
	if length(axis) <= epsilon {
		return fallback
	}
	return normalize(axis)
}

func buildSegments(id BeamID, path []Vec3, widths []float64, breaks []int, axes []Vec3) Ribbon {
	count := 2*len(path) + 2*len(breaks)
	vertices := make([]Vec3, 0, count)
	sampled := make([]float64, 0, len(path)+len(breaks))
	start := 0
	for segment := 0; segment <= len(breaks); segment++ {
		end := len(path) - 1
		if segment < len(breaks) {
			end = breaks[segment]
		}
		axis := axes[segment]
		first := start
		for i := first; i <= end; i++ {
			offset := mul(axis, widths[i]/2)
			vertices = append(vertices, add(path[i], offset), sub(path[i], offset))
			sampled = append(sampled, widths[i])
		}
		start = end
	}
	return Ribbon{
		BeamID:   id,
		Segments: len(breaks) + 1,
		Widths:   sampled,
		Vertices: vertices,
		LineEnds: []Vec3{path[0], path[len(path)-1]},
	}
}

func buildDegraded(id BeamID, path []Vec3) Ribbon {
	ends := []Vec3{path[0], path[len(path)-1]}
	return Ribbon{
		BeamID:   id,
		Segments: 1,
		Vertices: append([]Vec3(nil), ends...),
		Degraded: true,
		LineEnds: ends,
	}
}

func (b *BeamRibbon) commitBuildLocked(id BeamID, priority int, full, degraded Ribbon, parallel bool, lastAxis Vec3) (Ribbon, error) {
	if _, exists := b.beams[id]; !exists && len(b.beams) >= b.limits.MaxBeams {
		return Ribbon{}, ErrBadLimits
	}

	limit := b.budget
	if b.limits.MaxVertices < limit {
		limit = b.limits.MaxVertices
	}
	others := 0
	for beamID, state := range b.beams {
		if beamID != id {
			others += len(state.ribbon.Vertices)
		}
	}

	result := full
	if others+len(full.Vertices) > limit {
		candidates := make([]BeamID, 0, len(b.beams))
		for beamID, state := range b.beams {
			if beamID != id && state.priority < priority && len(state.ribbon.Vertices) > 2 {
				candidates = append(candidates, beamID)
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			left, right := b.beams[candidates[i]], b.beams[candidates[j]]
			if left.priority != right.priority {
				return left.priority < right.priority
			}
			return candidates[i] < candidates[j]
		})
		running := others
		for _, beamID := range candidates {
			if running+len(full.Vertices) <= limit {
				break
			}
			state := b.beams[beamID]
			replacement := *state
			replacement.ribbon = degradedFromExisting(state.ribbon)
			replacement.bounds = boundsForRibbon(replacement.ribbon)
			running -= len(state.ribbon.Vertices) - len(replacement.ribbon.Vertices)
			b.beams[beamID] = &replacement
			b.stats.Degraded++
		}
		others = running
	}
	if others+len(full.Vertices) > limit {
		result = degraded
		b.stats.Degraded++
	}

	b.beams[id] = &beamState{
		priority: priority,
		ribbon:   cloneRibbon(result),
		parallel: parallel,
		lastAxis: lastAxis,
		bounds:   boundsForRibbon(result),
	}
	return cloneRibbon(result), nil
}

func degradedFromExisting(ribbon Ribbon) Ribbon {
	var ends []Vec3
	if len(ribbon.LineEnds) >= 2 {
		ends = append([]Vec3(nil), ribbon.LineEnds[:2]...)
	} else if len(ribbon.Vertices) >= 2 {
		ends = []Vec3{ribbon.Vertices[0], ribbon.Vertices[len(ribbon.Vertices)-1]}
	}
	if len(ends) != 2 {
		return ribbon
	}
	return Ribbon{
		BeamID:   ribbon.BeamID,
		Segments: 1,
		Vertices: append([]Vec3(nil), ends...),
		Degraded: true,
		LineEnds: ends,
	}
}

func (b *BeamRibbon) rebalanceBudgetLocked() {
	limit := b.budget
	if b.limits.MaxVertices < limit {
		limit = b.limits.MaxVertices
	}
	total := 0
	for _, state := range b.beams {
		total += len(state.ribbon.Vertices)
	}
	if total <= limit {
		return
	}
	candidates := make([]BeamID, 0, len(b.beams))
	for id, state := range b.beams {
		if !state.ribbon.Degraded && len(state.ribbon.Vertices) > 2 {
			candidates = append(candidates, id)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := b.beams[candidates[i]], b.beams[candidates[j]]
		if left.priority != right.priority {
			return left.priority < right.priority
		}
		return candidates[i] < candidates[j]
	})
	for _, id := range candidates {
		if total <= limit {
			break
		}
		state := b.beams[id]
		replacement := degradedFromExisting(state.ribbon)
		if replacement.Degraded {
			updated := *state
			updated.ribbon = replacement
			updated.bounds = boundsForRibbon(replacement)
			total -= len(state.ribbon.Vertices) - len(replacement.Vertices)
			b.beams[id] = &updated
			b.stats.Degraded++
		}
	}
}

func boundsForRibbon(ribbon Ribbon) sphere {
	if len(ribbon.Vertices) == 0 {
		return sphere{}
	}
	minPoint := ribbon.Vertices[0]
	maxPoint := ribbon.Vertices[0]
	for _, point := range ribbon.Vertices[1:] {
		minPoint.X = math.Min(minPoint.X, point.X)
		minPoint.Y = math.Min(minPoint.Y, point.Y)
		minPoint.Z = math.Min(minPoint.Z, point.Z)
		maxPoint.X = math.Max(maxPoint.X, point.X)
		maxPoint.Y = math.Max(maxPoint.Y, point.Y)
		maxPoint.Z = math.Max(maxPoint.Z, point.Z)
	}
	center := mul(add(minPoint, maxPoint), 0.5)
	radius := 0.0
	for _, point := range ribbon.Vertices {
		if d := length(sub(point, center)); d > radius {
			radius = d
		}
	}
	return sphere{center: center, radius: radius}
}

func sphereInsideFrustum(bounds sphere, view Frustum) bool {
	for _, plane := range view.Planes {
		normalLength := length(plane.Normal)
		if normalLength <= epsilon || !finite(normalLength) || !finite(plane.Offset) {
			continue
		}
		normal := mul(plane.Normal, 1/normalLength)
		offset := plane.Offset / normalLength
		if dot(normal, bounds.center)+offset > bounds.radius+epsilon {
			return false
		}
	}
	return true
}

func minimumWorldWidth(distance float64, limits RibbonLimits) float64 {
	if distance <= 0 {
		return 0
	}
	return limits.MinPixelWidth * distance * pixelWorldScale / limits.PixelsPerUnit
}

func validRibbonState(ribbon Ribbon) bool {
	if len(ribbon.LineEnds) != 2 || !finiteVec(ribbon.LineEnds[0]) || !finiteVec(ribbon.LineEnds[1]) {
		return false
	}
	if ribbon.Degraded {
		return len(ribbon.Vertices) == 2
	}
	if ribbon.Segments <= 0 || ribbon.Segments > len(ribbon.Widths) || len(ribbon.Widths) == 0 || len(ribbon.Vertices) != 2*len(ribbon.Widths) {
		return false
	}
	for _, point := range ribbon.Vertices {
		if !finiteVec(point) {
			return false
		}
	}
	for _, width := range ribbon.Widths {
		if width <= 0 || !finite(width) {
			return false
		}
	}
	return true
}

func cloneKeys(keys []Key) []Key {
	if keys == nil {
		return nil
	}
	return append([]Key(nil), keys...)
}

func cloneRibbon(ribbon Ribbon) Ribbon {
	ribbon.Widths = append([]float64(nil), ribbon.Widths...)
	ribbon.Vertices = append([]Vec3(nil), ribbon.Vertices...)
	ribbon.LineEnds = append([]Vec3(nil), ribbon.LineEnds...)
	return ribbon
}

func add(a, b Vec3) Vec3 {
	return Vec3{X: a.X + b.X, Y: a.Y + b.Y, Z: a.Z + b.Z}
}

func sub(a, b Vec3) Vec3 {
	return Vec3{X: a.X - b.X, Y: a.Y - b.Y, Z: a.Z - b.Z}
}

func mul(a Vec3, scalar float64) Vec3 {
	return Vec3{X: a.X * scalar, Y: a.Y * scalar, Z: a.Z * scalar}
}

func dot(a, b Vec3) float64 {
	return a.X*b.X + a.Y*b.Y + a.Z*b.Z
}

func cross(a, b Vec3) Vec3 {
	return Vec3{
		X: a.Y*b.Z - a.Z*b.Y,
		Y: a.Z*b.X - a.X*b.Z,
		Z: a.X*b.Y - a.Y*b.X,
	}
}

func length(v Vec3) float64 {
	return math.Sqrt(dot(v, v))
}

func normalize(v Vec3) Vec3 {
	vecLength := length(v)
	if vecLength <= epsilon {
		return Vec3{}
	}
	return mul(v, 1/vecLength)
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func finiteVec(v Vec3) bool {
	return finite(v.X) && finite(v.Y) && finite(v.Z)
}

func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
