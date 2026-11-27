# 流体表面模拟：粒子推进子步、邻域查询预算与密度阈值网格重建

## 题面

流体表面上线后，水面翻滚时出现三类问题：瀑布口附近的粒子成片穿出封闭水槽，掉到场景外再也收不回来；同一股水流在 30 帧和 60 帧下速度不一样，帧率越低粒子飞得越远；水面网格在低密度碎片处留下一堆漂浮的小三角，帧率一紧张整个水体又缩成一张平板。玩家还能看到粒子在槽底两个面交界处来回抖动，像被两只手你来我往地推。

落点在 gsb/batch51-go-game-b-07 的 fluid_surface_scheduler.go，类型 FluidSimScheduler，包名 task，模块路径 gsb/batch51-go-game-b-07。

公开接口保持：NewFluidSimScheduler(cfg FluidConfig) *FluidSimScheduler、AddParticle(pos, vel Vec3) (ParticleID, error)、RemoveParticle(id ParticleID) error、SetBoundary(bounds Bounds) error、Step(dt time.Duration, budget int) (FluidState, error)、Neighbors(pos Vec3) []ParticleID、BuildSurface(state FluidState) (Mesh, error)、SetDensityThreshold(n int) error、State() FluidState、PendingParticles() int、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) 配置校验：MaxSubsteps、NeighborBudget、MaxParticles、MaxVertices、DensityThreshold 不得小于一，MaxStep 与 NeighborRadius 必须为正，Restitution 落在 [0,1]，不满足时 Step 与 AddParticle 返回 ErrBadConfig。
2) AddParticle 的粒子号从一开始严格递增，删除后也不回收；State 返回的粒子表按粒子号升序排列；位置或速度出现 NaN、Inf 返回 ErrBadParticle；粒子数达到 MaxParticles 返回 ErrParticleLimit；设置过边界后落在盒外的粒子返回 ErrOutOfBounds。
3) SetBoundary 要求三个轴上的 Min 都严格小于 Max，否则返回 ErrBadBounds 且旧边界继续生效。
4) Step 的 dt 必须为正、budget 必须为正，否则分别返回 ErrBadDuration 与 ErrBadBudget；子步数取 dt 除以 MaxStep 向上取整、最少一步，子步数超过 MaxSubsteps 时整帧返回 ErrSubstepLimit 且一个粒子都不动。
5) 每个子步按速度乘子步时长推进位置，位移总量等于速度乘 dt，不得因为拆分子步而放大速度；Step 返回的 Substeps、Tick、Advanced 与 Particles 要如实反映这一帧。
6) 边界反弹按轴逐个处理：越界分量镜像回盒内并把该轴速度取反乘 Restitution，另外两轴不受影响。
7) 粒子在边界夹角处同时命中两个面时只按更近的那个面反弹一次，不能两面交替推着来回抖动。
8) budget 是本帧允许推进的粒子数硬预算：按粒子号升序至多推进 budget 个；没推进的粒子位置与速度保持上一帧原样，进入待推进队列，下一帧优先于新加入的粒子推进。
9) Neighbors 返回距离在 NeighborRadius 之内的粒子号，升序，最多 NeighborBudget 个。
10) BuildSurface 以 NeighborRadius 为格宽建体素，粒子数达到 DensityThreshold 的格子生成一个顶点，顶点按格坐标先 X 后 Y 后 Z 升序排列，Indices 为与顶点等长的 0、1、2 顺排索引；顶点数超过 MaxVertices 返回 ErrVertexLimit；SetDensityThreshold 小于一返回 ErrBadThreshold。
11) 密度阈值一旦改变，已有网格必须整体失效重建，不许只把新增粒子补进去。
12) 快照带粒子表、边界盒、Tick、待推进队列与密度阈值；Restore 要么整体生效要么不改现有状态，损坏载荷返回 ErrBadSnapshot。

硬约束：AddParticle、Step、Neighbors 会被多个模拟线程并发调用，读到的粒子表只能是完整的一帧之前或一帧之后；每帧推进的粒子数与邻域查询条数都是硬预算，超预算一律顺延到下一帧，不许降精度硬塞；顶点数受 MaxVertices 硬顶。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-07`
- 实现文件：`fluid_surface_scheduler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`FluidSimScheduler`
