# 图鉴的条目发现、套装完成与版本迁移

## 题面

新版本上线当天，图鉴页面炸了：有玩家明明集齐了青铜套装，奖励却一直灰着点不动；有人把重复的铜币转成碎片之后，原本已经点亮的套装又变回差一件；客服还发现隐藏条目在没揭晓之前就被算进了进度，揭晓之后奖励已经提前发过一次。更糟的是版本迁移把老条目编号全部重映射，迁移后玩家的旧收集记录一半消失，另一半又把新版本的套装奖励领了一遍。

落点在 gsb/batch51-go-game-a-25 的 collection_codex_ledger.go，类型 CollectionCodexLedger，包名 task，模块路径 gsb/batch51-go-game-a-25。

公开接口保持：NewCollectionCodexLedger(cfg CodexConfig) *CollectionCodexLedger、RegisterEntry(e CatalogEntry) error、RegisterSet(s EntrySet) error、RegisterVersion(v VersionSpec) error、Discover(acct string, ev DiscoverEvent) (DiscoverResult, error)、Reveal(acct string, entryID string) error、UnlockVariant(acct string, entryID string, variant string) error、ClaimSet(acct string, setID string) (SetReward, error)、ConvertDuplicate(acct string, entryID string, qty int) (ConvertResult, error)、Migrate(acct string, fromVer, toVer string) error、Explain(acct string, entryID string) []Reason、Stats() CodexStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 条目编号与名称都不能为空，否则报 ErrBadEntry，重复注册报 ErrDuplicateEntry；套装至少包含一个条目，条目必须已注册，否则报 ErrBadSet、ErrUnknownEntry，重复套装报 ErrDuplicateSet。
2) Discover 的事件编号与账号不能为空，条目必须已注册，数量必须为正，分别报 ErrBadEvent、ErrBadAccount、ErrUnknownEntry、ErrBadQty。
3) 发现事件按事件编号幂等：同号重复上报返回第一次的结果并标记 Replayed，不增加计数，也不重复触发套装完成。
4) 同一账号同一件再次发现时计数加一并在结果里标记 Duplicate；事件带变体且该变体首次出现时解锁，结果 VariantUnlocked 为真，重复解锁不再计数。
5) 隐藏条目在未揭示前不计入套装进度；揭示后补算套装完成，但已经发放过的套装奖励不得重复发放。
6) 套装集齐时由 Discover 的结果列出新完成的套装编号；ClaimSet 一次性原子发放奖励，未集齐报 ErrSetIncomplete 并在 Missing 里列出缺少的条目，未知套装报 ErrUnknownSet。
7) 已领取的套装再次领取报 ErrAlreadyClaimed，奖励不重复发放。
8) 重复转化要求该条目至少有两份，数量必须为正且不超过多余份数，否则报 ErrNoDuplicate、ErrBadQty；转化消耗条目后，已经完成的套装不能被打回未完成。
9) 转化按每天的次数上限计数，超过上限报 ErrDailyLimit；每次产出的碎片数等于消耗数乘 ConvertRate。
10) Migrate 只接受已登记的版本，未登记报 ErrUnknownVersion，目标版本序号不大于来源版本时报 ErrBadVersion；迁移保留旧版完成记录，被移除的条目保留 legacy 标记，但不能据此领取新版本的套装奖励。
11) Explain 按条目给出原因列表，至少包含 discovered 与 variant 两类；Stats 给出条目数、套装数、账号数、发现数、变体数、完成与已领套装数、转化数。
12) 快照包含条目表、套装表、版本表与各账号的图鉴状态，恢复后计数、完成与领取状态一致；载荷损坏报 ErrBadSnapshot 且不改变现有状态。

硬约束：发现、领取与转化会被多个客户端回调并发调用，同一账号的套装完成与发奖必须原子，不能出现两个人同时领到同一份奖励；单账号条目数、事件缓存条数与迁移批大小都是硬顶，超限拒绝而不是静默丢弃；快照与写入互斥，恢复期间不接受新事件。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-25`
- 实现文件：`collection_codex_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CollectionCodexLedger`
