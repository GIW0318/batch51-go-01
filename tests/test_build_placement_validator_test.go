package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-a-21"
)

func buildLimits() impl.BuildLimits {
	return impl.BuildLimits{MaxBatch: 4, MaxFootprint: 9, MaxPieces: 10, MaxUndoDepth: 3}
}

func wallDef() impl.ItemDef {
	return impl.ItemDef{ID: "wall", Width: 1, Depth: 1, Height: 3, Mass: 6, MaxLoad: 20, Foundation: true}
}

func padDef() impl.ItemDef {
	return impl.ItemDef{ID: "pad", Width: 2, Depth: 2, Height: 1, Mass: 20, MaxLoad: 100, Foundation: true}
}

func towerDef() impl.ItemDef {
	return impl.ItemDef{ID: "tower", Width: 1, Depth: 1, Height: 4, Mass: 15, MaxLoad: 60, Foundation: true}
}

func lanternDef() impl.ItemDef {
	return impl.ItemDef{ID: "lantern", Width: 1, Depth: 1, Height: 1, Mass: 5, MaxLoad: 0}
}

func commitAll(t *testing.T, v *impl.BuildPlacementValidator, batch []impl.Placement) []impl.PlacedID {
	t.Helper()
	ids, err := v.Commit(batch)
	if err != nil {
		t.Fatalf("commit %d pieces: %v", len(batch), err)
	}
	return ids
}

func sameCells(t *testing.T, got, want []impl.Cell) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("footprint cells = %+v, want %+v", got, want)
	}
	key := func(c impl.Cell) int { return c.X*1000 + c.Y }
	seen := make(map[int]int, len(got))
	for _, c := range got {
		seen[key(c)]++
	}
	for _, c := range want {
		if seen[key(c)] == 0 {
			t.Fatalf("footprint cells = %+v, want the set %+v", got, want)
		}
		seen[key(c)]--
	}
}

func TestSetGridRejectsNonPositive(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	if err := v.SetGrid(0); !errors.Is(err, impl.ErrBadGrid) {
		t.Fatalf("grid 0 = %v, want ErrBadGrid", err)
	}
	if err := v.SetGrid(-0.5); !errors.Is(err, impl.ErrBadGrid) {
		t.Fatalf("grid -0.5 = %v, want ErrBadGrid", err)
	}
	if err := v.SetGrid(0.5); err != nil {
		t.Fatalf("grid 0.5 = %v, want nil", err)
	}
	if err := v.SetGrid(2); err != nil {
		t.Fatalf("grid 2 = %v, want nil", err)
	}
}

func TestPreviewSnapsPositionToCell(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	if err := v.SetGrid(2); err != nil {
		t.Fatalf("set grid: %v", err)
	}
	p, err := v.Preview(wallDef(), impl.Vec3{X: 5, Y: 7}, 0)
	if err != nil {
		t.Fatalf("preview wall = %v, want nil", err)
	}
	wantCell := impl.Cell{X: 2, Y: 3}
	if p.Cell != wantCell {
		t.Fatalf("snapped cell = %+v, want %+v", p.Cell, wantCell)
	}
	if p.Pos.X != 4 || p.Pos.Y != 6 || p.Pos.Z != 0 {
		t.Fatalf("snapped position = %+v, want 4/6/0", p.Pos)
	}
	sameCells(t, p.Cells, []impl.Cell{wantCell})
	if p.Layer != 0 {
		t.Fatalf("preview layer = %d, want 0", p.Layer)
	}
}

func TestPreviewRotationSwapsFootprint(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	beam := impl.ItemDef{ID: "beam", Width: 1, Depth: 3, Height: 1, Mass: 4, MaxLoad: 10, Foundation: true}
	flat, err := v.Preview(beam, impl.Vec3{}, 0)
	if err != nil {
		t.Fatalf("preview at rotation 0: %v", err)
	}
	sameCells(t, flat.Cells, []impl.Cell{{X: 0, Y: 0}, {X: 0, Y: 1}, {X: 0, Y: 2}})
	turned, err := v.Preview(beam, impl.Vec3{}, 90)
	if err != nil {
		t.Fatalf("preview at rotation 90: %v", err)
	}
	sameCells(t, turned.Cells, []impl.Cell{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 2, Y: 0}})
	if turned.Rot != 90 {
		t.Fatalf("rotation echoed as %d, want 90", turned.Rot)
	}
}

