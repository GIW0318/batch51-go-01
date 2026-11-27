// Package task resolves simultaneous turns of a tactical board: it collects
// the orders, seals the turn and settles every move in one deterministic pass
// with conflict arbitration and swap handling.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Order kinds.
const (
	OrderMove = "move"
	OrderSwap = "swap"
	OrderHold = "hold"
)

// Unit move statuses.
const (
	MoveMoved   = "moved"
	MoveBlocked = "blocked"
	MoveSwapped = "swapped"
	MoveVoid    = "void"
)

// Conflict reasons reported by Explain.
const (
	ReasonSameCell      = "same_cell"
	ReasonBlockedCell   = "blocked_cell"
	ReasonSwapUnmatched = "swap_unmatched"
	ReasonUnitDown      = "unit_down"
)

// Errors reported by the resolver.
var (
	ErrBadOrder       = errors.New("order is invalid")
	ErrBadPath        = errors.New("order path is invalid")
	ErrDuplicateOrder = errors.New("unit already ordered this turn")
	ErrOrderLimit     = errors.New("turn order cap reached")
	ErrTurnSealed     = errors.New("turn is sealed")
	ErrTurnOpen       = errors.New("turn is not sealed")
	ErrTurnClosed     = errors.New("turn is already resolved")
	ErrUnknownOrder   = errors.New("unknown order")
	ErrBadPolicy      = errors.New("collision policy is invalid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Cell is one square of the board.
type Cell struct {
	X int
	Y int
}

// OrderID is the monotonic identifier of one submitted order.
type OrderID int64

// Order is one unit order of a turn.
type Order struct {
	UnitID     string
	Kind       string
	Path       []Cell
	ActionCost int
}

// UnitState is the board view of one unit at settlement time.
type UnitState struct {
	ID    string
	Cell  Cell
	Level int
	Alive bool
}

// BoardView is the board snapshot passed to one settlement.
type BoardView struct {
	Units   []UnitState
	Blocked []Cell
}

// CollisionPolicy selects the arbitration precedence.
type CollisionPolicy struct {
	PreferLowerCost   bool
	PreferHigherLevel bool
	PreferLowerID     bool
}

// UnitMove is the settled move of one unit.
type UnitMove struct {
	UnitID string
	From   Cell
	To     Cell
	Status string
}

// TurnResult is the deterministic outcome of one turn.
type TurnResult struct {
	Turn      int
	Moves     []UnitMove
	Conflicts int
}

// ConflictTrace explains one conflict of a settled turn.
type ConflictTrace struct {
	Cell   Cell
	Winner string
	Losers []string
	Reason string
}

// TurnStats aggregates the turn counters.
type TurnStats struct {
	Submitted int
	Sealed    int
	Resolved  int
	Cancelled int
	Conflicts int
}

// TurnConfig bounds the resolver.
type TurnConfig struct {
	MaxOrdersPerTurn int
	MaxPathLen       int
	MaxResolvePasses int
}

// SimultaneousTurnResolver owns the pending orders and the settled turns.
type SimultaneousTurnResolver struct {
	cfg TurnConfig
}

// NewSimultaneousTurnResolver builds a resolver with the given caps.
func NewSimultaneousTurnResolver(cfg TurnConfig) *SimultaneousTurnResolver {
	return &SimultaneousTurnResolver{cfg: cfg}
}

// SubmitOrder records one order of one open turn.
func (r *SimultaneousTurnResolver) SubmitOrder(turn int, order Order) (OrderID, error) {
	return 0, errNotImplemented
}

// SealTurn closes the order collection of one turn.
func (r *SimultaneousTurnResolver) SealTurn(turn int) error {
	return errNotImplemented
}

// Resolve settles one sealed turn exactly once.
func (r *SimultaneousTurnResolver) Resolve(turn int, board BoardView) (TurnResult, error) {
	return TurnResult{}, errNotImplemented
}

// SetCollisionPolicy installs the arbitration precedence.
func (r *SimultaneousTurnResolver) SetCollisionPolicy(p CollisionPolicy) error {
	return errNotImplemented
}

// CancelOrder withdraws one order of an open turn.
func (r *SimultaneousTurnResolver) CancelOrder(turn int, id OrderID) error {
	return errNotImplemented
}

// Explain lists the conflicts of the last settlement of one turn.
func (r *SimultaneousTurnResolver) Explain(turn int) []ConflictTrace {
	return nil
}

// Stats returns a copy of the turn counters.
func (r *SimultaneousTurnResolver) Stats() TurnStats {
	return TurnStats{}
}

// Snapshot serialises the pending orders, the seals and the results.
func (r *SimultaneousTurnResolver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the resolver state with a snapshot, all or nothing.
func (r *SimultaneousTurnResolver) Restore(data []byte) error {
	return errNotImplemented
}
