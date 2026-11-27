// Package task schedules fluid surface particles: it splits a frame into CFL
// bounded substeps, advances a capped number of particles per frame, answers
// neighbour queries inside a budget and rebuilds the surface mesh from a
// density threshold.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

// Errors returned by the scheduler.
var (
	ErrBadConfig       = errors.New("fluid config is invalid")
	ErrBadBounds       = errors.New("boundary box is invalid")
	ErrBadParticle     = errors.New("particle position or velocity is not finite")
	ErrOutOfBounds     = errors.New("particle is outside the boundary box")
	ErrParticleLimit   = errors.New("particle table is full")
	ErrUnknownParticle = errors.New("particle id is unknown")
	ErrBadDuration     = errors.New("step duration must be positive")
	ErrBadBudget       = errors.New("step budget must be positive")
	ErrSubstepLimit    = errors.New("cfl substep count exceeds the configured limit")
	ErrBadThreshold    = errors.New("density threshold must be at least one")
	ErrVertexLimit     = errors.New("surface mesh exceeds the vertex limit")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Vec3 is a point or a velocity in simulation space.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// ParticleID identifies one particle.
type ParticleID int

// Bounds is the closed axis aligned box particles may occupy.
type Bounds struct {
	Min Vec3 `json:"min"`
	Max Vec3 `json:"max"`
}

// Particle is one simulation particle.
type Particle struct {
	ID  ParticleID `json:"id"`
	Pos Vec3       `json:"pos"`
	Vel Vec3       `json:"vel"`
}

// FluidState is the snapshot of one finished step.
type FluidState struct {
	Tick      int        `json:"tick"`
	Substeps  int        `json:"substeps"`
	Advanced  int        `json:"advanced"`
	Particles []Particle `json:"particles"`
}

// Mesh is the surface extracted from a fluid state.
type Mesh struct {
	Vertices  []Vec3   `json:"vertices"`
	Indices   []uint32 `json:"indices"`
	Threshold int      `json:"threshold"`
}

// FluidConfig fixes the substep ladder, the neighbour budget and the caps.
type FluidConfig struct {
	MaxSubsteps      int           `json:"max_substeps"`
	MaxStep          time.Duration `json:"max_step"`
	Restitution      float64       `json:"restitution"`
	NeighborRadius   float64       `json:"neighbor_radius"`
	NeighborBudget   int           `json:"neighbor_budget"`
	MaxParticles     int           `json:"max_particles"`
	MaxVertices      int           `json:"max_vertices"`
	DensityThreshold int           `json:"density_threshold"`
}

// FluidSimScheduler owns the particle table, the boundary box and the deferred
// queue left over from the previous frame.
type FluidSimScheduler struct {
	cfg FluidConfig

	mu        sync.RWMutex
	particles []Particle
	nextID    ParticleID
	bounds    Bounds
	hasBounds bool
	pending   []ParticleID
	tick      int
	substeps  int
	advanced  int
}

// NewFluidSimScheduler builds a scheduler with the given config.
func NewFluidSimScheduler(cfg FluidConfig) *FluidSimScheduler {
	return &FluidSimScheduler{
		cfg:    cfg,
		nextID: 1,
	}
}

// AddParticle inserts one particle and returns its identifier.
func (s *FluidSimScheduler) AddParticle(pos, vel Vec3) (ParticleID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateConfig(s.cfg); err != nil {
		return 0, err
	}
	if !finiteVec3(pos) || !finiteVec3(vel) {
		return 0, ErrBadParticle
	}
	if s.hasBounds && !insideBounds(pos, s.bounds) {
		return 0, ErrOutOfBounds
	}
	if len(s.particles) >= s.cfg.MaxParticles {
		return 0, ErrParticleLimit
	}

	id := s.nextID
	s.nextID++
	s.particles = append(s.particles, Particle{ID: id, Pos: pos, Vel: vel})
	return id, nil
}

// RemoveParticle drops one particle from the table.
func (s *FluidSimScheduler) RemoveParticle(id ParticleID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	index := sort.Search(len(s.particles), func(i int) bool {
		return s.particles[i].ID >= id
	})
	if index == len(s.particles) || s.particles[index].ID != id {
		return ErrUnknownParticle
	}

	copy(s.particles[index:], s.particles[index+1:])
	s.particles = s.particles[:len(s.particles)-1]

	for i, pendingID := range s.pending {
		if pendingID == id {
			copy(s.pending[i:], s.pending[i+1:])
			s.pending = s.pending[:len(s.pending)-1]
			break
		}
	}
	return nil
}

// SetBoundary installs the closed box particles must stay inside.
func (s *FluidSimScheduler) SetBoundary(bounds Bounds) error {
	if !validBounds(bounds) {
		return ErrBadBounds
	}

	s.mu.Lock()
	s.bounds = bounds
	s.hasBounds = true
	s.mu.Unlock()
	return nil
}

// Step advances the particles that fit into the budget for one frame.
func (s *FluidSimScheduler) Step(dt time.Duration, budget int) (FluidState, error) {
	if dt <= 0 {
		return FluidState{}, ErrBadDuration
	}
	if budget <= 0 {
		return FluidState{}, ErrBadBudget
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateConfig(s.cfg); err != nil {
		return FluidState{}, err
	}

	substeps := int64(dt) / int64(s.cfg.MaxStep)
	if int64(dt)%int64(s.cfg.MaxStep) != 0 {
		substeps++
	}
	if substeps < 1 {
		substeps = 1
	}
	if substeps > int64(s.cfg.MaxSubsteps) {
		return FluidState{}, ErrSubstepLimit
	}

	order := s.advanceOrder()
	advanceCount := budget
	if advanceCount > len(order) {
		advanceCount = len(order)
	}

	durations := make([]time.Duration, int(substeps))
	base := dt / time.Duration(substeps)
	remainder := int(dt % time.Duration(substeps))
	for i := range durations {
		durations[i] = base
		if i < remainder {
			durations[i] += time.Nanosecond
		}
	}

	for _, id := range order[:advanceCount] {
		index := sort.Search(len(s.particles), func(i int) bool {
			return s.particles[i].ID >= id
		})
		if index == len(s.particles) || s.particles[index].ID != id {
			continue
		}
		s.particles[index] = s.advanceParticle(s.particles[index], durations)
	}

	s.pending = append(s.pending[:0], order[advanceCount:]...)
	s.tick++
	s.substeps = int(substeps)
	s.advanced = advanceCount
	return s.stateLocked(), nil
}

// Neighbors lists the particle ids close to a position, ascending and capped.
func (s *FluidSimScheduler) Neighbors(pos Vec3) []ParticleID {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if validateConfig(s.cfg) != nil {
		return nil
	}

	radiusSquared := s.cfg.NeighborRadius * s.cfg.NeighborRadius
	result := make([]ParticleID, 0, minInt(s.cfg.NeighborBudget, len(s.particles)))
	for _, particle := range s.particles {
		if len(result) >= s.cfg.NeighborBudget {
			break
		}
		dx := particle.Pos.X - pos.X
		dy := particle.Pos.Y - pos.Y
		dz := particle.Pos.Z - pos.Z
		if dx*dx+dy*dy+dz*dz <= radiusSquared {
			result = append(result, particle.ID)
		}
	}
	return result
}

// BuildSurface extracts the dense cells of a state into a point mesh.
func (s *FluidSimScheduler) BuildSurface(state FluidState) (Mesh, error) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	if validateConfig(cfg) != nil {
		return Mesh{}, ErrBadConfig
	}

	type cell struct {
		x int64
		y int64
		z int64
	}
	counts := make(map[cell]int, len(state.Particles))
	for _, particle := range state.Particles {
		key := cell{
			x: int64(math.Floor(particle.Pos.X / cfg.NeighborRadius)),
			y: int64(math.Floor(particle.Pos.Y / cfg.NeighborRadius)),
			z: int64(math.Floor(particle.Pos.Z / cfg.NeighborRadius)),
		}
		counts[key]++
	}

	dense := make([]cell, 0, len(counts))
	for key, count := range counts {
		if count >= cfg.DensityThreshold {
			dense = append(dense, key)
		}
	}
	if len(dense) > cfg.MaxVertices {
		return Mesh{}, ErrVertexLimit
	}

	sort.Slice(dense, func(i, j int) bool {
		if dense[i].x != dense[j].x {
			return dense[i].x < dense[j].x
		}
		if dense[i].y != dense[j].y {
			return dense[i].y < dense[j].y
		}
		return dense[i].z < dense[j].z
	})

	vertices := make([]Vec3, len(dense))
	indices := make([]uint32, len(dense))
	for i, key := range dense {
		vertices[i] = Vec3{
			X: (float64(key.x) + 0.5) * cfg.NeighborRadius,
			Y: (float64(key.y) + 0.5) * cfg.NeighborRadius,
			Z: (float64(key.z) + 0.5) * cfg.NeighborRadius,
		}
		indices[i] = uint32(i)
	}
	return Mesh{Vertices: vertices, Indices: indices, Threshold: cfg.DensityThreshold}, nil
}

// SetDensityThreshold changes the minimum particle count of a dense cell.
func (s *FluidSimScheduler) SetDensityThreshold(n int) error {
	if n < 1 {
		return ErrBadThreshold
	}
	s.mu.Lock()
	s.cfg.DensityThreshold = n
	s.mu.Unlock()
	return nil
}

// State returns the most recent state produced by Step.
func (s *FluidSimScheduler) State() FluidState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stateLocked()
}

