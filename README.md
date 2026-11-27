# 装备强化的成功率结算、幸运值保底与降级原子性

## 题面

维护后强化台彻底乱了：有玩家连点两次确认，同一件武器从三段跳到五段，材料只扣了一次；有玩家把武器点到满段后继续点，保护符被扣走却没有任何变化；还有人强化失败掉到零段，界面显示负一段，之后再点直接报错。运营还发现保底计数在换装备之后被清零，两把武器交替点，谁都不保底。

落点在 gsb/batch51-go-game-a-23 的 enhancement_attempt_ledger.go，类型 EnhancementAttemptLedger，包名 task，模块路径 gsb/batch51-go-game-a-23。

公开接口保持：NewEnhancementAttemptLedger(cfg LedgerConfig) *EnhancementAttemptLedger、SetCurve(c EnhancementCurve) error、SetPity(p PityConfig) error、GrantMaterials(player string, n int) error、GrantCharms(player string, n int) error、Materials(player string) int、Charms(player string) int、SetLevel(itemID string, level int) error、Level(itemID string) (int, error)、Preview(player string, itemID string) (Odds, error)、Attempt(player string, itemID string, useCharm bool, idemKey string) (AttemptResult, error)、Repair(itemID string) error、History(itemID string) ([]AttemptRecord, error)、Stats() EnhanceStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 曲线必须带非空编号、至少一段，成功率数组长度等于最大等级且每档落在零到一千之间，否则报 ErrBadCurve；没有装好曲线就发起强化报 ErrBadCurve。
2) 等级只允许零到最大等级，越界报 ErrBadLevel；装备已在最大等级时强化与预览都报 ErrMaxLevel，且不扣材料、不消耗保护符。
3) 玩家、装备编号与幂等键都不能为空，否则报 ErrBadInput；材料不足报 ErrInsufficientMaterials，带保护符却一个都没有报 ErrNoCharm，这两种情况都不扣任何东西。
4) 一次强化内扣材料、掷随机、更新等级、写历史必须原子完成，不能出现材料已扣而等级未变。
5) 成功时等级加一、幸运值清零、不消耗保护符；失败且曲线允许降级时，带保护符不降级但消耗一个，没带保护符降一级并标记为待修理。
6) 降级不低于零级；零级失败时保护符仍按规则消耗。
7) 幸运值按装备各自累计，达到保底阈值后下一次必定成功并清零，保底不能跨装备共享。
8) 待修理的装备再次强化报 ErrDamaged；Repair 清掉该状态，未损坏时报 ErrNotDamaged，未知装备报 ErrUnknownItem。
9) 同一幂等键重复提交返回第一次的结果并标记 Replayed，不重复扣材料、不重复写历史；统计里尝试数与重放数分开计数。
10) History 按时间先后返回该装备的记录，条数受 MaxHistory 限制，超出淘汰最旧。
11) 快照包含曲线、等级、幸运值、材料、保护符、历史与幂等键，恢复后逐字段一致，恢复后继续强化得到与中断前一致的结果；载荷损坏报 ErrBadSnapshot。
12) 材料与保护符的发放只接受非负数量，玩家为空报 ErrBadInput。

硬约束：同一装备的强化请求要串行化结算，四路并发带着不同幂等键打进来时材料扣减与等级变化必须一一对应，不能出现两人同时读到同一等级；同一幂等键在结算途中再次到达，必须等在途结果并返回同一份结果，不得并发扣两次。单件历史条数与幸运值上限都是硬顶，超出按规则截断或封顶而不是无限增长。快照与强化互斥，恢复要么整体生效要么不改现有状态。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-23`
- 实现文件：`enhancement_attempt_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`EnhancementAttemptLedger`
