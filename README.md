# 命令白名单、参数转义与元字符注入审计

## 题面

发布系统把命令执行收归统一白名单之后，安全组复盘了一起注入事故：运维把用户提交的镜像名前缀直接拼进 systemctl 的参数，攻击者填了一个带分号的字符串，整条命令在宿主机上多执行了一段；同一周还发现有人用软链接把白名单里的工具指到别的程序上，白名单只认链接路径，没认真实目标；另有脚本通过环境变量前缀写入动态库路径，审计里只留了命令名，环境那一半完全查不到。请把模块路径 gsb/batch51-go-safety-r-03、实现文件 cmd_injection_audit.go 里的 CmdInjectionAudit 补齐。

对外签名保持：NewCmdInjectionAudit(cfg AuditConfig) *CmdInjectionAudit、AllowProgram(path string, opts AllowOptions) error、Programs() []string、RegisterLink(link, target string) error、Resolve(program string) (string, error)、Check(inv Invocation) (Verdict, error)、ScanArgs(args []string) error、CheckEnv(env []string) error、Escape(args []string) (string, error)、Denied(source string) int、Audit() []AuditEvent、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的故障与边界：

（1）程序路径不是绝对路径报 ErrNotAbsolute，白名单条目缺路径或参数上限为负报 ErrBadAllowRule，两种情况都不得留下条目。

（2）白名单按完整真实路径匹配，同一条目重复登记幂等；条目数按 MaxPrograms 硬顶，超了报 ErrProgramLimit。

（3）软链接登记后 Resolve 逐跳跟随到真实路径；链长超过 MaxLinkHops 报 ErrLinkDepth；成环报 ErrLinkCycle，不得返回中间路径当结果。

（4）命令名与参数分开校验：参数里出现分号、竖线、与号、重定向、括号、反引号或美元括号替换一律拒绝，结论码 cma_metachar，不能只过滤命令名。

（5）参数里出现换行或制表符时同样按元字符拒绝，不能因为不在常见的 shell 特殊字符表里就放行。

（6）白名单命令经软链接指向非白名单目标时，按解析后的真实路径重新裁决，不能拿链接路径命中白名单。

（7）参数个数超过条目上限（条目没设就用全局 MaxArgs）拒绝，结论码 cma_arg_limit；截断后继续执行不允许。

（8）环境变量赋值前缀只允许白名单键，出现其它键报 ErrEnvKey；值里出现命令替换要拒绝整个调用，报 ErrEnvSubstitution。

（9）环境变量条目没有等号报 ErrBadInvocation，不允许猜键名继续。

（10）Escape 对每个参数做单引号包裹，参数内的单引号转成 '\''，空参数给一对空引号；入参为空报 ErrBadInvocation。

（11）每次裁决写审计，带自增序号、来源、解析后程序、结论码、是否放行与参数个数；按来源统计拒绝次数供阈值告警。

（12）快照要带出白名单、软链接表、拒绝计数与序号水位，恢复后序号接着涨；损坏快照报 ErrBadSnapshot 且不得改动现网状态；审计表按 MaxAudit 封顶。

硬约束两类：一是并发与原子性，裁决与白名单登记并发时裁决只能看到完整的旧表或新表，审计序号只能发一次；二是上限与失败关闭，白名单、软链接跳数、参数个数、环境变量长度都是硬边界，超限一律拒绝而不是截断放行。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-r-03`
- 实现文件：`cmd_injection_audit.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CmdInjectionAudit`、`Invocation`、`AllowOptions`、`Verdict`、`AuditEvent`、`AuditConfig`