func TestPreviewRejectsInvalidRotation(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	if _, err := v.Preview(wallDef(), impl.Vec3{}, 45); !errors.Is(err, impl.ErrInvalidRotation) {
		t.Fatalf("rotation 45 = %v, want ErrInvalidRotation", err)
	}
	if _, err := v.Preview(wallDef(), impl.Vec3{}, -90); !errors.Is(err, impl.ErrInvalidRotation) {
		t.Fatalf("rotation -90 = %v, want ErrInvalidRotation", err)
	}
	if _, err := v.Preview(wallDef(), impl.Vec3{}, 180); err != nil {
		t.Fatalf("rotation 180 = %v, want nil", err)
	}
}

func TestPreviewRejectsOutOfBounds(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	wide := impl.ItemDef{ID: "wide", Width: 2, Depth: 1, Height: 1, Mass: 4, MaxLoad: 10, Foundation: true}
	if _, err := v.Preview(wide, impl.Vec3{X: 31, Y: 5}, 0); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("footprint past the east edge = %v, want ErrOutOfBounds", err)
	}
	if _, err := v.Preview(wallDef(), impl.Vec3{X: -1, Y: 4}, 0); !errors.Is(err, impl.ErrOutOfBounds) {
		t.Fatalf("footprint on a negative cell = %v, want ErrOutOfBounds", err)
	}
	if _, err := v.Preview(wallDef(), impl.Vec3{X: 31, Y: 31}, 0); err != nil {
		t.Fatalf("last corner cell = %v, want nil", err)
	}
}

func TestPreviewRejectsBadItemAndOversizeFootprint(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	if _, err := v.Preview(impl.ItemDef{ID: "  ", Width: 1, Depth: 1, Height: 1}, impl.Vec3{}, 0); !errors.Is(err, impl.ErrBadItem) {
		t.Fatalf("blank item id = %v, want ErrBadItem", err)
	}
	if _, err := v.Preview(impl.ItemDef{ID: "flat", Width: 0, Depth: 1, Height: 1}, impl.Vec3{}, 0); !errors.Is(err, impl.ErrBadItem) {
		t.Fatalf("zero width = %v, want ErrBadItem", err)
	}
	huge := impl.ItemDef{ID: "hall", Width: 4, Depth: 4, Height: 2, Mass: 40, MaxLoad: 90, Foundation: true}
	if _, err := v.Preview(huge, impl.Vec3{}, 0); !errors.Is(err, impl.ErrFootprintTooLarge) {
		t.Fatalf("4 by 4 footprint = %v, want ErrFootprintTooLarge", err)
	}
}

func TestPreviewRejectsOverlap(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	commitAll(t, v, []impl.Placement{{BatchID: "b1", Def: padDef(), Cell: impl.Cell{X: 2, Y: 2}}})
	if _, err := v.Preview(wallDef(), impl.Vec3{X: 3, Y: 3}, 0); !errors.Is(err, impl.ErrCellOccupied) {
		t.Fatalf("cell inside the pad = %v, want ErrCellOccupied", err)
	}
	if _, err := v.Preview(wallDef(), impl.Vec3{X: 4, Y: 2}, 0); err != nil {
		t.Fatalf("cell next to the pad = %v, want nil", err)
	}
}

