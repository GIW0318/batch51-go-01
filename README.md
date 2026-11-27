# Cookie 作用域判定与容器淘汰

## 题面

边缘代理最近被安全扫描连续点名：标记 __Host- 的登录 Cookie 出现在子域请求里，路径 /foo 下的会话 Cookie 被 /foobar 页面读到，还有供应商把 Domain 写成 com.cn 也能顺利存进容器；缓存节点重启后容器被清空，所有用户一起掉登录。值班同学翻审计日志，发现被拒的 Cookie 连一条记录都没有，根本说不清是哪一步放过的；扫描报告只给了两张截图，连请求样本都没有，复现全靠猜。落点在模块路径 gsb/batch51-go-safety-b-04、文件 cookie_scope_guard.go，类型 CookieScopeGuard，请把作用域判定、前缀约束与容器淘汰补完整。

关键接口保持：NewCookieScopeGuard(cfg CookiePolicy) *CookieScopeGuard、Store(origin string, c Cookie) error、Select(origin string, req Request) []Cookie、Delete(origin, name string) error、Evict(now time.Time) int、Explain(name string) (Scope, error)、Snapshot() ([]byte, error)、Restore([]byte) error；另外提供 Cookies(origin string) []Cookie、Clear(origin string) int、Audit() []CookieEvent 与 Stats() CookieStats。

要修的行为：一、Store 解析 origin 的 scheme 与 host；名字为空或含控制字符、分隔符报 ErrBadName；没有 Domain 属性按 host-only 处理，Domain 取 origin 主机名；Path 为空取 DefaultPath，不以斜杠开头报 ErrBadPath。二、Domain 属性去掉前导点并转小写后必须等于 origin 主机或为其父域，否则报 ErrDomainMismatch；Domain 落在公共后缀或顶级域上时整条报 ErrPublicSuffix，不能退化成 host-only 接受。三、Path 匹配按目录前缀并落在分隔符边界，/foo 只匹配 /foo 与 /foo/ 下的请求，不能匹配 /foobar。四、__Host- 前缀要求 Secure、Path 为 / 且没有 Domain；__Secure- 要求 Secure；任一条件不满足整条报 ErrPrefixViolation，不能存一半。带引号或八进制转义的值要按原样保留，校验前缀时不得先解码。五、Secure Cookie 从 http origin 写入报 ErrInsecureOrigin；SameSite=None 必须同时 Secure，否则报 ErrSameSiteNone；SameSite 只接受 lax、strict、none（大小写不敏感），其他值报 ErrBadSameSite。六、Select 同时按域、路径、Secure 通道与过期时间筛选：host-only Cookie 只发给完全相等的主机，不得发给子域；Secure Cookie 只在安全通道回注；结果按路径长度降序、同路径按创建时间升序。七、同名同域同路径的 Cookie 按 Created 新的覆盖旧的，旧记录不得反向覆盖新记录。八、Evict 清理过期条目并返回条数，累加 Evicted；每域条数、单条大小与容器总条数都有上限，单条超限报 ErrCookieTooLarge，域或总量溢出按最久未使用淘汰，同等条件下创建更早的先出。九、Delete 删除 origin 可见的同名 Cookie，没有则报 ErrCookieNotFound；Clear 返回删除条数；Cookies 与 Explain 返回稳定顺序和可解释作用域。十、Audit 记录每次拒绝与淘汰，Stats 给出 Total、Domains、Stored、Rejected、Evicted 计数。十一、快照要覆盖全部 Cookie 与索引，损坏快照报 ErrBadSnapshot 且不改动现网。

硬约束：Store、Select、Delete、Evict 与快照必须并发安全，容器按域与路径长度建索引也不能出现读到半更新状态；Snapshot 与 Restore 要么看到全量旧状态、要么看到全量新状态，Cookie、审计与计数跨重启可恢复。

验收：go test ./... 的 15 条必须全过，不得修改或跳过测试、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-04`
- 实现文件：`cookie_scope_guard.go`
- 包名：`task`；`tests/` 下的测试同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CookieScopeGuard`
