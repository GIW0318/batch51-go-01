# 块设备引导区写保护与恢复

## 题面

凌晨批量刷固件时，一台机器的引导区被主机代理直接覆盖，重启后进不了系统；运维翻出现场，只有一份被改坏的主引导记录，GPT 备份头也被顺带写过，分区表跟着读不出来。值班同事手工 dd 恢复了三个小时才把盘救回来，事后审计又发现当时那段写入既没有留证据，也没有任何授权记录，谁申请、写的是哪一段、票据有没有用过，全都对不上。这台机器还赶上过一次在线扩容，扩容之后备份 GPT 被写到了旧位置，巡检脚本报出来的布局指纹和登记时完全两样。这个场景要在代理侧彻底堵住。落点在 `gsb/batch51-go-safety-c-01` 的 `boot_region_guard.go`，类型 `BootRegionGuard`，模块路径 `gsb/batch51-go-safety-c-01`，包名 `task`，把这套写保护、授权与恢复链路补全。

公开接口保持：`NewBootRegionGuard(cfg GuardConfig) *BootRegionGuard`、`RegisterDevice(dev string, image []byte) error`、`Inspect(dev string) (Layout, error)`、`Authorize(dev string, req WriteRequest) (Decision, error)`、`Write(dev string, off int64, data []byte) error`、`Read(dev string, off int64, n int) ([]byte, error)`、`RestoreRegion(dev string, snap SnapshotID) error`、`Repair(dev string) (RepairReport, error)`、`Evidence(dev string) []Evidence`、`Tickets(dev string) []TicketRecord`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。配置里的 `SectorSize`、`ReserveBytes`、`MaxTickets`、`MaxEvidence`、`MaxWriteQueue` 都要生效，缺省按 512 字节扇区与前 1MiB 预留处理。

要修的地方：设备没登记就查布局报 `ErrUnknownDevice`，设备名为空报 `ErrBadDevice`；镜像不足两个扇区或主引导记录结尾不是 `0x55AA` 都要拒绝登记，分别报 `ErrDeviceTooSmall` 与 `ErrBadMBR`；首次接管要把 MBR、GPT 主头、主分区表与备份区一起算进保护区，按扇区对齐、按起始偏移升序给出且段间不重叠；写前 1MiB 或主头、主表、备分区一律报 `ErrProtectedWrite`，主头永远不接受授权，只有分区表项变更能用票据放行；授权没写原因报 `ErrBadReason`，在途票据超过 `MaxTickets` 报 `ErrTicketLimit`；票据只覆盖申请时的偏移与长度，偏了报 `ErrTicketMismatch`，已消费的票据再写报 `ErrTicketUsed`；撤销后未用票据全部作废，再用报 `ErrApprovalRequired`；数据区写入不需要票据，写完 `Read` 要读回同样字节，越界写入报 `ErrOutOfRange`；主 GPT 头 CRC 损坏而备份头完好时要用备份恢复，不能把设备判成不可用；跨保护区与数据区边界的写要整笔拒绝，不能截掉受保护部分只写剩下的；设备容量变化后备份 GPT 位置要重算，旧指纹不能继续用；主备头都不可用时 `Inspect` 要标出半写，证据副本按编号整段写回、份数超过 `MaxEvidence` 淘汰最旧，损坏快照报 `ErrBadSnapshot` 且不动现网。

硬约束：同一设备的授权、写入与恢复必须串行，票据一次性，消费与落盘原子，两路写入不能同时改动同一段保护区；快照要能跨重启带回布局指纹、证据副本、票据水位与拒写计数，恢复后已用票据不得复活。保护区段数、证据快照份数与单设备写队列都按配置封顶，队列满要当场拒绝而不是覆盖旧请求。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发写入与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-01`
- 实现文件：`boot_region_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`BootRegionGuard`
