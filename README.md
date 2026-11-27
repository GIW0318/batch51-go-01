# 皮肤次表面散射扩散剖面烘焙与预积分表

## 题面

皮肤角色在近距离过场镜头里塑料感很重：脸颊本该透出的暖红完全看不到，耳廓边缘反而糊成一片灰；美术改了散射颜色和平均自由程，重启客户端后画面一点变化都没有。更麻烦的是把厚度曲线改成中段带平台的一版之后，靠近耳根的位置在连续两帧里明暗反转，烘焙到一半切场景还会看到新旧两张表拼在同一个角色身上。

落点在 gsb/batch51-go-game-b-01 的 subsurface_profile_baker.go，类型 SubsurfaceProfile，包名 task，模块路径 gsb/batch51-go-game-b-01。

公开接口保持：NewSubsurfaceProfile(limits BakeLimits) *SubsurfaceProfile、Bake(id string, cfg ScatterConfig) (ProfileTable, error)、Advance(budget int) (BakeProgress, error)、Sample(id string, radius, thickness float64) (Spectrum, error)、Invalidate(id string) error、Profile(id string) (ProfileTable, bool)、Pending(id string) (BakeProgress, bool)、Version(id string) (int64, error)、ChangedRows(id string, cfg ScatterConfig) ([]int, error)、Count() int、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 配置校验：散射三分量都必须在零到一之间，平均自由程每个通道必须大于零，厚度曲线至少两个点且厚度严格递增、半径不得递减，ThicknessMin 必须小于 ThicknessMax，MaxRadius 必须大于零，Rows 不得小于二；任何一条不满足返回 ErrBadConfig，现有表保持不动。
2) Rows 超过 limits.MaxRows 返回 ErrRowLimit，不截断；已提交表份数达到 MaxProfiles 后新建返回 ErrProfileLimit。
3) 共 Rows 行，第 i 行厚度按 ThicknessMin 到 ThicknessMax 等距划分并含两端；半径取曲线在该厚度上的分段线性插值再钳到零到 MaxRadius；半径随行号单调不减。曲线中段出现平台时半径保持不变，但采样不得在平台两侧来回跳。
4) 每行每个通道的扩散权重为 scatter 乘 mfp 除以 mfp 加 radius，再乘一加 Curvature，结果先钳到零到一；整表提交时每个通道的权重列各自归一化到总和一，该通道 scatter 为零时整列保持零；TotalEnergy 是归一化后各通道之和，MeanFreePath 原样保留。
5) 曲率补偿只改变局部亮度，不改变整表总能量；某个通道的局部权重被抬过一时按通道钳到一，不能对整行或整列统一缩放，否则色相会漂。
6) Sample 先按 thickness 在行间线性插值，低于首行取首行、高于末行取末行，再乘径向衰减 mfp 除以 mfp 加 radius；radius 为负返回 ErrBadConfig，未知 id 返回 ErrUnknownProfile。
7) Bake 受 RowBudget 控制分帧：一次最多烘 limits.RowBudget 行，RowBudget 为零时按八行处理；没烘完时返回的表视图 Complete 为 false，Profile、Version 与 Sample 在此期间只能看到已提交的旧表。
8) Advance 继续当前待完成任务，budget 为零或负数时用 RowBudget，没有待完成任务返回 ErrNoPendingBake；最后一行烘完才一次性提交整表，版本号加一。
9) 烘焙中途同一 id 再次 Bake 视为配置变更：旧任务作废，新任务从零开始，提交时整表替换，绝不能把两次任务的行拼在一起。
10) ChangedRows 对已提交 id 返回与当前表相比厚度或半径变化的行号，从零起升序；配置完全相同时返回空；未知 id 返回 ErrUnknownProfile。
11) Invalidate 删除某个 id 的已提交表和待完成任务，未知 id 返回 ErrUnknownProfile，删除后 Sample 报 ErrUnknownProfile；快照包含已提交表、待完成任务和版本水位，Restore 要么整体生效要么不改现有状态，载荷损坏返回 ErrBadSnapshot。

硬约束：Bake、Advance、Sample、Snapshot 会被渲染线程并发调用，读到的只能是完整旧表或完整新表，不得读到半张表；每份表行数受 MaxRows 硬顶；提交、失效与快照互斥。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-01`
- 实现文件：`subsurface_profile_baker.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SubsurfaceProfile`
