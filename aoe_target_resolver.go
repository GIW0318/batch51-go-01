// Package task resolves area skill casts against a world snapshot: it applies
// the shape geometry, drops occluded targets, scales damage by distance and
// truncates the hit list to the configured target cap.
package task

import (
	"context"
	"errors"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Area shape kinds.
const (
	ShapeCircle = "circle"
	ShapeSector = "sector"
	ShapeRect   = "rect"
)

// Line of sight modes.
const (
	LosStrict         = "strict"
	LosPenetrablePass = "penetrable_pass"
)

// Falloff curve kinds.
const (
	FalloffNone   = "none"
	FalloffLinear = "linear"
)

// ScaleFullPermille is the damage scale applied at the shape origin.
const ScaleFullPermille = 1000

// Trace reason codes reported by Explain.
const (
	TraceInShape       = "in_shape"
	TraceOutOfShape    = "out_of_shape"
	TraceBlocked       = "blocked_los"
	TraceOverTargetCap = "over_target_cap"
	TraceOverCandidate = "over_candidate_cap"
)

// Errors reported by the resolver.
var (
	ErrBadShape    = errors.New("aoe shape is invalid")
	ErrBadTargets  = errors.New("max targets must be positive")
	ErrBadFalloff  = errors.New("falloff curve is invalid")
	ErrBadLosMode  = errors.New("line of sight mode is unknown")
	ErrBadCast     = errors.New("cast request is invalid")
	ErrUnknownCast = errors.New("unknown cast")
	ErrCastLimit   = errors.New("too many casts are in flight")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Vec2 is a point on the ground plane.
type Vec2 struct {
	X float64
	Y float64
}

// AoeShape is the geometry of one area skill.
type AoeShape struct {
	Kind     string
	Radius   float64
	AngleDeg float64
	Width    float64
	Length   float64
}

// FalloffCurve maps distance to a damage scale in permille.
type FalloffCurve struct {
	Kind          string
	FloorPermille int
}

// CastReq is one cast submitted by the combat loop.
type CastReq struct {
	CasterID  string
	Origin    Vec2
	FacingDeg float64
	Power     int
}

// CastID is the monotonic identifier of one cast.
type CastID int64

// Entity is one target candidate of the world snapshot.
type Entity struct {
	ID     string
	Pos    Vec2
	Radius float64
}

// Occluder is an axis aligned rectangle that may block line of sight.
type Occluder struct {
	Min        Vec2
	Max        Vec2
	Penetrable bool
}

// WorldView is the entity and occluder snapshot passed to one resolution.
type WorldView struct {
	Entities  []Entity
	Occluders []Occluder
}

// AoeLimits bounds one resolver.
type AoeLimits struct {
	MaxCandidates  int
	MaxRays        int
	MaxActiveCasts int
}

// AoeHit is one resolved target.
type AoeHit struct {
	EntityID      string
	Distance      float64
	ScalePermille int
	Damage        int
}

// AoeResult is the deterministic outcome of one cast.
type AoeResult struct {
	CastID    CastID
	Origin    Vec2
	Hits      []AoeHit
	Truncated int
}

// TargetTrace explains why one candidate was kept or dropped.
type TargetTrace struct {
	EntityID string
	Distance float64
	AngleDeg float64
	Included bool
	Reason   string
}

// AoeStats aggregates the resolver counters.
type AoeStats struct {
	CastsSubmitted int
	CastsResolved  int
	Candidates     int
	HitTotal       int
	DamageTotal    int
	HateTotal      int
}

// AoeTargetResolver owns the shape configuration, the frozen casts and the
// resolved results.
type AoeTargetResolver struct {
	limits AoeLimits
}

// NewAoeTargetResolver builds a resolver with the given caps.
func NewAoeTargetResolver(limits AoeLimits) *AoeTargetResolver {
	return &AoeTargetResolver{limits: limits}
}

// SetShape installs the geometry used by subsequent casts.
func (r *AoeTargetResolver) SetShape(s AoeShape) error {
	return errNotImplemented
}

// SetMaxTargets installs the hit cap used by subsequent casts.
func (r *AoeTargetResolver) SetMaxTargets(n int) error {
	return errNotImplemented
}

// SetFalloff installs the falloff curve used by subsequent casts.
func (r *AoeTargetResolver) SetFalloff(f FalloffCurve) error {
	return errNotImplemented
}

// SetLosMode installs the line of sight mode used by subsequent casts.
func (r *AoeTargetResolver) SetLosMode(m string) error {
	return errNotImplemented
}

// SubmitCast freezes the current configuration and assigns a cast id.
func (r *AoeTargetResolver) SubmitCast(c CastReq) (CastID, error) {
	return 0, errNotImplemented
}

// Resolve evaluates one cast against a world snapshot exactly once.
func (r *AoeTargetResolver) Resolve(ctx context.Context, id CastID, world WorldView) (AoeResult, error) {
	return AoeResult{}, errNotImplemented
}

// Explain returns the candidate traces of the last resolution of one cast.
func (r *AoeTargetResolver) Explain(id CastID) []TargetTrace {
	return nil
}

// Stats returns a copy of the resolver counters.
func (r *AoeTargetResolver) Stats() AoeStats {
	return AoeStats{}
}

// Snapshot serialises the configuration, frozen casts and resolved results.
func (r *AoeTargetResolver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the resolver state with a snapshot, all or nothing.
func (r *AoeTargetResolver) Restore(data []byte) error {
	return errNotImplemented
}
