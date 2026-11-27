# 关卡寻路网格的动态障碍与人群避让

## 题面

副本上线后寻路相关的投诉集中爆发。第二关卡门打开时，几波怪同时从通道涌进来，一部分角色贴着墙原地抽搐，一部分直接穿过了新刷出的石柱；玩家点地面移动，角色绕了一大圈才走到目标点，明明石柱三十秒前就被打碎了。更离谱的是人群：二十个玩家挤在同一个格子里，后排的角色被前排推来推去，卡在门框上谁也进不去，团长在语音里喊了十分钟。

落点在 `gsb/batch51-go-game-r-08` 的 `navmesh_crowd_guard.go`，类型 `NavmeshCrowdGuard`，包名 `task`，模块路径 `gsb/batch51-go-game-r-08`。

公开接口保持：`NewNavmeshCrowdGuard(grid Grid, cfg CrowdConfig) (*NavmeshCrowdGuard, error)`、`AddObstacle(obs Obstacle) error`、`RemoveObstacle(id string) error`、`Obstacles() []Obstacle`、`FindPath(req PathRequest) (PathResult, error)`、`ClaimCell(cell Cell, unitID string, priority int) (ClaimResult, error)`、`ReleaseCell(cell Cell, unitID string) error`、`CrowdAt(cell Cell) int`、`AvoidStep(unitID string, from, desired Cell) (Cell, error)`、`Recompute(region Region) (RecomputeReport, error)`、`NearestReachable(from Cell) (Cell, bool, error)`、`SetDensityLimit(limit int) error`、`Degraded() bool`、`Stats() GuardStats`、`Audit() []AuditEntry`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. 网格宽高必须为正，`Blocked` 要么为空表示全通，要么长度等于宽乘高，否则报 `ErrBadGrid`；`MaxUnits`、`DensityLimit`、`RecomputeBudget`、`MaxObstacles`、`MaxAudit` 必须为正，否则报 `ErrBadConfig`。
2. 坐标越界一律报 `ErrOutOfRange`；静态阻挡格不能走，也不能被占位；单位编号为空报 `ErrBadUnit`。
3. 动态障碍编号非空、区域合法且不越界，否则报 `ErrBadObstacle` 或 `ErrOutOfRange`；重复编号报 `ErrObstacleExists`，删除不存在的障碍报 `ErrUnknownObstacle`，障碍数量超过 `MaxObstacles` 报 `ErrObstacleLimit`。
4. 寻路只走上下左右四邻格，角色路径首格为起点、末格为终点，代价按步数计，结果里带上路径、代价与是否到达。
5. 寻路必须绕开静态阻挡与全部动态障碍，怪物与角色都不能穿过阻挡格，路径中不得出现任何阻挡格。
6. 单次寻路的扩展格数超过 `RecomputeBudget` 报 `ErrBudgetExhausted`，不得给角色返回一条走不通的路径。
7. 怪物与角色的占位请求要校验格子在网格内且不是阻挡格；同一单位重复占同一格幂等成功，人群计数不重复累加。
8. 释放只认当前占位者，别人来释放报 `ErrNotOwner`，释放后该格人群计数下降。
9. `CrowdAt` 返回该格当前占位单位数；`AvoidStep` 在目标格人群数达到 `DensityLimit` 时角色原地不动并置降级，人群回落后恢复移动；`SetDensityLimit` 只接受正数。
10. `Recompute` 只重算路径穿过指定区域的单位，区域外的单位路径保持原样，结果按单位编号升序列出受影响单位；区域反向报 `ErrBadRegion`，越界报 `ErrOutOfRange`；统计给出寻路次数、成功路径数、重算次数、占位数、释放数与降级次数。
11. 动态障碍移除后路径要恢复，不能一直走绕行路线；同一格被多个单位同时占用时按优先级裁决，低优先级单位重新寻路；寻路失败要返回最近可达点并标注，不能返回空路径让角色原地卡住。
12. 快照要带网格、障碍、占位、每个单位最近一次路径、统计与审计；恢复后继续寻路与占位，损坏快照报 `ErrBadSnapshot` 且不动现网。

硬性约束：`ClaimCell`、`ReleaseCell` 与 `FindPath` 会被多个战斗协程并发调用，同一格的人群计数不得丢更新；重算与快照互斥，任何时刻读到的路径表与占位表都是完整的；障碍数、审计条数与扩展预算都受上限硬顶。

验收：`go test ./...` 15 条全部通过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-r-08`
- 实现文件：`navmesh_crowd_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`NavmeshCrowdGuard`
