# 进程级推测执行缓解的收敛与豁免治理

## 题面

某互联网公司的安全团队把推测执行缓解从装机模板搬到进程级配置之后，线上接连出现几件事：同一台机器上不同进程看到的缓解等级不一样，多线程服务的部分线程仍然跑在无缓解路径上；运维为压测临时关掉一项缓解，压测脚本退出后缓解没有自己回来，第二天安全扫描直接报高危；一台宿主机的 CPU 不在支持列表里，计划里却显示已经缓解，漏洞扫描和监控口径对不上；还有进程 exec 之后策略被洗掉，重新拉起又回到默认等级。请把 gsb/batch51-go-safety-d-02 的 speculation_mitigation_guard.go 里的 SpeculationMitigationGuard 补齐。

对外签名保持：NewSpeculationMitigationGuard(cfg GuardConfig) *SpeculationMitigationGuard、Plan(host HostProfile, policy Policy) (MitigationPlan, error)、Apply(pid int, plan MitigationPlan) error、Read(pid int) (MitigationState, error)、Relax(pid int, feature string, reason string) error、Inherit(parent, child int) error、Exec(pid int) (MitigationState, error)、Exit(pid int) error、Tick(now time.Time) []ExpiryEvent、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）主机型号去掉首尾空白后为空、策略没有列出任何缓解项、或者写了不认识的缓解名，Plan 一律报 ErrBadPolicy，不得返回半成品计划。

（2）计划里的缓解名先去重再按字典序排列，等级等于各缓解权重之和，权重取 retpoline 2、ibrs 2、ibpb 1、ssbd 2、md_clear 3、l1d_flush 1、verw 2；策略带的 MinLevel 高于权重和时以 MinLevel 为准。

（3）计划摘要用策略名、排好序的缓解名与等级拼出来的 sha256 小写十六进制，同一输入两次结果必须一致，策略名不同摘要必须不同。

（4）策略要求而宿主不支持的缓解要显式拒绝，报 ErrUnsatisfiable，并把缺的项放进 Missing，不能只把能开的打开就当成整体已缓解。

（5）pid 小于等于零报 ErrBadProcess，对未登记的 pid 调 Read 报 ErrUnknownProcess；首次 Apply 写入等级、缓解集合、基线等级与代次 1，相同计划重复 Apply 必须幂等，等级与代次都不再变化。

（6）新计划的等级低于当前等级时拒绝，报 ErrDowngradeBlocked，现有等级与代次不动。

（7）Relax 必须带非空原因，否则报 ErrBadReason 且等级不变；缓解名不在当前集合里报 ErrUnknownFeature；对未登记进程报 ErrUnknownProcess。

（8）Relax 成功后等级减去对应权重，缓解集合里去掉这一项，Relaxed 置真并记下缓解名，代次加一，基线等级保持原值。

（9）Inherit 把父进程的等级与缓解集合复制给子进程，子进程首次继承记为一代；子进程等级更高时保持原样，不能被父进程拉低；父进程未登记报 ErrUnknownProcess。

（10）Exec 之后进程策略必须原样保留，只把代次加一；对未登记进程报 ErrUnknownProcess。

（11）Exit 清掉进程记录，重复退出报 ErrUnknownProcess，同一 pid 再被 Apply 时从一代重新开始。

（12）豁免到期后 Tick 要把进程自动回落到基线等级并清掉豁免标记，返回的每条事件带 pid、缓解名、回落后的等级与时刻；豁免期间进程新建的线程也必须按基线等级而不是豁免后的等级执行。

硬约束两条。其一是并发与原子性：同一进程的 Apply、Relax、Inherit 并发进来只能有一个推进，等级只升不降对并发同样成立，任何时刻读到的等级都不能低于已经生效的最高等级。其二是快照与重启：进程表、等级、缓解集合、代次与豁免到期时刻都要进快照，恢复后 Tick 仍能按原到期时刻回收，损坏快照报 ErrBadSnapshot 且不得改动现网状态。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-d-02`
- 实现文件：`speculation_mitigation_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SpeculationMitigationGuard`
