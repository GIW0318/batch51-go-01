# 体积云密度场构建、步进预算规划与阴影缓存失效

## 题面

阴天场景上线后，云层在远景处整块消失，镜头推进到云底附近又突然糊成一片白；打开体积光后，云层边缘每隔几条屏幕行就出现一道明显的条带，越靠近地平线越密。另一个更隐蔽的问题出现在阴影上：同一片云在光源转向后仍然沿用旧角度的投影，地面上的云影和云本身对不上，美术反复调整天气图也压不住。

落点在 gsb/batch51-go-game-b-02 的 volumetric_cloud_planner.go，类型 CloudVolume，包名 task，模块路径 gsb/batch51-go-game-b-02。

公开接口保持：NewCloudVolume(cfg CloudConfig) *CloudVolume、Build(weather WeatherMap, camera Camera) (Volume, error)、PlanSteps(view Viewport, budget int) []StepRange、SampleShadow(pos Vec3, lightDir Vec3) (float64, error)、InvalidateRegion(bounds Bounds) error、DensityAt(pos Vec3) (float64, error)、CoverageAt(x, y float64) (float64, error)、CloudBounds() (Bounds, bool)、ShadowCache() ShadowStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) Build 校验配置：TopAltitude 必须大于 BaseAltitude，CoverageThreshold 落在零到一之间，MaxSteps、PlanRows、ShadowBuckets 必须为正，AngleToleranceDeg 必须大于零，否则返回 ErrBadConfig。天气图宽高必须为正、Coverage 长度等于宽乘高、ExtentX 与 ExtentY 为正、每个覆盖度落在零到一，否则返回 ErrBadWeather。两种失败都不得留下半建状态。
2) 建成的 Volume 边界在 X、Y 上是零到 ExtentX 与零到 ExtentY，Z 上是 BaseAltitude 到 TopAltitude；Coverage 取天气图全部格点的算术平均，Threshold 原样带出；密度格点每轴四格共六十四格，每格取值落在零到一。
3) CoverageAt 先按 ExtentX、ExtentY 把坐标映射到天气图的列与行，再做双线性插值，越界坐标钳到边缘行列。
4) 密度由覆盖度、高度层与确定性三维噪声合成：Z 不在云底到云顶之间直接返回零（含两端），覆盖度低于阈值返回零，其余位置为正；同一坐标多次采样结果必须相同，且整片区域不能是常数。
5) PlanSteps 把视口按 PlanRows 等分，行号零是最近处；预算不少于行数时每行先分一步，余量按行数减行号的权重摊到各行，除不尽的余数从最近的行开始逐行补一，保证总和恰好等于预算，近处的步数不少于远处；预算小于行数时只给最近的前若干行各一步；行区间的 Start 与 End 是零到一的比例。
6) budget 为零或负数时按 MaxSteps 处理；建成之前调用 PlanSteps 返回空。
7) SampleShadow 要求已建成，否则返回 ErrNotBuilt；光方向长度为零返回 ErrBadLight；采样点沿单位光方向前进云层厚度的八分之一，透过率为一除以一加该点密度，密度为零时严格等于一。
8) 阴影缓存按位置桶与光方向桶复用，同一位置同一方向第二次采样必须命中；命中但实际光方向与缓存记录方向的夹角超过 AngleToleranceDeg 时整桶作废重采样，不得按旧角度直接返回。
9) InvalidateRegion 删除采样点落在盒内的缓存条目并累计 Invalidated；未建成返回 ErrNotBuilt；盒为空或没有命中条目不报错。
10) 相机进入云层内部时步进从相机位置开始，并扣掉已经穿过的那一段，不能从云底重新算一遍。
11) 天气图插值跨过覆盖阈值时必须按新密度重算云边界，不能沿用上一帧的边界。
12) 快照带天气图、相机、建成状态与缓存计数，Restore 要么整体生效要么不改现有状态，损坏载荷返回 ErrBadSnapshot。

硬约束：Build 与 SampleShadow 会被渲染线程并发调用，缓存读写不得返回半更新条目；每帧步进总量受预算硬顶，不得超发；快照与缓存更新互斥。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-02`
- 实现文件：`volumetric_cloud_planner.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CloudVolume`