// PendingParticles reports how many particles were left over by the last step.
func (s *FluidSimScheduler) PendingParticles() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pending)
}

// Snapshot serialises particles, the boundary box, the counters and the
// deferred queue.
func (s *FluidSimScheduler) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	payload := snapshotPayload{
		Version:          snapshotVersion,
		Particles:        cloneParticles(s.particles),
		NextID:           s.nextID,
		HasBounds:        s.hasBounds,
		Bounds:           s.bounds,
		Tick:             s.tick,
		Substeps:         s.substeps,
		Advanced:         s.advanced,
		Pending:          append([]ParticleID(nil), s.pending...),
		DensityThreshold: s.cfg.DensityThreshold,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *FluidSimScheduler) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	particles := cloneParticles(payload.Particles)
	sort.Slice(particles, func(i, j int) bool {
		return particles[i].ID < particles[j].ID
	})
	for i, particle := range particles {
		if particle.ID < 1 || (i > 0 && particle.ID == particles[i-1].ID) {
			return ErrBadSnapshot
		}
		if !finiteVec3(particle.Pos) || !finiteVec3(particle.Vel) {
			return ErrBadSnapshot
		}
	}
	if payload.NextID < 1 || (len(particles) > 0 && payload.NextID <= particles[len(particles)-1].ID) {
		return ErrBadSnapshot
	}
	if payload.HasBounds {
		if !validBounds(payload.Bounds) {
			return ErrBadSnapshot
		}
		for _, particle := range particles {
			if !insideBounds(particle.Pos, payload.Bounds) {
				return ErrBadSnapshot
			}
		}
	}
	if payload.Tick < 0 || payload.Substeps < 0 || payload.Advanced < 0 || payload.Advanced > len(particles) {
		return ErrBadSnapshot
	}
	if payload.DensityThreshold < 1 {
		return ErrBadSnapshot
	}

	exists := make(map[ParticleID]struct{}, len(particles))
	for _, particle := range particles {
		exists[particle.ID] = struct{}{}
	}
	pending := append([]ParticleID(nil), payload.Pending...)
	seenPending := make(map[ParticleID]struct{}, len(pending))
	for _, id := range pending {
		if _, ok := exists[id]; !ok {
			return ErrBadSnapshot
		}
		if _, duplicate := seenPending[id]; duplicate {
			return ErrBadSnapshot
		}
		seenPending[id] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(particles) > s.cfg.MaxParticles {
		return ErrBadSnapshot
	}
	s.particles = particles
	s.nextID = payload.NextID
	s.hasBounds = payload.HasBounds
	s.bounds = payload.Bounds
	s.pending = pending
	s.tick = payload.Tick
	s.substeps = payload.Substeps
	s.advanced = payload.Advanced
	s.cfg.DensityThreshold = payload.DensityThreshold
	return nil
}

