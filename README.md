# 隐藏参数发现：响应差分、单参数定位与逐轮预算

## 题面

文档里那个搜索接口只写了 `page` 和 `size`，红队报告却说加一个 `debug=1` 就能拿到调试输出。复测的同学把候选参数一个一个试了两天，一次都没复现，最后才发现 `debug` 与 `trace` 必须一起带才生效；另一轮更冤，基线响应里带了一个时间戳，整轮探测都被判成"有差异"，报上去十九条全部是假的。

落点在 `gsb/batch51-go-safety-c-40` 的 `hidden_param_miner.go`，类型 `HiddenParamMiner`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-40`。

公开接口保持：`NewHiddenParamMiner(cfg ProberConfig) *HiddenParamMiner`、`SetTransport(t Transport) error`、`Baseline(endpoint Endpoint) (Baseline, error)`、`Candidates(endpoint Endpoint, seed []string) ([]ParamCandidate, error)`、`Probe(endpoint Endpoint, batch []ParamCandidate) ([]ParamObservation, error)`、`Judge(obs []ParamObservation) ([]ParamFinding, error)`、`Confirmed() []ParamFinding`、`Observations() []ParamObservation`、`Budget() (BudgetState, error)`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `Baseline` 取状态码、长度、字段集合与指纹，按配置的样本数取样并比对；字段集合把正文按 `&` 拆开后取 `=` 前面的键名，去重升序；端点名为空或路径为空报 `ErrBadEndpoint`，没装传输报 `ErrNoTransport`，每个样本都记一次预算。
2. `Candidates` 按来源分组：调用方给的表、端点既有参数、页面正文里 `name="..."` 的引用；组内按名字升序，同名忽略大小写只留第一次出现的那条。
3. 一个候选都凑不出来报 `ErrNoCandidates`。
4. `Probe` 整批注入，每个候选带一个唯一哨兵值；正文里出现某候选的哨兵时，该候选记反射，位置写 `body`。
5. 整批有差异却没有反射时，按对半拆分递归复测，直到把变化定位到单个候选，层级不超过 `MaxSplits`；不许把一批的变化算到第一个候选头上。
6. 每个候选都要有一条观测，顺序与入参一致；没造成差异的候选记为无差异，证据写 `none`。
7. 空批次报 `ErrEmptyBatch`；批次里同名的候选只探测一次。
8. 总请求预算是硬顶，预算不够时整批探测报 `ErrBudgetExhausted`，不返回半份观测，也不把这一轮的观测落库。
9. `Judge` 收到空观测报 `ErrNoObservations`，还没取到基线就判定报 `ErrNoBaseline`；有差异的观测产出发现项，按参数名升序，来源与证据照抄观测。
10. 已确认的参数在续跑时直接返回上次观测，不再发请求；`Confirmed` 与 `Observations` 都要能整体列出。
11. `Snapshot` 带上基线、观测、已确认参数与预算消耗；损坏快照报 `ErrBadSnapshot` 且不动现有状态。

另有三处要在实现里落实：参数被接受但值被忽略时响应毫无变化，这类参数只能靠反射位置或错误码变化认出来，不能因为没差异就直接排除；同一参数名在表单与 JSON 两种请求体里语义不同，两边的结果不能互相替代；基线响应里带时间戳或随机令牌时先做片段归一，否则整轮都会被判成有差异。

硬约束：批量探测必须能定位到单个参数，一批里只有一个真正生效时其余候选不能跟着被判命中；响应本身就在波动的端点先降级为不可判定，不许硬判；快照要带上基线与已确认参数，续跑既不重复探测也不把预算清零；请求预算与拆分层级两条上限同时生效，任何一条用尽都当场收手。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；批量定位与重启续跑自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-40`
- 实现文件：`hidden_param_miner.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`HiddenParamMiner`
