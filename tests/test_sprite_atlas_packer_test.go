package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-b-12"
)

func smallAtlas(t *testing.T) *impl.SpriteAtlasPacker {
	t.Helper()
	p, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{
		PageWidth:  256,
		PageHeight: 256,
		MaxPages:   4,
		Padding:    4,
		MaxArea:    1 << 30,
		MaxSprite:  256,
	})
	if err != nil {
		t.Fatalf("new atlas packer: %v", err)
	}
	return p
}

func mustAdd(t *testing.T, p *impl.SpriteAtlasPacker, s impl.SpriteReq) impl.Placement {
	t.Helper()
	pl, err := p.Add(s)
	if err != nil {
		t.Fatalf("add %s: %v", s.ID, err)
	}
	return pl
}

func TestAddPlacesFirstSpriteAtOriginAndPadsShelf(t *testing.T) {
	p := smallAtlas(t)
	first := mustAdd(t, p, impl.SpriteReq{ID: "hero-idle", Width: 100, Height: 40})
	if first.Page != 0 {
		t.Fatalf("first sprite page = %d, want 0", first.Page)
	}
	if first.X != 0 || first.Y != 0 {
		t.Fatalf("first sprite origin = (%d,%d), want (0,0)", first.X, first.Y)
	}
	if first.Width != 100 || first.Height != 40 || first.Rotated {
		t.Fatalf("first sprite slot = %dx%d rotated=%v, want 100x40 plain", first.Width, first.Height, first.Rotated)
	}
	if first.PageWidth != 256 || first.PageHeight != 256 {
		t.Fatalf("first sprite page size = %dx%d, want 256x256", first.PageWidth, first.PageHeight)
	}
	if first.U0 != 0 || first.V0 != 0 {
		t.Fatalf("first sprite uv origin = (%v,%v), want (0,0)", first.U0, first.V0)
	}
	if diff := first.U1 - 0.390625; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("first sprite u1 = %v, want 100/256", first.U1)
	}
	if diff := first.V1 - 0.15625; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("first sprite v1 = %v, want 40/256", first.V1)
	}
	second := mustAdd(t, p, impl.SpriteReq{ID: "hero-run", Width: 60, Height: 40})
	if second.Page != 0 || second.Y != 0 {
		t.Fatalf("second sprite page=%d y=%d, want page 0 on the same shelf", second.Page, second.Y)
	}
	if second.X != 104 {
		t.Fatalf("second sprite x = %d, want 100+4 padding = 104", second.X)
	}
	if got := p.PageCount(); got != 1 {
		t.Fatalf("page count = %d, want 1", got)
	}
}

func TestAddRejectsBadSpriteAndBadConfig(t *testing.T) {
	if _, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{PageWidth: 0, PageHeight: 256, MaxPages: 2, MaxArea: 1 << 20, MaxSprite: 128}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero page width must fail with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{PageWidth: 128, PageHeight: 128, MaxPages: 0, MaxArea: 1 << 20, MaxSprite: 64}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero page budget must fail with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{PageWidth: 128, PageHeight: 128, MaxPages: 2, Padding: -1, MaxArea: 1 << 20, MaxSprite: 64}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("negative padding must fail with ErrBadConfig, got %v", err)
	}
	p := smallAtlas(t)
	bad := []impl.SpriteReq{
		{ID: "   ", Width: 32, Height: 32},
		{ID: "zero-width", Width: 0, Height: 32},
		{ID: "negative", Width: 32, Height: -8},
		{ID: "too-wide", Width: 300, Height: 32},
	}
	for i, s := range bad {
		if _, err := p.Add(s); !errors.Is(err, impl.ErrBadSprite) {
			t.Fatalf("bad sprite %d must fail with ErrBadSprite, got %v", i, err)
		}
	}
	if got := p.Stats().Sprites; got != 0 {
		t.Fatalf("rejected requests must not be packed, got %d sprites", got)
	}
}

func TestAddRejectsDuplicateID(t *testing.T) {
	p := smallAtlas(t)
	first := mustAdd(t, p, impl.SpriteReq{ID: "icon-a", Width: 32, Height: 32})
	_, err := p.Add(impl.SpriteReq{ID: "icon-a", Width: 64, Height: 64})
	if !errors.Is(err, impl.ErrDuplicateSprite) {
		t.Fatalf("duplicate id must fail with ErrDuplicateSprite, got %v", err)
	}
	again, ok := p.Placement("icon-a")
	if !ok {
		t.Fatalf("the original placement must stay published")
	}
	if again != first {
		t.Fatalf("a duplicate add must not move the original placement: got %+v want %+v", again, first)
	}
	if got := p.Stats().Sprites; got != 1 {
		t.Fatalf("sprite count = %d, want 1", got)
	}
}

