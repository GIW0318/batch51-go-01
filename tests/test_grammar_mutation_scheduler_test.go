package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-safety-c-42"
)

func leaf(value string) impl.Node {
	return impl.Node{Value: value}
}

func prod(name string, children ...impl.Node) impl.Node {
	return impl.Node{Production: name, Children: children}
}

func testGrammar() impl.GrammarSpec {
	return impl.GrammarSpec{
		Target: "parser-json",
		Root:   "doc",
		Productions: []impl.Production{
			{Name: "doc", Alternatives: [][]string{
				{"{", "pair", ",", "pair", "}"},
				{"[", "item", "]"},
				{"null"},
				{"{", "item", "}"},
			}},
			{Name: "pair", Alternatives: [][]string{{"\"k\"", ":", "value"}}},
			{Name: "item", Alternatives: [][]string{{"value"}}},
			{Name: "value", Alternatives: [][]string{{"1"}, {"2"}, {"-1"}}},
		},
		NumericRules: []string{"value"},
		Boundaries:   []string{"0", "-1", "2147483648", "1e309", "-0", "0x7fffffff", "%00", "NaN"},
		MaxDepth:     8,
		MaxNodes:     32,
	}
}

func seedTree() impl.Node {
	return prod("doc",
		leaf("{"),
		prod("pair", leaf("\"k\""), leaf(":"), prod("value", leaf("1"))),
		leaf(","),
		prod("pair", leaf("\"j\""), leaf(":"), prod("value", leaf("2"))),
		leaf("}"),
	)
}

func seedInput() impl.Input {
	return impl.Input{Target: "parser-json", Op: impl.OpReplace, Tree: seedTree()}
}

func render(n impl.Node) string {
	if n.Production == "" {
		return "|" + n.Value
	}
	out := n.Production + "("
	for _, child := range n.Children {
		out += render(child) + ";"
	}
	return out + ")"
}

func loadedScheduler(t *testing.T) *impl.GrammarMutationScheduler {
	t.Helper()
	s := impl.NewGrammarMutationScheduler()
	if err := s.LoadGrammar(testGrammar()); err != nil {
		t.Fatalf("load grammar: %v", err)
	}
	return s
}

func TestLoadGrammarValidates(t *testing.T) {
	cases := []struct {
		name string
		spec impl.GrammarSpec
	}{
		{"unknown root", impl.GrammarSpec{Target: "p", Root: "missing", Productions: []impl.Production{{Name: "stmt", Alternatives: [][]string{{"x"}}}}}},
		{"no productions", impl.GrammarSpec{Target: "p", Root: "stmt"}},
		{"empty alternative", impl.GrammarSpec{Target: "p", Root: "stmt", Productions: []impl.Production{{Name: "stmt"}}}},
		{"bad numeric rule", impl.GrammarSpec{Target: "p", Root: "stmt", Productions: []impl.Production{{Name: "stmt", Alternatives: [][]string{{"x"}}}}, NumericRules: []string{"ghost"}}},
		{"duplicate production", impl.GrammarSpec{Target: "p", Root: "stmt", Productions: []impl.Production{
			{Name: "stmt", Alternatives: [][]string{{"x"}}},
			{Name: "stmt", Alternatives: [][]string{{"y"}}},
		}}},
		{"negative limit", impl.GrammarSpec{Target: "p", Root: "stmt", Productions: []impl.Production{{Name: "stmt", Alternatives: [][]string{{"x"}}}}, MaxNodes: -3}},
	}
	for _, c := range cases {
		s := impl.NewGrammarMutationScheduler()
		if err := s.LoadGrammar(c.spec); !errors.Is(err, impl.ErrBadGrammar) {
			t.Fatalf("%s: want ErrBadGrammar, got %v", c.name, err)
		}
	}
	s := impl.NewGrammarMutationScheduler()
	if _, err := s.Mutate(seedInput(), impl.OpReplace); !errors.Is(err, impl.ErrNoGrammar) {
		t.Fatalf("mutation before loading a grammar must fail with ErrNoGrammar, got %v", err)
	}
}

