# 卷帘快门校正：逐行曝光与陀螺对齐

## 题面

运动相机拍滑板跟拍，画面里的栏杆是斜的，人一跑整帧像被拧了一把；换成二百四十帧慢动作之后斜得更明显，远处的路灯杆一段直一段弯。调参同学把校正强度拉满，边缘反而被拉出彩色拖影。另一段素材中间切了镜头，切完整帧一直歪着，直到重新导入才恢复。

落点在 gsb/batch51-go-game-c-03 的 rolling_shutter_corrector.go，类型 RollingShutterCorrector，包名 task，模块路径 gsb/batch51-go-game-c-03。

公开接口保持：NewRollingShutterCorrector(cfg ShutterConfig) *RollingShutterCorrector、Calibrate(meta FrameMeta) (ShutterModel, error)、Correct(frame Frame, gyro []GyroSample) (Frame, error)、RowExposure(model ShutterModel, row int) (int64, error)、Integrate(gyro []GyroSample, fromUS, toUS int64) (Vec3, error)、SetGrid(res GridSize) error、Reset(reason string) error、Snapshot() ([]byte, error)、Restore([]byte) error、Grid() GridSize、StrengthMax() float64、Segments() int、Clamped() int、Model() (ShutterModel, bool)、SampleBudget() int。

需要落实的行为：

1) 配置缺省时读出时间取两万微秒、强度上限取零点八、网格取十六乘九、每帧陀螺样本上限取一千零二十四、网格顶点上限取四千零九十六。
2) 标定要求宽高为正、起始时刻不为负；每行曝光间隔等于读出时间整除行数，除不尽向下取整，结果小于一微秒报 ErrBadMeta；第零行曝光时刻等于起始时刻，第 n 行等于起始时刻加 n 乘每行间隔，行号为负或不小于行数报 ErrBadRow。
3) 积分按梯形法对区间端点插值，角速度单位是度每秒、时间单位是微秒；十度每秒累计一百毫秒得到一度，零点二五到零点七五秒区间得到零点五度；空样本返回零向量，起止时刻颠倒报 ErrBadInterval。
4) 校正前必须已经标定，否则报 ErrNotCalibrated；帧高与模型行数不一致或宽为零报 ErrBadFrame。
5) 每行偏移取从帧起始时刻到该行曝光时刻的积分，四行、每行一千微秒、十度每秒时应得到零、零点零一、零点零二、零点零三度。
6) 返回帧保留原编号与尺寸，并带上逐行偏移、实际施加的强度与是否被钳制的标记。
7) 陀螺样本数超过每帧上限报 ErrSampleBudget，不截断也不吞掉多余样本。
8) 网格任一边小于一或顶点总数超过上限报 ErrBadGrid，失败时保留原网格。
9) 场景切换或陀螺缺口要分段重置，分段计数随之加一；重置原因为空报 ErrBadReason。
10) 校正强度有上限，超出上限按比例衰减并标记，不能继续放大导致边缘拉伸。
11) 陀螺样本时间戳乱序或重复时按最近有效样本插值并记录缺口，不能累加负时间。
12) 场景切换后第一帧必须清空积分历史，不能把上一场景的旋转偏移带过来。

硬约束：Calibrate、Correct、Snapshot 会被预览线程与导出线程同时调用，快照里的模型、网格、分段计数要么全是旧值要么全是新值；Correct 与 Reset 并发时不能让重置后的帧拿到上一分段的积分。网格顶点数、每帧样本数与强度上限都是硬顶。Snapshot 与 Restore 必须跨进程还原模型、网格、分段计数与已钳制帧数，损坏快照报 ErrBadSnapshot 且不动现网。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-03`
- 实现文件：`rolling_shutter_corrector.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RollingShutterCorrector`
