// Package task resolves area skill casts against a world snapshot: it applies
// the shape geometry, drops occluded targets, scales damage by distance and
// truncates the hit list to the configured target cap.
package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
)

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
	mu sync.Mutex

	limits     AoeLimits
	shape      AoeShape
	maxTargets int
	falloff    FalloffCurve
	losMode    string

	nextID  CastID
	active  int
	casts   map[CastID]*frozenCast
	results map[CastID]*resolvedCast
	stats   AoeStats
}

type frozenCast struct {
	ID         CastID
	CasterID   string
	Origin     Vec2
	FacingDeg  float64
	Power      int
	Shape      AoeShape
	MaxTargets int
	Falloff    FalloffCurve
	LosMode    string
}

type resolvedCast struct {
	Result AoeResult
	Traces []TargetTrace
}

type candidate struct {
	entity   Entity
	distance float64
	angleDeg float64
}

// NewAoeTargetResolver builds a resolver with the given caps.
func NewAoeTargetResolver(limits AoeLimits) *AoeTargetResolver {
	return &AoeTargetResolver{
		limits:  limits,
		falloff: FalloffCurve{Kind: FalloffNone},
		losMode: LosStrict,
		nextID:  1,
		casts:   make(map[CastID]*frozenCast),
		results: make(map[CastID]*resolvedCast),
	}
}

// SetShape installs the geometry used by subsequent casts.
func (r *AoeTargetResolver) SetShape(s AoeShape) error {
	if !validShape(s) {
		return ErrBadShape
	}
	r.mu.Lock()
	r.shape = s
	r.mu.Unlock()
	return nil
}

// SetMaxTargets installs the hit cap used by subsequent casts.
func (r *AoeTargetResolver) SetMaxTargets(n int) error {
	if n <= 0 {
		return ErrBadTargets
	}
	r.mu.Lock()
	r.maxTargets = n
	r.mu.Unlock()
	return nil
}

// SetFalloff installs the falloff curve used by subsequent casts.
func (r *AoeTargetResolver) SetFalloff(f FalloffCurve) error {
	switch f.Kind {
	case FalloffNone:
	case FalloffLinear:
		if f.FloorPermille < 0 || f.FloorPermille > ScaleFullPermille {
			return ErrBadFalloff
		}
	default:
		return ErrBadFalloff
	}
	r.mu.Lock()
	r.falloff = f
	r.mu.Unlock()
	return nil
}

// SetLosMode installs the line of sight mode used by subsequent casts.
func (r *AoeTargetResolver) SetLosMode(m string) error {
	switch m {
	case LosStrict, LosPenetrablePass:
	default:
		return ErrBadLosMode
	}
	r.mu.Lock()
	r.losMode = m
	r.mu.Unlock()
	return nil
}

