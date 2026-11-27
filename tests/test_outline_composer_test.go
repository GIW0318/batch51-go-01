package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-b-14"
)

func defaultOutlineConfig() impl.OutlineConfig {
	return impl.OutlineConfig{
		MaxStyles:       8,
		MaxWidthPx:      6,
		DepthThreshold:  0.1,
		NormalThreshold: 0.9,
		MaxPixels:       4096,
		MaxTexPixels:    4096,
	}
}

func newComposer(t *testing.T) *impl.OutlineComposer {
	t.Helper()
	c, err := impl.NewOutlineComposer(defaultOutlineConfig())
	if err != nil {
		t.Fatalf("new composer: %v", err)
	}
	return c
}

func enemyStyle() impl.OutlineStyle {
	return impl.OutlineStyle{
		ID:       "enemy",
		Color:    impl.RGBA{R: 1, G: 0.2, B: 0.1, A: 1},
		WidthPx:  1,
		Priority: 5,
	}
}

func flatDepth(w, h int) impl.DepthTex {
	return impl.DepthTex{Width: w, Height: h, Data: make([]float32, w*h)}
}

func flatNormal(w, h int, v impl.Vec3) impl.NormalTex {
	data := make([]impl.Vec3, w*h)
	for i := range data {
		data[i] = v
	}
	return impl.NormalTex{Width: w, Height: h, Data: data}
}

// blockMask is a five by five mask with a three by three target block centred
// inside it; everything else is background.
func blockMask() impl.MaskTex {
	m := impl.MaskTex{Width: 5, Height: 5, Data: make([]uint8, 25)}
	for y := 1; y <= 3; y++ {
		for x := 1; x <= 3; x++ {
			m.Data[y*5+x] = 1
		}
	}
	return m
}

func fullMask(w, h int) impl.MaskTex {
	m := impl.MaskTex{Width: w, Height: h, Data: make([]uint8, w*h)}
	for i := range m.Data {
		m.Data[i] = 1
	}
	return m
}

func TestSetStyleValidatesWidthColorAndPriority(t *testing.T) {
	c := newComposer(t)
	bad := []struct {
		id string
		st impl.OutlineStyle
	}{
		{"   ", impl.OutlineStyle{Color: impl.RGBA{A: 1}, WidthPx: 1}},
		{"enemy", impl.OutlineStyle{Color: impl.RGBA{A: 1}, WidthPx: 0}},
		{"enemy", impl.OutlineStyle{Color: impl.RGBA{A: 1}, WidthPx: -2}},
		{"enemy", impl.OutlineStyle{Color: impl.RGBA{A: 1}, WidthPx: 9}},
		{"enemy", impl.OutlineStyle{Color: impl.RGBA{R: 1.4, A: 1}, WidthPx: 1}},
		{"enemy", impl.OutlineStyle{Color: impl.RGBA{A: -0.2}, WidthPx: 1}},
		{"enemy", impl.OutlineStyle{Color: impl.RGBA{A: 1}, WidthPx: 1, Priority: -1}},
		{"enemy", impl.OutlineStyle{Color: impl.RGBA{A: 1}, WidthPx: 1, Priority: 99}},
		{"enemy", impl.OutlineStyle{ID: "other", Color: impl.RGBA{A: 1}, WidthPx: 1}},
	}
	for i, b := range bad {
		if err := c.SetStyle(b.id, b.st); !errors.Is(err, impl.ErrBadStyle) {
			t.Fatalf("bad style %d must fail with ErrBadStyle, got %v", i, err)
		}
	}
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("valid style must be accepted, got %v", err)
	}
	for i := 0; i < 7; i++ {
		id := "filler-" + string(rune('a'+i))
		if err := c.SetStyle(id, impl.OutlineStyle{Color: impl.RGBA{A: 1}, WidthPx: 1}); err != nil {
			t.Fatalf("style %s must fit the table, got %v", id, err)
		}
	}
	if err := c.SetStyle("overflow", impl.OutlineStyle{Color: impl.RGBA{A: 1}, WidthPx: 1}); !errors.Is(err, impl.ErrStyleLimit) {
		t.Fatalf("a ninth style must fail with ErrStyleLimit, got %v", err)
	}
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("replacing an existing style must not need a free slot, got %v", err)
	}
}

