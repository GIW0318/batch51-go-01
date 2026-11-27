# 主机头污染探测与取值链判定

## 题面

运营在后台发了密码重置邮件，用户点开链接却跳到了另一个域名；排查时页面上看不出任何异常，只有邮件模板里生成的链接被替换了。值班同事又在缓存里看到一条以陌生主机名为键的记录，说明污染值还可能进了缓存键；同一个入口用直连主机头、转发主机头和绝对请求行三种方式注入，到底哪一层取了值、取的是哪一个，谁也说不清。运维临时把入口换成固定域名后问题消失，可活动页依赖的第三方域名也跟着不可用，只能再改回来。需要一套把来源与取值层对应起来的探测能力。落点在 `gsb/batch51-go-safety-c-25` 的 `host_header_poison_prober.go`，类型 `HostHeaderPoisonProber`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-25`。

公开接口保持：`NewHostHeaderPoisonProber(cfg ProberConfig) *HostHeaderPoisonProber`、`AddEntrypoint(policy HostPolicy) error`、`Build(source, host string) (Request, error)`、`Probe(endpoint string, req Request) (Observation, error)`、`Judge(endpoint string) (Verdict, error)`、`Sources(endpoint string) []SourceResult`、`Records(endpoint string) []ProbeRecord`、`Stats(endpoint string) ProberStats`、`RecycleMarkers(endpoint string) int`、`Save() ([]byte, error)`、`Reload(data []byte) error`。

要修的地方：入口名为空白报 `ErrBadEndpoint`、重名报 `ErrDuplicateEndpoint`、未注册报 `ErrUnknownEndpoint`；主机名为空白报 `ErrBadHost`，来源不认识报 `ErrBadSource`；直连来源只填 `Host` 头，转发来源只填 `X-Forwarded-Host`，绝对请求行要写成 `GET http://<主机>/ HTTP/1.1`，三种来源各自独立构造、不能互相填充；每个请求带一枚可回收标记，一轮判定内不得重复；重定向目标取前端层的值，生成链接取后端层的值，缓存键固定为 `v1:` 加缓存层的值；某层配置的来源与本次请求来源不一致时，该层必须退回 `app.internal`，不能去猜别的来源；结论要给出具体取值链路与生效层，不能只报存在，同一入口重复判定得到的取值链必须一致；前端取第一个转发主机头而后端取最后一个时，判定必须体现分层差异；污染值只在邮件模板里渲染、页面响应看不到时，要通过触发接口取证；注入值被编码后仍进入链接时，不能判为已经正确转义；三个来源按直连、转发、绝对请求行的固定顺序逐个提交，同一入口串行执行；每入口探测条数由 `MaxProbesPerEndpoint` 硬顶，超出报 `ErrProbeBudget`；保存与重载要跨重启带回策略、记录与计数，损坏快照报 `ErrBadSnapshot` 且不改动现网。

硬约束：同一入口的三种来源必须按固定顺序串行提交，前一条没有收尾不得发下一条；标记一次性使用，回收之后不得复用；保存与重载整体成功或整体拒绝，重载后已记录的顺序与计数不变。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发探测与重启恢复自行构造程序验证；只做本地实现。交付源码、测试记录，以及一页以内的取值链口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-25`
- 实现文件：`host_header_poison_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`HostHeaderPoisonProber`
