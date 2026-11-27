// Package task schedules grammar preserving mutations for a parser target and
// keeps the per operator budget bookkeeping.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sync"
)

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

	loaded      bool
	grammar     GrammarSpec
	productions map[string]Production
	numeric     map[string]bool

	seeds []Input

	// Operator statistics are kept per parser target so that a grammar reload
	// cannot make one target borrow another target's weights.
	stats map[string]map[MutationOp]OpStats

	issued    map[string]struct{}
	submitted map[string]struct{}
	ledger    []string
	budget    BudgetState
}

// NewGrammarMutationScheduler builds an empty scheduler.
func NewGrammarMutationScheduler() *GrammarMutationScheduler {
	return &GrammarMutationScheduler{
		stats:     make(map[string]map[MutationOp]OpStats),
		issued:    make(map[string]struct{}),
		submitted: make(map[string]struct{}),
	}
}

// LoadGrammar validates and installs one grammar.
func (s *GrammarMutationScheduler) LoadGrammar(spec GrammarSpec) error {
	normalized, productions, err := normalizeGrammar(spec)
	if err != nil {
		return err
	}

	numeric := make(map[string]bool, len(normalized.NumericRules))
	for _, name := range normalized.NumericRules {
		numeric[name] = true
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	s.loaded = true
	s.grammar = normalized
	s.productions = productions
	s.numeric = numeric
	if s.stats[normalized.Target] == nil {
		s.stats[normalized.Target] = make(map[MutationOp]OpStats)
	}
	return nil
}

// Grammar returns the installed grammar.
func (s *GrammarMutationScheduler) Grammar() GrammarSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneGrammar(s.grammar)
}

// AddSeed registers one well formed input as a mutation seed.
func (s *GrammarMutationScheduler) AddSeed(in Input) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	if !s.loaded {
		return ErrNoGrammar
	}
	if err := s.validateSeedLocked(in); err != nil {
		return err
	}
	s.seeds = append(s.seeds, cloneInput(in))
	return nil
}

// Mutate applies one operator to a seed and returns a fresh input.
func (s *GrammarMutationScheduler) Mutate(seed Input, op MutationOp) (Input, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	if !s.loaded {
		return Input{}, ErrNoGrammar
	}
	if !knownOp(op) {
		return Input{}, ErrUnknownOp
	}
	if err := s.validateSeedLocked(seed); err != nil {
		return Input{}, err
	}

	tree, err := s.mutateTreeLocked(seed.Tree, op)
	if err != nil {
		return Input{}, err
	}
	out := Input{
		Target: s.grammar.Target,
		Op:     op,
		Tree:   tree,
	}
	out.Fingerprint = s.Fingerprint(out)
	s.budget.Generated++
	return out, nil
}

// Fingerprint returns the canonical identity of one input tree.
func (s *GrammarMutationScheduler) Fingerprint(in Input) string {
	data, err := json.Marshal(in.Tree)
	if err != nil {
		data = []byte{}
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Submit records the parser feedback of one input.
func (s *GrammarMutationScheduler) Submit(input Input, feedback Feedback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()

	if input.Fingerprint == "" || !knownOp(input.Op) {
		return ErrBadInput
	}
	if _, exists := s.submitted[input.Fingerprint]; exists {
		return ErrDuplicateInput
	}

	target := input.Target
	if target == "" && s.loaded {
		target = s.grammar.Target
	}
	if s.stats[target] == nil {
		s.stats[target] = make(map[MutationOp]OpStats)
	}
	stat := s.stats[target][input.Op]
	stat.Op = input.Op
	stat.Submitted++
	if feedback.Parsed {
		stat.Accepted++
	}
	if feedback.NewCoverage > 0 {
		stat.Gain += feedback.NewCoverage
		stat.ZeroGain = 0
	} else {
		stat.ZeroGain++
	}
	stat.Weight = weightFor(stat)
	s.stats[target][input.Op] = stat

	s.submitted[input.Fingerprint] = struct{}{}
	s.ledger = append(s.ledger, input.Fingerprint)
	s.budget.Submitted++
	return nil
}

// Score returns the current allocation weight of one operator.
func (s *GrammarMutationScheduler) Score(op MutationOp) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	if !knownOp(op) {
		return 0, ErrUnknownOp
	}
	return s.weightLocked(op), nil
}

// Stats returns the ledger of one operator.
func (s *GrammarMutationScheduler) Stats(op MutationOp) (OpStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	if !knownOp(op) {
		return OpStats{}, ErrUnknownOp
	}
	stat := s.statLocked(op)
	stat.Op = op
	stat.Weight = weightFor(stat)
	return stat, nil
}

// Next produces a batch of fresh inputs within the given budget.
func (s *GrammarMutationScheduler) Next(budget int) ([]Input, error) {
	if budget <= 0 {
		return nil, ErrBadBudget
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()

	if !s.loaded {
		if len(s.seeds) == 0 {
			return nil, ErrNoSeeds
		}
		return nil, ErrNoGrammar
	}
	seeds := make([]Input, 0, len(s.seeds))
	for _, seed := range s.seeds {
		if seed.Target == s.grammar.Target && s.validateSeedLocked(seed) == nil {
			seeds = append(seeds, cloneInput(seed))
		}
	}
	if len(seeds) == 0 {
		return nil, ErrNoSeeds
	}

	weights := make([]float64, len(orderedOps))
	for i, op := range orderedOps {
		weights[i] = s.weightLocked(op)
	}
	quotas := allocateQuotas(weights, budget)

	batch := make([]Input, 0)
	for i, op := range orderedOps {
		batch = append(batch, s.generateLocked(seeds, op, quotas[i])...)
	}
	s.budget.Issued += len(batch)
	return batch, nil
}

// Ledger lists submitted fingerprints, oldest first.
func (s *GrammarMutationScheduler) Ledger() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.ledger))
	copy(out, s.ledger)
	return out
}

