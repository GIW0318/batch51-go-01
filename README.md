# 系统时钟跳变的授权与下游保护

## 题面

值班早高峰时计费服务把一批订单的时间戳算成了负数，对账系统跟着重放了两遍；排查发现主机时钟被一条不受控的云端校时命令向后拨了六分钟，TOTP 校验、证书有效期和租约同时误判，下游子系统有的暂停有的没停。这台机器前一天刚做过内核热补丁，重启后 chrony 还没连上，代理就把一条人工校时请求当成系统命令直接执行了；事后翻日志只看到一次跳变结果，看不到是谁批准、跳了多少、跳之前水位停在哪里。问题落在 `gsb/batch51-go-safety-c-02` 的 `clock_step_guard.go`，类型 `ClockStepGuard`，模块路径 `gsb/batch51-go-safety-c-02`，包名 `task`。修复目标是把每一次调整都变成可申请、可追溯、可回放的动作。这次事件之后运维把校时权限整体收回，只留下一个受控入口，所有跳变都要先申请再执行，执行完还要能对上跳变前后的水位。

公开接口保持：`NewClockStepGuard(cfg ClockPolicy) *ClockStepGuard`、`SetSource(src string) error`、`SetWall(t time.Time, mono int64) error`、`Request(delta time.Duration, reason, actor string) (Ticket, error)`、`Apply(t time.Time, mono int64) error`、`Status() ClockStatus`、`Subscribe(name string) error`、`Unsubscribe(name string) error`、`Subscribers() []SubscriberState`、`Ticket(id string) (Ticket, bool)`、`RevokeTicket(id string) error`、`Pending() int`、`Watermarks() []Watermark`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。

要修的地方：时钟源只认可信 NTP、人工与未知三类，写别的值报 `ErrBadSource`；未设置基线就调 `Apply` 报 `ErrBadTimestamp`，单调值给负数报 `ErrBadMono`，未知来源的调整一律拒绝并报 `ErrUntrustedSource`；人工跳变超过 `ManualStepLimit` 又没有票据报 `ErrApprovalRequired`，申请票据说不清原因或没写操作人分别报 `ErrBadReason` 与 `ErrBadActor`；在途票据超过 `MaxTickets` 报 `ErrTicketLimit`，超过有效期还没使用的票据要作废，不能一直占着在途额度；票据只认申请时的位移，差一点报 `ErrTicketMismatch` 而且不消耗票据；撤销过的票据再用报 `ErrApprovalRequired`；墙钟向后跳时单调水位不能倒退，下游算耗时必须用单调值；同一张票据只能应用一次，重复 `Apply` 要报错；NTP 平滑调整累计跨过阈值要按累计量判定，不能因为单步很小就免授权；跳变超过阈值时给订阅方发暂停信号、回到阈值内再恢复并记录暂停次数，`Status` 里的步数只统计真正生效的跳变；订阅重名、超过 `MaxSubscribers`、空名字、退订不存在的订阅分别报 `ErrDuplicateSubscriber`、`ErrSubscriberLimit`、`ErrBadSubscriber` 与 `ErrUnknownSubscriber`，历史水位只留最近 `MaxWatermarks` 条且序号严格递增；快照恢复要带回墙钟、单调值、水位与订阅状态，损坏快照报 `ErrBadSnapshot` 且不动现网。

硬约束：调整与水位写入必须原子，并发申请票据要串行发券，不能两张票据拿到同一个序号；订阅状态与墙钟推进在同一把锁内切换，读方不能看到墙钟已经跳过而订阅还停在旧的暂停状态。在途票据数、订阅者数量与历史水位保留条数都按配置封顶。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发调整与重启恢复自行验证；只做本地实现，交付时附一份水位与票据的对照说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-02`
- 实现文件：`clock_step_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ClockStepGuard`
