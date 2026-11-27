# 符号执行路径探索的调度、去重与预算续跑

## 题面

凌晨两点，风控平台的路径探索任务跑了一整夜，早上发现三件事：同一条 HTTP 解析链被展开成四万多条记录，大半是同一程序点、同一约束集的翻版；一批状态卡在求解器里不返回，把待探索队列堵死，后面的入口一条都没轮到；机器中途重启，进度全丢，只能从入口重跑，白天预算直接烧光。

落点在 `gsb/batch51-go-safety-c-36` 的 `symbolic_path_explorer.go`，类型 `SymbolicPathExplorer`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-36`。

公开接口保持：`NewSymbolicPathExplorer(program []Block, cfg ExplorerConfig) *SymbolicPathExplorer`、`SetSolver(s Solver) error`、`Seed(entry string, args []Symbol) error`、`Next() (PathState, error)`、`Step(stateID string, budget int) ([]PathState, error)`、`Solve(stateID string) (Model, error)`、`Prune(stateID, reason string) error`、`Merge(stateIDs []string) (PathState, error)`、`Pending() int`、`State(stateID string) (PathState, bool)`、`States() []PathState`、`Stats() ExplorerStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `Seed` 入口名去空白后为空报 `ErrBadEntry`，入口不在程序块表里报 `ErrUnknownBlock`，重复播种报 `ErrAlreadySeeded`；根状态编号从 1 起，别名表按符号名升序。
2. `Step` 的 `budget` 是本次允许展开的块数，小于等于 0 报 `ErrBadBudget`；未知状态报 `ErrUnknownState`，已终结或已剪枝的状态再展开报 `ErrStateFinished`。
3. 展开一块消耗 1 点全局展开预算（`ExplorerConfig.MaxExpansions`），预算耗尽时把当前状态置为 `suspended` 并原样返回，队列不再计它。
4. 展开产生的子状态会让待处理队列超过 `ExplorerConfig.MaxPending` 时，整次展开不落库，返回 `ErrPendingLimit`，原状态保持待处理。
5. 同一程序点加同一约束指纹只保留一份，重复的那份丢弃并计入去重计数。
6. `Next` 返回权重最高的待处理状态但不改变队列；权重按覆盖点个数乘十再加深度算，同权重时深度大的先出，队列空时报 `ErrNoPending`。
7. `Prune` 理由去空白后为空报 `ErrBadPruneReason`，成功后状态转 `pruned` 并移出队列，待处理计数相应减少。
8. `Merge` 少于两条报 `ErrMergeTooFew`，程序点不同或同一符号的内存地址不同一律报 `ErrMergeMismatch`；合并结果覆盖点与约束取并集、代价相加、深度取大，原状态转 `merged`。
9. `Solve` 对未知状态报 `ErrUnknownState`，对已剪枝状态报 `ErrStatePruned`；正常返回的模型要为每个受约束符号给出满足其全部约束的最小非负值，`>=` 取边界值、`>` 取边界值加一、`==` 取等值，没有下界的取 0。
10. 子状态深度超过 `ExplorerConfig.MaxDepth` 时不再创建，当前状态按 `depth` 剪枝计一次。
11. `Snapshot` 要带上状态表、队列顺序、编号水位、各项计数与已消耗的展开数；`Restore` 遇到损坏数据报 `ErrBadSnapshot` 且不动现有状态，恢复后新分配的编号必须大于此前任何一条。
12. `Stats` 的播种、展开、去重、剪枝、合并五项要和真实调用次数对上，剩余展开预算随消耗下降。

另有三处要在实现里落实：约束不可满足与求解超时结论不同，前者永久剪枝，后者挂起并在预算恢复后重试；状态合并只在内存别名完全一致时进行，别名不确定就保留两条路径而非求并集；覆盖点与深度都相同的状态按发现顺序稳定出队，不能因遍历顺序让两次运行结果不同。

硬约束：多路协程同时对同一状态调 `Step` 只能成功一次，另一路要么报 `ErrStateFinished` 要么报 `ErrUnknownState`，不允许把同一个状态展开两遍；快照与展开并发时读到的要么全是旧状态要么全是新状态；恢复后继续跑出的状态序列要和中断前一致；待处理队列与状态表按配置硬顶，任何一路超限都不许顶掉已有记录。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发展开与重启续跑自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-36`
- 实现文件：`symbolic_path_explorer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SymbolicPathExplorer`