// Budget reports how many candidates were produced, issued and submitted.
func (s *GrammarMutationScheduler) Budget() (BudgetState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget, nil
}

// Snapshot serialises the grammar, the ledgers and the fingerprint sets.
func (s *GrammarMutationScheduler) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()

	payload := snapshotPayload{
		Magic:     snapshotMagic,
		Version:   snapshotVersion,
		Loaded:    s.loaded,
		Grammar:   cloneGrammar(s.grammar),
		Seeds:     cloneInputs(s.seeds),
		Issued:    sortedSet(s.issued),
		Submitted: sortedSet(s.submitted),
		Ledger:    append([]string(nil), s.ledger...),
		Budget:    s.budget,
	}

	targets := make([]string, 0, len(s.stats))
	for target := range s.stats {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		entry := targetStats{Target: target}
		for _, op := range orderedOps {
			stat := s.stats[target][op]
			stat.Op = op
			stat.Weight = weightFor(stat)
			entry.Ops = append(entry.Ops, stat)
		}
		payload.Stats = append(payload.Stats, entry)
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *GrammarMutationScheduler) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Magic != snapshotMagic || payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	restored := &GrammarMutationScheduler{
		loaded:    payload.Loaded,
		grammar:   cloneGrammar(payload.Grammar),
		seeds:     cloneInputs(payload.Seeds),
		stats:     make(map[string]map[MutationOp]OpStats),
		issued:    make(map[string]struct{}, len(payload.Issued)),
		submitted: make(map[string]struct{}, len(payload.Submitted)),
		ledger:    append([]string(nil), payload.Ledger...),
		budget:    payload.Budget,
	}

	if restored.loaded {
		normalized, productions, err := normalizeGrammar(restored.grammar)
		if err != nil {
			return ErrBadSnapshot
		}
		restored.grammar = normalized
		restored.productions = productions
		restored.numeric = make(map[string]bool, len(normalized.NumericRules))
		for _, name := range normalized.NumericRules {
			restored.numeric[name] = true
		}
		for _, seed := range restored.seeds {
			if restored.validateSeedLocked(seed) != nil {
				return ErrBadSnapshot
			}
		}
	} else if len(restored.seeds) != 0 {
		return ErrBadSnapshot
	}

	for _, entry := range payload.Stats {
		if _, duplicate := restored.stats[entry.Target]; duplicate {
			return ErrBadSnapshot
		}
		ops := make(map[MutationOp]OpStats)
		for _, stat := range entry.Ops {
			if !knownOp(stat.Op) {
				return ErrBadSnapshot
			}
			if _, duplicate := ops[stat.Op]; duplicate {
				return ErrBadSnapshot
			}
			if stat.Submitted < 0 || stat.Accepted < 0 || stat.Gain < 0 ||
				stat.ZeroGain < 0 || stat.Accepted > stat.Submitted ||
				stat.ZeroGain > stat.Submitted {
				return ErrBadSnapshot
			}
			expected := weightFor(stat)
			if math.Abs(expected-stat.Weight) > 0.0000001 {
				return ErrBadSnapshot
			}
			stat.Weight = expected
			ops[stat.Op] = stat
		}
		for _, op := range orderedOps {
			if _, exists := ops[op]; !exists {
				ops[op] = OpStats{Op: op, Weight: 1}
			}
		}
		restored.stats[entry.Target] = ops
	}

	for _, fingerprint := range payload.Issued {
		if fingerprint == "" {
			return ErrBadSnapshot
		}
		if _, duplicate := restored.issued[fingerprint]; duplicate {
			return ErrBadSnapshot
		}
		restored.issued[fingerprint] = struct{}{}
	}
	for _, fingerprint := range payload.Submitted {
		if fingerprint == "" {
			return ErrBadSnapshot
		}
		if _, duplicate := restored.submitted[fingerprint]; duplicate {
			return ErrBadSnapshot
		}
		restored.submitted[fingerprint] = struct{}{}
	}

	if payload.Budget.Generated < 0 || payload.Budget.Issued < 0 || payload.Budget.Submitted < 0 {
		return ErrBadSnapshot
	}
	if payload.Budget.Issued != len(restored.issued) ||
		payload.Budget.Submitted != len(restored.ledger) ||
		len(restored.submitted) != len(restored.ledger) {
		return ErrBadSnapshot
	}
	totalSubmitted := 0
	for _, ops := range restored.stats {
		for _, stat := range ops {
			totalSubmitted += stat.Submitted
		}
	}
	if totalSubmitted != payload.Budget.Submitted {
		return ErrBadSnapshot
	}
	seenLedger := make(map[string]struct{}, len(restored.ledger))
	for _, fingerprint := range restored.ledger {
		if fingerprint == "" {
			return ErrBadSnapshot
		}
		if _, duplicate := seenLedger[fingerprint]; duplicate {
			return ErrBadSnapshot
		}
		seenLedger[fingerprint] = struct{}{}
		if _, exists := restored.submitted[fingerprint]; !exists {
			return ErrBadSnapshot
		}
	}

	s.mu.Lock()
	s.loaded = restored.loaded
	s.grammar = restored.grammar
	s.productions = restored.productions
	s.numeric = restored.numeric
	s.seeds = restored.seeds
	s.stats = restored.stats
	s.issued = restored.issued
	s.submitted = restored.submitted
	s.ledger = restored.ledger
	s.budget = restored.budget
	s.mu.Unlock()
	return nil
}

