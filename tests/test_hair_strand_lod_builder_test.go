package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-game-b-03"
)

func baseStrandLimits() impl.StrandLimits {
	return impl.StrandLimits{
		MaxClusters:        4,
		MaxGuides:          16,
		LevelCount:         4,
		LevelDistance:      100,
		VertexBudget:       1 << 20,
		MaxStrandsPerLevel: 0,
		StrandVertices:     8,
		CardVertices:       12,
		StrandsPerCard:     4,
		HysteresisRatio:    0.15,
	}
}

func sampleGuides() []impl.GuideStrand {
	return []impl.GuideStrand{
		{ID: "g-01", Bundle: "b-1", Section: "crown", Root: impl.Vec3{X: 0, Y: 10, Z: 0}, Strands: 8, Coverage: 0.9, WindPhase: 10},
		{ID: "g-02", Bundle: "b-1", Section: "crown", Root: impl.Vec3{X: 2, Y: 10, Z: 0}, Strands: 8, Coverage: 0.7, WindPhase: 30},
		{ID: "g-03", Bundle: "b-2", Section: "nape", Root: impl.Vec3{X: 100, Y: 0, Z: 0}, Strands: 4, Coverage: 0.5, WindPhase: 50},
	}
}

func clustered(t *testing.T) *impl.HairStrandLod {
	t.Helper()
	h := impl.NewHairStrandLod(baseStrandLimits())
	if _, err := h.Cluster(sampleGuides()); err != nil {
		t.Fatalf("cluster sample guides: %v", err)
	}
	return h
}

func TestClusterGroupsGuidesBySection(t *testing.T) {
	h := impl.NewHairStrandLod(baseStrandLimits())
	clusters, err := h.Cluster(sampleGuides())
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("clusters = %d, want one per section, got %+v", len(clusters), clusters)
	}
	crown := clusters[0]
	if crown.ID != "crown" || crown.Section != "crown" {
		t.Fatalf("first cluster = %q section %q, want crown", crown.ID, crown.Section)
	}
	if len(crown.Guides) != 2 || crown.Guides[0] != "g-01" || crown.Guides[1] != "g-02" {
		t.Fatalf("crown guides = %v, want [g-01 g-02]", crown.Guides)
	}
	if crown.Strands != 16 {
		t.Fatalf("crown strands = %d, want 16", crown.Strands)
	}
	if crown.Coverage < 0.8-1e-9 || crown.Coverage > 0.8+1e-9 {
		t.Fatalf("crown coverage = %v, want the mean 0.8", crown.Coverage)
	}
	if crown.Root != (impl.Vec3{X: 1, Y: 10, Z: 0}) {
		t.Fatalf("crown root = %+v, want the guide mean (1,10,0)", crown.Root)
	}
	if crown.Phase < 20-1e-9 || crown.Phase > 20+1e-9 {
		t.Fatalf("crown wind phase = %v, want the mean 20", crown.Phase)
	}
	nape := clusters[1]
	if nape.ID != "nape" || nape.Strands != 4 {
		t.Fatalf("second cluster = %q with %d strands, want nape with 4", nape.ID, nape.Strands)
	}
}

func TestClusterDeterministicAcrossInputOrder(t *testing.T) {
	forward := impl.NewHairStrandLod(baseStrandLimits())
	a, err := forward.Cluster(sampleGuides())
	if err != nil {
		t.Fatalf("forward cluster: %v", err)
	}
	reversed := sampleGuides()
	reversed[0], reversed[2] = reversed[2], reversed[0]
	backward := impl.NewHairStrandLod(baseStrandLimits())
	b, err := backward.Cluster(reversed)
	if err != nil {
		t.Fatalf("reversed cluster: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("cluster counts differ: %d then %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("cluster %d id = %q then %q, order must not depend on input order", i, a[i].ID, b[i].ID)
		}
		if len(a[i].Guides) != len(b[i].Guides) {
			t.Fatalf("cluster %q guide counts differ", a[i].ID)
		}
		for j := range a[i].Guides {
			if a[i].Guides[j] != b[i].Guides[j] {
				t.Fatalf("cluster %q guides = %v then %v", a[i].ID, a[i].Guides, b[i].Guides)
			}
		}
	}
	if got, ok := forward.ClusterOf("g-03"); !ok || got != "nape" {
		t.Fatalf("cluster of g-03 = %q ok=%v, want nape", got, ok)
	}
	if _, ok := forward.ClusterOf("g-404"); ok {
		t.Fatalf("unknown guide must not resolve to a cluster")
	}
}

