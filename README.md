# 结构化输入的语法变异调度：语法树保真变异、接受率反馈与覆盖增益配比

## 题面

解析器压测跑了一轮，覆盖增长停在第三天。翻账才发现同一个算子被反复派发，交过的输入又被打了一遍，评分还把触发业务报错的用例算成解析失败，算子权重越跑越偏。这套调度要补到 `gsb/batch51-go-safety-c-42` 的 `grammar_mutation_scheduler.go`，类型名 `GrammarMutationScheduler`，构造 `NewGrammarMutationScheduler()`。

接口：`LoadGrammar(spec GrammarSpec) error`、`Grammar() GrammarSpec`、`AddSeed(in Input) error`、`Mutate(seed Input, op MutationOp) (Input, error)`、`Fingerprint(in Input) string`、`Submit(input Input, feedback Feedback) error`、`Score(op MutationOp) (float64, error)`、`Stats(op MutationOp) (OpStats, error)`、`Next(budget int) ([]Input, error)`、`Ledger() []string`、`Budget() (BudgetState, error)`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。

语法是若干产生式，每个带若干字面量备选；输入是解析树，产生式节点带子节点，字面量叶子带值。变异分替换、边界插入、子树拼接三种，算子名为 `replace`、`boundary`、`splice`。

要落地的十二条：

1. 装载时校验：根不存在、产生式无备选、`NumericRules` 名字不在产生式里、产生式重名、上限为负，都报 `ErrBadGrammar`；边界清单留空用 `0`、`-1`、`2147483648` 兜底；未装载语法就变异报 `ErrNoGrammar`。
2. 种子目标与语法不符、或者树是空的，报 `ErrBadSeed`；算子名不认识报 `ErrUnknownOp`。
3. 替换按前序找第一个子节点正好等于某备选的产生式节点，换成下一个备选；备选里的产生式名按该产生式第一备选展开。找不到报 `ErrNoApplicableNode`。
4. 边界插入只在 `NumericRules` 指定规则的叶子上动手，取边界清单里第一个在整棵树中还没出现过的值；只换叶子值，不动结构。
5. 拼接找前序里第一对同名产生式节点，把两者子节点整棵互换，不做扁平化；没有这样一对节点报 `ErrNoApplicableNode`。
6. 变异不得改动入参树，产物要带回语法目标与算子名，并把自己的指纹写进去；`Fingerprint` 对同一棵树必须稳定，两棵只差一个叶子值的树指纹必须不同。
7. `Submit` 按指纹记台账：同一指纹第二次提交报 `ErrDuplicateInput`；缺指纹或算子不认识报 `ErrBadInput`；`Ledger` 按提交先后返回。
8. 被解析器接受但语义被业务规则拒绝的输入，仍要计入接受率，不能当成解析失败；接受率与增益分开记。
9. 权重初始为 1，等于（1+累计增益）/（1+提交次数）；连续零增益从第二次起每次再乘二分之一，次数为连续零增益次数减一；结果最低压到 `MinWeight`，四舍五入到四位小数。中间来一次有增益的提交，连续计数清零。
10. `Next` 按权重占比分配名额：先向下取整，余数按权重从高到低逐个补一，同权按 `replace`、`boundary`、`splice` 的固定序；生成时跳过已发放和已提交的指纹。预算非正报 `ErrBadBudget`，没有种子报 `ErrNoSeeds`，凑不满就返回现有的，不报错。
11. 拼接后深度或节点数超上限要整棵回退重试，不能截半棵树交出去；同一算子在两类目标上的增益分桶统计，不能互借权重。
12. 快照保存语法、种子、算子台账与两个指纹集合；恢复后权重与配额进度不得重置；快照损坏报 `ErrBadSnapshot`，失败不改动现网状态。

硬约束：`Next` 与 `Submit` 可以并发调用，已发放与已提交两个集合的读写要串行裁决，同一指纹只能落在一个批次里；快照只能读到全量旧状态或全量新状态；同一批种子重复生成，算子顺序、名额与指纹顺序必须完全一致。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-42`
- 实现文件：`grammar_mutation_scheduler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`GrammarMutationScheduler`
