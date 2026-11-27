package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-12"
)

func testConfig() impl.FreezerConfig {
	return impl.FreezerConfig{MaxCgroups: 32, MaxDepth: 6, MaxRounds: 4}
}

func buildTree(t *testing.T, cfg impl.FreezerConfig) *impl.FreezerCoordinator {
	t.Helper()
	c := impl.NewFreezerCoordinator(cfg)
	for _, node := range []struct {
		path   string
		parent string
		procs  int
	}{
		{"root", "", 4},
		{"a", "root", 2},
		{"b", "root", 3},
		{"a1", "a", 1},
	} {
		if err := c.AddCgroup(node.path, node.parent, node.procs); err != nil {
			t.Fatalf("add cgroup %s: %v", node.path, err)
		}
	}
	return c
}

func TestAddCgroupStoresThawedNode(t *testing.T) {
	c := impl.NewFreezerCoordinator(testConfig())
	if err := c.AddCgroup("root", "", 5); err != nil {
		t.Fatalf("add root cgroup: %v", err)
	}
	node, err := c.Status("root")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if node.Path != "root" || node.Parent != "" {
		t.Fatalf("node identity = %q/%q, want root and an empty parent", node.Path, node.Parent)
	}
	if node.State != impl.StateThawed {
		t.Fatalf("fresh state = %q, want %q", node.State, impl.StateThawed)
	}
	if node.Processes != 5 {
		t.Fatalf("process count = %d, want 5", node.Processes)
	}
	if node.Depth != 0 {
		t.Fatalf("root depth = %d, want 0", node.Depth)
	}
	if node.Stuck != 0 {
		t.Fatalf("fresh stuck count = %d, want 0", node.Stuck)
	}
}

func TestAddCgroupRejectsInvalidPathAndParent(t *testing.T) {
	c := impl.NewFreezerCoordinator(testConfig())
	for _, path := range []string{"", "   ", "a/../b"} {
		if err := c.AddCgroup(path, "", 1); !errors.Is(err, impl.ErrInvalidPath) {
			t.Fatalf("path %q must be rejected with ErrInvalidPath, got %v", path, err)
		}
	}
	if err := c.AddCgroup("root", "", 1); err != nil {
		t.Fatalf("add root: %v", err)
	}
	if err := c.AddCgroup("root", "", 1); !errors.Is(err, impl.ErrDuplicateCgroup) {
		t.Fatalf("duplicate cgroup must fail with ErrDuplicateCgroup, got %v", err)
	}
	if err := c.AddCgroup("child", "ghost", 1); !errors.Is(err, impl.ErrUnknownCgroup) {
		t.Fatalf("unknown parent must fail with ErrUnknownCgroup, got %v", err)
	}
	if got := len(c.Tree("root")); got != 1 {
		t.Fatalf("tree size = %d after rejected adds, want 1", got)
	}
}

