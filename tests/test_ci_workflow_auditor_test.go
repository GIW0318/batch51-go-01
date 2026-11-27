package task

import (
	"errors"
	"strings"
	"testing"

	impl "gsb/batch51-go-safety-c-31"
)

func newAuditor() *impl.CiWorkflowAuditor {
	return impl.NewCiWorkflowAuditor(impl.Limits{MaxFiles: 8, MaxTotalBytes: 65536}, nil)
}

func findRule(findings []impl.Finding, rule string) (impl.Finding, bool) {
	for _, f := range findings {
		if f.RuleID == rule {
			return f, true
		}
	}
	return impl.Finding{}, false
}

func TestLoadMergesDocumentsInPathOrder(t *testing.T) {
	a := newAuditor()
	set, err := a.Load([]impl.SourceFile{
		{Path: "zeta.yml", Content: `{"workflows":[{"id":"deploy","triggers":["push"],"jobs":[]}]}`},
		{Path: "alpha.yml", Content: `{"workflows":[{"id":"build","triggers":["push"],"jobs":[{"id":"unit","steps":[{"name":"lint","uses":"./tools/lint"}]}]}]}`},
	})
	if err != nil {
		t.Fatalf("load must succeed, got %v", err)
	}
	if set == nil {
		t.Fatalf("load must return a workflow set")
	}
	if set.FileCount != 2 {
		t.Fatalf("file count = %d, want 2", set.FileCount)
	}
	if len(set.Workflows) != 2 {
		t.Fatalf("merged workflows = %d, want 2", len(set.Workflows))
	}
	if set.Workflows[0].ID != "build" || set.Workflows[0].Path != "alpha.yml" {
		t.Fatalf("first workflow = %q/%q, want build/alpha.yml forced by path order", set.Workflows[0].ID, set.Workflows[0].Path)
	}
	if set.Workflows[1].ID != "deploy" || set.Workflows[1].Path != "zeta.yml" {
		t.Fatalf("second workflow = %q/%q, want deploy/zeta.yml", set.Workflows[1].ID, set.Workflows[1].Path)
	}
	if len(set.Workflows[0].Jobs) != 1 || set.Workflows[0].Jobs[0].ID != "unit" {
		t.Fatalf("first workflow jobs = %+v, want exactly unit", set.Workflows[0].Jobs)
	}
}

func TestLoadRejectsTooManyFiles(t *testing.T) {
	a := impl.NewCiWorkflowAuditor(impl.Limits{MaxFiles: 2, MaxTotalBytes: 65536}, nil)
	_, err := a.Load([]impl.SourceFile{
		{Path: "a.yml", Content: `{"workflows":[]}`},
		{Path: "b.yml", Content: `{"workflows":[]}`},
		{Path: "c.yml", Content: `{"workflows":[]}`},
	})
	if !errors.Is(err, impl.ErrFileLimit) {
		t.Fatalf("third document must hit the file cap, got %v", err)
	}
}

func TestLoadRejectsOversizedContent(t *testing.T) {
	a := impl.NewCiWorkflowAuditor(impl.Limits{MaxFiles: 8, MaxTotalBytes: 24}, nil)
	_, err := a.Load([]impl.SourceFile{
		{Path: "a.yml", Content: `{"workflows":[{"id":"build"}]}`},
		{Path: "b.yml", Content: `{"workflows":[{"id":"deploy"}]}`},
	})
	if !errors.Is(err, impl.ErrTotalBytes) {
		t.Fatalf("total content over the budget must be rejected with ErrTotalBytes, got %v", err)
	}
}

func TestClassifyTriggerSplitsReachability(t *testing.T) {
	a := newAuditor()
	external := []string{"pull_request_target", "workflow_run", "issue_comment"}
	for _, name := range external {
		if got := a.ClassifyTrigger(name); got != impl.TriggerExternal {
			t.Fatalf("trigger %q classified as %q, want %q", name, got, impl.TriggerExternal)
		}
	}
	internal := []string{"push", "schedule", "workflow_dispatch"}
	for _, name := range internal {
		if got := a.ClassifyTrigger(name); got != impl.TriggerInternal {
			t.Fatalf("trigger %q classified as %q, want %q", name, got, impl.TriggerInternal)
		}
	}
	for _, name := range []string{"deployment_status", "repository_dispatch", ""} {
		if got := a.ClassifyTrigger(name); got != impl.TriggerUnknown {
			t.Fatalf("trigger %q classified as %q, want %q", name, got, impl.TriggerUnknown)
		}
	}
}

