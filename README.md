# 自启动持久化面的清点与基线对账

## 题面

一台被跑进挖矿程序的机器在清理时反复复活，运维删了用户级 systemd 单元、清了 crontab，重启之后进程照旧；后来才发现同一条自启动项在系统级单元、cron 文件和 shell 启动文件里各写了一份，枚举脚本只按名字去重，遮蔽关系完全没算，删掉的只是被遮住的那一份。更麻烦的是内容改过但路径没变的自启动项被当成老条目，基线对账一直是零差异，值班同事因此签字确认了两次。这台机器此前已经按同一份基线做过三次合规检查，三次都显示无差异，直到流量告警才把问题翻出来，事后追责时谁也说不清是哪一次扫描漏掉了系统级目录。落点在 `gsb/batch51-go-safety-c-05` 的 `autostart_surface_auditor.go`，类型 `AutostartSurfaceAuditor`，模块路径 `gsb/batch51-go-safety-c-05`，包名 `task`。

公开接口保持：`NewAutostartSurfaceAuditor(cfg AuditConfig) *AutostartSurfaceAuditor`、`Register(items []Item) error`、`Scan(scope string) ([]Entry, error)`、`Baseline(scope string) error`、`Diff(scope string) ([]Delta, error)`、`Allow(entryID string, until time.Time) error`、`RevokeAllow(entryID string) error`、`AllowList() []Allowance`、`Remove(entryID string) error`、`Entries(scope string) []Entry`、`Entry(id string) (Entry, bool)`、`SourceNames() []string`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。配置里的 `MaxEntries`、`MaxBaselines`、`MaxAllowances` 与 `Protected` 都要生效。

要修的地方：来源只认 systemd 系统级与用户级、cron、at、shell 启动文件、桌面自启动、udev 规则与模块加载配置，写别的报 `ErrBadSource`，条目路径为空报 `ErrBadItem`；`Scan` 的 scope 只认 all、system、user，别的报 `ErrUnknownScope`；单次扫描条目数超过 `MaxEntries` 报 `ErrScanLimit`；同一条目被多处声明时合并成一条并保留全部来源，来源列表不能丢，登记批次里出现重复路径时以最后一次内容为准；条目标识用规范化路径加内容摘要，内容变化视为新条目而不是同名沿用，路径变了摘要没变同样算新增；同名单元在用户级与系统级目录各有一份时，实际生效的是遮蔽关系决定的那份，枚举不能简单取并集；与基线比较要区分新增、修改、消失与仅时间戳变化，没有基线就做对比报 `ErrNoBaseline`；豁免受保护条目报 `ErrProtectedEntry`，到期时间为空报 `ErrBadUntil`，豁免条数超过 `MaxAllowances` 报 `ErrAllowanceLimit`；移除受保护条目报 `ErrProtectedEntry`，移除未知条目报 `ErrUnknownEntry`，撤销不存在的豁免报 `ErrUnknownAllowance`；cron 文件被删除但仍有进程持有旧 inode 时，移除动作要以配置消失为准而不是继续报存在；基线快照超过 `MaxBaselines` 淘汰最旧，只保留最近若干次；快照恢复要带回条目、基线与豁免，损坏快照报 `ErrBadSnapshot` 且不动现网。

硬约束：扫描与移除串行，批量移除中途失败要把已处理的条目回滚回去，不能留下半清理的现场；同一 scope 的扫描与基线写入不能互相看到半份数据。单次扫描条目数、基线快照数与豁免条目都按配置封顶，超限当场拒绝而不是截断。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发扫描与重启恢复自行验证；只做本地实现，交付时说明基线与豁免的保留口径。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-05`
- 实现文件：`autostart_surface_auditor.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AutostartSurfaceAuditor`
