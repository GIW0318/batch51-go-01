# JSON 原型链污染探测与残留恢复

## 题面

会员中心的资料合并接口上线后，前端某个从未声明过的默认值突然对所有用户生效，刷新页面也不消失，只有重启服务才恢复；接口只接受 JSON，合并逻辑在解析成功之后把对象键递归拷进共享配置，出问题的请求体里带着一段看起来人畜无害的嵌套键。排查时又发现另一个入口只是解析成功、并没有真的改到共享配置，两种情形从响应上看几乎一样。值班同事把请求体里的嵌套键逐个删掉重放，才把范围缩到两处合并入口，但哪一处真的改到了共享配置、改完之后又该怎么退回去，仍然没有定论。需要把这套判定与清理做成统一的探测能力。落点在 `gsb/batch51-go-safety-c-23` 的 `prototype_pollution_prober.go`，类型 `PrototypePollutionProber`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-23`。

公开接口保持：`NewPrototypePollutionProber(cfg ProberConfig) *PrototypePollutionProber`、`AddTarget(spec TargetSpec) error`、`Generate(kind, key, value string) (Payload, error)`、`Inject(target string, payload Payload) (Response, error)`、`Verify(target, marker string) (Verification, error)`、`Probe(target, kind, key, value string) (Finding, error)`、`Baseline(target string) (Baseline, error)`、`Recover(target string) (Recovery, error)`、`ResidualKeys(target string) []string`、`Stats(target string) ProberStats`、`Export() ([]byte, error)`、`Import(data []byte) error`。

要修的地方：目标名为空白报 `ErrBadTarget`、重名报 `ErrDuplicateTarget`、未注册报 `ErrUnknownTarget`；键为空报 `ErrBadKey`，路径类型不认识报 `ErrBadPathKind`；proto 载荷必须产出 `{"__proto__":{"键":"值"}}`，constructor 载荷必须是 `constructor.prototype` 三层嵌套，数组载荷要产出一个顶层数组、首元素里带 `__proto__`，三条路径分别记成 `__proto__.键`、`constructor.prototype.键`、`[0].__proto__.键`；`safe` 模式只接受请求但不把原型键写进共享配置，`vulnerable` 模式才真正落地；`Verify` 要用新建对象读回键值，读到值才算继承，通道记 `response`；`Probe` 必须复用已有基线快照，基线序号写进结论；恢复要删掉写入的键并返回删除清单，残留清单为空，未被动过的默认键不能丢；同一目标多轮探测期间基线不能被污染带跑；污染只在子进程或新工作线程里可见时，本轮响应看不到，但副作用接口必须能看到，通道记 `side_effect`；目标对 `__proto__` 做了黑名单却放行 `constructor.prototype` 时不能判成安全；恢复失败要把残留键名记下来，下一轮基线不得带着污染继续比对；同一对键值重复注入多轮，污染计数按目标累计而残留键只记一份；每个目标的探测条数由 `MaxProbesPerTarget` 硬顶，超出报 `ErrProbeBudget`；导出与导入要跨重启带回共享配置、基线与结论，损坏快照报 `ErrBadSnapshot` 且不改动现网。

硬约束：同一目标的注入必须串行，共享配置的读写要么看到完整的旧值要么看到完整的新值，不能读到写了一半的映射；导出与导入整体成功或整体拒绝；残留键数量按 `MaxResidualKeys` 封顶，写满不许覆盖已有记录。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发注入与重启恢复自行构造程序验证；只做本地实现。交付源码、测试记录，以及一页以内的判定与恢复口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-23`
- 实现文件：`prototype_pollution_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PrototypePollutionProber`