// SubmitCast freezes the current configuration and assigns a cast id.
func (r *AoeTargetResolver) SubmitCast(c CastReq) (CastID, error) {
	if strings.TrimSpace(c.CasterID) == "" || c.Power < 0 {
		return 0, ErrBadCast
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active >= r.limits.MaxActiveCasts {
		return 0, ErrCastLimit
	}

	id := r.nextID
	r.nextID++
	r.active++
	r.casts[id] = &frozenCast{
		ID:         id,
		CasterID:   c.CasterID,
		Origin:     c.Origin,
		FacingDeg:  c.FacingDeg,
		Power:      c.Power,
		Shape:      r.shape,
		MaxTargets: r.maxTargets,
		Falloff:    r.falloff,
		LosMode:    r.losMode,
	}
	r.stats.CastsSubmitted++
	return id, nil
}

// Resolve evaluates one cast against a world snapshot exactly once.
func (r *AoeTargetResolver) Resolve(ctx context.Context, id CastID, world WorldView) (AoeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	cast, ok := r.casts[id]
	if !ok {
		return AoeResult{}, ErrUnknownCast
	}
	if resolved, ok := r.results[id]; ok {
		return cloneResult(resolved.Result), nil
	}
	if err := ctx.Err(); err != nil {
		return AoeResult{}, err
	}

	result, traces, err := resolveCast(ctx, cast, world, r.limits)
	if err != nil {
		return AoeResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return AoeResult{}, err
	}

	r.results[id] = &resolvedCast{
		Result: cloneResult(result),
		Traces: cloneTraces(traces),
	}
	if r.active > 0 {
		r.active--
	}
	r.stats.CastsResolved++
	r.stats.Candidates += len(traces)
	r.stats.HitTotal += len(result.Hits)
	for _, hit := range result.Hits {
		r.stats.DamageTotal += hit.Damage
		r.stats.HateTotal += hit.Damage
	}
	return cloneResult(result), nil
}

// Explain returns the candidate traces of the last resolution of one cast.
func (r *AoeTargetResolver) Explain(id CastID) []TargetTrace {
	r.mu.Lock()
	defer r.mu.Unlock()
	resolved, ok := r.results[id]
	if !ok {
		return nil
	}
	return cloneTraces(resolved.Traces)
}

// Stats returns a copy of the resolver counters.
func (r *AoeTargetResolver) Stats() AoeStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

// Snapshot serialises the configuration, frozen casts and resolved results.
func (r *AoeTargetResolver) Snapshot() ([]byte, error) {
	r.mu.Lock()
	state := snapshotState{
		Limits:     r.limits,
		Shape:      r.shape,
		MaxTargets: r.maxTargets,
		Falloff:    r.falloff,
		LosMode:    r.losMode,
		NextID:     r.nextID,
		Stats:      r.stats,
	}
	castIDs := make([]CastID, 0, len(r.casts))
	for id := range r.casts {
		castIDs = append(castIDs, id)
	}
	sort.Slice(castIDs, func(i, j int) bool { return castIDs[i] < castIDs[j] })
	state.Casts = make([]snapshotCast, 0, len(castIDs))
	for _, id := range castIDs {
		cast := r.casts[id]
		state.Casts = append(state.Casts, snapshotCast{
			ID:         cast.ID,
			CasterID:   cast.CasterID,
			Origin:     cast.Origin,
			FacingDeg:  cast.FacingDeg,
			Power:      cast.Power,
			Shape:      cast.Shape,
			MaxTargets: cast.MaxTargets,
			Falloff:    cast.Falloff,
			LosMode:    cast.LosMode,
		})
	}
	resultIDs := make([]CastID, 0, len(r.results))
	for id := range r.results {
		resultIDs = append(resultIDs, id)
	}
	sort.Slice(resultIDs, func(i, j int) bool { return resultIDs[i] < resultIDs[j] })
	state.Results = make([]snapshotResult, 0, len(resultIDs))
	for _, id := range resultIDs {
		resolved := r.results[id]
		state.Results = append(state.Results, snapshotResult{
			CastID: id,
			Result: cloneResult(resolved.Result),
			Traces: cloneTraces(resolved.Traces),
		})
	}
	r.mu.Unlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(digest[:]),
		State:    payload,
	}
	return json.Marshal(envelope)
}

