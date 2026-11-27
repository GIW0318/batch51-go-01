// Package task validates procedural dungeon layouts: key and door
// dependencies, entry to boss reachability, deadlock reporting, bounded budget
// analysis with resumable tokens and door and key only repair hints.
package task

import "errors"

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Room kinds.
const (
	RoomEntry  = "entry"
	RoomNormal = "normal"
	RoomKey    = "key"
	RoomBoss   = "boss"
)

// Repair kinds.
const (
	RepairAddDoor = "add_door"
	RepairAddKey  = "add_key"
)

// Errors returned by the validator.
var (
	ErrInvalidConfig  = errors.New("layout configuration is invalid")
	ErrRoomLimit      = errors.New("room count exceeds the configured limit")
	ErrEdgeLimit      = errors.New("door count exceeds the configured limit")
	ErrBadLayout      = errors.New("layout definition is invalid")
	ErrBudgetExceeded = errors.New("visit budget is exhausted")
	ErrBadToken       = errors.New("resume token is invalid")
	ErrInvalidBudget  = errors.New("resume budget must be positive")
	ErrUnknownSeed    = errors.New("seed has not been generated")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Room is one node of the dungeon.
type Room struct {
	ID   int    `json:"id"`
	Kind string `json:"kind"`
}

// KeyToken is one key lying in one room.
type KeyToken struct {
	ID   string `json:"id"`
	Room int    `json:"room"`
}

// Door is one passage between two rooms. A non empty Key means the passage
// needs that key; Bidirectional means it can be walked both ways.
type Door struct {
	ID            int    `json:"id"`
	From          int    `json:"from"`
	To            int    `json:"to"`
	Key           string `json:"key"`
	Bidirectional bool   `json:"bidirectional"`
}

// Layout is one fully described dungeon.
type Layout struct {
	Rooms   []Room     `json:"rooms"`
	Keys    []KeyToken `json:"keys"`
	Doors   []Door     `json:"doors"`
	Entry   int        `json:"entry"`
	Boss    int        `json:"boss"`
	Version int        `json:"version"`
}

// ResumeToken carries the confirmed reachable set of a truncated analysis.
type ResumeToken []byte

// Report is the outcome of one analysis.
type Report struct {
	Seed        uint64      `json:"seed"`
	Reachable   []int       `json:"reachable"`
	Unreachable []int       `json:"unreachable"`
	Locked      []int       `json:"locked"`
	MissingKeys []string    `json:"missing_keys"`
	Deadlocks   [][]int     `json:"deadlocks"`
	Visited     int         `json:"visited"`
	Truncated   bool        `json:"truncated"`
	Complete    bool        `json:"complete"`
	Token       ResumeToken `json:"-"`
}

// Graph is the deterministic dependency view of one generated layout.
type Graph struct {
	Nodes []int    `json:"nodes"`
	Edges [][2]int `json:"edges"`
	Order []int    `json:"order"`
}

// Repair is one suggested fix. Only doors and keys may be added.
type Repair struct {
	Kind string `json:"kind"`
	Door int    `json:"door"`
	Key  string `json:"key"`
	Room int    `json:"room"`
}

// Limits bounds the analysis work of one validator.
type Limits struct {
	MaxRooms int `json:"max_rooms"`
	MaxEdges int `json:"max_edges"`
	MaxVisit int `json:"max_visit"`
}

// LayoutConfig describes the layout to generate from a seed.
type LayoutConfig struct {
	RoomCount int `json:"room_count"`
	DoorCount int `json:"door_count"`
	KeyCount  int `json:"key_count"`
	Version   int `json:"version"`
}

// DungeonLayoutValidator owns the generated layouts and the analysis budget.
type DungeonLayoutValidator struct {
	limits Limits
}

// NewDungeonLayoutValidator builds a validator with the given limits.
func NewDungeonLayoutValidator(limits Limits) *DungeonLayoutValidator {
	return &DungeonLayoutValidator{limits: limits}
}

// Generate produces the deterministic layout of one seed.
func (v *DungeonLayoutValidator) Generate(seed uint64, cfg LayoutConfig) (Layout, error) {
	return Layout{}, errNotImplemented
}

// AnalyzeLayout validates one explicit layout.
func (v *DungeonLayoutValidator) AnalyzeLayout(layout Layout) (Report, error) {
	return Report{}, errNotImplemented
}

// Validate generates and analyses the layout of one seed in one call.
func (v *DungeonLayoutValidator) Validate(seed uint64, cfg LayoutConfig) (Report, error) {
	return Report{}, errNotImplemented
}

// DependencyGraph returns the deterministic graph of an already generated seed.
func (v *DungeonLayoutValidator) DependencyGraph(seed uint64) (Graph, error) {
	return Graph{}, errNotImplemented
}

// SuggestRepair lists door and key additions that would help a report.
func (v *DungeonLayoutValidator) SuggestRepair(report Report) []Repair {
	return nil
}

// Resume continues a truncated analysis from its token.
func (v *DungeonLayoutValidator) Resume(token ResumeToken, budget int) (Report, error) {
	return Report{}, errNotImplemented
}

// IsReachable reports whether a room is in the reachable set of a report.
func (v *DungeonLayoutValidator) IsReachable(report Report, room int) bool {
	return false
}

// LayoutOf reads a layout generated earlier.
func (v *DungeonLayoutValidator) LayoutOf(seed uint64) (Layout, bool) {
	return Layout{}, false
}

// KnownSeeds lists the seeds generated by this validator in ascending order.
func (v *DungeonLayoutValidator) KnownSeeds() []uint64 {
	return nil
}

// Snapshot serialises the generated layouts and their configuration versions.
func (v *DungeonLayoutValidator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (v *DungeonLayoutValidator) Restore(data []byte) error {
	return errNotImplemented
}
