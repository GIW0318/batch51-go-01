# 据点建造的网格吸附、支撑连通与批量原子提交

## 题面

上线三天后据点建造出了怪事：玩家在斜坡边缘贴着一排地基摆围墙，客户端预览是绿的，点确认之后围墙一件都没出现，背包里的材料却少了一半；另一批玩家反馈把承重柱拆掉之后，二层的平台还悬在半空，人走过去直接掉出地图。更麻烦的是两个人几乎同时在同一块空地下单，两批件都记成了已放置，格子叠在一起，之后谁也拆不掉。

落点在 gsb/batch51-go-game-a-21 的 build_placement_validator.go，类型 BuildPlacementValidator，包名 task，模块路径 gsb/batch51-go-game-a-21。

公开接口保持：NewBuildPlacementValidator(limits BuildLimits) *BuildPlacementValidator、Preview(def ItemDef, pos Vec3, rot int) (Placement, error)、Commit(batch []Placement) ([]PlacedID, error)、Remove(id PlacedID) error、SetGrid(cell float64) error、SetTerrain(t TerrainView) error、CheckSupport(id PlacedID) (SupportReport, error)、Undo(batchID string) error、Stats() BuildStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 物件编号不能为空，宽、深、高必须为正，否则报 ErrBadItem；宽乘深超过 MaxFootprint 报 ErrFootprintTooLarge。
2) 旋转只接受 0、90、180、270，其它值报 ErrInvalidRotation；90 与 270 交换宽深，占地格数不变。
3) 落点按网格向下吸附到最近的格点，返回的 Cell 与 Pos 都是吸附后的值，Pos.Z 取地形在该格的高度；未设置地形时按 32 行 32 列、高度全零、坡度上限 1 的平地处理。
4) 任何一格越出地形范围（含负坐标）报 ErrOutOfBounds。
5) 同一层上占地格相交即冲突报 ErrCellOccupied，只有相邻不算；Preview 只校验几何与占位，Layer 恒为零。
6) Commit 整批要么全落要么全不落：任一件非法时返回 *Conflict，Index 与 ItemID 指向首个冲突件，返回值不带编号，已放置数量与批次数都不变。
7) 一批的 BatchID 必须非空且整批一致，否则 ErrBadBatch；批内件数超过 MaxBatch 报 ErrBatchTooLarge；总数超过 MaxPieces 报 ErrPieceLimit。
8) 地基件落在第 0 层，四角最陡坡度超过 MaxSlope 报 ErrSteepTerrain；非地基件必须与某个已支撑件占格相交才能落在它上面一层，否则报 ErrUnsupported；压在承重件上的总质量超过 MaxLoad 报 ErrOverloaded。
9) CheckSupport 返回是否被支撑、从该件本身逐层向下直到地基的编号路径、路径长度，以及该件四角的最陡坡度。
10) Remove 删除不存在的编号报 ErrUnknownPlacement；删除承重件会让上方悬空时，要么整体拒绝，要么级联删除并在统计的 Cascaded 里记下连带件数，不允许留下悬空件。
11) 撤销只允许最近一批，非最近批报 ErrNotLatestBatch，已出栈的批报 ErrUnknownBatch；撤销深度超过 MaxUndoDepth 时最旧一批不再保留在栈上。
12) 快照保存已放置集合与撤销栈，恢复后能继续撤销；载荷损坏报 ErrBadSnapshot 且不改变现有状态；SetGrid 在有已放置件时拒绝改动，报 ErrGridLocked。

硬约束：Preview、Commit、Remove、CheckSupport、Snapshot 会被多个建造请求并发调用，读到的只能是完整的旧状态或完整的新状态，不能看到落了一半的批次；单批件数、单件占地、总件数与撤销深度都是硬顶，超限拒绝而不是截断。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-21`
- 实现文件：`build_placement_validator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`BuildPlacementValidator`
