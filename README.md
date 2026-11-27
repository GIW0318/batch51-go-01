# 毛皮壳层规划、密度采样与每帧壳层预算

## 题面

美术验收报上来一堆毛皮问题：狼人远景几乎看不到毛，镜头推到两米以内又像套了一层硬壳；把毛长从三厘米改到六厘米重启后壳层数一点没变。分屏之后更乱，同一个 groom 被两个视口以不同距离请求，壳层数互相覆盖，一个视口把另一个的密度图也改掉了。战斗场景帧率掉到二十帧以下时，远处角色的毛糊成半透明残影，密度采样计数一路往上冲，跑十分钟必然卡死。

落点在 gsb/batch51-go-game-b-11 的 fur_shell_pipeline.go，类型 FurShellPipeline，包名 task，模块路径 gsb/batch51-go-game-b-11。

公开接口保持：NewFurShellPipeline() *FurShellPipeline、Configure(cfg FurConfig) error、BeginFrame(frame int64) error、SetGroomer(id string, groom GroomData) error、SetDensity(id string, tex DensityMap) error、PlanLayers(view ViewParams) (LayerPlan, error)、BuildShells(plan LayerPlan) ([]ShellInstance, error)、ResolveAlpha(shell int, uv Vec2) (float32, error)、Generation(id string) (int64, error)、DropGroomer(id string) error、Stats() FurStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) Configure 校验：MaxShells 至少为二，ShellBudget、SampleBudget、MinStep、FurLengthMax 都为正，LODDistances 与 LODShells 长度相差一且距离严格递增，每个壳层数落在一到 MaxShells 之间并随距离不减，HysteresisBand 不小于零且小于第一档距离与相邻档位间距中的最小值；任一条不满足返回 ErrBadConfig，且不得改动已有配置。首次成功配置前，除 Configure 外的调用一律返回 ErrNotConfigured。
2) SetGroomer：空 id、id 与 groom.ID 不一致、毛长不在零到 FurLengthMax 之间（含零）、BaseAlpha 不在零到一之间（含零）都返回 ErrBadGroom。新 groom 代际从一开始，同一 id 再次设置时代际加一。
3) PlanLayers 按相机距离选档：取第一个满足 CameraDist 不超过 LODDistances[i] 的档位 i，超出最后一档就按最后一档；FullShells 取该档壳层数，Step 等于毛长除以 FullShells。空视口标识或相机距离不为正返回 ErrBadView，未注册的 groom 返回 ErrUnknownGroom。
4) 相机在档位边界附近来回移动时，同一视口要按 HysteresisBand 保持住上一次的档位，不能每帧重排壳层。
5) 计划按视口隔离：同一 groom 被不同视口以不同距离请求时各拿各的档位与壳层数，而密度图是所有视口共用的只读数据。
6) BuildShells 依计划生成 Shells 个实例，序号从零起升序，偏移等于序号乘 Step，透明度等于 BaseAlpha 乘 FullShells 减序号除以 FullShells。计划代际与 groom 当前代际不一致返回 ErrStalePlan，Shells 为负或大于 MaxShells 返回 ErrBadPlan，未注册的 groom 返回 ErrUnknownGroom。
7) BeginFrame 开启新帧，帧号必须严格递增否则返回 ErrBadFrame，并把本帧的计划数与壳层数清零、Grooms 与各代际保留。每帧壳层数受 ShellBudget 硬顶，超出部分按相机距离由远及近裁掉，被裁掉的壳层透明度必须归零，不能留下半透明残影；计划里 Shells 加 Trimmed 恒等于 FullShells。
8) ResolveAlpha 用当前帧已建壳层的全局序号取透明度，再乘密度图在 uv 处的双线性采样值，uv 与边缘都钳到零到一；未设密度图时采样值按一算，序号越界返回 ErrNoShell。每帧密度采样次数受 SampleBudget 约束。
9) SetDensity 校验宽高为正、数值个数等于宽乘高、每个值落在零到一，否则返回 ErrBadDensity；未注册的 groom 返回 ErrUnknownGroom。
10) DropGroomer 把 groom 连同密度图与计划一起移除，未知 id 返回 ErrUnknownGroom。
11) 快照带配置、各 groom 的代际与毛长、密度图、各视口的档位记忆与帧计数；Restore 要么整体生效要么不动现有状态，载荷损坏或为空返回 ErrBadSnapshot，恢复后已有视口不得重新规划。

硬约束：SetGroomer、PlanLayers、BuildShells、Snapshot 会被渲染线程并发调用，groom 表与密度表整表原子替换，读者只能看到完整旧代或完整新代；每帧实例数与采样次数受硬顶，不得超发；快照与规划互斥。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-11`
- 实现文件：`fur_shell_pipeline.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`FurShellPipeline`
