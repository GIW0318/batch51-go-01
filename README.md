# 怪物图鉴的多来源发现、区域日增量上限与版本追溯补算

## 题面

图鉴改版后运营连着收到三类反馈：同一只精英怪先在野外击杀、又在活动里观察、最后朋友分享了一次，图鉴里只记了击杀一条，来源栏空了一半；森林区域第一天就把进度条点满，第二天上线的玩家反而一只都不涨；旧版本存档里打死过的怪，升到新版本后图鉴没有补亮，客服只能手工发奖。请在 gsb/batch51-go-game-a-12 的 bestiary_codex_ledger.go 里把 CodexLedger 补齐，调用方按下面的签名使用：NewCodexLedger(cfg CodexConfig) *CodexLedger、Discover(accountID, entryID string, source Source, at time.Time) (Discovery, error)、Progress(accountID, regionID string) (Progress, error)、Backfill(accountID string, records []KillRecord) (BackfillResult, error)、SetVersion(v int) error、Snapshot() ([]byte, error)、Restore([]byte) error；读取侧还用到 Version、Entry、Pending、Rollover、Hidden。

需要落实的行为：

1) Discover 的账号为空或全空白返回 ErrBlankAccount；条目不在当前版本的表里返回 ErrUnknownEntry；来源种类只接受 kill、observe、share，其余返回 ErrBadSource。
2) 同一条目被同一账号反复发现只算一次首次，First 仅在第一次为真；Discovery 的 Seq 从 1 起按成功发现顺序递增；来源集合按 kill、observe、share 升序保留全部来源，重复来源不重复追加。
3) 首次发现才计入区域完成度；通过他人分享获得的条目只点亮图鉴，不计入本人区域完成度，除非本人后来自己完成过一次击杀或观察。
4) 区域完成度只统计 EntryVersion 不高于当前版本的条目，Progress 的 Total 取该区域当前可见条目数，Percent 为向下取整的百分比，达到 Total 时 Complete 为真；未知区域返回零值且不报错。
5) 每个账号每个区域每个 UTC 自然日最多计入 DailyRegionGain 条新发现，超出的按原始发现顺序挂账，Discovery 的 Deferred 为挂账后的待处理条数，Progress 的 Backlog 同步反映。
6) Rollover 以传入日期为结算日，按该日上限把挂账补入完成度并返回本次补入条数，跨日补算保持原始发现顺序，首杀标记不因挂账改变。
7) Backfill 按记录时间顺序补算历史击杀，只补当前版本可见且本人未发现的条目，Added 为新计入条目，Skipped 为已拥有或未知条目，Rewarded 为本次实际发奖次数，同一批记录重复提交不得再次发奖。
8) 历史击杀落在当前版本未知的条目上时只记入挂账，版本升级后按原时间顺序自动补算，不得重复发奖。
9) SetVersion 只接受正整数，否则 ErrBadVersion 且版本保持不变；升级后新条目参与 Total 统计与挂账补算。
10) 版本回退时，高于当前版本的条目转入隐藏区而不删除，Hidden 按条目号升序返回；恢复版本后原进度必须还原。
11) 快照带账号记录、来源集合、挂账、版本与隐藏区；Restore 要么整体替换要么完全不动，损坏载荷返回 ErrBadSnapshot。

硬约束：Discover、Backfill、Rollover 与快照会被多个协程并发调用，同一账号同一条目的首次判定只能成立一次，来源集合不得重复；快照与写入互斥，读到的只能是完整旧状态或完整新状态；每账号每日计入条数与挂账长度受 DailyRegionGain 与区域条目数硬顶。

验收要求：go test ./... 15 条用例必须全部通过，测试文件不得修改、不得跳过、不得按固定输入写死；并发发现与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-12`
- 实现文件：`bestiary_codex_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CodexLedger`
