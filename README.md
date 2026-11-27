# POSIX 消息队列的命名隔离、优先级出队与配额回收

## 题面

作业调度切到消息队列后，两个租户的工人互相读到了对方的任务，同一优先级的消息还出现后发先至；某个作业退出后队列名一直占着，新作业打不开同名队列，运维只能重启代理。落点在 gsb/batch51-go-safety-c-09 的 posix_mqueue_guard.go，类型 PosixMqueueGuard 负责为作业分配消息队列并约束使用。队列名按租户与作业编码，打开后拿到一个句柄，收发都走句柄；消息体、条数与字节数都按队列记账，超限按打开模式决定是拒收还是等一小会儿。

签名保持：NewPosixMqueueGuard(cfg QueueConfig) *PosixMqueueGuard；Open(tenant, job, name string, flags OpenFlags) (Handle, error)；Send(h Handle, msg Message, prio int) error；Receive(h Handle, max int) ([]Message, error)；Notify(h Handle, req NotifyRequest) error；Close(h Handle) error；Stats(tenant, job string) (QueueStats, error)；Pending(h Handle) int；Unlink(tenant, job string) error；Snapshot() ([]byte, error)；Restore(data []byte) error。打开标志为 FlagCreate、FlagExclusive、FlagNonBlock，优先级取 0 到 31。

要处理的十二处：① 租户、作业或队列名为空白仍能打开，应报 ErrInvalidName；② 不带创建标志打开不存在的队列也成功，应报 ErrNotFound，带独占标志打开已存在的队列应报 ErrExists；③ 另一个租户用同一队列名也能打开，应报 ErrCrossTenant，原属主照常可用；④ 队列数超过 MaxQueues 仍能新建，应报 ErrQueueLimit；⑤ 出队按插入顺序而不是优先级，必须优先级降序、同优先级先进先出；⑥ Receive 忽略条数上限，应最多返回 max 条，max 小于等于零报 ErrInvalidCount；⑦ 条数或字节配额超限仍入队，非阻塞句柄应报 ErrWouldBlock，阻塞句柄在 BlockTimeoutMs 内等不到空间应报 ErrTimeout；⑧ 通知可以注册多份且反复触发，队列只允许一个注册、触发一次即清空，替换注册要原子；⑨ 已关闭或别的守护实例的句柄仍能收发，应报 ErrInvalidHandle，重复关闭同样报错；⑩ 关闭描述符会丢队列内容，内容必须留在队列里，重开可见；⑪ Unlink 之后队列仍可打开，应报 ErrNotFound 且统计不可读；⑫ 快照恢复丢掉消息顺序与待决通知，应完整恢复，损坏快照报 ErrBadSnapshot 且不动现网。

两条硬约束：入队、出队与配额更新必须原子，队列数与消息数在并发下只放行到上限；快照只能呈现全量旧状态或全量新状态，恢复失败原样保留现网，队列数、每队列消息数与通知注册数都是硬上限。另有三处按现场口径处理：关闭最后一个描述符时若还挂着通知注册，队列要按租约延后删除，不能立刻丢名导致等待方报错；同一优先级的并发发送必须按入队完成时刻定序，不得因为并发而乱序；消息体超过单条上限时整条拒绝，不能截断后入队。

验收：go test ./... 的 15 条用例必须全过；测试不得修改、跳过或按固定输入返回常量；并发收发与重启恢复自行写程序验证；只做本地实现。交付源码、测试输出与一页以内的配额说明，写清条数、字节与通知三种上限的判定顺序，以及句柄生命周期和租约到期后的回收路径。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-09`
- 实现文件：`posix_mqueue_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PosixMqueueGuard`，句柄是 `Handle`，统计是 `QueueStats`
