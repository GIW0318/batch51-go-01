// Package task looks for request handling components that were registered into
// a live process at runtime.
package task

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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
	ErrNilSnapshot  = errors.New("process snapshot is nil")
	ErrTargetExited = errors.New("target process exited before the scan finished")
	ErrByteBudget   = errors.New("process memory exceeds the scan budget")
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

// confirmedScore is awarded to candidates whose request chain signature is
// verified inside the mapping the registration points into.
const confirmedScore = 90

// MemoryShellScanner keeps the limits and the last completed candidate list.
type MemoryShellScanner struct {
	mu     sync.RWMutex
	limits Limits
	last   []Candidate
}

// NewMemoryShellScanner builds a scanner with the given caps.
func NewMemoryShellScanner(limits Limits) *MemoryShellScanner {
	return &MemoryShellScanner{limits: limits}
}

// Scan walks the snapshot and returns the candidates of this round.
func (s *MemoryShellScanner) Scan(snap *ProcessSnapshot) ([]Candidate, error) {
	if snap == nil {
		return nil, ErrNilSnapshot
	}
	if !snap.Alive {
		// Abort before any state changes so the previous complete result
		// remains available through LastCandidates.
		return nil, ErrTargetExited
	}
	if s.limits.MaxBytesPerProcess > 0 && s.ByteCost(snap.Regions) > s.limits.MaxBytesPerProcess {
		// Hard cap: reject the whole snapshot instead of returning a partial
		// candidate list.
		return nil, ErrByteBudget
	}

	found := make([]Candidate, 0, len(snap.Registrations))
	for _, reg := range snap.Registrations {
		region, ok := s.containingReadableRegion(snap.Regions, reg.From)
		if !ok {
			// The registration does not land in any readable mapping, so
			// there is nothing to inspect and no candidate is produced.
			continue
		}
		found = append(found, s.evaluateRegistration(reg, region))
	}
	found = s.SortCandidates(found)

	// Commit the complete round atomically. Concurrent scans can therefore
	// only ever expose the previous or the current full result.
	s.mu.Lock()
	s.last = cloneCandidates(found)
	s.mu.Unlock()
	return cloneCandidates(found), nil
}

// Verify re-checks one candidate against the snapshot it came from.
func (s *MemoryShellScanner) Verify(snap *ProcessSnapshot, c Candidate) (Candidate, error) {
	if snap == nil {
		return Candidate{}, ErrNilSnapshot
	}
	result := Candidate{Name: c.Name, Region: c.Region}
	region, ok := findRegionByBase(snap.Regions, c.Region)
	if !ok {
		result.Verdict = VerdictUncertain
		result.Evidence = []string{
			fmt.Sprintf("region %#x backing candidate %q is no longer present in the snapshot", c.Region, c.Name),
		}
		return result, nil
	}
	if !s.isReadable(region.Perm) || len(region.Data) == 0 {
		result.Verdict = VerdictUncertain
		result.Evidence = []string{
			fmt.Sprintf("region %#x cannot be read (perm=%q, remaps=%d)", region.Base, region.Perm, region.Remaps),
		}
		return result, nil
	}
	if s.MatchSignature(region.Data, ComponentSignature) {
		result.Verdict = VerdictConfirmed
		result.Score = confirmedScore
		result.Evidence = []string{
			fmt.Sprintf("request chain signature %q matched inside region %#x", string(ComponentSignature), region.Base),
			"verdict confirmed by the registered handler chain, not by the component name",
		}
		return result, nil
	}
	result.Verdict = VerdictBenign
	result.Evidence = []string{
		fmt.Sprintf("request chain signature %q not found inside region %#x", string(ComponentSignature), region.Base),
	}
	return result, nil
}

