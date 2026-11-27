# 容器时间命名空间偏移的一致代际

## 题面

边缘机房里跑着一批多租户定时任务，最近接连出三种怪象：容器里设的十五分钟超时，实际十几秒就触发；证书在容器内显示还有两小时有效期，真正握手时却判成已过期；审计流水里同一次操作的两个时间戳，正好相差一个偏移量。这些任务跑在同一台宿主上，只是各自的时间命名空间偏移不同。落点在 gsb/batch51-go-safety-c-11 的 time_namespace_offset_guard.go，类型 TimeNamespaceOffsetGuard 为每个命名空间保存 realtime 与 boottime 两套偏移，让定时器换算、有效期判定与时间戳记账共用同一代偏移。偏移一旦切换就换代，任何一次读取拿到的三元组必须完整属于某一代，不能一半新一半旧。值班同学把三台机器的宿主时间逐一比对过，宿主时钟本身是准的，偏差只在偏移切换的那一瞬间冒出来，所以问题不在时钟源，而在这套偏移账本换代的时机与读写口径。

签名保持：NewTimeNamespaceOffsetGuard(cfg GuardConfig) *TimeNamespaceOffsetGuard；CreateNamespace(namespace string) (Handle, error)；Handle(namespace string) (Handle, error)；ApplyOffset(h Handle, off TimeOffset) (int64, error)；Now(h Handle) (TimeSnapshot, error)；TranslateDeadline(h Handle, guestDeadline int64) (Deadline, error)；Resolve(h Handle, guestStamp int64) (int64, error)；Generation(h Handle) (int64, error)；Release(h Handle) error；Namespaces() []NamespaceInfo；Snapshot() ([]byte, error)；Restore(data []byte) error。

要处理的十一处：① 命名空间名为空白或含分隔符仍能注册，应报 ErrInvalidNamespace；② 同名重复注册会顶掉旧登记，应报 ErrNamespaceExists 且旧句柄仍然可用；③ 注册数超过 MaxNamespaces 仍放行，应报 ErrNamespaceLimit；④ 偏移绝对值超过 MaxOffsetNanos 或 slew 速率超过 MaxSlewPPM 仍被接受，应报 ErrOffsetRange 且代数不变；⑤ 新注册的命名空间代数从零起算，应从 InitialGeneration 开始，每次 ApplyOffset 严格加一；⑥ Now 返回的 realtime、boottime 与两个偏移若不是同一代，读到的三元组就会分属新旧两代，代数与偏移必须能对上；⑦ TranslateDeadline 拿旧代偏移折算宿主剩余时长，应按当前代 realtime 偏移换算并把代数写进结果；⑧ Resolve 把客机时间戳换回宿主时刻时漏减偏移，应按当前代 realtime 偏移相减；⑨ 时间戳为零或为负仍被换算，应报 ErrInvalidStamp；⑩ 已回收的句柄还能继续换算，应报 ErrStaleHandle，完全没登记过的句柄报 ErrUnknownHandle；⑪ 快照恢复后代数回零、偏移丢失、旧句柄复活，必须完整恢复代数与偏移，损坏快照报 ErrBadSnapshot 且不动现网。

两条硬约束：同一命名空间的 ApplyOffset 与 Now 必须走原子换代，读路径不许持锁回调业务代码，多个协程同时切偏移时放行次数与代数增量必须相等，任何一次读取都不能看到 realtime 与 boottime 分属两代；快照只能呈现全量旧状态或全量新状态，恢复后代数不得回退，注册数与偏移幅度都是硬上限，超限一律报错，不允许截断后继续。

另有三处按现场口径处理：偏移为负且换算结果越过 boottime 零点时，定时器剩余时长不得为负或回绕；命名空间回收之后，旧句柄上的换算请求同样必须报错，不能沿用最后一代偏移；一次性跳变与持续 slew 混用时，单调时钟只允许前进，不允许回退。

验收：go test ./... 的 15 条用例必须全过；测试不得修改、跳过或按固定输入返回常量；并发换代与重启恢复自行写程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-11`
- 实现文件：`time_namespace_offset_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过 `gsb/batch51-go-safety-c-11` 导入本模块
- 主要类型：`TimeNamespaceOffsetGuard`，句柄类型 `Handle`，配置类型 `GuardConfig`
