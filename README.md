# 殖民地居民工作派发：技能匹配、工位预留超时回收与班次边界替换

## 题面

殖民地更新后，值班主管发现居民整段时间在工位之间来回跑：同一个高技能居民刚被派去净水站，下一 tick 又被派回农场，产出一点没涨；一名居民在前往工位途中被塌方砸死，预留的工位一直挂着，别人再也接不了活；更麻烦的是读档之后，已经收获过的作物被重新结算了一遍，仓库凭空多出两批物资。请在 gsb/batch51-go-game-a-15 的 colony_job_dispatcher.go 里把 ColonyJobDispatcher 补齐，调用方按下面的签名使用：NewColonyJobDispatcher(cfg ColonyConfig) *ColonyJobDispatcher、AddResident(r Resident) error、AddJob(j Job) error、Assign(now time.Time) []Assignment、Tick(now time.Time) []ColonyEvent、Preempt(residentID, jobID string, reason string) error、Complete(jobID, residentID string, units int) error、Snapshot() ([]byte, error)、Restore([]byte) error；另外用到 Produced、SetCondition、SetShift、AssignmentOf、Resident、Job。

需要落实的行为：

1) AddResident 的居民号空白返回 ErrBadResident，重复登记返回 ErrDuplicateResident 且保留原记录；AddJob 的工位号空白返回 ErrBadJob，重复登记返回 ErrDuplicateJob 且保留原记录。
2) 派发打分固定为工位优先级乘 100 加技能匹配乘 10 减距离，技能匹配取居民技能与工位最低技能中的较小值；居民技能低于工位最低技能的不得派发。
3) 健康低于 HealthFloor、饥饿不低于 HungerLimit 或疲劳不低于 FatigueLimit 的居民不参与派发；派发按分数从高到低进行，同分时居民号小的优先。
4) 每个工位同时最多占用 Slots 个居民，每个居民同时只能有一个有效派发，派发成功返回状态为 active 的 Assignment，序号从 1 起递增。
5) 每次 Assign 最多新建 MaxJobsPerTick 个派发，超出的居民留到下一个 tick，不得在同一 tick 内反复抢占同一居民。
6) Tick 发现已派发居民的饥饿或疲劳越过阈值时强制离岗并释放工位，事件类型为 preempted，原因分别为 hunger 与 fatigue。
7) 派发即预留工位；居民在前往工位途中死亡或长时间未到达时，预留由 Tick 的超时回收器释放，事件类型为 reclaimed、原因为 reservation_timeout，不得依赖到达事件。
8) Preempt 只接受当前有效的派发，不存在时返回 ErrNotAssigned，原因为空白返回 ErrBadReason；成功释放的工位在同一 tick 之后可以重新派发。
9) 高优先级工位被低技能居民临时占用时，高技能居民只能在班次边界替换，不能立即踢人；SetShift 记录下一个班次边界，替换事件类型为 shift_replaced。
10) Complete 为工位累计产出并释放居民与工位，单位数必须为正，重复完成同一派发返回 ErrNotAssigned；Produced 返回该工位累计产出，已完成产出不得重算。
11) 读档后所有在途派发转为待重新派发，已完成产出不得重算。
12) 快照带居民、工位、在途派发与累计产出；Restore 要么整体替换要么完全不动，损坏载荷返回 ErrBadSnapshot。

硬约束：Assign、Tick、Preempt 与快照会被多个协程并发调用，同一工位的槽位与同一居民的有效派发不得超卖；快照与写入互斥，读到的只能是完整旧状态或完整新状态；每 tick 派发次数、每工位槽位与累计产出都受配置与登记表硬顶。

验收要求：go test ./... 15 条用例必须全部通过，测试文件不得修改、不得跳过、不得按固定输入写死；并发派发与读档恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-15`
- 实现文件：`colony_job_dispatcher.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ColonyJobDispatcher`