var orderedOps = []MutationOp{OpReplace, OpBoundary, OpSplice}

const (
	snapshotMagic   = "gsb/grammar-mutation-scheduler"
	snapshotVersion = 1
)

type snapshotPayload struct {
	Magic     string        `json:"magic"`
	Version   int           `json:"version"`
	Loaded    bool          `json:"loaded"`
	Grammar   GrammarSpec   `json:"grammar"`
	Seeds     []Input       `json:"seeds"`
	Stats     []targetStats `json:"stats"`
	Issued    []string      `json:"issued"`
	Submitted []string      `json:"submitted"`
	Ledger    []string      `json:"ledger"`
	Budget    BudgetState   `json:"budget"`
}

type targetStats struct {
	Target string    `json:"target"`
	Ops    []OpStats `json:"ops"`
}

func knownOp(op MutationOp) bool {
	return op == OpReplace || op == OpBoundary || op == OpSplice
}

func (s *GrammarMutationScheduler) ensureLocked() {
	if s.stats == nil {
		s.stats = make(map[string]map[MutationOp]OpStats)
	}
	if s.issued == nil {
		s.issued = make(map[string]struct{})
	}
	if s.submitted == nil {
		s.submitted = make(map[string]struct{})
	}
}

func normalizeGrammar(spec GrammarSpec) (GrammarSpec, map[string]Production, error) {
	if spec.Root == "" || len(spec.Productions) == 0 || spec.MaxDepth < 0 || spec.MaxNodes < 0 {
		return GrammarSpec{}, nil, ErrBadGrammar
	}

	normalized := cloneGrammar(spec)
	productions := make(map[string]Production, len(normalized.Productions))
	for _, production := range normalized.Productions {
		if production.Name == "" || len(production.Alternatives) == 0 {
			return GrammarSpec{}, nil, ErrBadGrammar
		}
		if _, exists := productions[production.Name]; exists {
			return GrammarSpec{}, nil, ErrBadGrammar
		}
		for _, alternative := range production.Alternatives {
			if len(alternative) == 0 {
				return GrammarSpec{}, nil, ErrBadGrammar
			}
		}
		productions[production.Name] = production
	}
	if _, exists := productions[normalized.Root]; !exists {
		return GrammarSpec{}, nil, ErrBadGrammar
	}
	for _, name := range normalized.NumericRules {
		if _, exists := productions[name]; !exists {
			return GrammarSpec{}, nil, ErrBadGrammar
		}
	}
	if len(normalized.Boundaries) == 0 {
		normalized.Boundaries = []string{"0", "-1", "2147483648"}
	}
	return normalized, productions, nil
}

