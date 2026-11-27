# 批量赋值探测：可写字段枚举、越权字段差分与服务端默认值识别

## 题面

上线前自查时发现，创建订单的接口除了文档里的两个字段，还能把 `status` 和 `role` 一起写进去，改完再读回来值确实生效；另一个团队照着同样的思路去打更新接口，把只读字段被忽略直接当成了"不可写"，漏报了洞。这套字段探测要补到 `gsb/batch51-go-safety-c-43` 的 `mass_assignment_prober.go`，类型名 `MassAssignmentProber`，构造签名 `NewMassAssignmentProber(store Store, catalog Catalog, limits Limits) *MassAssignmentProber`。

接口：`Schema(endpoint Endpoint) (FieldSet, error)`、`Marker(field Field) string`、`Probe(endpoint Endpoint, fields FieldSet) ([]FieldObservation, error)`、`Diff(obs []FieldObservation) ([]FieldFinding, error)`、`Judge(f FieldFinding) (MassAssignVerdict, error)`、`Budget() (BudgetState, error)`、`Catalog() Catalog`、`Created() []string`、`CleanupState() []string`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。

要落地的十二条：

1. `Schema` 把端点自己声明的字段与同资源兄弟端点的字段名取并集，按名字升序去重；只读标记按各自端点的声明保留，不跨端点借用。
2. 端点标识为空报 `ErrBadEndpoint`，目录里没有的端点报 `ErrUnknownEndpoint`，通道为 nil 报 `ErrNilStore`，字段集为空报 `ErrEmptyFieldSet`，字段集里的端点与入参对不上也报 `ErrBadEndpoint`。
3. 注入值：字符串字段用 `gsb-<字段名>-probe`，整型用 `424242`，布尔用 `true`，枚举取最后一个候选值；同一批里每个字段各带各的值。
4. 分批注入：字段按名字排好再按 `BatchSize` 切片，一批一次创建或更新；每次创建或更新算一次探测，超过 `MaxProbes` 报 `ErrProbeBudget`，已经建出来的探测对象仍要清掉。
5. 回读判定：响应回显只说明服务端收下了，能不能写进去必须再读一次比。回读值与注入值相同的才算命中，只被回显没落库的不算。
6. 批量命中：一批里命中两个及以上字段时，要逐个字段单独再探一次做归因，归因后的观测标 `Bisected` 并带自己的批次号，批级观测不再保留这些字段。
7. 清理：探测创建的对象要全删，删除失败记进 `CleanupState` 并让待清理计数不为零，但不能因为清理失败中断探测；更新接口先用基线当前值做快照，每批探完写回原值，基线不得残留注入值。
8. 同一端点重入报 `ErrBusy`，探测结束才释放；不同端点之间互不阻塞。
9. `Diff` 按端点分组，字段与命中字段各自升序，输出按端点升序；没有观测报 `ErrNoObservations`。
10. 一批里多个命中却没有做单字段归因的，`Diff` 要把 `RequiresBisect` 置真；有清理失败的把 `CleanupNeeded` 置真。
11. `Judge`：命中 0 个不确认、级别 `none`；1 个低、2 个中、3 个及以上高；待归因未消解时不下结论；缺端点标识报 `ErrBadFinding`。
12. 字段被接受但值被服务端默认值覆盖的不算越权，必须拿显式赋值后的回读值来比；布尔与枚举字段的默认值可能与注入值相同，命中要用非默认值判；只读字段在更新接口被忽略、在创建接口可写时，两个接口要分别判定。

硬约束：字段台账（端点、字段、批次、注入值、回读值、清理失败标记）要能整体进快照，恢复后台账与额度不得清零；同一批字段重复探测，观测顺序与批次编号必须完全一致；探测与快照互斥，读到的要么是整批旧状态要么是整批新状态。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-43`
- 实现文件：`mass_assignment_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`MassAssignmentProber`
