package task

import (
	"errors"
	"strings"
	"testing"

	impl "gsb/batch51-go-safety-c-33"
)

func scanLimits() impl.Limits {
	return impl.Limits{MaxBytesPerProcess: 1 << 20, ChunkBytes: 256}
}

func execRegion(base uint64, size int, data []byte) impl.MapRegion {
	return impl.MapRegion{Base: base, Size: size, Perm: "r-xp", Data: data}
}

func TestScanFindsRegisteredComponent(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	snap := &impl.ProcessSnapshot{
		PID:   4242,
		Alive: true,
		Regions: []impl.MapRegion{
			execRegion(0x1000, 4096, []byte("....FILTER_CHAIN_ENTRY....")),
		},
		Registrations: []impl.Registration{{
			Kind:   impl.KindRuntimeRegister,
			Name:   "FilterGate",
			Target: "HttpFilterChain",
			From:   0x1200,
		}},
	}
	cs, err := s.Scan(snap)
	if err != nil {
		t.Fatalf("scan must succeed, got %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("candidates = %d, want 1", len(cs))
	}
	if cs[0].Name != "FilterGate" {
		t.Fatalf("candidate name = %q, want FilterGate", cs[0].Name)
	}
	if cs[0].Region != 0x1000 {
		t.Fatalf("candidate region = %#x, want the mapping that holds the registration", cs[0].Region)
	}
	if cs[0].Verdict != impl.VerdictConfirmed {
		t.Fatalf("candidate verdict = %q, want %q", cs[0].Verdict, impl.VerdictConfirmed)
	}
}

func TestScanSkipsUnreadableRegion(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	snap := &impl.ProcessSnapshot{
		PID:     4243,
		Alive:   true,
		Regions: []impl.MapRegion{{Base: 0x5000, Size: 4096, Perm: "--p", Data: []byte("FILTER_CHAIN_ENTRY")}},
		Registrations: []impl.Registration{{
			Kind:   impl.KindRuntimeRegister,
			Name:   "Guard",
			Target: "HttpFilterChain",
			From:   0x5100,
		}},
	}
	cs, err := s.Scan(snap)
	if err != nil {
		t.Fatalf("scan must succeed, got %v", err)
	}
	if len(cs) != 0 {
		t.Fatalf("a mapping without read permission must not yield candidates, got %+v", cs)
	}
}

func TestScanRejectsByteBudget(t *testing.T) {
	s := impl.NewMemoryShellScanner(impl.Limits{MaxBytesPerProcess: 1024, ChunkBytes: 256})
	snap := &impl.ProcessSnapshot{
		PID:   4244,
		Alive: true,
		Regions: []impl.MapRegion{
			execRegion(0x1000, 2048, []byte("FILTER_CHAIN_ENTRY")),
			execRegion(0x9000, 2048, []byte("FILTER_CHAIN_ENTRY")),
		},
	}
	if _, err := s.Scan(snap); !errors.Is(err, impl.ErrByteBudget) {
		t.Fatalf("a snapshot over the per process budget must fail with ErrByteBudget, got %v", err)
	}
}

func TestScanAbortsWhenTargetExited(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	alive := &impl.ProcessSnapshot{
		PID:     4245,
		Alive:   true,
		Regions: []impl.MapRegion{execRegion(0x1000, 4096, []byte("FILTER_CHAIN_ENTRY"))},
		Registrations: []impl.Registration{{
			Kind:   impl.KindRuntimeRegister,
			Name:   "FilterGate",
			Target: "HttpFilterChain",
			From:   0x1100,
		}},
	}
	first, err := s.Scan(alive)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first scan candidates = %d, want 1", len(first))
	}
	dead := &impl.ProcessSnapshot{PID: 4245, Alive: false}
	if _, err := s.Scan(dead); !errors.Is(err, impl.ErrTargetExited) {
		t.Fatalf("scanning an exited target must fail with ErrTargetExited, got %v", err)
	}
	kept := s.LastCandidates()
	if len(kept) != 1 || kept[0].Name != "FilterGate" {
		t.Fatalf("candidates kept after the abort = %+v, want the previous FilterGate result", kept)
	}
}

func TestVerifyConfirmsSignature(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	snap := &impl.ProcessSnapshot{
		PID:     4246,
		Alive:   true,
		Regions: []impl.MapRegion{execRegion(0x2000, 4096, []byte("prefix-FILTER_CHAIN_ENTRY-suffix"))},
	}
	got, err := s.Verify(snap, impl.Candidate{Name: "Gate", Region: 0x2000, Verdict: impl.VerdictUncertain})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Verdict != impl.VerdictConfirmed {
		t.Fatalf("verdict = %q, want %q when the chain signature is present", got.Verdict, impl.VerdictConfirmed)
	}
	if len(got.Evidence) == 0 {
		t.Fatalf("a confirmed candidate must carry evidence, got %+v", got)
	}
	if got.Score <= 0 {
		t.Fatalf("a confirmed candidate must score above zero, got %d", got.Score)
	}
}

