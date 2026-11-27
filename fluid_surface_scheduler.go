// Package task schedules fluid surface particles: it splits a frame into CFL
// bounded substeps, advances a capped number of particles per frame, answers
// neighbour queries inside a budget and rebuilds the surface mesh from a
// density threshold.
package task

import (
	"errors"
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
}

// NewFluidSimScheduler builds a scheduler with the given config.
func NewFluidSimScheduler(cfg FluidConfig) *FluidSimScheduler {
	return &FluidSimScheduler{cfg: cfg}
}

// AddParticle inserts one particle and returns its identifier.
func (s *FluidSimScheduler) AddParticle(pos, vel Vec3) (ParticleID, error) {
	return 0, errors.New("not implemented")
}

// RemoveParticle drops one particle from the table.
func (s *FluidSimScheduler) RemoveParticle(id ParticleID) error {
	return errors.New("not implemented")
}

// SetBoundary installs the closed box particles must stay inside.
func (s *FluidSimScheduler) SetBoundary(bounds Bounds) error {
	return errors.New("not implemented")
}

// Step advances the particles that fit into the budget for one frame.
func (s *FluidSimScheduler) Step(dt time.Duration, budget int) (FluidState, error) {
	return FluidState{}, errors.New("not implemented")
}

// Neighbors lists the particle ids close to a position, ascending and capped.
func (s *FluidSimScheduler) Neighbors(pos Vec3) []ParticleID {
	return nil
}

// BuildSurface extracts the dense cells of a state into a point mesh.
func (s *FluidSimScheduler) BuildSurface(state FluidState) (Mesh, error) {
	return Mesh{}, errors.New("not implemented")
}

// SetDensityThreshold changes the minimum particle count of a dense cell.
func (s *FluidSimScheduler) SetDensityThreshold(n int) error {
	return errors.New("not implemented")
}

// State returns the most recent state produced by Step.
func (s *FluidSimScheduler) State() FluidState {
	return FluidState{}
}

// PendingParticles reports how many particles were left over by the last step.
func (s *FluidSimScheduler) PendingParticles() int {
	return 0
}

// Snapshot serialises particles, the boundary box, the counters and the
// deferred queue.
func (s *FluidSimScheduler) Snapshot() ([]byte, error) {
	return nil, errors.New("not implemented")
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *FluidSimScheduler) Restore(data []byte) error {
	return errors.New("not implemented")
}
