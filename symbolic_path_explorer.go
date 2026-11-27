// Package task schedules symbolic path exploration over a small block graph.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
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
	ErrSolverTimeout  = errors.New("solver timed out")
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
	byName  map[string]Block
	cfg     ExplorerConfig
	solver  Solver
	mu      sync.Mutex

	states       map[string]PathState
	queue        []string
	fingerprints map[string]string
	nextSeq      int64
	seeded       bool
	consumed     int
	stats        ExplorerStats
}

// NewSymbolicPathExplorer loads a program and the explorer limits.
func NewSymbolicPathExplorer(program []Block, cfg ExplorerConfig) *SymbolicPathExplorer {
	e := &SymbolicPathExplorer{
		program:      append([]Block(nil), program...),
		byName:       make(map[string]Block, len(program)),
		cfg:          cfg,
		states:       make(map[string]PathState),
		fingerprints: make(map[string]string),
		nextSeq:      1,
	}
	for i, block := range e.program {
		block.Covers = copyStrings(block.Covers)
		block.Guard = copyConstraints(block.Guard)
		block.Binds = copyAliases(block.Binds)
		block.Next = copyStrings(block.Next)
		e.program[i] = block
		e.byName[block.Name] = block
	}
	return e
}

// SetSolver installs the constraint solver consulted before a state is queued.
func (e *SymbolicPathExplorer) SetSolver(s Solver) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.solver = s
	return nil
}

// Seed creates the root state for one entry block.
func (e *SymbolicPathExplorer) Seed(entry string, args []Symbol) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return ErrBadEntry
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.seeded {
		return ErrAlreadySeeded
	}
	entryBlock, ok := e.byName[entry]
	if !ok {
		return ErrUnknownBlock
	}

	aliases := make([]Alias, 0, len(args)+len(entryBlock.Binds))
	for _, arg := range args {
		aliases = append(aliases, Alias{Name: arg.Name, Address: arg.Address})
	}
	aliases = append(aliases, entryBlock.Binds...)
	aliases = canonicalAliases(aliases)

	seq := e.nextSeq
	e.nextSeq++
	root := PathState{
		ID:          fmt.Sprintf("st-%d", seq),
		PC:          entry,
		Depth:       0,
		Constraints: canonicalConstraints(entryBlock.Guard),
		Aliases:     aliases,
		Status:      StatusPending,
		Seq:         seq,
	}
	e.states[root.ID] = root
	e.queue = append(e.queue, root.ID)
	e.fingerprints[dedupFingerprint(root.PC, root.Constraints, root.Aliases)] = root.ID
	e.seeded = true
	e.stats.Seeded++
	return nil
}

// Next reports the highest weighted pending state without dequeuing it.
func (e *SymbolicPathExplorer) Next() (PathState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var best PathState
	haveBest := false
	for _, id := range e.queue {
		st, ok := e.states[id]
		if !ok || st.Status != StatusPending {
			continue
		}
		if !haveBest || stateBefore(st, best) {
			best = st
			haveBest = true
		}
	}
	if !haveBest {
		return PathState{}, ErrNoPending
	}
	return cloneState(best), nil
}

