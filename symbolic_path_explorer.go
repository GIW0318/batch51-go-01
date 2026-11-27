// Package task schedules symbolic path exploration over a small block graph.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Statuses carried by one path state.
const (
	StatusPending   = "pending"
	StatusSuspended = "suspended"
	StatusExpanded  = "expanded"
	StatusPruned    = "pruned"
	StatusMerged    = "merged"
)

// Answers a solver may give for a constraint set.
const (
	SatSatisfiable   = "sat"
	SatUnsatisfiable = "unsat"
	SatTimeout       = "timeout"
)

// Prune reason codes written into the audit trail.
const (
	PruneUnsat  = "unsat"
	PruneDepth  = "depth"
	PruneBudget = "budget"
)

// Errors reported by the explorer.
var (
	ErrBadEntry       = errors.New("entry block name is empty")
	ErrUnknownBlock   = errors.New("block is not part of the loaded program")
	ErrAlreadySeeded  = errors.New("program already seeded")
	ErrUnknownState   = errors.New("unknown path state")
	ErrStateFinished  = errors.New("path state is no longer expandable")
	ErrStatePruned    = errors.New("path state was pruned")
	ErrBadBudget      = errors.New("step budget must be positive")
	ErrPendingLimit   = errors.New("pending queue limit reached")
	ErrBadPruneReason = errors.New("prune reason is empty")
	ErrMergeTooFew    = errors.New("merge needs at least two states")
	ErrMergeMismatch  = errors.New("states cannot be merged")
	ErrNoPending      = errors.New("no state is waiting for expansion")
	ErrUnsatisfiable  = errors.New("path constraints are unsatisfiable")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Constraint is one branch condition on a symbolic argument.
type Constraint struct {
	Symbol string `json:"symbol"`
	Op     string `json:"op"`
	Value  int64  `json:"value"`
}

// Alias records where a symbolic pointer currently points.
type Alias struct {
	Name    string `json:"name"`
	Address uint64 `json:"address"`
}

// Symbol is one symbolic argument handed to the entry block.
type Symbol struct {
	Name    string `json:"name"`
	Address uint64 `json:"address"`
}

// Block is one basic block of the program under exploration.
type Block struct {
	Name   string       `json:"name"`
	Cost   int          `json:"cost"`
	Covers []string     `json:"covers"`
	Guard  []Constraint `json:"guard"`
	Binds  []Alias      `json:"binds"`
	Next   []string     `json:"next"`
}

// PathState is one queued exploration state.
type PathState struct {
	ID          string       `json:"id"`
	PC          string       `json:"pc"`
	Depth       int          `json:"depth"`
	Covers      []string     `json:"covers"`
	Constraints []Constraint `json:"constraints"`
	Aliases     []Alias      `json:"aliases"`
	Cost        int          `json:"cost"`
	Status      string       `json:"status"`
	Seq         int64        `json:"seq"`
}

// Model is the witness produced for a satisfiable path.
type Model struct {
	StateID     string           `json:"state_id"`
	Constraints []Constraint     `json:"constraints"`
	Values      map[string]int64 `json:"values"`
}

// ExplorerConfig bounds the live tables and the global budget.
type ExplorerConfig struct {
	MaxPending    int
	MaxDepth      int
	MaxExpansions int
}

// ExplorerStats reports the counters of the explorer.
type ExplorerStats struct {
	Seeded     int
	Expanded   int
	Deduped    int
	Pruned     int
	Merged     int
	BudgetLeft int
}

// Solver answers satisfiability questions for one constraint set.
type Solver interface {
	Check(cons []Constraint) string
}

// SymbolicPathExplorer owns the state table, the queue and the counters.
type SymbolicPathExplorer struct {
	program []Block
	cfg     ExplorerConfig
	solver  Solver
	mu      sync.Mutex
}

// NewSymbolicPathExplorer loads a program and the explorer limits.
func NewSymbolicPathExplorer(program []Block, cfg ExplorerConfig) *SymbolicPathExplorer {
	return &SymbolicPathExplorer{program: program, cfg: cfg}
}

// SetSolver installs the constraint solver consulted before a state is queued.
func (e *SymbolicPathExplorer) SetSolver(s Solver) error {
	return errNotImplemented
}

// Seed creates the root state for one entry block.
func (e *SymbolicPathExplorer) Seed(entry string, args []Symbol) error {
	return errNotImplemented
}

// Next reports the highest weighted pending state without dequeuing it.
func (e *SymbolicPathExplorer) Next() (PathState, error) {
	return PathState{}, errNotImplemented
}

// Step expands one state by up to budget blocks.
func (e *SymbolicPathExplorer) Step(stateID string, budget int) ([]PathState, error) {
	return nil, errNotImplemented
}

// Solve produces a witness for the constraints of one state.
func (e *SymbolicPathExplorer) Solve(stateID string) (Model, error) {
	return Model{}, errNotImplemented
}

// Prune retires one state together with the reason.
func (e *SymbolicPathExplorer) Prune(stateID, reason string) error {
	return errNotImplemented
}

// Merge folds several states that share program point and aliases.
func (e *SymbolicPathExplorer) Merge(stateIDs []string) (PathState, error) {
	return PathState{}, errNotImplemented
}

// Pending reports how many states are waiting for expansion.
func (e *SymbolicPathExplorer) Pending() int {
	return 0
}

// State reads one state by identifier.
func (e *SymbolicPathExplorer) State(stateID string) (PathState, bool) {
	return PathState{}, false
}

// States lists every known state ordered by its sequence number.
func (e *SymbolicPathExplorer) States() []PathState {
	return nil
}

// Stats reports the counters of the explorer.
func (e *SymbolicPathExplorer) Stats() ExplorerStats {
	return ExplorerStats{}
}

// Snapshot serialises the states, the queue and the counters.
func (e *SymbolicPathExplorer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole exploration state, all or nothing.
func (e *SymbolicPathExplorer) Restore(data []byte) error {
	return errNotImplemented
}
