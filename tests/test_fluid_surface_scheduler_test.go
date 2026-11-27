package task

import (
	"errors"
	"math"
	"testing"
	"time"

	impl "gsb/batch51-go-game-b-07"
)

func fluidConfig() impl.FluidConfig {
	return impl.FluidConfig{
		MaxSubsteps:      8,
		MaxStep:          100 * time.Millisecond,
		Restitution:      0.5,
		NeighborRadius:   1,
		NeighborBudget:   4,
		MaxParticles:     32,
		MaxVertices:      16,
		DensityThreshold: 1,
	}
}

func near(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

func TestAddParticleAssignsAscendingIDs(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	first, err := s.AddParticle(impl.Vec3{}, impl.Vec3{})
	if err != nil {
		t.Fatalf("add the first particle: %v", err)
	}
	second, err := s.AddParticle(impl.Vec3{X: 0.5}, impl.Vec3{})
	if err != nil {
		t.Fatalf("add the second particle: %v", err)
	}
	if first != 1 {
		t.Fatalf("first particle id = %d, want 1", first)
	}
	if second != 2 {
		t.Fatalf("second particle id = %d, want 2", second)
	}
	state := s.State()
	if len(state.Particles) != 2 {
		t.Fatalf("particle table holds %d particles, want 2", len(state.Particles))
	}
	if state.Particles[0].ID != first || state.Particles[1].ID != second {
		t.Fatalf("particle table order = %+v, want ascending ids", state.Particles)
	}
}

func TestAddParticleEnforcesParticleLimit(t *testing.T) {
	cfg := fluidConfig()
	cfg.MaxParticles = 2
	s := impl.NewFluidSimScheduler(cfg)
	for i := 0; i < 2; i++ {
		if _, err := s.AddParticle(impl.Vec3{X: float64(i) * 0.1}, impl.Vec3{}); err != nil {
			t.Fatalf("add particle %d: %v", i, err)
		}
	}
	if _, err := s.AddParticle(impl.Vec3{X: 0.5}, impl.Vec3{}); !errors.Is(err, impl.ErrParticleLimit) {
		t.Fatalf("a particle beyond the cap must fail with ErrParticleLimit, got %v", err)
	}
	if got := len(s.State().Particles); got != 2 {
		t.Fatalf("particle table holds %d particles after the rejection, want 2", got)
	}
}

func TestAddParticleRejectsOutsideBoundary(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	if err := s.SetBoundary(impl.Bounds{Min: impl.Vec3{X: -1, Y: -1, Z: -1}, Max: impl.Vec3{X: 1, Y: 1, Z: 1}}); err != nil {
		t.Fatalf("set boundary: %v", err)
	}
	if _, err := s.AddParticle(impl.Vec3{X: 2}, impl.Vec3{}); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("a particle outside the box must fail with ErrOutOfBounds, got %v", err)
	}
	if _, err := s.AddParticle(impl.Vec3{Y: -1.5}, impl.Vec3{}); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("a particle below the box must fail with ErrOutOfBounds, got %v", err)
	}
	if err := s.SetBoundary(impl.Bounds{Min: impl.Vec3{X: 1}, Max: impl.Vec3{X: 1, Y: 1, Z: 1}}); !errors.Is(err, impl.ErrBadBounds) {
		t.Fatalf("a flat boundary box must fail with ErrBadBounds, got %v", err)
	}
	if _, err := s.AddParticle(impl.Vec3{}, impl.Vec3{}); err != nil {
		t.Fatalf("the rejected boundary must not replace the working box: %v", err)
	}
}

func TestStepRejectsInvalidArguments(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	if _, err := s.AddParticle(impl.Vec3{}, impl.Vec3{}); err != nil {
		t.Fatalf("add a particle: %v", err)
	}
	if _, err := s.Step(0, 4); !errors.Is(err, impl.ErrBadDuration) {
		t.Fatalf("a zero duration must fail with ErrBadDuration, got %v", err)
	}
	if _, err := s.Step(-time.Second, 4); !errors.Is(err, impl.ErrBadDuration) {
		t.Fatalf("a negative duration must fail with ErrBadDuration, got %v", err)
	}
	if _, err := s.Step(time.Millisecond, 0); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("a zero budget must fail with ErrBadBudget, got %v", err)
	}
	if _, err := s.Step(time.Millisecond, -3); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("a negative budget must fail with ErrBadBudget, got %v", err)
	}
}

