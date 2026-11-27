package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-b-17"
)

func newBuilder(t *testing.T) *impl.UiBatchBuilder {
	t.Helper()
	b := impl.NewUiBatchBuilder()
	if err := b.SetCanvasSize(640, 480); err != nil {
		t.Fatalf("SetCanvasSize(640,480) must succeed, got %v", err)
	}
	return b
}

func quad(atlas, sampler, layer, verts int) impl.UiQuad {
	return impl.UiQuad{Atlas: atlas, Material: 0, Sampler: sampler, Layer: layer, Verts: verts}
}

func mustBuild(t *testing.T, b *impl.UiBatchBuilder) []impl.UiBatch {
	t.Helper()
	batches, err := b.Build()
	if err != nil {
		t.Fatalf("Build must succeed, got %v", err)
	}
	return batches
}

func TestResetRestartsBatchNumbering(t *testing.T) {
	b := newBuilder(t)
	b.Reset(5)
	for i := 0; i < 2; i++ {
		if err := b.AddQuad(quad(1, 0, 0, 4)); err != nil {
			t.Fatalf("AddQuad must succeed, got %v", err)
		}
	}
	batches := mustBuild(t, b)
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	if batches[0].Index != 0 {
		t.Fatalf("first batch index = %d, want 0", batches[0].Index)
	}
	if st := b.Stats(); st.Frame != 5 {
		t.Fatalf("stats frame = %d, want 5", st.Frame)
	}
	if err := b.AddQuad(quad(1, 0, 0, 4)); err != nil {
		t.Fatalf("AddQuad must succeed, got %v", err)
	}
	batches = mustBuild(t, b)
	if len(batches) != 1 || batches[0].Index != 1 {
		t.Fatalf("second build = %+v, want a single batch numbered 1", batches)
	}
	if got := b.BatchCount(); got != 1 {
		t.Fatalf("BatchCount = %d, want 1", got)
	}
	b.Reset(6)
	if err := b.AddQuad(quad(1, 0, 0, 4)); err != nil {
		t.Fatalf("AddQuad after Reset must succeed, got %v", err)
	}
	batches = mustBuild(t, b)
	if len(batches) != 1 || batches[0].Index != 0 {
		t.Fatalf("build after Reset = %+v, want a single batch numbered 0", batches)
	}
}

func TestAddQuadValidation(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	cases := []struct {
		name string
		q    impl.UiQuad
	}{
		{"zero verts", quad(0, 0, 0, 0)},
		{"negative verts", quad(0, 0, 0, -4)},
		{"negative atlas", quad(-1, 0, 0, 4)},
		{"negative layer", quad(0, 0, -2, 4)},
	}
	for _, tc := range cases {
		if err := b.AddQuad(tc.q); !errors.Is(err, impl.ErrBadQuad) {
			t.Fatalf("%s: AddQuad error = %v, want ErrBadQuad", tc.name, err)
		}
	}
	if err := b.AddQuad(quad(0, 0, 0, 4)); err != nil {
		t.Fatalf("valid quad must be accepted, got %v", err)
	}
}

func TestPushClipValidation(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	if err := b.PushClip(impl.Rect{X: 0, Y: 0, W: 10, H: 10}, 0); !errors.Is(err, impl.ErrBadClipToken) {
		t.Fatalf("PushClip with token 0 = %v, want ErrBadClipToken", err)
	}
	if err := b.PushClip(impl.Rect{X: 0, Y: 0, W: 0, H: 10}, 1); !errors.Is(err, impl.ErrBadClip) {
		t.Fatalf("PushClip with zero width = %v, want ErrBadClip", err)
	}
	if err := b.PushClip(impl.Rect{X: 0, Y: 0, W: 10, H: -1}, 1); !errors.Is(err, impl.ErrBadClip) {
		t.Fatalf("PushClip with negative height = %v, want ErrBadClip", err)
	}
	if err := b.PushClip(impl.Rect{X: 0, Y: 0, W: 10, H: 10}, 1); err != nil {
		t.Fatalf("valid clip must be accepted, got %v", err)
	}
	if err := b.PushClip(impl.Rect{X: 0, Y: 0, W: 10, H: 10}, 1); !errors.Is(err, impl.ErrBadClipToken) {
		t.Fatalf("duplicate token = %v, want ErrBadClipToken", err)
	}
}