const snapshotVersion = 1

type snapshotPayload struct {
	Version          int          `json:"version"`
	Particles        []Particle   `json:"particles"`
	NextID           ParticleID   `json:"next_id"`
	HasBounds        bool         `json:"has_bounds"`
	Bounds           Bounds       `json:"bounds"`
	Tick             int          `json:"tick"`
	Substeps         int          `json:"substeps"`
	Advanced         int          `json:"advanced"`
	Pending          []ParticleID `json:"pending"`
	DensityThreshold int          `json:"density_threshold"`
}

func validateConfig(cfg FluidConfig) error {
	if cfg.MaxSubsteps < 1 ||
		cfg.NeighborBudget < 1 ||
		cfg.MaxParticles < 1 ||
		cfg.MaxVertices < 1 ||
		cfg.DensityThreshold < 1 ||
		cfg.MaxStep <= 0 ||
		!finite(cfg.NeighborRadius) || cfg.NeighborRadius <= 0 ||
		!(cfg.Restitution >= 0 && cfg.Restitution <= 1) {
		return ErrBadConfig
	}
	return nil
}

func validBounds(bounds Bounds) bool {
	return finiteVec3(bounds.Min) && finiteVec3(bounds.Max) &&
		bounds.Min.X < bounds.Max.X &&
		bounds.Min.Y < bounds.Max.Y &&
		bounds.Min.Z < bounds.Max.Z
}

