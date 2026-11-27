# 关卡掉落的独立掉落、保底计数与跨场景继承

## 题面

新副本上线第一周，掉落就被玩家抓出三件事。同一支队伍里两个人同时开箱，第二个人拿到的还是第一箱的同款，论坛上说是掉落池被抽干了；一个连开四十次没出橙的玩家，保底计数在中途莫名其妙归了零；换了一张地图之后保底又从零开始数，欧皇觉得自己被针对。运营想复现这些争议，结果每次跑出来的掉落都不一样，自己对账都对不上。

落点在 `gsb/batch51-go-game-r-06` 的 `loot_table_guard.go`，类型 `LootTableGuard`，包名 `task`，模块路径 `gsb/batch51-go-game-r-06`。

公开接口保持：`NewLootTableGuard(limits Limits) (*LootTableGuard, error)`、`RegisterScene(sceneID, group string) error`、`SceneGroupOf(sceneID string) (string, bool)`、`RegisterTable(table LootTable) error`、`TableOf(tableID string) (LootTable, bool)`、`Roll(req RollRequest) (RollResult, error)`、`PityOf(playerID string) (PityState, bool)`、`ResetPity(playerID string) error`、`SwitchScene(playerID, sceneID string) error`、`CurrentScene(playerID string) (string, bool)`、`Audit() []AuditEntry`、`Stats() GuardStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. 每批掉落数上限、单表条目上限、审计条数上限都必须为正，保底品质只能是 common、rare、epic 之一，保底阈值必须为正，否则报 `ErrBadConfig`。
2. 场景编号或场景组为空报 `ErrBadScene`；同一场景重复登记到同一组幂等成功，改登记到别的组报 `ErrSceneExists`；未登记的场景查询不到组。
3. 掉落表编号、场景组为空，条目为空或超上限，条目品质未知、权重为负、物品编号重复，权重合计为零，保底品质没有任何可用权重，统统报 `ErrBadTable`；表编号重复报 `ErrTableExists`。
4. 掉落请求先判表是否存在（`ErrUnknownTable`），再判玩家号非空（`ErrBadRequest`）、批量数在预算内（`ErrBadCount`），然后判玩家是否已有场景（`ErrBadScene`）以及表的场景组与玩家所在组是否一致（`ErrSceneMismatch`）。
5. 每次掉落相互独立：同一张表可以被反复使用，不消耗库存，也不影响别的玩家；同一玩家同一张表连续开出的结果只由随机源决定。
6. 每次掉落只向注入的随机源要一个 `[0, 权重合计)` 的整数，按条目在表里的顺序累加权重决定命中项；随机源为空时使用默认实现。
7. 命中保底品质的自然掉落把保底计数清零，其余掉落把计数加一；计数达到阈值后，下一次掉落必出保底品质并清零，结果里要标出这次是保底。
8. 一批多个目标同时掉落时，保底只消耗一次，不能一批里连发多件保底物品。
9. 保底计数按场景组分别保存，同一个组的场景之间来回切换要接着原计数走；不同组各自计数，互不混算，切场景后当前场景要能查回来；`ResetPity` 只清当前组的计数，未知玩家报 `ErrUnknownPlayer`。
10. 审计按动作追加、序号严格递增，只保留最近若干条；`Stats` 给出表数、场景数、掉落批数、掉落件数、保底件数与切场景次数。
11. 快照要带掉落表、场景映射、每个玩家在每个组的保底计数、当前场景、序号与审计；恢复后计数接着走、保底照常触发、表还能用；损坏快照报 `ErrBadSnapshot` 且不动现网。
12. 多个玩家并发掉落时计数各自准确，同一玩家的并发掉落不得丢计数或读到半更新状态，任何时刻读到的表与玩家状态都是完整的。

验收：`go test ./...` 15 条全部通过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-r-06`
- 实现文件：`loot_table_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`LootTableGuard`
