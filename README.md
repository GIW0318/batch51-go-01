# 光束条带展开：切线断裂分段、视线平行切换展开轴与全局顶点预算降级

## 题面

光剑、拉索和技能拖尾换成新条带之后，玩家反馈三类问题：挥砍到一半刀光会自己打一个结，靠近镜头的位置出现一条翻折的亮带；镜头贴近光束平面时整条光带逐帧在两种朝向之间来回跳，看起来像在闪；同屏光束一多，最远处的几条直接消失，而不是变细。美术还发现宽度曲线改小之后近处条带仍然很粗，远处的细丝则被整条剔掉。

落点在 gsb/batch51-go-game-b-09 的 beam_ribbon_builder.go，类型 BeamRibbon，包名 task，模块路径 gsb/batch51-go-game-b-09。

公开接口保持：NewBeamRibbon(limits RibbonLimits) *BeamRibbon、SetActiveBeam(beamID string, priority int) error、Build(path []Vec3, camera Camera) (Ribbon, error)、SetWidthCurve(beamID string, keys []Key) error、WidthAt(beamID string, t float64) (float64, error)、Cull(view Frustum) []BeamID、SetBudget(vertices int) error、Budget() (int, int)、Stats() RibbonStats、Release(beamID string) error、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) 未调用 SetActiveBeam 时按名为 default、优先级零的光束处理；SetActiveBeam 拒绝空白光束号返回 ErrBadBeam，优先级为负返回 ErrBadPriority。
2) SetWidthCurve 要求键值非空、按 T 严格递增、T 落在 [0,1]、宽度为正，否则返回 ErrBadCurve；WidthAt 把 t 钳到 [0,1] 后分段线性插值，没有曲线登记的光束返回 ErrUnknownBeam。
3) Build 的路径至少两个点，否则 ErrShortPath；相邻两点重合返回 ErrDegeneratePath；相机 forward 为零向量返回 ErrBadCamera。
4) 中心线按相机朝向展开：每个路径点生成一对顶点，两个顶点以路径点为中点、沿展开轴各偏移半个宽度，顶点按路径点顺序成对排列在第 2i 与 2i+1 位。
5) 宽度沿路径按曲线采样，第 i 个点的 t 取 i 除以点数减一；没有登记曲线时用 DefaultWidth。
6) 宽度受最小像素宽度约束：按路径点到相机位置的距离换算像素宽度，不足 MinPixelWidth 时把世界宽度放大到刚好满足，放大后的宽度同时写进顶点间距与 Widths。
7) 切线突变超过 BreakAngleDeg 时断开为多段，断点同时作为前段末点与后段首点，顶点数为二乘以路径点数加断点数；段数超过 MaxSegments 返回 ErrTooManySegments。
8) 路径与视线接近平行时切换展开轴，并带滞回，不能逐帧翻转。
9) 路径在某一帧出现 180 度折返时必须断成两段，不能产生自交。
10) Cull 对每条已登记光束做包围球与视锥平面测试，返回存活光束号并按升序排列，被剔除的数量计入 Stats；Release 之后该光束不再参与 Cull，释放次数计入 Stats 的 Released，重复释放返回 ErrUnknownBeam。
11) SetBudget 的顶点预算必须为正，否则 ErrBadBudget；Budget 返回上次构建使用的顶点数与当前总预算；全局顶点预算耗尽时低优先级光束降级为线段，Ribbon 的 Degraded 置真并且只给两个端点，不能整体消失。
12) 快照带宽度曲线、已登记光束、预算与计数器；Restore 要么整体生效要么不改现有状态，损坏载荷返回 ErrBadSnapshot。

硬约束：Build、Cull、Release 会被渲染线程与技能线程并发调用，同一帧里 Cull 只能看到整批更新前或更新后的光束表；多段共用一份顶点缓冲时每段顶点按段对齐写入，不得越界覆盖相邻段；顶点总数与段数都受配置上限硬顶。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-09`
- 实现文件：`beam_ribbon_builder.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`BeamRibbon`
