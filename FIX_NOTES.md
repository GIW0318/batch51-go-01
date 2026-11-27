# wasm_plugin_sandbox.go 修复说明

## 结果

已实现 `WasmPluginSandbox` 的装载、实例、调用、撤销、统计、路径授权和快照恢复。原验收测试 15/15 通过；额外以临时用例验证了预开目录符号链接逃逸、`symlink/..` 顺序、撤销时在途调用、fuel 后重建实例、快照硬顶和恢复失败不改现网，并在 `go test -race` 下通过。

## 关键修复

- `Load`：拒绝空白名称/空代码；同名先判 `ErrDuplicatePlugin` 且不触碰原状态；插件表满返回 `ErrPluginLimit`。代码、清单与宿主函数集合均做拷贝。
- `Instantiate`：实例配额检查、实例写入、计数更新在同一把锁内完成；并发 12 路、上限 2 时恰好 2 个成功、10 个 `ErrInstanceLimit`。预开目录在实例化时逐组件解析真实路径，符号链接指向根外返回 `ErrPathEscape`。
- `Call`：先验证实例可用、宿主函数已声明、参数不超 `MaxMemoryBytes`，被拒绝的调用不会进入 Runner。Runner 返回 fuel 超预算时记录本次调用与实际 fuel，返回 `ErrFuelExhausted`，插件置为 `failed`，该实例留下不可用墓碑；再次调用旧实例返回 `ErrInstanceUnusable`，`Instantiate` 才会生成新实例。
- `CheckPath`：请求路径先按操作系统语义解析符号链接与 `..`；根外返回 `ErrPathEscape`，根内但不在实例预开目录返回 `ErrPathDenied`；预开目录内指向别处或根外的链接不能绕过授权。
- `Revoke`：置 `revoked`、清空全部实例与存活计数；新调用返回 `ErrPluginRevoked`，已在 Runner 中的调用可完成并计入累计值；重复撤销返回 `ErrPluginRevoked`。
- `Stats/Plugins/Instances`：同一锁内读取 `state`、存活实例数、累计调用数、累计 fuel、内存峰值；`Plugins` 按名称排序。

## 并发与快照

- 所有插件表、实例表、计数和配额变更由单一 `RWMutex` 串行化；`Usage` 不会出现半旧半新的字段。快照在同一读锁内序列化，只会得到完整旧状态或完整新状态。
- 快照包含：格式版本、生成时根目录及解析后根目录、下一插件/实例序号；插件 `id/name/code/manifest/state/calls/fuel_used/memory_peak`；实例 `id/plugin_id/live/preopens`（实例化时已解析的真实预开路径）。恢复到新根时，只把旧根内路径按相对位置映射到新根内；旧根外路径不迁移，后续仍会被授权检查拒绝。
- `Restore` 先完整解析并校验插件数、实例数、ID/名称唯一性、状态和引用关系，全部通过后才一次性换表；失败时现网原样保留。快照序列化/恢复均受 `MaxSnapshotBytes` 硬顶，超限直接报错，不截断。
- 插件 ID 为锁内单调分配 `plugin-<n>`，实例 ID 为 `instance-<n>`；快照保留序号，恢复后继续分配并跳过既有 ID，因此实例标识稳定且不复用。
- 撤销后名称仍占用、状态和计数器仍保留；旧实例 ID 不回收，重复撤销明确报错。这样既满足审计展示，也避免同一个已撤销插件被静默复用。

## 验收证据

- `go test ./...`：15 条全部通过；`go test -race ./...`：通过。
- 配额命中：夹具插件 `gamma` 在 `MaxPlugins=2` 时第三个装载返回 `ErrPluginLimit`；插件 `alpha` 在 `MaxInstancesPerPlugin=2` 时并发实例化只放行 2 个。
- fuel 超限：插件 `alpha` 的 `kv_read` 调用由 Runner 报告使用 51 fuel，`FuelPerCall=50`，该次调用返回 `ErrFuelExhausted` 且插件状态为 `failed`。
