# OpenFlow 1.3 流表事务与角色选举

## 题面

园区网割接那天，控制器把一批流表改动推到两台接入交换机，其中一台只生效了一半：新的引流规则上去了，配套的计量表没上，第二天早高峰整层楼断网；另外割接脚本里有一段切换控制权，切完之后旧主控还在写，两台设备互相覆盖配置。请补齐模块 gsb/batch51-go-safety-b-13 里 openflow_bundle_guard.go 的 OpenflowBundleGuard，把 bundle 原子提交、barrier 顺序栅栏与控制器角色选举做对。

对外签名保持：NewOpenflowBundleGuard(limits Limits) *OpenflowBundleGuard、OpenBundle(dpid string, req BundleReq) (BundleID, error)、Add(bundleID string, msg OfMsg) error、Commit(bundleID string, props CommitProps) (CommitResult, error)、Barrier(dpid string) (BarrierTicket, error)、BarrierReply(dpid string, xid uint32) error、BarrierState(dpid string) (BarrierTicket, bool)、SetRole(dpid string, role Role, gen uint64) (RoleState, error)、Reconnect(dpid string) (int, error)、BundleState(bundleID string) (Bundle, bool)、FlowTable(dpid string) []OfMsg、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的故障与边界：

（1）dpid 去空白后为空报 ErrBadDpid；一个 dpid 首次 OpenBundle 即登记，默认角色为主控、代次 0。

（2）Add 只接受 flow_mod、group_mod、meter_mod 三类消息，其它类型或 xid 为 0 报 ErrBadMessage；往已提交或不存在的 bundle 追加报 ErrUnknownBundle。

（3）bundle 要么整份生效要么整份回滚：提交时先做引用校验，bundle 内引用了尚未创建且本包也没有创建的 group 时整包拒绝，不能先落地一部分再靠后续消息补齐。

（4）空 bundle 提交报 ErrEmptyBundle，且不改动流表；提交成功后 Applied 等于消息条数，流表按 Add 顺序可见。

（5）每落地一条消息就挂一个待回复 xid；CommitResult.Barrier 在 props.Barrier 为真时给出本次留下的未回复条数，为假时为 0。

（6）Barrier 返回当前未回复条数与角色；BarrierState 的 Pending 与 Done 按实时未回复集合计算，不能返回开票那一刻的旧值。

（7）同一 xid 的重复回复只能被消费一次，重复到达不能再推进任何完成计数；未登记的 dpid 上报 barrier 或回复都报 ErrUnknownDpid。

（8）SetRole 的代次必须严格大于当前值，相等或回退报 ErrStaleGeneration；角色取值只允许 equal、master、slave，其它报 ErrRoleDenied。

（9）角色切到 slave 之后，开启新 bundle、追加消息、提交一律按 ErrNotMaster 拒绝且不改动流表；仍在途的 barrier 回复按原样返回，Barrier 本身不拒绝。

（10）同一 dpid 的并发提交串行化，既不丢消息也不打乱各 bundle 内部顺序；不同 dpid 之间互不阻塞。

（11）交换机重连后未完成的 barrier 要重新对齐：Reconnect 返回需要重发的栅栏条数，这些消息仍记为未回复，不能悄悄丢掉。

（12）快照保留 bundle 内容与状态、角色代次、流表与未回复集合；恢复后代次不能被重新打开，未回复项继续可被回复清空；损坏的快照报 ErrBadSnapshot 且不改动现网状态。

硬约束有两类。其一是并发与原子性：同一 dpid 的提交与角色变更互斥，提交要么整份可见要么完全不可见，读者永远看不到半个 bundle。其二是容量与恢复：bundle 数、单包消息数与待回复条数都是硬上限，超出报错而不是淘汰在用条目；快照与恢复整份成功或整份失败。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重连场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-13`
- 实现文件：`openflow_bundle_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`OpenflowBundleGuard`