func TestStepRejectsSubstepOverflow(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	id, err := s.AddParticle(impl.Vec3{}, impl.Vec3{X: 1})
	if err != nil {
		t.Fatalf("add particle: %v", err)
	}
	_, err = s.Step(time.Second, 8)
	if !errors.Is(err, impl.ErrSubstepLimit) {
		t.Fatalf("ten needed substeps against a limit of eight must fail with ErrSubstepLimit, got %v", err)
	}
	for _, p := range s.State().Particles {
		if p.ID == id && p.Pos.X != 0 {
			t.Fatalf("a rejected step moved the particle to x=%v, want 0", p.Pos.X)
		}
	}
}

func TestStepAdvancesByVelocityTimesDuration(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	if _, err := s.AddParticle(impl.Vec3{}, impl.Vec3{X: 1}); err != nil {
		t.Fatalf("add particle: %v", err)
	}
	state, err := s.Step(500*time.Millisecond, 8)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if state.Substeps != 5 {
		t.Fatalf("substeps for 500ms with a 100ms cap = %d, want 5", state.Substeps)
	}
	if state.Tick != 1 {
		t.Fatalf("tick after one step = %d, want 1", state.Tick)
	}
	if state.Advanced != 1 {
		t.Fatalf("advanced particles = %d, want 1", state.Advanced)
	}
	if !near(state.Particles[0].Pos.X, 0.5) {
		t.Fatalf("x after 500ms at 1 m/s = %v, want 0.5", state.Particles[0].Pos.X)
	}
	if !near(state.Particles[0].Vel.X, 1) {
		t.Fatalf("velocity must stay unchanged without a boundary hit, got %v", state.Particles[0].Vel.X)
	}
}

func TestStepSplitsSubstepsWithoutInflatingSpeed(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	if _, err := s.AddParticle(impl.Vec3{}, impl.Vec3{X: 1}); err != nil {
		t.Fatalf("add particle: %v", err)
	}
	state, err := s.Step(250*time.Millisecond, 8)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if state.Substeps != 3 {
		t.Fatalf("substeps for 250ms with a 100ms cap = %d, want 3", state.Substeps)
	}
	if !near(state.Particles[0].Pos.X, 0.25) {
		t.Fatalf("x after 250ms at 1 m/s = %v, want 0.25", state.Particles[0].Pos.X)
	}
	if !near(state.Particles[0].Vel.X, 1) {
		t.Fatalf("splitting must not scale the velocity, got %v", state.Particles[0].Vel.X)
	}
}

func TestStepBouncesInsideBoundary(t *testing.T) {
	cfg := fluidConfig()
	s := impl.NewFluidSimScheduler(cfg)
	if err := s.SetBoundary(impl.Bounds{Min: impl.Vec3{X: -1, Y: -1, Z: -1}, Max: impl.Vec3{X: 1, Y: 1, Z: 1}}); err != nil {
		t.Fatalf("set boundary: %v", err)
	}
	if _, err := s.AddParticle(impl.Vec3{X: 0.95}, impl.Vec3{X: 1}); err != nil {
		t.Fatalf("add particle: %v", err)
	}
	state, err := s.Step(100*time.Millisecond, 4)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	p := state.Particles[0]
	if !near(p.Pos.X, 0.95) {
		t.Fatalf("x after the bounce = %v, want the penetration mirrored back to 0.95", p.Pos.X)
	}
	if !near(p.Vel.X, -0.5) {
		t.Fatalf("x velocity after the bounce = %v, want -0.5 with restitution 0.5", p.Vel.X)
	}
	if p.Pos.Y != 0 || p.Pos.Z != 0 {
		t.Fatalf("the bounce must not touch the other axes, got y=%v z=%v", p.Pos.Y, p.Pos.Z)
	}
}