func TestSetSelectiveRejectsUnknownTarget(t *testing.T) {
	c := newComposer(t)
	if err := c.SetSelective("ghost", true); !errors.Is(err, impl.ErrUnknownStyle) {
		t.Fatalf("selective on an unknown target must fail with ErrUnknownStyle, got %v", err)
	}
	if c.Selective("ghost") {
		t.Fatalf("an unknown target must never be selective")
	}
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if c.Selective("enemy") {
		t.Fatalf("a fresh style must start non selective")
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("enable selective: %v", err)
	}
	if !c.Selective("enemy") {
		t.Fatalf("selective must read back as enabled")
	}
	if err := c.RemoveStyle("enemy"); err != nil {
		t.Fatalf("remove style: %v", err)
	}
	if err := c.SetSelective("enemy", true); !errors.Is(err, impl.ErrUnknownStyle) {
		t.Fatalf("selective on a removed target must fail with ErrUnknownStyle, got %v", err)
	}
}

func TestComposeRequiresMatchingTextures(t *testing.T) {
	c := newComposer(t)
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	depth := flatDepth(5, 5)
	normal := flatNormal(5, 5, impl.Vec3{Z: 1})
	short := impl.MaskTex{Width: 5, Height: 5, Data: make([]uint8, 12)}
	if _, err := c.Compose(depth, normal, short); !errors.Is(err, impl.ErrBadTexture) {
		t.Fatalf("a mask whose payload is too short must fail with ErrBadTexture, got %v", err)
	}
	if _, err := c.Compose(depth, normal, impl.MaskTex{Width: 4, Height: 5, Data: make([]uint8, 20)}); !errors.Is(err, impl.ErrBadTexture) {
		t.Fatalf("a mask with a different width must fail with ErrBadTexture, got %v", err)
	}
	if _, err := c.Compose(depth, normal, impl.MaskTex{}); !errors.Is(err, impl.ErrBadTexture) {
		t.Fatalf("a zero sized mask must fail with ErrBadTexture, got %v", err)
	}
	if _, err := c.Compose(flatDepth(5, 5), impl.NormalTex{Width: 5, Height: 5}, flatMask(5, 5)); !errors.Is(err, impl.ErrBadTexture) {
		t.Fatalf("a normal buffer without payload must fail with ErrBadTexture, got %v", err)
	}
}

func flatMask(w, h int) impl.MaskTex {
	return impl.MaskTex{Width: w, Height: h, Data: make([]uint8, w*h)}
}

func TestComposeDetectsSilhouetteOnMaskBoundary(t *testing.T) {
	c := newComposer(t)
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("enable selective: %v", err)
	}
	if err := c.SetWidthPx(1); err != nil {
		t.Fatalf("set width: %v", err)
	}
	img, err := c.Compose(flatDepth(5, 5), flatNormal(5, 5, impl.Vec3{Z: 1}), blockMask())
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if img.Width != 5 || img.Height != 5 || len(img.Pixels) != 25 {
		t.Fatalf("image is %dx%d with %d pixels, want 5x5 with 25", img.Width, img.Height, len(img.Pixels))
	}
	st := c.Stats()
	if st.Pixels != 8 {
		t.Fatalf("outline pixels = %d, want the eight border pixels of the block", st.Pixels)
	}
	if st.Targets != 1 {
		t.Fatalf("composed targets = %d, want 1", st.Targets)
	}
	border := []int{1*5 + 1, 1*5 + 2, 1*5 + 3, 2*5 + 1, 2*5 + 3, 3*5 + 1, 3*5 + 2, 3*5 + 3}
	for _, index := range border {
		if img.Pixels[index] != enemyStyle().Color {
			t.Fatalf("pixel %d = %+v, want the style colour %+v", index, img.Pixels[index], enemyStyle().Color)
		}
	}
	if img.Pixels[2*5+2].A != 0 {
		t.Fatalf("the block centre must stay transparent, got %+v", img.Pixels[2*5+2])
	}
	if img.Pixels[0].A != 0 {
		t.Fatalf("background pixels must stay transparent, got %+v", img.Pixels[0])
	}
}

func TestComposeIgnoresBlankMask(t *testing.T) {
	c := newComposer(t)
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("enable selective: %v", err)
	}
	img, err := c.Compose(flatDepth(6, 4), flatNormal(6, 4, impl.Vec3{Z: 1}), flatMask(6, 4))
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if img.Width != 6 || img.Height != 4 || len(img.Pixels) != 24 {
		t.Fatalf("image is %dx%d with %d pixels, want 6x4 with 24", img.Width, img.Height, len(img.Pixels))
	}
	for i, px := range img.Pixels {
		if px != (impl.RGBA{}) {
			t.Fatalf("pixel %d = %+v, want a transparent overlay", i, px)
		}
	}
	if got := c.Stats().Pixels; got != 0 {
		t.Fatalf("a blank mask must not produce outline pixels, got %d", got)
	}
}