// Step expands one state by up to budget blocks.
func (e *SymbolicPathExplorer) Step(stateID string, budget int) ([]PathState, error) {
	if budget <= 0 {
		return nil, ErrBadBudget
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	st, ok := e.states[stateID]
	if !ok {
		return nil, ErrUnknownState
	}
	if st.Status != StatusPending && st.Status != StatusSuspended {
		return nil, ErrStateFinished
	}

	states := cloneStates(e.states)
	queue := append([]string(nil), e.queue...)
	fingerprints := cloneStringMap(e.fingerprints)
	seq := e.nextSeq
	consumed := e.consumed
	stats := e.stats

	commit := func() {
		e.states = states
		e.queue = queue
		e.fingerprints = fingerprints
		e.nextSeq = seq
		e.consumed = consumed
		e.stats = stats
	}

	nextID := func() string {
		id := fmt.Sprintf("st-%d", seq)
		seq++
		return id
	}

	work := []string{stateID}
	frontier := make([]string, 0)
	expandedThisCall := 0

	for expandedThisCall < budget && len(work) > 0 {
		curID := work[0]
		work = work[1:]
		frontier = removeQueueID(frontier, curID)

		cur, exists := states[curID]
		if !exists {
			continue
		}

		if e.solver != nil && cur.Status == StatusSuspended {
			switch e.solver.Check(canonicalConstraints(cur.Constraints)) {
			case SatTimeout:
				cur.Status = StatusSuspended
				states[curID] = cur
				queue = removeQueueID(queue, curID)
				commit()
				return []PathState{cloneState(cur)}, nil
			case SatUnsatisfiable:
				if cur.Status != StatusPruned {
					cur.Status = StatusPruned
					states[curID] = cur
					queue = removeQueueID(queue, curID)
					stats.Pruned++
				}
				continue
			}
		}

		if consumed >= e.cfg.MaxExpansions {
			cur.Status = StatusSuspended
			states[curID] = cur
			queue = removeQueueID(queue, curID)
			if e.cfg.MaxExpansions > 0 && consumed == e.cfg.MaxExpansions {
				consumed++
			}
			commit()
			return []PathState{cloneState(cur)}, nil
		}

		block, blockOK := e.byName[cur.PC]
		if blockOK && len(block.Next) > 0 && cur.Depth+1 > e.cfg.MaxDepth {
			cur.Status = StatusPruned
			states[curID] = cur
			queue = removeQueueID(queue, curID)
			stats.Pruned++
			continue
		}

		consumed++
		expandedThisCall++
		stats.Expanded++
		cur.Status = StatusExpanded
		states[curID] = cur
		queue = removeQueueID(queue, curID)

		if !blockOK {
			continue
		}

		for _, target := range block.Next {
			childBlock, known := e.byName[target]
			if !known {
				continue
			}

			child := PathState{
				PC:          target,
				Depth:       cur.Depth + 1,
				Covers:      canonicalCovers(append(copyStrings(cur.Covers), block.Covers...)),
				Constraints: canonicalConstraints(append(copyConstraints(cur.Constraints), childBlock.Guard...)),
				Aliases:     canonicalAliases(append(copyAliases(cur.Aliases), childBlock.Binds...)),
				Cost:        cur.Cost + block.Cost,
				Status:      StatusPending,
			}
			fp := dedupFingerprint(child.PC, child.Constraints, child.Aliases)
			if _, duplicate := fingerprints[fp]; duplicate {
				stats.Deduped++
				continue
			}

			child.ID = nextID()
			child.Seq = seq - 1
			states[child.ID] = child
			fingerprints[fp] = child.ID

			if e.solver != nil {
				switch e.solver.Check(child.Constraints) {
				case SatTimeout:
					child.Status = StatusSuspended
					states[child.ID] = child
					frontier = append(frontier, child.ID)
					continue
				case SatUnsatisfiable:
					child.Status = StatusPruned
					states[child.ID] = child
					stats.Pruned++
					continue
				}
			}

			frontier = append(frontier, child.ID)
			work = append(work, child.ID)
			queue = append(queue, child.ID)
			if len(queue) > e.cfg.MaxPending {
				return nil, ErrPendingLimit
			}
		}
	}

	out := make([]PathState, 0, len(frontier))
	for _, id := range frontier {
		if s, exists := states[id]; exists {
			out = append(out, cloneState(s))
		}
	}
	commit()
	return out, nil
}

// Solve produces a witness for the constraints of one state.
func (e *SymbolicPathExplorer) Solve(stateID string) (Model, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	st, ok := e.states[stateID]
	if !ok {
		return Model{}, ErrUnknownState
	}
	if st.Status == StatusPruned {
		return Model{}, ErrStatePruned
	}

	if e.solver != nil {
		switch e.solver.Check(canonicalConstraints(st.Constraints)) {
		case SatUnsatisfiable:
			if st.Status != StatusPruned {
				st.Status = StatusPruned
				e.states[stateID] = st
				e.queue = removeQueueID(e.queue, stateID)
				e.stats.Pruned++
			}
			return Model{}, ErrUnsatisfiable
		case SatTimeout:
			st.Status = StatusSuspended
			e.states[stateID] = st
			e.queue = removeQueueID(e.queue, stateID)
			return Model{}, ErrSolverTimeout
		}
	}

	cons := canonicalConstraints(st.Constraints)
	values, err := solveConstraints(cons)
	if err != nil {
		if st.Status != StatusPruned {
			st.Status = StatusPruned
			e.states[stateID] = st
			e.queue = removeQueueID(e.queue, stateID)
			e.stats.Pruned++
		}
		return Model{}, err
	}

	return Model{
		StateID:     stateID,
		Constraints: copyConstraints(cons),
		Values:      values,
	}, nil
}

// Prune retires one state together with the reason.
func (e *SymbolicPathExplorer) Prune(stateID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrBadPruneReason
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	st, ok := e.states[stateID]
	if !ok {
		return ErrUnknownState
	}
	if st.Status == StatusPruned {
		return nil
	}
	st.Status = StatusPruned
	e.states[stateID] = st
	e.queue = removeQueueID(e.queue, stateID)
	e.stats.Pruned++
	return nil
}

// Merge folds several states that share program point and aliases.
func (e *SymbolicPathExplorer) Merge(stateIDs []string) (PathState, error) {
	if len(stateIDs) < 2 {
		return PathState{}, ErrMergeTooFew
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	sources := make([]PathState, 0, len(stateIDs))
	for _, id := range stateIDs {
		st, ok := e.states[id]
		if !ok {
			return PathState{}, ErrUnknownState
		}
		sources = append(sources, st)
	}

	pc := sources[0].PC
	aliases := canonicalAliases(sources[0].Aliases)
	for _, st := range sources[1:] {
		if st.PC != pc || !sameAliases(aliases, st.Aliases) {
			return PathState{}, ErrMergeMismatch
		}
	}

	covers := make([]string, 0)
	constraints := make([]Constraint, 0)
	cost := 0
	depth := 0
	for _, st := range sources {
		covers = append(covers, st.Covers...)
		constraints = append(constraints, st.Constraints...)
		cost += st.Cost
		if st.Depth > depth {
			depth = st.Depth
		}
	}

	nextQueue := append([]string(nil), e.queue...)
	for _, id := range stateIDs {
		nextQueue = removeQueueID(nextQueue, id)
	}
	if len(nextQueue)+1 > e.cfg.MaxPending {
		return PathState{}, ErrPendingLimit
	}

	merged := PathState{
		ID:          fmt.Sprintf("st-%d", e.nextSeq),
		PC:          pc,
		Depth:       depth,
		Covers:      canonicalCovers(covers),
		Constraints: canonicalConstraints(constraints),
		Aliases:     aliases,
		Cost:        cost,
		Status:      StatusPending,
		Seq:         e.nextSeq,
	}
	e.nextSeq++

	for _, id := range stateIDs {
		st := e.states[id]
		st.Status = StatusMerged
		e.states[id] = st
	}
	e.states[merged.ID] = merged
	e.queue = append(nextQueue, merged.ID)
	e.fingerprints[dedupFingerprint(merged.PC, merged.Constraints, merged.Aliases)] = merged.ID
	e.stats.Merged++
	return cloneState(merged), nil
}

// Pending reports how many states are waiting for expansion.
func (e *SymbolicPathExplorer) Pending() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.queue)
}

