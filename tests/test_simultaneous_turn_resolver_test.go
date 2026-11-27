package task

import (
	"errors"
	"reflect"
	"testing"

	impl "gsb/batch51-go-game-a-20"
)

func newResolver() *impl.SimultaneousTurnResolver {
	return impl.NewSimultaneousTurnResolver(impl.TurnConfig{
		MaxOrdersPerTurn: 8,
		MaxPathLen:       8,
		MaxResolvePasses: 3,
	})
}

func defaultPolicy() impl.CollisionPolicy {
	return impl.CollisionPolicy{PreferLowerCost: true, PreferHigherLevel: true, PreferLowerID: true}
}

func cell(x, y int) impl.Cell { return impl.Cell{X: x, Y: y} }

func unit(id string, c impl.Cell, level int) impl.UnitState {
	return impl.UnitState{ID: id, Cell: c, Level: level, Alive: true}
}

func mustPolicy(t *testing.T, r *impl.SimultaneousTurnResolver) {
	t.Helper()
	if err := r.SetCollisionPolicy(defaultPolicy()); err != nil {
		t.Fatalf("set collision policy: %v", err)
	}
}

func mustSubmit(t *testing.T, r *impl.SimultaneousTurnResolver, turn int, o impl.Order) impl.OrderID {
	t.Helper()
	id, err := r.SubmitOrder(turn, o)
	if err != nil {
		t.Fatalf("submit order for %s: %v", o.UnitID, err)
	}
	return id
}

func mustSeal(t *testing.T, r *impl.SimultaneousTurnResolver, turn int) {
	t.Helper()
	if err := r.SealTurn(turn); err != nil {
		t.Fatalf("seal turn %d: %v", turn, err)
	}
}

func mustResolve(t *testing.T, r *impl.SimultaneousTurnResolver, turn int, b impl.BoardView) impl.TurnResult {
	t.Helper()
	res, err := r.Resolve(turn, b)
	if err != nil {
		t.Fatalf("resolve turn %d: %v", turn, err)
	}
	return res
}

func moveOf(t *testing.T, res impl.TurnResult, unitID string) impl.UnitMove {
	t.Helper()
	for _, m := range res.Moves {
		if m.UnitID == unitID {
			return m
		}
	}
	t.Fatalf("no move recorded for %s in %+v", unitID, res.Moves)
	return impl.UnitMove{}
}

func TestSubmitOrderRejectsBlankUnit(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	if _, err := r.SubmitOrder(0, impl.Order{UnitID: "u-1", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}}); !errors.Is(err, impl.ErrBadOrder) {
		t.Fatalf("turn 0 must fail with ErrBadOrder, got %v", err)
	}
	if _, err := r.SubmitOrder(1, impl.Order{UnitID: "  ", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}}); !errors.Is(err, impl.ErrBadOrder) {
		t.Fatalf("blank unit must fail with ErrBadOrder, got %v", err)
	}
	if _, err := r.SubmitOrder(1, impl.Order{UnitID: "u-1", Kind: "teleport", Path: []impl.Cell{cell(0, 0), cell(1, 0)}}); !errors.Is(err, impl.ErrBadOrder) {
		t.Fatalf("unknown order kind must fail with ErrBadOrder, got %v", err)
	}
	if _, err := r.SubmitOrder(1, impl.Order{UnitID: "u-1", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}, ActionCost: -2}); !errors.Is(err, impl.ErrBadOrder) {
		t.Fatalf("negative action cost must fail with ErrBadOrder, got %v", err)
	}
}

func TestSubmitOrderRejectsBadPath(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	bad := [][]impl.Cell{
		nil,
		{cell(0, 0)},
		{cell(0, 0), cell(2, 0)},
		{cell(0, 0), cell(1, 0), cell(1, 2)},
	}
	for i, path := range bad {
		if _, err := r.SubmitOrder(1, impl.Order{UnitID: "u-1", Kind: impl.OrderMove, Path: path}); !errors.Is(err, impl.ErrBadPath) {
			t.Fatalf("path case %d must fail with ErrBadPath, got %v", i, err)
		}
	}
	tiny := impl.NewSimultaneousTurnResolver(impl.TurnConfig{MaxOrdersPerTurn: 8, MaxPathLen: 3, MaxResolvePasses: 2})
	mustPolicy(t, tiny)
	long := []impl.Cell{cell(0, 0), cell(1, 0), cell(2, 0), cell(3, 0), cell(4, 0)}
	if _, err := tiny.SubmitOrder(1, impl.Order{UnitID: "u-2", Kind: impl.OrderMove, Path: long}); !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("over long path must fail with ErrBadPath, got %v", err)
	}
}

