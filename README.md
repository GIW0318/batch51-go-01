# 缺页处理区间归属与处理者失效治理

## 题面

沙箱进程把缺页交给外部处理者以后，处理者已经退出，内核还在等一个永远不会来的应答，整台机器上的容器一起卡死；另一个进程解除了一段区间，边界页却整段丢了，回收之后访问直接落到错误的分支；还有派生出来的子进程和父进程共用同一张处理者票据，父进程一退出子进程的缺页就全失败；值班同学只能重启整个节点来恢复，业务侧看到的是整批容器超时。落点在 gsb/batch51-go-safety-c-20 的 userfaultfd_range_guard.go，类型 UserfaultRangeGuard 登记每个沙箱进程的缺页处理区间，按区间路由缺页并判定处理者是否还活着。

签名保持：NewUserfaultRangeGuard(cfg Config) *UserfaultRangeGuard、RegisterHandler(id string) error、Heartbeat(handlerID string, now time.Time) error、HandlerAlive(handlerID string) bool、Register(reg Registration) (uint64, error)、Unregister(id uint64) error、UnregisterRange(pid int, start, end uint64) error、Handle(fault Fault) (Resolution, error)、ReportTimeout(handlerID string, page uint64) error、Fork(parentPID, childPID int) error、Ranges(pid int) []RangeInfo、Sweep(now time.Time) []uint64、Stat(pid int) ProcessStat、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的行为：一、处理者登记空标识拒绝，重复登记报已存在，心跳只认已登记且仍存活的处理者。二、区间必须页对齐且终点大于起点，同一进程内不得重叠，相邻允许，进程号非正直接拒绝。三、登记要把处理者、备份来源与代际一并记下，处理者缺失或已失效直接拒绝，区间标识从 1 开始单调递增，代际从 1 开始递增。四、缺页按地址路由到唯一处理者，返回的页地址按页大小向下取整，落在所有区间之外的地址失败关闭，不得回落到别的处理者。五、缺页处理返回的令牌单调递增，同一页重复处理不得复用令牌。六、处理者心跳丢失超过 TTL 后，其已登记区间立即失效并随清理摘除，清理返回被摘除的区间标识。七、处理者退出后其区间上的缺页一律失败关闭，不能把缺页留给无人处理的内核等待。八、单页处理超时且重试用尽要按失败关闭处理，不能一直阻塞。九、部分解除登记按页粒度重新归属边界页，其余页保持原处理者。十、进程派生后子进程的区间要么显式拒绝要么整段复制，不能与父进程共享同一处理者票据。十一、每进程区间数有硬上限，超限拒绝而不是覆盖旧区间；每进程统计给出区间数与已处理缺页数。十二、快照恢复保住处理者、区间、代际与令牌水位；解除登记后该区间不再参与路由，重复解除报未知区间而不是静默成功；恢复要么整份生效要么原样不动。

硬约束：登记、解除与缺页路由互斥推进，任一时刻只能看到完整的区间表；同一处理者的区间在失效后不得继续路由；缺页令牌与登记代际一起返回，调用方凭令牌回填时不得跨代使用；每进程区间数与未完成处理数封顶，超限失败关闭而不是排队堆积。

验收：go test ./... 的 15 条必须全过，不改测试、不跳过、不按固定输入写死；并发缺页与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-20`
- 实现文件：`userfaultfd_range_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`UserfaultRangeGuard`