// State reads one state by identifier.
func (e *SymbolicPathExplorer) State(stateID string) (PathState, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.states[stateID]
	if !ok {
		return PathState{}, false
	}
	return cloneState(st), true
}

// States lists every known state ordered by its sequence number.
func (e *SymbolicPathExplorer) States() []PathState {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]PathState, 0, len(e.states))
	for _, st := range e.states {
		out = append(out, cloneState(st))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Stats reports the counters of the explorer.
func (e *SymbolicPathExplorer) Stats() ExplorerStats {
	e.mu.Lock()
	defer e.mu.Unlock()

	stats := e.stats
	stats.BudgetLeft = e.cfg.MaxExpansions - e.consumed
	if stats.BudgetLeft < 0 {
		stats.BudgetLeft = 0
	}
	return stats
}

const snapshotVersion = 1

type explorerSnapshot struct {
	Version  int         `json:"version"`
	States   []PathState `json:"states"`
	Queue    []string    `json:"queue"`
	NextSeq  int64       `json:"next_seq"`
	Seeded   int         `json:"seeded"`
	Expanded int         `json:"expanded"`
	Deduped  int         `json:"deduped"`
	Pruned   int         `json:"pruned"`
	Merged   int         `json:"merged"`
	Consumed int         `json:"consumed_expansions"`
}

// Snapshot serialises the states, the queue and the counters.
func (e *SymbolicPathExplorer) Snapshot() ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	states := make([]PathState, 0, len(e.states))
	for _, st := range e.states {
		states = append(states, cloneState(st))
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].Seq != states[j].Seq {
			return states[i].Seq < states[j].Seq
		}
		return states[i].ID < states[j].ID
	})

	p := explorerSnapshot{
		Version:  snapshotVersion,
		States:   states,
		Queue:    append([]string(nil), e.queue...),
		NextSeq:  e.nextSeq,
		Seeded:   e.stats.Seeded,
		Expanded: e.stats.Expanded,
		Deduped:  e.stats.Deduped,
		Pruned:   e.stats.Pruned,
		Merged:   e.stats.Merged,
		Consumed: e.consumed,
	}
	return json.Marshal(p)
}

