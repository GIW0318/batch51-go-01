# 流水线配置外部触发面与作业令牌审计

## 题面

凌晨两点，一条由外部 PR 触发的流水线在检出提交之后拿到了可写令牌，把制品仓库的版本号直接覆盖掉了。复盘时发现审计工具只把"外部触发"当成一句告警文案，既没有看作业真正持有的权限，也没有看第三方步骤是以什么方式固定的。现在要把这套审计规则落到 `gsb/batch51-go-safety-c-31` 的 `ci_workflow_auditor.go` 里，类型名为 `CiWorkflowAuditor`。

配置按 JSON 文档给出，一次审计喂进多个文件，每个文件形如 `{"workflows":[...],"templates":[...]}`；作业可以用 `template` 字段引用同批载入的复用片段，片段里的步骤与权限要合进调用方，引用不到片段必须报错。入口签名保持：`NewCiWorkflowAuditor(limits Limits, internalOrgs []string) *CiWorkflowAuditor`、`Load(files []SourceFile) (*WorkflowSet, error)`、`Audit(set *WorkflowSet) ([]Finding, error)`、`Report(findings []Finding) string`、`ClassifyTrigger(trigger string) string`、`MergePermissions(caller, overlay PermissionSet) PermissionSet`、`HasWriteToken(perms PermissionSet) bool`、`ClassifyAction(uses string) string`、`IsMutableRef(uses string) bool`、`Interpolations(text string) []string`、`SortFindings(findings []Finding) []Finding`、`DangerousCombination(trigger, uses string, perms PermissionSet) (string, bool)`。

要落地的十一条：

1. `pull_request_target`、`workflow_run`、`issue_comment` 一类算外部触发，`push`、`schedule`、`workflow_dispatch` 算内部触发，其余事件一律归为未知，不许默认按内部处理；同一份配置里既有外部又有内部事件时，每个事件各自归类，不得取其中一个代表整份配置。
2. 合并权限时复用片段的声明覆盖调用方的同名条目；合并返回新映射，不得就地修改调用方传进来的那两个映射。
3. 合并后的权限里只要有一条 `write`，`HasWriteToken` 就必须为真。
4. 外部触发、检出代码、持有写令牌三者同时成立，`DangerousCombination` 返回组合规则名与真；少一项就返回空串与假。
5. 步骤引用没有提交号固定的（标签、分支、甚至完全不写引用）算可变引用；带四十位提交号的固定引用不算；`docker://` 用摘要固定也不算。
6. `./` 开头的本地动作、`docker://` 引入的镜像、配置里声明为组织内来源的动作、其余远端动作，要分成四类返回。
7. 表达式出现在 `run` 命令里按高危报；只出现在步骤名或 `with` 回显参数里按中危报，两处都有时取高的那条，不要重复计数。
8. 审计输出按严重度降序，同档再按文件路径、作业 ID、规则名升序，同一批输入重复审计顺序必须完全一致。
9. 一次审计的文件数超过 `Limits.MaxFiles`，或者所有文件正文合计超过 `Limits.MaxTotalBytes`，直接报 `ErrFileLimit`、`ErrTotalBytes`，不许给出半份结果。
10. 空的工作流集（一个作业都没有）不报错，返回空清单；传入空指针报 `ErrNilSet`。
11. `Report` 首行固定为 `findings=<总数> critical=<n> high=<n> medium=<n> low=<n> info=<n>`，之后每条问题占一行；没有任何问题时首行计数全部为 0，且只输出这一行，不要留空行。

硬约束：多个文件允许并发解析，但合并结果只由文件路径和文档内顺序决定，与协程完成先后无关；审计与报告对同一输入连续调用必须逐字节一致；文件数与总字节是硬顶，超限即拒，不做截断。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；并发解析与重复审计的稳定性自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-31`
- 实现文件：`ci_workflow_auditor.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CiWorkflowAuditor`
