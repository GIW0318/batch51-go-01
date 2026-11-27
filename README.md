# SMB2 方言协商与 preauth 完整性链

## 题面

文件网关上线新的兼容层后，安全审计连续两天报出异常：一台只支持旧方言的客户端接进来后，原本强制开启的签名悄悄消失，抓包还能看到会话建立过程中有人重放了 SESSION_SETUP；财务共享盘上同时出现了指向父目录的 TreeConnect，差点读到隔壁部门的目录。值班重启网关只能恢复一会儿，问题客户端一重连又出现。落点在模块路径 gsb/batch51-go-safety-b-03、文件 smb2_session_guard.go，类型 Smb2SessionGuard，请把协商、会话建立与路径授权补完整。

关键接口保持：NewSmb2SessionGuard(cfg Smb2Policy) *Smb2SessionGuard、Negotiate(req NegotiateRequest) (NegotiateResponse, error)、SessionSetup(sessionID string, req SessionSetupRequest) (Session, error)、TreeConnect(sessionID, path string) (Tree, error)、VerifySigned(msg Smb2Message) error、Snapshot() ([]byte, error)、Restore([]byte) error；另外提供 Sign(sessionID string, messageID uint64, payload string) (string, error)、CloseSession(sessionID string) error、Session(sessionID string) (Session, bool)、Trees(sessionID string) []Tree、PreauthDigest(sessionID string) (string, error) 与 Stats() Smb2Stats。

要修的行为：一、方言只能从服务端支持集合与客户端列表的交集里选最高的共同项；没有共同项报 ErrNoCommonDialect；策略或客户端要求签名时，选中的方言必须支持签名，不得回落到更旧的方言。二、Negotiate 要开出一个 pending 会话并返回非空 SessionID 与初始 preauth 哈希；未认证会话表有上限，超出报 ErrPendingLimit。三、SessionSetup 对未知会话报 ErrUnknownSession；用户名或安全 blob 为空报 ErrBadCredentials；成功后会话转 active，序号为正。四、preauth 完整性哈希链要把 NEGOTIATE 的请求响应与随后每一轮 SESSION_SETUP 的请求响应按发生顺序依次串入，只追加不重算；链断或消息重放要终止会话；失败的 SESSION_SETUP 也要继续追加，后续成功消息必须能对上前面的失败记录。五、并发活跃会话数有上限，超出报 ErrSessionLimit；同一连接的消息处理必须串行。六、TreeConnect 只允许 active 会话，路径先把反斜杠归一并折叠重复分隔符，共享名取第二段，例如 \\fileserver\data\docs 归一成 //fileserver/data/docs、Share 填 data；共享名必须在 AllowedShares 内。七、路径中出现 .. 段、冒号备用数据流、或 CON、PRN、AUX、NUL、COM1、LPT1 这类保留名，整条报 ErrBadPath。八、每会话 TreeConnect 数有上限，超出报 ErrTreeLimit；重复连接同一归一路径要幂等返回已有 Tree，不重复计数。九、Sign 按会话当前 preauth 摘要、消息号与负载生成签名，VerifySigned 校验不符报 ErrSignatureFailed；签名开启后收到未签名请求要断开整条连接并关闭其全部 Tree，不能只拒这一条消息。十、CloseSession 关闭会话并释放全部 Tree，已关闭会话再操作报 ErrSessionClosed。十一、会话、Tree 与哈希链要能整体快照恢复，损坏快照报 ErrBadSnapshot 且不改动现网；各项拒绝与哈希链追加都要计数。

硬约束：同一条连接上的协商、会话建立与 TreeConnect 必须串行裁决，哈希链只追加；快照与恢复要么看到全量旧状态、要么看到全量新状态，会话、Tree、preauth 摘要与计数跨重启可恢复。

验收：go test ./... 的 15 条必须全过，不得修改或跳过测试、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-03`
- 实现文件：`smb2_session_guard.go`
- 包名：`task`；`tests/` 下的测试同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`Smb2SessionGuard`