func TestClusterRejectsBadInput(t *testing.T) {
	cases := map[string][]impl.GuideStrand{
		"empty": nil,
		"blank id": {
			{ID: " ", Section: "crown", Strands: 4, Coverage: 0.5},
		},
		"blank section": {
			{ID: "g-1", Section: "", Strands: 4, Coverage: 0.5},
		},
		"duplicate id": {
			{ID: "g-1", Section: "crown", Strands: 4, Coverage: 0.5},
			{ID: "g-1", Section: "nape", Strands: 4, Coverage: 0.5},
		},
		"coverage over one": {
			{ID: "g-1", Section: "crown", Strands: 4, Coverage: 1.2},
		},
		"zero strands": {
			{ID: "g-1", Section: "crown", Strands: 0, Coverage: 0.5},
		},
		"negative phase": {
			{ID: "g-1", Section: "crown", Strands: 4, Coverage: 0.5, WindPhase: -5},
		},
	}
	for name, guides := range cases {
		h := impl.NewHairStrandLod(baseStrandLimits())
		if _, err := h.Cluster(guides); !errors.Is(err, impl.ErrBadInput) {
			t.Fatalf("%s: cluster error = %v, want ErrBadInput", name, err)
		}
	}
}

func TestClusterEnforcesGuideLimit(t *testing.T) {
	limits := baseStrandLimits()
	limits.MaxGuides = 4
	h := impl.NewHairStrandLod(limits)
	guides := make([]impl.GuideStrand, 0, 5)
	for i := 0; i < 5; i++ {
		guides = append(guides, impl.GuideStrand{
			ID: "g-limit-" + string(rune('a'+i)), Section: "crown",
			Strands: 2, Coverage: 0.5,
		})
	}
	if _, err := h.Cluster(guides); !errors.Is(err, impl.ErrTooManyGuides) {
		t.Fatalf("five guides over a cap of four must fail with ErrTooManyGuides, got %v", err)
	}
	if _, ok := h.ClusterOf(guides[0].ID); ok {
		t.Fatalf("a rejected cluster batch must not be stored")
	}
}

func TestClusterEnforcesClusterLimit(t *testing.T) {
	limits := baseStrandLimits()
	limits.MaxClusters = 2
	h := impl.NewHairStrandLod(limits)
	guides := []impl.GuideStrand{
		{ID: "g-a", Section: "a", Strands: 2, Coverage: 0.5},
		{ID: "g-b", Section: "b", Strands: 2, Coverage: 0.5},
		{ID: "g-c", Section: "c", Strands: 2, Coverage: 0.5},
	}
	if _, err := h.Cluster(guides); !errors.Is(err, impl.ErrClusterLimit) {
		t.Fatalf("three sections over a cap of two must fail with ErrClusterLimit, got %v", err)
	}
}

func TestBuildLodHalvesStrandsPerLevel(t *testing.T) {
	h := clustered(t)
	wants := []struct {
		level   int
		strands int
		verts   int
	}{
		{0, 16, 128},
		{1, 8, 64},
		{2, 4, 32},
		{3, 2, 12},
	}
	prev := -1
	for _, want := range wants {
		mesh, err := h.BuildLod("crown", want.level)
		if err != nil {
			t.Fatalf("build lod level %d: %v", want.level, err)
		}
		if mesh.ClusterID != "crown" || mesh.Level != want.level {
			t.Fatalf("mesh identity = %q level %d, want crown level %d", mesh.ClusterID, mesh.Level, want.level)
		}
		if mesh.StrandCount != want.strands {
			t.Fatalf("level %d strand count = %d, want %d", want.level, mesh.StrandCount, want.strands)
		}
		if mesh.VertexCount != want.verts {
			t.Fatalf("level %d vertex count = %d, want %d", want.level, mesh.VertexCount, want.verts)
		}
		if prev >= 0 && mesh.VertexCount >= prev {
			t.Fatalf("level %d vertices %d do not shrink below the previous level %d", want.level, mesh.VertexCount, prev)
		}
		prev = mesh.VertexCount
		stored, ok := h.Mesh("crown", want.level)
		if !ok || stored.VertexCount != want.verts {
			t.Fatalf("stored mesh level %d = %+v ok=%v, want %d vertices", want.level, stored, ok, want.verts)
		}
	}
}

