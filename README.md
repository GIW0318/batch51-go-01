# 同时回合结算：移动冲突仲裁、交换位、路径交叉与确定性结算

## 题面

战棋同时回合上线后，玩家在论坛贴出大量“幽灵位移”录像：两名单位从不同方向走向同一格，结算时一个过去了，另一个却停在原地，战力更高的那个反而被挡；更常见的是两个单位前后相邻，后面的走向前面刚离开的格子，结算后两个都没动。公会战里还出现过两个单位互换位置，结算成一个动了一个没动，下一回合站到了同一格里。运营按不同的点击顺序重放同一批指令，结算结果居然不一样。

落点在 gsb/batch51-go-game-a-20 的 simultaneous_turn_resolver.go，类型 SimultaneousTurnResolver，包名 task，模块路径 gsb/batch51-go-game-a-20。公开接口保持：NewSimultaneousTurnResolver(cfg TurnConfig) *SimultaneousTurnResolver、SubmitOrder(turn int, order Order) (OrderID, error)、SealTurn(turn int) error、Resolve(turn int, board BoardView) (TurnResult, error)、SetCollisionPolicy(p CollisionPolicy) error、CancelOrder(turn int, id OrderID) error、Explain(turn int) []ConflictTrace、Stats() TurnStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) SubmitOrder 的回合号必须为正、单位号不能为空白、指令类型只能是移动、换位与原地待命、行动力消耗不得为负，否则 ErrBadOrder。
2) 路径至少两个格子、相邻两格必须在八邻域内、长度不得超过 MaxPathLen，否则 ErrBadPath；路径起点与棋盘位置不一致的指令在结算时作废。
3) 同一单位在同一回合只能有一条有效指令，重复提交返回 ErrDuplicateOrder；指令数达到 MaxOrdersPerTurn 后新指令返回 ErrOrderLimit；未封盘的指令可以用 CancelOrder 撤回，撤回不存在的指令返回 ErrUnknownOrder。
4) 封盘后本回合不再接收指令，重复封盘返回 ErrTurnClosed；未封盘直接结算返回 ErrTurnOpen；封盘后到达的指令要拒绝并带回封盘序号，不得顺延到下一回合。
5) 结算必须先把指令按单位号稳定排序再统一处理，结果与提交顺序、协程调度顺序无关；同一回合重复结算返回同一份结果。
6) 多个单位终点相同时按行动力消耗升序、单位等级降序、单位号升序依次裁决，胜者进入目标格，败者停在原格并记为 blocked，冲突计入 Conflicts。
7) 目标格在棋盘障碍表里时该单位停在原格，状态 blocked；单位在结算前已经倒下时其指令作废，状态 void，但它原来占的格子本回合仍然不能让别的单位重复结算。
8) 两条换位指令互为终点时双方都成功，状态 swapped；只有一方申请换位时该方失败并停在原格，不能单方面把对方挤走。
9) 单位走向另一单位本回合离开的格子时双方都成功，只有终点相同才算冲突，路径在中途重叠不算冲突。
10) Explain 按格子坐标与单位号顺序给出冲突格、胜者、败者与被拒原因；Stats 汇总提交数、封盘数、结算数、撤回数与冲突数。
11) 单回合指令数、路径长度与冲突重解次数分别受 MaxOrdersPerTurn、MaxPathLen、MaxResolvePasses 限制，重解次数用尽仍无法收敛时按单位号升序保留先者、其余停在原格。
12) 快照保存未结算指令、封盘状态与已结算结果；恢复后可以继续封盘与结算，结果与未重启一致；载荷损坏返回 ErrBadSnapshot 且不动现有状态。

硬约束：SubmitOrder、SealTurn、Resolve 与快照会被战斗服与回放工具并发调用，同一回合的结算只能发生一次；指令表与封盘状态必须原子切换；同一棋盘与同一批指令任意次数结算必须逐字段一致。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-20`
- 实现文件：`simultaneous_turn_resolver.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SimultaneousTurnResolver`
