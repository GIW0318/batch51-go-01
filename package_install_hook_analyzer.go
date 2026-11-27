// Package task grades the install hooks declared by a package manifest.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Hook stages.
const (
	StagePreinstall  = "preinstall"
	StagePostinstall = "postinstall"
)

// Capability names produced by DetectCapabilities.
const (
	CapNetworkDownload = "network_download"
	CapProcessExec     = "process_exec"
	CapCredentialRead  = "credential_read"
	CapEnvWrite        = "env_write"
	CapFileWrite       = "file_write"
)

// Assessment levels.
const (
	LevelCritical = "critical"
	LevelHigh     = "high"
	LevelMedium   = "medium"
	LevelLow      = "low"
)

// Assessment verdicts.
const (
	VerdictMalicious    = "malicious"
	VerdictSuspicious   = "suspicious"
	VerdictBenign       = "benign"
	VerdictUndetermined = "undetermined"
)

// Errors reported by the analyzer.
var (
	ErrBadStage      = errors.New("hook stage is not supported")
	ErrMissingScript = errors.New("hook script is not part of the package")
	ErrEmptyBlob     = errors.New("payload blob is empty")
	ErrBadDepth      = errors.New("depth budget must be positive")
)

// Manifest is the extracted package description.
type Manifest struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
	Hooks   []Hook            `json:"hooks"`
}

// Hook is one install time script declared by the manifest.
type Hook struct {
	Stage    string `json:"stage"`
	Script   string `json:"script"`
	Platform string `json:"platform"`
	Encoded  bool   `json:"encoded"`
}

// Evidence is one capability observed in one file.
type Evidence struct {
	Capability string `json:"capability"`
	Source     string `json:"source"`
	Snippet    string `json:"snippet"`
	Truncated  bool   `json:"truncated"`
}

// Assessment is the graded outcome for one package.
type Assessment struct {
	Package  string     `json:"package"`
	Score    int        `json:"score"`
	Level    string     `json:"level"`
	Verdict  string     `json:"verdict"`
	Opaque   bool       `json:"opaque"`
	Evidence []Evidence `json:"evidence"`
}

// Budget bounds decoding work.
type Budget struct {
	MaxDepth int
	MaxBytes int
}

// PackageInstallHookAnalyzer grades packages using the configured budget.
type PackageInstallHookAnalyzer struct {
	budget Budget
}

// NewPackageInstallHookAnalyzer builds an analyzer with the given budget.
func NewPackageInstallHookAnalyzer(budget Budget) *PackageInstallHookAnalyzer {
	return &PackageInstallHookAnalyzer{budget: budget}
}

// Extract validates the declared hooks and returns them in manifest order.
func (a *PackageInstallHookAnalyzer) Extract(m Manifest) ([]Hook, error) {
	return nil, errNotImplemented
}

// Score grades one package by capability combination.
func (a *PackageInstallHookAnalyzer) Score(m Manifest, hooks []Hook) (Assessment, error) {
	return Assessment{}, errNotImplemented
}

// Explain renders the assessment header and the evidence lines.
func (a *PackageInstallHookAnalyzer) Explain(assessment Assessment) string {
	return ""
}

// DecodeLayers peels encoded layers while the budget allows it.
func (a *PackageInstallHookAnalyzer) DecodeLayers(blob string, budget Budget) (string, int, bool, error) {
	return "", 0, false, errNotImplemented
}

// DetectCapabilities scans one file body and reports what it can do.
func (a *PackageInstallHookAnalyzer) DetectCapabilities(source, text string) []Evidence {
	return nil
}

// FollowScripts walks the scripts a hook pulls in.
func (a *PackageInstallHookAnalyzer) FollowScripts(m Manifest, entry string, maxDepth int) ([]string, error) {
	return nil, errNotImplemented
}

// ResolvePath resolves a reference relative to the file that contains it.
func (a *PackageInstallHookAnalyzer) ResolvePath(base, ref string) (string, bool) {
	return "", false
}

// CombineScore turns a capability set into one bounded score.
func (a *PackageInstallHookAnalyzer) CombineScore(evidence []Evidence) int {
	return 0
}

// SortedEvidence orders evidence and drops exact duplicates.
func (a *PackageInstallHookAnalyzer) SortedEvidence(evidence []Evidence) []Evidence {
	return nil
}

// Stages lists the distinct hook stages in first seen order.
func (a *PackageInstallHookAnalyzer) Stages(hooks []Hook) []string {
	return nil
}

// MergeEvidence appends two evidence lists while keeping the first one intact.
func (a *PackageInstallHookAnalyzer) MergeEvidence(left, right []Evidence) []Evidence {
	return nil
}
