package task

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-game-b-06"
)

func snowConfig() impl.SnowConfig {
	return impl.SnowConfig{
		TilesX:       2,
		TilesY:       1,
		TileCells:    2,
		CellSize:     0.5,
		MaxDepth:     0.4,
		MaxRim:       0.2,
		RecoverRate:  0.1,
		UploadBudget: 4096,
	}
}

func flatField(version uint64) impl.HeightField {
	return impl.HeightField{Version: version, Width: 4, Height: 2, Heights: make([]float64, 8)}
}

func loadedSnow(t *testing.T, cfg impl.SnowConfig) *impl.SnowDeformation {
	t.Helper()
	d := impl.NewSnowDeformation(cfg)
	if err := d.Load(flatField(7)); err != nil {
		t.Fatalf("load the flat base field: %v", err)
	}
	return d
}

func TestLoadRejectsMismatchedHeightField(t *testing.T) {
	d := impl.NewSnowDeformation(snowConfig())
	short := flatField(3)
	short.Heights = short.Heights[:7]
	if err := d.Load(short); !errors.Is(err, impl.ErrBadHeightField) {
		t.Fatalf("a short height slice must be rejected with ErrBadHeightField, got %v", err)
	}
	wrongGrid := impl.HeightField{Version: 3, Width: 3, Height: 2, Heights: make([]float64, 6)}
	if err := d.Load(wrongGrid); !errors.Is(err, impl.ErrBadHeightField) {
		t.Fatalf("a field that disagrees with the configured grid must be rejected, got %v", err)
	}
	if _, err := d.SampleHeight(impl.Vec2{X: 0.25, Y: 0.25}); !errors.Is(err, impl.ErrNotLoaded) {
		t.Fatalf("a rejected load must leave the tracker unloaded, got %v", err)
	}
}

func TestLoadResetsDeformationAndDirtyTiles(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	brush := impl.Brush{Radius: 0.4, RimRatio: 0, RimSpread: 1.5}
	if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 0.5); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if got := d.PendingTiles(); got != 1 {
		t.Fatalf("pending tiles after a stamp = %d, want 1", got)
	}
	if err := d.Load(flatField(8)); err != nil {
		t.Fatalf("reload a fresh base field: %v", err)
	}
	if got := d.PendingTiles(); got != 0 {
		t.Fatalf("pending tiles after the reload = %d, want 0", got)
	}
	depth, err := d.DepthAt(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("depth after the reload: %v", err)
	}
	if depth != 0 {
		t.Fatalf("depth after the reload = %v, want 0", depth)
	}
	version, err := d.TileVersion(0, 0)
	if err != nil {
		t.Fatalf("tile version after the reload: %v", err)
	}
	if version != 0 {
		t.Fatalf("tile version after the reload = %d, want 0", version)
	}
}

func TestSampleHeightOutOfBounds(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	for _, pos := range []impl.Vec2{
		{X: -0.1, Y: 0.1},
		{X: 0.1, Y: -0.5},
		{X: 2, Y: 0.1},
		{X: 0.1, Y: 1},
	} {
		if _, err := d.SampleHeight(pos); !errors.Is(err, impl.ErrOutOfBounds) {
			t.Fatalf("sample at %+v must fail with ErrOutOfBounds, got %v", pos, err)
		}
	}
	if _, err := d.DepthAt(impl.Vec2{X: 3, Y: 3}); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("depth outside the field must fail with ErrOutOfBounds, got %v", err)
	}
	if _, err := d.RimHeight(impl.Vec2{X: -1, Y: -1}); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("rim height outside the field must fail with ErrOutOfBounds, got %v", err)
	}
	if _, err := d.TileVersion(2, 0); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("a tile index outside the grid must fail with ErrOutOfBounds, got %v", err)
	}
	if _, err := d.TileVersion(0, 1); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("a tile row outside the grid must fail with ErrOutOfBounds, got %v", err)
	}
}

func TestSampleHeightReadsBaseHeight(t *testing.T) {
	d := impl.NewSnowDeformation(snowConfig())
	field := flatField(9)
	for i := range field.Heights {
		field.Heights[i] = 1.5
	}
	field.Heights[0] = 2.25
	if err := d.Load(field); err != nil {
		t.Fatalf("load: %v", err)
	}
	first, err := d.SampleHeight(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("sample the first cell: %v", err)
	}
	if first != 2.25 {
		t.Fatalf("height over the first cell = %v, want 2.25", first)
	}
	last, err := d.SampleHeight(impl.Vec2{X: 1.75, Y: 0.75})
	if err != nil {
		t.Fatalf("sample the last cell: %v", err)
	}
	if last != 1.5 {
		t.Fatalf("height over the last cell = %v, want 1.5", last)
	}
}

