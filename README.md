# 竞速赛事罚时裁决：赛道边界、碰撞责任分摊、申诉改判与奖励差额补退

## 题面

赛会仲裁组的邮箱在分站赛后被投诉塞满：一台车在弯心四轮出白线抄了近道，赛会只给了一条赛道边界警告，位置优势没有让回也没有罚时；两台车在刹车区接触后各自上传了同一段录像，系统却开出两张独立罚单，两边车队的积分都被扣；还有一支车队在奖励已经打进账户后才申诉成功，赛会直接把奖金整笔回收，赞助商合同因此触发违约条款。请在 gsb/batch51-go-game-a-14 的 race_penalty_steward.go 里把 RaceSteward 补齐，调用方按下面的签名使用：NewRaceSteward(rules RaceRules) *RaceSteward、ObserveLap(carID string, samples []PoseSample) []Incident、Judge(incidentID string) (Penalty, error)、Apply(raceID, carID string, p Penalty) error、Appeal(incidentID, carID string, evidence Evidence) (Verdict, error)、Standings(raceID string) ([]Standing, error)、ReportContact(c Contact) (Incident, error)、Snapshot() ([]byte, error)、Restore([]byte) error；另外用到 Gaps、Incident、Incidents、RecordResult、GiveBack、Settle、Rewards。

需要落实的行为：

1) ObserveLap 采样数少于 MinSamples 时不判罚并记一条 short_lap 缺口；采样序号或时间戳不再递增时同样不判罚，记 out_of_order 缺口；缺口按上传顺序保留。
2) 位置偏移绝对值严格大于 HalfWidth 才算越界，连续越界样本数达到 CutTolerance 才生成一条 track_limits 事件，恰好压在白线上的样本合法。
3) 进度连续两段以上下降生成一条 reverse 事件；track_limits 与 reverse 的罚时秒数分别取 TrackPenaltySeconds 与 ReversePenaltySeconds，事件初始状态为 open。
4) Judge 对未知事件返回 ErrUnknownIncident；同一事件重复裁决返回同一份罚时且幂等，状态转为 judged。
5) Apply 把罚时叠加到该车该赛事的成绩上，同一事件第二次 Apply 返回 ErrAlreadyApplied 且成绩不变；罚时车号与事件车号不一致返回 ErrCarMismatch。
6) ReportContact 的让位空间 YieldSpace 取值 0 到 1，上报方责任百分比等于 100 减去 YieldSpace 乘 100 的四舍五入值；事件罚时为 ContactPenaltySeconds 乘 Severity。
7) 同一次碰撞被双方各自上报时必须合并为同一事件并按责任比例分摊，不得各罚一次。
8) 申诉只能针对已裁决事件，否则返回 ErrNotJudged；提交时刻晚于事件时刻加 AppealWindow 返回 ErrAppealWindowClosed。
9) 申诉证据样本不少于 MinSamples 且全部偏移绝对值不超过 HalfWidth 时改判为无罚时，否则维持原判；改判后 Verdict 给出旧值、新值与差值，已 Apply 的成绩按新罚时重排。
10) 切弯获得的位置优势在下一弯让回后罚时降为警告而不是取消，GiveBack 记录让回动作。
11) 奖励已发放时申诉改判只补退差额，不回收已解锁的赛事成就；Settle 按名次发放积分 25、18、15 与奖金 100000、60000、40000，重复调用幂等。
12) 快照带事件、罚时、成绩与结算状态；Restore 要么整体替换要么完全不动，损坏载荷返回 ErrBadSnapshot。

硬约束：ObserveLap、Apply、Appeal 与快照会被多个协程并发调用，同一事件的裁决与计罚只允许生效一次；快照与写入互斥，读到的只能是完整旧状态或完整新状态；每个赛事的成绩条目受参赛车数约束，反复申诉不得让罚时累积。

验收要求：go test ./... 15 条用例必须全部通过，测试文件不得修改、不得跳过、不得按固定输入写死；并发裁决与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-14`
- 实现文件：`race_penalty_steward.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RaceSteward`
