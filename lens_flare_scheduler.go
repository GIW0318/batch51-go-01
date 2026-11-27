// Package task plans screen space lens flares for a set of scene light
// sources, using asynchronously reported occlusion results and per frame fade.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sync"
)

// Defaults applied by NewLensFlareScheduler for limits left at zero.
const (
	DefaultFadeIn       float32 = 0.25
	DefaultFadeOut      float32 = 0.2
	DefaultMaxSources           = 256
	DefaultMaxInstances         = 32
	DefaultMaxQueries           = 8
)

const snapshotVersion = 1

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
	ErrBadSource     = errors.New("flare source is not usable")
	ErrSourceExists  = errors.New("flare source id is already registered")
	ErrSourceLimit   = errors.New("flare source limit reached")
	ErrUnknownSource = errors.New("flare source id is not registered")
	ErrBadOcclusion  = errors.New("occlusion visibility must be between zero and one")
	ErrBadLimit      = errors.New("instance limit must be positive")
	ErrBadFade       = errors.New("fade rate must be inside the open unit interval")
	ErrBadCamera     = errors.New("camera is not usable")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

type sourceState struct {
	source FlareSource

	// intensity is the fade progress exposed to the renderer.
	intensity float32
	// visibility is the last occlusion result consumed by a plan. reported is
	// the newest backend result and is promoted to visibility when this source
	// gets one of the next frame's query slots.
	visibility float32
	reported   float32
	deferred   bool
}

// LensFlareScheduler owns the source table, the fade progress and the per
// frame query and instance budgets.
type LensFlareScheduler struct {
	mu sync.RWMutex

	maxSources   int
	maxQueries   int
	maxInstances int
	fadeIn       float32
	fadeOut      float32
	frame        int

	sources    map[string]*sourceState
	order      []string
	priorities map[string]int
	stats      FlareStats
}

// NewLensFlareScheduler builds a scheduler with the given caps.
func NewLensFlareScheduler(limits FlareLimits) *LensFlareScheduler {
	if limits.MaxSources <= 0 {
		limits.MaxSources = DefaultMaxSources
	}
	if limits.MaxInstances <= 0 {
		limits.MaxInstances = DefaultMaxInstances
	}
	if limits.MaxQueriesPerFrame <= 0 {
		limits.MaxQueriesPerFrame = DefaultMaxQueries
	}
	return &LensFlareScheduler{
		maxSources:   limits.MaxSources,
		maxInstances: limits.MaxInstances,
		maxQueries:   limits.MaxQueriesPerFrame,
		fadeIn:       DefaultFadeIn,
		fadeOut:      DefaultFadeOut,
		sources:      make(map[string]*sourceState),
		priorities:   make(map[string]int),
	}
}

func validSourceID(id string) bool {
	return id != ""
}

func validSource(src FlareSource) bool {
	return finitePositive(src.Brightness) && finitePositive(src.Radius)
}

func finitePositive(v float32) bool {
	return v > 0 && !float32IsInf(v) && !float32IsNaN(v)
}

func float32IsNaN(v float32) bool {
	return v != v
}

func float32IsInf(v float32) bool {
	return math.IsInf(float64(v), 0)
}