func TestAddWrapsToNewShelfAndPage(t *testing.T) {
	p, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{
		PageWidth:  100,
		PageHeight: 100,
		MaxPages:   4,
		Padding:    0,
		MaxArea:    1 << 30,
		MaxSprite:  100,
	})
	if err != nil {
		t.Fatalf("new atlas packer: %v", err)
	}
	mustAdd(t, p, impl.SpriteReq{ID: "f0", Width: 100, Height: 40})
	mustAdd(t, p, impl.SpriteReq{ID: "f1", Width: 100, Height: 40})
	third := mustAdd(t, p, impl.SpriteReq{ID: "f2", Width: 100, Height: 40})
	if third.Page != 1 {
		t.Fatalf("third row sprite page = %d, want a fresh page 1", third.Page)
	}
	if third.X != 0 || third.Y != 0 {
		t.Fatalf("third row sprite origin = (%d,%d), want (0,0) on page 1", third.X, third.Y)
	}
	if got := p.PageCount(); got != 2 {
		t.Fatalf("page count = %d, want 2", got)
	}
	st := p.Stats()
	if st.Sprites != 3 || st.Pages != 2 {
		t.Fatalf("stats sprites=%d pages=%d, want 3 and 2", st.Sprites, st.Pages)
	}
}

func TestPageBudgetRejectsExtraPages(t *testing.T) {
	p, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{
		PageWidth:  100,
		PageHeight: 100,
		MaxPages:   1,
		Padding:    0,
		MaxArea:    1 << 30,
		MaxSprite:  100,
	})
	if err != nil {
		t.Fatalf("new atlas packer: %v", err)
	}
	mustAdd(t, p, impl.SpriteReq{ID: "p0", Width: 100, Height: 50})
	mustAdd(t, p, impl.SpriteReq{ID: "p1", Width: 100, Height: 50})
	if _, err := p.Add(impl.SpriteReq{ID: "p2", Width: 100, Height: 10}); !errors.Is(err, impl.ErrPageBudget) {
		t.Fatalf("third page must fail with ErrPageBudget, got %v", err)
	}
	if got := p.PageCount(); got != 1 {
		t.Fatalf("page count after the rejected request = %d, want 1", got)
	}
	if _, ok := p.Placement("p2"); ok {
		t.Fatalf("rejected sprite must not be published")
	}
}

func TestAreaBudgetRejectsExtraPage(t *testing.T) {
	p, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{
		PageWidth:  100,
		PageHeight: 100,
		MaxPages:   8,
		Padding:    0,
		MaxArea:    20000,
		MaxSprite:  100,
	})
	if err != nil {
		t.Fatalf("new atlas packer: %v", err)
	}
	mustAdd(t, p, impl.SpriteReq{ID: "a0", Width: 100, Height: 100})
	mustAdd(t, p, impl.SpriteReq{ID: "a1", Width: 100, Height: 100})
	if _, err := p.Add(impl.SpriteReq{ID: "a2", Width: 100, Height: 100}); !errors.Is(err, impl.ErrAreaBudget) {
		t.Fatalf("a third page of 10000 pixels must fail with ErrAreaBudget, got %v", err)
	}
	st := p.Stats()
	if st.Pages != 2 {
		t.Fatalf("pages after the rejected request = %d, want 2", st.Pages)
	}
}

