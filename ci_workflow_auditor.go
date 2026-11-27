// Package task audits pipeline configuration for externally reachable
// triggers, job level token permissions and expression interpolation points.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Severity levels attached to a finding.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
	SeverityInfo     = "info"
)

// Trigger classes returned by ClassifyTrigger.
const (
	TriggerExternal = "external"
	TriggerInternal = "internal"
	TriggerUnknown  = "unknown"
)

// Action classes returned by ClassifyAction.
const (
	ActionLocal    = "local"
	ActionDocker   = "docker"
	ActionInternal = "internal"
	ActionExternal = "external"
)

// Rule identifiers carried by a finding.
const (
	RuleMutableActionRef   = "mutable_action_ref"
	RuleExternalWriteToken = "external_write_token"
	RuleCheckoutWriteToken = "external_trigger_checkout_write"
	RuleExpressionInRun    = "expression_in_run"
	RuleExpressionInLog    = "expression_in_log"
	RuleMissingPermissions = "missing_permissions"
)

// Errors reported by the auditor.
var (
	ErrFileLimit       = errors.New("too many workspace files in one audit")
	ErrTotalBytes      = errors.New("workspace content exceeds the byte budget")
	ErrBadWorkflow     = errors.New("workflow document cannot be parsed")
	ErrNilSet          = errors.New("workflow set is nil")
	ErrUnknownTemplate = errors.New("job references a template that was not loaded")
)

// PermissionSet maps a token scope to its granted level.
type PermissionSet map[string]string

// Limits caps one audit run. A non positive field means unlimited.
type Limits struct {
	MaxFiles      int
	MaxTotalBytes int
}

// SourceFile is one configuration document fed into Load.
type SourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Step is a single step of a job or of a reusable template.
type Step struct {
	Name string            `json:"name"`
	Uses string            `json:"uses"`
	Run  string            `json:"run"`
	With map[string]string `json:"with"`
}

// Job is one job of a workflow.
type Job struct {
	ID          string        `json:"id"`
	Permissions PermissionSet `json:"permissions"`
	Steps       []Step        `json:"steps"`
	Template    string        `json:"template"`
}

// Workflow is one pipeline document unit.
type Workflow struct {
	ID          string        `json:"id"`
	Path        string        `json:"path"`
	Triggers    []string      `json:"triggers"`
	Permissions PermissionSet `json:"permissions"`
	Jobs        []Job         `json:"jobs"`
}

// Template is a reusable fragment shared by several callers.
type Template struct {
	ID          string        `json:"id"`
	Permissions PermissionSet `json:"permissions"`
	Steps       []Step        `json:"steps"`
}

// WorkflowSet is the merged result of all loaded documents.
type WorkflowSet struct {
	FileCount int
	Workflows []Workflow
	Templates map[string]Template
}

// Finding is one graded audit result.
type Finding struct {
	RuleID   string `json:"rule_id"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	JobID    string `json:"job_id"`
	Detail   string `json:"detail"`
}

// CiWorkflowAuditor holds the audit limits and the known internal owners.
type CiWorkflowAuditor struct {
	limits Limits
	orgs   []string
}

// NewCiWorkflowAuditor builds an auditor with the given caps and owner list.
func NewCiWorkflowAuditor(limits Limits, internalOrgs []string) *CiWorkflowAuditor {
	return &CiWorkflowAuditor{limits: limits, orgs: internalOrgs}
}

// Load parses every document, resolves template references and merges the
// result into a stable order.
func (a *CiWorkflowAuditor) Load(files []SourceFile) (*WorkflowSet, error) {
	return nil, errNotImplemented
}

// Audit grades the merged set and returns findings in the documented order.
func (a *CiWorkflowAuditor) Audit(set *WorkflowSet) ([]Finding, error) {
	return nil, errNotImplemented
}

// Report renders a one line summary followed by one line per finding.
func (a *CiWorkflowAuditor) Report(findings []Finding) string {
	return ""
}

// ClassifyTrigger maps an event name onto its reachability class.
func (a *CiWorkflowAuditor) ClassifyTrigger(trigger string) string {
	return ""
}

// MergePermissions overlays a reusable fragment on top of the caller.
func (a *CiWorkflowAuditor) MergePermissions(caller, overlay PermissionSet) PermissionSet {
	return nil
}

// HasWriteToken reports whether any scope carries the write level.
func (a *CiWorkflowAuditor) HasWriteToken(perms PermissionSet) bool {
	return false
}

// ClassifyAction separates local, container, in house and third party usage.
func (a *CiWorkflowAuditor) ClassifyAction(uses string) string {
	return ""
}

// IsMutableRef reports whether a step reference can move without a code change.
func (a *CiWorkflowAuditor) IsMutableRef(uses string) bool {
	return false
}

// Interpolations lists the expressions embedded in a piece of text, in order.
func (a *CiWorkflowAuditor) Interpolations(text string) []string {
	return nil
}

// SortFindings orders findings by severity, then location, then rule id.
func (a *CiWorkflowAuditor) SortFindings(findings []Finding) []Finding {
	return nil
}

// DangerousCombination grades the trigger, checkout and token combination.
func (a *CiWorkflowAuditor) DangerousCombination(trigger, uses string, perms PermissionSet) (string, bool) {
	return "", false
}
