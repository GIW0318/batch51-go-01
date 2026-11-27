// Package task freezes and thaws a cgroup subtree and reports, layer by
// layer, how much of that subtree actually reached the requested state.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Freezer states a cgroup can be in.
const (
	StateThawed   = "thawed"
	StateFreezing = "freezing"
	StateFrozen   = "frozen"
	StateThawing  = "thawing"
)

// Outcomes reported by Freeze.
const (
	OutcomeFrozen  = "frozen"
	OutcomePartial = "partial"
	OutcomeTimeout = "timeout"
)

// Errors reported by the coordinator.
var (
	ErrInvalidPath     = errors.New("cgroup path is not usable")
	ErrDuplicateCgroup = errors.New("cgroup already registered")
	ErrUnknownCgroup   = errors.New("unknown cgroup")
	ErrCgroupLimit     = errors.New("cgroup limit reached")
	ErrDepthLimit      = errors.New("cgroup depth limit reached")
	ErrHasChildren     = errors.New("cgroup still has children")
	ErrNotFrozen       = errors.New("subtree is not frozen")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Node is one cgroup of the tree.
type Node struct {
	Path      string
	Parent    string
	State     string
	Processes int
	Stuck     int
	Depth     int
}

// FreezeResult reports one freeze attempt.
type FreezeResult struct {
	Root       string
	Outcome    string
	Frozen     []string
	Pending    []string
	Uncovered  []string
	RolledBack []string
	Rounds     int
}

// ThawResult reports one thaw attempt.
type ThawResult struct {
	Root    string
	Thawed  []string
	Skipped []string
	Rounds  int
}

// FreezerConfig carries the hard bounds of the tree.
type FreezerConfig struct {
	MaxCgroups    int
	MaxDepth      int
	MaxRounds     int
	RoundObserver func(round int)
}

type cgroupNode struct {
	Node
	rounds int
}

// FreezerCoordinator owns the cgroup tree and the freezer states.
type FreezerCoordinator struct {
	cfg FreezerConfig

	opMu  sync.Mutex
	mu    sync.Mutex
	nodes map[string]*cgroupNode
	round int
}

// NewFreezerCoordinator builds a coordinator around the given config.
func NewFreezerCoordinator(cfg FreezerConfig) *FreezerCoordinator {
	return &FreezerCoordinator{
		cfg:   cfg,
		nodes: make(map[string]*cgroupNode),
	}
}

// AddCgroup registers one cgroup below the given parent, empty for a root.
func (c *FreezerCoordinator) AddCgroup(path, parent string, processes int) error {
	if !validPath(path) {
		return ErrInvalidPath
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.nodes[path]; ok {
		return ErrDuplicateCgroup
	}

	depth := 0
	if parent != "" {
		p, ok := c.nodes[parent]
		if !ok {
			return ErrUnknownCgroup
		}
		depth = p.Depth + 1
	}
	if depth > c.cfg.MaxDepth {
		return ErrDepthLimit
	}
	if len(c.nodes) >= c.cfg.MaxCgroups {
		return ErrCgroupLimit
	}

	c.nodes[path] = &cgroupNode{Node: Node{
		Path:      path,
		Parent:    parent,
		State:     StateThawed,
		Processes: processes,
		Depth:     depth,
	}}
	return nil
}

// RemoveCgroup unregisters a leaf cgroup.
func (c *FreezerCoordinator) RemoveCgroup(path string) error {
	if !validPath(path) {
		return ErrInvalidPath
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.nodes[path]; !ok {
		return ErrUnknownCgroup
	}
	for _, n := range c.nodes {
		if n.Parent == path {
			return ErrHasChildren
		}
	}
	delete(c.nodes, path)
	return nil
}

// Freeze freezes the whole subtree of root and reports the aggregate outcome.
func (c *FreezerCoordinator) Freeze(root string) (FreezeResult, error) {
	// Freeze and Thaw are whole-tree transactions. The operation mutex keeps
	// them serialized while the data mutex is only held for short, atomic
	// reads and the final commit. Callers therefore observe either the old
	// state or the committed state, never a half-placed/half-rolled-back tree.
	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.mu.Lock()
	if _, ok := c.nodes[root]; !ok {
		c.mu.Unlock()
		return FreezeResult{}, ErrUnknownCgroup
	}
	initial := cloneNodes(c.subtree(root))
	c.mu.Unlock()
	if len(initial) == 0 {
		return FreezeResult{}, ErrUnknownCgroup
	}
	initialSet := make(map[string]struct{}, len(initial))
	for _, n := range initial {
		initialSet[n.Path] = struct{}{}
	}

	allFrozen := true
	for _, n := range initial {
		if n.State != StateFrozen {
			allFrozen = false
			break
		}
	}
	if allFrozen {
		return FreezeResult{
			Root:    root,
			Outcome: OutcomeFrozen,
			Frozen:  sortedPaths(initial),
			Rounds:  1,
		}, nil
	}

	// Placement is deliberately a separate, top-down pass. Confirmation below
	// then walks from the leaves back to the root.
	for _, n := range initial {
		if n.State != StateFrozen {
			n.State = StateFreezing
			n.rounds++
		}
	}
	children := make(map[string][]*cgroupNode, len(initial))
	for _, n := range initial {
		if _, ok := initialSet[n.Parent]; ok {
			children[n.Parent] = append(children[n.Parent], n)
		}
	}

	roundLimit := c.cfg.MaxRounds

	rounds := 0
	for round := 1; round <= roundLimit; round++ {
		rounds = round
		if c.cfg.RoundObserver != nil {
			c.cfg.RoundObserver(round)
		}

		// MarkStuck and Wake are allowed while a round is being observed. Pick
		// up those facts before confirming this round's layers.
		c.mu.Lock()
		for _, n := range initial {
			if live, ok := c.nodes[n.Path]; ok {
				n.Stuck = live.Stuck
			}
		}
		c.mu.Unlock()

		for i := len(initial) - 1; i >= 0; i-- {
			n := initial[i]
			if n.State == StateFrozen || n.Stuck > 0 || !allChildrenFrozen(children[n.Path]) {
				continue
			}
			n.State = StateFrozen
		}
		if c.allFrozen(initial) {
			break
		}
	}

	// Commit the transaction in one critical section. Any cgroup registered
	// below root after the initial scan is reported as uncovered rather than
	// being silently treated as frozen.
	c.mu.Lock()
	defer c.mu.Unlock()
	c.round += rounds

	shadow := make(map[string]*cgroupNode, len(initial))
	surviving := make([]*cgroupNode, 0, len(initial))
	for _, n := range initial {
		shadow[n.Path] = n
		if _, ok := c.nodes[n.Path]; ok {
			surviving = append(surviving, n)
		}
	}

	uncoveredSet := make(map[string]struct{})
	for _, n := range c.subtree(root) {
		if _, ok := initialSet[n.Path]; !ok {
			uncoveredSet[n.Path] = struct{}{}
		}
	}
	for path := range uncoveredSet {
		parent := c.nodes[path].Parent
		for parent != "" {
			if n := shadow[parent]; n != nil {
				n.State = StateFreezing
			}
			live, ok := c.nodes[parent]
			if !ok {
				break
			}
			parent = live.Parent
		}
	}

	frozen := make([]string, 0, len(surviving))
	pending := make([]string, 0, len(surviving))
	for _, n := range surviving {
		if n.State == StateFrozen {
			frozen = append(frozen, n.Path)
		} else {
			pending = append(pending, n.Path)
		}
	}
	sort.Strings(frozen)
	sort.Strings(pending)
	uncovered := make([]string, 0, len(uncoveredSet))
	for path := range uncoveredSet {
		uncovered = append(uncovered, path)
	}
	sort.Strings(uncovered)

	res := FreezeResult{
		Root:      root,
		Frozen:    frozen,
		Pending:   pending,
		Uncovered: uncovered,
		Rounds:    rounds,
	}

	switch {
	case len(pending) == 0 && len(uncovered) == 0:
		res.Outcome = OutcomeFrozen
		res.Pending = nil
	case len(frozen) == 0:
		res.Outcome = OutcomeTimeout
		res.RolledBack = c.rollback(surviving)
		res.Pending = nil
	default:
		res.Outcome = OutcomePartial
	}

	for _, n := range surviving {
		live := c.nodes[n.Path]
		live.State = n.State
		live.rounds = n.rounds
	}
	return res, nil
}

// Thaw thaws the subtree of root in the reverse of the freeze order.
func (c *FreezerCoordinator) Thaw(root string) (ThawResult, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.mu.Lock()
	if _, ok := c.nodes[root]; !ok {
		c.mu.Unlock()
		return ThawResult{}, ErrUnknownCgroup
	}
	nodes := cloneNodes(c.subtree(root))
	c.mu.Unlock()
	if len(nodes) == 0 {
		return ThawResult{}, ErrUnknownCgroup
	}

	hasFrozen := false
	for _, n := range nodes {
		if n.State != StateThawed {
			hasFrozen = true
			break
		}
	}
	if !hasFrozen {
		return ThawResult{}, ErrNotFrozen
	}

	if c.cfg.RoundObserver != nil {
		c.cfg.RoundObserver(1)
	}

	// Release strictly from the deepest layer upwards. Within one layer a
	// stable path order keeps retries and audit records deterministic.
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Depth != nodes[j].Depth {
			return nodes[i].Depth > nodes[j].Depth
		}
		return nodes[i].Path < nodes[j].Path
	})
	res := ThawResult{Root: root, Rounds: 1}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.round++
	for _, n := range nodes {
		live, ok := c.nodes[n.Path]
		if !ok {
			continue
		}
		if live.State == StateThawed {
			res.Skipped = append(res.Skipped, n.Path)
			continue
		}
		live.State = StateThawed
		res.Thawed = append(res.Thawed, n.Path)
	}
	return res, nil
}

// Status reads one cgroup node.
func (c *FreezerCoordinator) Status(path string) (Node, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.nodes[path]
	if !ok {
		return Node{}, ErrUnknownCgroup
	}
	return n.Node, nil
}

// Tree lists the subtree of root ordered by depth and then by path.
func (c *FreezerCoordinator) Tree(root string) []Node {
	c.mu.Lock()
	defer c.mu.Unlock()

	nodes := c.subtree(root)
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Node)
	}
	return out
}