func TestCommitAssignsIDsInOrder(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	ids := commitAll(t, v, []impl.Placement{
		{BatchID: "b1", Def: padDef(), Cell: impl.Cell{X: 2, Y: 2}},
		{BatchID: "b1", Def: towerDef(), Cell: impl.Cell{X: 6, Y: 6}},
	})
	if len(ids) != 2 {
		t.Fatalf("committed ids = %+v, want two", ids)
	}
	if ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("committed ids = %+v, want two distinct non blank ids", ids)
	}
	stats := v.Stats()
	if stats.Placed != 2 || stats.Batches != 1 || stats.UndoDepth != 1 {
		t.Fatalf("stats after one batch = %+v, want 2 placed, 1 batch, depth 1", stats)
	}
}

func TestCommitIsAtomicOnConflict(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	commitAll(t, v, []impl.Placement{{BatchID: "b1", Def: padDef(), Cell: impl.Cell{X: 2, Y: 2}}})
	ids, err := v.Commit([]impl.Placement{
		{BatchID: "b2", Def: towerDef(), Cell: impl.Cell{X: 8, Y: 8}},
		{BatchID: "b2", Def: towerDef(), Cell: impl.Cell{X: 3, Y: 3}},
	})
	var conflict *impl.Conflict
	if !errors.As(err, &conflict) {
		t.Fatalf("commit overlap = %v, want a *Conflict", err)
	}
	if conflict.Index != 1 || conflict.ItemID != "tower" {
		t.Fatalf("conflict = %+v, want index 1 on tower", conflict)
	}
	if conflict.Cell != (impl.Cell{X: 3, Y: 3}) {
		t.Fatalf("conflict cell = %+v, want 3/3", conflict.Cell)
	}
	if len(ids) != 0 {
		t.Fatalf("rejected commit returned ids %+v, want none", ids)
	}
	stats := v.Stats()
	if stats.Placed != 1 || stats.Batches != 1 || stats.Conflicts != 1 {
		t.Fatalf("stats after a rejected batch = %+v, want 1 placed, 1 batch, 1 conflict", stats)
	}
}

func TestCommitRejectsBatchTooLarge(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	batch := make([]impl.Placement, 0, 5)
	for i := 0; i < 5; i++ {
		batch = append(batch, impl.Placement{BatchID: "b1", Def: towerDef(), Cell: impl.Cell{X: i, Y: 0}})
	}
	if _, err := v.Commit(batch); !errors.Is(err, impl.ErrBatchTooLarge) {
		t.Fatalf("five pieces with a budget of four = %v, want ErrBatchTooLarge", err)
	}
	if stats := v.Stats(); stats.Placed != 0 {
		t.Fatalf("rejected batch left %d pieces behind", stats.Placed)
	}
}

func TestCommitRejectsMixedBatchID(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	mixed := []impl.Placement{
		{BatchID: "b1", Def: towerDef(), Cell: impl.Cell{X: 1, Y: 1}},
		{BatchID: "b2", Def: towerDef(), Cell: impl.Cell{X: 3, Y: 1}},
	}
	if _, err := v.Commit(mixed); !errors.Is(err, impl.ErrBadBatch) {
		t.Fatalf("mixed batch ids = %v, want ErrBadBatch", err)
	}
	blank := []impl.Placement{
		{BatchID: "  ", Def: towerDef(), Cell: impl.Cell{X: 1, Y: 1}},
		{BatchID: "  ", Def: towerDef(), Cell: impl.Cell{X: 3, Y: 1}},
	}
	if _, err := v.Commit(blank); !errors.Is(err, impl.ErrBadBatch) {
		t.Fatalf("blank batch id = %v, want ErrBadBatch", err)
	}
	if stats := v.Stats(); stats.Placed != 0 {
		t.Fatalf("rejected batches left %d pieces behind", stats.Placed)
	}
}