func TestAddCgroupEnforcesCountAndDepthLimits(t *testing.T) {
	c := impl.NewFreezerCoordinator(impl.FreezerConfig{MaxCgroups: 3, MaxDepth: 6, MaxRounds: 4})
	for _, name := range []string{"root", "a", "b"} {
		parent := ""
		if name != "root" {
			parent = "root"
		}
		if err := c.AddCgroup(name, parent, 1); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	if err := c.AddCgroup("c", "root", 1); !errors.Is(err, impl.ErrCgroupLimit) {
		t.Fatalf("fourth cgroup must fail with ErrCgroupLimit, got %v", err)
	}
	if err := c.RemoveCgroup("b"); err != nil {
		t.Fatalf("remove leaf: %v", err)
	}
	if err := c.AddCgroup("c", "root", 1); err != nil {
		t.Fatalf("adding after a removal must succeed, got %v", err)
	}
	if err := c.RemoveCgroup("root"); !errors.Is(err, impl.ErrHasChildren) {
		t.Fatalf("removing a parent must fail with ErrHasChildren, got %v", err)
	}
	if err := c.RemoveCgroup("ghost"); !errors.Is(err, impl.ErrUnknownCgroup) {
		t.Fatalf("removing an unknown cgroup must fail with ErrUnknownCgroup, got %v", err)
	}

	deep := impl.NewFreezerCoordinator(impl.FreezerConfig{MaxCgroups: 16, MaxDepth: 2, MaxRounds: 4})
	if err := deep.AddCgroup("root", "", 1); err != nil {
		t.Fatalf("add deep root: %v", err)
	}
	if err := deep.AddCgroup("a", "root", 1); err != nil {
		t.Fatalf("add depth one: %v", err)
	}
	if err := deep.AddCgroup("a1", "a", 1); err != nil {
		t.Fatalf("add depth two: %v", err)
	}
	if err := deep.AddCgroup("a2", "a1", 1); !errors.Is(err, impl.ErrDepthLimit) {
		t.Fatalf("depth three must fail with ErrDepthLimit, got %v", err)
	}
}

func TestFreezeMarksWholeSubtreeFrozen(t *testing.T) {
	c := buildTree(t, testConfig())
	res, err := c.Freeze("root")
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if res.Outcome != impl.OutcomeFrozen {
		t.Fatalf("outcome = %q, want %q", res.Outcome, impl.OutcomeFrozen)
	}
	if res.Root != "root" {
		t.Fatalf("result root = %q, want root", res.Root)
	}
	want := []string{"a", "a1", "b", "root"}
	if len(res.Frozen) != len(want) {
		t.Fatalf("frozen list = %v, want %v", res.Frozen, want)
	}
	for i, path := range want {
		if res.Frozen[i] != path {
			t.Fatalf("frozen list = %v, want the sorted list %v", res.Frozen, want)
		}
	}
	if len(res.Pending) != 0 {
		t.Fatalf("pending list = %v, want empty for a fully frozen tree", res.Pending)
	}
	if res.Rounds < 1 || res.Rounds > testConfig().MaxRounds {
		t.Fatalf("rounds = %d, want between 1 and %d", res.Rounds, testConfig().MaxRounds)
	}
	for _, path := range want {
		node, err := c.Status(path)
		if err != nil {
			t.Fatalf("status %s: %v", path, err)
		}
		if node.State != impl.StateFrozen {
			t.Fatalf("state of %s = %q, want %q", path, node.State, impl.StateFrozen)
		}
	}
}

func TestFreezeRejectsUnknownRoot(t *testing.T) {
	c := buildTree(t, testConfig())
	if _, err := c.Freeze("ghost"); !errors.Is(err, impl.ErrUnknownCgroup) {
		t.Fatalf("freezing an unknown root must fail with ErrUnknownCgroup, got %v", err)
	}
	if _, err := c.Freeze(""); !errors.Is(err, impl.ErrUnknownCgroup) {
		t.Fatalf("freezing an empty path must fail with ErrUnknownCgroup, got %v", err)
	}
	node, err := c.Status("root")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if node.State != impl.StateThawed {
		t.Fatalf("rejected freeze changed the root state to %q", node.State)
	}
}

func TestFreezeRollsBackWhenRootCannotFreeze(t *testing.T) {
	c := impl.NewFreezerCoordinator(testConfig())
	if err := c.AddCgroup("solo", "", 2); err != nil {
		t.Fatalf("add cgroup: %v", err)
	}
	if err := c.MarkStuck("solo"); err != nil {
		t.Fatalf("mark stuck: %v", err)
	}
	res, err := c.Freeze("solo")
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if res.Outcome != impl.OutcomeTimeout {
		t.Fatalf("outcome = %q, want %q when no layer reaches frozen", res.Outcome, impl.OutcomeTimeout)
	}
	if len(res.RolledBack) != 1 || res.RolledBack[0] != "solo" {
		t.Fatalf("rolled back = %v, want exactly solo", res.RolledBack)
	}
	node, err := c.Status("solo")
	if err != nil {
		t.Fatalf("status after rollback: %v", err)
	}
	if node.State != impl.StateThawed {
		t.Fatalf("state after rollback = %q, want %q", node.State, impl.StateThawed)
	}
	if got := c.Covered("solo"); len(got) != 0 {
		t.Fatalf("covered after rollback = %v, want an empty subtree", got)
	}
}

func TestWakeThenFreezeSucceeds(t *testing.T) {
	c := impl.NewFreezerCoordinator(testConfig())
	if err := c.AddCgroup("root", "", 1); err != nil {
		t.Fatalf("add root: %v", err)
	}
	if err := c.AddCgroup("child", "root", 1); err != nil {
		t.Fatalf("add child: %v", err)
	}
	if err := c.MarkStuck("child"); err != nil {
		t.Fatalf("mark stuck: %v", err)
	}
	first, err := c.Freeze("root")
	if err != nil {
		t.Fatalf("first freeze: %v", err)
	}
	if first.Outcome == impl.OutcomeFrozen {
		t.Fatalf("freeze with a stuck child must not claim success, got %q", first.Outcome)
	}
	if err := c.Wake("child"); err != nil {
		t.Fatalf("wake: %v", err)
	}
	child, err := c.Status("child")
	if err != nil {
		t.Fatalf("status child: %v", err)
	}
	if child.Stuck != 0 {
		t.Fatalf("stuck count after wake = %d, want 0", child.Stuck)
	}
	second, err := c.Freeze("root")
	if err != nil {
		t.Fatalf("second freeze: %v", err)
	}
	if second.Outcome != impl.OutcomeFrozen {
		t.Fatalf("second freeze outcome = %q, want %q", second.Outcome, impl.OutcomeFrozen)
	}
}

func TestFreezeIsIdempotentOnFrozenTree(t *testing.T) {
	c := buildTree(t, testConfig())
	first, err := c.Freeze("root")
	if err != nil {
		t.Fatalf("first freeze: %v", err)
	}
	second, err := c.Freeze("root")
	if err != nil {
		t.Fatalf("second freeze: %v", err)
	}
	if second.Outcome != impl.OutcomeFrozen {
		t.Fatalf("second outcome = %q, want %q", second.Outcome, impl.OutcomeFrozen)
	}
	if len(second.Frozen) != len(first.Frozen) {
		t.Fatalf("second frozen list = %v, want the same %v", second.Frozen, first.Frozen)
	}
	if second.Rounds < 1 || second.Rounds > 2 {
		t.Fatalf("second freeze spent %d rounds, want at most 2 on an already frozen tree", second.Rounds)
	}
}

func TestThawThawsBottomUp(t *testing.T) {
	c := buildTree(t, testConfig())
	if _, err := c.Freeze("root"); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	res, err := c.Thaw("root")
	if err != nil {
		t.Fatalf("thaw: %v", err)
	}
	want := []string{"a1", "a", "b", "root"}
	if len(res.Thawed) != len(want) {
		t.Fatalf("thawed list = %v, want %v", res.Thawed, want)
	}
	for i, path := range want {
		if res.Thawed[i] != path {
			t.Fatalf("thaw order = %v, want the reverse of the freeze order %v", res.Thawed, want)
		}
	}
	for _, path := range want {
		node, err := c.Status(path)
		if err != nil {
			t.Fatalf("status %s: %v", path, err)
		}
		if node.State != impl.StateThawed {
			t.Fatalf("state of %s after thaw = %q, want %q", path, node.State, impl.StateThawed)
		}
	}
	if got := c.Covered("root"); len(got) != 0 {
		t.Fatalf("covered after thaw = %v, want empty", got)
	}
}

func TestThawRejectsThawedTree(t *testing.T) {
	c := buildTree(t, testConfig())
	if _, err := c.Thaw("root"); !errors.Is(err, impl.ErrNotFrozen) {
		t.Fatalf("thawing a thawed tree must fail with ErrNotFrozen, got %v", err)
	}
	if _, err := c.Thaw("ghost"); !errors.Is(err, impl.ErrUnknownCgroup) {
		t.Fatalf("thawing an unknown root must fail with ErrUnknownCgroup, got %v", err)
	}
	if _, err := c.Freeze("root"); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if _, err := c.Thaw("root"); err != nil {
		t.Fatalf("thaw after freeze: %v", err)
	}
	if _, err := c.Thaw("root"); !errors.Is(err, impl.ErrNotFrozen) {
		t.Fatalf("second thaw must fail with ErrNotFrozen, got %v", err)
	}
}

func TestTreeOrdersByDepthThenPath(t *testing.T) {
	c := buildTree(t, testConfig())
	nodes := c.Tree("root")
	want := []string{"root", "a", "b", "a1"}
	if len(nodes) != len(want) {
		t.Fatalf("tree size = %d, want %d", len(nodes), len(want))
	}
	for i, path := range want {
		if nodes[i].Path != path {
			t.Fatalf("tree order = %v, want %v", pathsOf(nodes), want)
		}
	}
	if got := c.Tree("a"); len(got) != 2 || got[0].Path != "a" || got[1].Path != "a1" {
		t.Fatalf("subtree of a = %v, want [a a1]", pathsOf(got))
	}
	if got := c.Tree("ghost"); len(got) != 0 {
		t.Fatalf("tree of an unknown root = %v, want empty", pathsOf(got))
	}
	if nodes[2].Depth != 1 || nodes[3].Depth != 2 {
		t.Fatalf("depths = %d,%d, want 1 and 2", nodes[2].Depth, nodes[3].Depth)
	}
}

func pathsOf(nodes []impl.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Path)
	}
	return out
}

