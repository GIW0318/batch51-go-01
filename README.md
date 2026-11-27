# Vsock 对端准入与信用窗口隔离

## 题面

某私有云用 vsock 做虚拟机与宿主之间的管理通道，接入改造之后出了几档事：一台虚拟机的管理端口被另一台机器连上，对端报上来的 CID 一比才发现本该属于 guest 的号段被宿主进程拿去用了；某个端口连接打满之后，新请求不但被拒，还把已有连接挤掉；握手没完成的连接就能发数据，接收端缓冲区被灌满；运维调整端口允许的 CID 名单，结果在线的长连接被一起踢下线，业务侧看到管理通道集体重连。请把 gsb/batch51-go-safety-d-05 的 vsock_peer_guard.go 里的 VsockPeerGuard 补齐。

对外签名保持：NewVsockPeerGuard(cfg VsockConfig) *VsockPeerGuard、Listen(cid, port uint32, policy PortPolicy) (Listener, error)、Connect(peer PeerAddr, req ConnectRequest) (Conn, error)、Authorize(connID string, cred PeerCred) (Decision, error)、Credit(connID string, bytes uint32) (Window, error)、Grant(connID string, bytes uint32) (Window, error)、Shutdown(connID string, how string) error、ConnState(connID string) (Conn, bool)、Listeners() []Listener、CloseListener(listenerID string) error、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）监听只允许绑在宿主 CID 2 上，别的号报 ErrBadCID；端口为 0 报 ErrBadPort；租户空白、允许 CID 集合为空、端口信用上限不是正数或超过全局上限，都报 ErrBadPolicy；同一端口重复绑定报 ErrPortInUse。

（2）连接请求先校验方向：guest 侧对端的 CID 必须落在 3 及以上且不等于 0xFFFFFFFF，host 侧对端的 CID 只能是 1 或 2，越界一律报 ErrCidMismatch，方向不认识报 ErrBadSide。

（3）目标端口没有监听报 ErrNoListener；对端 CID 不在该端口的允许集合里报 ErrCidDenied。

（4）同一端口的并发连接数有硬顶，连接总量也有硬顶，超限直接报 ErrConnLimit，不排队、也不挤掉已有连接；新连接进入握手中状态，发送与接收信用都是 0，并带一个按端口单调递增的代次。

（5）握手要校验对端凭据：租户必须与端口租户一致，否则报 ErrTenantMismatch；令牌去掉空白后为空报 ErrBadCredential；重复握手报 ErrAlreadyAuthorized；未登记的连接报 ErrConnNotFound。

（6）握手成功后连接转入已建立，凭据标记置真，双向信用各取全局基础额度，裁决带允许标记与租户。

（7）未握手成功就记账报 ErrNotEstablished；记账额度为 0 报 ErrBadCredit。

（8）发送信用不足时整笔拒绝并报 ErrCreditExhausted，已用额度不变，不允许部分扣减；发送与接收两个方向的窗口独立记账。

（9）Grant 由接收方通告额度，累计不得超过端口策略与全局上限中的较小值；超出部分被截断，返回里给出实际增加量并把截断标记置真；已经顶到上限时增加量为 0。

（10）Shutdown 只认 read、write、both，其他报 ErrBadShutdown；关掉写方向之后本端再记账报 ErrShutdown，连接进入半关闭；两个方向都关掉之后连接从表里移除。

（11）CID 复用之后，旧连接的迟到数据不能落到新连接上，代次是唯一的归属凭据，按代次丢弃。

（12）半关闭状态下本端仍要能收对端数据，接收窗口不因为关掉写方向而清零；端口允许的 CID 集合变更只影响之后新建的连接，已有连接保留到关闭。

硬约束两条。其一是并发与原子性：同一端口的连接计数与允许集合检查必须串行化，并发连接不能突破上限，也不能出现已经超限还放行的情况。其二是快照与重启：快照要带出监听表、连接、信用余额与端口代次，恢复后连接状态与剩余额度与快照前一致，握手结果不能被重放一遍，损坏快照报 ErrBadSnapshot 且不得改动现网状态。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-d-05`
- 实现文件：`vsock_peer_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`VsockPeerGuard`
