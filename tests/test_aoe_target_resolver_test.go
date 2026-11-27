package task

import (
	"context"
	"errors"
	"reflect"
	"testing"

	impl "gsb/batch51-go-game-a-16"
)

func newResolver() *impl.AoeTargetResolver {
	return impl.NewAoeTargetResolver(impl.AoeLimits{MaxCandidates: 32, MaxRays: 32, MaxActiveCasts: 4})
}

func configure(t *testing.T, r *impl.AoeTargetResolver, shape impl.AoeShape, maxTargets int, f impl.FalloffCurve, mode string) {
	t.Helper()
	if err := r.SetShape(shape); err != nil {
		t.Fatalf("set shape: %v", err)
	}
	if err := r.SetMaxTargets(maxTargets); err != nil {
		t.Fatalf("set max targets: %v", err)
	}
	if err := r.SetFalloff(f); err != nil {
		t.Fatalf("set falloff: %v", err)
	}
	if err := r.SetLosMode(mode); err != nil {
		t.Fatalf("set los mode: %v", err)
	}
}

func mustCast(t *testing.T, r *impl.AoeTargetResolver, caster string, power int) impl.CastID {
	t.Helper()
	id, err := r.SubmitCast(impl.CastReq{CasterID: caster, Origin: impl.Vec2{X: 0, Y: 0}, FacingDeg: 0, Power: power})
	if err != nil {
		t.Fatalf("submit cast: %v", err)
	}
	return id
}

func mustResolve(t *testing.T, r *impl.AoeTargetResolver, id impl.CastID, world impl.WorldView) impl.AoeResult {
	t.Helper()
	res, err := r.Resolve(context.Background(), id, world)
	if err != nil {
		t.Fatalf("resolve cast %d: %v", id, err)
	}
	return res
}

func hitIDs(res impl.AoeResult) []string {
	ids := make([]string, len(res.Hits))
	for i, h := range res.Hits {
		ids[i] = h.EntityID
	}
	return ids
}

func TestSetShapeRejectsInvalidGeometry(t *testing.T) {
	r := newResolver()
	bad := []impl.AoeShape{
		{Kind: "sphere", Radius: 10},
		{Kind: impl.ShapeCircle, Radius: 0},
		{Kind: impl.ShapeCircle, Radius: -3},
		{Kind: impl.ShapeSector, Radius: 10, AngleDeg: 0},
		{Kind: impl.ShapeSector, Radius: 10, AngleDeg: 361},
		{Kind: impl.ShapeRect, Length: 10, Width: 0},
	}
	for i, shape := range bad {
		if err := r.SetShape(shape); !errors.Is(err, impl.ErrBadShape) {
			t.Fatalf("shape %d (%+v) must be rejected with ErrBadShape, got %v", i, shape, err)
		}
	}
	if err := r.SetShape(impl.AoeShape{Kind: impl.ShapeCircle, Radius: 6}); err != nil {
		t.Fatalf("a valid circle must be accepted, got %v", err)
	}
}

func TestSubmitCastAssignsMonotonicIDs(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 10}, 4, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	first := mustCast(t, r, "mage-1", 100)
	second := mustCast(t, r, "mage-1", 100)
	third := mustCast(t, r, "mage-2", 100)
	if first != 1 {
		t.Fatalf("first cast id = %d, want 1", first)
	}
	if second != first+1 || third != second+1 {
		t.Fatalf("cast ids must increase by one, got %d %d %d", first, second, third)
	}
	if got := r.Stats().CastsSubmitted; got != 3 {
		t.Fatalf("submitted counter = %d, want 3", got)
	}
}

