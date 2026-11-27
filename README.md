# 技能冷却与充能的服务端裁决

## 题面

新赛季竞技场开放后，技能释放的频率明显不对。有玩家反馈同一个爆发技能能在半秒内按出两次，第二次明明显示冷却中却打出了伤害；也有玩家说技能攒满两层充能后，多等的那几秒全白等，重开一局才回到两层，跟公告写的初始层数对不上。更麻烦的是跨天维护：服务器把对局内的游戏时间往回调了一小段做校准，结果所有冷却集体归零，一波技能在开场十秒内全放了出来，录像被玩家逐帧截图发到了论坛。

落点在 `gsb/batch51-go-game-r-09` 的 `skill_cooldown_guard.go`，类型 `SkillCooldownGuard`，包名 `task`，模块路径 `gsb/batch51-go-game-r-09`。

公开接口保持：`NewSkillCooldownGuard(cfg CooldownConfig) (*SkillCooldownGuard, error)`、`RegisterSkill(def SkillDef) error`、`SkillDefOf(skillID string) (SkillDef, bool)`、`BeginBattle(battleID string, startTick int64) error`、`ResetBattle(battleID string, startTick int64) error`、`Cast(casterID, skillID string) (CastResult, error)`、`AdvanceTicks(n int64) (int, error)`、`SyncTick(serverTick int64) error`、`ChargesOf(casterID, skillID string) (int, bool)`、`CooldownOf(casterID, skillID string) (int64, bool)`、`Castable(casterID, skillID string) bool`、`ApplyCooldownReduction(casterID, skillID string, ticks int64) error`、`CurrentTick() int64`、`BattleID() (string, bool)`、`Audit() []AuditEntry`、`Stats() GuardStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `MaxCasters` 与 `MaxAudit` 必须为正，否则报 `ErrBadConfig`；新实例的当前 tick 为零、没有进行中的战斗，各项计数都是零。
2. 技能编号非空、冷却 tick 非负、最大层数至少一层、初始层数落在零到最大层数之间、多层技能的充能间隔为正，否则报 `ErrBadSkill`；重复注册报 `ErrSkillExists`。
3. 开战前施放报 `ErrNoBattle`；开战编号为空报 `ErrBadBattle`，已经在战斗中再开报 `ErrBattleActive`；重开只认当前战斗编号，编号不符报 `ErrUnknownBattle`。
4. 施放要校验战斗者非空（`ErrBadCaster`）与技能已注册（`ErrUnknownSkill`），成功时扣一层、返回剩余层数与冷却结束 tick。
5. 冷却按游戏 tick 推进，`AdvanceTicks` 只接受正数，`CooldownOf` 返回剩余 tick，推进到零后可再次施放。
6. 充能按 `RechargeTicks` 逐层恢复，`AdvanceTicks` 返回本次补上的层数；层数未满时充能计时连续，不能因为反复施放把计时重置成零。
7. `SyncTick` 只允许向前校准，服务端 tick 小于当前 tick 报 `ErrTickRegression` 且当前 tick 不动。
8. 冷却缩减只减少剩余冷却、不得为负，缩减量小于零报 `ErrBadReduction`，缩减到零为止，不得凭空补层。
9. `CurrentTick` 与 `BattleID` 如实反映当前状态；`Castable` 只在有可用层数、冷却已结束且战斗进行中时返回真。
10. 审计按动作追加、序号严格递增，只保留最近 `MaxAudit` 条；`Stats` 给出施放数、被拒数、充能层数与重开次数。
11. 快照要带当前 tick、战斗编号、每个战斗者每个技能的层数与冷却、审计与统计；恢复后冷却接着走，损坏快照报 `ErrBadSnapshot` 且不动现网。
12. 同一技能连续施放两次时第二次按剩余冷却拒绝，不能排队等待；充能层数满时继续充能只保留时间戳，不溢出层数；战斗重开时冷却与充能按配置重置，已消耗层数不回补。

硬性约束：`Cast` 与 `AdvanceTicks` 会被多路战斗协程并发调用，同一战斗者的层数只能扣一次、不能扣成负数；快照与结算互斥，任何时刻读到的都是完整状态；每个战斗者每个技能的层数与审计条数都受上限硬顶。

验收：`go test ./...` 15 条全部通过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-r-09`
- 实现文件：`skill_cooldown_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SkillCooldownGuard`
