# 回环设备租约：后备文件绑定、自动清理语义与分离顺序

## 题面

值班同学凌晨接到两通电话：一台多租户隔离的构建机上所有镜像挂载都卡住了，`losetup -a` 里明明还有空闲设备号，新的挂载请求却一个都拿不到；另一台机器上两个只读挂载共用同一个镜像之后，第三个要写镜像的挂载被拒了，把所有挂载卸掉重来又恢复正常。更麻烦的是进程崩溃重启之后，设备表里残留一批没人引用的设备，谁也不敢手工清，怕把仍在使用的挂载一起摘掉。

落点在 `gsb/batch51-go-safety-d-06` 的 `loop_device_lease_guard.go`，类型 `LoopDeviceLeaseGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-d-06`。

公开接口保持：`NewLoopDeviceLeaseGuard(cfg GuardConfig) *LoopDeviceLeaseGuard`、`Attach(backing string, req AttachRequest) (Lease, error)`、`Detach(leaseID string) error`、`Reconfigure(leaseID string, opts LoopOptions) error`、`List() []LeaseView`、`Lease(leaseID string) (LeaseView, bool)`、`Reconcile(now time.Time) ([]LeaseEvent, error)`、`Backing(path string) (BackingInfo, error)`、`ReplaceBacking(path string) (BackingInfo, error)`、`OpenRef(leaseID string) (int, error)`、`CloseRef(leaseID string) (int, error)`、`SetHolderAlive(holder string, alive bool)`、`Events() []LeaseEvent`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. 持有者或后备文件名为空白时报 `ErrBlankHolder`、`ErrBlankBacking`，一个设备号都不许占。
2. 设备号从 `DeviceBase` 起连续分配，超出 `MaxLeases` 或 `DeviceCount` 报 `ErrNoFreeDevice`，被释放的号要能回到池子里复用。
3. 同一后备文件上的写租约必须独占，第二次写租约报 `ErrBackingBusy`；换一个后备文件照常分配新号。
4. 只读租约可以共用同一台设备，共用之后引用计数按租约累加，`List` 里它们的设备号相同而租约号不同。
5. `Detach` 未知租约报 `ErrUnknownLease`，对已释放的租约再 `Detach` 报 `ErrLeaseReleased`；释放后的租约仍可查询，但不再出现在 `List` 里。
6. 引用计数逐次递减，降到零之后继续 `CloseRef` 报 `ErrBadRefCount`，计数不许变成负数；中途重新打开引用要把计数加回去。
7. `Reconfigure` 只接受 `BlockSizes` 里列出的块大小，其余报 `ErrBadOptions` 且不改动原配置；未知租约报 `ErrUnknownLease`。
8. `Reconcile` 只回收持有者已退出且挂载时间早于 `StaleAfter` 的租约，持有者仍存活或还没过期的租约一律不动也不产生事件，审计事件里带租约号、持有者、设备号与原因。
9. `List` 按设备号升序、同号按租约号升序，已释放的租约不出现在列表里。
10. 后备文件被替换后，原有租约仍绑定替换之前的那份内容，重新挂载要按文件句柄判定，不能只看路径字符串是否相同。
11. 带自动清理语义的设备只有在最后一个引用关闭之后才真正释放，提前 `Detach` 只能标成待释放，设备号不能立刻回到池子。
12. 先有只读租约、再申请写租约时，只读租约全部退出之前写租约不能授予，只能用 `ErrBackingBusy` 回绝。

硬约束：`Attach` 与 `Detach` 必须串行裁决，同一后备文件在同一时刻只允许一路写租约成功；快照与租约变更互斥，读到的要么是完整的旧状态要么是完整的新状态；快照要带设备号、后备文件标识与引用计数，恢复之后新分配的租约号必须大于此前任何一条，损坏快照报 `ErrBadSnapshot` 且不动现有状态。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发挂载与重启恢复自行构造程序验证；只做本地实现。交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-d-06`
- 实现文件：`loop_device_lease_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`LoopDeviceLeaseGuard`
