# SR-IOV 虚拟功能分配与信任位边界

## 题面

某云平台把网卡的 SR-IOV 直通能力开放给租户之后，运维一周内收到四类投诉：租户把虚拟机迁到另一台宿主机，原宿主机上的虚拟功能仍显示已分配，新宿主机却报没有空闲虚拟功能；同一块网卡上两台虚拟机的 MAC 撞车，交换机端口跟着学错；租户给虚拟功能加了 VLAN，改回去再重启，VLAN 全丢；还有一台宿主机缩减虚拟功能数量后，正在跑的虚拟机网络中断，宿主机自己也卡在复位流程里。请把 gsb/batch51-go-safety-d-01 的 sriov_vf_allocator.go 里的 SriovVfAllocator 补齐。

对外签名保持：NewSriovVfAllocator(cfg AllocatorConfig) *SriovVfAllocator、Enable(pf string, count int) ([]VF, error)、Allocate(pf string, req VFRequest) (VFLease, error)、Configure(vfID string, cfg VFConfig) (VFState, error)、SetTrust(vfID string, trust bool) error、Release(vfID string) error、LeaseOf(vfID string) (VFLease, bool)、FreeCount(pf string) int、AttachGuest(vfID, guestID string) (VFState, error)、CompleteReclaim(vfID string) (VFState, error)、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）物理网卡名去掉首尾空白后为空，或者数量不在 1 到 MaxVfsPerPF 之间，Enable 分别报 ErrBadRequest 与 ErrBadCount，不建立任何记录。

（2）虚拟功能按 0 递增编号，编号拼成 <pf>:vf<索引>；同数量重复 Enable 必须幂等，已有编号与代次都不变；数量增大只追加新编号，不重建已有记录。

（3）分配永远取当前最小的空闲编号；租约里带租户、MAC、VLAN 列表、信任位与代次，代次创建时为 0，每次成功状态变更加一。

（4）租户名空白、VLAN 列表超过 MaxVlansPerVf 或有重复项都拒绝，分别报 ErrBadRequest 与 ErrVlanLimit；在未启用的网卡上分配报 ErrUnknownPF。

（5）每个租户的租约数受 MaxVfsPerTenant 硬顶，超了报 ErrTenantQuota；网卡上没有空闲编号报 ErrNoFreeVF，已租出的编号不得回收给新请求。

（6）同一虚拟功能的并发分配只能有一个赢家，其余请求全部拿到 ErrNoFreeVF，空闲计数不得为负。

（7）信任位关闭时拒绝改 MAC、拒绝改 VLAN 列表，报 ErrTrustRequired，原有配置与代次都不动；只改 spoofchk 不受这条限制。

（8）信任位打开后改 MAC 成功，VLAN 列表与 spoofchk 都按这一次请求整体覆盖，代次继续加一；SetTrust 只对已租出的虚拟功能生效，空闲编号报 ErrVFNotLeased，未知编号报 ErrVFNotFound。

（9）Release 之后编号回到空闲池，LeaseOf 读不到租约，再次分配能拿回同一个编号；未知编号报 ErrVFNotFound。

（10）虚拟功能被 guest 驱动占用时，Release 只能把它标成待回收并保留租约，不能立刻复位这条功能，要等 guest 侧解绑后由 CompleteReclaim 真正归还。

（11）即使信任位已经打开，把 MAC 改成与物理网卡相同的地址仍要报 ErrMacConflict，越权边界不因信任而消失。

（12）缩减 PF 上的虚拟功能数量时，只要待裁掉的编号里还有租约在身，就必须整体拒绝并报 ErrShrinkBlocked，不能静默销毁。

硬约束两条。其一是并发与原子性：分配、配置、回收走同一把互斥锁，空闲检查与占用之间不能留窗口，代次与空闲计数必须一起推进，并发分配结束后空闲数加上租约数等于池容量。其二是快照与重启：网卡表、虚拟功能、租约与代次都要进快照，恢复后池结构、租约归属与空闲计数和原状态一致，损坏快照报 ErrBadSnapshot 且不得改动现网状态。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-d-01`
- 实现文件：`sriov_vf_allocator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SriovVfAllocator`