func cloneGrammar(spec GrammarSpec) GrammarSpec {
	out := spec
	out.Productions = make([]Production, len(spec.Productions))
	for i, production := range spec.Productions {
		out.Productions[i] = Production{Name: production.Name}
		out.Productions[i].Alternatives = make([][]string, len(production.Alternatives))
		for j, alternative := range production.Alternatives {
			out.Productions[i].Alternatives[j] = append([]string(nil), alternative...)
		}
	}
	out.NumericRules = append([]string(nil), spec.NumericRules...)
	out.Boundaries = append([]string(nil), spec.Boundaries...)
	return out
}

func cloneNode(node Node) Node {
	out := node
	if node.Children != nil {
		out.Children = make([]Node, len(node.Children))
		for i, child := range node.Children {
			out.Children[i] = cloneNode(child)
		}
	}
	return out
}

func cloneNodes(nodes []Node) []Node {
	out := make([]Node, len(nodes))
	for i, node := range nodes {
		out[i] = cloneNode(node)
	}
	return out
}

func cloneInput(input Input) Input {
	out := input
	out.Tree = cloneNode(input.Tree)
	return out
}

func cloneInputs(inputs []Input) []Input {
	out := make([]Input, len(inputs))
	for i, input := range inputs {
		out[i] = cloneInput(input)
	}
	return out
}

func sortedSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (s *GrammarMutationScheduler) validateSeedLocked(input Input) error {
	if input.Target != s.grammar.Target {
		return ErrBadSeed
	}
	if input.Tree.Production != s.grammar.Root {
		return ErrBadSeed
	}
	if !validTree(input.Tree, s.productions, s.grammar.Root) {
		return ErrBadSeed
	}
	return nil
}

func validTree(tree Node, productions map[string]Production, root string) bool {
	if tree.Production == "" && tree.Value == "" && len(tree.Children) == 0 {
		return false
	}
	if tree.Production != "" {
		if tree.Value != "" || len(tree.Children) == 0 {
			return false
		}
		if _, exists := productions[tree.Production]; !exists {
			return false
		}
		for _, child := range tree.Children {
			if !validTree(child, productions, root) {
				return false
			}
		}
		return true
	}
	if len(tree.Children) != 0 {
		return false
	}
	return true
}

func weightFor(stat OpStats) float64 {
	if stat.Submitted <= 0 {
		return 1
	}
	weight := float64(1+stat.Gain) / float64(1+stat.Submitted)
	if stat.ZeroGain > 0 {
		weight *= math.Pow(0.5, float64(stat.ZeroGain-1))
	}
	if math.IsNaN(weight) || math.IsInf(weight, 0) {
		return MinWeight
	}
	weight = math.Round(weight*10000) / 10000
	if weight < MinWeight {
		weight = MinWeight
	}
	return weight
}

func (s *GrammarMutationScheduler) statLocked(op MutationOp) OpStats {
	target := ""
	if s.loaded {
		target = s.grammar.Target
	}
	if byOp := s.stats[target]; byOp != nil {
		return byOp[op]
	}
	return OpStats{Op: op}
}

func (s *GrammarMutationScheduler) weightLocked(op MutationOp) float64 {
	return weightFor(s.statLocked(op))
}

func allocateQuotas(weights []float64, budget int) []int {
	quotas := make([]int, len(weights))
	if len(weights) == 0 || budget <= 0 {
		return quotas
	}
	total := 0.0
	for _, weight := range weights {
		if weight > 0 && !math.IsNaN(weight) && !math.IsInf(weight, 0) {
			total += weight
		}
	}
	if total <= 0 {
		for i := range weights {
			weights[i] = 1
		}
		total = float64(len(weights))
	}

	used := 0
	for i, weight := range weights {
		if weight <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
			weight = 0
		}
		quota := int(math.Floor(float64(budget) * weight / total))
		quotas[i] = quota
		used += quota
	}

	order := make([]int, len(weights))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return weights[order[i]] > weights[order[j]]
	})
	remaining := budget - used
	for i := 0; i < remaining && len(order) > 0; i++ {
		quotas[order[i%len(order)]]++
	}
	return quotas
}

