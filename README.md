# Kerberos AS 交换的预认证协商、时钟偏移与重放治理

## 题面

上周三早上，财务共享平台的域账号集中续签，KDC 前置代理在二十分钟里退回了四百多次 AS-REQ。运维看到的现象很别扭：客户端第一次请求能拿到加密类型列表和盐，紧接着带 PA-ENC-TIMESTAMP 的第二次请求却被判成时间偏差，重试几次后账号直接锁死；另有几台时钟正常的机器，网络重传的同一条请求第二次也被记成失败，锁号范围越滚越大。反向排查时还发现，同一账号走 FAST 通道时换了一条 armor，代理仍按上一次的绑定结论放行。值班同学把代理日志和 KDC 侧审计逐条对完，判断问题出在前置这一层的待决会话、偏移判定与重放记账上，而且是几处一起发作。监控面板上待决会话曲线一度顶到上限，之后所有新登录都在排队。落点 gsb/batch51-go-safety-a-06 的 as_preauth_guard.go，类型 AsPreauthGuard，包名 task。

接口保持：NewAsPreauthGuard(cfg RealmPolicy) *AsPreauthGuard、Begin(req AsRequest) (AsReply, error)、Preauth(sessionID string, pa PreauthData) (AsReply, error)、CheckReplay(client, realm string, ts time.Time, etype int) error、Policy(tenant string) Policy、Pending(sessionID string) (AsRequest, bool)、Failures(client string) int、Stats(tenant string) TicketStats、ReplaySize() int、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：一、Begin 不带预认证，必须回 continue 状态与 preauth_required 原因，ETypes 按强度从高到低给出，盐按 realm/client 拼接且可复算；二、账号、领域、租户任一为空按 ErrBadRequest 拒绝，且不留待决会话；三、每账号待决会话不得超过 MaxPending，超出报 ErrPendingLimit，其它账号不受牵连；四、Preauth 成功要回 issued、选定的 etype 与递增序号，校验通过的四元组写入重放缓存，会话从待决表摘掉且只能用一次；五、失败次数按账号累计，etype 不合法与偏移越界各计一次，命中重放不计，达到 MaxFailures 后连 Begin 一起锁死，回 ErrTooManyFailures；六、etype 不在允许集合报 ErrUnsupportedEType，低于 MinEType 报 ErrWeakEType，只拒绝不降级，也不消耗待决会话；七、时间戳落在偏移窗口外时，未来方向与过去方向必须给出不同原因码，不能合并成一条，此类拒绝按失败计数并保留会话；八、同一条请求因网络重传到达两次，第二次命中重放缓存拒绝，不能记成两次失败；九、FAST 请求换了 armor 通道但内部 client 名一致时，必须重新核对绑定，不得复用上一次结论；十、CheckReplay 的判定与写入要原子完成，容量满时回 ErrReplayCacheFull，不淘汰旧条目也不截断；十一、Policy 返回该租户的只读视图，改动返回值不得影响内部配置，非本租户查询给零值；十二、快照要带失败计数、重放条目与序号水位，恢复后新会话序号必须大于恢复前任一会话。

硬约束两条：重放判定与插入在同一临界区内完成，同账号并发请求按到达顺序计数，锁号判定不允许两个请求同时越过上限；重放条目数、每账号待决会话数与累计失败数设硬顶，越界一律报错而不是静默丢弃。快照与恢复要么全量生效要么完全不动，损坏载荷回 ErrBadSnapshot 且现网状态不变。

验收：在模块目录执行 go test ./...，15 条必须全部通过；不得改动测试、跳过用例或按固定输入写死；并发与重启恢复场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-06`
- 实现文件：`as_preauth_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AsPreauthGuard`