func TestStampRejectsInvalidPressure(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	brush := impl.Brush{Radius: 0.4, RimRatio: 0, RimSpread: 1.5}
	for _, pressure := range []float64{0, -0.25, 1.5, 2} {
		if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, pressure); !errors.Is(err, impl.ErrBadPressure) {
			t.Fatalf("pressure %v must be rejected with ErrBadPressure, got %v", pressure, err)
		}
	}
	if got := d.PendingTiles(); got != 0 {
		t.Fatalf("rejected stamps left %d dirty tiles, want 0", got)
	}
	if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 1); err != nil {
		t.Fatalf("a pressure of exactly 1 must be accepted, got %v", err)
	}
}

func TestStampRejectsInvalidBrush(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	cases := []impl.Brush{
		{Radius: 0, RimRatio: 0.5, RimSpread: 1.5},
		{Radius: -1, RimRatio: 0.5, RimSpread: 1.5},
		{Radius: 0.4, RimRatio: -0.1, RimSpread: 1.5},
		{Radius: 0.4, RimRatio: 1.2, RimSpread: 1.5},
		{Radius: 0.4, RimRatio: 0.5, RimSpread: 1},
	}
	for i, brush := range cases {
		if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 0.5); !errors.Is(err, impl.ErrBadBrush) {
			t.Fatalf("brush case %d must be rejected with ErrBadBrush, got %v", i, err)
		}
	}
	if got := d.PendingTiles(); got != 0 {
		t.Fatalf("rejected brushes left %d dirty tiles, want 0", got)
	}
}

func TestStampOutOfBoundsLeavesFieldUntouched(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	brush := impl.Brush{Radius: 0.4, RimRatio: 0.5, RimSpread: 1.5}
	if err := d.Stamp(brush, impl.Vec3{X: -1, Y: 0.25}, 1); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("a stamp left of the field must fail with ErrOutOfBounds, got %v", err)
	}
	if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 4}, 1); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("a stamp above the field must fail with ErrOutOfBounds, got %v", err)
	}
	if got := d.PendingTiles(); got != 0 {
		t.Fatalf("out of bounds stamps left %d dirty tiles, want 0", got)
	}
	depth, err := d.DepthAt(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("depth after the rejected stamps: %v", err)
	}
	if depth != 0 {
		t.Fatalf("depth after the rejected stamps = %v, want 0", depth)
	}
}

func TestStampAppliesPressureFalloff(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	brush := impl.Brush{Radius: 1, RimRatio: 0, RimSpread: 1.5}
	if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 0.5); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	centre, err := d.DepthAt(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("depth under the brush centre: %v", err)
	}
	if math.Abs(centre-0.2) > 1e-9 {
		t.Fatalf("depth under the brush centre = %v, want MaxDepth*pressure = 0.2", centre)
	}
	half, err := d.DepthAt(impl.Vec2{X: 0.75, Y: 0.25})
	if err != nil {
		t.Fatalf("depth at half of the radius: %v", err)
	}
	if math.Abs(half-0.1) > 1e-9 {
		t.Fatalf("depth at half of the radius = %v, want 0.1", half)
	}
	height, err := d.SampleHeight(impl.Vec2{X: 0.75, Y: 0.25})
	if err != nil {
		t.Fatalf("sample over the half radius cell: %v", err)
	}
	if math.Abs(height+0.1) > 1e-9 {
		t.Fatalf("height over a 0.1 dent = %v, want -0.1", height)
	}
}