// Restore replaces the resolver state with a snapshot, all or nothing.
func (r *AoeTargetResolver) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || len(envelope.State) == 0 || envelope.Checksum == "" {
		return ErrBadSnapshot
	}
	digest := sha256.Sum256(envelope.State)
	if envelope.Checksum != hex.EncodeToString(digest[:]) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.State, &state); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshotState(state); err != nil {
		return ErrBadSnapshot
	}

	casts := make(map[CastID]*frozenCast, len(state.Casts))
	for _, saved := range state.Casts {
		casts[saved.ID] = &frozenCast{
			ID:         saved.ID,
			CasterID:   saved.CasterID,
			Origin:     saved.Origin,
			FacingDeg:  saved.FacingDeg,
			Power:      saved.Power,
			Shape:      saved.Shape,
			MaxTargets: saved.MaxTargets,
			Falloff:    saved.Falloff,
			LosMode:    saved.LosMode,
		}
	}
	results := make(map[CastID]*resolvedCast, len(state.Results))
	for _, saved := range state.Results {
		results[saved.CastID] = &resolvedCast{
			Result: cloneResult(saved.Result),
			Traces: cloneTraces(saved.Traces),
		}
	}
	active := 0
	for id := range casts {
		if _, resolved := results[id]; !resolved {
			active++
		}
	}

	r.mu.Lock()
	r.limits = state.Limits
	r.shape = state.Shape
	r.maxTargets = state.MaxTargets
	r.falloff = state.Falloff
	r.losMode = state.LosMode
	r.nextID = state.NextID
	r.active = active
	r.casts = casts
	r.results = results
	r.stats = state.Stats
	r.mu.Unlock()
	return nil
}

const (
	snapshotVersion = 1
	geometryEpsilon = 1e-9
)

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

type snapshotState struct {
	Limits     AoeLimits        `json:"limits"`
	Shape      AoeShape         `json:"shape"`
	MaxTargets int              `json:"max_targets"`
	Falloff    FalloffCurve     `json:"falloff"`
	LosMode    string           `json:"los_mode"`
	NextID     CastID           `json:"next_id"`
	Casts      []snapshotCast   `json:"casts"`
	Results    []snapshotResult `json:"results"`
	Stats      AoeStats         `json:"stats"`
}

type snapshotCast struct {
	ID         CastID       `json:"id"`
	CasterID   string       `json:"caster_id"`
	Origin     Vec2         `json:"origin"`
	FacingDeg  float64      `json:"facing_deg"`
	Power      int          `json:"power"`
	Shape      AoeShape     `json:"shape"`
	MaxTargets int          `json:"max_targets"`
	Falloff    FalloffCurve `json:"falloff"`
	LosMode    string       `json:"los_mode"`
}

type snapshotResult struct {
	CastID CastID        `json:"cast_id"`
	Result AoeResult     `json:"result"`
	Traces []TargetTrace `json:"traces"`
}

func validShape(s AoeShape) bool {
	switch s.Kind {
	case ShapeCircle:
		return positiveFinite(s.Radius)
	case ShapeSector:
		return positiveFinite(s.Radius) &&
			positiveFinite(s.AngleDeg) &&
			s.AngleDeg <= 360
	case ShapeRect:
		return positiveFinite(s.Length) && positiveFinite(s.Width)
	default:
		return false
	}
}

