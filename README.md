# 设备 ioctl 准入：请求号解码、方向与长度校验及透传白名单的代次管理

## 题面

安全评审在一个摄像头管理程序上发现，运维能用一个只读查询的请求号直接改到设备固件：准入表只比了请求号本身，方向位和大小位根本没参与判定，缓冲区长度也没校验。另一次事故里，同一台机器换了新固件之后，旧设备上几百条放行规则被继续套到新设备上，一份本该只给旧型号看的寄存器全被读了出来。值班同学还发现，两个进程同时透传同一个读请求时，返回的数据互相串了。

落点在 `gsb/batch51-go-safety-d-10` 的 `ioctl_policy_guard.go`，类型 `IoctlPolicyGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-d-10`。

公开接口保持：`NewIoctlPolicyGuard(cfg GuardConfig) *IoctlPolicyGuard`、`Register(device string, ruleSet RuleSet) error`、`Check(device string, req IoctlRequest, cred Cred) (Decision, error)`、`Forward(device string, req IoctlRequest, buf []byte) ([]byte, error)`、`Revoke(device string, ruleID string) error`、`Audit(device string) []AuditEntry`、`SetHandler(device string, h Handler) error`、`Decode(code uint32) IoctlInfo`、`ReplaceDevice(device string) (uint64, error)`、`Rules(device string) []Rule`、`Generation(device string) (uint64, error)`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. 设备名为空白报 `ErrBlankDevice`，规则集为空报 `ErrEmptyRuleSet`，规则 ID 为空白报 `ErrBlankRule`，同一设备的规则 ID 重复报 `ErrDuplicateRule`；被拒的注册不建立设备也不占用代次。
2. 规则里的请求号方向位为空却带着非零大小位时报 `ErrRuleMismatch`，这种号永远不许进规则表。
3. `Decode` 要把请求号拆成方向、大小、类型与编号四段，`Check` 的结论里也要原样带出这四段，方便复盘时对得上。
4. `Check` 未命中任何规则时返回拒绝并记 no_rule，不许回落到放行，也不许因为设备存在就默认允许。
5. 命中规则但规则已被撤销时记 rule_revoked；撤销不存在的规则报 `ErrUnknownRule`，重复撤销幂等成功。
6. 规则要求管理能力而调用方没有时报 privilege_required，带管理能力的调用方照常放行。
7. 缓冲区长度与大小位不一致要拒绝并记 buffer_length_mismatch，长度过大同样拒绝，只有完全相等才放行。
8. 请求号方向位为空却写了大小位时直接按 request_code_invalid 拒绝，不许先走到规则匹配。
9. 只写方向的请求携带的输入长度仍然要与大小位一致，不能因为不读回就跳过校验。
10. 同一请求号在不同设备上的语义可能不同，规则表按设备存放，不能按请求号做成全局表复用。
11. 检查与透传之间设备不能被替换，`Forward` 要么落在同一代次上要么直接拒绝；同一请求的并发透传不许共用同一块可变缓冲区。
12. 设备被替换之后旧代次的规则整体失效，已经打开的句柄在关闭时回收，不能继续拿旧规则放行。

硬约束：检查与透传必须串行裁决同一设备的规则表，设备代次在两次调用之间不许静默变化；`Forward` 交给处置器的缓冲区必须是私有副本，处置器写坏它也不能影响调用方的数据；审计要按设备逐条留痕、序号严格递增，放行与拒绝都要记录规则号与原因；快照要带规则集、设备代次与审计水位，恢复后新注册的代次必须大于此前任何一条，损坏快照报 `ErrBadSnapshot` 且不动现有状态。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发透传与重启恢复自行构造程序验证；只做本地实现。交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-d-10`
- 实现文件：`ioctl_policy_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`IoctlPolicyGuard`
