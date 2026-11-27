# 观战自动导播的镜头评分、切换预算与人工接管冷却

## 题面

秋季赛的观战频道被观众剪了十几段鬼畜：导播在两名选手视角之间每秒横跳，比分条还没看清画面又回去了；关键团战刚起势，镜头突然锁死在暂停的教练视角，人工切走后三分钟不再自动跟拍；还有一局切换次数早早用完，龙只剩一丝血时也没能切过去，解说只能对着旧镜头讲。请在 gsb/batch51-go-game-a-13 的 spectator_camera_director.go 里把 SpectatorCameraDirector 补齐，调用方按下面的签名使用：NewSpectatorCameraDirector(cfg DirectorConfig) *SpectatorCameraDirector、Observe(ev MatchEvent) error、Select(now time.Time) (CameraShot, error)、Override(cameraID string, until time.Time) error、Release(cameraID string) error、Snapshot() ([]byte, error)、Restore([]byte) error；读取侧还用到 Score、Scores、Current、SwitchCount。

需要落实的行为：

1) Observe 只接受配置里存在的视角，未知视角返回 ErrUnknownCamera；事件种类只认 kill、teamfight、objective、ultimate、objective_critical，其余返回 ErrBadEvent；事件时间早于上一条返回 ErrEventOutOfOrder 且不得加分。
2) 动作分按种类累加：kill 40、teamfight 30、objective 20、ultimate 10、objective_critical 50；Score 返回累计值，未知视角返回 ErrUnknownCamera。
3) Select 首次调用时若有视角达到 ScoreThreshold，直接采用分数最高的视角并记为一次切换；没有任何候选达到阈值时采用配置里的第一个视角，Reason 为 reason_idle 且不计切换。
4) 切换后必须停留满 MinDwell 才允许再次切换，停留期内保持当前视角，Reason 为 reason_dwell。
5) 候选视角的分数必须比当前视角高出至少 Hysteresis 才允许切换，否则保持当前视角，Reason 为 reason_hysteresis；两个候选同分时保持当前视角，不得按视角编号抖动。
6) 每局自动切换次数受 SwitchBudget 硬顶，用完后保持当前视角，Reason 为 reason_budget；唯一例外是目标即将被摧毁的事件，允许强制切一次并记为 reason_critical，该次占用下一局预算。
7) Select 返回的 At 等于传入时刻，Timeline 等于 At 减去 Delay，Score 取当前视角的累计分，所有视角与延迟观战时间线对齐。
8) Override 只接受配置里存在的视角，未知视角返回 ErrUnknownCamera，已有接管时再次接管返回 ErrAlreadyOverridden；接管期内 Select 返回接管视角，Reason 为 reason_manual，不消耗切换预算。
9) 接管期间发生的击杀只累加到当前评分，交还后作为当前评分输入，不补切接管期间的历史镜头。
10) Release 只接受当前接管视角，没有接管时返回 ErrNotOverridden；交还后从接管到期时刻起算 OverrideCooldown，冷却期内保持接管视角，Reason 为 reason_cooldown，冷却结束后恢复自动选择，当前视角仍为接管视角。
11) 快照带分数表、当前视角、切换计数、接管状态与冷却截止；Restore 要么整体替换要么完全不动，损坏载荷返回 ErrBadSnapshot。

硬约束：Observe、Select 与 Override 会被多个协程并发调用，同一事件只能计一次分，切换计数与预算不得因并发重复消耗；快照与状态写入互斥，读到的只能是完整旧状态或完整新状态；分数表规模受视角数约束，切换次数受 SwitchBudget 硬顶。

验收要求：go test ./... 15 条用例必须全部通过，测试文件不得修改、不得跳过、不得按固定输入写死；并发导播与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-13`
- 实现文件：`spectator_camera_director.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SpectatorCameraDirector`
