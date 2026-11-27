# 账号休眠分级、告警窗口与登录标识符回收

## 题面

某 SaaS 租户的账号治理从季度手工清理改成自动分级后，第一周就出了两起投诉。十几台只跑定时任务的接口账号没有真人登录，被一路降到 suspended，接口连续两晚鉴权失败，运维临时把它们加进豁免名单，下一次扫描又把豁免顶掉了。客服组一个离职账号的登录名在回收后被新员工注册，登录时报出的主体还是已经离职的那个人，审计里完全看不出这层关系。还有一处更隐蔽：租户把闲置阈值从九十天调到三十天后，原本停在 warning 档的账号一夜之间直接进了 reclaimable，标识符没走停用期就被回收。三起投诉指向同一个后台任务，扫描日志里却看不出是哪一级判错了。落点 gsb/batch51-go-safety-a-07 的 dormancy_lifecycle_guard.go，类型 DormancyLifecycleGuard，包名 task。

接口保持：NewDormancyLifecycleGuard(cfg DormancyPolicy) *DormancyLifecycleGuard、Register(account string, at time.Time) error、Touch(account string, at time.Time, source string) error、Sweep(now time.Time) ([]Transition, error)、State(account string) (DormancyState, error)、Exempt(account string, reason string, until time.Time) error、Reactivate(account string, proof Proof) error、ReloadPolicy(cfg DormancyPolicy) error、History(account string) []Transition、Notices(tenant string) []Notice、PendingNotices(tenant string) int、CancelNotice(account string) error、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：一、未登记账号不参与任何迁移，Touch、Exempt、State 一律按 ErrUnknownAccount 处理，重复登记报 ErrAccountExists；二、账号按最后活跃水位在 active、idle、warning、suspended、reclaimable 之间推进，各档阈值从水位起算，进入当前档的时刻记在 Since，登记时与水位一致；三、单次扫描每个账号最多前进一级，同一时刻再次扫描时已推进过的账号不得重复推进，但扫描要从游标处继续覆盖后面的账号；四、进入 warning 发一条可取消通知并把告警次数加一，取消后再取消按 ErrNoticeNotFound，通知预算耗尽时报 ErrNoticeBudget 并停在该账号之前；五、suspended 阶段禁止登录但保留数据，只有 reclaimable 才允许回收登录标识符并把 Reclaimed 置真；六、豁免账号不参与迁移，理由为空报 ErrBadReason，豁免条数超过 MaxExempt 报 ErrExemptionLimit，失败的豁免不得占用额度；七、服务账号与豁免账号都不因一次扫描被连带降级；八、重新激活按停用深度要求不同证明，suspended 需 manager、reclaimable 需 in_person，证明未验证一律 ErrProofRejected，激活后水位回到当前时刻，但历史累计的告警次数不清零，再次休眠接着累加；九、标识符回收后设保留期，保留期内原账号重新注册要报 ErrIdentifierReserved，不能当成新账号建出来；十、租户把阈值调短后，已在 warning 的账号不能跳过 suspended 直接进 reclaimable，扫描仍按级推进；十一、每次扫描受 BatchLimit 约束，游标按账号名升序稳定推进，可断点续跑，迁移与通知发放都幂等，快照要带账号、历史、通知与扫描游标；十二、历史与通知序号递增，跨重启不得回退。

硬约束两条：扫描与迁移在租户内串行推进，同一账号不会被两次并发扫描重复推进，迁移与通知发放成对出现；每租户在途通知数、单次扫描批量与豁免条数设硬顶，越界报错而不是静默截断。快照与恢复要么全量生效要么完全不动，损坏载荷回 ErrBadSnapshot 且现网状态不变。

验收：在模块目录执行 go test ./...，15 条必须全部通过；不得改动测试、跳过用例或按固定输入写死；并发与重启恢复场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-07`
- 实现文件：`dormancy_lifecycle_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DormancyLifecycleGuard`