func TestSubmitCastRejectsBlankCaster(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 10}, 4, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	bad := []impl.CastReq{
		{Origin: impl.Vec2{X: 0, Y: 0}, Power: 10},
		{CasterID: "   ", Origin: impl.Vec2{X: 0, Y: 0}, Power: 10},
		{CasterID: "mage-1", Origin: impl.Vec2{X: 0, Y: 0}, Power: -1},
	}
	for i, req := range bad {
		if _, err := r.SubmitCast(req); !errors.Is(err, impl.ErrBadCast) {
			t.Fatalf("cast request %d must be rejected with ErrBadCast, got %v", i, err)
		}
	}
	if got := r.Stats().CastsSubmitted; got != 0 {
		t.Fatalf("rejected casts must not be counted, got %d", got)
	}
}

func TestCircleShapeKeepsInsideTargets(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 10}, 8, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	id := mustCast(t, r, "mage-3", 100)
	res := mustResolve(t, r, id, impl.WorldView{Entities: []impl.Entity{
		{ID: "near", Pos: impl.Vec2{X: 3, Y: 0}},
		{ID: "far", Pos: impl.Vec2{X: 12, Y: 0}},
		{ID: "side", Pos: impl.Vec2{X: 0, Y: -9}},
	}})
	if got := hitIDs(res); !reflect.DeepEqual(got, []string{"near", "side"}) {
		t.Fatalf("circle hits = %v, want [near side]", got)
	}
	if res.Hits[0].Distance != 3 {
		t.Fatalf("first hit distance = %v, want 3", res.Hits[0].Distance)
	}
	if res.Hits[0].ScalePermille != impl.ScaleFullPermille || res.Hits[0].Damage != 100 {
		t.Fatalf("no falloff hit = %d permille / %d damage, want full scale and 100", res.Hits[0].ScalePermille, res.Hits[0].Damage)
	}
	if res.Truncated != 0 {
		t.Fatalf("truncated = %d, want 0", res.Truncated)
	}
}

func TestSectorShapeExcludesOutsideAngle(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeSector, Radius: 20, AngleDeg: 90}, 8, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	id := mustCast(t, r, "mage-4", 100)
	res := mustResolve(t, r, id, impl.WorldView{Entities: []impl.Entity{
		{ID: "front", Pos: impl.Vec2{X: 10, Y: 0}},
		{ID: "side", Pos: impl.Vec2{X: 0, Y: 10}},
		{ID: "behind", Pos: impl.Vec2{X: -10, Y: 0}},
	}})
	if got := hitIDs(res); !reflect.DeepEqual(got, []string{"front"}) {
		t.Fatalf("sector hits = %v, want only front", got)
	}
}

func TestRectShapeUsesCastFacing(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeRect, Length: 20, Width: 4}, 8, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	id, err := r.SubmitCast(impl.CastReq{CasterID: "mage-5", Origin: impl.Vec2{X: 0, Y: 0}, FacingDeg: 90, Power: 100})
	if err != nil {
		t.Fatalf("submit cast: %v", err)
	}
	res := mustResolve(t, r, id, impl.WorldView{Entities: []impl.Entity{
		{ID: "north", Pos: impl.Vec2{X: 0, Y: 15}},
		{ID: "east", Pos: impl.Vec2{X: 15, Y: 0}},
		{ID: "south", Pos: impl.Vec2{X: 0, Y: -6}},
	}})
	if got := hitIDs(res); !reflect.DeepEqual(got, []string{"north"}) {
		t.Fatalf("rect hits = %v, want only north for a 90 degree facing", got)
	}
}

func TestHitsSortedByDistanceThenEntityID(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 20}, 8, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	id := mustCast(t, r, "mage-6", 100)
	res := mustResolve(t, r, id, impl.WorldView{Entities: []impl.Entity{
		{ID: "zone-b", Pos: impl.Vec2{X: 0, Y: 4}},
		{ID: "first", Pos: impl.Vec2{X: 1, Y: 0}},
		{ID: "zone-a", Pos: impl.Vec2{X: 4, Y: 0}},
	}})
	want := []string{"first", "zone-a", "zone-b"}
	if got := hitIDs(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("hit order = %v, want %v", got, want)
	}
	if res.Hits[1].Distance != res.Hits[2].Distance {
		t.Fatalf("tied hits must report the same distance, got %v and %v", res.Hits[1].Distance, res.Hits[2].Distance)
	}
}

