# Netlink 套接字策略：按协议族授权、端口标识归属与转储续传的会话一致性

## 题面

监控进程在一次升级之后集体哑掉：同一个 netlink 端口被两个采集进程同时绑定，谁也没报错，结果告警消息被拆成两半，两边各拿到一部分；另一台机器上运维退订了一个多播组，剩下的组跟着一起静默，队列里已经排好的事件全丢了。最麻烦的是一次全量转储跑到一半，目标网络命名空间被删掉，游标还在继续翻页，前端拿到一批已经不存在对象的名字。

落点在 `gsb/batch51-go-safety-d-09` 的 `netlink_socket_guard.go`，类型 `NetlinkSocketGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-d-09`。

公开接口保持：`NewNetlinkSocketGuard(cfg GuardConfig) *NetlinkSocketGuard`、`Open(family int, cred Cred) (Socket, error)`、`Bind(sockID string, groups []uint32) (BindResult, error)`、`BindPort(sockID string, portID uint32, groups []uint32) (BindResult, error)`、`UnbindGroup(sockID string, group uint32) error`、`Send(sockID string, msg Message) error`、`Recv(sockID string, max int) ([]Message, error)`、`Dump(sockID string, req DumpRequest) (DumpCursor, error)`、`DumpPage(cursorID string) (DumpPage, error)`、`RegisterObject(family int, ns, name string) error`、`Socket(sockID string) (Socket, bool)`、`Close(sockID string) error`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. 协议族为负数报 `ErrBadFamily`，不在 `AllowedFamilies` 里报 `ErrUnsupportedFamily`，审计类协议族没有管理能力时报 `ErrPermission`，套接字表超过 `MaxSockets` 报 `ErrSocketLimit`。
2. `Bind` 首次分配端口标识，从 `PortBase` 起顺序取空闲号，标识与代次一起返回；超出 `PortCount` 范围报 `ErrBadPort`。
3. 同一端口标识上的绑定只能有一路成功，其余报 `ErrPortInUse`；已经绑定的套接字再绑报 `ErrAlreadyBound`。
4. 多播组重复出现报 `ErrDuplicateGroup`，组号为零或超出掩码报 `ErrBadGroup`，绑定结果里的组按升序去重。
5. `Send` 之前必须已绑定，未绑定报 `ErrNotBound`；消息类型不是 ack、data、control 报 `ErrBadMessage`；发往未订阅的组报 `ErrUnknownGroup`。
6. 每个套接字队列上限为 `MaxQueue`，写满之后报 `ErrQueueFull` 且不许顶掉旧消息；`Recv` 的条数上限小于等于零报 `ErrBadMax`。
7. 消息序号由服务端统一递增，ACK 与数据消息按发送顺序投递，`Recv` 一次最多返回请求的条数，队列空时返回空列表而不是错误。
8. 转储请求的协议族必须与套接字一致，不一致报 `ErrFamilyMismatch`；未知套接字与未知游标分别报 `ErrUnknownSocket`、`ErrUnknownCursor`。
9. 转储按对象名升序分页，游标要带命名空间、代次与进度，页大小取请求值、缺省用 `DumpPageSize`，最后一页把 done 置上。
10. 绑定到多播组之后撤销单个组不能影响其他组，已经排队的消息仍要照常投递。
11. 转储进行中命名空间被销毁时游标立即失效，不能继续返回已经被删除的对象。
12. 端口标识回收之后重新绑定要换新代次，新端口收不到旧端口没有读走的消息。

硬约束：同一端口标识的并发绑定只能有一路成功，绑定表与端口表的更新要么一起完成要么都不做；ACK 与数据消息按序投递，队列满时不丢头上已经排好的消息；快照要带绑定表、队列内容与未完成的转储，恢复后从游标处继续，既不重复投递也不丢页，损坏快照报 `ErrBadSnapshot` 且不动现有状态。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发绑定与重启续传自行构造程序验证；只做本地实现。交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-d-09`
- 实现文件：`netlink_socket_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`NetlinkSocketGuard`