func TestLoadGrammarStoresSpec(t *testing.T) {
	s := loadedScheduler(t)
	got := s.Grammar()
	if got.Target != "parser-json" || got.Root != "doc" {
		t.Fatalf("grammar target/root = %q/%q", got.Target, got.Root)
	}
	if len(got.Productions) != 4 {
		t.Fatalf("productions = %d, want 4", len(got.Productions))
	}
	if len(got.Boundaries) != len(testGrammar().Boundaries) || got.Boundaries[0] != "0" {
		t.Fatalf("boundaries = %v, want the declared list", got.Boundaries)
	}
	if err := s.LoadGrammar(impl.GrammarSpec{Target: "p", Root: "stmt", Productions: []impl.Production{{Name: "stmt", Alternatives: [][]string{{"x"}}}}}); err != nil {
		t.Fatalf("second load must replace the grammar: %v", err)
	}
	if got := s.Grammar(); got.Target != "p" || len(got.Boundaries) == 0 {
		t.Fatalf("grammar after reload = %+v, want the second spec with default boundaries", got)
	}
}

func TestMutateReplaceAdvancesAlternative(t *testing.T) {
	s := loadedScheduler(t)
	out, err := s.Mutate(seedInput(), impl.OpReplace)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if out.Op != impl.OpReplace {
		t.Fatalf("output op = %q, want replace", out.Op)
	}
	if out.Target != "parser-json" {
		t.Fatalf("output target = %q, want the grammar target", out.Target)
	}
	doc := out.Tree
	if doc.Production != "doc" || len(doc.Children) != 3 {
		t.Fatalf("root after replace = %+v, want the second alternative rendered", doc)
	}
	if doc.Children[0].Value != "[" || doc.Children[2].Value != "]" {
		t.Fatalf("root children = %q ... %q, want the [ item ] alternative", doc.Children[0].Value, doc.Children[2].Value)
	}
	if doc.Children[1].Production != "item" || len(doc.Children[1].Children) != 1 {
		t.Fatalf("item node = %+v, want the first alternative of item", doc.Children[1])
	}
	value := doc.Children[1].Children[0]
	if value.Production != "value" || len(value.Children) != 1 || value.Children[0].Value != "1" {
		t.Fatalf("nested value = %+v, want value -> 1", value)
	}
}

func TestMutateRejectsUnusableRequests(t *testing.T) {
	s := loadedScheduler(t)
	if _, err := s.Mutate(seedInput(), impl.MutationOp("shuffle")); !errors.Is(err, impl.ErrUnknownOp) {
		t.Fatalf("unknown operator must fail with ErrUnknownOp, got %v", err)
	}
	foreign := seedInput()
	foreign.Target = "parser-other"
	if _, err := s.Mutate(foreign, impl.OpReplace); !errors.Is(err, impl.ErrBadSeed) {
		t.Fatalf("a foreign seed must fail with ErrBadSeed, got %v", err)
	}
	if _, err := s.Mutate(impl.Input{}, impl.OpReplace); !errors.Is(err, impl.ErrBadSeed) {
		t.Fatalf("an empty seed must fail with ErrBadSeed, got %v", err)
	}
	flat := impl.NewGrammarMutationScheduler()
	if err := flat.LoadGrammar(impl.GrammarSpec{
		Target: "parser-json",
		Root:   "stmt",
		Productions: []impl.Production{
			{Name: "stmt", Alternatives: [][]string{{"select", "1"}}},
		},
		NumericRules: []string{"stmt"},
		Boundaries:   []string{"0"},
		MaxDepth:     4,
		MaxNodes:     8,
	}); err != nil {
		t.Fatalf("load flat grammar: %v", err)
	}
	flatSeed := impl.Input{Target: "parser-json", Tree: prod("stmt", leaf("select"), leaf("1"))}
	if _, err := flat.Mutate(flatSeed, impl.OpReplace); !errors.Is(err, impl.ErrNoApplicableNode) {
		t.Fatalf("a grammar without alternatives must fail with ErrNoApplicableNode, got %v", err)
	}
	if _, err := flat.Mutate(flatSeed, impl.OpSplice); !errors.Is(err, impl.ErrNoApplicableNode) {
		t.Fatalf("a tree without sibling productions must fail with ErrNoApplicableNode, got %v", err)
	}
}