func TestMaxTargetsTruncatesFarthest(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 50}, 3, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	id := mustCast(t, r, "mage-7", 100)
	entities := make([]impl.Entity, 0, 5)
	for i := 1; i <= 5; i++ {
		entities = append(entities, impl.Entity{ID: "mob-" + string(rune('0'+i)), Pos: impl.Vec2{X: float64(i), Y: 0}})
	}
	res := mustResolve(t, r, id, impl.WorldView{Entities: entities})
	if got := hitIDs(res); !reflect.DeepEqual(got, []string{"mob-1", "mob-2", "mob-3"}) {
		t.Fatalf("truncated hits = %v, want the three nearest mobs", got)
	}
	if res.Truncated != 2 {
		t.Fatalf("truncated = %d, want 2", res.Truncated)
	}
}

func TestLinearFalloffScalesDamage(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 10}, 8, impl.FalloffCurve{Kind: impl.FalloffLinear, FloorPermille: 200}, impl.LosStrict)
	id := mustCast(t, r, "mage-8", 1000)
	res := mustResolve(t, r, id, impl.WorldView{Entities: []impl.Entity{
		{ID: "center", Pos: impl.Vec2{X: 0, Y: 0}},
		{ID: "half", Pos: impl.Vec2{X: 5, Y: 0}},
		{ID: "outer", Pos: impl.Vec2{X: 8, Y: 0}},
	}})
	wantScale := map[string]int{"center": 1000, "half": 600, "outer": 360}
	wantDamage := map[string]int{"center": 1000, "half": 600, "outer": 360}
	for _, hit := range res.Hits {
		if hit.ScalePermille != wantScale[hit.EntityID] {
			t.Fatalf("%s scale = %d, want %d", hit.EntityID, hit.ScalePermille, wantScale[hit.EntityID])
		}
		if hit.Damage != wantDamage[hit.EntityID] {
			t.Fatalf("%s damage = %d, want %d", hit.EntityID, hit.Damage, wantDamage[hit.EntityID])
		}
	}
	if len(res.Hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(res.Hits))
	}
}

func TestStrictLosDropsBlockedTarget(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 20}, 8, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	id := mustCast(t, r, "mage-9", 100)
	world := impl.WorldView{
		Entities: []impl.Entity{
			{ID: "blocked", Pos: impl.Vec2{X: 12, Y: 0}},
			{ID: "open", Pos: impl.Vec2{X: 0, Y: 12}},
		},
		Occluders: []impl.Occluder{{Min: impl.Vec2{X: 5, Y: -5}, Max: impl.Vec2{X: 6, Y: 5}}},
	}
	res := mustResolve(t, r, id, world)
	if got := hitIDs(res); !reflect.DeepEqual(got, []string{"open"}) {
		t.Fatalf("strict los hits = %v, want only open", got)
	}
	traces := r.Explain(id)
	blocked := false
	for _, tr := range traces {
		if tr.EntityID == "blocked" {
			blocked = true
			if tr.Included {
				t.Fatalf("blocked target must not be marked included")
			}
			if tr.Reason != impl.TraceBlocked {
				t.Fatalf("blocked target reason = %q, want %q", tr.Reason, impl.TraceBlocked)
			}
		}
	}
	if !blocked {
		t.Fatalf("explain must keep a trace for the blocked target, got %+v", traces)
	}
}