func TestRimSnowLandsOutsideTheRadius(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	brush := impl.Brush{Radius: 0.4, RimRatio: 1, RimSpread: 1.5}
	if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 0.5); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	rim, err := d.RimHeight(impl.Vec2{X: 0.75, Y: 0.25})
	if err != nil {
		t.Fatalf("rim height at distance 0.5: %v", err)
	}
	if math.Abs(rim-0.1) > 1e-9 {
		t.Fatalf("rim height at distance 0.5 = %v, want 0.1", rim)
	}
	depth, err := d.DepthAt(impl.Vec2{X: 0.75, Y: 0.25})
	if err != nil {
		t.Fatalf("depth on a rim cell: %v", err)
	}
	if depth != 0 {
		t.Fatalf("rim cells must not be depressed, depth = %v", depth)
	}
	depthSum, rimSum := d.Totals()
	if math.Abs(depthSum-0.2) > 1e-9 {
		t.Fatalf("total indentation = %v, want 0.2", depthSum)
	}
	if math.Abs(rimSum-0.2) > 1e-9 {
		t.Fatalf("total rim snow = %v, want RimRatio * 0.2 = 0.2", rimSum)
	}
	height, err := d.SampleHeight(impl.Vec2{X: 0.75, Y: 0.25})
	if err != nil {
		t.Fatalf("sample a rim cell: %v", err)
	}
	if math.Abs(height-0.1) > 1e-9 {
		t.Fatalf("height over a rim cell = %v, want 0.1", height)
	}
}

func TestRelaxRecoversIndentation(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	brush := impl.Brush{Radius: 0.4, RimRatio: 0, RimSpread: 1.5}
	if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 0.5); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if err := d.Relax(-time.Second); !errors.Is(err, impl.ErrBadDuration) {
		t.Fatalf("a negative relax duration must fail with ErrBadDuration, got %v", err)
	}
	if err := d.Relax(time.Second); err != nil {
		t.Fatalf("relax for one second: %v", err)
	}
	depth, err := d.DepthAt(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("depth after relaxing: %v", err)
	}
	if math.Abs(depth-0.1) > 1e-9 {
		t.Fatalf("depth after Relax(1s) with RecoverRate 0.1 = %v, want 0.1", depth)
	}
	if err := d.Relax(time.Second); err != nil {
		t.Fatalf("relax a second time: %v", err)
	}
	depth, err = d.DepthAt(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("depth after the second relax: %v", err)
	}
	if depth != 0 {
		t.Fatalf("depth must stop at zero, got %v", depth)
	}
	height, err := d.SampleHeight(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("sample after full recovery: %v", err)
	}
	if height != 0 {
		t.Fatalf("height after full recovery = %v, want 0", height)
	}
}

func TestFlushTilesRespectsLimitAndClearsDirty(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	brush := impl.Brush{Radius: 0.4, RimRatio: 0, RimSpread: 1.5}
	if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 0.5); err != nil {
		t.Fatalf("stamp the first tile: %v", err)
	}
	if err := d.Stamp(brush, impl.Vec3{X: 1.25, Y: 0.25}, 0.5); err != nil {
		t.Fatalf("stamp the second tile: %v", err)
	}
	if got := d.PendingTiles(); got != 2 {
		t.Fatalf("pending tiles = %d, want 2", got)
	}
	if _, err := d.FlushTiles(0); !errors.Is(err, impl.ErrBadLimit) {
		t.Fatalf("flush with limit 0 must fail with ErrBadLimit, got %v", err)
	}
	if _, err := d.FlushTiles(-2); !errors.Is(err, impl.ErrBadLimit) {
		t.Fatalf("flush with a negative limit must fail with ErrBadLimit, got %v", err)
	}
	first, err := d.FlushTiles(1)
	if err != nil {
		t.Fatalf("flush one tile: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("FlushTiles(1) returned %d tiles, want 1", len(first))
	}
	if first[0].Bytes != 8 {
		t.Fatalf("delta bytes for one changed cell = %d, want 8", first[0].Bytes)
	}
	if got := d.PendingTiles(); got != 1 {
		t.Fatalf("pending tiles after a partial flush = %d, want 1", got)
	}
	rest, err := d.FlushTiles(8)
	if err != nil {
		t.Fatalf("flush the remaining tiles: %v", err)
	}
	if len(rest) != 1 {
		t.Fatalf("the second flush returned %d tiles, want 1", len(rest))
	}
	empty, err := d.FlushTiles(8)
	if err != nil {
		t.Fatalf("flush on a clean tracker: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("flush on a clean tracker returned %d tiles, want 0", len(empty))
	}
}

func TestTileVersionIncrementsPerStamp(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	brush := impl.Brush{Radius: 0.4, RimRatio: 0, RimSpread: 1.5}
	for i := 0; i < 3; i++ {
		if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 0.1); err != nil {
			t.Fatalf("stamp %d: %v", i, err)
		}
	}
	version, err := d.TileVersion(0, 0)
	if err != nil {
		t.Fatalf("tile version: %v", err)
	}
	if version != 3 {
		t.Fatalf("tile version after three stamps = %d, want 3", version)
	}
	if got := d.PendingTiles(); got != 1 {
		t.Fatalf("a tile that is already dirty must stay a single entry, got %d", got)
	}
	other, err := d.TileVersion(1, 0)
	if err != nil {
		t.Fatalf("version of an untouched tile: %v", err)
	}
	if other != 0 {
		t.Fatalf("untouched tile version = %d, want 0", other)
	}
	tiles, err := d.FlushTiles(4)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(tiles) != 1 || tiles[0].Version != 3 {
		t.Fatalf("flushed tiles = %+v, want a single entry with version 3", tiles)
	}
}