func TestBuildLodRejectsBadLevelAndUnknownCluster(t *testing.T) {
	h := clustered(t)
	for _, level := range []int{-1, 4, 99} {
		if _, err := h.BuildLod("crown", level); !errors.Is(err, impl.ErrBadLevel) {
			t.Fatalf("level %d must fail with ErrBadLevel, got %v", level, err)
		}
	}
	if _, err := h.BuildLod("ghost", 0); !errors.Is(err, impl.ErrUnknownCluster) {
		t.Fatalf("unknown cluster must fail with ErrUnknownCluster, got %v", err)
	}
}

func TestBuildLodMergesOverCap(t *testing.T) {
	limits := baseStrandLimits()
	limits.MaxStrandsPerLevel = 4
	h := impl.NewHairStrandLod(limits)
	guides := []impl.GuideStrand{
		{ID: "g-1", Section: "crown", Strands: 8, Coverage: 0.9},
		{ID: "g-2", Section: "crown", Strands: 8, Coverage: 0.8},
		{ID: "g-3", Section: "crown", Strands: 8, Coverage: 0.6},
		{ID: "g-4", Section: "crown", Strands: 8, Coverage: 0.5},
	}
	if _, err := h.Cluster(guides); err != nil {
		t.Fatalf("cluster: %v", err)
	}
	mesh, err := h.BuildLod("crown", 0)
	if err != nil {
		t.Fatalf("build lod: %v", err)
	}
	if mesh.StrandCount != 4 {
		t.Fatalf("strand count over a cap of four = %d, want 4", mesh.StrandCount)
	}
	if mesh.Merged != 28 {
		t.Fatalf("merged strands = %d, want 32-4 = 28", mesh.Merged)
	}
	if mesh.VertexCount != 32 {
		t.Fatalf("vertex count = %d, want 4*8 = 32", mesh.VertexCount)
	}
}

func TestBuildLodCardLevelVertices(t *testing.T) {
	h := impl.NewHairStrandLod(baseStrandLimits())
	guides := []impl.GuideStrand{
		{ID: "g-1", Section: "crown", Strands: 5, Coverage: 0.9},
		{ID: "g-2", Section: "crown", Strands: 5, Coverage: 0.8},
		{ID: "g-3", Section: "crown", Strands: 5, Coverage: 0.7},
		{ID: "g-4", Section: "crown", Strands: 5, Coverage: 0.6},
	}
	if _, err := h.Cluster(guides); err != nil {
		t.Fatalf("cluster: %v", err)
	}
	mesh, err := h.BuildLod("crown", 3)
	if err != nil {
		t.Fatalf("build lod level 3: %v", err)
	}
	if mesh.StrandCount != 3 {
		t.Fatalf("card level strand count = %d, want ceil(20/8) = 3", mesh.StrandCount)
	}
	if mesh.CardCount != 1 {
		t.Fatalf("card count = %d, want ceil(3/4) = 1", mesh.CardCount)
	}
	if mesh.VertexCount != 12 {
		t.Fatalf("card vertex count = %d, want 1*12 = 12", mesh.VertexCount)
	}
	mid, err := h.BuildLod("crown", 2)
	if err != nil {
		t.Fatalf("build lod level 2: %v", err)
	}
	if mid.CardCount != 0 || mid.VertexCount != 5*8 {
		t.Fatalf("level 2 mesh = %+v, want 5 strands and 40 vertices with no cards", mid)
	}
}

func TestSelectLodPicksLevelByDistance(t *testing.T) {
	h := clustered(t)
	view := impl.Camera{Position: impl.Vec3{}, Forward: impl.Vec3{Z: 1}}
	selections := h.SelectLod(view, 1<<20)
	if len(selections) != 2 {
		t.Fatalf("selections = %d, want 2", len(selections))
	}
	byID := map[string]impl.LodSelection{}
	for _, sel := range selections {
		byID[sel.ClusterID] = sel
	}
	crown, ok := byID["crown"]
	if !ok || crown.Level != 0 {
		t.Fatalf("crown selection = %+v ok=%v, want level 0 at distance ~10", crown, ok)
	}
	nape, ok := byID["nape"]
	if !ok || nape.Level != 1 {
		t.Fatalf("nape selection = %+v ok=%v, want level 1 at distance 100", nape, ok)
	}
	if crown.VertexCount != 128 {
		t.Fatalf("crown vertex count = %d, want the level 0 mesh 128", crown.VertexCount)
	}
	if nape.VertexCount != 16 {
		t.Fatalf("nape vertex count = %d, want the level 1 mesh 16", nape.VertexCount)
	}
}