// RegisterSource adds one light source.
func (s *LensFlareScheduler) RegisterSource(id string, src FlareSource) error {
	if !validSourceID(id) || !validSource(src) {
		return ErrBadSource
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sources[id]; exists {
		return ErrSourceExists
	}
	if len(s.sources) >= s.maxSources {
		return ErrSourceLimit
	}
	s.sources[id] = &sourceState{
		source:     src,
		visibility: 1,
		reported:   1,
	}
	s.order = append(s.order, id)
	return nil
}

// UpdateSource replaces the geometry of a registered light source.
func (s *LensFlareScheduler) UpdateSource(id string, src FlareSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, exists := s.sources[id]
	if !exists {
		return ErrUnknownSource
	}
	if !validSourceID(id) || !validSource(src) {
		return ErrBadSource
	}
	state.source = src
	return nil
}

// SetOcclusionResult stores the visibility reported by the occlusion backend.
func (s *LensFlareScheduler) SetOcclusionResult(id string, visible float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, exists := s.sources[id]
	if !exists {
		return ErrUnknownSource
	}
	if !(visible >= 0 && visible <= 1) {
		return ErrBadOcclusion
	}
	state.reported = visible
	return nil
}

type cameraBasis struct {
	position Vec3
	forward  Vec3
	right    Vec3
	up       Vec3
	tanY     float64
	aspect   float64
}

func makeCameraBasis(cam Camera) (cameraBasis, bool) {
	if !(cam.FovDeg > 0) || float32IsNaN(cam.FovDeg) || float32IsInf(cam.FovDeg) || cam.FovDeg >= 180 {
		return cameraBasis{}, false
	}
	if cam.ViewportW <= 0 || cam.ViewportH <= 0 {
		return cameraBasis{}, false
	}

	forward, ok := normalize(cam.Forward)
	if !ok {
		return cameraBasis{}, false
	}

	right, ok := normalize(cross(cam.Up, forward))
	if !ok {
		reference := Vec3{Y: 1}
		if math.Abs(float64(forward.Y)) > 0.99 {
			reference = Vec3{X: 1}
		}
		right, ok = normalize(cross(reference, forward))
		if !ok {
			return cameraBasis{}, false
		}
	}
	up, ok := normalize(cross(forward, right))
	if !ok {
		return cameraBasis{}, false
	}

	tanY := math.Tan(float64(cam.FovDeg) * math.Pi / 360)
	if !(tanY > 0) || math.IsInf(tanY, 0) || math.IsNaN(tanY) {
		return cameraBasis{}, false
	}
	return cameraBasis{
		position: cam.Position,
		forward:  forward,
		right:    right,
		up:       up,
		tanY:     tanY,
		aspect:   float64(cam.ViewportW) / float64(cam.ViewportH),
	}, true
}

func normalize(v Vec3) (Vec3, bool) {
	lengthSq := float64(v.X)*float64(v.X) + float64(v.Y)*float64(v.Y) + float64(v.Z)*float64(v.Z)
	if !(lengthSq > 0) || math.IsInf(lengthSq, 0) || math.IsNaN(lengthSq) {
		return Vec3{}, false
	}
	length := math.Sqrt(lengthSq)
	return Vec3{
		X: float32(float64(v.X) / length),
		Y: float32(float64(v.Y) / length),
		Z: float32(float64(v.Z) / length),
	}, true
}

func cross(a, b Vec3) Vec3 {
	return Vec3{
		X: a.Y*b.Z - a.Z*b.Y,
		Y: a.Z*b.X - a.X*b.Z,
		Z: a.X*b.Y - a.Y*b.X,
	}
}

func dot(a, b Vec3) float64 {
	return float64(a.X)*float64(b.X) + float64(a.Y)*float64(b.Y) + float64(a.Z)*float64(b.Z)
}

type projectedSource struct {
	id        string
	state     *sourceState
	ndcX      float32
	ndcY      float32
	depth     float32
	intensity float32
	scale     float32
}

// Plan advances one frame and returns the flares to draw.
func (s *LensFlareScheduler) Plan(cam Camera) ([]FlareInstance, error) {
	basis, ok := makeCameraBasis(cam)
	if !ok {
		return nil, ErrBadCamera
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.frame++
	stats := FlareStats{
		Frame:   s.frame,
		Sources: len(s.order),
	}

	visibleSources := make([]projectedSource, 0, len(s.order))
	for _, id := range s.order {
		state := s.sources[id]
		delta := Vec3{
			X: state.source.Position.X - basis.position.X,
			Y: state.source.Position.Y - basis.position.Y,
			Z: state.source.Position.Z - basis.position.Z,
		}

		depth := dot(delta, basis.forward)
		if !(depth > 0) || math.IsInf(depth, 0) || math.IsNaN(depth) {
			state.intensity = advanceFade(state.intensity, 0, s.fadeIn, s.fadeOut)
			stats.Behind++
			continue
		}

		depth32 := float32(depth)
		cameraX := dot(delta, basis.right)
		cameraY := dot(delta, basis.up)
		ndcX := cameraX / (depth * basis.tanY * basis.aspect)
		ndcY := cameraY / (depth * basis.tanY)
		if !(ndcX >= -1 && ndcX <= 1 && ndcY >= -1 && ndcY <= 1) ||
			math.IsInf(ndcX, 0) || math.IsNaN(ndcX) ||
			math.IsInf(ndcY, 0) || math.IsNaN(ndcY) {
			state.intensity = advanceFade(state.intensity, 0, s.fadeIn, s.fadeOut)
			stats.Offscreen++
			continue
		}

		visibleSources = append(visibleSources, projectedSource{
			id:    id,
			state: state,
			ndcX:  float32(ndcX),
			ndcY:  float32(ndcY),
			depth: depth32,
		})
	}

	// Sources deferred on the previous frame get the first query slots, then
	// the rest are served in registration order. Every on-screen source still
	// participates in sorting, even when its query has to wait.
	selected := make(map[string]bool, len(visibleSources))
	slots := 0
	for _, projected := range visibleSources {
		if slots >= s.maxQueries {
			break
		}
		if projected.state.deferred {
			selected[projected.id] = true
			slots++
		}
	}
	for _, projected := range visibleSources {
		if slots >= s.maxQueries {
			break
		}
		if selected[projected.id] || projected.state.deferred {
			continue
		}
		selected[projected.id] = true
		slots++
	}

	stats.Queries = slots
	for i := range visibleSources {
		projected := &visibleSources[i]
		state := projected.state
		if selected[projected.id] {
			state.visibility = state.reported
			state.deferred = false
		} else {
			state.deferred = true
			stats.Deferred++
		}

		state.intensity = advanceFade(state.intensity, state.visibility, s.fadeIn, s.fadeOut)
		if state.visibility > 0 {
			stats.Visible++
		} else {
			stats.Occluded++
		}
		projected.intensity = state.intensity
		if projected.depth > 0 {
			projected.scale = state.source.Brightness * state.source.Radius / projected.depth
		}
	}

	sort.Slice(visibleSources, func(i, j int) bool {
		if visibleSources[i].scale == visibleSources[j].scale {
			return visibleSources[i].id < visibleSources[j].id
		}
		return visibleSources[i].scale > visibleSources[j].scale
	})

	if len(visibleSources) > s.maxInstances {
		visibleSources = visibleSources[:s.maxInstances]
	}

	instances := make([]FlareInstance, len(visibleSources))
	priorities := make(map[string]int, len(visibleSources))
	for i, projected := range visibleSources {
		instances[i] = FlareInstance{
			ID:        projected.id,
			NdcX:      projected.ndcX,
			NdcY:      projected.ndcY,
			Intensity: projected.state.intensity,
			Scale:     projected.scale,
			Order:     i,
		}
		priorities[projected.id] = i
	}

	stats.Instances = len(instances)
	s.stats = stats
	s.priorities = priorities
	return instances, nil
}

func advanceFade(current, target, fadeIn, fadeOut float32) float32 {
	target = clamp01(target)
	switch {
	case target > current:
		current += fadeIn
		if current > target {
			current = target
		}
	case target < current:
		current -= fadeOut
		if current < target {
			current = target
		}
	}
	return clamp01(current)
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// SetMaxInstances replaces the per frame instance cap.
func (s *LensFlareScheduler) SetMaxInstances(n int) error {
	if n <= 0 {
		return ErrBadLimit
	}
	s.mu.Lock()
	s.maxInstances = n
	s.mu.Unlock()
	return nil
}

// SetFadeRates replaces the per frame fade in and fade out steps.
func (s *LensFlareScheduler) SetFadeRates(in, out float32) error {
	if !validFadeRate(in) || !validFadeRate(out) {
		return ErrBadFade
	}
	s.mu.Lock()
	s.fadeIn = in
	s.fadeOut = out
	s.mu.Unlock()
	return nil
}

func validFadeRate(rate float32) bool {
	return rate > 0 && rate < 1 && !float32IsNaN(rate) && !float32IsInf(rate)
}

// Priority reports the position of a source in the most recent plan.
func (s *LensFlareScheduler) Priority(id string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	priority, exists := s.priorities[id]
	if !exists {
		return -1
	}
	return priority
}

// Intensity reports the current fade progress of a source.
func (s *LensFlareScheduler) Intensity(id string) float32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, exists := s.sources[id]
	if !exists {
		return 0
	}
	return state.intensity
}

// SourceCount reports how many sources are registered.
func (s *LensFlareScheduler) SourceCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.order)
}

