# 双目渲染的共享可见性与姿态锁存提交

## 题面

头显换成高刷之后画面在转头时被撕成两半，右眼总比左眼晚半帧；靠墙走的时候画面边缘出现一圈被拉长的细条，越靠近视口边界越长。偶尔姿态时间戳乱序，整个人物在画面里闪回上一秒的位置，一帧之后又跳回来。性能分析显示某一帧左右眼送进去的绘制项数量差了整整一倍，之后那一侧的画面就再也没补齐。

落点在 gsb/batch51-go-game-b-19 的 stereo_frame_submitter.go，类型 StereoFrameSubmitter，包名 task，模块路径 gsb/batch51-go-game-b-19。

公开接口保持：NewStereoFrameSubmitter(budget StereoBudget) *StereoFrameSubmitter、SetBudget(budget StereoBudget) error、BeginFrame(hmd HmdState) error、SetEyePose(eye Eye, pose Pose, ts int64) error、LatchPose(eye Eye, at int64) (Pose, error)、ShareVisibility(vis VisibilitySet) error、SharedIDs() []uint32、PerEyeCull(eye Eye) ([]DrawItem, error)、Submit(eye Eye, items []DrawItem) error、EndFrame() (FrameReport, error)、Stats() StereoStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) BeginFrame 要求起始时间戳与刷新率都为正、帧号严格大于上一帧，否则报 ErrBadHmd；上一帧还没结束就再次开始报 ErrFrameOpen，坏帧同样不得推进帧号。
2) 眼别只接受左眼与右眼，其余报 ErrBadEye；帧未开始时的姿态、共享可见性、裁剪与提交都报 ErrFrameClosed。
3) 姿态必须带位置与朝向，全零姿态报 ErrBadPose；每只眼在帧内只能锁存一次，重复锁存报 ErrAlreadyLatched，锁存之后再送进来的姿态只影响下一帧。
4) 锁存返回该眼时间戳不晚于 at 的最新一次姿态；锁存前一次姿态都没有报 ErrNoPose；时间戳早于帧开始的姿态直接拒绝并沿用上一有效姿态，报 ErrStalePose。
5) 共享可见性按编号去重，重复编号或双眼都不命中的条目报 ErrBadVisibility；登记之后裁剪只读它，不得就地改写，调用方拿到编号副本之后改动也不影响内部状态。
6) 每眼裁剪按眼别过滤共享条目，结果按编号升序，DrawItem 带上眼别与该眼顶点数；一眼的条目数或顶点数超过上限报 ErrEyeBudget，只影响这一眼本次的裁剪。
7) 左右眼提交顺序固定为左先右后，先右后左、同一眼提交两次都报 ErrSubmitOrder；未知眼别报 ErrBadEye。
8) EndFrame 要求两眼都提交过，缺一只报 ErrSubmitOrder，没有打开的帧报 ErrFrameClosed；成功后返回帧号、两眼条目数、两眼锁存姿态，帧计数加一。
9) 重投影之后仍越出裁剪空间的绘制项整项丢弃，不得截断到边界上，否则画面边缘会出现拉长的细条；是否丢弃要看该条目已经用掉的重投影次数。
10) Stats 里的帧数、锁存次数、提交次数与预算拒绝次数跨帧累积，不随 BeginFrame 清零。
11) 快照包含帧号、锁存姿态、提交计数与预算，载荷损坏报 ErrBadSnapshot 且状态不变；恢复后帧号接着原来的值继续，预算与已完成帧数都还在。

硬约束：BeginFrame、SetEyePose、LatchPose、PerEyeCull、Submit 会被渲染线程与姿态回调线程并发调用，帧内状态只允许整帧可见；每眼条目数与顶点数是硬顶，超限只裁该眼本次的提交，不得连带另一眼；快照只能落在帧边界上，恢复后计数连续。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发锁存与快照恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-19`
- 实现文件：`stereo_frame_submitter.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`StereoFrameSubmitter`
