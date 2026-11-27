# DHCP 侦听准入与租约绑定表

## 题面

接入层最近连续报障：财务区的终端早上开机拿到地址后访问不了内网，抓包发现同一地址在同一时段还被另一层楼的机器使用；另有一次机房临时接了一台路由器，整栋楼的客户端在几分钟内全部掉线。现场排查指向网关上的 DHCP 侦听逻辑没有把租约绑定和端口信任关系管住；值班同事重启网关后现象暂时消失，第二天又复现，说明绑定状态也没有跨重启保留。落点在模块路径 gsb/batch51-go-safety-b-01、文件 dhcp_snooping_guard.go，类型 DhcpSnoopingGuard，请把这块准入与绑定表补完整。

关键接口保持：NewDhcpSnoopingGuard(cfg SnoopConfig) *DhcpSnoopingGuard、Ingest(port string, trusted bool, msg DhcpMessage) (Verdict, error)、Binding(mac string, vlan int) (Binding, bool)、CheckSource(port, mac string, ip netip.Addr) error、Expire(now time.Time) int、Snapshot() ([]byte, error)、Restore([]byte) error；另外提供 PortBindings(port string) []Binding、Conflicts() []Conflict、Audit() []AuditEvent、Stats() SnoopStats 与 SetPortTrusted(port string, trusted bool)。

要修的行为：一、服务器角色报文 OFFER、ACK、NAK 只允许出现在信任端口，非信任端口出现一律丢弃、记 untrusted_server 审计并累加告警，这种报文不得创建或刷新任何客户端绑定；信任端口上的服务器报文放行并记 trusted_server。二、客户端在非信任端口的 DISCOVER 按 (mac, vlan) 建占位绑定，端口取到达端口，地址留空，判定 allow/client_bound；REQUEST 首次补齐 requested-IP 与 server-id，成功同样判 allow/client_bound，之后两者必须与记录一致，不一致丢弃并记 request_mismatch，原绑定保持不动。三、同一 MAC 换端口请求视为迁移，先让旧端口绑定失效再建新绑定，旧端口流量按未绑定处理，不允许两个端口同时放行。四、DECLINE 撤销绑定、判 allow/declined，并把地址放进冲突隔离，隔离期内同一地址的 OFFER 要拒绝，隔离到期后才允许重新分配。五、未知报文类型丢弃并记 unknown_message，不返回内部错误。六、CheckSource 同时校验端口、MAC 与地址，端口不符报 ErrSourcePort，地址不符报 ErrSourceIP，无绑定报 ErrNoBinding，租约过期报 ErrBindingExpired。七、Expire 清理租约到期的绑定与隔离到期的冲突，返回被清掉的绑定条数，并累加 Expired 计数；丢弃计入 Dropped。八、总绑定数、每端口 MAC 数、审计条数都有上限，溢出按最久未活动淘汰并记 capacity_evict 或 port_limit_evict；审计只保留最新若干条。九、绑定与审计的序号从 1 开始单调递增，两者都以序号反映到达顺序；MAC 统一小写归一，VLAN 参与绑定键；PortBindings 按 MAC 升序返回。十、DISCOVER 占位绑定的地址为空，后续 REQUEST 才写入地址与 server-id，租约截止时间从成功 REQUEST 起按 LeaseTTL 计算。

硬约束：Ingest、Binding、CheckSource、Expire 与快照必须并发安全，同一 (mac, vlan) 的报文按到达序处理；Snapshot 与 Restore 要么看到全量旧状态、要么看到全量新状态，损坏快照报 ErrBadSnapshot 且不能改动现网，绑定、审计与计数跨重启可恢复。

验收：go test ./... 的 15 条必须全过，不得修改或跳过测试、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-01`
- 实现文件：`dhcp_snooping_guard.go`
- 包名：`task`；`tests/` 下的测试同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DhcpSnoopingGuard`