func TestPopClipRejectsUnmatchedToken(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	if err := b.PopClip(1); !errors.Is(err, impl.ErrClipUnderflow) {
		t.Fatalf("PopClip on an empty stack = %v, want ErrClipUnderflow", err)
	}
	if err := b.PushClip(impl.Rect{X: 0, Y: 0, W: 100, H: 100}, 7); err != nil {
		t.Fatalf("PushClip must succeed, got %v", err)
	}
	if st := b.Stats(); st.ClipDepth != 1 {
		t.Fatalf("clip depth after push = %d, want 1", st.ClipDepth)
	}
	if err := b.PopClip(8); !errors.Is(err, impl.ErrClipMismatch) {
		t.Fatalf("PopClip with the wrong token = %v, want ErrClipMismatch", err)
	}
	if err := b.PopClip(7); err != nil {
		t.Fatalf("PopClip with the matching token must succeed, got %v", err)
	}
	if st := b.Stats(); st.ClipDepth != 0 {
		t.Fatalf("clip depth after pop = %d, want 0", st.ClipDepth)
	}
}

func TestClipRectMustFitCanvas(t *testing.T) {
	plain := impl.NewUiBatchBuilder()
	if err := plain.SetCanvasSize(0, 480); !errors.Is(err, impl.ErrBadCanvas) {
		t.Fatalf("SetCanvasSize(0,480) = %v, want ErrBadCanvas", err)
	}
	if err := plain.SetCanvasSize(640, -1); !errors.Is(err, impl.ErrBadCanvas) {
		t.Fatalf("SetCanvasSize(640,-1) = %v, want ErrBadCanvas", err)
	}
	if err := plain.SetCanvasSize(1280, 720); err != nil {
		t.Fatalf("SetCanvasSize(1280,720) must succeed, got %v", err)
	}
	b := newBuilder(t)
	b.Reset(1)
	if err := b.PushClip(impl.Rect{X: 90, Y: 10, W: 20, H: 20}, 1); !errors.Is(err, impl.ErrBadClip) {
		t.Fatalf("clip running past the canvas = %v, want ErrBadClip", err)
	}
	if err := b.PushClip(impl.Rect{X: -5, Y: 0, W: 10, H: 10}, 1); !errors.Is(err, impl.ErrBadClip) {
		t.Fatalf("clip starting outside the canvas = %v, want ErrBadClip", err)
	}
	if err := b.PushClip(impl.Rect{X: 0, Y: 0, W: 640, H: 480}, 1); err != nil {
		t.Fatalf("clip matching the canvas must be accepted, got %v", err)
	}
	if err := b.PopClip(1); err != nil {
		t.Fatalf("PopClip must succeed, got %v", err)
	}
	if st := b.Stats(); st.ClipDepth != 0 {
		t.Fatalf("clip depth = %d, want 0", st.ClipDepth)
	}
}

func TestSameStateQuadsMergeIntoOneBatch(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	for i := 0; i < 3; i++ {
		if err := b.AddQuad(quad(2, 1, 3, 4)); err != nil {
			t.Fatalf("AddQuad must succeed, got %v", err)
		}
	}
	batches := mustBuild(t, b)
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	got := batches[0]
	if got.Kind != impl.BatchKindQuad {
		t.Fatalf("batch kind = %q, want %q", got.Kind, impl.BatchKindQuad)
	}
	if got.Items != 3 {
		t.Fatalf("batch items = %d, want 3", got.Items)
	}
	if got.Verts != 12 {
		t.Fatalf("batch verts = %d, want 12", got.Verts)
	}
	if got.Atlas != 2 || got.Sampler != 1 || got.Layer != 3 {
		t.Fatalf("batch state = %+v, want atlas 2 sampler 1 layer 3", got)
	}
	if st := b.Stats(); st.Batches != 1 || st.Verts != 12 || st.Quads != 3 {
		t.Fatalf("stats = %+v, want 1 batch, 12 verts, 3 quads", st)
	}
}

