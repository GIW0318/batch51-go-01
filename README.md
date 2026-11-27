# XML 外部实体探测与带外回连归并

## 题面

售后系统支持商户上传对账文件，解析器对外部实体的处理一直说不清楚：有的入口把 DTD 里的实体整段展开回显，有的入口只在后台留下一次出网请求，还有的入口直接报错退出。安全同事手工试了三种载荷，回连记录混在共享监听器里，谁对谁根本对不上，最后只能凭时间猜。需要一套按探测编号把回显、报错与带外回连三路证据归并起来的探测能力。落点在 `gsb/batch51-go-safety-c-22` 的 `xxe_entity_prober.go`，类型 `XxeEntityProber`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-22`。

公开接口保持：`NewXxeEntityProber(cfg ProberConfig) *XxeEntityProber`、`RegisterEndpoint(ep Endpoint) error`、`BuildPayload(kind, probeID string) (Payload, error)`、`Submit(endpoint string, payload Payload) (Response, error)`、`DeliverCallback(cb Callback) error`、`Callbacks(endpoint string) []Callback`、`Correlate(endpoint string) []Correlation`、`Probes(endpoint string) []ProbeRecord`、`Probe(endpoint, kind string) (Finding, error)`、`Advance(ticks int64) int64`、`Expire(endpoint string) int`、`Snapshot() ([]byte, error)`、`Load(data []byte) error`。

要修的地方：端点名为空白报 `ErrBadEndpoint`，重名报 `ErrDuplicateEndpoint`，提交到未注册端点报 `ErrUnknownEndpoint`；探测编号为空报 `ErrBadProbeID`，载荷类型不认识报 `ErrBadPayloadKind`；内部实体载荷必须声明 `<!ENTITY 编号 ...>` 并在文档里引用 `&编号;`，外部实体与参数实体都要把编号嵌进 `http://collector.gsb/<编号>` 的带外地址；没有 DOCTYPE 的载荷报 `ErrBadPayload` 且不留探测记录；会展开通用实体的入口要在响应里回显 `GSB-<编号>` 并记为 `echo` 通道已确认；完全不展开实体的入口既不能算确认也不能算不存在，理由写成 `entity_not_expanded`；能解析外部实体的入口要自动登记一条同编号回连，`Correlate` 按编号归并三路证据；回连晚于提交时刻加回调窗口属于迟到，要回填到原探测并标 `Late`，不许新开一条关联；同一编号收到多次回连只保留一条关联、次数累加；过了回调窗口仍无回连的探测标 `uncertain`，不能标成不存在；解析器只展开参数实体而不展开通用实体时，只能靠带外通道确认；回调都来自同一个共享出口地址时，归属必须看载荷里嵌的编号而不是来源地址；实体展开把响应体截断时，不能把截断当成解析拒绝。每个端点可提交的探测条数由 `MaxProbesPerEndpoint` 硬顶，超出报 `ErrProbeBudget`；在途探测数不超过 `MaxConcurrent` 并统计峰值；快照要跨重启带回端点、探测记录、回连与逻辑时钟，损坏快照报 `ErrBadSnapshot` 且不改动现网。

硬约束：共享监听器上的并发探测必须按编号隔离，一条回连只能落到它自己的探测记录上；快照与加载要么整体成功要么整体拒绝，加载后原编号仍能继续回填；探测条数与回连表都按配置封顶，满了不许覆盖仍在等待的探测。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发回连与重启恢复自行构造程序验证；只做本地实现。交付源码、测试记录，以及一页以内的证据归并口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-22`
- 实现文件：`xxe_entity_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`XxeEntityProber`
