// Package task freezes and thaws a cgroup subtree and reports, layer by
// layer, how much of that subtree actually reached the requested state.
package task

import "errors"

// errNotImplemented is returned by behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

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

// FreezerCoordinator owns the cgroup tree and the freezer states.
type FreezerCoordinator struct {
	cfg FreezerConfig
}

// NewFreezerCoordinator builds a coordinator around the given config.
func NewFreezerCoordinator(cfg FreezerConfig) *FreezerCoordinator {
	return &FreezerCoordinator{cfg: cfg}
}

// AddCgroup registers one cgroup below the given parent, empty for a root.
func (c *FreezerCoordinator) AddCgroup(path, parent string, processes int) error {
	return errNotImplemented
}

// RemoveCgroup unregisters a leaf cgroup.
func (c *FreezerCoordinator) RemoveCgroup(path string) error {
	return errNotImplemented
}

// Freeze freezes the whole subtree of root and reports the aggregate outcome.
func (c *FreezerCoordinator) Freeze(root string) (FreezeResult, error) {
	return FreezeResult{}, errNotImplemented
}

// Thaw thaws the subtree of root in the reverse of the freeze order.
func (c *FreezerCoordinator) Thaw(root string) (ThawResult, error) {
	return ThawResult{}, errNotImplemented
}

// Status reads one cgroup node.
func (c *FreezerCoordinator) Status(path string) (Node, error) {
	return Node{}, errNotImplemented
}

// Tree lists the subtree of root ordered by depth and then by path.
func (c *FreezerCoordinator) Tree(root string) []Node {
	return nil
}

// MarkStuck records one process of the cgroup that will not reach a safe point.
func (c *FreezerCoordinator) MarkStuck(path string) error {
	return errNotImplemented
}

// Wake clears the stuck processes of the cgroup.
func (c *FreezerCoordinator) Wake(path string) error {
	return errNotImplemented
}

// Covered lists the paths of root that are frozen or on their way to frozen.
func (c *FreezerCoordinator) Covered(root string) []string {
	return nil
}

// Snapshot serialises the tree, the states and the counters.
func (c *FreezerCoordinator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole tree with a snapshot, all or nothing.
func (c *FreezerCoordinator) Restore(data []byte) error {
	return errNotImplemented
}