func TestCoveredListsFrozenNodes(t *testing.T) {
	c := buildTree(t, testConfig())
	if got := c.Covered("root"); len(got) != 0 {
		t.Fatalf("covered before freezing = %v, want empty", got)
	}
	if _, err := c.Freeze("root"); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	got := c.Covered("root")
	want := []string{"a", "a1", "b", "root"}
	if len(got) != len(want) {
		t.Fatalf("covered = %v, want %v", got, want)
	}
	for i, path := range want {
		if got[i] != path {
			t.Fatalf("covered = %v, want the sorted list %v", got, want)
		}
	}
	sub := c.Covered("a")
	if len(sub) != 2 || sub[0] != "a" || sub[1] != "a1" {
		t.Fatalf("covered below a = %v, want [a a1]", sub)
	}
}

func TestConcurrentFreezeThawKeepStatesValid(t *testing.T) {
	c := buildTree(t, testConfig())
	var wg sync.WaitGroup
	bad := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := c.Freeze("root")
			if err != nil {
				bad <- err
				return
			}
			if res.Outcome != impl.OutcomeFrozen {
				bad <- errors.New("freeze of a healthy tree did not report frozen")
				return
			}
			if _, err := c.Thaw("root"); err != nil && !errors.Is(err, impl.ErrNotFrozen) {
				bad <- err
			}
		}(i)
	}
	wg.Wait()
	close(bad)
	for err := range bad {
		t.Fatalf("concurrent freeze/thaw: %v", err)
	}
	if _, err := c.Freeze("root"); err != nil {
		t.Fatalf("final freeze: %v", err)
	}
	res, err := c.Thaw("root")
	if err != nil {
		t.Fatalf("final thaw: %v", err)
	}
	if len(res.Thawed) != 4 {
		t.Fatalf("final thaw released %d layers, want all 4", len(res.Thawed))
	}
	for _, path := range []string{"root", "a", "b", "a1"} {
		node, err := c.Status(path)
		if err != nil {
			t.Fatalf("status %s: %v", path, err)
		}
		if node.State != impl.StateThawed {
			t.Fatalf("state of %s after the final thaw = %q", path, node.State)
		}
	}
}

