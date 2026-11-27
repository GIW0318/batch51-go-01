# 程序化地牢的钥匙门依赖校验、死锁识别与预算续跑

## 题面

新赛季的程序化地牢上线后，测试组连续卡在几个打不通的种子上：有的种子从入口根本走不到 Boss，报告只写“某房间不可达”，两把钥匙分别锁在对方门后时也只报了一条不可达路径；有的种子校验跑到一半就超时，续跑后预算翻倍还是跑不完；同一台机器同一种子生成出来的地牢每次还不一样，复现都做不到。请在 gsb/batch51-go-game-a-04 的 dungeon_layout_validator.go 里把 DungeonLayoutValidator 改到位，调用方按下面的签名使用：NewDungeonLayoutValidator(limits Limits) *DungeonLayoutValidator、Generate(seed uint64, cfg LayoutConfig) (Layout, error)、Validate(seed uint64, cfg LayoutConfig) (Report, error)、AnalyzeLayout(layout Layout) (Report, error)、DependencyGraph(seed uint64) (Graph, error)、SuggestRepair(report Report) []Repair、Resume(token ResumeToken, budget int) (Report, error)、IsReachable(report Report, room int) bool、LayoutOf(seed uint64) (Layout, bool)、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) Generate 按种子确定性产出房间序列：房间号从 0 递增，0 号房间种类为 entry，Boss 房在房间列表内且种类为 boss，门数与钥匙数与配置一致，同一种子重复调用结果逐字段相同。
2) 配置非法直接拒绝：RoomCount 小于 2、KeyCount 为负或 Version 小于 1 返回 ErrInvalidConfig；RoomCount 超过 Limits.MaxRooms 返回 ErrRoomLimit，DoorCount 超过 MaxEdges 返回 ErrEdgeLimit。
3) AnalyzeLayout 先做结构校验：入口必须是 0 号房且种类为 entry，Boss 必须在房间列表内且种类为 boss，门号唯一，门两端房间都必须存在，任一不满足返回 ErrBadLayout 且不出报告。
4) 可达性从入口按门做不动点推进：房间里的钥匙可以用来开它后面的门，双向门两个方向都要算，环形连接不得让分析陷入死循环。
5) Reachable 与 Unreachable 把房间恰好分一次，两者都按房间号升序；只有全部房间可达时 Complete 才为 true。
6) 被钥匙挡住的门按门号升序进 Locked，拿不到的钥匙按名称升序进 MissingKeys。
7) 两把钥匙分别锁在对方门后时必须报告为同一强连通分量死锁，不能只报一条不可达路径；同一死锁房间集合放进 Deadlocks。
8) AnalyzeLayout 受 Limits.MaxVisit 约束，超预算时返回部分报告、Truncated 为 true 和可用的 ResumeToken，同时以 ErrBudgetExceeded 提示调用方；已访问房间数写在 Visited。
9) 续跑时已确认可达的房间集合必须从令牌恢复，不能从头重算导致预算翻倍。
10) 相同种子在不同配置版本下允许布局不同，但依赖图的节点、边与访问顺序必须仍然确定；DependencyGraph 对未生成的种子返回 ErrUnknownSeed。
11) SuggestRepair 只允许加门与补钥匙两类建议，不得建议移动入口或 Boss 房；拿不到的钥匙给 add_key 建议并带上钥匙名，不可达区域给 add_door 建议；完全可达的报告不给任何建议。
12) Resume 的预算必须为正，否则返回 ErrInvalidBudget；令牌为空或损坏返回 ErrBadToken。快照要带上已生成的布局与配置版本，Restore 遇到损坏载荷返回 ErrBadSnapshot 且不动现有状态。

硬约束：分析与生成会被多个协程并发调用，已生成布局表与快照必须原子可见，读到的只能是某一次完整生成的结果；预算与已访问集合按令牌精确结转，续跑不得让总访问量超过原始预算；已生成布局按种子登记，快照恢复不得丢失任何已登记种子。

验收要求：go test ./... 15 条用例必须全部通过，测试文件不得修改、不得跳过、不得按固定输入写死；并发生成、续跑与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-04`
- 实现文件：`dungeon_layout_validator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DungeonLayoutValidator`
