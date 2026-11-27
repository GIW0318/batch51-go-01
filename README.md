# 关注区域编码的 QP 偏移分配、码率守门与逐帧平滑

## 题面

屏幕共享和游戏直播改用关注区域编码之后，主讲人的脸反而比背景更糊：框选区域一设，背景块每帧都在闪，播放器像在呼吸。把目标码率压到会议档之后更糟，人物整块变成色斑，本来静止的背景也开始糊。复现时又发现：有人在预览里把框拖出画面，服务没有报错，整帧都被当成兴趣区；切换场景之后，前几帧的量化还在沿用上一场景的偏移。

落点在 gsb/batch51-go-game-c-12 的 roi_bitrate_allocator.go，类型 RoiBitrateAllocator，包名 task，模块路径 gsb/batch51-go-game-c-12。

公开接口保持：NewRoiBitrateAllocator(limits Limits) *RoiBitrateAllocator、SetRoi(rect Rect, weight float32) error、ClearRoi() error、SetSaliencyMap(m SaliencyMap) error、PlanQP(frame Frame, targetBits int) (QPTable, error)、SetQPRange(min, max int) error、SetSmoothWindow(frames int) error、MarkSceneCut() error、Commit(qp QPTable) error、Stats() RoiStats、Roi() (Rect, float32, bool)、QPRange() (int, int)、SmoothWindow() int、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 缺省块边长十六、QP 允许区间十到四十五、平滑窗口四帧、每帧块数上限四千零九十六、显著性采样上限六万五千五百三十六。块数与采样数超限分别报 ErrBlockBudget 与 ErrSampleBudget，超限不产出表也不计数。
2) 块数等于按块边长向上取整后的列数乘行数。基准 QP 按每块目标比特数分档：不低于一千零二十四取二十，不低于五百一十二取二十六，不低于二百五十六取三十二，不低于一百二十八取三十八，其余取四十五，再夹到允许区间；没有权重的普通块在基准上加二。宽高不为正或块边长为负报 ErrBadFrame，目标比特不为正报 ErrBadBits。
3) SetRoi 要求宽高为正、原点非负、权重大于零且不超过四，否则报 ErrBadRect 或 ErrBadWeight；面积为零一律拒绝，不能悄悄扩成整帧。
4) 计划时 ROI 必须完整落在画面内，越界报 ErrRoiOutOfFrame 且不产出表、不累加计数。
5) 块权重换算：ROI 块按权重乘以四向下取整降 QP；显著性图按采样值除以六十四得到权重，同样乘以四向下取整；显著性图的宽高必须为正且样本数等于宽乘高，否则报 ErrBadSaliency，失败时保留原图。最终 QP 一律夹到允许区间。
6) SetQPRange 要求下界不小于零、上界不大于五十一、下界严格小于上界，否则报 ErrBadQPRange；SetSmoothWindow 只接受零到六十四，越界报 ErrBadWindow；失败保留原值。
7) 平滑：窗口大于零时，同一块相对上一次已提交的 QP 每帧变化不超过八除以窗口向上取整；窗口为零表示不限；被限幅的块计入 Clamped。
8) Commit 只接受已经规划、尚未提交、行列与计划一致的表，否则分别报 ErrUnknownPlan、ErrStaleFrame、ErrBadTable；提交后 LastFrameID 前进，重复提交同一帧拒绝。
9) ROI 与显著性图重叠时块权重取两者较大者，不能相加，否则同一块被重复降码。
10) 目标码率极低时 ROI 也要按比例降码，不能把 ROI 保到整帧不可用。
11) 场景切换时调用 MarkSceneCut 清空平滑历史，否则切换后几帧仍带上一场景的偏移。

硬约束：PlanQP、Commit、Stats 会被采集线程与编码线程同时调用，同一帧只能提交一次；Snapshot 要么看到整批提交前的 ROI、显著性图与平滑历史，要么看到提交后的。每帧块数与显著性采样数是硬上限，超限报错而不是静默降采样。Snapshot 与 Restore 要能跨进程还原 ROI、权重、显著性图、QP 区间、平滑窗口、平滑历史与全部计数，损坏的快照报 ErrBadSnapshot 且不动现网状态。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-12`
- 实现文件：`roi_bitrate_allocator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RoiBitrateAllocator`