func TestSubmitOrderRespectsTurnOrderCap(t *testing.T) {
	r := impl.NewSimultaneousTurnResolver(impl.TurnConfig{MaxOrdersPerTurn: 2, MaxPathLen: 8, MaxResolvePasses: 2})
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "u-1", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}})
	mustSubmit(t, r, 1, impl.Order{UnitID: "u-2", Kind: impl.OrderMove, Path: []impl.Cell{cell(5, 5), cell(5, 6)}})
	if _, err := r.SubmitOrder(1, impl.Order{UnitID: "u-3", Kind: impl.OrderMove, Path: []impl.Cell{cell(9, 9), cell(9, 8)}}); !errors.Is(err, impl.ErrOrderLimit) {
		t.Fatalf("third order must fail with ErrOrderLimit, got %v", err)
	}
	mustSeal(t, r, 1)
	res := mustResolve(t, r, 1, impl.BoardView{Units: []impl.UnitState{
		unit("u-1", cell(0, 0), 1),
		unit("u-2", cell(5, 5), 1),
	}})
	if len(res.Moves) != 2 {
		t.Fatalf("settled moves = %d, want the two accepted orders", len(res.Moves))
	}
}

func TestDuplicateUnitOrderRejected(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	first := mustSubmit(t, r, 1, impl.Order{UnitID: "u-1", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}})
	if _, err := r.SubmitOrder(1, impl.Order{UnitID: "u-1", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(0, 1)}}); !errors.Is(err, impl.ErrDuplicateOrder) {
		t.Fatalf("second order of one unit must fail with ErrDuplicateOrder, got %v", err)
	}
	if err := r.CancelOrder(1, first); err != nil {
		t.Fatalf("cancel order: %v", err)
	}
	if err := r.CancelOrder(1, first); !errors.Is(err, impl.ErrUnknownOrder) {
		t.Fatalf("repeated cancel must fail with ErrUnknownOrder, got %v", err)
	}
	mustSubmit(t, r, 1, impl.Order{UnitID: "u-1", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(0, 1)}})
}

func TestResolveRequiresSealedTurn(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "u-1", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}})
	b := impl.BoardView{Units: []impl.UnitState{unit("u-1", cell(0, 0), 1)}}
	if _, err := r.Resolve(1, b); !errors.Is(err, impl.ErrTurnOpen) {
		t.Fatalf("resolve before seal must fail with ErrTurnOpen, got %v", err)
	}
	mustSeal(t, r, 1)
	if err := r.SealTurn(1); !errors.Is(err, impl.ErrTurnClosed) {
		t.Fatalf("second seal must fail with ErrTurnClosed, got %v", err)
	}
	first := mustResolve(t, r, 1, b)
	second := mustResolve(t, r, 1, b)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated resolve = %+v, want the cached %+v", second, first)
	}
}

func TestResolveMovesUnitsToDestinations(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "beta", Kind: impl.OrderMove, Path: []impl.Cell{cell(5, 5), cell(5, 6)}})
	mustSubmit(t, r, 1, impl.Order{UnitID: "alpha", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}})
	mustSeal(t, r, 1)
	res := mustResolve(t, r, 1, impl.BoardView{Units: []impl.UnitState{
		unit("alpha", cell(0, 0), 1),
		unit("beta", cell(5, 5), 1),
	}})
	if len(res.Moves) != 2 {
		t.Fatalf("moves = %d, want 2", len(res.Moves))
	}
	if res.Moves[0].UnitID != "alpha" || res.Moves[1].UnitID != "beta" {
		t.Fatalf("move order = %s,%s, want alpha,beta", res.Moves[0].UnitID, res.Moves[1].UnitID)
	}
	alpha := moveOf(t, res, "alpha")
	if alpha.Status != impl.MoveMoved || alpha.From != cell(0, 0) || alpha.To != cell(1, 0) {
		t.Fatalf("alpha move = %+v, want moved 0,0 -> 1,0", alpha)
	}
	beta := moveOf(t, res, "beta")
	if beta.Status != impl.MoveMoved || beta.To != cell(5, 6) {
		t.Fatalf("beta move = %+v, want moved to 5,6", beta)
	}
	if res.Conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", res.Conflicts)
	}
}

