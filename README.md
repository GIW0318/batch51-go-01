# 毛皮壳层生成：密度图控制层数、重力偏移回退与阴影壳层世界位置重采样

## 题面

狐妖角色的毛领近看像一排硬塑料片，边缘锯齿特别明显；美术把密度图里没毛的区域刷成零之后，那些地方直接透出黑洞，连底下的皮肤网格也跟着消失。换成有重力的长毛版本后，模型腹部的壳层穿透到地面以下，整片毛被从根部截短，而背部完全没变。最麻烦的是阴影：主壳层换过层数之后，阴影壳层还按旧的序号一一对应，投出来的影子错位半层，远处物体的阴影开销也没降下来。

落点在 gsb/batch51-go-game-b-04 的 fur_shell_generator.go，类型 FurShellGenerator，包名 task，模块路径 gsb/batch51-go-game-b-04。

公开接口保持：NewFurShellGenerator(limits FurLimits) *FurShellGenerator、Generate(mesh Mesh, density DensityMap) (ShellSet, error)、SampleDensity(uv UV) (float64, error)、BuildShadowShells(set ShellSet, maxLayers int) (ShellSet, error)、SetOffsetPolicy(p OffsetPolicy) error、OffsetPolicy() OffsetPolicy、TargetLayers(density float64) (int, error)、LayerAt(set ShellSet, index int) (ShellLayer, error)、VertexTotal(set ShellSet) int、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) Generate 校验网格：没有顶点、法线长度为零、UV 不在零到一之间返回 ErrBadMesh；密度图宽高必须为正、Values 长度等于宽乘高、每个值落在零到一之间，否则返回 ErrBadDensity。
2) TargetLayers 把密度乘 MaxLayers 四舍五入并钳到零到 MaxLayers，密度不在零到一之间返回 ErrBadDensity。
3) 每个顶点按自身密度决定层数；层偏移方向为法线加重力加风再归一化，向量和为零时退回法线；第 i 层距离为 LayerSpacing 乘 i 加一，再乘方向得到 Offset 向量；MaxOffset 大于零时距离不得超过它。
4) 聚合层：第 i 层的 Vertices 是层数大于 i 的顶点个数；Alpha 为 1 减 (i+1) 除以 (层数+1)，这里的层数是本次实际生成的层数；最外层 Tip 为 true，其余为 false；VertexCount 为所有顶点层数之和。
5) 密度为零的顶点不生成任何壳层，但这些区域必须保留底层皮肤可见；只要有一个顶点没有壳层，SkinVisible 就为 true，全都有壳层时为 false。
6) 总顶点数超过 MaxVertices 时逐步压低每顶点层数上限直到装下，压低后 Alpha 按新的层数重算；压到每顶点一层仍超预算返回 ErrVertexBudget。
7) 重力偏移让壳层在模型底部穿透地面时按局部法线回退该顶点的偏移，不能把整片毛统一缩短。
8) SampleDensity 对最近一次 Generate 的密度图做双线性采样，UV 越界钳到边缘；还没 Generate 过返回 ErrNotReady。
9) BuildShadowShells 的层数取 maxLayers、limits.ShadowLayers 与主壳层数三者的最小值，maxLayers 不大于零返回 ErrBadLayers；ShellSet.Distance 超过 ShadowDistance 时返回空的阴影集且不报错，Shadow 标志仍为 true；阴影壳层与主壳层序号不对应时按世界位置重采样，不能按序号硬配，且只对近距离对象生成。
10) SetOffsetPolicy 的 MaxOffset 为负返回 ErrBadPolicy，合法策略只影响后续 Generate；OffsetPolicy 返回当前策略；LayerAt 越界返回 ErrBadLayers；VertexTotal 等于集合的 VertexCount；快照带密度图与偏移策略，Restore 要么整体生效要么不改现有状态，损坏载荷返回 ErrBadSnapshot。

硬约束：Generate、SampleDensity 与 Snapshot 会被渲染线程并发调用，读到的要么是完整旧集合要么是完整新集合；壳层顶点数受 MaxVertices 硬顶；快照与生成互斥。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-04`
- 实现文件：`fur_shell_generator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`FurShellGenerator`