func positiveFinite(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func resolveCast(ctx context.Context, cast *frozenCast, world WorldView, limits AoeLimits) (AoeResult, []TargetTrace, error) {
	result := AoeResult{
		CastID: cast.ID,
		Origin: cast.Origin,
		Hits:   make([]AoeHit, 0),
	}
	candidates := make([]candidate, 0, len(world.Entities))
	for _, entity := range world.Entities {
		if err := ctx.Err(); err != nil {
			return AoeResult{}, nil, err
		}
		dx := entity.Pos.X - cast.Origin.X
		dy := entity.Pos.Y - cast.Origin.Y
		candidates = append(candidates, candidate{
			entity:   entity,
			distance: math.Hypot(dx, dy),
			angleDeg: relativeAngleDeg(cast.FacingDeg, dx, dy),
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.distance < right.distance {
			return true
		}
		if left.distance > right.distance {
			return false
		}
		if math.IsNaN(left.distance) && !math.IsNaN(right.distance) {
			return false
		}
		if !math.IsNaN(left.distance) && math.IsNaN(right.distance) {
			return true
		}
		return left.entity.ID < right.entity.ID
	})

	candidateLimit := limits.MaxCandidates
	if candidateLimit < 0 {
		candidateLimit = 0
	}
	if len(candidates) > candidateLimit {
		candidates = candidates[:candidateLimit]
	}
	if err := ctx.Err(); err != nil {
		return AoeResult{}, nil, err
	}

	traces := make([]TargetTrace, 0, len(candidates))
	hitLimit := cast.MaxTargets
	if hitLimit < 0 {
		hitLimit = 0
	}
	rayLimit := limits.MaxRays
	if rayLimit < 0 {
		rayLimit = 0
	}
	raysUsed := 0
	truncated := 0

	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			return AoeResult{}, nil, err
		}
		trace := TargetTrace{
			EntityID: cand.entity.ID,
			Distance: cand.distance,
			AngleDeg: cand.angleDeg,
		}

		if !insideShape(cast.Shape, cast.Origin, cast.FacingDeg, cand) {
			trace.Reason = TraceOutOfShape
			traces = append(traces, trace)
			continue
		}
		if raysUsed >= rayLimit {
			trace.Reason = TraceOverCandidate
			traces = append(traces, trace)
			continue
		}
		raysUsed++

		if blockedByLos(cast.Origin, cand.entity.Pos, world.Occluders, cast.LosMode) {
			trace.Reason = TraceBlocked
			traces = append(traces, trace)
			continue
		}
		if len(result.Hits) >= hitLimit {
			truncated++
			trace.Reason = TraceOverTargetCap
			traces = append(traces, trace)
			continue
		}

		scale := scalePermille(cast, cand.distance)
		result.Hits = append(result.Hits, AoeHit{
			EntityID:      cand.entity.ID,
			Distance:      cand.distance,
			ScalePermille: scale,
			Damage:        cast.Power * scale / ScaleFullPermille,
		})
		trace.Included = true
		trace.Reason = TraceInShape
		traces = append(traces, trace)
	}

	result.Truncated = truncated
	return result, traces, nil
}

func insideShape(shape AoeShape, origin Vec2, facingDeg float64, cand candidate) bool {
	switch shape.Kind {
	case ShapeCircle:
		return cand.distance <= shape.Radius+geometryEpsilon
	case ShapeSector:
		if cand.distance > shape.Radius+geometryEpsilon {
			return false
		}
		if cand.distance == 0 {
			return true
		}
		return cand.angleDeg <= shape.AngleDeg/2+geometryEpsilon
	case ShapeRect:
		facingRad := facingDeg * math.Pi / 180
		forwardX := math.Cos(facingRad)
		forwardY := math.Sin(facingRad)
		dx := cand.entity.Pos.X - origin.X
		dy := cand.entity.Pos.Y - origin.Y
		forward := dx*forwardX + dy*forwardY
		lateral := math.Abs(dx*forwardY - dy*forwardX)
		return forward >= -geometryEpsilon &&
			forward <= shape.Length+geometryEpsilon &&
			lateral <= shape.Width/2+geometryEpsilon
	default:
		return false
	}
}

func relativeAngleDeg(facingDeg, dx, dy float64) float64 {
	if dx == 0 && dy == 0 {
		return 0
	}
	facingRad := facingDeg * math.Pi / 180
	forwardX := math.Cos(facingRad)
	forwardY := math.Sin(facingRad)
	dot := dx*forwardX + dy*forwardY
	cross := math.Abs(forwardX*dy - forwardY*dx)
	angle := math.Atan2(cross, dot) * 180 / math.Pi
	if angle > 180 {
		angle = 180
	}
	return angle
}

func scalePermille(cast *frozenCast, distance float64) int {
	if cast.Falloff.Kind != FalloffLinear || distance <= 0 {
		return ScaleFullPermille
	}
	span := cast.Shape.Radius
	if cast.Shape.Kind == ShapeRect {
		span = cast.Shape.Length
	}
	floor := cast.Falloff.FloorPermille
	if floor < 0 {
		floor = 0
	}
	if floor > ScaleFullPermille {
		floor = ScaleFullPermille
	}
	if span <= 0 || math.IsNaN(span) || math.IsInf(span, 0) {
		return ScaleFullPermille
	}
	progress := distance / span
	if progress >= 1 {
		return floor
	}
	if progress <= 0 {
		return ScaleFullPermille
	}
	scale := int(math.Floor(float64(ScaleFullPermille) -
		float64(ScaleFullPermille-floor)*progress))
	if scale < floor {
		return floor
	}
	if scale > ScaleFullPermille {
		return ScaleFullPermille
	}
	return scale
}