func TestSameCellConflictLowerCostWins(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "slow", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}, ActionCost: 3})
	mustSubmit(t, r, 1, impl.Order{UnitID: "fast", Kind: impl.OrderMove, Path: []impl.Cell{cell(2, 0), cell(1, 0)}, ActionCost: 1})
	mustSeal(t, r, 1)
	b := impl.BoardView{Units: []impl.UnitState{
		unit("slow", cell(0, 0), 9),
		unit("fast", cell(2, 0), 1),
	}}
	res := mustResolve(t, r, 1, b)
	if got := moveOf(t, res, "fast"); got.Status != impl.MoveMoved || got.To != cell(1, 0) {
		t.Fatalf("fast move = %+v, want it to win the cell", got)
	}
	if got := moveOf(t, res, "slow"); got.Status != impl.MoveBlocked || got.To != cell(0, 0) {
		t.Fatalf("slow move = %+v, want it blocked at the origin", got)
	}
	if res.Conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", res.Conflicts)
	}
	traces := r.Explain(1)
	if len(traces) != 1 {
		t.Fatalf("traces = %+v, want exactly one conflict", traces)
	}
	if traces[0].Cell != cell(1, 0) || traces[0].Winner != "fast" || !reflect.DeepEqual(traces[0].Losers, []string{"slow"}) {
		t.Fatalf("trace = %+v, want fast to beat slow at 1,0", traces[0])
	}
}

func TestSameCellConflictHigherLevelWins(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "veteran", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}, ActionCost: 2})
	mustSubmit(t, r, 1, impl.Order{UnitID: "rookie", Kind: impl.OrderMove, Path: []impl.Cell{cell(2, 0), cell(1, 0)}, ActionCost: 2})
	mustSeal(t, r, 1)
	res := mustResolve(t, r, 1, impl.BoardView{Units: []impl.UnitState{
		unit("veteran", cell(0, 0), 5),
		unit("rookie", cell(2, 0), 2),
	}})
	if got := moveOf(t, res, "veteran"); got.Status != impl.MoveMoved {
		t.Fatalf("veteran move = %+v, want it to win on level", got)
	}
	if got := moveOf(t, res, "rookie"); got.Status != impl.MoveBlocked {
		t.Fatalf("rookie move = %+v, want it blocked", got)
	}
}

func TestSameCellConflictTieBrokenByUnitID(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "unit-b", Kind: impl.OrderMove, Path: []impl.Cell{cell(2, 0), cell(1, 0)}, ActionCost: 2})
	mustSubmit(t, r, 1, impl.Order{UnitID: "unit-a", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}, ActionCost: 2})
	mustSeal(t, r, 1)
	res := mustResolve(t, r, 1, impl.BoardView{Units: []impl.UnitState{
		unit("unit-a", cell(0, 0), 3),
		unit("unit-b", cell(2, 0), 3),
	}})
	if got := moveOf(t, res, "unit-a"); got.Status != impl.MoveMoved {
		t.Fatalf("unit-a move = %+v, want it to win the id tie break", got)
	}
	if got := moveOf(t, res, "unit-b"); got.Status != impl.MoveBlocked {
		t.Fatalf("unit-b move = %+v, want it blocked", got)
	}
}

func TestBlockedCellRejectsMove(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "u-1", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}})
	mustSeal(t, r, 1)
	b := impl.BoardView{
		Units:   []impl.UnitState{unit("u-1", cell(0, 0), 1)},
		Blocked: []impl.Cell{cell(1, 0)},
	}
	res := mustResolve(t, r, 1, b)
	got := moveOf(t, res, "u-1")
	if got.Status != impl.MoveBlocked || got.To != cell(0, 0) {
		t.Fatalf("blocked move = %+v, want the unit to stay at 0,0", got)
	}
}

func TestSwapBothSucceed(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "a", Kind: impl.OrderSwap, Path: []impl.Cell{cell(0, 0), cell(1, 0)}, ActionCost: 2})
	mustSubmit(t, r, 1, impl.Order{UnitID: "b", Kind: impl.OrderSwap, Path: []impl.Cell{cell(1, 0), cell(0, 0)}, ActionCost: 2})
	mustSeal(t, r, 1)
	res := mustResolve(t, r, 1, impl.BoardView{Units: []impl.UnitState{
		unit("a", cell(0, 0), 1),
		unit("b", cell(1, 0), 1),
	}})
	if got := moveOf(t, res, "a"); got.Status != impl.MoveSwapped || got.To != cell(1, 0) {
		t.Fatalf("a swap = %+v, want swapped to 1,0", got)
	}
	if got := moveOf(t, res, "b"); got.Status != impl.MoveSwapped || got.To != cell(0, 0) {
		t.Fatalf("b swap = %+v, want swapped to 0,0", got)
	}
	if res.Conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0 for a matched swap", res.Conflicts)
	}
}

