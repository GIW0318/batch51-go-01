# 有状态防火墙的连接跟踪与逐流放行

## 题面

机房出口节点的防火墙在高峰期把一批已经建立的数据库连接误判成新连接，业务端成片超时；同一时段监控显示跟踪表被短命 UDP 会话塞满，真正的长连接反而被挤掉。落点 gsb/batch51-go-safety-b-07 的 conntrack_policy_gate.go，类型 ConntrackPolicyGate，签名保持：NewConntrackPolicyGate(cfg GateConfig) *ConntrackPolicyGate、Admit(pkt Packet) (Verdict, error)、Lookup(flow FlowKey) (ConnState, bool)、Expire(now time.Time) int、Reload(rules []Rule) error、Delete(flow FlowKey) bool、FlowCount() int、Rules() []Rule、Stats() Stats、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的故障与边界：

一、首包必须是不带 ACK 的裸 SYN 才能建表，只带 ACK、SYN+ACK、FIN 的首包一律按 reason_no_syn 丢弃且不建表；建表按规则集顺序取首个命中，动作 allow 放行、deny 丢弃，一条都不命中按默认拒绝 reason_no_rule。

二、命中放行的首个 SYN 建表为 new，回程 SYN+ACK 与客户端 ACK 迁到 established，迁移这一步的原因码用 reason_handshake；之后同向或反向的数据包都按 established 放行，原因码 reason_established，建表与握手包同样计入包数与字节数。

三、反向包按反向五元组命中同一表项，不新建条目；new 状态收到与握手无关的包按 reason_bad_state 丢弃，且不得改动表项里的包数与状态。

四、FIN 或 RST 把表项迁到 closed，原因码 reason_closed，closed 之后只再接受 RST；到期由 Expire(now) 按协议超时清理并返回清理条数，TCP 的超时明显长于 UDP 与 ICMP，未到期的表项不得被清掉。

五、UDP 首包按规则判定并建 new 伪状态，任一方向的第二个包起迁到 established，迁移原因码同样用 reason_established；ICMP echo 类报文按自身五元组建轻量表项，错误类报文只能靠内嵌五元组关联到已有流，关联不上按 reason_related_flow 丢弃，且不得凭内嵌五元组新建表项。

六、表满时先淘汰最久未活动的非 established 表项再插入；一条可淘汰的都没有时，新流按 reason_table_full 丢弃，已有表项不动。

七、每源流数超过 MaxFlowsPerSource 报 reason_source_limit，每流包数超过 MaxPacketsPerFlow 报 reason_flow_quota，两种超限都不得改动计数。

八、Reload 整代替换规则并把代次加一，首次加载为第 1 代，新流判定带新代次；规则里出现非法网段、未知动作或未知协议报 ErrBadRule，整代保持原样、代次不动。

九、在途连接按进入时的代次继续跑完，重载默认不因为新规则默认拒绝而立刻断开；只有 ReEvaluateOnReload 打开时才按新规则重评存量表项。

十、Stats 汇总放行数、丢弃数、过期数、淘汰数与当前表项数；Lookup 只按精确五元组命中，Delete 命中返回 true、重复删除返回 false。

十一、快照覆盖表项、规则、代次与计数器，Restore 要么全量生效要么全量不动，坏载荷报 ErrBadSnapshot，失败不得影响现网表项。

十二、同一流并发到达时只允许存在一个表项，重复到达的首个 SYN 按已有连接处理而不是新建；表项插入、状态迁移与淘汰必须是一个原子动作。

硬约束：Admit、Reload、Expire 与快照可以并发调用，同流的并发包按进入顺序串行裁决，不得出现两条表项、状态回退或计数丢失；表大小、每源流数与每流包数都有硬上限，溢出只能按第六、七条处理，不允许静默扩容。

验收：go test ./... 的 15 条用例全部通过，不得修改测试、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现，不接外部依赖。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-07`
- 实现文件：`conntrack_policy_gate.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ConntrackPolicyGate`
