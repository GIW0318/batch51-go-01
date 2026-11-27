# 雪地压痕累积：笔刷补间、边缘堆雪守恒与脏块版本化重载

## 题面

雪地场景上线后，玩家连续反馈三类问题：同一块雪被反复踩十几脚后脚陷得越来越深，最后半个角色沉进地面；滑雪板高速划过时压痕不是一条连续的槽，而是一串断开的坑；退出副本再进来，之前压出的车辙有一半叠成了两层，深度直接翻倍。美术把雪地最大压痕调小之后，已经踩过的区块深度没有任何变化，上传队列里同一个块也一直挂着传不完。

落点在 gsb/batch51-go-game-b-06 的 snow_deformation_tracker.go，类型 SnowDeformation，包名 task，模块路径 gsb/batch51-go-game-b-06。

公开接口保持：NewSnowDeformation(cfg SnowConfig) *SnowDeformation、Load(base HeightField) error、Stamp(brush Brush, at Vec3, pressure float64) error、Stroke(from, to Vec3, pressure float64, steps int) error、SampleHeight(pos Vec2) (float64, error)、DepthAt(pos Vec2) (float64, error)、RimHeight(pos Vec2) (float64, error)、Totals() (float64, float64)、Relax(dt time.Duration) error、FlushTiles(limit int) ([]TileDelta, error)、PendingTiles() int、TileVersion(tx, ty int) (uint64, error)、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) Load 安装基础高度图：Heights 长度必须等于 Width 乘 Height，Width 与 Height 又必须等于配置的 TilesX 乘 TileCells、TilesY 乘 TileCells，否则报 ErrBadHeightField；成功后压痕、堆雪与脏块全部清零，基础图版本记为 base.Version。
2) 还没装载基础图时 SampleHeight、DepthAt、RimHeight 一律返回 ErrNotLoaded；坐标落在 [0, 宽乘 CellSize) 与 [0, 高乘 CellSize) 之外返回 ErrOutOfBounds；块下标越界同样返回 ErrOutOfBounds。
3) 任意一点的高度等于基础高度加边缘堆雪减压痕深度。
4) Stamp 的压力必须落在 (0,1]，否则报 ErrBadPressure；笔刷半径必须为正、RimRatio 落在 [0,1]、RimSpread 大于 1，否则报 ErrBadBrush；落点在图外报 ErrOutOfBounds 并且地形一点不变。
5) 压痕：格心到落点距离 d 不超过半径的格子，本笔目标增量为 MaxDepth 乘 pressure 乘 (1 减 d 除以半径)，单格累计压痕以 MaxDepth 为上限，实际压下量按取上限之后的差值计。
6) 边缘堆雪：格心距离落在 (半径, 半径乘 RimSpread] 的格子承接 RimRatio 乘本笔实际压下总量，并在这一圈格子上均摊；单格堆雪上限为 MaxRim，到达上限的格子不再分摊，多余部分丢弃。
7) Relax 按 RecoverRate 每秒削减压痕，削减到零为止；恢复出来的雪不转成堆雪；dt 为负返回 ErrBadDuration。
8) 脏块：任何一次改动把所在块标记为脏并把该块版本加一，块版本在 Load 之后从零起；同一个块在 Flush 之前重复变脏只保留一条记录，版本取最新；FlushTiles 按先脏先传返回至多 limit 条，返回过的块不再算脏；limit 小于等于零报 ErrBadLimit；每条 delta 的 Bytes 取该块本次变动的格子数乘八。
9) 同一次落笔的格子改动必须整笔生效，读方不能看到一半压下、另一半还没堆雪的中间态。
10) 同一位置连续快速踩踏时压痕深度累积但不得超过 MaxDepth，边缘堆积不能无限增长。
11) 高速载具产生稀疏采样时，Stroke 必须在相邻两个采样点之间按 steps 插值补笔刷，两端各落一次，否则压痕会断成一串坑。
12) 快照带基础图版本、压痕、堆雪、块版本与脏块队列；Restore 要么整体生效要么不改现有状态，损坏载荷报 ErrBadSnapshot；重载之后脏块记录的基础图版本与当前基础图版本不一致时，以基础图为准丢弃过期脏块，不得把旧压痕叠上去。

硬约束：Stamp、SampleHeight、FlushTiles 会被物理线程与上传线程并发调用，读方只能看到整笔之前或整笔之后的雪面；单帧上传是硬预算，一次返回的字节总和不得超过 UploadBudget，放不下就少传，不允许超发；块版本与脏标记必须一起推进，不能出现版本加了但队列没进的情况。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-06`
- 实现文件：`snow_deformation_tracker.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SnowDeformation`
