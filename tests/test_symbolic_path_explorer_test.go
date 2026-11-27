package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-36"
)

func chainProgram() []impl.Block {
	return []impl.Block{
		{Name: "a", Cost: 1, Next: []string{"b"}},
		{Name: "b", Cost: 1, Next: []string{"c"}},
		{Name: "c", Cost: 1, Next: []string{"d"}},
		{Name: "d", Cost: 1},
	}
}

func splitProgram() []impl.Block {
	return []impl.Block{
		{Name: "main", Cost: 1, Next: []string{"left", "right"}},
		{Name: "left", Cost: 1, Next: []string{"join"}},
		{Name: "right", Cost: 1, Next: []string{"join"}},
		{Name: "join", Cost: 1},
	}
}

func looseConfig() impl.ExplorerConfig {
	return impl.ExplorerConfig{MaxPending: 64, MaxDepth: 32, MaxExpansions: 100}
}

func TestSeedCreatesRootState(t *testing.T) {
	e := impl.NewSymbolicPathExplorer(chainProgram(), looseConfig())
	args := []impl.Symbol{{Name: "x", Address: 0x1000}, {Name: "buf", Address: 0x2000}}
	if err := e.Seed("a", args); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	if got := e.Pending(); got != 1 {
		t.Fatalf("pending after seed = %d, want 1", got)
	}
	st, err := e.Next()
	if err != nil {
		t.Fatalf("next after seed must succeed, got %v", err)
	}
	if st.ID == "" {
		t.Fatalf("root state id must not be empty")
	}
	if st.PC != "a" {
		t.Fatalf("root program counter = %q, want a", st.PC)
	}
	if st.Depth != 0 {
		t.Fatalf("root depth = %d, want 0", st.Depth)
	}
	if st.Seq != 1 {
		t.Fatalf("root sequence = %d, want 1", st.Seq)
	}
	if st.Status != impl.StatusPending {
		t.Fatalf("root status = %q, want %q", st.Status, impl.StatusPending)
	}
	if len(st.Constraints) != 0 {
		t.Fatalf("root constraints = %v, want empty", st.Constraints)
	}
	if len(st.Aliases) != 2 {
		t.Fatalf("root alias count = %d, want 2", len(st.Aliases))
	}
	if st.Aliases[0].Name != "buf" || st.Aliases[0].Address != 0x2000 {
		t.Fatalf("alias[0] = %+v, want buf at 0x2000 (names ascend)", st.Aliases[0])
	}
	if st.Aliases[1].Name != "x" || st.Aliases[1].Address != 0x1000 {
		t.Fatalf("alias[1] = %+v, want x at 0x1000", st.Aliases[1])
	}
}

func TestSeedRejectsUnknownEntryAndSecondSeed(t *testing.T) {
	e := impl.NewSymbolicPathExplorer(chainProgram(), looseConfig())
	if err := e.Seed("   ", nil); !errors.Is(err, impl.ErrBadEntry) {
		t.Fatalf("blank entry error = %v, want %v", err, impl.ErrBadEntry)
	}
	if err := e.Seed("nowhere", nil); !errors.Is(err, impl.ErrUnknownBlock) {
		t.Fatalf("unknown entry error = %v, want %v", err, impl.ErrUnknownBlock)
	}
	if err := e.Seed("a", nil); err != nil {
		t.Fatalf("first seed must succeed, got %v", err)
	}
	if err := e.Seed("b", nil); !errors.Is(err, impl.ErrAlreadySeeded) {
		t.Fatalf("second seed error = %v, want %v", err, impl.ErrAlreadySeeded)
	}
	if got := e.Pending(); got != 1 {
		t.Fatalf("pending after rejected reseed = %d, want 1", got)
	}
}

