# 跨源资源共享配置探测

## 题面

市场活动页接入第三方客服组件之后，合作方反馈他们自己的站点能带着用户 Cookie 读到我们的账号信息；安全同事用命令行工具试了几组来源头，有的来源被原样回显，有的只在预检里放行、实际请求又被拒，还有的接口只在错误分支里回显来源，单看一次成功请求根本判断不了到底安不安全。运维先按来源逐个加白名单，问题暂时躲过去了，可新一轮活动一上又复现，谁也不敢保证下一次不会再漏。需要一套把来源、凭据与预检三组变量固定下来逐对提交的探测能力。落点在 `gsb/batch51-go-safety-c-24` 的 `cors_misconfig_prober.go`，类型 `CorsMisconfigProber`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-24`。

公开接口保持：`NewCorsMisconfigProber(cfg ProberConfig) *CorsMisconfigProber`、`AddEndpoint(policy CorsPolicy) error`、`Build(kind, origin string, withCredentials, preflight bool) (Request, error)`、`Probe(endpoint string, req Request) (Response, error)`、`Judge(endpoint string) (Verdict, error)`、`Records(endpoint string) []ProbeRecord`、`Stats(endpoint string) ProberStats`、`GlobalStats() ProberStats`、`RecycleMarkers(endpoint string) int`、`Dump() ([]byte, error)`、`Load(data []byte) error`。

要修的地方：端点名为空白报 `ErrBadEndpoint`、重名报 `ErrDuplicateEndpoint`、未注册报 `ErrUnknownEndpoint`；来源为空白报 `ErrBadOrigin`，来源变体不认识报 `ErrBadKind`；精确来源请求带 GET 与 `Origin` 头，预检请求用 OPTIONS 并带 `Access-Control-Request-Method`，带凭据时补 `Cookie`，每个请求都要带一枚可回收标记且一轮判定内不重复；精确白名单只回显白名单内的来源并置 `Vary: Origin`；后缀相似来源不得被当成白名单命中；全反射实现要把任意来源原样回显；凭据开关只看策略，不能因为请求带了 Cookie 就放行；判定要区分精确白名单、后缀匹配与全反射三类实现，并记录预检与实际请求结论不一致的接口；允许来源头只在错误分支返回时，不能凭单次成功请求判成安全；空来源被放行要单独定级，不能与普通来源反射合并；预检允许而实际请求被拒时，结论要标注可利用前提而不是直接判存在；同一接口的成对请求按预检在前、实际在后的固定顺序提交；单接口在途探测数设上限并统计峰值；每接口探测条数由 `MaxProbesPerEndpoint` 硬顶，超出报 `ErrProbeBudget`；转储与加载要跨重启带回策略、记录与计数，损坏快照报 `ErrBadSnapshot` 且不改动现网。

硬约束：同一接口的成对请求必须串行，一轮判定里前一个请求没有收尾不得提交下一个；全局在途数不超过 `MaxConcurrent`；标记一次性使用，回收之后不得再出现在新请求里；转储与加载整体成功或整体拒绝，加载后已有记录的顺序与计数不变。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发探测与重启恢复自行构造程序验证；只做本地实现。交付源码、测试记录，以及一页以内的分级口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-24`
- 实现文件：`cors_misconfig_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CorsMisconfigProber`
