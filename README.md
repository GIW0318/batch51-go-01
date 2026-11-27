# 游戏货币的总量审计、乱序补录与透支标记

## 题面

季度结算前夜，运营发现全服金币总量对不上账：流水里明明回收了三百万，拍卖行的余额合计只少了两百万，中间那一百万找不到出处；同一笔补偿因为网络重试被投递了两次，第二次把金额覆盖成了另一份数值；还有几个已经注销的账号，注销之后在审计视图里连历史流水都不见了，期初期末直接对不上。值班同学拿审计窗口去查，窗口内的数字和总量永远差一个固定值，谁也说不清是归集时间还是到达时间算错了。

落点在 gsb/batch51-go-game-a-24 的 currency_supply_auditor.go，类型 CurrencySupplyAuditor，包名 task，模块路径 gsb/batch51-go-game-a-24。

公开接口保持：NewCurrencySupplyAuditor(limits AuditLimits) *CurrencySupplyAuditor、RegisterAccount(acct string, currency string) error、CloseAccount(acct string) error、Record(entry LedgerEntry) error、SnapshotTotals(currency string) (Totals, error)、Audit(currency string, from, to int64) (AuditReport, error)、SetTolerance(t Tolerances) error、FlagAccount(acct string, reason string) error、Reconcile(currency string) (ReconcileResult, error)、Stats() AuditStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 注册账户要求账号与币种都非空，否则报 ErrBadAccount；重复注册同一账号报 ErrDuplicateAccount；账户数超过 MaxAccounts 报 ErrAccountLimit。
2) 流水号、账号、币种必须非空，金额不能为零，业务时间必须为正，否则报 ErrBadEntry；账号未注册报 ErrUnknownAccount，流水币种与账户登记不一致报 ErrCurrencyMismatch。
3) Record 按流水号幂等：同一流水号同一金额重复到达只计一次，计入重复数并返回成功，总量与条数都不变。
4) 同一流水号金额不同按冲突处理，保留首条并计入冲突数，不能覆盖原流水，也不能改动任何总量。
5) 注销账户后不再接受新流水，报 ErrAccountClosed；已注销账户的历史流水仍要计入该币种的总量与审计视图。
6) 流水按业务时间归集，与到达顺序无关；补录进来的旧流水归入业务时间所在区间，而不是到达时间。
7) SnapshotTotals 返回该币种的发行额、回收额、净额、流水条数与账户数。
8) Audit 的窗口为左闭右开：期初等于窗口之前全部净额，发行与回收只统计窗口内，期末等于期初加发行减回收。
9) Audit 的窗口起止倒置报 ErrBadWindow，未知币种报 ErrUnknownCurrency，窗口内条数超过 MaxWindowEntries 报 ErrWindowLimit。
10) 窗口内某账户的余额被扣成负数时，逐条列出账户、流水号与金额，原因记为 overdraft；把这些负余额的绝对值合计记为 Delta，超过容忍度时 WithinTolerance 为假。
11) FlagAccount 必须带原因，空原因报 ErrBadReason，未知账号报 ErrUnknownAccount；被标记账号要出现在 Reconcile 的 Flagged 里，Reconcile 还要给出账户余额合计、流水净额合计、差额与流水条数。
12) 快照保存账户、余额、流水与序号水位，恢复后幂等与冲突判定照旧；载荷损坏报 ErrBadSnapshot 且不改变现有状态。

硬约束：记账与审计会被多个结算协程并发调用，审计只能看到某个一致时点上的完整流水集合，不能读到记了一半的条目；账户数、单币种流水缓存与审计窗口条数都是硬顶，超限拒绝而不是静默淘汰；恢复期间不接受新流水，快照要么整体生效要么完全不生效。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-24`
- 实现文件：`currency_supply_auditor.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CurrencySupplyAuditor`