func TestStepDefersParticlesOverBudget(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	start := make(map[impl.ParticleID]float64, 3)
	for _, x := range []float64{-0.5, 0, 0.5} {
		id, err := s.AddParticle(impl.Vec3{X: x}, impl.Vec3{X: 1})
		if err != nil {
			t.Fatalf("add particle at %v: %v", x, err)
		}
		start[id] = x
	}
	state, err := s.Step(100*time.Millisecond, 2)
	if err != nil {
		t.Fatalf("step with a budget of 2: %v", err)
	}
	if state.Advanced != 2 {
		t.Fatalf("advanced particles = %d, want the budget 2", state.Advanced)
	}
	if got := s.PendingParticles(); got != 1 {
		t.Fatalf("pending particles = %d, want 1", got)
	}
	moved := 0
	for _, p := range state.Particles {
		if !near(p.Pos.X, start[p.ID]) {
			moved++
		}
	}
	if moved != 2 {
		t.Fatalf("particles that actually moved = %d, want exactly the budget 2", moved)
	}
	state, err = s.Step(100*time.Millisecond, 8)
	if err != nil {
		t.Fatalf("step with a budget of 8: %v", err)
	}
	if state.Advanced != 3 {
		t.Fatalf("advanced particles with a large budget = %d, want 3", state.Advanced)
	}
	if got := s.PendingParticles(); got != 0 {
		t.Fatalf("pending particles after a large budget = %d, want 0", got)
	}
}

func TestNeighborsWithinRadius(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	first, err := s.AddParticle(impl.Vec3{}, impl.Vec3{})
	if err != nil {
		t.Fatalf("add the first particle: %v", err)
	}
	second, err := s.AddParticle(impl.Vec3{X: 0.5}, impl.Vec3{})
	if err != nil {
		t.Fatalf("add the second particle: %v", err)
	}
	if _, err := s.AddParticle(impl.Vec3{X: 3}, impl.Vec3{}); err != nil {
		t.Fatalf("add the far particle: %v", err)
	}
	ids := s.Neighbors(impl.Vec3{})
	if len(ids) != 2 {
		t.Fatalf("neighbours within one metre = %v, want two ids", ids)
	}
	if ids[0] != first || ids[1] != second {
		t.Fatalf("neighbour list = %v, want ascending ids %d and %d", ids, first, second)
	}
}

func TestNeighborsRespectsBudget(t *testing.T) {
	cfg := fluidConfig()
	cfg.NeighborBudget = 2
	s := impl.NewFluidSimScheduler(cfg)
	for i := 0; i < 5; i++ {
		if _, err := s.AddParticle(impl.Vec3{X: float64(i) * 0.1}, impl.Vec3{}); err != nil {
			t.Fatalf("add particle %d: %v", i, err)
		}
	}
	ids := s.Neighbors(impl.Vec3{})
	if len(ids) != 2 {
		t.Fatalf("neighbour list length = %d, want the budget 2", len(ids))
	}
	if ids[0] >= ids[1] {
		t.Fatalf("neighbour list = %v, want ascending ids", ids)
	}
	if ids[0] != 1 {
		t.Fatalf("the first neighbour = %d, want the lowest id 1", ids[0])
	}
}

func TestBuildSurfaceKeepsDenseCells(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	for i := 0; i < 2; i++ {
		if _, err := s.AddParticle(impl.Vec3{X: 0.5, Y: 0.5, Z: 0.5}, impl.Vec3{}); err != nil {
			t.Fatalf("add the clustered particle %d: %v", i, err)
		}
	}
	if _, err := s.AddParticle(impl.Vec3{X: 2.5, Y: 0.5, Z: 0.5}, impl.Vec3{}); err != nil {
		t.Fatalf("add the lonely particle: %v", err)
	}
	state, err := s.Step(100*time.Millisecond, 8)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if err := s.SetDensityThreshold(2); err != nil {
		t.Fatalf("set density threshold: %v", err)
	}
	mesh, err := s.BuildSurface(state)
	if err != nil {
		t.Fatalf("build surface: %v", err)
	}
	if len(mesh.Vertices) != 1 {
		t.Fatalf("vertices with a threshold of two = %d, want only the dense cell", len(mesh.Vertices))
	}
	if mesh.Threshold != 2 {
		t.Fatalf("mesh threshold = %d, want 2", mesh.Threshold)
	}
	if len(mesh.Indices) != len(mesh.Vertices) {
		t.Fatalf("index count = %d, want one index per vertex", len(mesh.Indices))
	}
	if err := s.SetDensityThreshold(1); err != nil {
		t.Fatalf("lower the density threshold: %v", err)
	}
	mesh, err = s.BuildSurface(state)
	if err != nil {
		t.Fatalf("build surface again: %v", err)
	}
	if len(mesh.Vertices) != 2 {
		t.Fatalf("vertices with a threshold of one = %d, want 2", len(mesh.Vertices))
	}
	for i, index := range mesh.Indices {
		if int(index) != i {
			t.Fatalf("index %d = %d, want the identity order", i, index)
		}
	}
}

