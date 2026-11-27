# 接口限流键推断与维度绕过判定

## 题面

网关换了一版限流之后，灰度两天里同一个登录接口被外部刷了三轮：账户维度的桶打满之后，对面只是把来源地址头换一个就继续拿到 200；另一轮把 `/API/Login` 整段大写也照样进得去；最麻烦的是值班同学把 500 和传输超时都按"没被限流"记进报表，最后判成可以绕过，安全评审直接被打回。

落点在 `gsb/batch51-go-safety-c-37` 的 `api_rate_limit_bypass_prober.go`，类型 `ApiRateLimitBypassProber`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-37`。

公开接口保持：`NewApiRateLimitBypassProber(cfg ProberConfig) *ApiRateLimitBypassProber`、`SetTransport(t Transport) error`、`SetClock(c Clock) error`、`Baseline(endpoint Endpoint) (LimitProfile, error)`、`Burst(endpoint Endpoint, n int) (BurstResult, error)`、`Variants(profile LimitProfile) ([]BypassVariant, error)`、`Probe(variant BypassVariant) (Observation, error)`、`Judge(obs []Observation) (BypassVerdict, error)`、`Budget() (BudgetState, error)`、`Observations() []Observation`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `Baseline` 连续请求到首个 429 为止，`Limit` 记首个 429 之前的通过数，`Probes` 记真实发出的请求数，`Blocked` 标记观测到限流；整段突发都没有 429 报 `ErrNoLimitObserved`，不能把没观测到当成无限额度。
2. 端点名为空或路径为空报 `ErrBadEndpoint`，没装传输报 `ErrNoTransport`；每一次真实请求都要记一次预算。
3. `Burst` 最多发 n 个请求，遇 429 提前收手，返回请求数、通过数、拒绝数与最后一个状态码；n 小于等于 0 报 `ErrBadBurst`。
4. `Variants` 覆盖六个维度且顺序固定：来源地址头、账户与会话、路径大小写、尾斜杠、方法覆盖、查询参数顺序；同一 profile 连调两次，编号、路径与顺序必须完全一致。
5. 路径大小写变体只把问号之前的路径段转大写，方法与查询串保持原样；账户与会话变体至少携带一个身份头，路径不动。
6. 每个变体只发一次并记下时间戳，重复探测报 `ErrVariantProbed` 且不再发请求；多路协程同时探测同一变体，只能有一个请求真正落地。
7. 不是 `Variants` 产出的编号与维度报 `ErrUnknownVariant`，一个请求都不发。
8. 总请求预算是硬顶，用满后报 `ErrBudgetExhausted` 且不发请求；`Budget` 要能读出已用、上限与剩余。
9. `Judge` 收到空观测报 `ErrNoObservations`；2xx 与 3xx 算放行，5xx 与传输失败一律按阻塞记，不能当成没被限流。
10. `Judge` 的三档结论：全部阻塞算全局，身份维度有放行算独立桶，其余放行算共享桶；`Bypassed` 按编号升序，`Reason` 不能为空。
11. `Snapshot` 保存已推断的 profile、已探测变体、观测与预算消耗；恢复后同一变体不再重复探测，损坏快照报 `ErrBadSnapshot` 且不动现有状态。
12. 判定结果只由观测决定，同一组观测重复判定，结构、放行列表与阻塞列表必须完全一致。

另有三处要在实现里落实：429 响应带 `Retry-After` 时按该值等待，退避期内不得换维度重试，否则会把共享桶的额度耗光；路径大小写绕过只对区分大小写的路由生效，一次同码响应不能外推到其它路径；限流窗口是滑动还是固定决定边界行为，单次突发不足以判定，要在窗口末端复测。

硬约束：同一突发窗口内每个变体只发一次请求，并发下也不许发第二次；退避与总请求预算两条上限同时生效，任何一条卡住都不许换一条路绕过去；快照要带上已推断的键结构与已探测变体，续跑时既不重复探测也不把预算清零；判定与探测并发时，读到的观测集合要么是旧的完整一份要么是新的完整一份。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发探测与重启续跑自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-37`
- 实现文件：`api_rate_limit_bypass_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ApiRateLimitBypassProber`
