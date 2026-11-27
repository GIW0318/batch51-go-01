# 反序列化白名单：类型图裁决、深度上限与 gadget 链阻断

## 题面

投诉：订单中心接了一个外部合作方的批量导入接口，上周被安全扫描挂了两条高危。第一条是导入体里带了一个我们没见过的类名，短名和白名单里的 `Request` 一模一样，只是包名不同，校验只比了短名就放了过去，落地之后触发了一条反序列化链，把进程里的命令执行器带了起来。第二条更直接：对方塞了一个自己指向自己的类型图，校验在展开字段时栈溢出，导入服务连着重启了三次。还有一类误伤：白名单只管了外层类型，运营把合法类型塞进未登记的子类型时被一刀切拒掉，投诉说我们拦得太死。排查下来三件事都出在校验这一层：一处只比短名，一处没有深度上限，还有一处颗粒度太粗、外层放行就不再往下看。当天值班只能先把导入接口整个下线，隔天下午才恢复。

落点在 `gsb/batch51-go-safety-r-07` 的 `deserialization_allowlist_guard.go`，类型 `DeserializationAllowlistGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-r-07`。

公开接口保持：`NewDeserializationAllowlistGuard(cfg AllowlistConfig) (*DeserializationAllowlistGuard, error)`、`Allow(name string) error`、`Drop(name string) error`、`Parse(raw []byte) (Payload, error)`、`BuildGraph(p Payload) (TypeGraph, error)`、`Adjudicate(graph TypeGraph) (Decision, error)`、`Check(raw []byte) (Decision, error)`、`Policy() []string`、`Audit() []CheckRecord`、`Stats() GuardStats`、`MaxDepth() int`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `MaxDepth`、`MaxNodes`、`MaxAudit` 任一不为正报 `ErrBadConfig`；深度上限要能通过 `MaxDepth()` 读回。
2. `Allow` 与 `Drop` 都只接受全限定名（至少一段包名加一个点），不合格报 `ErrBadTypeName`；重复 `Allow` 幂等，`Drop` 不存在的条目报 `ErrUnknownType`；`Policy` 按升序返回。
3. `Parse` 只认一种载荷：一个根类型名加一组节点声明；非法 JSON 报 `ErrBadPayload`，根为空报 `ErrBadRoot`，同名类型出现两次报 `ErrDuplicateType`；节点里的字段名与类型引用按原样保留。
4. `BuildGraph` 要把字段引用里的 `[]`、`*` 装饰剥掉再解析；引用到载荷里没有声明的类型报 `ErrUnknownType`，根不在节点表里报 `ErrMissingRoot`，节点数超过 `MaxNodes` 报 `ErrGraphTooLarge`。
5. 裁决先看根类型：全限定名不在白名单里直接拒，原因 `reason_root_denied`，`Type` 记根名。
6. 根通过后逐字段展开，字段的实际类型不在白名单里照样要拒，原因 `reason_field_denied`，`Type` 记字段类型、`Field` 记字段名，不能只看外层。
7. 深度从根算 1，访问到超过 `MaxDepth` 的类型时拒，原因 `reason_depth_exceeded`，`Type` 记越界类型、`Depth` 记实际深度。
8. 类型图出现环，也就是同一类型在当前路径上重复出现时，按 `reason_cycle_detected` 拒，`Path` 带上环上的类型序列，不能无限展开。
9. 白名单按全限定名精确匹配：`pkg.Request` 不等于 `other.Request`，也不等于 `pkg.Request.Inner`。
10. 每次裁决写审计，序号严格递增，只留最近 `MaxAudit` 条；被拒的那条同样要进审计，别只记放行的；`Stats` 给出检查数、放行数、拒绝数与白名单条数。
11. 快照带上白名单、审计、序号与计数；恢复之后白名单继续生效、序号继续递增；损坏快照报 `ErrBadSnapshot` 且不动现网。
12. 并发 `Check` 与 `Allow` 同时发生时要串行化，计数不丢、审计不重复，一次裁决只写一条记录；同一份载荷重复送进来，拿到的裁决必须一致。

验收：`go test ./...` 15 条全过；测试不得修改、不得跳过、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-r-07`
- 实现文件：`deserialization_allowlist_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DeserializationAllowlistGuard`、`AllowlistConfig`、`Payload`、`TypeNode`、`TypeGraph`、`Decision`、`CheckRecord`、`GuardStats`