func TestSelectLodRespectsBudget(t *testing.T) {
	h := clustered(t)
	view := impl.Camera{Position: impl.Vec3{}, Forward: impl.Vec3{Z: 1}}
	selections := h.SelectLod(view, 130)
	total := 0
	for _, sel := range selections {
		total += sel.VertexCount
	}
	if total > 130 {
		t.Fatalf("selection uses %d vertices over the budget 130: %+v", total, selections)
	}
	if len(selections) != 1 || selections[0].ClusterID != "crown" || selections[0].Level != 0 {
		t.Fatalf("selection = %+v, want the near crown cluster kept at level 0", selections)
	}
}

func TestSelectLodDeterministicOrder(t *testing.T) {
	h := clustered(t)
	view := impl.Camera{Position: impl.Vec3{X: 20, Y: 0, Z: 0}, Forward: impl.Vec3{X: 1}}
	first := h.SelectLod(view, 1<<20)
	second := h.SelectLod(view, 1<<20)
	if len(first) != len(second) || len(first) != 2 {
		t.Fatalf("selection sizes = %d then %d, want 2", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("selection %d = %+v then %+v, repeated calls must match", i, first[i], second[i])
		}
	}
	if first[0].ClusterID != "crown" || first[1].ClusterID != "nape" {
		t.Fatalf("selection order = %q,%q, want cluster id ascending", first[0].ClusterID, first[1].ClusterID)
	}
}

func TestSetFadeValidation(t *testing.T) {
	h := impl.NewHairStrandLod(baseStrandLimits())
	if err := h.SetFade(-time.Millisecond); !errors.Is(err, impl.ErrBadFade) {
		t.Fatalf("negative fade must fail with ErrBadFade, got %v", err)
	}
	if err := h.SetFade(0); err != nil {
		t.Fatalf("a zero fade must be accepted, got %v", err)
	}
	if err := h.SetFade(120 * time.Millisecond); err != nil {
		t.Fatalf("a normal fade must be accepted, got %v", err)
	}
	if got := h.Fades(); len(got) != 0 {
		t.Fatalf("fade windows before any switch = %+v, want none", got)
	}
	if got := h.AdvanceFade(60 * time.Millisecond); len(got) != 0 {
		t.Fatalf("advance with no active window = %+v, want none", got)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	h := clustered(t)
	mesh, err := h.BuildLod("crown", 1)
	if err != nil {
		t.Fatalf("build lod: %v", err)
	}
	if err := h.SetFade(80 * time.Millisecond); err != nil {
		t.Fatalf("set fade: %v", err)
	}
	data, err := h.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewHairStrandLod(baseStrandLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, ok := restored.ClusterOf("g-01"); !ok || got != "crown" {
		t.Fatalf("restored cluster of g-01 = %q ok=%v, want crown", got, ok)
	}
	got, ok := restored.Mesh("crown", 1)
	if !ok || got.StrandCount != mesh.StrandCount || got.VertexCount != mesh.VertexCount {
		t.Fatalf("restored mesh = %+v ok=%v, want %+v", got, ok, mesh)
	}
	if err := restored.Restore([]byte("hair")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := restored.ClusterOf("g-01"); !ok {
		t.Fatalf("failed restore wiped the live cluster table")
	}
}

func TestConcurrentBuildAndSelect(t *testing.T) {
	h := clustered(t)
	var wg sync.WaitGroup
	failures := make(chan string, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := h.BuildLod("crown", (i+j)%4); err != nil {
					failures <- "build lod: " + err.Error()
					return
				}
				sel := h.SelectLod(impl.Camera{Position: impl.Vec3{X: float64(i)}, Forward: impl.Vec3{Z: 1}}, 1<<20)
				if len(sel) != 2 {
					failures <- "selection lost clusters"
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Fatalf("concurrent lod work failed: %s", msg)
	}
	mesh, ok := h.Mesh("crown", 0)
	if !ok || mesh.VertexCount != 128 {
		t.Fatalf("mesh after concurrent builds = %+v ok=%v, want 128 vertices", mesh, ok)
	}
}
