// Package task looks for request handling components that were registered into
// a live process at runtime.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Region classes returned by ClassifyRegion.
const (
	RegionExec  = "exec"
	RegionData  = "data"
	RegionOther = "other"
)

// Registration kinds.
const (
	KindRuntimeRegister = "runtime_register"
	KindFrameworkReload = "framework_reload"
)

// Candidate verdicts.
const (
	VerdictConfirmed = "confirmed"
	VerdictBenign    = "benign"
	VerdictUncertain = "uncertain"
)

// ComponentSignature is the byte pattern a registered request handler chain
// leaves in the executable mapping.
var ComponentSignature = []byte("FILTER_CHAIN_ENTRY")

// Errors reported by the scanner.
var (
	ErrNilSnapshot = errors.New("process snapshot is nil")
	ErrTargetExited = errors.New("target process exited before the scan finished")
	ErrByteBudget  = errors.New("process memory exceeds the scan budget")
)

// MapRegion is one memory mapping of the target process.
type MapRegion struct {
	Base   uint64 `json:"base"`
	Size   int    `json:"size"`
	Perm   string `json:"perm"`
	Data   []byte `json:"-"`
	Remaps int    `json:"remaps"`
}

// Registration is one entry recorded by the target runtime.
type Registration struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Target string `json:"target"`
	From   uint64 `json:"from"`
}

// ProcessSnapshot is everything one scan run may look at.
type ProcessSnapshot struct {
	PID           int            `json:"pid"`
	Alive         bool           `json:"alive"`
	Regions       []MapRegion    `json:"regions"`
	Registrations []Registration `json:"registrations"`
	Declarations  []string       `json:"declarations"`
}

// Candidate is one component the scanner wants a human to look at.
type Candidate struct {
	Name     string   `json:"name"`
	Region   uint64   `json:"region"`
	Verdict  string   `json:"verdict"`
	Score    int      `json:"score"`
	Evidence []string `json:"evidence"`
}

// Limits caps one scan run. A non positive field means unlimited.
type Limits struct {
	MaxBytesPerProcess int
	ChunkBytes         int
}

// MemoryShellScanner keeps the limits and the last completed candidate list.
type MemoryShellScanner struct {
	limits Limits
	last   []Candidate
}

// NewMemoryShellScanner builds a scanner with the given caps.
func NewMemoryShellScanner(limits Limits) *MemoryShellScanner {
	return &MemoryShellScanner{limits: limits}
}

// Scan walks the snapshot and returns the candidates of this round.
func (s *MemoryShellScanner) Scan(snap *ProcessSnapshot) ([]Candidate, error) {
	return nil, errNotImplemented
}

// Verify re-checks one candidate against the snapshot it came from.
func (s *MemoryShellScanner) Verify(snap *ProcessSnapshot, c Candidate) (Candidate, error) {
	return Candidate{}, errNotImplemented
}

// Report renders a one line summary followed by one line per candidate.
func (s *MemoryShellScanner) Report(candidates []Candidate) string {
	return ""
}

// LastCandidates returns the candidates of the last completed scan.
func (s *MemoryShellScanner) LastCandidates() []Candidate {
	return nil
}

// ChunkPlan lists the read offsets of one region.
func (s *MemoryShellScanner) ChunkPlan(region MapRegion) []int {
	return nil
}

// ClassifyRegion maps a permission string onto a region class.
func (s *MemoryShellScanner) ClassifyRegion(perm string) string {
	return ""
}

// MatchSignature reports whether sig occurs inside data.
func (s *MemoryShellScanner) MatchSignature(data, sig []byte) bool {
	return false
}

// IsHotReload reports whether a registration came from framework reloading.
func (s *MemoryShellScanner) IsHotReload(reg Registration) bool {
	return false
}

// SortCandidates orders candidates by region base and then by name.
func (s *MemoryShellScanner) SortCandidates(candidates []Candidate) []Candidate {
	return nil
}

// RegisteredNames lists the registration names in the order they were seen.
func (s *MemoryShellScanner) RegisteredNames(regs []Registration) []string {
	return nil
}

// ByteCost sums the readable bytes of the given regions.
func (s *MemoryShellScanner) ByteCost(regions []MapRegion) int {
	return 0
}