func TestResolveReusesFirstResultForMovedWorld(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 50}, 8, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	id := mustCast(t, r, "mage-10", 100)
	first := mustResolve(t, r, id, impl.WorldView{Entities: []impl.Entity{
		{ID: "mob-1", Pos: impl.Vec2{X: 1, Y: 0}},
		{ID: "mob-2", Pos: impl.Vec2{X: 2, Y: 0}},
	}})
	second := mustResolve(t, r, id, impl.WorldView{Entities: []impl.Entity{
		{ID: "mob-1", Pos: impl.Vec2{X: 40, Y: 40}},
		{ID: "mob-3", Pos: impl.Vec2{X: 3, Y: 0}},
	}})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("second resolve = %+v, want the frozen first result %+v", second, first)
	}
	if got := r.Stats().CastsResolved; got != 1 {
		t.Fatalf("resolved counter = %d, want 1 for a repeated resolve", got)
	}
}

func TestResolveUnknownCastFails(t *testing.T) {
	r := newResolver()
	if _, err := r.Resolve(context.Background(), impl.CastID(404), impl.WorldView{}); !errors.Is(err, impl.ErrUnknownCast) {
		t.Fatalf("unknown cast must fail with ErrUnknownCast, got %v", err)
	}
	if traces := r.Explain(impl.CastID(404)); len(traces) != 0 {
		t.Fatalf("unknown cast traces = %+v, want none", traces)
	}
}

func TestResolveHonorsCanceledContext(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 10}, 4, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	id := mustCast(t, r, "mage-11", 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Resolve(ctx, id, impl.WorldView{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context must fail with context.Canceled, got %v", err)
	}
	if got := r.Stats().CastsResolved; got != 0 {
		t.Fatalf("resolved counter = %d, want 0 after a canceled resolve", got)
	}
}

func TestCandidateCapKeepsNearest(t *testing.T) {
	r := impl.NewAoeTargetResolver(impl.AoeLimits{MaxCandidates: 3, MaxRays: 32, MaxActiveCasts: 4})
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 50}, 10, impl.FalloffCurve{Kind: impl.FalloffNone}, impl.LosStrict)
	id := mustCast(t, r, "mage-12", 100)
	entities := make([]impl.Entity, 0, 6)
	for i := 1; i <= 6; i++ {
		entities = append(entities, impl.Entity{ID: "mob-" + string(rune('0'+i)), Pos: impl.Vec2{X: float64(i), Y: 0}})
	}
	res := mustResolve(t, r, id, impl.WorldView{Entities: entities})
	if got := hitIDs(res); !reflect.DeepEqual(got, []string{"mob-1", "mob-2", "mob-3"}) {
		t.Fatalf("candidate cap hits = %v, want the three nearest mobs", got)
	}
	if len(r.Explain(id)) != 3 {
		t.Fatalf("candidate traces = %d, want the capped 3", len(r.Explain(id)))
	}
	if res.Truncated != 0 {
		t.Fatalf("truncated = %d, want 0 when only the candidate cap bites", res.Truncated)
	}
}

func TestSnapshotRestoreKeepsResolvedResult(t *testing.T) {
	r := newResolver()
	configure(t, r, impl.AoeShape{Kind: impl.ShapeCircle, Radius: 50}, 8, impl.FalloffCurve{Kind: impl.FalloffLinear, FloorPermille: 400}, impl.LosStrict)
	id := mustCast(t, r, "mage-13", 500)
	first := mustResolve(t, r, id, impl.WorldView{Entities: []impl.Entity{
		{ID: "mob-1", Pos: impl.Vec2{X: 5, Y: 0}},
		{ID: "mob-2", Pos: impl.Vec2{X: 25, Y: 0}},
	}})
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newResolver()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	second, err := restored.Resolve(context.Background(), id, impl.WorldView{})
	if err != nil {
		t.Fatalf("resolve after restore: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("restored resolve = %+v, want the frozen result %+v", second, first)
	}
	next := mustCast(t, restored, "mage-14", 500)
	if next <= id {
		t.Fatalf("cast id after restore = %d, must continue past %d", next, id)
	}
}
