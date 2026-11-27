# 地形 Splat 笔刷的 dab 累积与脏块撤销

## 题面

美术在地形上刷出来的草线一跨块就断成两截，同一笔从缓坡刷到陡坡，接缝两侧的权重一边偏一边淡；重传的输入偶尔会把某一笔的浓度刷到发白。更麻烦的是撤销：撤掉一笔之后法线还留着一道亮痕，草丛也还长在原来的位置，得手动再刷一遍才能压下去。

落点在 gsb/batch51-go-game-b-16 的 terrain_splat_brush.go，类型 TerrainSplatBrush，包名 task，模块路径 gsb/batch51-go-game-b-16。

公开接口保持：NewTerrainSplatBrush(limits BrushLimits) *TerrainSplatBrush、Limits() BrushLimits、BeginStroke(st StrokeStart) (StrokeID, error)、AddDab(id StrokeID, d Dab) error、EndStroke(id StrokeID) (StrokeResult, error)、CancelStroke(id StrokeID) error、SampleWeight(x, y, layer int) (float32, error)、DirtyBlocks() []BlockCoord、FlushDerived(blocks []BlockCoord) error、Undo(id StrokeID) error、ActiveStrokes() []StrokeID、Stats() BrushStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) 开笔时图层只接受第零到第三层，半径与间距必须为正，坐标与地形编号不得为负，否则分别报 ErrBadLayer 与 ErrBadDab；笔画编号从一起严格递增，ActiveStrokes 按编号升序给出进行中的笔画。
2) 每条 dab 的图层、半径、权重与坐标逐条校验，越界报 ErrBadLayer 或 ErrBadDab；超过 MaxDabsPerStroke 的那条 dab 直接拒绝并报 ErrDabLimit，不计入本笔，本笔其余内容照常提交。
3) 提交时按像素把各层原始权重折算成定点：每层取 round(w 乘 65535 除以总和)，舍入残差吸收到权重最大的那一层，四层之和精确等于一；没有写入过的像素四层都是零，SampleWeight 读到的是折算后的值，图层越界报 ErrBadLayer。
4) 脏块由像素坐标整除 BlockSize 得到，提交结果里的块列表按块坐标升序去重；同一个块只留一份前后像。
5) 单笔脏块数超过 MaxDirtyBlocksPerStroke 报 ErrDirtyLimit，整笔作废，不得留下半份权重或半份脏块。
6) 单笔需要的前后像字节数超过 MaxSnapshotBytes 报 ErrSnapshotLimit，同样整笔作废；每块前后像等于 BlockSize 见方乘四层再乘四字节的两份拷贝，StrokeResult 里的字节数就是块数乘这个值。
7) FlushDerived 把给定块登记为法线与草层派生块；块当前不脏报 ErrUnknownBlock 并且一个都不登记；同一块重复登记只计一次。
8) Undo 只接受已提交的笔画，把该笔覆盖的块恢复到写入前的像，撤销后同一像素回到上一笔的结果；CancelStroke 既能作废进行中的笔画，也能回滚已提交的笔画，两种都计入取消计数。
9) 已经结束、撤销或取消的编号再操作报 ErrUnknownStroke 或 ErrStrokeClosed，编号不回退也不复用；对未知编号的 dab 报 ErrUnknownStroke。
10) 快照包含像素权重、脏块集合、派生标记、笔画记录与计数器；载荷损坏报 ErrBadSnapshot 且现有状态一点不动。
11) Limits 返回真正生效的上限，构造时没填的项补默认值：块边长十六、单笔脏块三十二、单笔 dab 上限二百五十六、快照预算一兆字节。

硬约束：BeginStroke、AddDab、EndStroke、Undo、Snapshot 会被多路输入与渲染线程同时调用，一笔只能整笔提交或整笔不提交；按地形分区加锁，相邻块不得同时进入写入阶段；快照只能看到全量旧状态或全量新状态，恢复后笔画编号、脏块与撤销栈都要接得上。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发提交与快照恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-16`
- 实现文件：`terrain_splat_brush.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`TerrainSplatBrush`