func TestMutateBoundaryPicksFirstUnusedValue(t *testing.T) {
	s := loadedScheduler(t)
	first, err := s.Mutate(seedInput(), impl.OpBoundary)
	if err != nil {
		t.Fatalf("boundary: %v", err)
	}
	if got := render(first.Tree); got != render(prod("doc",
		leaf("{"),
		prod("pair", leaf("\"k\""), leaf(":"), prod("value", leaf("0"))),
		leaf(","),
		prod("pair", leaf("\"j\""), leaf(":"), prod("value", leaf("2"))),
		leaf("}"),
	)) {
		t.Fatalf("first boundary mutation = %s, want the first unused boundary value on the first numeric leaf", got)
	}
	second, err := s.Mutate(first, impl.OpBoundary)
	if err != nil {
		t.Fatalf("second boundary: %v", err)
	}
	if got := render(second.Tree); got != render(prod("doc",
		leaf("{"),
		prod("pair", leaf("\"k\""), leaf(":"), prod("value", leaf("-1"))),
		leaf(","),
		prod("pair", leaf("\"j\""), leaf(":"), prod("value", leaf("2"))),
		leaf("}"),
	)) {
		t.Fatalf("second boundary mutation = %s, want the next unused boundary value", got)
	}
}

func TestMutateSpliceSwapsSameProductionSubtrees(t *testing.T) {
	s := loadedScheduler(t)
	out, err := s.Mutate(seedInput(), impl.OpSplice)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	doc := out.Tree
	if len(doc.Children) != 5 {
		t.Fatalf("root children = %d, want the structure preserved", len(doc.Children))
	}
	if doc.Children[1].Children[0].Value != "\"j\"" {
		t.Fatalf("first pair header = %q, want the second pair children spliced in", doc.Children[1].Children[0].Value)
	}
	if doc.Children[3].Children[0].Value != "\"k\"" {
		t.Fatalf("second pair header = %q, want the first pair children spliced in", doc.Children[3].Children[0].Value)
	}
	if doc.Children[1].Children[2].Children[0].Value != "2" || doc.Children[3].Children[2].Children[0].Value != "1" {
		t.Fatalf("spliced values = %q/%q, want the whole subtrees swapped", doc.Children[1].Children[2].Children[0].Value, doc.Children[3].Children[2].Children[0].Value)
	}
}

func TestMutateLeavesSeedUntouched(t *testing.T) {
	s := loadedScheduler(t)
	seed := seedInput()
	before := render(seed.Tree)
	beforePrint := s.Fingerprint(seed)
	out, err := s.Mutate(seed, impl.OpSplice)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if render(seed.Tree) != before {
		t.Fatalf("mutation modified the seed tree in place")
	}
	if s.Fingerprint(seed) != beforePrint {
		t.Fatalf("mutation changed the seed fingerprint")
	}
	if out.Fingerprint == "" || out.Fingerprint == beforePrint {
		t.Fatalf("mutated input fingerprint = %q, want a fresh identity", out.Fingerprint)
	}
	if s.Fingerprint(out) != out.Fingerprint {
		t.Fatalf("the recorded fingerprint does not match Fingerprint(output)")
	}
}

func TestFingerprintStability(t *testing.T) {
	s := loadedScheduler(t)
	first := impl.Input{Tree: seedTree()}
	copyInput := impl.Input{Tree: seedTree()}
	if s.Fingerprint(first) != s.Fingerprint(copyInput) {
		t.Fatalf("equal trees must share a fingerprint")
	}
	if s.Fingerprint(first) == "" {
		t.Fatalf("fingerprint must not be empty")
	}
	if s.Fingerprint(first) != s.Fingerprint(impl.Input{Tree: seedTree()}) {
		t.Fatalf("fingerprint must be stable across calls")
	}
	other := impl.Input{Tree: prod("doc",
		leaf("{"),
		prod("pair", leaf("\"k\""), leaf(":"), prod("value", leaf("2"))),
		leaf(","),
		prod("pair", leaf("\"j\""), leaf(":"), prod("value", leaf("2"))),
		leaf("}"),
	)}
	if s.Fingerprint(first) == s.Fingerprint(other) {
		t.Fatalf("trees that differ in one leaf must not share a fingerprint")
	}
}

func TestSubmitRejectsDuplicateAndBadInput(t *testing.T) {
	s := loadedScheduler(t)
	if err := s.Submit(impl.Input{}, impl.Feedback{Parsed: true}); !errors.Is(err, impl.ErrBadInput) {
		t.Fatalf("an input without a fingerprint must fail with ErrBadInput, got %v", err)
	}
	in, err := s.Mutate(seedInput(), impl.OpBoundary)
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := s.Submit(in, impl.Feedback{Parsed: true, NewCoverage: 1}); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if err := s.Submit(in, impl.Feedback{Parsed: true}); !errors.Is(err, impl.ErrDuplicateInput) {
		t.Fatalf("second submit of the same fingerprint must fail with ErrDuplicateInput, got %v", err)
	}
	if got := len(s.Ledger()); got != 1 {
		t.Fatalf("ledger = %d entries, want 1", got)
	}
	if s.Ledger()[0] != in.Fingerprint {
		t.Fatalf("ledger = %v, want the submitted fingerprint", s.Ledger())
	}
}

