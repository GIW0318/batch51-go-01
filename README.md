# Cookie 作用域探测：域与路径匹配、前缀约束与跨子域覆盖的可写性判定

## 题面

客服反馈同一品牌下两个子域的登录态会互相踢掉：营销站写的一份同名 Cookie 把应用站的那份盖住了，应用站再写回来又把营销站顶掉。安全同事去复现时更糊涂了，面板把 Domain 写成上级域的那条记录标成了"仅本机"，路径为 `/app` 的 Cookie 也被判成只覆盖目录。现在要的是确定结论：谁能覆盖谁、覆盖方向是什么、前缀到底有没有生效。这套判定要补到 `gsb/batch51-go-safety-c-44` 的 `cookie_scope_prober.go`，类型名 `CookieScopeProber`，构造签名 `NewCookieScopeProber(limits Limits) *CookieScopeProber`。

接口：`Jar(host string) (CookieJar, error)`、`Set(host string, c CookieSpec) (SetResult, error)`、`Delete(host, name, path string) error`、`Match(req Request) ([]CookieMatch, error)`、`Judge(matches []CookieMatch) (ScopeVerdict, error)`、`Verdict(host, path string) (ScopeVerdict, bool)`、`Verdicts() []string`、`Budget() (BudgetState, error)`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。

要落地的十二条：

1. `Set` 校验：主机为空或带斜杠报 `ErrBadHost`；名称为空报 `ErrBadName`；值不带 `gsb-` 前缀报 `ErrBadValue`；路径不以斜杠开头报 `ErrBadPath`；SameSite 不认识报 `ErrBadSameSite`。
2. 没写 Domain 的按 host-only 存，域字段直接填主机本身，`HostOnly` 置真；Path 留空归一成 `/`；被接受时 `Reason` 为 `reason_stored`。
3. 写了 Domain 的先去前导点、转小写，必须等于主机或者是主机的点边界后缀，且至少两段，否则报 `ErrDomainMismatch`。
4. `SameSite` 取值大小写不敏感；取 `none` 时必须带 `Secure`，否则报 `ErrInsecureSameSite`。
5. 前缀单独校验：`__Secure-` 不带 `Secure`、`__Host-` 不满足 Secure 加 Path 为 `/` 加无 Domain 时，返回 `Accepted` 为假、`Prefix` 为 `invalid`、`Reason` 为 `reason_prefix_violated`，既不报错也不入库；合规的带前缀 Cookie 正常入库，并把前缀填进结果。
6. 同一主机下同名同域同路径再次写入是替换，`StoreID` 保持不变；路径不同则各占一格。
7. `MaxAge` 为负表示删除，返回 `Accepted` 为假、`Reason` 为 `reason_deleted`；删除不存在的 Cookie 不报错。
8. 每主机条数上限由 `MaxPerHost` 约束，超了淘汰最早写入的那条，淘汰条数写进 `SetResult.Evicted`；`Jar` 按写入先后列出。
9. 匹配：host-only 的只发给该主机，带 Domain 的发给该域本身及其子域；路径按请求路径前缀匹配；`Secure` 的 Cookie 只在安全请求上发。
10. SameSite 过滤：跨站请求下 `strict` 不发，`lax` 只在 GET 上发，`none` 还需 `Secure` 才发；站内请求全部可发。
11. 同名多条只发一条，路径最长优先、同长按写入先后；返回值按发送顺序编号 `SendOrder`。
12. 判定：同名出现两条判 `reason_ambiguous_scope`；只有一条但带 Domain 判 `reason_parent_domain`；全是 host-only 判 `reason_host_only`；`Winners` 用 `名称@域|路径` 的形式给出。结论按主机加请求路径记录，同一组合只留最后一份；没有匹配报 `ErrNoMatches`。

另外三条容易判反：未带 Domain 的 host-only Cookie 与写成上级域的同名 Cookie 不会互相覆盖，方向要分清；`__Host-` 缺任一项都算前缀失效，而不是退化成普通 Cookie；路径匹配就是请求路径前缀，`/app` 的 Cookie 也会发给 `/application`，别按目录边界理解。

硬约束：只用 `gsb-` 开头的标记值，不许出现真实凭据；`Set`、`Match` 与 `Judge` 并发调用时注册表与判定表要串行裁决，同一主机同一路径只留一份结论；快照保存全部 Cookie 与判定组合，恢复后操作计数与条数不得清零；`MaxOps` 为 0 表示不限，超了报 `ErrBudget`。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-44`
- 实现文件：`cookie_scope_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CookieScopeProber`
