# 租户交换配额的页粒度记账与回收节流

## 题面

高峰期一批在线服务被内核 OOM 杀掉，事后翻交换账单却发现对不上：有的租户明明只换出两页，账上记了二十页；有的页换出之后又被换入，账目一路减成了负值；还有租户被删掉重建，新实例一上来就背着上一任的欠账。落点在 gsb/batch51-go-safety-c-13 的 swap_quota_governor.go，类型 SwapQuotaGovernor 按租户与工作负载记录交换页用量，超过硬限直接拒绝，靠近软限按回收优先级腾挪，并给运维一个能对上账的快照。值班同学先怀疑是内核统计口径的问题，比对应用侧日志后才确认，是配额账本的预约、提交与回收三条路径各记各的账，谁也不认谁的数。

签名保持：NewSwapQuotaGovernor(cfg QuotaConfig) *SwapQuotaGovernor；SetQuota(tenant string, q TenantQuota) error；Reserve(tenant, workload string, pages int64) (Reservation, error)；Commit(id string) error；Release(id string) error；Reclaim(tenant string) (ReclaimReport, error)；Usage(tenant string) (UsageSnapshot, error)；RemoveTenant(tenant string) error；Tenants() []string；PendingCount() int；Snapshot() ([]byte, error)；Restore(data []byte) error。

要处理的十二处：① 租户名或工作负载名为空白仍能开盘，应报 ErrInvalidTenant 与 ErrInvalidWorkload；② 页数为零或负数仍被受理，应报 ErrInvalidPages；③ 硬限为零、软限为零或软限大于硬限仍被接受，应报 ErrInvalidQuota；④ 租户数超过 MaxTenants 仍能新建，应报 ErrTenantLimit，删除租户要连账单和预约一起清掉，重复删除报 ErrUnknownTenant，清掉之后旧预约不得再提交；⑤ 待提交预约数超过 MaxPending 仍放行，应报 ErrPendingLimit；⑥ 已提交加待提交超过硬限仍放行，应报 ErrQuotaExceeded，正好等于硬限必须放行；⑦ 预约提交后仍挂在待提交上，提交必须把页从待提交移到已提交并按工作负载累加；⑧ 已释放的预约还能再提交或再释放，应报 ErrReservationState，完全没登记过的预约报 ErrUnknownReservation；⑨ 释放已提交的预约不扣工作负载账单，应等额扣回且账单不出现负数；⑩ 越过软限仍照常受理，Reserve 必须在这一次申请里按 ReclaimBatchPages 回收并把腾出的页数写回预约；⑪ 回收后仍高于软限却报成功，应报 ErrReclaimFailed，每个受影响工作负载的失败计数一次只加一；⑫ 快照恢复丢掉账单、预约与失败计数，必须完整恢复，损坏快照报 ErrBadSnapshot 且不动现网。

两条硬约束：预约、提交、释放与回收必须原子推进，硬限在并发申请下只放行到上限，记账按页单调推进，任何一次 Usage 读取都必须落在某个完整时刻，不能一半新一半旧；快照只能呈现全量旧状态或全量新状态，恢复失败原样保留现网，页数上限、租户数与待提交数都是硬顶，超限一律报错，不允许截断后继续。

另有三处按现场口径处理：同一页在换出与换入之间被重复结算时，账单不得出现负值；租户删除后残留额度要按代际作废，新建同名租户不得继承旧账；回收节流窗口内的突发申请要排队等待，而不是一律拒绝。

验收：go test ./... 的 15 条用例必须全过；测试不得修改、跳过或按固定输入返回常量；并发预约与重启恢复自行写程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-13`
- 实现文件：`swap_quota_governor.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过 `gsb/batch51-go-safety-c-13` 导入本模块
- 主要类型：`SwapQuotaGovernor`，账单类型 `UsageSnapshot`，预约类型 `Reservation`