func insideBounds(pos Vec3, bounds Bounds) bool {
	return pos.X >= bounds.Min.X && pos.X <= bounds.Max.X &&
		pos.Y >= bounds.Min.Y && pos.Y <= bounds.Max.Y &&
		pos.Z >= bounds.Min.Z && pos.Z <= bounds.Max.Z
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finiteVec3(value Vec3) bool {
	return finite(value.X) && finite(value.Y) && finite(value.Z)
}

func cloneParticles(particles []Particle) []Particle {
	return append([]Particle(nil), particles...)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *FluidSimScheduler) stateLocked() FluidState {
	return FluidState{
		Tick:      s.tick,
		Substeps:  s.substeps,
		Advanced:  s.advanced,
		Particles: cloneParticles(s.particles),
	}
}

func (s *FluidSimScheduler) advanceOrder() []ParticleID {
	order := make([]ParticleID, 0, len(s.particles))
	seen := make(map[ParticleID]struct{}, len(s.pending)+len(s.particles))
	for _, id := range s.pending {
		index := sort.Search(len(s.particles), func(i int) bool {
			return s.particles[i].ID >= id
		})
		if index < len(s.particles) && s.particles[index].ID == id {
			if _, duplicate := seen[id]; !duplicate {
				order = append(order, id)
				seen[id] = struct{}{}
			}
		}
	}
	for _, particle := range s.particles {
		if _, deferred := seen[particle.ID]; !deferred {
			order = append(order, particle.ID)
		}
	}
	return order
}

func (s *FluidSimScheduler) advanceParticle(particle Particle, durations []time.Duration) Particle {
	for _, duration := range durations {
		seconds := float64(duration) / float64(time.Second)
		particle.Pos.X += particle.Vel.X * seconds
		particle.Pos.Y += particle.Vel.Y * seconds
		particle.Pos.Z += particle.Vel.Z * seconds
		if s.hasBounds {
			particle = bounceParticle(particle, s.bounds, s.cfg.Restitution)
		}
	}
	return particle
}

type boundaryHit struct {
	axis  int
	depth float64
}

func bounceParticle(particle Particle, bounds Bounds, restitution float64) Particle {
	hits := make([]boundaryHit, 0, 3)
	if particle.Pos.X < bounds.Min.X {
		hits = append(hits, boundaryHit{axis: 0, depth: bounds.Min.X - particle.Pos.X})
	} else if particle.Pos.X > bounds.Max.X {
		hits = append(hits, boundaryHit{axis: 0, depth: particle.Pos.X - bounds.Max.X})
	}
	if particle.Pos.Y < bounds.Min.Y {
		hits = append(hits, boundaryHit{axis: 1, depth: bounds.Min.Y - particle.Pos.Y})
	} else if particle.Pos.Y > bounds.Max.Y {
		hits = append(hits, boundaryHit{axis: 1, depth: particle.Pos.Y - bounds.Max.Y})
	}
	if particle.Pos.Z < bounds.Min.Z {
		hits = append(hits, boundaryHit{axis: 2, depth: bounds.Min.Z - particle.Pos.Z})
	} else if particle.Pos.Z > bounds.Max.Z {
		hits = append(hits, boundaryHit{axis: 2, depth: particle.Pos.Z - bounds.Max.Z})
	}
	if len(hits) == 0 {
		return particle
	}

	closest := hits[0]
	for _, hit := range hits[1:] {
		if hit.depth < closest.depth {
			closest = hit
		}
	}

	switch closest.axis {
	case 0:
		particle.Pos.X = mirrorAxis(particle.Pos.X, bounds.Min.X, bounds.Max.X)
		particle.Vel.X = -particle.Vel.X * restitution
	case 1:
		particle.Pos.Y = mirrorAxis(particle.Pos.Y, bounds.Min.Y, bounds.Max.Y)
		particle.Vel.Y = -particle.Vel.Y * restitution
	case 2:
		particle.Pos.Z = mirrorAxis(particle.Pos.Z, bounds.Min.Z, bounds.Max.Z)
		particle.Vel.Z = -particle.Vel.Z * restitution
	}

	for _, hit := range hits {
		if hit.axis == closest.axis {
			continue
		}
		switch hit.axis {
		case 0:
			particle.Pos.X = mirrorAxis(particle.Pos.X, bounds.Min.X, bounds.Max.X)
		case 1:
			particle.Pos.Y = mirrorAxis(particle.Pos.Y, bounds.Min.Y, bounds.Max.Y)
		case 2:
			particle.Pos.Z = mirrorAxis(particle.Pos.Z, bounds.Min.Z, bounds.Max.Z)
		}
	}
	return particle
}

func mirrorAxis(value, minimum, maximum float64) float64 {
	width := maximum - minimum
	if width <= 0 {
		return minimum
	}
	position := math.Mod(value-minimum, 2*width)
	if position < 0 {
		position += 2 * width
	}
	if position > width {
		position = 2*width - position
	}
	return minimum + position
}
