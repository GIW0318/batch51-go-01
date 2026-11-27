# 目录口令策略的过期宽限与强制改密状态机

## 题面

目录服务接入统一口令策略后，服务台一上午收到四类投诉：一是有人连续输错三次被锁，等了二十分钟仍然进不去，只能重装客户端；二是口令早就过期的账号照样能进业务系统，安全扫描直接判了高危；三是改密页面提示新口令不合规，可这条口令明明符合长度和字符类要求，原因是历史口令比对拿了错误的基准；四是管理员重置口令之后，用户手上的旧会话还在继续用，重置形同虚设。请把这套绑定前的口令裁决与变更记账补齐，模块路径 gsb/batch51-go-safety-a-04，实现文件 directory_password_policy.go，类型 DirectoryPasswordPolicy。

对外签名保持：NewDirectoryPasswordPolicy(cfg PolicyConfig) *DirectoryPasswordPolicy、Provision(account, tenant, initialPW string) error、Bind(account, pw string, now time.Time) (BindResult, error)、Change(account, oldPW, newPW string) (ChangeResult, error)、Reset(account, tempPW string, mustChange bool) error、Status(account string) (AccountStatus, error)、Policy(tenant string) Policy、SetPolicy(tenant string, p Policy) error、Sessions(account string) []string、Authorize(account, sessionID string) error、HistoryDepth(account string) int、GraceUsed(account string) int、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）账号、租户或口令为空报 ErrMissingField；租户没有策略报 ErrUnknownTenant；同一账号重复开户报 ErrAccountExists。

（2）初始口令与临时口令都要按租户策略校验长度、字符类与最大长度，不合规报 ErrPasswordPolicy。

（3）绑定依次判定账号状态、口令正确性、过期时间与宽限次数；口令错误累加失败次数并返回剩余次数，达到上限转入锁定，锁定期内即使口令正确也报 ErrAccountLocked，锁定窗口过去之后自动解锁且失败计数清零。

（4）口令过期但宽限次数还没用完时放行本次绑定，结果标记必须改密并给出剩余宽限次数。

（5）宽限次数用完仍然过期，绑定报 ErrPasswordExpired，且不得留下会话。

（6）处于宽限状态或被重置成临时口令的会话只能用于改密，用这类会话做别的事一律拒绝，不能因为口令正确就放行。

（7）改密要校验旧口令，新口令不合规报 ErrPasswordPolicy，旧口令不符报 ErrBadPassword。

（8）新口令在历史深度内出现过就报 ErrPasswordReused；历史比对必须覆盖当前口令，改成与现在一模一样同样算复用。

（9）改密成功后清掉失败计数与宽限计数、口令年龄归零，并吊销该账号全部既有会话，结果里给出吊销条数。

（10）管理员重置要把旧口令与全部既有会话立即作废，临时口令只能用于改密，改密前不得用于其它操作。

（11）策略增删改要校验自身合法性，不合规报 ErrPasswordPolicy；策略版本变化后下一次绑定必须按新策略重算，不能沿用上次的判定结论。

（12）快照要带出账号、历史摘要环、宽限计数与会话索引，恢复后判定结论一致；损坏的快照报 ErrBadSnapshot 且不改动现网。

硬约束有两类。其一是串行与原子：同一账号的绑定计数、改密与重置必须串行处理，失败累加与会话吊销不能只做一半；宽限计数与历史摘要在并发改密下不得出现两条记录同时生效。其二是时间与容量：一切过期、锁定与口令年龄判定都以配置里注入的时钟为准，不许直接读系统时间；历史摘要环、宽限计数与会话索引都有容量上限，超过按最旧淘汰，账号数不设无限增长。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发改密与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-04`
- 实现文件：`directory_password_policy.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DirectoryPasswordPolicy`
