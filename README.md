# NTLM 协商挑战账本与降级拦截

## 题面

NTLM 协商上线两周，安全组在同一次渗透里复现了三件事：同一段响应被截获后原样重放，网关照样放行；客户端把签名位与扩展会话位悄悄摘掉，响应降级成旧版质询响应，网关也没有拦；反向代理的连接在 keep-alive 之后被另一个账号的请求复用，前一个账号的协商状态被后来的账号接着用，审计里查不出到底是谁完成的认证。三件事都落在同一段三消息往返的协商账本上。落点在 `gsb/batch51-go-safety-a-11` 的 `ntlm_challenge_guard.go`，类型 `NtlmChallengeGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-a-11`。

公开接口保持：`NewNtlmChallengeGuard(cfg GuardConfig) *NtlmChallengeGuard`、`SetPolicy(p Policy) error`、`Policy(account string) Policy`、`Challenge(connID, account string, clientFlags uint32) (Challenge, error)`、`Verify(resp Response) (Result, error)`、`ResetConnection(connID string) int`、`Pending(connID string) int`、`Expire(at time.Time) int`、`Audit(connID string) []AuditEvent`、`Stats() GuardStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：连接号或账号为空、纯空白报 `ErrBadIdentity`，计数不跟着涨；挑战值不少于十六个字符，按连接与账号建索引，两次签发不得复用同一个值，过期时刻等于签发时刻加 `ChallengeTTL`；服务端挑战强制置位签名位与扩展会话位并携带目标信息，客户端回传的标志必须包含这些位，摘掉任意一位报 `ErrDowngradeRefused`；同一挑战值只能兑换一次，成功后再提交报 `ErrChallengeReused`，不能被当成从未见过的值；别的连接签发的挑战值出现在响应里报 `ErrChallengeMismatch`，本连接从未签发过的随机串报 `ErrUnknownChallenge`；超过截止时刻提交报 `ErrChallengeExpired`，时钟回拨也不能让已过期的挑战复活；校验失败不消耗挑战，同一个挑战可以重试到尝试次数用尽；策略要求通道绑定时，响应缺失通道绑定值报 `ErrChannelBindingRequired`；响应不是 NTLMv2 报 `ErrWeakResponse`，完整性校验不过报 `ErrMicInvalid`；连续失败达到 `MaxAttempts` 后账号锁定，换个连接发起协商也报 `ErrAccountLocked`；待处理挑战表超过 `MaxPending` 报 `ErrPendingLimit`，不静默顶掉旧记录；`ResetConnection` 返回清掉的条数，被清掉的挑战不能再兑换，其它连接的待处理项不受影响；审计按连接记录 accepted、rejected、expired 与原因，`Stats` 的签发、通过、拒绝、过期四项与真实调用一致。

还有三处只在现场暴露过、没有写进常规回归的边界：客户端回传的目标信息被替换，但完整性校验与响应本身仍能通过时，必须按通道绑定单独报 `ErrChannelBindingMismatch`，不能只靠完整性校验兜住；同一账号在多条连接上并行协商时，各连接的挑战互不覆盖，一方兑换成功不得让另一方的挑战失效；协商进行到一半连接被复用到另一账号时，必须复位该连接的全部协商状态，旧账号的挑战不能在新账号名下兑换。

硬约束：并发下同一账号的签发与校验必须串行，待处理计数与统计不能出现半更新，签发、校验、重置三路并发时账本自洽；快照要带上待处理挑战、消费记录、锁定状态与序号水位，损坏快照报 `ErrBadSnapshot` 且不改动现网，恢复后新签发的序号必须大于此前任一条；待处理表与每账号尝试次数都按配置硬顶，任何一路超限都不许覆盖已有记录。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发协商与重启恢复自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-11`
- 实现文件：`ntlm_challenge_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`NtlmChallengeGuard`