func blockedByLos(origin, target Vec2, occluders []Occluder, mode string) bool {
	for _, occluder := range occluders {
		if mode == LosPenetrablePass && occluder.Penetrable {
			continue
		}
		if segmentIntersectsRect(origin, target, occluder) {
			return true
		}
	}
	return false
}

func segmentIntersectsRect(origin, target Vec2, occluder Occluder) bool {
	minX := math.Min(occluder.Min.X, occluder.Max.X)
	maxX := math.Max(occluder.Min.X, occluder.Max.X)
	minY := math.Min(occluder.Min.Y, occluder.Max.Y)
	maxY := math.Max(occluder.Min.Y, occluder.Max.Y)
	dx := target.X - origin.X
	dy := target.Y - origin.Y
	tMin := 0.0
	tMax := 1.0
	if !clipSegmentSlab(origin.X, dx, minX, maxX, &tMin, &tMax) {
		return false
	}
	if !clipSegmentSlab(origin.Y, dy, minY, maxY, &tMin, &tMax) {
		return false
	}
	return tMin <= tMax
}

func clipSegmentSlab(start, delta, minValue, maxValue float64, tMin, tMax *float64) bool {
	if delta == 0 {
		return start >= minValue && start <= maxValue
	}
	first := (minValue - start) / delta
	second := (maxValue - start) / delta
	if first > second {
		first, second = second, first
	}
	if first > *tMin {
		*tMin = first
	}
	if second < *tMax {
		*tMax = second
	}
	return *tMin <= *tMax
}

func cloneResult(source AoeResult) AoeResult {
	result := source
	result.Hits = cloneHits(source.Hits)
	return result
}

func cloneHits(source []AoeHit) []AoeHit {
	if source == nil {
		return nil
	}
	cloned := make([]AoeHit, len(source))
	copy(cloned, source)
	return cloned
}

func cloneTraces(source []TargetTrace) []TargetTrace {
	if source == nil {
		return nil
	}
	cloned := make([]TargetTrace, len(source))
	copy(cloned, source)
	return cloned
}

func validateSnapshotState(state snapshotState) error {
	if state.NextID < 1 {
		return ErrBadSnapshot
	}
	casts := make(map[CastID]struct{}, len(state.Casts))
	var maxID CastID
	for _, cast := range state.Casts {
		if cast.ID < 1 || strings.TrimSpace(cast.CasterID) == "" || cast.Power < 0 {
			return ErrBadSnapshot
		}
		if _, exists := casts[cast.ID]; exists {
			return ErrBadSnapshot
		}
		casts[cast.ID] = struct{}{}
		if cast.ID > maxID {
			maxID = cast.ID
		}
	}
	if state.NextID <= maxID {
		return ErrBadSnapshot
	}
	results := make(map[CastID]struct{}, len(state.Results))
	for _, resolved := range state.Results {
		if _, exists := casts[resolved.CastID]; !exists {
			return ErrBadSnapshot
		}
		if _, exists := results[resolved.CastID]; exists {
			return ErrBadSnapshot
		}
		if resolved.Result.CastID != resolved.CastID || resolved.Result.Truncated < 0 {
			return ErrBadSnapshot
		}
		results[resolved.CastID] = struct{}{}
	}
	if state.Stats.CastsSubmitted < 0 ||
		state.Stats.CastsResolved < 0 ||
		state.Stats.Candidates < 0 ||
		state.Stats.HitTotal < 0 ||
		state.Stats.DamageTotal < 0 ||
		state.Stats.HateTotal < 0 {
		return ErrBadSnapshot
	}
	return nil
}
