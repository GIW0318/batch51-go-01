# SMB2 信用授予与租约断开治理

## 题面

文件服务器升级后，共享盘上同时出现三类故障：某条连接一口气发了几十个并发请求，服务端授予的额度被透支，后面的请求全部挂死；一个打开着的表格被另一个客户端强行改写，租约降级通知发出去还没等到确认，服务端已经按旧权限放行；断线重连之后老句柄被别人冒领，重连上来的新连接还继承了上一轮的额度，直接把窗口打满。请把 gsb/batch51-go-safety-b-19 的 smb2_credit_lease_guard.go 里 Smb2CreditLeaseGuard 补好，对外签名保持：NewSmb2CreditLeaseGuard(cfg ServerConfig) *Smb2CreditLeaseGuard、Negotiate(req NegotiateReq) (NegotiateResp, error)、Charge(connID string, cost uint16) (uint16, error)、Refund(connID string, credits uint16) (uint16, error)、Open(connID string, req CreateReq) (FileID, error)、Break(leaseKey string, to LeaseState) (BreakResult, error)、AckBreak(leaseKey string, state LeaseState) error、Reconnect(connID string, fileID FileID) (Handle, error)、Disconnect(connID string) error、LeaseOf(leaseKey string) (LeaseEntry, bool)、Stats() CreditStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）连接号或客户端标识去掉空白后为空分别报 ErrBlankConn、ErrBlankClient，重复协商同一连接报 ErrConnExists，方言与配置不符报 ErrBadDialect。

（2）协商额度取客户端请求、InitialCredits 与 MaxCredits 的最小值，请求为零时给 InitialCredits；Charge 扣减后返回剩余额度，成本为零报 ErrBadCost，超出剩余报 ErrCreditExhausted 并带回剩余额度，不得透支。

（3）Refund 把未消费额度还回窗口，超出窗口大小的部分截断，返回归还后的可用额度；未知连接报 ErrConnUnknown。

（4）Open 必须在已协商的连接上进行，未知连接报 ErrConnUnknown；路径必须是绝对路径，空串与相对路径报 ErrBadPath；租约键已存在时返回同一个文件标识，不新建句柄。

（5）空租约键的打开不产生租约，但句柄仍要登记供重连使用；租约状态超出读、写、句柄三位掩码报 ErrBadLeaseState。

（6）Break 先查租约再校验目标：未知租约报 ErrLeaseNotFound；目标不是当前状态的严格子集报 ErrBadLeaseState；已经在降级中报 ErrBreakPending；成功时返回降级前后的状态并要求确认，修订号递增，租约标记为降级中并记下目标状态。

（7）AckBreak 必须与待确认的目标状态一致，不一致报 ErrBreakMismatch 且不改变租约；没有待确认的降级报 ErrNoBreakPending；确认后状态落到目标值并清除降级标记。

（8）Reconnect 先校验连接、再校验句柄存在、最后校验归属：未知连接报 ErrConnUnknown，未知文件标识报 ErrHandleNotFound，客户端标识与句柄登记的不一致报 ErrHandleOwner；归属正确时把句柄绑到新连接并返回路径与文件标识。

（9）降级确认之前，同一租约键上的新请求要按降级后的目标状态判定，不能继续按旧状态授予读写。

（10）连接断开后未确认的额度要回收，重连的新连接从保守窗口重新起算，不能继承旧额度。

（11）同一文件上多个租约键按共享冲突矩阵裁决，读租约与写租约不得同时授予。

（12）Disconnect 关掉连接，之后的 Charge 报 ErrConnUnknown，重复断开也报 ErrConnUnknown；快照要带连接额度、租约状态与句柄表，恢复后额度、租约与重连都能继续用，损坏快照报 ErrBadSnapshot 且不改动现网。

硬约束两条。其一是并发与原子性：并发 Charge 不得透支窗口，成功次数恰好等于窗口容量；Break、AckBreak 与 Open 并发时同一租约只能有一个状态推进，租约表不得出现半更新。其二是容量与恢复：租约条数受 MaxLeases 硬顶，满了报 ErrLeaseLimit 且不淘汰已有租约；快照保存租约、句柄与额度水位，恢复后重连仍按原客户端标识判定归属。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-19`
- 实现文件：`smb2_credit_lease_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`Smb2CreditLeaseGuard`、`LeaseState`、`NegotiateReq`、`NegotiateResp`、`CreateReq`、`FileID`、`Handle`、`LeaseEntry`、`BreakResult`、`CreditStats`