func TestScoreDecaysOnZeroGain(t *testing.T) {
	s := loadedScheduler(t)
	first, _ := s.Mutate(seedInput(), impl.OpReplace)
	second, _ := s.Mutate(first, impl.OpReplace)
	third, _ := s.Mutate(second, impl.OpReplace)
	for _, in := range []impl.Input{first, second} {
		if err := s.Submit(in, impl.Feedback{Parsed: true}); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}
	score, err := s.Score(impl.OpReplace)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if score != 0.1667 {
		t.Fatalf("score after two zero gain submits = %v, want 1/3 halved = 0.1667", score)
	}
	if err := s.Submit(third, impl.Feedback{Parsed: true, NewCoverage: 4}); err != nil {
		t.Fatalf("submit with gain: %v", err)
	}
	score, err = s.Score(impl.OpReplace)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if score != 1.25 {
		t.Fatalf("score after a gain = %v, want (1+4)/(1+3) = 1.25 with the streak cleared", score)
	}
	stats, err := s.Stats(impl.OpReplace)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Submitted != 3 || stats.Accepted != 3 || stats.Gain != 4 || stats.ZeroGain != 0 {
		t.Fatalf("stats = %+v, want 3 submitted, 3 accepted, gain 4, streak cleared", stats)
	}
	if stats.Weight != 1.25 {
		t.Fatalf("stats weight = %v, want it to follow Score", stats.Weight)
	}
}

func TestScoreHasFloor(t *testing.T) {
	s := loadedScheduler(t)
	if score, err := s.Score(impl.OpBoundary); err != nil || score != 1 {
		t.Fatalf("an untried operator must score 1, got %v err=%v", score, err)
	}
	current, err := s.Mutate(seedInput(), impl.OpBoundary)
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	for i := 0; i < 6; i++ {
		if err := s.Submit(current, impl.Feedback{Parsed: true}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		next, err := s.Mutate(current, impl.OpBoundary)
		if err != nil {
			break
		}
		current = next
	}
	score, err := s.Score(impl.OpBoundary)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if score != impl.MinWeight {
		t.Fatalf("score after a long zero gain streak = %v, want the floor %v", score, impl.MinWeight)
	}
	stats, _ := s.Stats(impl.OpBoundary)
	if stats.Submitted < 2 || stats.ZeroGain < 2 {
		t.Fatalf("stats = %+v, want the zero gain streak recorded", stats)
	}
	if _, err := s.Score(impl.MutationOp("shuffle")); !errors.Is(err, impl.ErrUnknownOp) {
		t.Fatalf("scoring an unknown operator must fail with ErrUnknownOp, got %v", err)
	}
}

func TestNextAllocatesEvenQuotas(t *testing.T) {
	s := loadedScheduler(t)
	if err := s.AddSeed(seedInput()); err != nil {
		t.Fatalf("add seed: %v", err)
	}
	batch, err := s.Next(6)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(batch) != 6 {
		t.Fatalf("batch = %d inputs, want 6", len(batch))
	}
	counts := map[impl.MutationOp]int{}
	seen := map[string]bool{}
	for _, in := range batch {
		if in.Fingerprint == "" || seen[in.Fingerprint] {
			t.Fatalf("batch contains an empty or duplicate fingerprint: %q", in.Fingerprint)
		}
		seen[in.Fingerprint] = true
		if in.Target != "parser-json" {
			t.Fatalf("batch input target = %q, want the grammar target", in.Target)
		}
		counts[in.Op]++
	}
	for _, op := range []impl.MutationOp{impl.OpReplace, impl.OpBoundary, impl.OpSplice} {
		if counts[op] != 2 {
			t.Fatalf("quota for %q = %d, want 2 of 6 with equal weights", op, counts[op])
		}
	}
	state, err := s.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if state.Issued != 6 || state.Submitted != 0 {
		t.Fatalf("budget = %+v, want 6 issued and nothing submitted", state)
	}
	if _, err := s.Next(0); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("a non positive budget must fail with ErrBadBudget, got %v", err)
	}
}

