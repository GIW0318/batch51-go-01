# 证书路径的策略树求值与可接受策略集

## 题面

开放银行平台给三家合作方换了新的签名证书，上线第二天风控发现两笔本该拒绝的交易被放行。复盘时链路校验本身是通过的，问题出在证书策略求值：中间 CA 的 policyMappings 把 A 映射成 B 之后，末端证书里只出现了 anyPolicy，系统仍认为策略是原来的 A；另一条路径上，中间 CA 已经带了 inhibitPolicyMapping 且计数归零，更下面的映射仍被继续沿用，末端证书里的策略被凭空放行；还有一份证书带了 requireExplicitPolicy 且计数为零，anyPolicy 却被当成满足显式策略要求，风控那条必须落在指定策略内的硬规则形同虚设。合规同事要求给出每张证书上策略集的变化过程，现有实现只能给一个布尔结论，连是哪一步丢的策略都说不清。落点 gsb/batch51-go-safety-a-08 的 policy_tree_evaluator.go，类型 PolicyTreeEvaluator，包名 task。

接口保持：NewPolicyTreeEvaluator(cfg EvalConfig) *PolicyTreeEvaluator、Evaluate(chain []CertView, req PolicyRequest) (PolicyResult, error)、ValidPolicy(chain []CertView, oid string) (bool, error)、Explain(oid string) (Trace, error)、Cached(chain []CertView, req PolicyRequest) bool、Invalidate() error、Normalize(chain []CertView) ([]CertView, error)、Fingerprint(chain []CertView) (string, error)、PolicySet(chain []CertView, req PolicyRequest) ([]string, error)、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：一、chain[0] 是信任锚，后续每张证书的 Issuer 必须等于上一张的 Subject，断链报 ErrChainBroken，空链报 ErrEmptyChain；二、有效策略集从请求的初始策略开始，请求为空时取信任锚自身的策略，逐证书求交并升序去重输出；三、证书里的 anyPolicy 只在允许展开时补足集合，展开结果必须具体到 OID，不能把 anyPolicy 原样留在结果里；四、inhibitAnyPolicy 计数到达零之后，后续证书的 anyPolicy 一律不得展开；五、policyMappings 把 A 映射成 B 后，B 必须在下一张证书里以具体 OID 出现才算通过，仅以 anyPolicy 形式出现不算；六、inhibitPolicyMapping 计数归零后仍存在的映射要按未映射处理，不能沿用前面的映射结论；七、显式策略要求未满足时返回 no_valid_policy，requireExplicitPolicy 为零时 anyPolicy 不得充当通过依据；八、三个跳过计数为负表示扩展缺失，非负值按证书顺序逐张递减且不降到零以下，自颁发证书不递减；九、单证书策略条数超过 MaxPoliciesPerCert 报 ErrTooManyPolicies，映射层数超过 MaxMappingDepth 报 ErrMappingDepth，节点预算按链上证书数与策略条目数之和计，超限报 ErrNodeBudget；十、求值结果按链指纹与初始策略集缓存，返回的切片必须是副本，Invalidate 清空缓存；十一、Explain 只对上一次求值过的 OID 给出逐证书轨迹，未求值过的 OID 报 ErrUnknownPolicy，Steps 记本次访问的证书数，ValidPolicy 按信任锚自身策略求值；十二、快照要能跨重启复用缓存，损坏载荷回 ErrBadSnapshot 且不影响已有缓存。

硬约束两条：缓存按链指纹只读共享，并发求值同一路径只能有一份结果写入，返回给调用方的策略切片不得反向影响缓存；策略树节点数、映射深度与单证书策略数设硬顶，超限报错而不是截断后给出通过。

验收：在模块目录执行 go test ./...，15 条必须全部通过；不得改动测试、跳过用例或按固定输入写死；并发与重启恢复场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-08`
- 实现文件：`policy_tree_evaluator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PolicyTreeEvaluator`
