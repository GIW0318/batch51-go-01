# 发丝分簇与发束 LOD：跨区归属、覆盖率合并与两级淡入顶点预算

## 题面

角色换成头发细分之后，靠近镜头的一缕头发在发束 LOD 切换时突然左右甩一下，远看整片头发像块硬板；美术把发束卡片化之后轮廓又塌了，剪影边缘能明显看出四方的卡片。更麻烦的是额角那束跨过分区的头发，一半被算进头顶、一半被算进后颈，切级时两半各自挑一个档位，中间直接裂开一道缝。验收时还发现淡入叠了两级之后顶点数翻倍，移动端当场掉帧。

落点在 gsb/batch51-go-game-b-03 的 hair_strand_lod_builder.go，类型 HairStrandLod，包名 task，模块路径 gsb/batch51-go-game-b-03。

公开接口保持：NewHairStrandLod(limits StrandLimits) *HairStrandLod、Cluster(guides []GuideStrand) ([]StrandCluster, error)、BuildLod(clusterID string, level int) (LodMesh, error)、SelectLod(view Camera, budget int) []LodSelection、SetFade(d time.Duration) error、AdvanceFade(elapsed time.Duration) []FadeWindow、Fades() []FadeWindow、ClusterOf(strandID string) (string, bool)、Mesh(clusterID string, level int) (LodMesh, bool)、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) Cluster 校验：guides 为空、ID 或 Section 为空白、ID 重复、Strands 不大于零、Coverage 不在零到一之间、WindPhase 为负都返回 ErrBadInput，且整批不落地；引导发丝总数超过 MaxGuides 返回 ErrTooManyGuides，不同分区数超过 MaxClusters 返回 ErrClusterLimit。
2) 一个头皮分区聚成一个簇，簇 ID 就是分区名；同一发束的引导发丝必须整体落在同一个簇里，发束跨分区时按该发束第一条引导发丝所属分区归属，不能从中间切断。簇内 Guides 按 ID 升序，Strands 求和，Coverage 与 WindPhase 取算术平均，Root 取分量平均；簇按 ID 升序返回。
3) ClusterOf 能按任意引导发丝 ID 找回所属簇，未登记的 ID 返回 false。
4) BuildLod 对未知簇返回 ErrUnknownCluster，level 不在零到 LevelCount 减一之间返回 ErrBadLevel。
5) 第 level 级的发丝数为 base 除以 2 的 level 次方后向上取整；非卡片级顶点数为发丝数乘 StrandVertices；最高级用卡片替代，卡片数为发丝数除以 StrandsPerCard 向上取整，顶点数为卡片数乘 CardVertices。
6) MaxStrandsPerLevel 大于零时每级发丝数受它硬顶，超出部分优先合并覆盖率低的发束，Merged 记录被合并掉的发丝数，顶点数按合并后的发丝数重算。
7) SelectLod 按相机到簇 Root 的距离除以 LevelDistance 取整得到档位并钳到合法范围；预算不足时先把最远簇逐级降档，降到最高级仍超预算就丢掉最远的簇，近处簇优先保留；返回结果按簇 ID 升序。
8) 选中档位与上一帧不同且淡入时长大于零时返回 Fading 为 true、FromLevel 为旧档、VertexCount 为两级顶点数之和；淡入期间两级同时存在，总顶点预算按两级之和计算，超限则延后切换。
9) 切换档位时风力动画相位必须连续，不能因为发束合并而跳变；新级完整提交前旧级继续渲染。
10) SetFade 负时长返回 ErrBadFade，零表示不做淡入；AdvanceFade 推进窗口并按簇 ID 升序返回仍未完成的窗口，推进超过时长后窗口结束；Fades 返回当前活动窗口。
11) 快照带簇表、各级网格与淡入状态，Restore 要么整体生效要么不改现有状态，损坏载荷返回 ErrBadSnapshot；顶点预算与每级发丝上限都是硬顶，不得超发。

硬约束：Cluster、BuildLod、SelectLod 会被渲染线程并发调用，簇表与网格只在整批提交后可见；快照与提交互斥；恢复后 ClusterOf 与 Mesh 的结果与中断前一致。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-03`
- 实现文件：`hair_strand_lod_builder.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`HairStrandLod`