func TestVerifyMarksMissingSignatureBenign(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	snap := &impl.ProcessSnapshot{
		PID:     4247,
		Alive:   true,
		Regions: []impl.MapRegion{execRegion(0x3000, 4096, []byte("ordinary code path"))},
	}
	got, err := s.Verify(snap, impl.Candidate{Name: "Gate", Region: 0x3000, Verdict: impl.VerdictUncertain})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Verdict != impl.VerdictBenign {
		t.Fatalf("verdict = %q, want %q when the signature is absent", got.Verdict, impl.VerdictBenign)
	}
}

func TestChunkPlanSplitsByChunkSize(t *testing.T) {
	s := impl.NewMemoryShellScanner(impl.Limits{MaxBytesPerProcess: 1 << 20, ChunkBytes: 256})
	plan := s.ChunkPlan(impl.MapRegion{Base: 0x4000, Size: 900, Perm: "r-xp"})
	want := []int{0, 256, 512, 768}
	if len(plan) != len(want) {
		t.Fatalf("chunk plan = %v, want %v", plan, want)
	}
	for i, off := range want {
		if plan[i] != off {
			t.Fatalf("chunk plan = %v, want %v", plan, want)
		}
	}
}

func TestClassifyRegionKinds(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	if got := s.ClassifyRegion("r-xp"); got != impl.RegionExec {
		t.Fatalf("r-xp classified as %q, want %q", got, impl.RegionExec)
	}
	if got := s.ClassifyRegion("rw-p"); got != impl.RegionData {
		t.Fatalf("rw-p classified as %q, want %q", got, impl.RegionData)
	}
	if got := s.ClassifyRegion("r--p"); got != impl.RegionData {
		t.Fatalf("r--p classified as %q, want %q", got, impl.RegionData)
	}
	if got := s.ClassifyRegion("--p"); got != impl.RegionOther {
		t.Fatalf("--p classified as %q, want %q", got, impl.RegionOther)
	}
}

func TestMatchSignatureFindsPattern(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	data := []byte{0x00, 0x01, 0xca, 0xfe, 0xba, 0xbe, 0x02}
	if !s.MatchSignature(data, []byte{0xca, 0xfe, 0xba, 0xbe}) {
		t.Fatalf("a signature present in the mapping must be found")
	}
	if s.MatchSignature(data, []byte{0xde, 0xad}) {
		t.Fatalf("a signature absent from the mapping must not be reported")
	}
	if s.MatchSignature(data, nil) {
		t.Fatalf("an empty signature must not match anything")
	}
}

func TestIsHotReloadDetectsFrameworkReload(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	if !s.IsHotReload(impl.Registration{Kind: impl.KindFrameworkReload, Name: "Router"}) {
		t.Fatalf("a framework reload registration must be recognised as hot reload")
	}
	if s.IsHotReload(impl.Registration{Kind: impl.KindRuntimeRegister, Name: "Router"}) {
		t.Fatalf("a runtime registration must not be treated as framework hot reload")
	}
}

func TestSortCandidatesByRegionThenName(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	in := []impl.Candidate{
		{Name: "beta", Region: 0x3000},
		{Name: "zeta", Region: 0x1000},
		{Name: "alpha", Region: 0x1000},
	}
	out := s.SortCandidates(in)
	if len(out) != 3 {
		t.Fatalf("sorted candidates = %d, want 3", len(out))
	}
	want := []string{"alpha", "zeta", "beta"}
	for i, name := range want {
		if out[i].Name != name {
			t.Fatalf("position %d holds %q, want %q", i, out[i].Name, name)
		}
	}
}

func TestReportCountsVerdicts(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	out := s.Report([]impl.Candidate{
		{Name: "Gate", Region: 0x1000, Verdict: impl.VerdictConfirmed},
		{Name: "Router", Region: 0x2000, Verdict: impl.VerdictBenign},
	})
	if out == "" {
		t.Fatalf("report must not be empty")
	}
	first := strings.SplitN(out, "\n", 2)[0]
	want := "candidates=2 confirmed=1 benign=1 uncertain=0"
	if first != want {
		t.Fatalf("report header = %q, want %q", first, want)
	}
}

func TestScanEmptySnapshotHasNoCandidates(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	cs, err := s.Scan(&impl.ProcessSnapshot{PID: 4248, Alive: true})
	if err != nil {
		t.Fatalf("an empty snapshot must scan cleanly, got %v", err)
	}
	if len(cs) != 0 {
		t.Fatalf("empty snapshot candidates = %+v, want none", cs)
	}
}

func TestRegisteredNamesKeepsOrder(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	names := s.RegisteredNames([]impl.Registration{
		{Kind: impl.KindRuntimeRegister, Name: "alpha"},
		{Kind: impl.KindFrameworkReload, Name: "beta"},
	})
	if len(names) != 2 {
		t.Fatalf("registered names = %v, want two entries", names)
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("registered names = %v, want alpha then beta", names)
	}
}

func TestScanRejectsNilSnapshot(t *testing.T) {
	s := impl.NewMemoryShellScanner(scanLimits())
	if _, err := s.Scan(nil); !errors.Is(err, impl.ErrNilSnapshot) {
		t.Fatalf("scanning a nil snapshot must fail with ErrNilSnapshot, got %v", err)
	}
}