func TestCheckSupportReportsChain(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	ids := commitAll(t, v, []impl.Placement{{BatchID: "b1", Def: padDef(), Cell: impl.Cell{X: 2, Y: 2}}})
	base, err := v.CheckSupport(ids[0])
	if err != nil {
		t.Fatalf("check support of the pad: %v", err)
	}
	if !base.Supported || base.Reason != impl.ReasonGround {
		t.Fatalf("pad report = %+v, want a grounded piece", base)
	}
	if len(base.Path) != 1 || base.Path[0] != ids[0] {
		t.Fatalf("pad support path = %+v, want itself", base.Path)
	}
	if base.MaxSlope != 0 {
		t.Fatalf("flat terrain slope = %v, want 0", base.MaxSlope)
	}
	lampIDs := commitAll(t, v, []impl.Placement{{BatchID: "b2", Def: lanternDef(), Cell: impl.Cell{X: 2, Y: 2}}})
	lamp, err := v.CheckSupport(lampIDs[0])
	if err != nil {
		t.Fatalf("check support of the lantern: %v", err)
	}
	if !lamp.Supported || lamp.Reason != impl.ReasonStacked {
		t.Fatalf("lantern report = %+v, want a stacked piece", lamp)
	}
	if len(lamp.Path) != 2 || lamp.Path[0] != lampIDs[0] || lamp.Path[1] != ids[0] {
		t.Fatalf("lantern support path = %+v, want itself then the pad", lamp.Path)
	}
}

func TestRemoveUnknownPlacement(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	if err := v.Remove("missing"); !errors.Is(err, impl.ErrUnknownPlacement) {
		t.Fatalf("remove unknown = %v, want ErrUnknownPlacement", err)
	}
	ids := commitAll(t, v, []impl.Placement{{BatchID: "b1", Def: towerDef(), Cell: impl.Cell{X: 5, Y: 5}}})
	if err := v.Remove(ids[0]); err != nil {
		t.Fatalf("remove a free standing tower: %v", err)
	}
	stats := v.Stats()
	if stats.Placed != 0 || stats.Removed != 1 {
		t.Fatalf("stats after remove = %+v, want 0 placed and 1 removed", stats)
	}
}

func TestUndoRestoresPreviousBatch(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	commitAll(t, v, []impl.Placement{
		{BatchID: "b1", Def: padDef(), Cell: impl.Cell{X: 2, Y: 2}},
		{BatchID: "b1", Def: towerDef(), Cell: impl.Cell{X: 6, Y: 6}},
	})
	commitAll(t, v, []impl.Placement{{BatchID: "b2", Def: towerDef(), Cell: impl.Cell{X: 8, Y: 8}}})
	if err := v.Undo("b1"); !errors.Is(err, impl.ErrNotLatestBatch) {
		t.Fatalf("undo of an older batch = %v, want ErrNotLatestBatch", err)
	}
	if err := v.Undo("b2"); err != nil {
		t.Fatalf("undo of the latest batch: %v", err)
	}
	if stats := v.Stats(); stats.Placed != 2 || stats.UndoDepth != 1 {
		t.Fatalf("stats after undo = %+v, want 2 placed and depth 1", stats)
	}
	if err := v.Undo("b2"); !errors.Is(err, impl.ErrUnknownBatch) {
		t.Fatalf("undo of a popped batch = %v, want ErrUnknownBatch", err)
	}
	if err := v.Undo("b1"); err != nil {
		t.Fatalf("undo of the remaining batch: %v", err)
	}
	if stats := v.Stats(); stats.Placed != 0 || stats.UndoDepth != 0 {
		t.Fatalf("stats after undoing everything = %+v, want an empty board", stats)
	}
}

func TestSnapshotRestoreKeepsPlacementsAndUndo(t *testing.T) {
	v := impl.NewBuildPlacementValidator(buildLimits())
	commitAll(t, v, []impl.Placement{{BatchID: "b1", Def: padDef(), Cell: impl.Cell{X: 2, Y: 2}}})
	data, err := v.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewBuildPlacementValidator(buildLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	stats := restored.Stats()
	if stats.Placed != 1 || stats.UndoDepth != 1 {
		t.Fatalf("restored stats = %+v, want 1 placed and depth 1", stats)
	}
	if err := restored.Undo("b1"); err != nil {
		t.Fatalf("undo after restore: %v", err)
	}
	if stats := restored.Stats(); stats.Placed != 0 {
		t.Fatalf("restored board after undo = %d pieces, want 0", stats.Placed)
	}
}
