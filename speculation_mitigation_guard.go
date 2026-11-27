// Package task plans and enforces per process speculative execution mitigations:
// it intersects a policy with the host CPU capabilities, keeps the mitigation
// level monotonic and hands the same level to every thread of a process.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// CPU vulnerability bits reported by the host profile.
const (
	VulnSpectre1 uint32 = 1 << 0
	VulnSpectre2 uint32 = 1 << 1
	VulnMeltdown uint32 = 1 << 2
	VulnMds      uint32 = 1 << 3
	VulnL1tf     uint32 = 1 << 4
)

// Mitigation feature names understood by Plan and Relax.
const (
	FeatRetpoline = "retpoline"
	FeatIbrs      = "ibrs"
	FeatIbpb      = "ibpb"
	FeatSsbd      = "ssbd"
	FeatMdClear   = "md_clear"
	FeatL1dFlush  = "l1d_flush"
	FeatVerw      = "verw"
)

// Feature weights used to score a mitigation level.
const (
	WeightRetpoline = 2
	WeightIbrs      = 2
	WeightIbpb      = 1
	WeightSsbd      = 2
	WeightMdClear   = 3
	WeightL1dFlush  = 1
	WeightVerw      = 2
)

// Errors returned by the guard.
var (
	ErrBadPolicy          = errors.New("mitigation policy is invalid")
	ErrBadProcess         = errors.New("process id is invalid")
	ErrUnsatisfiable      = errors.New("host cannot satisfy the mitigation policy")
	ErrUnknownProcess     = errors.New("process is not tracked")
	ErrDowngradeBlocked   = errors.New("mitigation level may only be raised")
	ErrBadReason          = errors.New("relaxation reason is required")
	ErrUnknownFeature     = errors.New("feature is not part of the current level")
	ErrRelaxWindowExpired = errors.New("relaxation window is closed")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// HostProfile is the CPU picture of one machine.
type HostProfile struct {
	Model     string   `json:"model"`
	Vulns     uint32   `json:"vulns"`
	Supported []string `json:"supported"`
}

// Policy is the mitigation contract a process must meet.
type Policy struct {
	Name     string   `json:"name"`
	Required []string `json:"required"`
	MinLevel int      `json:"min_level"`
}

// MitigationPlan is the resolved, explainable feature set.
type MitigationPlan struct {
	Policy      string   `json:"policy"`
	Level       int      `json:"level"`
	Features    []string `json:"features"`
	Missing     []string `json:"missing"`
	Satisfiable bool     `json:"satisfiable"`
	Digest      string   `json:"digest"`
}

// MitigationState is the live record of one process.
type MitigationState struct {
	PID          int       `json:"pid"`
	Level        int       `json:"level"`
	BaseLevel    int       `json:"base_level"`
	Features     []string  `json:"features"`
	Generation   int64     `json:"generation"`
	Relaxed      bool      `json:"relaxed"`
	RelaxFeature string    `json:"relax_feature"`
	RelaxUntil   time.Time `json:"relax_until"`
	RelaxCount   int       `json:"relax_count"`
}

// ExpiryEvent is emitted when a temporary relaxation falls back.
type ExpiryEvent struct {
	PID     int       `json:"pid"`
	Feature string    `json:"feature"`
	Level   int       `json:"level"`
	At      time.Time `json:"at"`
}

// GuardConfig bounds the relaxation windows.
type GuardConfig struct {
	MaxRelax     time.Duration
	DefaultLevel int
}

// SpeculationMitigationGuard owns the per process mitigation records.
type SpeculationMitigationGuard struct {
	cfg GuardConfig
}

// NewSpeculationMitigationGuard builds a guard with the given limits.
func NewSpeculationMitigationGuard(cfg GuardConfig) *SpeculationMitigationGuard {
	return &SpeculationMitigationGuard{cfg: cfg}
}

// FeatureWeight reports the level contribution of one feature name.
func FeatureWeight(name string) int {
	return 0
}

// Plan resolves a policy against a host and explains what is missing.
func (g *SpeculationMitigationGuard) Plan(host HostProfile, policy Policy) (MitigationPlan, error) {
	return MitigationPlan{}, errNotImplemented
}

// Apply installs a plan on one process.
func (g *SpeculationMitigationGuard) Apply(pid int, plan MitigationPlan) error {
	return errNotImplemented
}

// Read returns the live state of one process.
func (g *SpeculationMitigationGuard) Read(pid int) (MitigationState, error) {
	return MitigationState{}, errNotImplemented
}

// Relax drops one feature for a bounded window with an operator reason.
func (g *SpeculationMitigationGuard) Relax(pid int, feature string, reason string) error {
	return errNotImplemented
}

// Inherit copies the parent policy to a forked or exec'd child.
func (g *SpeculationMitigationGuard) Inherit(parent, child int) error {
	return errNotImplemented
}

// Exec re-checks a process after exec and keeps its state.
func (g *SpeculationMitigationGuard) Exec(pid int) (MitigationState, error) {
	return MitigationState{}, errNotImplemented
}

// Exit forgets a finished process.
func (g *SpeculationMitigationGuard) Exit(pid int) error {
	return errNotImplemented
}

// Tick expires the relaxation windows that are already over.
func (g *SpeculationMitigationGuard) Tick(now time.Time) []ExpiryEvent {
	return nil
}

// Snapshot serialises the process table and the relaxation deadlines.
func (g *SpeculationMitigationGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *SpeculationMitigationGuard) Restore(data []byte) error {
	return errNotImplemented
}
