# 镜头光晕的光源调度与遮挡淡出

## 题面

驾驶舱视角里同一颗恒星在画面里出现两次，一次在正前方，一次跑到脚下；灯被柱子挡住之后立刻灭掉，柱子挪开又要等三四秒才慢慢亮起来，切镜头时还会残留上一帧的余晖。性能档位调低之后有些灯干脆一辈子不出现，美术在编辑器里数得出来少了两颗。

落点在 gsb/batch51-go-game-b-18 的 lens_flare_scheduler.go，类型 LensFlareScheduler，包名 task，模块路径 gsb/batch51-go-game-b-18。

公开接口保持：NewLensFlareScheduler(limits FlareLimits) *LensFlareScheduler、RegisterSource(id string, src FlareSource) error、UpdateSource(id string, src FlareSource) error、SetOcclusionResult(id string, visible float32) error、Plan(cam Camera) ([]FlareInstance, error)、SetMaxInstances(n int) error、SetFadeRates(in, out float32) error、Priority(id string) int、Intensity(id string) float32、SourceCount() int、Stats() FlareStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) 注册光源时编号不能为空，亮度与半径必须为正，重复编号报 ErrSourceExists，光源数超过 MaxSources 报 ErrSourceLimit；更新不存在的编号报 ErrUnknownSource，更新时同样逐项校验。
2) 遮挡结果按零到一之间的可见度写入，越界报 ErrBadOcclusion，未知编号报 ErrUnknownSource；可见度为零表示被完全挡住。
3) 实例上限必须为正，报错码为 ErrBadLimit；两个衰减速率都必须大于零小于一，否则报 ErrBadFade。
4) 相机视场角、视口边长必须为正，视线方向不能是零向量，否则报 ErrBadCamera，坏相机不得推进帧号。
5) 规划时把光源投到相机空间：深度不大于零的光源不产生实例，越出视口的不产生实例，两种情况都只把强度往零推。
6) 强度逐帧向目标靠拢，目标大于当前值时按淡入速率增加，小于时按淡出速率减少，结果钳在零到一之间；新光源从零起步，第一帧就按速率抬升而不是直接拉满。
7) 光源离开视口之后强度归零，但注册记录、遮挡结果与淡出进度都保留，重新进入时从当前强度继续抬升。
8) 实例的 Scale 取亮度乘半径再除以相机深度，按 Scale 降序排列，成绩相同按编号升序，超过 MaxInstances 只保留前面的那些，Order 从零开始编号。
9) Priority 返回该光源在最近一次规划中的位次，没有进入最近一次规划的返回负一；Intensity 返回当前淡出进度，未知编号返回零。
10) 每帧遮挡查询数受 MaxQueriesPerFrame 限制，用满之后剩下的光源记入 Deferred 并沿用上一帧的可见度结果照常参与排序，下一帧优先补查，不能永久丢弃。
11) 快照包含光源表、淡出进度、最近可见度、实例上限、衰减速率与帧号，载荷损坏报 ErrBadSnapshot 且状态不变；恢复后同一光源的强度接着原来的值继续推进。

硬约束：Plan、SetOcclusionResult、UpdateSource、Snapshot 会被渲染线程与后端回调并发调用，规划只能看到整批旧状态或整批新状态；单帧查询数与实例数都是硬顶，超出的按顺序顺延而不是永久丢弃；被上限截掉的光源不得改变其余光源的强度、成绩或位次。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发规划与快照恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-18`
- 实现文件：`lens_flare_scheduler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`LensFlareScheduler`
