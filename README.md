# OSPF 邻接状态机与链路状态库准入

## 题面

分支机构的出口路由器每隔几天就抖一次路由：已经撤销的旧链路会重新出现在链路状态库里，邻居长时间停在 Init 状态，省界的区域边界路由器还把骨干区域的摘要灌回分支，访问总部的流量总绕一大圈。运维重启进程只能好一阵，旧 LSA 换个序列号又回来，现场怀疑邻接准入和序列号裁决都没有守住；监控面板上的丢弃计数一直是零，值班只能靠抓包猜哪些报文被拒，定位非常吃力。落点在模块路径 gsb/batch51-go-safety-b-02、文件 ospf_lsa_guard.go，类型 OspfLsaGuard，请把邻接状态机、LSA 准入与泛洪队列补完整。

关键接口保持：NewOspfLsaGuard(cfg AreaPolicy) *OspfLsaGuard、Hello(iface string, pkt HelloPacket) (NeighborState, error)、RecvLSA(area string, lsa LSA) (Verdict, error)、Age(now time.Time) []Expired、Flood(area string, lsa LSA) error、Snapshot() ([]byte, error)、Restore([]byte) error；另外提供 LSAKey(lsa LSA) string、Neighbor(iface, routerID string) (NeighborState, bool)、Neighbors(area string) []NeighborState、LSDB(area string) []LSA、FloodQueue(area string) []LSA、Purge(area, key string) error 与 Stats() OspfStats。

要修的行为：一、Hello 校验区域 ID、认证字段与 hello/dead 间隔，任何一项不符分别报 ErrAreaMismatch、ErrAuthFailed、ErrIntervalMismatch，并且不得改动已有邻居记录。二、首次合法 Hello 进 Init；第二次 Hello 的邻居列表含本地 RouterID 时进 TwoWay 并置 TwoWay 标志；第三次再进 Full，邻居按 (iface, routerID) 唯一，单区域邻居数超限报 ErrNeighborLimit。三、LSA 按 (Type, LinkStateID, AdvertisingRouter) 唯一；新键判 accept/new_lsa 并落库；同键更高序列号判 accept/newer_lsa 并覆盖；更低序列号判 reject/stale_lsa 且数据库不动；同序列号但正文或校验和不同判 reject/seq_conflict 且数据库不动；同序列号内容相同按 refresh_lsa 幂等接受。四、序列号按有符号 32 位比较，从 0x7FFFFFFF 回绕到 0x80000001 时仍要视为更新，不能直接按无符号大小判断。五、自生成 LSA 被邻居以更高序列号刷新时，要触发本地重新生成并累加 Regenerated，不能照收对方内容。六、到达 MaxAge 的 LSA 保留在库中并参与泛洪抑制，老化扫描真正删除之前仍要能回答邻居的数据库描述请求。七、Age 以 LSA 收到时刻加上经过时间判断 MaxAge，删除到期条目，返回值为 ReasonMaxAgeLSA 的 Expired 列表，并累加 Purged。八、每个区域的 LSDB 有上限，满时先淘汰优先级最低的条目再接收更高优先级来件；来件优先级不高于被淘汰者时判 reject/lsdb_limit。九、Flood 只入队不改库，队列满时更高优先级可以替换最低者，否则报 ErrFloodQueueFull，成功入队累加 Floods。十、Flood 的区域与配置不符报 ErrAreaMismatch；Purge 不存在的键报 ErrNoLSA。十一、Neighbors、LSDB 与 FloodQueue 返回副本并按稳定顺序排列，调用方改动返回值不得影响内部状态。

硬约束：Hello、RecvLSA、Age、Flood 与快照必须并发安全，同一个 LSA 键的接收要串行裁决；Snapshot 与 Restore 要么看到全量旧状态、要么看到全量新状态，损坏快照报 ErrBadSnapshot 且不能改动现网，邻居、LSDB、泛洪队列与计数跨重启可恢复。

验收：go test ./... 的 15 条必须全过，不得修改或跳过测试、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-02`
- 实现文件：`ospf_lsa_guard.go`
- 包名：`task`；`tests/` 下的测试同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`OspfLsaGuard`