func TestAddBatchAllOrNothing(t *testing.T) {
	p := smallAtlas(t)
	batch := []impl.SpriteReq{
		{ID: "b0", Width: 32, Height: 32},
		{ID: "b1", Width: 32, Height: 32},
		{ID: "b0", Width: 64, Height: 64},
	}
	if _, err := p.AddBatch(batch); !errors.Is(err, impl.ErrBatchRejected) {
		t.Fatalf("a batch with a repeated id must fail with ErrBatchRejected, got %v", err)
	}
	if got := p.Stats().Sprites; got != 0 {
		t.Fatalf("a rejected batch must leave the atlas empty, got %d sprites", got)
	}
	if got := p.PageCount(); got != 0 {
		t.Fatalf("a rejected batch must not open pages, got %d", got)
	}
	badSize := []impl.SpriteReq{
		{ID: "c0", Width: 32, Height: 32},
		{ID: "c1", Width: 0, Height: 32},
	}
	if _, err := p.AddBatch(badSize); !errors.Is(err, impl.ErrBatchRejected) {
		t.Fatalf("a batch with an invalid size must fail with ErrBatchRejected, got %v", err)
	}
	if got := p.Stats().Sprites; got != 0 {
		t.Fatalf("second rejected batch must leave the atlas empty, got %d sprites", got)
	}
	ok := []impl.SpriteReq{
		{ID: "d0", Width: 32, Height: 32},
		{ID: "d1", Width: 32, Height: 32},
	}
	placed, err := p.AddBatch(ok)
	if err != nil {
		t.Fatalf("a healthy batch must be accepted, got %v", err)
	}
	if len(placed) != 2 {
		t.Fatalf("healthy batch returned %d placements, want 2", len(placed))
	}
	if got := p.Stats().Sprites; got != 2 {
		t.Fatalf("sprite count after the healthy batch = %d, want 2", got)
	}
}

func TestAddBatchReturnsPlacementsInOrder(t *testing.T) {
	p, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{
		PageWidth:  100,
		PageHeight: 100,
		MaxPages:   4,
		Padding:    0,
		MaxArea:    1 << 30,
		MaxSprite:  100,
	})
	if err != nil {
		t.Fatalf("new atlas packer: %v", err)
	}
	reqs := []impl.SpriteReq{
		{ID: "row-a", Width: 60, Height: 40},
		{ID: "row-b", Width: 60, Height: 40},
		{ID: "row-c", Width: 60, Height: 40},
	}
	placed, err := p.AddBatch(reqs)
	if err != nil {
		t.Fatalf("add batch: %v", err)
	}
	if len(placed) != len(reqs) {
		t.Fatalf("placements = %d, want %d", len(placed), len(reqs))
	}
	for i, want := range reqs {
		if placed[i].ID != want.ID {
			t.Fatalf("placement %d id = %q, want %q in request order", i, placed[i].ID, want.ID)
		}
		if placed[i].Page != 0 {
			t.Fatalf("placement %d page = %d, want 0", i, placed[i].Page)
		}
		if placed[i].Width != 60 || placed[i].Height != 40 {
			t.Fatalf("placement %d slot = %dx%d, want 60x40", i, placed[i].Width, placed[i].Height)
		}
	}
	if placed[0].Y != placed[1].Y || placed[1].Y != placed[2].Y {
		t.Fatalf("the three rows must share one shelf, got y=%d,%d,%d", placed[0].Y, placed[1].Y, placed[2].Y)
	}
}

