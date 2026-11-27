# iSCSI 会话登录协商与命令窗口治理

## 题面

证券客户的集中存储挂在两台虚拟化宿主上，固件升级后备份服务器不定时收到会话被重置，重试几轮才勉强跑完，备份窗口越拖越长；更麻烦的是一次校验和报错之后同一批命令被重复执行，快照卷上多出一份副本。请补齐模块 gsb/batch51-go-safety-b-11 里 iscsi_session_guard_p3.go 的 IscsiSessionGuardP3，把 target 侧的登录协商、会话与连接状态以及命令窗口记账做对。

对外签名保持：NewIscsiSessionGuardP3(limits Limits) *IscsiSessionGuardP3、Login(ctx context.Context, req LoginRequest) (LoginResult, error)、Execute(sessionID string, cmd Command) (Status, error)、TaskManage(sessionID string, refCmdSN uint32, fn TaskFn) error、Ack(sessionID string, expCmdSN uint32) error、DigestFailed(connID string, cmdSN uint32) error、ConnectionLost(connID, reason string) error、Reconnect(sessionID, connID string) (ConnInfo, error)、Session(id string) (SessionInfo, bool)、Response(sessionID string, itt uint32) (Response, bool)、Attempts(sessionID string, itt uint32) int、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的故障与边界：

（1）发起方、目标名、门户地址任一为空，或者连接号列表为空、存在重复连接号，Login 报 ErrBadLogin，且不占用会话名额。

（2）登录参数里 AuthMethod 只接受 None 与 CHAP，其它取值或存在空白值报 ErrLoginRejected；登录成功返回的 TextKeys 按名字升序，首个 CmdSN 为 1。

（3）同一目标的会话数超过 MaxSessions 报 ErrSessionLimit；单会话连接数超过 MaxConnsPerSession 按 ErrBadLogin 拒绝。

（4）常规命令按会话单调分配 CmdSN，从 1 起步长 1；未确认条数达到 MaxCmdWindow 时报 ErrWindowExhausted，被拒绝的命令不占号也不产生响应。

（5）Ack 携带的 ExpCmdSN 只能落在当前 ExpCmdSN 与 NextCmdSN 之间，越界报 ErrCmdSNInvalid；确认后腾出的号码继续往后发，绝不回收已经用过的 CmdSN。

（6）摘要校验失败时只丢弃当前 PDU：该 CmdSN 的响应要撤掉、连接上的 StatSN 水位回退，窗口不推进，同一 ITT 可以按原号重发。

（7）立即命令不进入命令窗口、不消耗 CmdSN，会话里单独计数，响应标记为立即命令且不带 CmdSN；但它的响应要插在该连接当前响应流的正确位置，不能打乱后面常规命令的 StatSN 顺序。

（8）命令的 ITT 为 0、连接不属于该会话或已标记丢失时报错；全部连接丢失后该会话不能再执行命令。连接重建后 StatSN 从零重新计数而 CmdSN 继续，恢复与快照都必须按连接分别重算，不能几路连接共用一套计数。

（9）TaskManage 在给定参考 CmdSN 之后插入任务，回调收到的分配号紧接当前 NextCmdSN，并按提交顺序串行触发；参考号超过已发行水位、为 0 或回调为空时拒绝且不分配号码。

（10）同一 ITT 在首个结果尚未产生之前再次提交，按等待处理，既不报错也不能执行第二次，结果可被重复取回。

（11）两个会话的 CmdSN 窗口、响应表和 ITT 记录彼此独立，一个会话的命令不影响另一个会话的水位。

（12）快照要带出会话、连接、响应与 ITT 记录；恢复后 NextCmdSN、ExpCmdSN、各连接 StatSN 与恢复前一致，后续命令接着原水位往后发；损坏的快照报 ErrBadSnapshot 且不改动现网状态。

硬约束有两类。其一是并发与原子性：同一 ITT 被多路并发提交时只能真正执行一次，Attempts 恒为 1；窗口、响应表与 ITT 记录在同一把锁里推进，不能出现半更新的水位。其二是容量与恢复：会话数、连接数和窗口深度都是硬上限，满了就拒绝而不是覆盖旧条目；快照与恢复整份成功或整份失败。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-11`
- 实现文件：`iscsi_session_guard_p3.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`IscsiSessionGuardP3`