func TestMergePermissionsKeepsDisjointScopes(t *testing.T) {
	a := newAuditor()
	caller := impl.PermissionSet{"contents": "read"}
	overlay := impl.PermissionSet{"issues": "write"}
	merged := a.MergePermissions(caller, overlay)
	if len(merged) != 2 {
		t.Fatalf("merged permission scopes = %v, want contents and issues", merged)
	}
	if merged["contents"] != "read" || merged["issues"] != "write" {
		t.Fatalf("merged permissions = %v, want contents=read issues=write", merged)
	}
	if len(caller) != 1 || caller["contents"] != "read" {
		t.Fatalf("caller map was mutated: %v", caller)
	}
	if len(overlay) != 1 || overlay["issues"] != "write" {
		t.Fatalf("overlay map was mutated: %v", overlay)
	}
}

func TestHasWriteTokenDetectsWriteLevel(t *testing.T) {
	a := newAuditor()
	if !a.HasWriteToken(impl.PermissionSet{"contents": "read", "issues": "write"}) {
		t.Fatalf("a set holding one write scope must be reported as write capable")
	}
	if a.HasWriteToken(impl.PermissionSet{"contents": "read", "issues": "none"}) {
		t.Fatalf("read only scopes must not be reported as write capable")
	}
	if a.HasWriteToken(nil) {
		t.Fatalf("an empty permission set must not be write capable")
	}
}

func TestIsMutableRefDetectsFloatingAndPinnedRefs(t *testing.T) {
	a := newAuditor()
	if !a.IsMutableRef("actions/checkout@v4") {
		t.Fatalf("a version tag reference must be mutable")
	}
	if !a.IsMutableRef("actions/checkout@main") {
		t.Fatalf("a branch reference must be mutable")
	}
	if !a.IsMutableRef("acme/tool") {
		t.Fatalf("a reference without any revision must be mutable")
	}
	pinned := "actions/checkout@0123456789abcdef0123456789abcdef01234567"
	if a.IsMutableRef(pinned) {
		t.Fatalf("a forty character commit reference must not be mutable")
	}
}

func TestClassifyActionSeparatesLocalAndContainer(t *testing.T) {
	a := newAuditor()
	if got := a.ClassifyAction("./tools/lint"); got != impl.ActionLocal {
		t.Fatalf("in tree path classified as %q, want %q", got, impl.ActionLocal)
	}
	if got := a.ClassifyAction("docker://alpine:3.20"); got != impl.ActionDocker {
		t.Fatalf("container image classified as %q, want %q", got, impl.ActionDocker)
	}
	if got := a.ClassifyAction("acme/release@0123456789abcdef0123456789abcdef01234567"); got != impl.ActionExternal {
		t.Fatalf("unlisted owner classified as %q, want %q", got, impl.ActionExternal)
	}
}

func TestInterpolationsListsExpressionsInOrder(t *testing.T) {
	a := newAuditor()
	text := `run: echo ${{ github.event.issue.title }} && echo ${{ secrets.RELEASE_TOKEN }}`
	got := a.Interpolations(text)
	want := []string{"github.event.issue.title", "secrets.RELEASE_TOKEN"}
	if len(got) != len(want) {
		t.Fatalf("interpolations = %v, want %v", got, want)
	}
	for i, expr := range want {
		if got[i] != expr {
			t.Fatalf("interpolation %d = %q, want %q", i, got[i], expr)
		}
	}
	if len(a.Interpolations("echo plain")) != 0 {
		t.Fatalf("text without expressions must yield no interpolation")
	}
}

func TestSortFindingsOrdersBySeverityThenLocation(t *testing.T) {
	a := newAuditor()
	in := []impl.Finding{
		{RuleID: "rule_b", Severity: impl.SeverityHigh, Path: "zeta.yml", JobID: "j2"},
		{RuleID: "rule_c", Severity: impl.SeverityHigh, Path: "alpha.yml", JobID: "j1"},
		{RuleID: "rule_a", Severity: impl.SeverityCritical, Path: "alpha.yml", JobID: "j1"},
	}
	out := a.SortFindings(in)
	if len(out) != 3 {
		t.Fatalf("sorted findings = %d, want 3", len(out))
	}
	want := []string{"rule_a", "rule_c", "rule_b"}
	for i, rule := range want {
		if out[i].RuleID != rule {
			t.Fatalf("position %d holds %q, want %q (severity desc, then path, job, rule asc)", i, out[i].RuleID, rule)
		}
	}
}