func TestFlushTilesReportsOldestDirtyTileFirst(t *testing.T) {
	d := loadedSnow(t, snowConfig())
	brush := impl.Brush{Radius: 0.4, RimRatio: 0, RimSpread: 1.5}
	if err := d.Stamp(brush, impl.Vec3{X: 1.25, Y: 0.25}, 0.2); err != nil {
		t.Fatalf("stamp the second tile first: %v", err)
	}
	if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 0.2); err != nil {
		t.Fatalf("stamp the first tile second: %v", err)
	}
	tiles, err := d.FlushTiles(1)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(tiles) != 1 {
		t.Fatalf("flush returned %d tiles, want 1", len(tiles))
	}
	if tiles[0].TileX != 1 || tiles[0].TileY != 0 {
		t.Fatalf("first flushed tile = (%d,%d), want the tile that got dirty first (1,0)", tiles[0].TileX, tiles[0].TileY)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	cfg := snowConfig()
	d := loadedSnow(t, cfg)
	brush := impl.Brush{Radius: 0.4, RimRatio: 0.5, RimSpread: 1.5}
	if err := d.Stamp(brush, impl.Vec3{X: 0.25, Y: 0.25}, 0.5); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	before, err := d.SampleHeight(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("sample before the snapshot: %v", err)
	}
	data, err := d.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := loadedSnow(t, cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after, err := restored.SampleHeight(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("sample after the restore: %v", err)
	}
	if math.Abs(after-before) > 1e-12 {
		t.Fatalf("height after the restore = %v, want %v", after, before)
	}
	if got := restored.PendingTiles(); got != 1 {
		t.Fatalf("dirty tiles after the restore = %d, want 1", got)
	}
	version, err := restored.TileVersion(0, 0)
	if err != nil {
		t.Fatalf("tile version after the restore: %v", err)
	}
	if version != 1 {
		t.Fatalf("tile version after the restore = %d, want 1", version)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	kept, err := restored.SampleHeight(impl.Vec2{X: 0.25, Y: 0.25})
	if err != nil {
		t.Fatalf("sample after the rejected restore: %v", err)
	}
	if math.Abs(kept-before) > 1e-12 {
		t.Fatalf("a rejected restore changed the height to %v, want %v", kept, before)
	}
}

func TestConcurrentStampsKeepPerTileVersions(t *testing.T) {
	cfg := snowConfig()
	cfg.TilesX = 4
	d := impl.NewSnowDeformation(cfg)
	field := impl.HeightField{Version: 11, Width: 8, Height: 2, Heights: make([]float64, 16)}
	if err := d.Load(field); err != nil {
		t.Fatalf("load: %v", err)
	}
	brush := impl.Brush{Radius: 0.4, RimRatio: 0, RimSpread: 1.5}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for tile := 0; tile < 4; tile++ {
		wg.Add(1)
		go func(tile int) {
			defer wg.Done()
			x := 0.25 + float64(tile)
			for i := 0; i < 3; i++ {
				if err := d.Stamp(brush, impl.Vec3{X: x, Y: 0.25}, 0.1); err != nil {
					errs <- fmt.Errorf("tile %d stamp %d: %w", tile, i, err)
					return
				}
			}
		}(tile)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent stamp: %v", err)
	}
	if got := d.PendingTiles(); got != 4 {
		t.Fatalf("pending tiles after the concurrent stamps = %d, want 4", got)
	}
	for tile := 0; tile < 4; tile++ {
		version, err := d.TileVersion(tile, 0)
		if err != nil {
			t.Fatalf("tile %d version: %v", tile, err)
		}
		if version != 3 {
			t.Fatalf("tile %d version = %d, want 3", tile, version)
		}
	}
}