// Restore replaces the whole exploration state, all or nothing.
func (e *SymbolicPathExplorer) Restore(data []byte) error {
	var p explorerSnapshot
	if err := json.Unmarshal(data, &p); err != nil {
		return ErrBadSnapshot
	}
	if p.Version != snapshotVersion || p.NextSeq <= 0 || p.Seeded < 0 ||
		p.Expanded < 0 || p.Deduped < 0 || p.Pruned < 0 || p.Merged < 0 || p.Consumed < 0 {
		return ErrBadSnapshot
	}

	states := make(map[string]PathState, len(p.States))
	seqs := make(map[int64]string, len(p.States))
	maxSeq := int64(0)
	maxID := int64(0)
	for _, st := range p.States {
		if st.ID == "" || st.Seq <= 0 || !validStatus(st.Status) {
			return ErrBadSnapshot
		}
		if _, duplicate := states[st.ID]; duplicate {
			return ErrBadSnapshot
		}
		if _, duplicate := seqs[st.Seq]; duplicate {
			return ErrBadSnapshot
		}
		st = cloneState(st)
		states[st.ID] = st
		seqs[st.Seq] = st.ID
		if st.Seq > maxSeq {
			maxSeq = st.Seq
		}
		if strings.HasPrefix(st.ID, "st-") {
			if n, err := strconv.ParseInt(strings.TrimPrefix(st.ID, "st-"), 10, 64); err == nil && n > maxID {
				maxID = n
			}
		}
	}

	queue := append([]string(nil), p.Queue...)
	seenQueue := make(map[string]bool, len(queue))
	for _, id := range queue {
		if seenQueue[id] {
			return ErrBadSnapshot
		}
		st, ok := states[id]
		if !ok || st.Status != StatusPending {
			return ErrBadSnapshot
		}
		seenQueue[id] = true
	}
	for _, st := range states {
		if st.Status == StatusPending && !seenQueue[st.ID] {
			return ErrBadSnapshot
		}
	}

	nextSeq := int64(p.NextSeq)
	if maxSeq+1 > nextSeq {
		nextSeq = maxSeq + 1
	}
	if maxID+1 > nextSeq {
		nextSeq = maxID + 1
	}
	if nextSeq < 1 {
		nextSeq = 1
	}

	fingerprints := make(map[string]string, len(states))
	sorted := make([]PathState, 0, len(states))
	for _, st := range states {
		sorted = append(sorted, st)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	for _, st := range sorted {
		fingerprints[dedupFingerprint(st.PC, st.Constraints, st.Aliases)] = st.ID
	}

	stats := ExplorerStats{
		Seeded:   p.Seeded,
		Expanded: p.Expanded,
		Deduped:  p.Deduped,
		Pruned:   p.Pruned,
		Merged:   p.Merged,
	}
	seeded := p.Seeded > 0 || len(states) > 0

	e.mu.Lock()
	defer e.mu.Unlock()
	e.states = states
	e.queue = queue
	e.fingerprints = fingerprints
	e.nextSeq = nextSeq
	e.consumed = p.Consumed
	e.seeded = seeded
	e.stats = stats
	return nil
}

func validStatus(status string) bool {
	switch status {
	case StatusPending, StatusSuspended, StatusExpanded, StatusPruned, StatusMerged:
		return true
	default:
		return false
	}
}

func stateBefore(a, b PathState) bool {
	aw := len(a.Covers)*10 + a.Depth
	bw := len(b.Covers)*10 + b.Depth
	if aw != bw {
		return aw > bw
	}
	if a.Depth != b.Depth {
		return a.Depth > b.Depth
	}
	return a.Seq < b.Seq
}

func removeQueueID(q []string, id string) []string {
	for i, v := range q {
		if v != id {
			continue
		}
		out := make([]string, 0, len(q)-1)
		out = append(out, q[:i]...)
		out = append(out, q[i+1:]...)
		return out
	}
	return q
}

func cloneState(st PathState) PathState {
	st.Covers = copyStrings(st.Covers)
	st.Constraints = copyConstraints(st.Constraints)
	st.Aliases = copyAliases(st.Aliases)
	return st
}

func cloneStates(states map[string]PathState) map[string]PathState {
	out := make(map[string]PathState, len(states))
	for id, st := range states {
		out[id] = cloneState(st)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyStrings(in []string) []string {
	return append([]string(nil), in...)
}

func copyConstraints(in []Constraint) []Constraint {
	return append([]Constraint(nil), in...)
}

func copyAliases(in []Alias) []Alias {
	return append([]Alias(nil), in...)
}

func canonicalCovers(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, cover := range in {
		if _, ok := seen[cover]; ok {
			continue
		}
		seen[cover] = struct{}{}
		out = append(out, cover)
	}
	sort.Strings(out)
	return out
}

func canonicalConstraints(in []Constraint) []Constraint {
	if len(in) == 0 {
		return nil
	}
	type key struct {
		symbol string
		op     string
		value  int64
	}
	seen := make(map[key]struct{}, len(in))
	out := make([]Constraint, 0, len(in))
	for _, c := range in {
		k := key{symbol: c.Symbol, op: c.Op, value: c.Value}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		if out[i].Op != out[j].Op {
			return out[i].Op < out[j].Op
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func canonicalAliases(in []Alias) []Alias {
	if len(in) == 0 {
		return nil
	}
	addresses := make(map[string]uint64, len(in))
	for _, a := range in {
		addresses[a.Name] = a.Address
	}
	names := make([]string, 0, len(addresses))
	for name := range addresses {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Alias, 0, len(names))
	for _, name := range names {
		out = append(out, Alias{Name: name, Address: addresses[name]})
	}
	return out
}

func sameAliases(a, b []Alias) bool {
	ca := canonicalAliases(a)
	cb := canonicalAliases(b)
	if len(ca) != len(cb) {
		return false
	}
	for i := range ca {
		if ca[i].Name != cb[i].Name || ca[i].Address != cb[i].Address {
			return false
		}
	}
	return true
}

func dedupFingerprint(pc string, cons []Constraint, aliases []Alias) string {
	var b strings.Builder
	b.WriteString(strconv.Quote(pc))
	for _, c := range canonicalConstraints(cons) {
		b.WriteString("|c:")
		b.WriteString(strconv.Quote(c.Symbol))
		b.WriteString(":")
		b.WriteString(strconv.Quote(c.Op))
		b.WriteString(":")
		b.WriteString(strconv.FormatInt(c.Value, 10))
	}
	for _, a := range canonicalAliases(aliases) {
		b.WriteString("|a:")
		b.WriteString(strconv.Quote(a.Name))
		b.WriteString(":")
		b.WriteString(strconv.FormatUint(a.Address, 10))
	}
	return b.String()
}

type constraintBounds struct {
	hasLower bool
	lower    int64
	hasUpper bool
	upper    int64
	hasEqual bool
	equal    int64
	notEqual map[int64]bool
}

func solveConstraints(cons []Constraint) (map[string]int64, error) {
	bounds := make(map[string]*constraintBounds)
	for _, c := range cons {
		cb, ok := bounds[c.Symbol]
		if !ok {
			cb = &constraintBounds{notEqual: make(map[int64]bool)}
			bounds[c.Symbol] = cb
		}
		switch c.Op {
		case ">=":
			if !cb.hasLower || c.Value > cb.lower {
				cb.lower = c.Value
				cb.hasLower = true
			}
		case ">":
			v := c.Value
			if v == math.MaxInt64 {
				return nil, ErrUnsatisfiable
			}
			v++
			if !cb.hasLower || v > cb.lower {
				cb.lower = v
				cb.hasLower = true
			}
		case "==", "=":
			if cb.hasEqual && cb.equal != c.Value {
				return nil, ErrUnsatisfiable
			}
			cb.hasEqual = true
			cb.equal = c.Value
		case "<=":
			if !cb.hasUpper || c.Value < cb.upper {
				cb.upper = c.Value
				cb.hasUpper = true
			}
		case "<":
			v := c.Value
			if v == math.MinInt64 {
				return nil, ErrUnsatisfiable
			}
			v--
			if !cb.hasUpper || v < cb.upper {
				cb.upper = v
				cb.hasUpper = true
			}
		case "!=":
			cb.notEqual[c.Value] = true
		default:
			return nil, ErrUnsatisfiable
		}
	}

	values := make(map[string]int64, len(bounds))
	for symbol, cb := range bounds {
		if cb.hasEqual {
			if cb.equal < 0 || cb.hasLower && cb.equal < cb.lower ||
				cb.hasUpper && cb.equal > cb.upper || cb.notEqual[cb.equal] {
				return nil, ErrUnsatisfiable
			}
			values[symbol] = cb.equal
			continue
		}

		candidate := int64(0)
		if cb.hasLower && cb.lower > candidate {
			candidate = cb.lower
		}
		if cb.hasUpper && candidate > cb.upper {
			return nil, ErrUnsatisfiable
		}
		for cb.notEqual[candidate] {
			if candidate == math.MaxInt64 {
				return nil, ErrUnsatisfiable
			}
			candidate++
			if cb.hasUpper && candidate > cb.upper {
				return nil, ErrUnsatisfiable
			}
		}
		values[symbol] = candidate
	}
	return values, nil
}