func TestNextSkewsQuotaToHighWeight(t *testing.T) {
	s := loadedScheduler(t)
	if err := s.AddSeed(seedInput()); err != nil {
		t.Fatalf("add seed: %v", err)
	}
	rewarded, err := s.Mutate(seedInput(), impl.OpReplace)
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := s.Submit(rewarded, impl.Feedback{Parsed: true, NewCoverage: 4}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if score, _ := s.Score(impl.OpReplace); score != 2.5 {
		t.Fatalf("replace score = %v, want (1+4)/(1+1) = 2.5", score)
	}
	batch, err := s.Next(4)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	counts := map[impl.MutationOp]int{}
	for _, in := range batch {
		counts[in.Op]++
	}
	if counts[impl.OpReplace] != 3 {
		t.Fatalf("replace quota = %d, want 3 of 4 after gaining weight", counts[impl.OpReplace])
	}
	if counts[impl.OpBoundary] != 1 {
		t.Fatalf("boundary quota = %d, want the remaining 1", counts[impl.OpBoundary])
	}
	if counts[impl.OpSplice] != 0 {
		t.Fatalf("splice quota = %d, want 0 when the budget is short", counts[impl.OpSplice])
	}
	for _, in := range batch {
		if in.Fingerprint == rewarded.Fingerprint {
			t.Fatalf("the batch must not reuse the submitted fingerprint")
		}
	}
}

func TestNextSkipsIssuedFingerprints(t *testing.T) {
	s := loadedScheduler(t)
	if err := s.AddSeed(seedInput()); err != nil {
		t.Fatalf("add seed: %v", err)
	}
	first, err := s.Next(3)
	if err != nil {
		t.Fatalf("first next: %v", err)
	}
	second, err := s.Next(3)
	if err != nil {
		t.Fatalf("second next: %v", err)
	}
	seen := map[string]bool{}
	for _, in := range first {
		seen[in.Fingerprint] = true
	}
	for _, in := range second {
		if in.Fingerprint == "" {
			t.Fatalf("second batch contains an empty fingerprint")
		}
		if seen[in.Fingerprint] {
			t.Fatalf("second batch repeats the issued fingerprint %q", in.Fingerprint)
		}
		seen[in.Fingerprint] = true
	}
	if len(second) != 3 {
		t.Fatalf("second batch = %d inputs, want 3 fresh ones", len(second))
	}
	state, _ := s.Budget()
	if state.Issued != 6 {
		t.Fatalf("issued counter = %d, want 6 across both batches", state.Issued)
	}
	if _, err := impl.NewGrammarMutationScheduler().Next(2); !errors.Is(err, impl.ErrNoSeeds) {
		t.Fatalf("next without seeds must fail with ErrNoSeeds, got %v", err)
	}
}

func TestSnapshotRestoreKeepsWeightsAndLedger(t *testing.T) {
	s := loadedScheduler(t)
	if err := s.AddSeed(seedInput()); err != nil {
		t.Fatalf("add seed: %v", err)
	}
	submitted, err := s.Mutate(seedInput(), impl.OpBoundary)
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := s.Submit(submitted, impl.Feedback{Parsed: true, NewCoverage: 1}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	issued, err := s.Next(3)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	before, _ := s.Score(impl.OpBoundary)
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewGrammarMutationScheduler()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Grammar(); got.Target != "parser-json" || len(got.Productions) != 4 {
		t.Fatalf("restored grammar = %+v", got)
	}
	after, err := restored.Score(impl.OpBoundary)
	if err != nil {
		t.Fatalf("score after restore: %v", err)
	}
	if after != before {
		t.Fatalf("restored weight = %v, want the snapshot value %v", after, before)
	}
	if err := restored.Submit(submitted, impl.Feedback{Parsed: true}); !errors.Is(err, impl.ErrDuplicateInput) {
		t.Fatalf("the submitted ledger must survive a restore, got %v", err)
	}
	reissued, err := restored.Next(3)
	if err != nil {
		t.Fatalf("next after restore: %v", err)
	}
	seen := map[string]bool{}
	for _, in := range issued {
		seen[in.Fingerprint] = true
	}
	for _, in := range reissued {
		if seen[in.Fingerprint] {
			t.Fatalf("the issued ledger must survive a restore, %q was reused", in.Fingerprint)
		}
	}
	if err := restored.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	kept, err := restored.Score(impl.OpBoundary)
	if err != nil || kept != before {
		t.Fatalf("a rejected restore must not touch live state, score=%v err=%v", kept, err)
	}
}
