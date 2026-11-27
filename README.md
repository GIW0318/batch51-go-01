# 网络命名空间持有者计数与残留接口回收

## 题面

网络运维发现两件相反的怪事：删掉一个业务网络命名空间之后，残留的 veth 对端和规则表还挂在宿主上，过几天就把地址池耗尽；另一头，某个命名空间明明还有外部进程通过绑定挂载占着文件，回收脚本却把登记删掉了，业务网络当场断掉。落点在 gsb/batch51-go-safety-c-14 的 netns_reclaim_guard.go，类型 NetnsReclaimGuard 为每个网络命名空间登记进程、描述符与挂载点三类持有者，引用归零后进入待回收队列，回收前核对 veth 对端、路由表与规则表是否真的清干净了。两件事发生在同一周，运维组长要求先把持有者计数和回收前校验这两笔账理清，再谈自动化。

签名保持：NewNetnsReclaimGuard(cfg ReclaimConfig) *NetnsReclaimGuard；Register(id string, inode uint64) (NetnsInfo, error)；Acquire(id, kind, key string, inode uint64) error；Release(id, kind, key string) error；Pin(id string) error；Unpin(id string) error；Sweep() (SweepReport, error)；Info(id string) (NetnsInfo, bool)；Pending() []string；SetCleanup(id string, veth, routes, rules bool) error；Snapshot() ([]byte, error)；Restore(data []byte) error。

要处理的十二处：① 命名空间标识为空白仍能登记，应报 ErrInvalidID；② 同一标识重复登记顶掉旧记录，应报 ErrAlreadyRegistered 且原记录与持有者不动；③ 登记数超过 MaxNamespaces 仍放行，应报 ErrNamespaceLimit；④ 持有者种类不认识或键为空白仍能登记，应报 ErrInvalidHolder，同一持有者重复登记必须幂等、引用数不加；⑤ 单命名空间持有者超过 MaxHoldersPerNetns 仍放行，应报 ErrHolderLimit；⑥ 释放不存在的持有者返回成功，应报 ErrUnknownHolder，命名空间本身不存在报 ErrUnknownNamespace；⑦ 引用归零后不进待回收队列，最后一个持有者离开时必须入队并把状态转成 pending；⑧ 已经 Pin 住的命名空间在引用归零后照样被回收，Pin 必须把它从队列摘掉，Unpin 之后重新入队，重复 Unpin 报 ErrNotPinned；⑨ 回收前不核对 veth、路由表与规则表，三项都清干净才允许回收，否则留在队列里并计入 Failed；⑩ 回收失败后直接把对象从登记里删掉，必须保留登记以便重试；⑪ 队列里同一命名空间出现多条、Pending 顺序不稳定，应按标识升序且每个标识只出现一次；⑫ 快照恢复丢掉持有者、Pin 与清理标记，必须完整恢复，损坏快照报 ErrBadSnapshot 且不动现网。

两条硬约束：引用计数与待回收队列必须原子推进，Sweep 与新建、重新 Pin 并发时不得误删刚刚被重新 Pin 的对象，任何一次 Info 读到的引用数、Pin 标记与状态必须同属一个时刻；快照只能呈现全量旧状态或全量新状态，恢复失败原样保留现网，命名空间数、单命名空间持有者数与清理重试次数都是硬顶，超限一律报错。

另有三处按现场口径处理：同一个 inode 通过绑定挂载出现两次时只能算一个持有者；回收中途失败要保留墓碑并允许重试，不能直接把登记移除；外部进程仍然挂载着命名空间文件时，引用计数不得归零。

验收：go test ./... 的 15 条用例必须全过；测试不得修改、跳过或按固定输入返回常量；并发增减持有者与重启恢复自行写程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-14`
- 实现文件：`netns_reclaim_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过 `gsb/batch51-go-safety-c-14` 导入本模块
- 主要类型：`NetnsReclaimGuard`，登记信息 `NetnsInfo`，回收报告 `SweepReport`
