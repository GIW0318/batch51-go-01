# 头显晚期锁定重投影：旋转与位置分离、纯旋转降级与连续丢帧回升

## 题面

VR 版本上线后，玩家转头时画面边缘被撕开一条黑边，快速平移时整个视野向左偏半米；网络抖一下丢了两帧，画面直接掉到最低画质，之后再也没有恢复，玩家只能重启关卡。更奇怪的是同一个显示帧里姿态更新了三次，重投影拿到的方向却是三次叠加后的结果，画面像被拧了一圈。

落点在 gsb/batch51-go-game-b-05 的 late_latch_reprojector.go，类型 ReprojectionGuard，包名 task，模块路径 gsb/batch51-go-game-b-05。

公开接口保持：NewReprojectionGuard(cfg ReprojectConfig) *ReprojectionGuard、BeginFrame() (int64, error)、Latch(pose Pose, at time.Time) (LatchID, error)、LatchInfo(frameID int64) (Pose, bool)、Reproject(image FrameImage, latest Pose) (FrameImage, ReprojectStats, error)、Degrade(reason string) error、Recover() (Quality, error)、ReportFrame(ok bool) error、Quality() Quality、DroppedFrames() (int, int)、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 配置校验：MaxParallax 与 MaxShift 不得为负，MaskStep、LostThreshold、RecoverFrames、PixelPerMeter 必须为正，MaxQuality 只能是 high、medium、low、off 四个档位之一或留空表示 high，不满足时 Latch 返回 ErrBadConfig。
2) BeginFrame 每调用一次开一帧，帧号从一起严格递增；还没开帧就 Latch 返回 ErrNoFrame；Latch 锁存当前帧姿态并返回从一起严格递增的 LatchID，同一帧内多次姿态更新只保留最后一次、之前的更新不得累积，但仍返回同一个 LatchID。
3) LatchInfo 按帧号取回锁存姿态，未知帧返回 false。
4) Reproject 要求 image.FrameID 对应的帧已经锁存，否则返回 ErrNoLatch；图像宽高必须为正且 Pixels 长度等于宽乘高，否则返回 ErrBadImage。
5) 旋转差取 Yaw、Pitch、Roll 三个分量差绝对值的最大值，单位度；位置差是两姿态位置的欧氏距离。
6) 位置差不超过 MaxParallax 时按旋转加平移重投影：ShiftX 与 ShiftY 取位置差在 X、Y 上的分量乘 PixelPerMeter，再按整体长度钳到 MaxShift；Mode 为 rotation_parallax。
7) 位置差超过 MaxParallax 时退化为纯旋转：ShiftX 与 ShiftY 置零，EdgeMask 增加 MaskStep，Mode 为 rotation_only；不允许直接黑屏，也不允许丢弃整帧。
8) 重投影结果把本次旋转差写进图像 Rotation 的三个分量，Shift 与 EdgeMask 按上面规则写入；Stats 带帧号、模式、旋转差、位置差、边缘遮罩与当前质量。
9) Degrade 原因空白返回 ErrBadReason 且质量不变，合法原因把质量沿 high、medium、low、off 降一档，已经在 off 时保持 off 并返回成功；ReportFrame 记录一次成功或丢帧，丢帧时连续计数与总数都加一，连续丢帧达到 LostThreshold 时质量降一档并把连续计数清零，成功帧清零连续计数，降级后连续成功达到 RecoverFrames 才回升一档，恢复期不得直接跳回最高档。
10) Recover 在质量已经是最高档时返回 ErrNotDegraded，在连续成功帧数不足 RecoverFrames 时返回 ErrRecoverPending，满足条件才回升一档；快照带锁存表、帧号水位、丢帧计数与质量档，Restore 要么整体生效要么不改现有状态，损坏载荷返回 ErrBadSnapshot，恢复后新帧号必须大于此前任何一帧。

硬约束：一帧只允许一次有效锁存，BeginFrame、Latch、Reproject 与质量计数器会被渲染线程并发调用，锁存表与计数器只在完整更新后可见；质量只能沿档位阶梯一档一档变化，不能跨越；快照与锁存互斥。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-05`
- 实现文件：`late_latch_reprojector.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ReprojectionGuard`