func (s *GrammarMutationScheduler) generateLocked(seeds []Input, op MutationOp, need int) []Input {
	if need <= 0 {
		return nil
	}

	pool := make([]Input, 0, len(seeds))
	seenPool := make(map[string]struct{}, len(seeds))
	for _, seed := range seeds {
		candidate := cloneInput(seed)
		candidate.Target = s.grammar.Target
		candidate.Fingerprint = s.Fingerprint(candidate)
		if _, seen := seenPool[candidate.Fingerprint]; seen {
			continue
		}
		seenPool[candidate.Fingerprint] = struct{}{}
		pool = append(pool, candidate)
	}

	out := make([]Input, 0)
	for index := 0; index < len(pool) && len(out) < need; index++ {
		tree, err := s.mutateTreeLocked(pool[index].Tree, op)
		if err != nil {
			continue
		}
		candidate := Input{
			Target: s.grammar.Target,
			Op:     op,
			Tree:   tree,
		}
		candidate.Fingerprint = s.Fingerprint(candidate)
		s.budget.Generated++

		if _, seen := seenPool[candidate.Fingerprint]; !seen {
			seenPool[candidate.Fingerprint] = struct{}{}
			pool = append(pool, candidate)
		}
		if _, issued := s.issued[candidate.Fingerprint]; issued {
			continue
		}
		if _, submitted := s.submitted[candidate.Fingerprint]; submitted {
			continue
		}

		s.issued[candidate.Fingerprint] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func (s *GrammarMutationScheduler) mutateTreeLocked(seed Node, op MutationOp) (Node, error) {
	switch op {
	case OpReplace:
		return s.replaceLocked(seed)
	case OpBoundary:
		return s.boundaryLocked(seed)
	case OpSplice:
		return s.spliceLocked(seed)
	default:
		return Node{}, ErrUnknownOp
	}
}

func (s *GrammarMutationScheduler) replaceLocked(seed Node) (Node, error) {
	path, children, found := s.findReplacement(seed, nil)
	if !found {
		return Node{}, ErrNoApplicableNode
	}
	out := cloneNode(seed)
	node := nodeAtPath(&out, path)
	node.Children = children
	return out, nil
}

func (s *GrammarMutationScheduler) findReplacement(node Node, path []int) ([]int, []Node, bool) {
	if node.Production != "" {
		if production, exists := s.productions[node.Production]; exists {
			for index, alternative := range production.Alternatives {
				if index+1 >= len(production.Alternatives) || !s.childrenMatch(node.Children, alternative) {
					continue
				}
				stack := map[string]bool{node.Production: true}
				children, expanded := s.expandTokens(production.Alternatives[index+1], stack)
				if expanded {
					return clonePath(path), children, true
				}
			}
		}
	}
	for index, child := range node.Children {
		if foundPath, children, found := s.findReplacement(child, childPath(path, index)); found {
			return foundPath, children, true
		}
	}
	return nil, nil, false
}

func (s *GrammarMutationScheduler) childrenMatch(children []Node, alternative []string) bool {
	if len(children) != len(alternative) {
		return false
	}
	for index, token := range alternative {
		child := children[index]
		if _, isProduction := s.productions[token]; isProduction {
			if child.Production != token {
				return false
			}
			continue
		}
		if child.Production != "" || child.Value != token {
			return false
		}
	}
	return true
}

func (s *GrammarMutationScheduler) expandFirst(name string, stack map[string]bool) (Node, bool) {
	if stack[name] {
		return Node{}, false
	}
	production, exists := s.productions[name]
	if !exists || len(production.Alternatives) == 0 {
		return Node{}, false
	}
	stack[name] = true
	children, expanded := s.expandTokens(production.Alternatives[0], stack)
	delete(stack, name)
	if !expanded {
		return Node{}, false
	}
	return Node{Production: name, Children: children}, true
}

func (s *GrammarMutationScheduler) expandTokens(alternative []string, stack map[string]bool) ([]Node, bool) {
	children := make([]Node, 0, len(alternative))
	for _, token := range alternative {
		if _, isProduction := s.productions[token]; isProduction {
			child, expanded := s.expandFirst(token, stack)
			if !expanded {
				return nil, false
			}
			children = append(children, child)
			continue
		}
		children = append(children, Node{Value: token})
	}
	return children, true
}

func (s *GrammarMutationScheduler) boundaryLocked(seed Node) (Node, error) {
	path, found := s.firstNumericLeaf(seed, nil, false)
	if !found {
		return Node{}, ErrNoApplicableNode
	}

	present := make(map[string]struct{})
	collectLeafValues(seed, present)
	replacement := ""
	foundValue := false
	start := 0
	current := nodeAtPath(&seed, path).Value
	for index, boundary := range s.grammar.Boundaries {
		if boundary == current {
			start = index + 1
			break
		}
	}
	for offset := 0; offset < len(s.grammar.Boundaries); offset++ {
		index := (start + offset) % len(s.grammar.Boundaries)
		candidate := s.grammar.Boundaries[index]
		if _, exists := present[candidate]; !exists {
			replacement = candidate
			foundValue = true
			break
		}
	}
	if !foundValue {
		return Node{}, ErrNoApplicableNode
	}

	out := cloneNode(seed)
	nodeAtPath(&out, path).Value = replacement
	return out, nil
}

func (s *GrammarMutationScheduler) firstNumericLeaf(node Node, path []int, numericScope bool) ([]int, bool) {
	if node.Production == "" {
		if numericScope {
			return clonePath(path), true
		}
		return nil, false
	}
	scope := numericScope || s.numeric[node.Production]
	for index, child := range node.Children {
		if found, ok := s.firstNumericLeaf(child, childPath(path, index), scope); ok {
			return found, true
		}
	}
	return nil, false
}

func collectLeafValues(node Node, values map[string]struct{}) {
	if node.Production == "" {
		values[node.Value] = struct{}{}
		return
	}
	for _, child := range node.Children {
		collectLeafValues(child, values)
	}
}

type productionRef struct {
	path []int
	name string
}

func (s *GrammarMutationScheduler) spliceLocked(seed Node) (Node, error) {
	refs := make([]productionRef, 0)
	collectProductionRefs(seed, nil, &refs)
	if len(refs) < 2 {
		return Node{}, ErrNoApplicableNode
	}

	for left := 0; left < len(refs); left++ {
		for right := left + 1; right < len(refs); right++ {
			if refs[left].name != refs[right].name ||
				pathIsPrefix(refs[left].path, refs[right].path) ||
				pathIsPrefix(refs[right].path, refs[left].path) {
				continue
			}
			out := cloneNode(seed)
			leftNode := nodeAtPath(&out, refs[left].path)
			rightNode := nodeAtPath(&out, refs[right].path)
			leftChildren := cloneNodes(leftNode.Children)
			rightChildren := cloneNodes(rightNode.Children)
			leftNode.Children = rightChildren
			rightNode.Children = leftChildren
			if s.treeWithinLimits(out) {
				return out, nil
			}
		}
	}
	return Node{}, ErrNoApplicableNode
}

func collectProductionRefs(node Node, path []int, refs *[]productionRef) {
	if node.Production != "" {
		*refs = append(*refs, productionRef{path: clonePath(path), name: node.Production})
	}
	for index, child := range node.Children {
		collectProductionRefs(child, childPath(path, index), refs)
	}
}

func (s *GrammarMutationScheduler) treeWithinLimits(tree Node) bool {
	nodes, depth := treeSize(tree)
	if s.grammar.MaxNodes > 0 && nodes > s.grammar.MaxNodes {
		return false
	}
	if s.grammar.MaxDepth > 0 && depth > s.grammar.MaxDepth {
		return false
	}
	return true
}

func treeSize(node Node) (nodes int, depth int) {
	nodes = 1
	depth = 1
	for _, child := range node.Children {
		childNodes, childDepth := treeSize(child)
		nodes += childNodes
		if childDepth+1 > depth {
			depth = childDepth + 1
		}
	}
	return nodes, depth
}

func clonePath(path []int) []int {
	if path == nil {
		return nil
	}
	out := make([]int, len(path))
	copy(out, path)
	return out
}

func childPath(path []int, child int) []int {
	out := make([]int, len(path)+1)
	copy(out, path)
	out[len(path)] = child
	return out
}

func nodeAtPath(root *Node, path []int) *Node {
	node := root
	for _, index := range path {
		node = &node.Children[index]
	}
	return node
}

func pathIsPrefix(prefix, path []int) bool {
	if len(prefix) > len(path) {
		return false
	}
	for index := range prefix {
		if prefix[index] != path[index] {
			return false
		}
	}
	return true
}
