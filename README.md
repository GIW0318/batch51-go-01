# 战斗回放伤害飘字的合批与帧同步

## 题面

战斗回放系统的伤害飘字最近被玩家和策划同时投诉。同一段战斗录像在 30 帧和 60 帧的机器上播放，飘字停留时长对不上：高帧率机器上的数字一闪就没，低帧率机器上又挂着不走，切到两倍速回放更明显，整屏数字像被冻住。另一个问题是暴击：一次群体技能打在同一只怪身上三段伤害，只有一段暴击，屏幕上却只显示一个合并后的总数，暴击的光效和普通数字一起消失，玩家截了对比图说我们吞了暴击。还有更细的：一次技能在帧边界落地，回放里数字跑到了下一帧的判定之后才出现，解说视频里能明显看出伤害数字比血条晚了一格。

落点在 `gsb/batch51-go-game-r-07` 的 `damage_number_batch_guard.go`，类型 `DamageNumberBatchGuard`，包名 `task`，模块路径 `gsb/batch51-go-game-r-07`。

公开接口保持：`NewDamageNumberBatchGuard(cfg BatchConfig) (*DamageNumberBatchGuard, error)`、`Submit(ev DamageEvent) error`、`BatchFrame(frame int64) ([]FloatingText, error)`、`PendingFrames() []int64`、`SetPlaybackSpeed(permille int) error`、`PlaybackSpeed() int`、`AdvanceFrames(frames int64) (int, error)`、`Expire() int`、`Live() []FloatingText`、`StyleFor(ev DamageEvent) (string, error)`、`ExportReplay() []ReplayFrame`、`Stats() GuardStats`、`Audit() []AuditEntry`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`、`Reset() error`。

要修的地方：

1. `FrameRate`、`LifeFrames`、`MaxPending`、`MaxTexts`、`MaxAudit` 必须为正，否则报 `ErrBadConfig`；新实例的播放倍速默认 1000，表示一倍速。
2. 事件必须带正的 `Seq`、非负的 `Frame`、非空的 `TargetID` 与正的 `Amount`，元素只能是 physical、fire、ice、poison，其余报 `ErrBadEvent` 或 `ErrUnknownElement`。
3. 同一帧内同一目标的多次受击合并成一条飘字，数值相加，命中次数累加；不同目标按目标编号升序输出。
4. 合批只能按帧号递增进行，重复合批同一帧报 `ErrFrameClosed`，已经合批过的帧再收事件也报 `ErrFrameClosed`，倒着合批报 `ErrFrameOrder`。
5. 飘字数量超过 `MaxTexts` 时报 `ErrTextLimit` 并保持该帧未合批；待处理事件超过 `MaxPending` 时报 `ErrPendingLimit`，已收下的事件不能丢。
6. 每条飘字的过期帧为发生帧加 `LifeFrames`，过期只看游戏帧，`AdvanceFrames` 与 `Expire` 返回真正清掉的条数，推进零帧报 `ErrBadAdvance`。
7. 倍速只接受 1 到 8000 之间的千分比，越界报 `ErrBadSpeed`，查询倍速要如实返回，切换倍速不得改动已经合批的飘字。
8. `StyleFor` 按元素与暴击拼出样式名，普通为 `normal_<元素>`，暴击为 `crit_<元素>`，未知元素报 `ErrUnknownElement`。
9. `ExportReplay` 按帧号升序导出已合批的帧与飘字，未合批的事件不得混进回放；`PendingFrames` 按帧号升序给出待合批的帧。
10. 审计按动作追加、序号严格递增，只保留最近 `MaxAudit` 条；`Stats` 给出提交数、合批条数、合并次数与过期条数；`Reset` 清空回放但保留配置，并留下一条 reset 记录。
11. 快照要带当前帧、倍速、已合批帧、存活飘字、待处理事件、统计与审计；恢复后继续合批、继续过期，损坏快照报 `ErrBadSnapshot` 且不动现网。
12. 同一目标同一帧多次受击时合并数值但保留暴击次数，不能只显示总和；回放倍速变化时飘字生命周期按游戏帧计算，不按真实时间；跨帧边界的事件按发生帧归属，不能因合批漂到下一帧。

硬性约束：`Submit` 与 `BatchFrame` 会被多路网络协程并发调用，同一帧只能合批一次，并发提交的事件一条都不能丢；快照与合批互斥，任何时刻读到的都是完整状态；待处理事件与存活飘字都受上限硬顶。

验收：`go test ./...` 15 条全部通过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-r-07`
- 实现文件：`damage_number_batch_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DamageNumberBatchGuard`