// Report renders a one line summary followed by one line per candidate.
func (s *MemoryShellScanner) Report(candidates []Candidate) string {
	var confirmed, benign, uncertain int
	for _, candidate := range candidates {
		switch candidate.Verdict {
		case VerdictConfirmed:
			confirmed++
		case VerdictBenign:
			benign++
		case VerdictUncertain:
			uncertain++
		}
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "candidates=%d confirmed=%d benign=%d uncertain=%d",
		len(candidates), confirmed, benign, uncertain)
	for _, candidate := range candidates {
		fmt.Fprintf(&builder, "\n- name=%s region=%#x verdict=%s score=%d",
			candidate.Name, candidate.Region, candidate.Verdict, candidate.Score)
	}
	return builder.String()
}

// LastCandidates returns the candidates of the last completed scan.
func (s *MemoryShellScanner) LastCandidates() []Candidate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneCandidates(s.last)
}

// ChunkPlan lists the read offsets of one region.
func (s *MemoryShellScanner) ChunkPlan(region MapRegion) []int {
	if region.Size <= 0 {
		return nil
	}
	chunk := s.limits.ChunkBytes
	if chunk <= 0 {
		return []int{0}
	}
	plan := make([]int, 0, (region.Size+chunk-1)/chunk)
	for offset := 0; offset < region.Size; offset += chunk {
		plan = append(plan, offset)
	}
	return plan
}

// ClassifyRegion maps a permission string onto a region class.
func (s *MemoryShellScanner) ClassifyRegion(perm string) string {
	if !strings.ContainsRune(perm, 'r') {
		return RegionOther
	}
	if strings.ContainsRune(perm, 'x') {
		return RegionExec
	}
	return RegionData
}

// MatchSignature reports whether sig occurs inside data.
func (s *MemoryShellScanner) MatchSignature(data, sig []byte) bool {
	return len(sig) > 0 && bytes.Contains(data, sig)
}

// IsHotReload reports whether a registration came from framework reloading.
func (s *MemoryShellScanner) IsHotReload(reg Registration) bool {
	return reg.Kind == KindFrameworkReload
}

// SortCandidates orders candidates by region base and then by name.
func (s *MemoryShellScanner) SortCandidates(candidates []Candidate) []Candidate {
	sorted := cloneCandidates(candidates)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Region != sorted[j].Region {
			return sorted[i].Region < sorted[j].Region
		}
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}

// RegisteredNames lists the registration names in the order they were seen.
func (s *MemoryShellScanner) RegisteredNames(regs []Registration) []string {
	names := make([]string, 0, len(regs))
	for _, reg := range regs {
		names = append(names, reg.Name)
	}
	return names
}

// ByteCost sums the readable bytes of the given regions.
func (s *MemoryShellScanner) ByteCost(regions []MapRegion) int {
	total := 0
	for _, region := range regions {
		if s.isReadable(region.Perm) {
			total += region.Size
		}
	}
	return total
}

// isReadable reports whether the permission string grants read access.
func (s *MemoryShellScanner) isReadable(perm string) bool {
	return strings.ContainsRune(perm, 'r')
}

// containingReadableRegion finds the readable mapping whose address range
// contains addr. Mappings without read permission are never considered.
func (s *MemoryShellScanner) containingReadableRegion(regions []MapRegion, addr uint64) (MapRegion, bool) {
	for _, region := range regions {
		if !s.isReadable(region.Perm) || region.Size <= 0 {
			continue
		}
		if addr >= region.Base && addr-region.Base < uint64(region.Size) {
			return region, true
		}
	}
	return MapRegion{}, false
}

// findRegionByBase locates a mapping by its base address.
func findRegionByBase(regions []MapRegion, base uint64) (MapRegion, bool) {
	for _, region := range regions {
		if region.Base == base {
			return region, true
		}
	}
	return MapRegion{}, false
}