// MarkStuck records one process of the cgroup that will not reach a safe point.
func (c *FreezerCoordinator) MarkStuck(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.nodes[path]
	if !ok {
		return ErrUnknownCgroup
	}
	n.Stuck++
	return nil
}

// Wake clears the stuck processes of the cgroup.
func (c *FreezerCoordinator) Wake(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.nodes[path]
	if !ok {
		return ErrUnknownCgroup
	}
	n.Stuck = 0
	return nil
}

// Covered lists the paths of root that are frozen or on their way to frozen.
func (c *FreezerCoordinator) Covered(root string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	nodes := c.subtree(root)
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.State == StateFrozen || n.State == StateFreezing {
			out = append(out, n.Path)
		}
	}
	sort.Strings(out)
	return out
}

const snapshotVersion = 1

type snapshotNode struct {
	Path      string `json:"path"`
	Parent    string `json:"parent"`
	State     string `json:"state"`
	Processes int    `json:"processes"`
	Stuck     int    `json:"stuck"`
	Depth     int    `json:"depth"`
	Rounds    int    `json:"rounds"`
}

type snapshotPayload struct {
	Version int            `json:"version"`
	Round   int            `json:"round"`
	Nodes   []snapshotNode `json:"nodes"`
}

// Snapshot serialises the tree, the states and the counters.
func (c *FreezerCoordinator) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	payload := snapshotPayload{
		Version: snapshotVersion,
		Round:   c.round,
		Nodes:   make([]snapshotNode, 0, len(c.nodes)),
	}
	for _, n := range c.nodes {
		payload.Nodes = append(payload.Nodes, snapshotNode{
			Path:      n.Path,
			Parent:    n.Parent,
			State:     n.State,
			Processes: n.Processes,
			Stuck:     n.Stuck,
			Depth:     n.Depth,
			Rounds:    n.rounds,
		})
	}
	sort.Slice(payload.Nodes, func(i, j int) bool {
		return payload.Nodes[i].Path < payload.Nodes[j].Path
	})
	return json.Marshal(payload)
}