func TestUnmatchedSwapFails(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "a", Kind: impl.OrderSwap, Path: []impl.Cell{cell(0, 0), cell(1, 0)}, ActionCost: 2})
	mustSeal(t, r, 1)
	res := mustResolve(t, r, 1, impl.BoardView{Units: []impl.UnitState{
		unit("a", cell(0, 0), 1),
		unit("b", cell(1, 0), 1),
	}})
	got := moveOf(t, res, "a")
	if got.Status != impl.MoveBlocked || got.To != cell(0, 0) {
		t.Fatalf("unmatched swap = %+v, want the unit to stay at 0,0", got)
	}
	if len(res.Moves) != 1 {
		t.Fatalf("moves = %+v, want only the ordered unit", res.Moves)
	}
}

func TestFollowIntoVacatedCellAllowed(t *testing.T) {
	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "a", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}})
	mustSubmit(t, r, 1, impl.Order{UnitID: "b", Kind: impl.OrderMove, Path: []impl.Cell{cell(1, 0), cell(1, 1)}})
	mustSeal(t, r, 1)
	res := mustResolve(t, r, 1, impl.BoardView{Units: []impl.UnitState{
		unit("a", cell(0, 0), 1),
		unit("b", cell(1, 0), 1),
	}})
	if got := moveOf(t, res, "a"); got.Status != impl.MoveMoved || got.To != cell(1, 0) {
		t.Fatalf("follower = %+v, want it to enter the vacated cell", got)
	}
	if got := moveOf(t, res, "b"); got.Status != impl.MoveMoved || got.To != cell(1, 1) {
		t.Fatalf("leaver = %+v, want it to leave the cell", got)
	}
	if res.Conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0 when the vacating unit leaves", res.Conflicts)
	}
}

func TestResolveDeterministicAcrossSubmitOrder(t *testing.T) {
	units := []impl.UnitState{
		unit("alpha", cell(0, 0), 3),
		unit("beta", cell(2, 0), 1),
		unit("gamma", cell(5, 5), 2),
	}
	orderA := impl.Order{UnitID: "alpha", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}, ActionCost: 2}
	orderB := impl.Order{UnitID: "beta", Kind: impl.OrderMove, Path: []impl.Cell{cell(2, 0), cell(1, 0)}, ActionCost: 2}
	orderC := impl.Order{UnitID: "gamma", Kind: impl.OrderMove, Path: []impl.Cell{cell(5, 5), cell(5, 6)}, ActionCost: 1}

	forward := newResolver()
	mustPolicy(t, forward)
	mustSubmit(t, forward, 1, orderA)
	mustSubmit(t, forward, 1, orderB)
	mustSubmit(t, forward, 1, orderC)
	mustSeal(t, forward, 1)
	first := mustResolve(t, forward, 1, impl.BoardView{Units: units})

	backward := newResolver()
	mustPolicy(t, backward)
	mustSubmit(t, backward, 1, orderC)
	mustSubmit(t, backward, 1, orderB)
	mustSubmit(t, backward, 1, orderA)
	mustSeal(t, backward, 1)
	second := mustResolve(t, backward, 1, impl.BoardView{Units: units})

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("settlement depends on submit order: %+v vs %+v", first, second)
	}
	if first.Conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1 from the alpha/beta clash", first.Conflicts)
	}
}

func TestSnapshotRestoreContinuesTurn(t *testing.T) {
	units := []impl.UnitState{
		unit("alpha", cell(0, 0), 2),
		unit("beta", cell(5, 5), 1),
	}
	control := newResolver()
	mustPolicy(t, control)
	mustSubmit(t, control, 1, impl.Order{UnitID: "alpha", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}})
	mustSubmit(t, control, 1, impl.Order{UnitID: "beta", Kind: impl.OrderMove, Path: []impl.Cell{cell(5, 5), cell(5, 6)}})
	mustSeal(t, control, 1)
	want := mustResolve(t, control, 1, impl.BoardView{Units: units})

	r := newResolver()
	mustPolicy(t, r)
	mustSubmit(t, r, 1, impl.Order{UnitID: "alpha", Kind: impl.OrderMove, Path: []impl.Cell{cell(0, 0), cell(1, 0)}})
	mustSubmit(t, r, 1, impl.Order{UnitID: "beta", Kind: impl.OrderMove, Path: []impl.Cell{cell(5, 5), cell(5, 6)}})
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newResolver()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	mustSeal(t, restored, 1)
	got := mustResolve(t, restored, 1, impl.BoardView{Units: units})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored settlement = %+v, want %+v", got, want)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
}