func TestDifferentAtlasBreaksBatch(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	for _, atlas := range []int{5, 5, 6, 5} {
		if err := b.AddQuad(quad(atlas, 0, 0, 4)); err != nil {
			t.Fatalf("AddQuad must succeed, got %v", err)
		}
	}
	batches := mustBuild(t, b)
	if len(batches) != 3 {
		t.Fatalf("batches = %d, want 3", len(batches))
	}
	wantAtlas := []int{5, 6, 5}
	wantItems := []int{2, 1, 1}
	for i := range batches {
		if batches[i].Index != i {
			t.Fatalf("batch %d index = %d, want %d", i, batches[i].Index, i)
		}
		if batches[i].Atlas != wantAtlas[i] || batches[i].Items != wantItems[i] {
			t.Fatalf("batch %d = %+v, want atlas %d with %d items", i, batches[i], wantAtlas[i], wantItems[i])
		}
	}
}

func TestDifferentSamplerBreaksBatch(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	for _, sampler := range []int{0, 1, 1} {
		if err := b.AddQuad(quad(9, sampler, 0, 6)); err != nil {
			t.Fatalf("AddQuad must succeed, got %v", err)
		}
	}
	batches := mustBuild(t, b)
	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(batches))
	}
	if batches[0].Sampler != 0 || batches[0].Items != 1 {
		t.Fatalf("first batch = %+v, want sampler 0 with one item", batches[0])
	}
	if batches[1].Sampler != 1 || batches[1].Items != 2 {
		t.Fatalf("second batch = %+v, want sampler 1 with two items", batches[1])
	}
}

func TestAddTextRunBuildsSinglePageBatch(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	run := impl.TextRun{
		Font:  "ui-cjk",
		Layer: 4,
		Glyphs: []impl.Glyph{
			{Rune: 'a', Page: 2, Verts: 4},
			{Rune: 'b', Page: 2, Verts: 4},
			{Rune: 'c', Page: 2, Verts: 4},
		},
	}
	if err := b.AddTextRun(run); err != nil {
		t.Fatalf("AddTextRun must succeed, got %v", err)
	}
	batches := mustBuild(t, b)
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	got := batches[0]
	if got.Kind != impl.BatchKindText {
		t.Fatalf("batch kind = %q, want %q", got.Kind, impl.BatchKindText)
	}
	if got.Page != 2 {
		t.Fatalf("batch page = %d, want 2", got.Page)
	}
	if got.Layer != 4 {
		t.Fatalf("batch layer = %d, want 4", got.Layer)
	}
	if got.Items != 3 || got.Verts != 12 {
		t.Fatalf("batch = %d items / %d verts, want 3 / 12", got.Items, got.Verts)
	}
	if st := b.Stats(); st.TextRuns != 1 {
		t.Fatalf("stats text runs = %d, want 1", st.TextRuns)
	}
}

func TestAddTextRunValidation(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	if err := b.AddTextRun(impl.TextRun{Font: "ui", Layer: 0}); !errors.Is(err, impl.ErrBadText) {
		t.Fatalf("empty run = %v, want ErrBadText", err)
	}
	bad := impl.TextRun{Font: "ui", Layer: 0, Glyphs: []impl.Glyph{{Rune: 'x', Page: 1, Verts: 0}}}
	if err := b.AddTextRun(bad); !errors.Is(err, impl.ErrBadText) {
		t.Fatalf("run with a zero vertex glyph = %v, want ErrBadText", err)
	}
	negPage := impl.TextRun{Font: "ui", Layer: 0, Glyphs: []impl.Glyph{{Rune: 'x', Page: -1, Verts: 4}}}
	if err := b.AddTextRun(negPage); !errors.Is(err, impl.ErrBadText) {
		t.Fatalf("run with a negative page = %v, want ErrBadText", err)
	}
}

func TestBuildRejectsOverBatchBudget(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	if err := b.SetBudget(impl.UiBudget{MaxBatches: 0, MaxVerts: 100}); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("SetBudget with zero batches = %v, want ErrBadBudget", err)
	}
	if err := b.SetBudget(impl.UiBudget{MaxBatches: 1, MaxVerts: 1000}); err != nil {
		t.Fatalf("SetBudget must succeed, got %v", err)
	}
	if err := b.AddQuad(quad(1, 0, 0, 4)); err != nil {
		t.Fatalf("AddQuad must succeed, got %v", err)
	}
	if err := b.AddQuad(quad(2, 0, 0, 4)); err != nil {
		t.Fatalf("AddQuad must succeed, got %v", err)
	}
	if _, err := b.Build(); !errors.Is(err, impl.ErrBatchBudget) {
		t.Fatalf("Build over the batch cap = %v, want ErrBatchBudget", err)
	}
	if got := b.BatchCount(); got != 0 {
		t.Fatalf("BatchCount after a rejected build = %d, want 0", got)
	}
}