func TestAuditFlagsMutableThirdPartyAction(t *testing.T) {
	a := newAuditor()
	set := &impl.WorkflowSet{
		FileCount: 1,
		Workflows: []impl.Workflow{{
			ID:       "build",
			Path:     "ci.yml",
			Triggers: []string{"push"},
			Jobs: []impl.Job{{
				ID:          "unit",
				Permissions: impl.PermissionSet{"contents": "read"},
				Steps:       []impl.Step{{Name: "lint", Uses: "acme/lint@v1"}},
			}},
		}},
	}
	findings, err := a.Audit(set)
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	f, ok := findRule(findings, impl.RuleMutableActionRef)
	if !ok {
		t.Fatalf("audit must report %s, got %+v", impl.RuleMutableActionRef, findings)
	}
	if f.Severity != impl.SeverityHigh {
		t.Fatalf("mutable third party reference graded %q, want %q", f.Severity, impl.SeverityHigh)
	}
	if f.Path != "ci.yml" || f.JobID != "unit" {
		t.Fatalf("finding location = %q/%q, want ci.yml/unit", f.Path, f.JobID)
	}
}

func TestAuditFlagsExternalTriggerWithWriteToken(t *testing.T) {
	a := newAuditor()
	set := &impl.WorkflowSet{
		FileCount: 1,
		Workflows: []impl.Workflow{{
			ID:          "pr",
			Path:        "pr.yml",
			Triggers:    []string{"pull_request_target"},
			Permissions: impl.PermissionSet{"contents": "write"},
			Jobs: []impl.Job{{
				ID:    "publish",
				Steps: []impl.Step{{Name: "noop", Uses: "./tools/noop"}},
			}},
		}},
	}
	findings, err := a.Audit(set)
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	f, ok := findRule(findings, impl.RuleExternalWriteToken)
	if !ok {
		t.Fatalf("audit must report %s, got %+v", impl.RuleExternalWriteToken, findings)
	}
	if f.Severity != impl.SeverityCritical {
		t.Fatalf("external trigger holding a write token graded %q, want %q", f.Severity, impl.SeverityCritical)
	}
}

func TestAuditFlagsExpressionInsideRunCommand(t *testing.T) {
	a := newAuditor()
	set := &impl.WorkflowSet{
		FileCount: 1,
		Workflows: []impl.Workflow{{
			ID:       "title",
			Path:     "title.yml",
			Triggers: []string{"issues"},
			Jobs: []impl.Job{{
				ID:          "print",
				Permissions: impl.PermissionSet{"contents": "read"},
				Steps: []impl.Step{{
					Name: "echo title",
					Run:  `printf '%s' "${{ github.event.issue.title }}"`,
				}},
			}},
		}},
	}
	findings, err := a.Audit(set)
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	f, ok := findRule(findings, impl.RuleExpressionInRun)
	if !ok {
		t.Fatalf("audit must report %s, got %+v", impl.RuleExpressionInRun, findings)
	}
	if f.Severity != impl.SeverityHigh {
		t.Fatalf("expression interpolated into a command graded %q, want %q", f.Severity, impl.SeverityHigh)
	}
}

func TestAuditRejectsNilWorkflowSet(t *testing.T) {
	a := newAuditor()
	if _, err := a.Audit(nil); !errors.Is(err, impl.ErrNilSet) {
		t.Fatalf("auditing a nil set must fail with ErrNilSet, got %v", err)
	}
}

func TestReportRendersSeverityCounts(t *testing.T) {
	a := newAuditor()
	findings := []impl.Finding{
		{RuleID: impl.RuleExternalWriteToken, Severity: impl.SeverityCritical, Path: "pr.yml", JobID: "publish"},
		{RuleID: impl.RuleExpressionInRun, Severity: impl.SeverityHigh, Path: "pr.yml", JobID: "print"},
	}
	out := a.Report(findings)
	if out == "" {
		t.Fatalf("report must not be empty")
	}
	first := strings.SplitN(out, "\n", 2)[0]
	want := "findings=2 critical=1 high=1 medium=0 low=0 info=0"
	if first != want {
		t.Fatalf("report header = %q, want %q", first, want)
	}
}