func TestBuildSurfaceRejectsVertexOverflow(t *testing.T) {
	cfg := fluidConfig()
	cfg.MaxVertices = 2
	s := impl.NewFluidSimScheduler(cfg)
	for _, x := range []float64{0.5, 1.5, 2.5} {
		if _, err := s.AddParticle(impl.Vec3{X: x}, impl.Vec3{}); err != nil {
			t.Fatalf("add particle at %v: %v", x, err)
		}
	}
	state, err := s.Step(100*time.Millisecond, 4)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if _, err := s.BuildSurface(state); !errors.Is(err, impl.ErrVertexLimit) {
		t.Fatalf("three dense cells against a limit of two must fail with ErrVertexLimit, got %v", err)
	}
	if err := s.SetDensityThreshold(0); !errors.Is(err, impl.ErrBadThreshold) {
		t.Fatalf("a threshold of zero must fail with ErrBadThreshold, got %v", err)
	}
}

func TestRemoveParticleAndUnknownID(t *testing.T) {
	s := impl.NewFluidSimScheduler(fluidConfig())
	first, err := s.AddParticle(impl.Vec3{}, impl.Vec3{})
	if err != nil {
		t.Fatalf("add the first particle: %v", err)
	}
	if _, err := s.AddParticle(impl.Vec3{X: 0.5}, impl.Vec3{}); err != nil {
		t.Fatalf("add the second particle: %v", err)
	}
	if err := s.RemoveParticle(first); err != nil {
		t.Fatalf("remove the first particle: %v", err)
	}
	if got := len(s.State().Particles); got != 1 {
		t.Fatalf("particle table holds %d particles after the removal, want 1", got)
	}
	if err := s.RemoveParticle(first); !errors.Is(err, impl.ErrUnknownParticle) {
		t.Fatalf("removing an unknown id must fail with ErrUnknownParticle, got %v", err)
	}
	next, err := s.AddParticle(impl.Vec3{X: -0.5}, impl.Vec3{})
	if err != nil {
		t.Fatalf("add another particle: %v", err)
	}
	if next <= 2 {
		t.Fatalf("particle ids must keep growing after a removal, got %d", next)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	cfg := fluidConfig()
	s := impl.NewFluidSimScheduler(cfg)
	if err := s.SetBoundary(impl.Bounds{Min: impl.Vec3{X: -2, Y: -2, Z: -2}, Max: impl.Vec3{X: 2, Y: 2, Z: 2}}); err != nil {
		t.Fatalf("set boundary: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.AddParticle(impl.Vec3{X: float64(i) * 0.2}, impl.Vec3{X: 0.5}); err != nil {
			t.Fatalf("add particle %d: %v", i, err)
		}
	}
	if _, err := s.Step(100*time.Millisecond, 2); err != nil {
		t.Fatalf("step: %v", err)
	}
	before := s.State()
	pending := s.PendingParticles()
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewFluidSimScheduler(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after := restored.State()
	if after.Tick != before.Tick {
		t.Fatalf("tick after the restore = %d, want %d", after.Tick, before.Tick)
	}
	if len(after.Particles) != len(before.Particles) {
		t.Fatalf("particle count after the restore = %d, want %d", len(after.Particles), len(before.Particles))
	}
	for i := range before.Particles {
		if !near(after.Particles[i].Pos.X, before.Particles[i].Pos.X) || !near(after.Particles[i].Vel.X, before.Particles[i].Vel.X) {
			t.Fatalf("particle %d after the restore = %+v, want %+v", i, after.Particles[i], before.Particles[i])
		}
	}
	if got := restored.PendingParticles(); got != pending {
		t.Fatalf("pending particles after the restore = %d, want %d", got, pending)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.State().Tick; got != before.Tick {
		t.Fatalf("a rejected restore changed the tick to %d, want %d", got, before.Tick)
	}
}
