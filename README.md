# ACPI 表覆盖的准入与整批回滚

## 题面

一批同型号笔记本刷了自定义 DSDT 之后，休眠唤醒时电池电量直接跳到 0，另一批机器则出现了触摸板失灵；产线把 initrd 早期和后期两份 SSDT 一起塞进覆盖目录，谁生效完全看加载顺序，出问题时也说不清是哪一份在起作用。更糟的是一台机器的 OEM Table ID 与另一份表相同，构建脚本按 ID 去重，结果保留了旧内容，新表被静默丢掉了。这条覆盖链路本来是给产线调屏幕参数用的，结果成了一条谁都能改的内核配置入口；上一版构建把一份校验和算错的表也打了进去，机器启动到一半直接黑屏，最后只能整机返修重刷固件。审计要求每份表的来源、签名、长度和校验结果都能对上，回滚必须一步到位。落点在 `gsb/batch51-go-safety-c-04` 的 `acpi_override_guard.go`，类型 `AcpiOverrideGuard`，模块路径 `gsb/batch51-go-safety-c-04`，包名 `task`。

公开接口保持：`NewAcpiOverrideGuard(cfg OverridePolicy) *AcpiOverrideGuard`、`Register(table []byte, origin string) (TableID, error)`、`Validate(table []byte) error`、`Resolve(sig string) ([]Table, error)`、`Apply(order []TableID) error`、`Rollback() error`、`Active(sig string) (Table, bool)`、`Tables() []Table`、`Deregister(id TableID) error`、`Layers(sig string) int`、`SetSelfContained(id TableID, selfContained bool) error`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。策略里的 `MaxTables`、`MaxTableBytes`、`MaxLayers` 与 `RequireDsdtCompanion` 都要生效。

要修的地方：表短于 36 字节、长度字段与实际字节数不符、校验和相加不为 0 一律拒绝，分别报 `ErrShortTable`、`ErrLengthMismatch` 与 `ErrChecksumMismatch`；来源不是内置、initrd 早期、initrd 后期报 `ErrBadOrigin`；单表超过 `MaxTableBytes` 报 `ErrTableTooLarge`，登记表数超过 `MaxTables` 报 `ErrTableLimit`；同签名多份覆盖按来源优先级排序，内置低于 initrd 早期、initrd 早期低于 initrd 后期，只有最高优先级生效但被遮蔽的表保留，同签名同来源重复登记要各自保留而不是互相覆盖，`Resolve` 对没有登记过的签名报 `ErrUnknownSignature`；批量应用为空报 `ErrEmptyOrder`，含未登记编号报 `ErrUnknownTable` 且整批不生效，应用整批原子，任一表校验失败则整批不生效；覆盖 DSDT 必须同时提供配套 SSDT，否则报 `ErrDsdtNeedsSsdt`，声明自包含的表可以单独生效；单签名覆盖层数超过 `MaxLayers` 报 `ErrLayerLimit`；回滚要恢复到上一次生效的集合，没有可回滚的批次报 `ErrNoRollback`，仍在生效的表不允许注销并报 `ErrActiveTable`；SSDT 的 OEM Table ID 相同但内容不同要按来源与顺序区分，不能按 ID 去重；校验和正确但签名不是四个大写字母的表要拒绝；回滚到内置表时，被遮蔽的自定义表不能同时生效；快照恢复要带回登记表与生效集合，损坏快照报 `ErrBadSnapshot` 且不动现网。

硬约束：注册与解析并发安全，应用与回滚串行，回滚期间读方只能看到旧的完整集合或新的完整集合，回滚失败要能停在旧集合上，不能留下半套；表数量、单表大小与覆盖层数都按配置封顶，超限当场拒绝而不是截断。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发注册与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-04`
- 实现文件：`acpi_override_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AcpiOverrideGuard`
