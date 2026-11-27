// Package task schedules grammar preserving mutations for a parser target and
// keeps the per operator budget bookkeeping.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// MutationOp names one mutation operator.
type MutationOp string

// Supported mutation operators, in the fixed allocation order.
const (
	OpReplace  MutationOp = "replace"
	OpBoundary MutationOp = "boundary"
	OpSplice   MutationOp = "splice"
)

// Weight floor for operators that keep producing nothing new.
const MinWeight = 0.05

// Errors reported by the scheduler.
var (
	ErrNoGrammar        = errors.New("no grammar is loaded")
	ErrBadGrammar       = errors.New("grammar spec is invalid")
	ErrUnknownOp        = errors.New("mutation operator is not supported")
	ErrBadSeed          = errors.New("seed input does not belong to the grammar")
	ErrNoApplicableNode = errors.New("no node can carry this mutation")
	ErrBadInput         = errors.New("input is incomplete")
	ErrDuplicateInput   = errors.New("input fingerprint was already submitted")
	ErrBadBudget        = errors.New("batch budget must be positive")
	ErrNoSeeds          = errors.New("no seed inputs are registered")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Production is one grammar rule with one or more literal token alternatives.
type Production struct {
	Name         string     `json:"name"`
	Alternatives [][]string `json:"alternatives"`
}

// GrammarSpec is the declared grammar of one parser target.
type GrammarSpec struct {
	Target       string       `json:"target"`
	Root         string       `json:"root"`
	Productions  []Production `json:"productions"`
	NumericRules []string     `json:"numeric_rules"`
	Boundaries   []string     `json:"boundaries"`
	MaxDepth     int          `json:"max_depth"`
	MaxNodes     int          `json:"max_nodes"`
}

// Node is one node of a parse tree. Production nodes carry children, literal
// leaves carry a value.
type Node struct {
	Production string `json:"production"`
	Value      string `json:"value"`
	Children   []Node `json:"children,omitempty"`
}

// Input is one candidate that can be handed to the target parser.
type Input struct {
	Target      string     `json:"target"`
	Op          MutationOp `json:"op"`
	Tree        Node       `json:"tree"`
	Fingerprint string     `json:"fingerprint"`
}

// Feedback is what the target parser reported for one submitted input.
type Feedback struct {
	Parsed        bool `json:"parsed"`
	SemanticError bool `json:"semantic_error"`
	NewCoverage   int  `json:"new_coverage"`
}

// OpStats is the per operator ledger.
type OpStats struct {
	Op        MutationOp `json:"op"`
	Submitted int        `json:"submitted"`
	Accepted  int        `json:"accepted"`
	Gain      int        `json:"gain"`
	ZeroGain  int        `json:"zero_gain"`
	Weight    float64    `json:"weight"`
}

// BudgetState counts generated, issued and submitted candidates.
type BudgetState struct {
	Generated int `json:"generated"`
	Issued    int `json:"issued"`
	Submitted int `json:"submitted"`
}

// GrammarMutationScheduler owns the grammar, the seed pool, the operator
// ledgers and the issued fingerprints.
type GrammarMutationScheduler struct {
	mu sync.Mutex
}

// NewGrammarMutationScheduler builds an empty scheduler.
func NewGrammarMutationScheduler() *GrammarMutationScheduler {
	return &GrammarMutationScheduler{}
}

// LoadGrammar validates and installs one grammar.
func (s *GrammarMutationScheduler) LoadGrammar(spec GrammarSpec) error {
	return errNotImplemented
}

// Grammar returns the installed grammar.
func (s *GrammarMutationScheduler) Grammar() GrammarSpec {
	return GrammarSpec{}
}

// AddSeed registers one well formed input as a mutation seed.
func (s *GrammarMutationScheduler) AddSeed(in Input) error {
	return errNotImplemented
}

// Mutate applies one operator to a seed and returns a fresh input.
func (s *GrammarMutationScheduler) Mutate(seed Input, op MutationOp) (Input, error) {
	return Input{}, errNotImplemented
}

// Fingerprint returns the canonical identity of one input tree.
func (s *GrammarMutationScheduler) Fingerprint(in Input) string {
	return ""
}

// Submit records the parser feedback of one input.
func (s *GrammarMutationScheduler) Submit(input Input, feedback Feedback) error {
	return errNotImplemented
}

// Score returns the current allocation weight of one operator.
func (s *GrammarMutationScheduler) Score(op MutationOp) (float64, error) {
	return 0, errNotImplemented
}

// Stats returns the ledger of one operator.
func (s *GrammarMutationScheduler) Stats(op MutationOp) (OpStats, error) {
	return OpStats{}, errNotImplemented
}

// Next produces a batch of fresh inputs within the given budget.
func (s *GrammarMutationScheduler) Next(budget int) ([]Input, error) {
	return nil, errNotImplemented
}

// Ledger lists submitted fingerprints, oldest first.
func (s *GrammarMutationScheduler) Ledger() []string {
	return nil
}

// Budget reports how many candidates were produced, issued and submitted.
func (s *GrammarMutationScheduler) Budget() (BudgetState, error) {
	return BudgetState{}, errNotImplemented
}

// Snapshot serialises the grammar, the ledgers and the fingerprint sets.
func (s *GrammarMutationScheduler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *GrammarMutationScheduler) Restore(data []byte) error {
	return errNotImplemented
}