func TestComposeSkipsNonSelectiveTargets(t *testing.T) {
	c := newComposer(t)
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	img, err := c.Compose(flatDepth(5, 5), flatNormal(5, 5, impl.Vec3{Z: 1}), blockMask())
	if err != nil {
		t.Fatalf("compose without selective: %v", err)
	}
	for i, px := range img.Pixels {
		if px != (impl.RGBA{}) {
			t.Fatalf("pixel %d = %+v, want no outline while the target is not selective", i, px)
		}
	}
	if got := c.Stats().Pixels; got != 0 {
		t.Fatalf("non selective target produced %d pixels, want 0", got)
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("enable selective: %v", err)
	}
	if _, err := c.Compose(flatDepth(5, 5), flatNormal(5, 5, impl.Vec3{Z: 1}), blockMask()); err != nil {
		t.Fatalf("compose with selective: %v", err)
	}
	if got := c.Stats().Pixels; got != 8 {
		t.Fatalf("selective target produced %d pixels, want 8", got)
	}
}

func TestPriorityReportsRegisteredValue(t *testing.T) {
	c := newComposer(t)
	if got := c.Priority("enemy"); got != -1 {
		t.Fatalf("priority of an unknown target = %d, want -1", got)
	}
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if got := c.Priority("enemy"); got != 5 {
		t.Fatalf("priority = %d, want the authored 5", got)
	}
	updated := enemyStyle()
	updated.Priority = 7
	if err := c.SetStyle("enemy", updated); err != nil {
		t.Fatalf("update style: %v", err)
	}
	if got := c.Priority("enemy"); got != 7 {
		t.Fatalf("priority after the update = %d, want 7", got)
	}
}

func TestSetWidthPxValidatesRange(t *testing.T) {
	c := newComposer(t)
	for _, px := range []float32{0, -1, 6.5} {
		if err := c.SetWidthPx(px); !errors.Is(err, impl.ErrBadWidthPx) {
			t.Fatalf("width %v must fail with ErrBadWidthPx, got %v", px, err)
		}
	}
	if err := c.SetWidthPx(2); err != nil {
		t.Fatalf("width 2 must be accepted, got %v", err)
	}
}

func TestSetOcclusionModeValidates(t *testing.T) {
	c := newComposer(t)
	if err := c.SetOcclusionMode(impl.OcclusionMode(9)); !errors.Is(err, impl.ErrBadOcclusion) {
		t.Fatalf("an unknown occlusion mode must fail with ErrBadOcclusion, got %v", err)
	}
	if err := c.SetOcclusionMode(impl.OcclusionNone); err != nil {
		t.Fatalf("OcclusionNone must be accepted, got %v", err)
	}
	if err := c.SetOcclusionMode(impl.OcclusionMasked); err != nil {
		t.Fatalf("OcclusionMasked must be accepted, got %v", err)
	}
}

func TestComposeDepthDiscontinuityInsideTarget(t *testing.T) {
	c := newComposer(t)
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("enable selective: %v", err)
	}
	depth := flatDepth(5, 5)
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			if x >= 2 {
				depth.Data[y*5+x] = 1
			}
		}
	}
	img, err := c.Compose(depth, flatNormal(5, 5, impl.Vec3{Z: 1}), fullMask(5, 5))
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if got := c.Stats().Pixels; got != 10 {
		t.Fatalf("interior depth edge produced %d pixels, want the two columns of five", got)
	}
	for y := 0; y < 5; y++ {
		for _, x := range []int{1, 2} {
			if img.Pixels[y*5+x].A == 0 {
				t.Fatalf("pixel (%d,%d) must carry the depth edge outline", x, y)
			}
		}
		if img.Pixels[y*5+0].A != 0 || img.Pixels[y*5+4].A != 0 {
			t.Fatalf("row %d must not outline the outer columns", y)
		}
	}
}

func TestComposeBudgetDowngradesWidth(t *testing.T) {
	cfg := defaultOutlineConfig()
	cfg.MaxPixels = 20
	c, err := impl.NewOutlineComposer(cfg)
	if err != nil {
		t.Fatalf("new composer: %v", err)
	}
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("enable selective: %v", err)
	}
	if err := c.SetWidthPx(5); err != nil {
		t.Fatalf("set width: %v", err)
	}
	if _, err := c.Compose(flatDepth(5, 5), flatNormal(5, 5, impl.Vec3{Z: 1}), fullMask(5, 5)); err != nil {
		t.Fatalf("compose: %v", err)
	}
	st := c.Stats()
	if st.Pixels > 20 {
		t.Fatalf("outline pixels = %d, must stay inside the 20 pixel budget", st.Pixels)
	}
	if st.Pixels != 16 {
		t.Fatalf("outline pixels after the downgrade = %d, want the 16 pixel perimeter", st.Pixels)
	}
	if st.Downgraded < 1 {
		t.Fatalf("downgrade counter = %d, want at least one width step", st.Downgraded)
	}
}

