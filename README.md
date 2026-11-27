# 目录绑定代理的凭据转发与连接池重绑定

## 题面

目录代理换到第二版之后，运维同时收到两类反馈：一批服务账号在目录侧被锁，锁定计数比实际登录次数高出一大截，日志里同一个身份在同一时刻有七八个绑定请求同时打到上游；另一件事更麻烦，某个账号绑定失败后改用另一个账号复用同一条池化连接，居然直接成功了，上一条连接上的身份没有被重新认证。安全组还发现错误页把上游的锁定提示和剩余尝试次数原样回显给了前端，等于把锁定计数暴露给了攻击者。请把这段绑定代理补成可用的实现。落点在 `gsb/batch51-go-safety-a-12` 的 `ldap_bind_proxy_guard.go`，类型 `LdapBindProxyGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-a-12`。

公开接口保持：`NewLdapBindProxyGuard(cfg ProxyConfig) *LdapBindProxyGuard`、`NormalizeDN(dn string) (string, error)`、`Bind(dn, password string, level SecurityLevel) (string, error)`、`Rebind(connID, dn, password string) error`、`Release(connID string) error`、`Conn(connID string) (ConnInfo, bool)`、`PoolState(level SecurityLevel) PoolState`、`MarkStale(connID string) error`、`Stats() ProxyStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：条目名归一要去掉各段两侧空白、属性类型转小写、等号两侧空白一并去掉，值本身的大小写保持原样，归一结果必须幂等；条目名为空、缺少等号、属性名为空、中间出现空段都报 `ErrBadDN`；不支持的安级别（负数或高于 TLS）报 `ErrBadLevel`，并且不触达上游；非匿名绑定必须带非空条目名，否则报 `ErrBadDN`；同一身份同一安全级别的空闲连接优先复用，连接编号保持不变，但复用前必须重新向上游绑定一次；不同安全级别各占一个池，互不复用；每个安全级别的存活连接数超过 `MaxConnsPerLevel` 报 `ErrPoolExhausted`，被拒的请求不触达上游；上游返回非零结果码时按失败返回 `BindError`，带结果码与原始诊断文本，文本里不得出现口令；失败的绑定不得在池里留下连接；`Release` 幂等，重复释放不重复计入空闲槽，未知连接报 `ErrUnknownConn`；`Rebind` 换身份必须重新向上游认证，成功后连接沿用原编号，未知连接报 `ErrUnknownConn`；快照导出连接、计数与序号水位，不含任何口令，损坏快照报 `ErrBadSnapshot` 且不改动现网，恢复后连接编号保持不变。

另有三处只在事故复盘里提过、没有进入常规回归的边界：匿名绑定与空口令绑定必须分开处理，空口令不得被当成匿名放行；上游返回的锁定提示要透传，但剩余尝试次数只能留在代理内部，不得出现在错误文本或返回值里；池中连接被上游单方面关闭时，重新绑定只允许重试一次，第二次仍然失败就把连接移出池子，不能反复重试。

硬约束：同一身份的并发绑定必须串行，避免把上游的锁定计数放大，不同身份之间必须真正并行，一个被拒的身份不能拖住整批请求；快照与恢复必须整体成功或整体拒绝，恢复后新分配的连接序号大于此前任一条；每安全级别的连接数与在途绑定都按配置硬顶，任何一路超限都不许覆盖已有连接。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发绑定与重启恢复自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-12`
- 实现文件：`ldap_bind_proxy_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`LdapBindProxyGuard`
