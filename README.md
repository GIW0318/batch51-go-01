# 三路兵线波次调度、超级兵替换与压力补偿

## 题面

三路兵线的节奏最近被玩家反复投诉：高地塔倒下后该路有时整局都不出超级兵；有时一个 tick 内两座塔先后被推掉，波次序号直接跳过两号；基地被压时补偿波和常规波同一帧刷出，一波变两波；暂停回来又补发了一整段波次，经济曲线完全失真。请在 gsb/batch51-go-game-a-03 的 lane_wave_scheduler.go 里把 WaveScheduler 改到位，调用方按下面的签名使用：NewWaveScheduler(cfg LaneConfig) *WaveScheduler、Start(matchID string, at time.Time) error、Tick(now time.Time) []WaveEvent、WaveFor(lane Lane, n int) (Wave, error)、ApplyStructureBreak(lane Lane, tier int, at time.Time) error、ReportPressure(lane Lane, deficit int, at time.Time) error、NextWaveAt(lane Lane) (time.Time, error)、WaveIndex(lane Lane) (int, error)、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) Start 校验入参：空 match 标识返回 ErrBlankMatchID，WaveInterval 非正、近战与远程数量都为 0 或阈值为负返回 ErrInvalidConfig，重复 Start 返回 ErrAlreadyStarted；MatchID 返回已开始的标识。
2) 第一波时刻为 at 加 FirstWaveDelay，之后按 WaveInterval 等距推进；NextWaveAt 给出下一波时刻，WaveIndex 给出该路已生成的最高序号。
3) Tick 生成所有已到期且尚未生成的波次，每路每波一条事件，事件带波次号与计划时刻；一次跳过多波时按到期顺序补足，不能漏发也不能重发。
4) 每波近战与远程数量取配置值；CannonEvery 大于 0 时，序号为其整数倍的波额外带 1 个炮车，不改变近战与远程数量。
5) WaveFor 只计算不生成，序号从 1 起；未知路返回 ErrUnknownLane，序号小于 1 返回 ErrInvalidWaveIndex。
6) ApplyStructureBreak 记录被推倒的塔层级，层级只能是 1 到 3，越界返回 ErrInvalidTier，未开始返回 ErrNotStarted，同一路同一层重复上报返回 ErrStructureAlreadyBroken；每次成功上报追加一条 structure 事件，带路与层级。
7) 高地塔即第 3 层被推倒后，该路从下一波起每波替换出 SuperMinions 个超级兵，已生成的波次与序号保持不变，其它路不受影响。
8) 同一 tick 内两座高地塔被摧毁时，超级兵替换只触发一次，波次序号不得跳号。
9) ReportPressure 在压力差达到 PressureThreshold 时排队一笔补偿；补偿波不得与常规波同帧生成，必须顺延到下一波，并占用该路的补偿配额与总兵力上限，超出返回 ErrCompensationExhausted。
10) 每路补偿次数上限为 MaxCompensation，补偿总兵力上限 MaxCompensationUnits 按路计算；低于阈值的上报返回 ErrPressureBelowThreshold 且不占配额。
11) 对局暂停期间波次时钟冻结，恢复后从冻结时刻继续，不补发暂停期间的波次；重复 Pause 返回 ErrAlreadyPaused，重复 Resume 返回 ErrNotPaused。
12) 快照要包含时钟、每路已生成序号、塔的状态与补偿配额，恢复后 NextWaveAt、WaveFor 与后续 Tick 与中断前一致，损坏载荷返回 ErrBadSnapshot 且不动现有状态。

硬约束：Tick、ApplyStructureBreak 与 ReportPressure 会在同一帧被多个协程调用，同一路的波次生成与序号推进必须串行，事件不得重复或乱序；暂停与恢复要与 Tick 互斥，恢复后的第一帧不能把暂停期间的波次一起吐出；波次台账按路与序号有界增长，长时间对局不随 tick 次数膨胀。

验收要求：go test ./... 15 条用例必须全部通过，测试文件不得修改、不得跳过、不得按固定输入写死；并发生成、暂停恢复与重启续跑自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-03`
- 实现文件：`lane_wave_scheduler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`WaveScheduler`
