# 怪物仇恨表、嘲讽目标与仇恨转移结算

## 题面

公会副本的怪物最近像失了智。主坦开场嘲讽拉住首领，嘲讽一结束首领转身就去打治疗，可主坦明明还站着、仇恨也最高；另一次主坦在嘲讽期间被秒，嘲讽结束后首领直接不动了，站在场地中央发呆，直到第二个坦克补上嘲讽才恢复。还有更细的账：团长让副坦把仇恨转一部分给主坦，转完之后两个人的仇恨加起来比转之前多了一截，战斗记录里凭空多出来的那部分被玩家算成了缺陷。

落点在 `gsb/batch51-go-game-r-10` 的 `threat_table_guard.go`，类型 `ThreatTableGuard`，包名 `task`，模块路径 `gsb/batch51-go-game-r-10`。

公开接口保持：`NewThreatTableGuard(cfg ThreatConfig) (*ThreatTableGuard, error)`、`AddThreat(monsterID, unitID string, amount int64, atTick int64) error`、`Table(monsterID string) []ThreatEntry`、`CurrentTarget(monsterID string, atTick int64) (string, bool)`、`Taunt(monsterID, unitID string, atTick, untilTick int64) error`、`ClearTaunt(monsterID string) error`、`TauntRemaining(monsterID string, atTick int64) int64`、`MarkDead(unitID string) error`、`Revive(unitID string) error`、`TransferThreat(monsterID, from, to string, permille int) error`、`ResetTable(monsterID string) error`、`HighestThreat(monsterID string) (ThreatEntry, bool)`、`ClearAll() int`、`Audit() []AuditEntry`、`Stats() GuardStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `MaxMonsters`、`MaxUnitsPerTable`、`MaxAudit` 必须为正，否则报 `ErrBadConfig`；新实例没有任何怪物的仇恨表，各项计数都是零。
2. 仇恨来源要校验怪物编号、单位编号非空、仇恨值为正、发生 tick 非负，否则报 `ErrBadThreat`；已阵亡单位不得再累加仇恨，报 `ErrUnitDead`。
3. 同一怪物同一单位的多次仇恨累加，表里只留一条，数值与最近发生 tick 一起更新；单表单位数超过 `MaxUnitsPerTable` 报 `ErrUnitLimit`。
4. `Table` 按仇恨值降序返回，带上仇恨值与最近 tick；`HighestThreat` 跳过阵亡单位，返回当前最高的一条。
5. `CurrentTarget` 在没有嘲讽时取仇恨最高的存活单位；表为空返回不存在，不得返回空字符串的成功结果。
6. 嘲讽要校验单位已在仇恨表上（`ErrUnitNotOnTable`）、结束 tick 大于发生 tick（`ErrBadTaunt`）；嘲讽期间强制返回该单位，`TauntRemaining` 给出剩余 tick。
7. `ClearTaunt` 立即解除强制目标；重复解除幂等成功，不存在的怪物报 `ErrUnknownMonster`。
8. `MarkDead` 让单位退出目标竞争但保留表内记录并打上阵亡标记；`Revive` 让它重新参与，重复标记幂等。
9. 仇恨转移只接受零到一千之间的千分比，来源与目标必须是表上两个不同单位，否则报 `ErrBadTransfer` 或 `ErrUnitNotOnTable`。
10. `ResetTable` 清空单个怪物的仇恨与嘲讽，未知怪物报 `ErrUnknownMonster`；`ClearAll` 清空全部并返回清掉的怪物数；审计按动作追加、序号严格递增，只保留最近 `MaxAudit` 条；`Stats` 给出怪物数、累加次数、嘲讽次数、转移次数、阵亡单位数与重置次数。
11. 快照要带全部仇恨表、嘲讽窗口、阵亡单位、序号与审计；恢复后目标裁决照常，损坏快照报 `ErrBadSnapshot` 且不动现网。
12. 嘲讽结束时若原目标已阵亡，按仇恨表下一个目标接替，不能回落到空；仇恨值相同按最近造成仇恨者优先，不能按单位编号排序；仇恨转移只转移指定比例，转移后总量不得凭空增加。

硬性约束：`AddThreat`、`MarkDead` 与 `CurrentTarget` 会被多个战斗协程并发调用，同一怪物同一单位的仇恨不得丢更新，目标裁决只能读到完整的一张表；快照与写入互斥，任何时刻读到的都是完整状态；单表单位数与审计条数都受上限硬顶。

验收：`go test ./...` 15 条全部通过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-r-10`
- 实现文件：`threat_table_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ThreatTableGuard`