func TestRemoveDropsPlacementAndUsage(t *testing.T) {
	p := smallAtlas(t)
	mustAdd(t, p, impl.SpriteReq{ID: "keep", Width: 40, Height: 40})
	mustAdd(t, p, impl.SpriteReq{ID: "drop", Width: 40, Height: 40})
	before := p.Stats()
	if err := p.Remove("drop"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := p.Placement("drop"); ok {
		t.Fatalf("removed sprite must not stay published")
	}
	if _, ok := p.Sprite("drop"); ok {
		t.Fatalf("removed sprite must not stay in the request table")
	}
	after := p.Stats()
	if after.Sprites != before.Sprites-1 {
		t.Fatalf("sprite count after remove = %d, want %d", after.Sprites, before.Sprites-1)
	}
	if after.UsedArea != before.UsedArea-1600 {
		t.Fatalf("used area after remove = %d, want %d", after.UsedArea, before.UsedArea-1600)
	}
	if after.Pages != 1 || p.PageCount() != 1 {
		t.Fatalf("pages must not shrink on remove, got %d", after.Pages)
	}
	if err := p.Remove("drop"); !errors.Is(err, impl.ErrUnknownSprite) {
		t.Fatalf("removing a missing sprite must fail with ErrUnknownSprite, got %v", err)
	}
	if _, ok := p.Placement("keep"); !ok {
		t.Fatalf("the untouched sprite must stay published")
	}
}

func TestRebuildReportsMovedIDs(t *testing.T) {
	p := smallAtlas(t)
	mustAdd(t, p, impl.SpriteReq{ID: "m-a", Width: 100, Height: 40})
	mustAdd(t, p, impl.SpriteReq{ID: "m-b", Width: 100, Height: 40})
	mustAdd(t, p, impl.SpriteReq{ID: "m-c", Width: 100, Height: 40})
	if err := p.Remove("m-b"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	report, err := p.Rebuild(impl.RepackOptions{PageWidth: 256, PageHeight: 256, Padding: 4})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if report.Placed != 2 {
		t.Fatalf("repack placed %d sprites, want 2", report.Placed)
	}
	if report.Pages != 1 {
		t.Fatalf("repack pages = %d, want 1", report.Pages)
	}
	if len(report.Moved) != 1 || report.Moved[0] != "m-c" {
		t.Fatalf("moved ids = %v, want exactly [m-c] after the hole closed", report.Moved)
	}
	moved, ok := p.Placement("m-c")
	if !ok {
		t.Fatalf("rebuilt sprite must stay published")
	}
	if moved.X != 104 || moved.Y != 0 {
		t.Fatalf("rebuilt sprite origin = (%d,%d), want (104,0)", moved.X, moved.Y)
	}
}

func TestRebuildRejectsOverPageBudget(t *testing.T) {
	p, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{
		PageWidth:  100,
		PageHeight: 100,
		MaxPages:   2,
		Padding:    0,
		MaxArea:    1 << 30,
		MaxSprite:  100,
	})
	if err != nil {
		t.Fatalf("new atlas packer: %v", err)
	}
	for _, id := range []string{"r0", "r1", "r2"} {
		mustAdd(t, p, impl.SpriteReq{ID: id, Width: 50, Height: 50})
	}
	anchor, _ := p.Placement("r0")
	if _, err := p.Rebuild(impl.RepackOptions{PageWidth: 100, PageHeight: 100, Padding: 40}); !errors.Is(err, impl.ErrPageBudget) {
		t.Fatalf("a repack needing three pages must fail with ErrPageBudget, got %v", err)
	}
	same, ok := p.Placement("r0")
	if !ok || same != anchor {
		t.Fatalf("a rejected repack must leave the placements untouched, got %+v want %+v", same, anchor)
	}
	if err := p.SetPageBudget(0); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("a zero page budget must fail with ErrBadConfig, got %v", err)
	}
	if err := p.SetPageBudget(1); err != nil {
		t.Fatalf("lowering the budget to the live page count must succeed, got %v", err)
	}
}

func TestUVRangesAndNoOverlap(t *testing.T) {
	p := smallAtlas(t)
	reqs := []impl.SpriteReq{
		{ID: "u0", Width: 64, Height: 64},
		{ID: "u1", Width: 64, Height: 64},
		{ID: "u2", Width: 64, Height: 64},
		{ID: "u3", Width: 128, Height: 32},
		{ID: "u4", Width: 128, Height: 32},
		{ID: "u5", Width: 32, Height: 128},
	}
	for _, s := range reqs {
		mustAdd(t, p, s)
	}
	seen := make(map[string]impl.Placement, len(reqs))
	for _, s := range reqs {
		pl, ok := p.Placement(s.ID)
		if !ok {
			t.Fatalf("sprite %s is missing from the atlas", s.ID)
		}
		if pl.X < 0 || pl.Y < 0 {
			t.Fatalf("sprite %s origin = (%d,%d), must not be negative", s.ID, pl.X, pl.Y)
		}
		if pl.X+pl.Width > pl.PageWidth || pl.Y+pl.Height > pl.PageHeight {
			t.Fatalf("sprite %s rect ends at (%d,%d) outside the %dx%d page", s.ID, pl.X+pl.Width, pl.Y+pl.Height, pl.PageWidth, pl.PageHeight)
		}
		if pl.U0 < 0 || pl.V0 < 0 || pl.U1 > 1 || pl.V1 > 1 {
			t.Fatalf("sprite %s uv = (%v,%v)-(%v,%v) outside the unit square", s.ID, pl.U0, pl.V0, pl.U1, pl.V1)
		}
		if !(pl.U0 < pl.U1) || !(pl.V0 < pl.V1) {
			t.Fatalf("sprite %s uv rectangle is degenerate: (%v,%v)-(%v,%v)", s.ID, pl.U0, pl.V0, pl.U1, pl.V1)
		}
		wantU := float32(pl.X) / float32(pl.PageWidth)
		wantV := float32(pl.Y) / float32(pl.PageHeight)
		if diff := pl.U0 - wantU; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("sprite %s u0 = %v, want x/page_width = %v", s.ID, pl.U0, wantU)
		}
		if diff := pl.V0 - wantV; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("sprite %s v0 = %v, want y/page_height = %v", s.ID, pl.V0, wantV)
		}
		seen[s.ID] = pl
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, b := seen[ids[i]], seen[ids[j]]
			if a.Page != b.Page {
				continue
			}
			if a.X < b.X+b.Width && b.X < a.X+a.Width && a.Y < b.Y+b.Height && b.Y < a.Y+a.Height {
				t.Fatalf("sprites %s and %s overlap on page %d", a.ID, b.ID, a.Page)
			}
		}
	}
}

