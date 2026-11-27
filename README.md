# NTLM 协商握手的序列绑定与完整性裁决

## 题面

接入 Windows 域单点登录的兼容网关升级后，运维同时收到三张单：财务部几台终端早上集体登不进来，日志里同一个域账号在两分钟内冒出几十条协商记录；一台换了网段的终端仍然用旧会话进了后台，安全组怀疑凭据被中继；还有零星的登录显示成功，下游却拿不到账号，权限查询直接报找不到人。请把这个握手裁决组件补齐，模块路径 gsb/batch51-go-safety-a-01，实现文件 ntlm_exchange_guard.go，类型 NtlmExchangeGuard。

对外签名保持：NewNtlmExchangeGuard(cfg GuardConfig) *NtlmExchangeGuard、Begin(connID string, src Endpoint) (ChallengeMsg, error)、Accept(connID string, msg AuthenticateMsg) (Principal, error)、Abort(connID, reason string) error、BindChannel(connID string, cbt []byte) error、ChallengeFor(connID string) (ChallengeMsg, bool)、Pending() int、Stats() GuardStats、Audit(connID string) []AuditEvent、ReputationFor(addr string) int、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）连接号去掉首尾空白后为空，Begin 必须报 ErrEmptyConnection，且不占任何待决名额。

（2）一条连接只允许一次协商，同一连接第二次 Begin 报 ErrHandshakeReused，原有 challenge 不被替换。

（3）challenge 必须是 16 字节随机数的三十二位小写十六进制串，返回体里带上连接号与源地址，并且只能被消费一次。

（4）challenge 标志必须同时携带 Unicode、NTLM 与扩展会话安全三位；缺少扩展会话位，或者带上 LMKey、OEM 位，一律按协商降级拒绝，报 ErrDowngradeRefused。

（5）只接受 NTLMv2，版本号不是 2 报 ErrWeakResponse，同时给该来源记一次失败积分。

（6）用户名为空或消息带匿名标志报 ErrAnonymousSession。

（7）响应串短于 32 个字符报 ErrShortResponse，不允许截断后继续校验。

（8）消息引用的 challenge 不属于本连接时，一律按中继拒绝并单独计数，不能只看 nonce 是否还存在。

（9）MIC 取服务端 nonce、客户端 nonce 与用户名用竖线拼接后的 sha256 小写十六进制，与消息里的值不等报 ErrMicMismatch；校验失败要整条连接作废并清掉已经解出的身份，不允许降级成只看用户名放行。

（10）BindChannel 登记的是原始绑定值，Accept 用 sha256 比对消息携带值，不一致报 ErrChannelMismatch；先前没登记绑定值就报 ErrChannelBindingsGone；协商阶段声明支持通道绑定、认证消息却把绑定值省掉时按策略拒绝，不能默认放行。

（11）成功时返回账号、域、来源地址与本次审计序号，连接转入已消费状态，同一连接再 Accept 报 ErrHandshakeConsumed。

（12）Abort 对已存在的连接重复调用必须幂等成功，未知连接报 ErrHandshakeNotFound，被 Abort 的连接再 Accept 报 ErrHandshakeAborted。

硬约束有两类。其一是并发与原子性：同一连接的 Accept 并发进来只能有一个推进成功，challenge 分配、待决计数与审计序号必须原子；失败积分按账号与来源两个维度滑动汇总，来源积分到达上限后新的 Begin 直接报 ErrSourceBlocked。其二是容量与恢复：待决握手数、每源并发连接数与 challenge 表都有硬上限，满了要淘汰最旧的握手并让它的 Accept 拿到 ErrHandshakeLost；快照要带出 challenge 表、来源积分与审计序号，恢复后序号接着涨，损坏的快照报 ErrBadSnapshot 且不得改动现网状态。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-01`
- 实现文件：`ntlm_exchange_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`NtlmExchangeGuard`
