# 主机隐藏对象：交叉视图枚举、差集归因与采集受限降级

## 题面

一台对外服务的跳板机被通报后，应急同学在任务管理器里能看到一个陌生进程，换用直接系统调用枚举却什么都查不到；同一批机器上另一个模块在两个视图里的显示名不一样，工具把它算成两条，差集报告瞬间翻倍。更离谱的一次是直接系统调用被终端防护策略拦了，采集器返回空列表，工具却据此下结论说这台机器零差异、状态正常。请在 gsb/batch51-go-safety-c-29 的 rootkit_crossview_scanner.go 中补全 RootkitCrossViewScanner，签名保持：NewRootkitCrossViewScanner(collectors Collectors, policy Policy) *RootkitCrossViewScanner、Enumerate(kind string) (View, error)、Diff(view View) (DiffReport, error)、Attribute(item DiffItem) (Attribution, error)、Scan() (ScanResult, error)、Findings() []Finding、Snapshot() ([]byte, error)、Restore(data []byte) error、Stats() Stats、Reset()。采集器只读，不改动主机状态。

要修的点：只认 processes、modules、listeners 三类对象，其它类别报 ErrUnknownKind；两路采集必须在同一个时间窗内完成，窗口按两侧对象 SeenAt 的最小值与最大值计算，超出 TimeWindow 就整轮重采，重采次数不超过 MaxRecollect，仍然超窗报 ErrWindowExceeded 并把本轮记为不可判定，不能拿单路结果下结论。每类对象排序后按 MaxObjectsPerKind 截断，截断时把 Truncated 置真，保留键值较小的那部分。比对必须用稳定键：进程用进程号加启动时间，内核模块用加载地址加节区名，监听端口用协议加本地地址端口加 inode；显示名只作展示，同一对象在两侧名字不同也要合并成一条。两个视图之间消失的短命进程不能算隐藏对象，要结合启动时间与采集时刻判断。直接系统调用这一路被过滤策略拦截、根本拿不到对象时，要报采集受限和不可判定，不能当成零差异。差集要区分只在常规视图出现的隐藏项与只在直接视图出现的多余项，分别累计 HiddenCount 与 PhantomCount，条目按类别和稳定键升序排列。Attribute 对隐藏项给 hidden_from_direct 与严重度 80，对多余项给 only_in_direct 与严重度 30，类别未知报 ErrUnknownItem。Scan 依次枚举三类对象、做差集并追加新发现，同一稳定键同一定性只保留一条，重复扫描的 NewFindings 归零。Snapshot 保存发现项、序号与计数，损坏快照报 ErrBadSnapshot 且不动现网；Reset 清空发现项与计数。报告要能直接进工单，排查人一眼看出哪一路视图缺了对象。

硬约束：并发扫描要串行化，计数与发现项不能重复累加；快照恢复后序号继续递增，不能从头覆盖旧发现；枚举与差集都不依赖 map 遍历顺序，同一批输入重复扫描得到完全相同的条目顺序。

验收：go test ./... 15 条必须全过，不得修改测试、跳过测试或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-29`
- 实现文件：`rootkit_crossview_scanner.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RootkitCrossViewScanner`