func TestStepExpandsBranchIntoSuccessors(t *testing.T) {
	e := impl.NewSymbolicPathExplorer(splitProgram(), looseConfig())
	if err := e.Seed("main", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	kids, err := e.Step(root.ID, 1)
	if err != nil {
		t.Fatalf("step must succeed, got %v", err)
	}
	if len(kids) != 2 {
		t.Fatalf("successor count = %d, want 2", len(kids))
	}
	if kids[0].PC != "left" || kids[1].PC != "right" {
		t.Fatalf("successors = %q/%q, want left/right in program order", kids[0].PC, kids[1].PC)
	}
	for i, kid := range kids {
		if kid.Depth != 1 {
			t.Fatalf("successor %d depth = %d, want 1", i, kid.Depth)
		}
		if kid.ID == root.ID {
			t.Fatalf("successor %d reuses the parent id", i)
		}
		if kid.Seq <= root.Seq {
			t.Fatalf("successor %d sequence = %d, want greater than %d", i, kid.Seq, root.Seq)
		}
	}
	if got := e.Pending(); got != 2 {
		t.Fatalf("pending after branch = %d, want 2", got)
	}
	retired, ok := e.State(root.ID)
	if !ok {
		t.Fatalf("parent state must still be readable")
	}
	if retired.Status != impl.StatusExpanded {
		t.Fatalf("parent status = %q, want %q", retired.Status, impl.StatusExpanded)
	}
}

func TestStepCarriesGuardAndCoverage(t *testing.T) {
	program := []impl.Block{
		{Name: "main", Cost: 1, Next: []string{"mid"}},
		{Name: "mid", Cost: 2, Covers: []string{"blk-z", "blk-a"},
			Guard: []impl.Constraint{{Symbol: "n", Op: ">=", Value: 7}},
			Next:  []string{"tail"}},
		{Name: "tail", Cost: 1},
	}
	e := impl.NewSymbolicPathExplorer(program, looseConfig())
	if err := e.Seed("main", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	mid, err := e.Step(root.ID, 1)
	if err != nil || len(mid) != 1 {
		t.Fatalf("first step states = %d err = %v, want one state", len(mid), err)
	}
	if mid[0].PC != "mid" {
		t.Fatalf("intermediate pc = %q, want mid", mid[0].PC)
	}
	if len(mid[0].Covers) != 0 {
		t.Fatalf("intermediate covers = %v, want empty before the block is expanded", mid[0].Covers)
	}
	out, err := e.Step(mid[0].ID, 1)
	if err != nil || len(out) != 1 {
		t.Fatalf("second step states = %d err = %v, want one state", len(out), err)
	}
	st := out[0]
	if st.PC != "tail" {
		t.Fatalf("pc after guard block = %q, want tail", st.PC)
	}
	if len(st.Constraints) != 1 {
		t.Fatalf("constraint count = %d, want 1", len(st.Constraints))
	}
	c := st.Constraints[0]
	if c.Symbol != "n" || c.Op != ">=" || c.Value != 7 {
		t.Fatalf("constraint = %+v, want n >= 7", c)
	}
	if len(st.Covers) != 2 || st.Covers[0] != "blk-a" || st.Covers[1] != "blk-z" {
		t.Fatalf("covers = %v, want sorted union blk-a,blk-z", st.Covers)
	}
	if st.Cost != 3 {
		t.Fatalf("accumulated cost = %d, want 3", st.Cost)
	}
	if st.Depth != 2 {
		t.Fatalf("depth = %d, want 2", st.Depth)
	}
}

func TestStepDeduplicatesSamePCAndFingerprint(t *testing.T) {
	e := impl.NewSymbolicPathExplorer(splitProgram(), looseConfig())
	if err := e.Seed("main", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	side, err := e.Step(root.ID, 1)
	if err != nil || len(side) != 2 {
		t.Fatalf("branch states = %d err = %v, want 2", len(side), err)
	}
	first, err := e.Step(side[0].ID, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first join state = %d err = %v, want 1", len(first), err)
	}
	if first[0].PC != "join" {
		t.Fatalf("first join pc = %q, want join", first[0].PC)
	}
	second, err := e.Step(side[1].ID, 1)
	if err != nil {
		t.Fatalf("second step must not fail, got %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("duplicate state count = %d, want 0", len(second))
	}
	if e.Pending() != 1 {
		t.Fatalf("pending after dedup = %d, want 1", e.Pending())
	}
	if got := e.Stats().Deduped; got != 1 {
		t.Fatalf("deduped counter = %d, want 1", got)
	}
}

func TestStepFollowsSingleSuccessorUpToBudget(t *testing.T) {
	e := impl.NewSymbolicPathExplorer(chainProgram(), looseConfig())
	if err := e.Seed("a", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	out, err := e.Step(root.ID, 2)
	if err != nil {
		t.Fatalf("step must succeed, got %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("state count after two blocks = %d, want 1", len(out))
	}
	if out[0].PC != "c" {
		t.Fatalf("pc after two blocks = %q, want c", out[0].PC)
	}
	if out[0].Depth != 2 {
		t.Fatalf("depth after two blocks = %d, want 2", out[0].Depth)
	}
	if out[0].Cost != 2 {
		t.Fatalf("cost after two blocks = %d, want 2", out[0].Cost)
	}
	if got := e.Stats().Expanded; got != 2 {
		t.Fatalf("expanded counter = %d, want 2", got)
	}
	tail, err := e.Step(out[0].ID, 10)
	if err != nil {
		t.Fatalf("tail step must succeed, got %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("states after the terminal block = %d, want 0", len(tail))
	}
	if e.Pending() != 0 {
		t.Fatalf("pending after the terminal block = %d, want 0", e.Pending())
	}
}

func TestStepRejectsUnknownAndFinishedState(t *testing.T) {
	e := impl.NewSymbolicPathExplorer(chainProgram(), looseConfig())
	if err := e.Seed("a", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	if _, err := e.Step("st-nope", 1); !errors.Is(err, impl.ErrUnknownState) {
		t.Fatalf("step on unknown state error = %v, want %v", err, impl.ErrUnknownState)
	}
	if _, err := e.Step(root.ID, 0); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("step with zero budget error = %v, want %v", err, impl.ErrBadBudget)
	}
	if _, err := e.Step(root.ID, 10); err != nil {
		t.Fatalf("full step must succeed, got %v", err)
	}
	if _, err := e.Step(root.ID, 1); !errors.Is(err, impl.ErrStateFinished) {
		t.Fatalf("step on finished state error = %v, want %v", err, impl.ErrStateFinished)
	}
}

func TestNextPrefersCoverageGainThenDepth(t *testing.T) {
	program := []impl.Block{
		{Name: "main", Cost: 1, Next: []string{"wide", "thin", "deep"}},
		{Name: "wide", Cost: 1, Covers: []string{"k1", "k2"}, Next: []string{"wide2"}},
		{Name: "wide2", Cost: 1, Next: []string{"wide3"}},
		{Name: "thin", Cost: 1, Covers: []string{"k1"}, Next: []string{"thin2"}},
		{Name: "thin2", Cost: 1, Next: []string{"thin3"}},
		{Name: "deep", Cost: 1, Covers: []string{"k1", "k2"}, Next: []string{"deep2"}},
		{Name: "deep2", Cost: 1, Next: []string{"deep3"}},
		{Name: "wide3", Cost: 1},
		{Name: "thin3", Cost: 1},
		{Name: "deep3", Cost: 1},
	}
	e := impl.NewSymbolicPathExplorer(program, looseConfig())
	if err := e.Seed("main", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	branches, err := e.Step(root.ID, 1)
	if err != nil || len(branches) != 3 {
		t.Fatalf("branch states = %d err = %v, want 3", len(branches), err)
	}
	if _, err := e.Step(branches[0].ID, 1); err != nil {
		t.Fatalf("wide step must succeed, got %v", err)
	}
	if _, err := e.Step(branches[1].ID, 1); err != nil {
		t.Fatalf("thin step must succeed, got %v", err)
	}
	if _, err := e.Step(branches[2].ID, 2); err != nil {
		t.Fatalf("deep step must succeed, got %v", err)
	}
	if e.Pending() != 3 {
		t.Fatalf("pending = %d, want 3", e.Pending())
	}
	best, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	if best.PC != "deep3" {
		t.Fatalf("next pc = %q, want deep3 (two covers, greater depth)", best.PC)
	}
	if e.Pending() != 3 {
		t.Fatalf("next must not dequeue: pending = %d, want 3", e.Pending())
	}
}

func TestPruneRequiresReasonAndRemovesFromQueue(t *testing.T) {
	e := impl.NewSymbolicPathExplorer(chainProgram(), looseConfig())
	if err := e.Seed("a", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	if err := e.Prune(root.ID, "  "); !errors.Is(err, impl.ErrBadPruneReason) {
		t.Fatalf("blank reason error = %v, want %v", err, impl.ErrBadPruneReason)
	}
	if err := e.Prune("st-nope", impl.PruneUnsat); !errors.Is(err, impl.ErrUnknownState) {
		t.Fatalf("prune unknown error = %v, want %v", err, impl.ErrUnknownState)
	}
	if err := e.Prune(root.ID, impl.PruneUnsat); err != nil {
		t.Fatalf("prune must succeed, got %v", err)
	}
	if e.Pending() != 0 {
		t.Fatalf("pending after prune = %d, want 0", e.Pending())
	}
	st, ok := e.State(root.ID)
	if !ok {
		t.Fatalf("pruned state must stay readable")
	}
	if st.Status != impl.StatusPruned {
		t.Fatalf("pruned status = %q, want %q", st.Status, impl.StatusPruned)
	}
	if _, err := e.Next(); !errors.Is(err, impl.ErrNoPending) {
		t.Fatalf("next on empty queue error = %v, want %v", err, impl.ErrNoPending)
	}
	if got := e.Stats().Pruned; got != 1 {
		t.Fatalf("pruned counter = %d, want 1", got)
	}
}

func TestMergeCombinesCompatibleStates(t *testing.T) {
	program := []impl.Block{
		{Name: "main", Cost: 1, Next: []string{"left", "right"}},
		{Name: "left", Cost: 1, Covers: []string{"L"},
			Guard: []impl.Constraint{{Symbol: "n", Op: ">=", Value: 1}}, Next: []string{"join"}},
		{Name: "right", Cost: 1, Covers: []string{"R"},
			Guard: []impl.Constraint{{Symbol: "n", Op: "<=", Value: 9}}, Next: []string{"join"}},
		{Name: "join", Cost: 1},
	}
	e := impl.NewSymbolicPathExplorer(program, looseConfig())
	if err := e.Seed("main", []impl.Symbol{{Name: "n", Address: 0x4000}}); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	sides, err := e.Step(root.ID, 1)
	if err != nil || len(sides) != 2 {
		t.Fatalf("branch states = %d err = %v, want 2", len(sides), err)
	}
	first, err := e.Step(sides[0].ID, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("left join state = %d err = %v, want 1", len(first), err)
	}
	second, err := e.Step(sides[1].ID, 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("right join state = %d err = %v, want 1", len(second), err)
	}
	merged, err := e.Merge([]string{first[0].ID, second[0].ID})
	if err != nil {
		t.Fatalf("merge of compatible states failed: %v", err)
	}
	if merged.PC != "join" {
		t.Fatalf("merged pc = %q, want join", merged.PC)
	}
	if merged.Status != impl.StatusPending {
		t.Fatalf("merged status = %q, want %q", merged.Status, impl.StatusPending)
	}
	if len(merged.Constraints) != 2 {
		t.Fatalf("merged constraint count = %d, want 2", len(merged.Constraints))
	}
	if len(merged.Covers) != 2 || merged.Covers[0] != "L" || merged.Covers[1] != "R" {
		t.Fatalf("merged covers = %v, want sorted union L,R", merged.Covers)
	}
	if merged.Cost != 4 {
		t.Fatalf("merged cost = %d, want 4", merged.Cost)
	}
	if merged.Depth != 2 {
		t.Fatalf("merged depth = %d, want 2", merged.Depth)
	}
	if merged.ID == first[0].ID || merged.ID == second[0].ID {
		t.Fatalf("merged state must get a fresh id")
	}
	if e.Pending() != 1 {
		t.Fatalf("pending after merge = %d, want 1", e.Pending())
	}
	for _, id := range []string{first[0].ID, second[0].ID} {
		st, ok := e.State(id)
		if !ok || st.Status != impl.StatusMerged {
			t.Fatalf("source state %s status = %q, want %q", id, st.Status, impl.StatusMerged)
		}
	}
	if got := e.Stats().Merged; got != 1 {
		t.Fatalf("merged counter = %d, want 1", got)
	}
}

func TestMergeRejectsDifferentPCAndAliases(t *testing.T) {
	e := impl.NewSymbolicPathExplorer(splitProgram(), looseConfig())
	if err := e.Seed("main", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	sides, err := e.Step(root.ID, 1)
	if err != nil || len(sides) != 2 {
		t.Fatalf("branch states = %d err = %v, want 2", len(sides), err)
	}
	if _, err := e.Merge([]string{sides[0].ID, sides[1].ID}); !errors.Is(err, impl.ErrMergeMismatch) {
		t.Fatalf("merge of different program points error = %v, want %v", err, impl.ErrMergeMismatch)
	}
	if _, err := e.Merge([]string{sides[0].ID}); !errors.Is(err, impl.ErrMergeTooFew) {
		t.Fatalf("merge of a single state error = %v, want %v", err, impl.ErrMergeTooFew)
	}

	aliased := []impl.Block{
		{Name: "main", Cost: 1, Next: []string{"pa", "pb"}},
		{Name: "pa", Cost: 1, Binds: []impl.Alias{{Name: "p", Address: 0x2000}},
			Guard: []impl.Constraint{{Symbol: "p", Op: "==", Value: 1}}, Next: []string{"join"}},
		{Name: "pb", Cost: 1, Binds: []impl.Alias{{Name: "p", Address: 0x3000}},
			Guard: []impl.Constraint{{Symbol: "p", Op: "==", Value: 2}}, Next: []string{"join"}},
		{Name: "join", Cost: 1},
	}
	alt := impl.NewSymbolicPathExplorer(aliased, looseConfig())
	if err := alt.Seed("main", []impl.Symbol{{Name: "p", Address: 0x1000}}); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	proot, err := alt.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	paths, err := alt.Step(proot.ID, 1)
	if err != nil || len(paths) != 2 {
		t.Fatalf("aliased branch states = %d err = %v, want 2", len(paths), err)
	}
	left, err := alt.Step(paths[0].ID, 1)
	if err != nil || len(left) != 1 {
		t.Fatalf("aliased left state = %d err = %v, want 1", len(left), err)
	}
	right, err := alt.Step(paths[1].ID, 1)
	if err != nil || len(right) != 1 {
		t.Fatalf("aliased right state = %d err = %v, want 1", len(right), err)
	}
	if left[0].Aliases[0].Address == right[0].Aliases[0].Address {
		t.Fatalf("test setup is broken: both paths kept the same alias address")
	}
	if _, err := alt.Merge([]string{left[0].ID, right[0].ID}); !errors.Is(err, impl.ErrMergeMismatch) {
		t.Fatalf("merge with different aliases error = %v, want %v", err, impl.ErrMergeMismatch)
	}
}

func TestSolveReturnsModelForPendingState(t *testing.T) {
	program := []impl.Block{
		{Name: "main", Cost: 1, Next: []string{"a", "b"}},
		{Name: "a", Cost: 1,
			Guard: []impl.Constraint{
				{Symbol: "x", Op: ">=", Value: 5},
				{Symbol: "y", Op: "==", Value: 7},
			}},
		{Name: "b", Cost: 1},
	}
	e := impl.NewSymbolicPathExplorer(program, looseConfig())
	if err := e.Seed("main", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	kids, err := e.Step(root.ID, 1)
	if err != nil || len(kids) != 2 {
		t.Fatalf("branch states = %d err = %v, want 2", len(kids), err)
	}
	model, err := e.Solve(kids[0].ID)
	if err != nil {
		t.Fatalf("solve must succeed, got %v", err)
	}
	if model.StateID != kids[0].ID {
		t.Fatalf("model state id = %q, want %q", model.StateID, kids[0].ID)
	}
	if len(model.Constraints) != 2 {
		t.Fatalf("model constraint count = %d, want 2", len(model.Constraints))
	}
	if got := model.Values["x"]; got != 5 {
		t.Fatalf("model value for x = %d, want 5", got)
	}
	if got := model.Values["y"]; got != 7 {
		t.Fatalf("model value for y = %d, want 7", got)
	}
	if err := e.Prune(kids[1].ID, impl.PruneUnsat); err != nil {
		t.Fatalf("prune must succeed, got %v", err)
	}
	if _, err := e.Solve(kids[1].ID); !errors.Is(err, impl.ErrStatePruned) {
		t.Fatalf("solve on pruned state error = %v, want %v", err, impl.ErrStatePruned)
	}
	if _, err := e.Solve("st-nope"); !errors.Is(err, impl.ErrUnknownState) {
		t.Fatalf("solve on unknown state error = %v, want %v", err, impl.ErrUnknownState)
	}
}

func TestPendingLimitRejectsExpansionAtomically(t *testing.T) {
	program := []impl.Block{
		{Name: "main", Cost: 1, Next: []string{"a", "b", "c"}},
		{Name: "a", Cost: 1},
		{Name: "b", Cost: 1},
		{Name: "c", Cost: 1},
	}
	cfg := impl.ExplorerConfig{MaxPending: 2, MaxDepth: 32, MaxExpansions: 100}
	e := impl.NewSymbolicPathExplorer(program, cfg)
	if err := e.Seed("main", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	if _, err := e.Step(root.ID, 1); !errors.Is(err, impl.ErrPendingLimit) {
		t.Fatalf("over-limit expansion error = %v, want %v", err, impl.ErrPendingLimit)
	}
	if got := e.Pending(); got != 1 {
		t.Fatalf("pending after rejected expansion = %d, want 1", got)
	}
	st, ok := e.State(root.ID)
	if !ok || st.Status != impl.StatusPending {
		t.Fatalf("parent status after rejected expansion = %q, want %q", st.Status, impl.StatusPending)
	}
}

func TestConcurrentStepExpandsOnlyOnce(t *testing.T) {
	e := impl.NewSymbolicPathExplorer(splitProgram(), looseConfig())
	if err := e.Seed("main", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	const workers = 8
	errs := make([]error, workers)
	sizes := make([]int, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			kids, stepErr := e.Step(root.ID, 1)
			errs[idx] = stepErr
			sizes[idx] = len(kids)
		}(i)
	}
	wg.Wait()
	winners := 0
	for i := 0; i < workers; i++ {
		if errs[i] == nil {
			winners++
			if sizes[i] != 2 {
				t.Fatalf("winning step returned %d states, want 2", sizes[i])
			}
			continue
		}
		if !errors.Is(errs[i], impl.ErrStateFinished) && !errors.Is(errs[i], impl.ErrUnknownState) {
			t.Fatalf("losing step error = %v, want %v or %v", errs[i], impl.ErrStateFinished, impl.ErrUnknownState)
		}
	}
	if winners != 1 {
		t.Fatalf("successful expansions = %d, want exactly 1", winners)
	}
	if got := e.Pending(); got != 2 {
		t.Fatalf("pending after concurrent step = %d, want 2", got)
	}
}

func TestSnapshotRestoreResumesBudgetSuspendedState(t *testing.T) {
	cfg := impl.ExplorerConfig{MaxPending: 64, MaxDepth: 32, MaxExpansions: 2}
	e := impl.NewSymbolicPathExplorer(chainProgram(), cfg)
	if err := e.Seed("a", nil); err != nil {
		t.Fatalf("seed must succeed, got %v", err)
	}
	root, err := e.Next()
	if err != nil {
		t.Fatalf("next must succeed, got %v", err)
	}
	out, err := e.Step(root.ID, 10)
	if err != nil {
		t.Fatalf("step must succeed, got %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("suspended state count = %d, want 1", len(out))
	}
	if out[0].Status != impl.StatusSuspended {
		t.Fatalf("status after budget exhaustion = %q, want %q", out[0].Status, impl.StatusSuspended)
	}
	if e.Pending() != 0 {
		t.Fatalf("pending after budget exhaustion = %d, want 0", e.Pending())
	}
	if e.Stats().BudgetLeft != 0 {
		t.Fatalf("budget left = %d, want 0", e.Stats().BudgetLeft)
	}
	snap, err := e.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot len = %d err = %v, want a payload", len(snap), err)
	}
	resumed := impl.NewSymbolicPathExplorer(chainProgram(), looseConfig())
	if err := resumed.Restore(snap); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	restored, ok := resumed.State(out[0].ID)
	if !ok || restored.Status != impl.StatusSuspended {
		t.Fatalf("restored state status = %q, want %q", restored.Status, impl.StatusSuspended)
	}
	if got := resumed.Stats().BudgetLeft; got != 97 {
		t.Fatalf("restored budget left = %d, want 97", got)
	}
	tail, err := resumed.Step(out[0].ID, 10)
	if err != nil {
		t.Fatalf("resumed step must succeed, got %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("resumed tail states = %d, want 0", len(tail))
	}
	if got := resumed.Stats().Expanded; got != 4 {
		t.Fatalf("resumed expanded counter = %d, want 4", got)
	}
	if got := resumed.Stats().BudgetLeft; got != 95 {
		t.Fatalf("resumed budget left = %d, want 95", got)
	}
	if err := resumed.Restore([]byte("{not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want %v", err, impl.ErrBadSnapshot)
	}
	if got := resumed.Stats().Expanded; got != 4 {
		t.Fatalf("expanded counter after corrupt restore = %d, want 4", got)
	}
}