// evaluateRegistration turns one registration into a candidate. The verdict
// is derived from the registration kind and the handler chain signature in
// memory; the component name is never used as matching evidence.
func (s *MemoryShellScanner) evaluateRegistration(reg Registration, region MapRegion) Candidate {
	candidate := Candidate{Name: reg.Name, Region: region.Base}

	data, ok := s.readRegion(region)
	if !ok {
		candidate.Verdict = VerdictUncertain
		candidate.Evidence = []string{
			fmt.Sprintf("region %#x was remapped %d time(s); chunk read failed after one retry", region.Base, region.Remaps),
			fmt.Sprintf("registration %q targeting chain %q at %#x can neither be confirmed nor cleared", reg.Name, reg.Target, reg.From),
		}
		return candidate
	}

	chainOffset := bytes.Index(data, ComponentSignature)
	if s.IsHotReload(reg) {
		candidate.Verdict = VerdictBenign
		candidate.Evidence = []string{
			fmt.Sprintf("registration %q has kind %s: framework hot-reload entry, classified as benign", reg.Name, reg.Kind),
			fmt.Sprintf("entry at %#x lives in region %#x and targets chain %q", reg.From, region.Base, reg.Target),
		}
		if chainOffset >= 0 {
			candidate.Evidence = append(candidate.Evidence,
				fmt.Sprintf("request chain signature located at offset %d inside region %#x", chainOffset, region.Base))
		}
		return candidate
	}

	if chainOffset >= 0 {
		candidate.Verdict = VerdictConfirmed
		candidate.Score = confirmedScore
		candidate.Evidence = []string{
			fmt.Sprintf("runtime registration %q targets request chain %q from %#x", reg.Name, reg.Target, reg.From),
			fmt.Sprintf("request chain signature %q matched at offset %d inside region %#x", string(ComponentSignature), chainOffset, region.Base),
			"verdict confirmed by registration chain linkage, not by component name or keywords",
		}
		return candidate
	}

	candidate.Verdict = VerdictBenign
	candidate.Evidence = []string{
		fmt.Sprintf("runtime registration %q targets request chain %q from %#x", reg.Name, reg.Target, reg.From),
		fmt.Sprintf("request chain signature %q not found inside region %#x; no in-memory chain linkage", string(ComponentSignature), region.Base),
	}
	return candidate
}

// readRegion replays the chunked read of one mapping and assembles the bytes
// that were captured. A failed chunk read is retried exactly once; if the
// retry also fails the mapping is considered unreadable.
func (s *MemoryShellScanner) readRegion(region MapRegion) ([]byte, bool) {
	plan := s.ChunkPlan(region)
	if len(plan) == 0 {
		return []byte{}, true
	}
	var buffer bytes.Buffer
	for index, offset := range plan {
		chunk, ok := s.readChunk(region, offset, s.chunkLength(region, index, plan))
		if !ok {
			return nil, false
		}
		buffer.Write(chunk)
	}
	return buffer.Bytes(), true
}

// readChunk performs one chunk read relative to the mapping base and retries
// once when the bytes are unavailable.
func (s *MemoryShellScanner) readChunk(region MapRegion, offset, length int) ([]byte, bool) {
	for attempt := 0; attempt < 2; attempt++ {
		if len(region.Data) == 0 {
			continue
		}
		if offset >= len(region.Data) {
			// The capture does not cover this chunk; nothing to inspect,
			// but this is not a read failure.
			return []byte{}, true
		}
		end := offset + length
		if end > len(region.Data) {
			end = len(region.Data)
		}
		return region.Data[offset:end], true
	}
	return nil, false
}

// chunkLength returns the byte length of the chunk at plan[index].
func (s *MemoryShellScanner) chunkLength(region MapRegion, index int, plan []int) int {
	if index+1 < len(plan) {
		return plan[index+1] - plan[index]
	}
	return region.Size - plan[index]
}

// cloneCandidates returns a deep copy so callers cannot mutate scanner state.
func cloneCandidates(in []Candidate) []Candidate {
	if in == nil {
		return nil
	}
	out := make([]Candidate, len(in))
	for i, candidate := range in {
		candidate.Evidence = append([]string(nil), candidate.Evidence...)
		out[i] = candidate
	}
	return out
}