func TestBuildRejectsOverVertsBudget(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	if err := b.SetBudget(impl.UiBudget{MaxBatches: 8, MaxVerts: 5}); err != nil {
		t.Fatalf("SetBudget must succeed, got %v", err)
	}
	if err := b.AddQuad(quad(1, 0, 0, 6)); err != nil {
		t.Fatalf("AddQuad must succeed, got %v", err)
	}
	if _, err := b.Build(); !errors.Is(err, impl.ErrBatchBudget) {
		t.Fatalf("Build over the vertex cap = %v, want ErrBatchBudget", err)
	}
	if err := b.SetBudget(impl.UiBudget{MaxBatches: 8, MaxVerts: 6}); err != nil {
		t.Fatalf("SetBudget must succeed, got %v", err)
	}
	batches := mustBuild(t, b)
	if len(batches) != 1 || batches[0].Verts != 6 {
		t.Fatalf("build exactly at the vertex cap = %+v, want one batch with 6 verts", batches)
	}
}

func TestBuildRejectsUnbalancedClip(t *testing.T) {
	b := newBuilder(t)
	b.Reset(1)
	if err := b.PushClip(impl.Rect{X: 0, Y: 0, W: 100, H: 100}, 3); err != nil {
		t.Fatalf("PushClip must succeed, got %v", err)
	}
	if err := b.AddQuad(quad(1, 0, 0, 4)); err != nil {
		t.Fatalf("AddQuad must succeed, got %v", err)
	}
	if _, err := b.Build(); !errors.Is(err, impl.ErrClipUnbalanced) {
		t.Fatalf("Build with a clip left open = %v, want ErrClipUnbalanced", err)
	}
	if err := b.PopClip(3); err != nil {
		t.Fatalf("PopClip must succeed, got %v", err)
	}
	batches := mustBuild(t, b)
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	if batches[0].ClipToken != 3 {
		t.Fatalf("batch clip token = %d, want 3", batches[0].ClipToken)
	}
}

func TestSnapshotRestoreContinuesNumbering(t *testing.T) {
	b := newBuilder(t)
	b.Reset(9)
	if err := b.AddQuad(quad(1, 0, 0, 4)); err != nil {
		t.Fatalf("AddQuad must succeed, got %v", err)
	}
	if batches := mustBuild(t, b); len(batches) != 1 || batches[0].Index != 0 {
		t.Fatalf("first build = %+v, want one batch numbered 0", batches)
	}
	if err := b.AddQuad(quad(1, 0, 0, 4)); err != nil {
		t.Fatalf("AddQuad must succeed, got %v", err)
	}
	if batches := mustBuild(t, b); len(batches) != 1 || batches[0].Index != 1 {
		t.Fatalf("second build = %+v, want one batch numbered 1", batches)
	}
	data, err := b.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot must succeed, got %v", err)
	}
	other := impl.NewUiBatchBuilder()
	if err := other.Restore(data); err != nil {
		t.Fatalf("Restore must succeed, got %v", err)
	}
	if err := other.AddQuad(quad(1, 0, 0, 4)); err != nil {
		t.Fatalf("AddQuad after Restore must succeed, got %v", err)
	}
	batches := mustBuild(t, other)
	if len(batches) != 1 || batches[0].Index != 2 {
		t.Fatalf("build after Restore = %+v, want one batch numbered 2", batches)
	}
	if st := other.Stats(); st.Frame != 9 {
		t.Fatalf("restored frame = %d, want 9", st.Frame)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	b := newBuilder(t)
	if err := b.Restore([]byte{0x00, 0x01, 0x02, 0x03}); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("Restore of a corrupt payload = %v, want ErrBadSnapshot", err)
	}
	b.Reset(2)
	if err := b.AddQuad(quad(1, 0, 0, 4)); err != nil {
		t.Fatalf("builder must stay usable after a failed Restore, got %v", err)
	}
	batches := mustBuild(t, b)
	if len(batches) != 1 || batches[0].Index != 0 {
		t.Fatalf("build after a failed Restore = %+v, want one batch numbered 0", batches)
	}
}