// Restore replaces the whole tree with a snapshot, all or nothing.
func (c *FreezerCoordinator) Restore(data []byte) error {
	payload, nodes, err := c.parseSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes = nodes
	c.round = payload.Round
	return nil
}

func (c *FreezerCoordinator) parseSnapshot(data []byte) (snapshotPayload, map[string]*cgroupNode, error) {
	var payload snapshotPayload
	if len(data) == 0 || json.Unmarshal(data, &payload) != nil {
		return snapshotPayload{}, nil, ErrBadSnapshot
	}
	if payload.Version != snapshotVersion || payload.Round < 0 {
		return snapshotPayload{}, nil, ErrBadSnapshot
	}
	if len(payload.Nodes) > c.cfg.MaxCgroups {
		return snapshotPayload{}, nil, ErrBadSnapshot
	}

	nodes := make(map[string]*cgroupNode, len(payload.Nodes))
	for _, sn := range payload.Nodes {
		if !validPath(sn.Path) || !validState(sn.State) || sn.Processes < 0 || sn.Stuck < 0 || sn.Rounds < 0 {
			return snapshotPayload{}, nil, ErrBadSnapshot
		}
		if _, ok := nodes[sn.Path]; ok {
			return snapshotPayload{}, nil, ErrBadSnapshot
		}
		nodes[sn.Path] = &cgroupNode{Node: Node{
			Path:      sn.Path,
			Parent:    sn.Parent,
			State:     sn.State,
			Processes: sn.Processes,
			Stuck:     sn.Stuck,
			Depth:     sn.Depth,
		}, rounds: sn.Rounds}
	}

	children := make(map[string][]string)
	for _, sn := range payload.Nodes {
		if sn.Parent != "" {
			if _, ok := nodes[sn.Parent]; !ok {
				return snapshotPayload{}, nil, ErrBadSnapshot
			}
			children[sn.Parent] = append(children[sn.Parent], sn.Path)
		}
	}

	queue := make([]string, 0, len(nodes))
	for path, n := range nodes {
		if n.Parent == "" {
			if n.Depth != 0 {
				return snapshotPayload{}, nil, ErrBadSnapshot
			}
			queue = append(queue, path)
		}
	}
	sort.Strings(queue)

	seen := make(map[string]struct{}, len(nodes))
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if _, ok := seen[path]; ok {
			return snapshotPayload{}, nil, ErrBadSnapshot
		}
		seen[path] = struct{}{}
		parent := nodes[path]
		if parent.Depth > c.cfg.MaxDepth {
			return snapshotPayload{}, nil, ErrBadSnapshot
		}
		childPaths := children[path]
		sort.Strings(childPaths)
		for _, childPath := range childPaths {
			child := nodes[childPath]
			if child.Depth != parent.Depth+1 {
				return snapshotPayload{}, nil, ErrBadSnapshot
			}
			queue = append(queue, childPath)
		}
	}
	if len(seen) != len(nodes) {
		return snapshotPayload{}, nil, ErrBadSnapshot
	}
	return payload, nodes, nil
}