func TestSnapshotRestoreKeepsPlacements(t *testing.T) {
	p := smallAtlas(t)
	mustAdd(t, p, impl.SpriteReq{ID: "s0", Width: 48, Height: 32})
	mustAdd(t, p, impl.SpriteReq{ID: "s1", Width: 48, Height: 32})
	mustAdd(t, p, impl.SpriteReq{ID: "s2", Width: 200, Height: 200})
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{
		PageWidth:  256,
		PageHeight: 256,
		MaxPages:   4,
		Padding:    4,
		MaxArea:    1 << 30,
		MaxSprite:  256,
	})
	if err != nil {
		t.Fatalf("new restored packer: %v", err)
	}
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, want := restored.PageCount(), p.PageCount(); got != want {
		t.Fatalf("restored page count = %d, want %d", got, want)
	}
	for _, id := range []string{"s0", "s1", "s2"} {
		want, ok := p.Placement(id)
		if !ok {
			t.Fatalf("sprite %s missing from the source atlas", id)
		}
		got, ok := restored.Placement(id)
		if !ok {
			t.Fatalf("restored atlas lost sprite %s", id)
		}
		if got != want {
			t.Fatalf("restored placement of %s = %+v, want %+v", id, got, want)
		}
	}
	if got := restored.Stats().Sprites; got != 3 {
		t.Fatalf("restored sprite count = %d, want 3", got)
	}
	if _, err := restored.Add(impl.SpriteReq{ID: "s3", Width: 16, Height: 16}); err != nil {
		t.Fatalf("restored atlas must accept new sprites, got %v", err)
	}
}

func TestConcurrentAddsAreSerialized(t *testing.T) {
	p, err := impl.NewSpriteAtlasPacker(impl.AtlasConfig{
		PageWidth:  400,
		PageHeight: 400,
		MaxPages:   4,
		Padding:    1,
		MaxArea:    1 << 30,
		MaxSprite:  400,
	})
	if err != nil {
		t.Fatalf("new atlas packer: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	placements := make([]impl.Placement, 0, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pl, err := p.Add(impl.SpriteReq{ID: fmt.Sprintf("conc-%02d", i), Width: 20, Height: 20})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures++
				return
			}
			placements = append(placements, pl)
		}(i)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent adds failed, want none", failures)
	}
	if got := p.Stats().Sprites; got != 16 {
		t.Fatalf("sprite count after the race = %d, want 16", got)
	}
	slots := make(map[string]bool, len(placements))
	for _, pl := range placements {
		key := fmt.Sprintf("%d:%d:%d", pl.Page, pl.X, pl.Y)
		if slots[key] {
			t.Fatalf("two concurrent sprites were handed the same slot %s", key)
		}
		slots[key] = true
	}
	for i := 0; i < len(placements); i++ {
		for j := i + 1; j < len(placements); j++ {
			a, b := placements[i], placements[j]
			if a.Page != b.Page {
				continue
			}
			if a.X < b.X+b.Width && b.X < a.X+a.Width && a.Y < b.Y+b.Height && b.Y < a.Y+a.Height {
				t.Fatalf("concurrent sprites %s and %s overlap", a.ID, b.ID)
			}
		}
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	p := smallAtlas(t)
	mustAdd(t, p, impl.SpriteReq{ID: "live", Width: 32, Height: 32})
	if err := p.Restore([]byte("{\"sprites\":[")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := p.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := p.Placement("live"); !ok {
		t.Fatalf("a rejected restore must not touch the live atlas")
	}
	if got := p.Stats().Sprites; got != 1 {
		t.Fatalf("live sprite count after a rejected restore = %d, want 1", got)
	}
	if _, err := p.Add(impl.SpriteReq{ID: "later", Width: 16, Height: 16}); err != nil {
		t.Fatalf("live atlas must keep accepting sprites, got %v", err)
	}
}