func TestComposeRejectsOversizedTextures(t *testing.T) {
	cfg := defaultOutlineConfig()
	cfg.MaxTexPixels = 64
	c, err := impl.NewOutlineComposer(cfg)
	if err != nil {
		t.Fatalf("new composer: %v", err)
	}
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("enable selective: %v", err)
	}
	if _, err := c.Compose(flatDepth(9, 8), flatNormal(9, 8, impl.Vec3{Z: 1}), flatMask(9, 8)); !errors.Is(err, impl.ErrBadTexture) {
		t.Fatalf("a texture above the pixel cap must fail with ErrBadTexture, got %v", err)
	}
	if _, err := c.Compose(flatDepth(8, 8), flatNormal(8, 8, impl.Vec3{Z: 1}), flatMask(8, 8)); err != nil {
		t.Fatalf("a texture at the pixel cap must be accepted, got %v", err)
	}
}

func TestConcurrentStyleUpdatesAreAtomic(t *testing.T) {
	c := newComposer(t)
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("enable selective: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st := enemyStyle()
			st.Priority = i
			_ = c.SetStyle("enemy", st)
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Compose(flatDepth(5, 5), flatNormal(5, 5, impl.Vec3{Z: 1}), blockMask())
		}()
	}
	wg.Wait()
	got := c.Priority("enemy")
	if got < 0 || got > 7 {
		t.Fatalf("priority after the race = %d, want one of the eight written values", got)
	}
	img, err := c.Compose(flatDepth(5, 5), flatNormal(5, 5, impl.Vec3{Z: 1}), blockMask())
	if err != nil {
		t.Fatalf("compose after the race: %v", err)
	}
	if len(img.Pixels) != 25 {
		t.Fatalf("composed image has %d pixels, want 25", len(img.Pixels))
	}
	if st := c.Stats(); st.Pixels != 8 {
		t.Fatalf("outline pixels after the race = %d, want 8", st.Pixels)
	}
}

func TestSnapshotRestoreKeepsStyles(t *testing.T) {
	c := newComposer(t)
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("enable selective: %v", err)
	}
	if err := c.SetWidthPx(3); err != nil {
		t.Fatalf("set width: %v", err)
	}
	if err := c.SetOcclusionMode(impl.OcclusionMasked); err != nil {
		t.Fatalf("set occlusion: %v", err)
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored, err := impl.NewOutlineComposer(defaultOutlineConfig())
	if err != nil {
		t.Fatalf("new restored composer: %v", err)
	}
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Priority("enemy"); got != 5 {
		t.Fatalf("restored priority = %d, want 5", got)
	}
	if !restored.Selective("enemy") {
		t.Fatalf("restored selective flag must stay enabled")
	}
	depth := flatDepth(5, 5)
	normal := flatNormal(5, 5, impl.Vec3{Z: 1})
	mask := blockMask()
	img, err := restored.Compose(depth, normal, mask)
	if err != nil {
		t.Fatalf("compose after restore: %v", err)
	}
	if len(img.Pixels) != 25 {
		t.Fatalf("restored image has %d pixels, want 25", len(img.Pixels))
	}
	if _, err := c.Compose(depth, normal, mask); err != nil {
		t.Fatalf("compose on the live composer: %v", err)
	}
	if got, want := restored.Stats().Pixels, c.Stats().Pixels; got != want {
		t.Fatalf("restored compose produced %d pixels, want the %d of the live composer", got, want)
	}
	if got := restored.Stats().Pixels; got <= 8 {
		t.Fatalf("a three pixel width must spread past the eight silhouette pixels, got %d", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	c := newComposer(t)
	if err := c.SetStyle("enemy", enemyStyle()); err != nil {
		t.Fatalf("set style: %v", err)
	}
	if err := c.Restore([]byte("{\"styles\":")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := c.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := c.Priority("enemy"); got != 5 {
		t.Fatalf("live style must survive a rejected restore, got priority %d", got)
	}
	if err := c.SetSelective("enemy", true); err != nil {
		t.Fatalf("live selective must keep working, got %v", err)
	}
}
