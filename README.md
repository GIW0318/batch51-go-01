# OSPFv2 邻居与链路状态库治理

## 题面

区域内的路由反复震荡，值班同事整理出几张单子：一台接入交换机重启后，它发出的链路状态通告在库里留了两份实例，一份还参与算路；同一条通告相邻两次更新只差一个序列号，某次最早的旧实例被当成新实例覆盖回来；还有邻居从完全邻接掉下去以后，它产生的通告要等一小时老化才消失，这段时间流量一直往死路上导。请把 gsb/batch51-go-safety-b-20 的 ospf_lsa_guard_p3.go 里 OspfLsaGuardP3 补好，对外签名保持：NewOspfLsaGuardP3(cfg AreaConfig) *OspfLsaGuardP3、AddInterface(iface string, cfg IfaceConfig) error、Hello(iface string, pkt HelloPacket) (NeighborState, error)、RecvLSA(area string, lsa LSA) (LSAStatus, error)、Flood(area string, lsa LSA) ([]string, error)、Age(now time.Time) ([]LSAEvent, error)、ElectDR(area, iface string) (DRResult, error)、LSDB(area string) []LSA、SPFInput(area string) []LSA、Neighbors(iface string) []NeighborState、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）接口名去掉空白后为空报 ErrBlankInterface，本机 Router ID 为空报 ErrBlankRouterID，区域未配置报 ErrAreaNotFound，重复注册报 ErrInterfaceExists。

（2）Hello 必须带 Router ID 与非零 DeadInterval，否则报 ErrBadHello；未知接口报 ErrInterfaceNotFound；对端邻居列表里出现本机 Router ID 时状态为 full，没有出现时为 init。

（3）NeighborState 的 DeadInSec 是距邻居失效的剩余秒数，刚收到 Hello 时等于报文里的 DeadInterval；Age 超过这个窗口就把邻居置为失效并产出 neighbor_down 事件，事件填接口名与邻居 Router ID、key 留空。

（4）LSA 按类型、LinkStateID 与广告路由器三元组唯一，Key 用它们以竖线拼成；首次安装返回 installed，序列号更大返回 newer 并替换，更小返回 older 且不动库，序列号相同则比校验和，大者胜、相等返回 duplicate 且不覆盖正文。

（5）类型不在 1 到 5 之间报 ErrBadLSAType，序列号为 0 或保留值 0x80000000 报 ErrBadSequence，被拒的通告不得入库。

（6）序列号初值是 0x80000001；Age 每推进一秒就给库里的通告加一秒，累加到 3600 秒时从库里移除并产出 lsa_max_age 事件，事件填区域与 key；未到 3600 的在库里原地变老。

（7）SPFInput 只返回年龄未到 3600 的通告，LSDB 返回按 Key 升序的全量副本。

（8）Flood 返回该区域内邻居处于 full 的接口名并按名升序排列；区域不存在报 ErrAreaNotFound，通告未入库报 ErrLSAUnknown。

（9）邻居失效时要连带清掉它作为广告路由器产生的通告，不能等老化，清理结果必须落到 LSDB 而不只是计数。

（10）序列号涨到 0x7FFFFFFF 之后不能直接回绕，必须先泛洪一条 MaxAge 通告再以初始序列号重建，重建完成前旧实例不得参与算路。

（11）DR 选举把本机与邻接状态为 two_way 或 full 的邻居一起作为候选，优先级为 0 的不参选，优先级最高者当 DR、次高者当 BDR，没有候选时两者留空；优先级相同时按 Router ID 决胜；非 DR 与 BDR 的邻居增删不应触发重新选举。

（12）收到比自己更新的通告要先安装再泛洪，接收或泛洪失败不能留下只更新了一半的数据库。

硬约束两条。其一是并发与原子性：同一三元组的并发更新只能有一个赢家，最终库里只留最高序列号那一条；安装与泛洪在同一临界区完成，LSDB 不得出现半更新。其二是容量与恢复：快照要带出邻居状态、各区域库内容、序列号水位与老化时钟，恢复后继续按原时钟老化、邻居仍按原剩余时间失效，损坏快照报 ErrBadSnapshot 且不改动现网。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-20`
- 实现文件：`ospf_lsa_guard_p3.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`OspfLsaGuardP3`、`AreaConfig`、`IfaceConfig`、`HelloPacket`、`NeighborState`、`LSA`、`LSAEvent`、`DRResult`