func TestSnapshotRestoreKeepsStates(t *testing.T) {
	c := buildTree(t, testConfig())
	if _, err := c.Freeze("root"); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewFreezerCoordinator(testConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	nodes := restored.Tree("root")
	if len(nodes) != 4 {
		t.Fatalf("restored tree size = %d, want 4", len(nodes))
	}
	for _, path := range []string{"root", "a", "b", "a1"} {
		node, err := restored.Status(path)
		if err != nil {
			t.Fatalf("status %s after restore: %v", path, err)
		}
		if node.State != impl.StateFrozen {
			t.Fatalf("restored state of %s = %q, want %q", path, node.State, impl.StateFrozen)
		}
	}
	if got := restored.Covered("root"); len(got) != 4 {
		t.Fatalf("restored covered list = %v, want four layers", got)
	}
	if _, err := restored.Thaw("root"); err != nil {
		t.Fatalf("thaw after restore: %v", err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	c := buildTree(t, testConfig())
	if _, err := c.Freeze("root"); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if err := c.Restore([]byte("<<<not a snapshot>>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(c.Tree("root")); got != 4 {
		t.Fatalf("rejected restore left %d cgroups, want 4", got)
	}
	node, err := c.Status("a1")
	if err != nil {
		t.Fatalf("status after rejected restore: %v", err)
	}
	if node.State != impl.StateFrozen {
		t.Fatalf("rejected restore changed the state of a1 to %q", node.State)
	}
	if _, err := c.Thaw("root"); err != nil {
		t.Fatalf("thaw after rejected restore: %v", err)
	}
}
