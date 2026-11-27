# 文档查询操作符注入探测

## 题面

运营在工单后台的搜索框里填了一串带美元符号的条件，本该只看到自己租户的工单，结果整库数据都被翻了出来；同一天另一个入口把同样的条件当成普通文本存进了库里，页面一切正常，排查的人一度以为漏洞并不存在。两个入口的差别只在参数解析这一层，需要把这套判定做成可复用的探测能力。落点在 `gsb/batch51-go-safety-c-21` 的 `nosql_operator_prober.go`，类型 `NoSqlOperatorProber`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-21`。

公开接口保持：`NewNoSqlOperatorProber(cfg ProberConfig) (*NoSqlOperatorProber, error)`、`RegisterTarget(name string, docs []Document, mode string) error`、`Generate(field, value, op string) (Payload, error)`、`Inject(target string, payload Payload) (Response, error)`、`Baseline(target, field string) (Baseline, error)`、`Verdict(target, field string) (Verdict, error)`、`ProbeLog(target string) []ProbeRecord`、`Stats(target string) ProberStats`、`GlobalStats() ProberStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：目标名为空报 `ErrBadTarget`，同名重复注册报 `ErrDuplicateTarget`，探测未注册目标报 `ErrUnknownTarget`；字段名为空报 `ErrBadField`，不认识的比较符报 `ErrBadOperator`；`Generate` 必须产出 `{"字段":{"操作符":"值"}}` 形式的嵌套对象并把值按 JSON 字符串转义；对象解析路径下 `$eq` 命中字段相等的文档，`$ne` 命中字段存在且不相等的文档，`$gt` 与 `$lt` 按数值比较、两侧解析不成数字就不命中，`$regex` 按正则匹配字段原文，`$in` 按逗号分隔的候选值逐项比对；结果按注册时的文档顺序给出编号，状态码 200，解析不了的载荷返回 400；真假条件对必须成对出现，真对照样命中全部带该字段的文档，假对用同一哨兵值命中零条，两条记录共用同一个基线序号；真对命中为零时不能直接判成不可注入，要换计数或延时通道再复核一次；目标只收数组形式的参数时，对象载荷要改写成数组下标路径再试一遍，不能一次失败就收工；每个目标的可提交探测条数由 `MaxProbesPerTarget` 硬顶，超出报 `ErrProbeBudget` 并计入拒绝数，不静默丢弃；单个目标的探测串行提交，全局在途探测数不超过 `MaxConcurrent`，两个峰值都要如实统计；记录按 `MaxRecords` 淘汰最旧，不同目标的记录互不混算；快照要跨重启带回目标文档、解析模式、基线、记录与计数，损坏快照报 `ErrBadSnapshot` 且不改动现网状态。

硬约束：同一目标的探测提交必须串行，基线快照在一对探测之间不得被别的探测改写，统计峰值与实际在途数一致；快照与恢复必须整体成功或整体拒绝，恢复后新探测的序号大于此前任一条记录；探测条数、记录条数与并发数都按配置封顶，任何一路超限都不许覆盖已有记录。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发提交与重启恢复自行构造程序验证；只做本地实现。交付源码、测试记录，以及一页以内的判定口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-21`
- 实现文件：`nosql_operator_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`NoSqlOperatorProber`