func validPath(path string) bool {
	if strings.TrimSpace(path) == "" || strings.Contains(path, "..") {
		return false
	}
	return true
}

func validState(state string) bool {
	switch state {
	case StateThawed, StateFreezing, StateFrozen, StateThawing:
		return true
	default:
		return false
	}
}

// subtree returns root and all descendants ordered by depth and then path.
func (c *FreezerCoordinator) subtree(root string) []*cgroupNode {
	if _, ok := c.nodes[root]; !ok {
		return nil
	}
	children := c.childrenByParent()
	queue := []string{root}
	out := make([]*cgroupNode, 0)
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		n := c.nodes[path]
		if n == nil {
			continue
		}
		out = append(out, n)
		queue = append(queue, children[path]...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func (c *FreezerCoordinator) childrenByParent() map[string][]string {
	children := make(map[string][]string, len(c.nodes))
	for path, n := range c.nodes {
		if n.Parent != "" {
			children[n.Parent] = append(children[n.Parent], path)
		}
	}
	for parent := range children {
		sort.Strings(children[parent])
	}
	return children
}

func (c *FreezerCoordinator) allFrozen(nodes []*cgroupNode) bool {
	for _, n := range nodes {
		if n.State != StateFrozen {
			return false
		}
	}
	return true
}

func allChildrenFrozen(nodes []*cgroupNode) bool {
	for _, n := range nodes {
		if n.State != StateFrozen {
			return false
		}
	}
	return true
}

func cloneNodes(nodes []*cgroupNode) []*cgroupNode {
	out := make([]*cgroupNode, 0, len(nodes))
	for _, n := range nodes {
		clone := *n
		out = append(out, &clone)
	}
	return out
}

func (c *FreezerCoordinator) rollback(nodes []*cgroupNode) []string {
	ordered := append([]*cgroupNode(nil), nodes...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Depth != ordered[j].Depth {
			return ordered[i].Depth > ordered[j].Depth
		}
		return ordered[i].Path < ordered[j].Path
	})
	rolledBack := make([]string, 0, len(ordered))
	for _, n := range ordered {
		if n.State == StateThawed {
			continue
		}
		n.State = StateThawed
		rolledBack = append(rolledBack, n.Path)
	}
	return rolledBack
}

func sortedPaths(nodes []*cgroupNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Path)
	}
	sort.Strings(out)
	return out
}
