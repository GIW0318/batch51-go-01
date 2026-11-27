# 横屏转竖屏的自动重构图、主体轨迹平滑与安全区避让

## 题面

竖版短剧的自动重构上线后，人物一出画裁剪窗就跟着飘：主体从画面左边走到右边，窗子先冲过头再慢慢拉回，看久了发晕。带字幕的素材更糟，字幕被裁掉半行，运营只能手工重做。有一段素材主体被柱子挡了两秒，轨迹断开，窗子直接弹回画面正中，人回来时又弹一次。要上下裁的那一批，安全区优先级全乱了。

落点在 gsb/batch51-go-game-c-14 的 auto_reframe_planner.go，类型 AutoReframePlanner，包名 task，模块路径 gsb/batch51-go-game-c-14。

公开接口保持：NewAutoReframePlanner(limits Limits) *AutoReframePlanner、SetSource(width, height int) error、SetTargetAspect(a float64) error、SetSubjectTrack(track []SubjectSample) error、SetSafeRects(rects []Rect) error、SetSmoothing(window int) error、SetKeyframeInterval(frames int) error、Plan(start, end int64) (ReframePlan, error)、Validate(plan ReframePlan) error、Stats() ReframeStats、TargetAspect() float64、Smoothing() int、KeyframeInterval() int、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 缺省目标宽高比十六比九、平滑窗口三帧、关键帧间隔三十帧、关键帧上限两千零四十八、轨迹样本上限四千零九十六。比例只接受零点一到十，越界报 ErrBadAspect 并保留原值。
2) 源宽高必须为正，否则报 ErrBadSource；没有设置源尺寸就规划报 ErrNoSource；没有设置主体轨迹就规划报 ErrNoTrack。
3) 轨迹时间戳必须非递减、置信度落在零到一之间，否则报 ErrBadTrack；样本数超限报 ErrSampleBudget。安全区宽高必须为正、原点非负，否则报 ErrBadSafeRect。
4) 裁剪窗尺寸：目标比例不大于源比例时保持整高、宽度等于高度乘比例；大于源比例时保持整宽、高度等于宽度除比例；两个方向都四舍五入到整像素，并且不越出源画面。
5) 关键帧时间戳从 start 起按间隔递增到不超过 end，数量等于区间除间隔加一；关键帧数超限报 ErrKeyframeBudget；start 为负或 end 不大于 start 报 ErrBadRange。计划里的 Frames 是覆盖的源帧数，等于 end 减 start 加一。
6) 每个关键帧的主体位置按相邻样本线性插值，区间外取端点样本；平滑窗口大于零时取最近若干关键帧原始位置（未平滑）的算术平均，窗口为零表示不平滑。
7) 窗位等于平滑后的主体位置减去窗宽的一半，再夹到零与源宽减窗宽之间，竖直方向同理。
8) 安全区与窗位冲突时安全区优先：把窗位沿对应方向平移到刚好包含该安全区，仍受边界夹取；每平移一个关键帧，SafeShifts 加一。
9) 主体轨迹在关键帧处完全没有样本覆盖时按缺口处理，保持上一个稳定窗并缓慢回中，不能瞬移回画面正中；缺口关键帧数记进 Holes。
10) 目标比例大于源比例、需要上下裁剪时，安全区优先级要重新计算，不能沿用左右裁剪的判定。
11) 关键帧间隔与平滑窗口冲突时以不越界为准，不能为了等间隔把窗位推出画面。
12) Validate 拒绝窗位越界、宽高不为正、时间戳不递增与宽高比不符的计划，统一报 ErrInvalidPlan。

硬约束：Plan、SetSubjectTrack、Snapshot 会被预览线程与导出线程同时调用，同一实例的轨迹与安全区只能整批替换，快照要么看到替换前的轨迹，要么看到替换后的。关键帧数与轨迹样本数都是硬上限，超限报错而不静默截断。Snapshot 与 Restore 要能跨进程还原源尺寸、目标比例、轨迹、安全区、平滑窗口、关键帧间隔与全部计数，损坏的快照报 ErrBadSnapshot 且不动现网状态。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-14`
- 实现文件：`auto_reframe_planner.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AutoReframePlanner`
