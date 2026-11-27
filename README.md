# Wasm 插件沙箱的配额、预开目录与宿主调用门禁

## 题面

第三方插件上线后，宿主进程被拖垮：一个插件把机器内存吃光，另一个插件读到了别的租户目录里的配置，还有插件声称只导入两个函数，实际把宿主网络接口调起来却没有任何拦截记录。落点在 gsb/batch51-go-safety-c-06 的 wasm_plugin_sandbox.go，类型 WasmPluginSandbox 负责给每个第三方插件建受限运行实例，执行器由宿主通过 Runner 接口注入，沙箱只管授权、配额与生命周期。沙箱对外只有三段动作：先按名字装载代码与清单，再为插件建实例，最后在实例上调用清单里声明过的宿主函数；清单没写的东西一律看不见，统计面板靠 Stats 与 Plugins 展示每个插件的存活实例与累计消耗。

签名保持：NewWasmPluginSandbox(cfg SandboxConfig) *WasmPluginSandbox；Load(name string, code []byte, manifest Manifest) (PluginID, error)；Instantiate(pluginID PluginID) (Instance, error)；Call(inst Instance, fn string, args []byte) ([]byte, error)；Revoke(pluginID PluginID) error；Stats(pluginID PluginID) (Usage, error)；Plugins() []PluginInfo；Instances(pluginID PluginID) int；CheckPath(inst Instance, path string) error；Snapshot() ([]byte, error)；Restore(data []byte) error。

要处理的十一处：① 插件名为空白或代码为空仍能装载，应报 ErrInvalidPlugin；② 同名插件重复装载会覆盖旧实例，应报 ErrDuplicatePlugin 且原插件状态与实例不变；③ 插件数超过 MaxPlugins 仍继续装载，应报 ErrPluginLimit，已装载的不受影响；④ 每插件实例数超过 MaxInstancesPerPlugin 仍放行，应报 ErrInstanceLimit；⑤ 清单未声明的宿主函数照样被调用，应报 ErrHostFunctionDenied 且不得进入执行器；⑥ 参数体积超过 MaxMemoryBytes 仍透传，应报 ErrMemoryLimit；⑦ 未声明的目录可被访问，应报 ErrPathDenied，越出 RootDir 的路径应报 ErrPathEscape；⑧ 单次调用消耗的 fuel 超过 FuelPerCall 只记录不处理，应报 ErrFuelExhausted 并把插件标记为 failed；⑨ 撤销后新调用仍能跑，应报 ErrPluginRevoked，在途调用允许跑完当前 fuel 预算再退出，同时清掉该插件全部实例，重复撤销同样报 ErrPluginRevoked；⑩ 统计口径丢失，Stats 必须给出状态、实例数、累计调用数与累计 fuel；⑪ 快照恢复后插件表与计数回零，第二次撤销变成成功。

两条硬约束：实例配额与计数更新必须原子，同一插件被多路协程同时实例化时放行条数恰好等于上限，统计不能被并发调用写乱，读到的 Usage 必须是某个完整时刻的快照，不能一半旧一半新；快照只能呈现全量旧状态或全量新状态，恢复失败要原样保留现网，插件数、实例数、单次 fuel 与快照字节数都由配置硬顶，不许超限后截断。另有两处按现场口径处理：清单声明的路径在实例化时若被换成指向根外的符号链接，必须按解析后的真实路径拒绝；fuel 耗尽被终止的实例不能靠再次调用恢复，只能重建实例。

验收：go test ./... 的 15 条用例必须全过；测试不得修改、跳过或按固定输入返回常量；并发实例化与重启恢复自行写程序验证；只做本地实现。交付源码、测试输出与一页以内的修复说明，写清哪个插件顶到了配额、哪次调用超了 fuel，以及快照里保存了哪些字段、实例标识如何生成、撤销之后为何不再复用。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-06`
- 实现文件：`wasm_plugin_sandbox.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`WasmPluginSandbox`，能力声明走 `Manifest`，执行器走 `Runner`
