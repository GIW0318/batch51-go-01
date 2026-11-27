# 路径规范化绕过的编码层级探测与访问控制判定

## 题面

网关和后台对路径的解码规则一直不一致，渗透报告里写着：`/admin` 直接打是 403，但 `/admin/` 和 `/ADMIN` 都是 200，报告直接判成了越权成功；另一份报告说 `/admin%2fusers` 与 `/admin/%2e%2e/admin` 被前置网关放行了。复测的人跑了一轮才发现，那两个 200 其实只是统一登录页，跟后台数据一点关系都没有，真正被绕过的路径被埋在一堆误报里。

落点在 `gsb/batch51-go-safety-c-39` 的 `path_access_bypass_prober.go`，类型 `PathAccessBypassProber`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-39`。

公开接口保持：`NewPathAccessBypassProber(cfg ProberConfig) *PathAccessBypassProber`、`SetTransport(t Transport) error`、`SetClock(c Clock) error`、`Baseline(path string) (Baseline, error)`、`Variants(path string) ([]PathVariant, error)`、`Probe(variant PathVariant) (Observation, error)`、`Judge(obs []Observation) (BypassVerdict, error)`、`Budget() (BudgetState, error)`、`Observations() []Observation`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `Baseline` 取受保护路径本身的状态码、长度、关键标记与指纹；路径为空或纯空白报 `ErrBadPath`，没装传输报 `ErrNoTransport`，这一次请求同样计入预算。
2. 指纹取响应正文的 FNV-1a 64 位十六进制串；关键标记按配置顺序列出响应里出现的那几个，一个都没出现就留空。
3. `Variants` 覆盖六个维度且顺序固定：单层百分号编码、双层百分号编码、点段、分号参数、大小写、尾随斜杠；每个变体的路径都必须与源路径不同。
4. 同一路径连调两次 `Variants`，编号与路径完全一致；大小写变体把整段路径转大写，尾随斜杠变体在末尾追加一个斜杠。
5. 每个变体只探测一次并记下时间戳，重复探测报 `ErrVariantProbed` 且不再发请求；多路协程同时探测同一变体，只能有一个请求落地。
6. `Probe` 只接受 `Variants` 产出的编号与维度，其余报 `ErrUnknownVariant`，一个请求都不发。
7. 总请求预算是硬顶，用满报 `ErrBudgetExhausted` 且不发请求；`Budget` 要能读出已用、上限与剩余，基线请求也算在已用里。
8. `Judge` 收到空观测报 `ErrNoObservations`，还没取到基线就判定报 `ErrNoBaseline`。
9. 判定同时看状态码、长度、关键标记与指纹：2xx 且带受保护标记、并且指纹与基线不同才算绕过；403、404 以及其它非 2xx 一律算拒绝。
10. `Bypassed` 与 `Rejected` 都按变体编号升序，同一组观测重复判定，两个列表与理由必须完全一致。
11. `Snapshot` 带上基线、观测与预算消耗，恢复后同一变体不再重复探测，损坏快照报 `ErrBadSnapshot` 且不动现有状态。

另有三处要在实现里落实：后台返回 200 但内容还是登录页时不算绕过，必须和基线页面做结构比对；双层编码只有在前置网关解一次、后端再解一次时才生效，单层结果不能直接外推到双层；末尾斜杠与分号参数可能命中不同路由却落到同一个处理器，判定要按处理器指纹而不是状态码。

硬约束：同一路径的变体按确定性顺序探测，每个变体在整轮里只发一次请求，并发下也不许发第二次；401、403 与 404 的区分要保守，统一错误页不能当成绕过成功；基线指纹与已探测变体要随快照落盘，续跑不重复探测也不把预算清零；判定与探测并发时，读到的观测要么是旧的完整一份要么是新的完整一份。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发探测与重启续跑自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-39`
- 实现文件：`path_access_bypass_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PathAccessBypassProber`
