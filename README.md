# 多份 RBAC 策略合并、继承环与租户边界

## 题面

运维平台把权限改成多份策略文档合并之后，一周里出了三种乱子：同一个操作在策略 A 里写着允许、在策略 B 里写着拒绝，系统却按后加载的那份放行，事后翻日志只找到一条来源，没法解释；两个团队互相把角色继承写成了环，加载时没有报错，只是把继承链截断，结果一部分越权请求被静默放行；还有开发把另一个租户的管理员角色当父角色挂进自己的策略，跨租户的权限直接通了。请把模块路径 gsb/batch51-go-safety-r-02、实现文件 rbac_policy_merge_guard.go 里的 RbacPolicyMergeGuard 补齐。

对外签名保持：NewRbacPolicyMergeGuard(cfg MergeConfig) *RbacPolicyMergeGuard、Load(p Policy) error、Merge(policies []Policy) error、Expand(tenant, role string) ([]string, error)、Evaluate(req Request) (Decision, error)、Roles(tenant string) []string、Policies(tenant string) []string、Version(policyID string) (int64, bool)、Audit() []AuditEvent、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的故障与边界：

（1）策略文档缺 ID 或租户、规则效果不是 allow 与 deny 之一、角色名为空，一律报 ErrBadPolicy，且不得留下半份策略。

（2）同一 ID 重复加载时版本号必须严格变新，否则报 ErrStaleVersion，已加载的版本与规则保持不动。

（3）同一 ID 绑到另一个租户报 ErrDuplicatePolicy，不允许静默覆盖。

（4）父角色在当前租户所有已加载策略里都找不到时报 ErrUnknownRole，不能当叶子角色处理。

（5）角色继承出现自环或多角色互指时报 ErrInheritanceCycle，不能截断继承链继续求值；一份策略里同时存在自环和跨租户边时先报环再报越界，两个错误码不能互相覆盖。

（6）父角色写成 tenant:role 且租户与当前策略不同时报 ErrTenantBoundary，跨租户引用一律拒绝。

（7）批量加载要么全成要么全不成，任何一份失败都要把这一批整体回滚，Roles 与 Policies 不得看到部分结果。

（8）求值时动作或资源带 * 的规则按通配匹配；动作与资源都不匹配任何规则时拒绝，结论码 rbm_no_match。

（9）命中拒绝规则时一律拒绝，结论码 rbm_deny_rule；同一请求同时命中允许与拒绝时也按拒绝处理，审计要把两条来源都记下来。

（10）通配规则与精确资源规则并存时，精确规则优先给出结论来源，但拒绝规则仍然最高。

（11）请求的租户没有任何策略时报 rbm_unknown_tenant，角色不在该租户时报 rbm_unknown_role，都按拒绝返回而不是报错；继承展开要去重并按角色名升序返回，深度受 MaxDepth 硬顶，超了报 ErrDepthExceeded。

（12）每次求值写一条审计，带自增序号、租户、角色、动作、资源、结论码与命中来源条数，审计表按 MaxAudit 封顶；快照要带出策略表、版本号与审计水位，恢复后序号接着涨，损坏快照报 ErrBadSnapshot 且不得改动现网状态。

硬约束两类：一是并发与原子性，策略热加载与求值并发时，求值方只能看到旧的完整策略表或新的完整策略表，审计序号只能发一次；二是恢复与上限，策略表按 MaxPolicies 封顶，超了报 ErrPolicyLimit，加载失败不得留下任何半份策略。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-r-02`
- 实现文件：`rbac_policy_merge_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RbacPolicyMergeGuard`、`Policy`、`RoleDef`、`Rule`、`Request`、`Decision`、`Match`、`AuditEvent`、`MergeConfig`