// Stats reports the counters of the last plan.
func (s *LensFlareScheduler) Stats() FlareStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

type snapshotSource struct {
	ID         string      `json:"id"`
	Source     FlareSource `json:"source"`
	Intensity  float32     `json:"intensity"`
	Visibility float32     `json:"visibility"`
	Reported   float32     `json:"reported_visibility"`
	Deferred   bool        `json:"deferred"`
}

type schedulerSnapshot struct {
	Version            int              `json:"version"`
	Sources            []snapshotSource `json:"sources"`
	MaxSources         int              `json:"max_sources"`
	MaxInstances       int              `json:"max_instances"`
	MaxQueriesPerFrame int              `json:"max_queries_per_frame"`
	FadeIn             float32          `json:"fade_in"`
	FadeOut            float32          `json:"fade_out"`
	Frame              int              `json:"frame"`
}

// Snapshot serialises the source table, the fade progress and the budgets.
func (s *LensFlareScheduler) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := schedulerSnapshot{
		Version:            snapshotVersion,
		Sources:            make([]snapshotSource, 0, len(s.order)),
		MaxSources:         s.maxSources,
		MaxInstances:       s.maxInstances,
		MaxQueriesPerFrame: s.maxQueries,
		FadeIn:             s.fadeIn,
		FadeOut:            s.fadeOut,
		Frame:              s.frame,
	}
	for _, id := range s.order {
		state := s.sources[id]
		snapshot.Sources = append(snapshot.Sources, snapshotSource{
			ID:         id,
			Source:     state.source,
			Intensity:  state.intensity,
			Visibility: state.visibility,
			Reported:   state.reported,
			Deferred:   state.deferred,
		})
	}
	return json.Marshal(snapshot)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *LensFlareScheduler) Restore(data []byte) error {
	var snapshot schedulerSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	if !validSnapshot(snapshot) {
		return ErrBadSnapshot
	}

	sources := make(map[string]*sourceState, len(snapshot.Sources))
	order := make([]string, 0, len(snapshot.Sources))
	for _, saved := range snapshot.Sources {
		sources[saved.ID] = &sourceState{
			source:     saved.Source,
			intensity:  saved.Intensity,
			visibility: saved.Visibility,
			reported:   saved.Reported,
			deferred:   saved.Deferred,
		}
		order = append(order, saved.ID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxSources = snapshot.MaxSources
	s.maxInstances = snapshot.MaxInstances
	s.maxQueries = snapshot.MaxQueriesPerFrame
	s.fadeIn = snapshot.FadeIn
	s.fadeOut = snapshot.FadeOut
	s.frame = snapshot.Frame
	s.sources = sources
	s.order = order
	s.priorities = make(map[string]int)
	s.stats = FlareStats{Frame: snapshot.Frame, Sources: len(order)}
	return nil
}

func validSnapshot(snapshot schedulerSnapshot) bool {
	if snapshot.Version != snapshotVersion {
		return false
	}
	if snapshot.MaxSources <= 0 || snapshot.MaxInstances <= 0 || snapshot.MaxQueriesPerFrame <= 0 {
		return false
	}
	if !validFadeRate(snapshot.FadeIn) || !validFadeRate(snapshot.FadeOut) {
		return false
	}
	if snapshot.Frame < 0 || len(snapshot.Sources) > snapshot.MaxSources {
		return false
	}

	seen := make(map[string]struct{}, len(snapshot.Sources))
	for _, saved := range snapshot.Sources {
		if !validSourceID(saved.ID) || !validSource(saved.Source) {
			return false
		}
		if _, exists := seen[saved.ID]; exists {
			return false
		}
		seen[saved.ID] = struct{}{}
		if !unitInterval(saved.Intensity) ||
			!unitInterval(saved.Visibility) ||
			!unitInterval(saved.Reported) {
			return false
		}
	}
	return true
}

func unitInterval(v float32) bool {
	return v >= 0 && v <= 1 && !float32IsNaN(v) && !float32IsInf(v)
}
