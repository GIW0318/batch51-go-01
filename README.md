# HTTP/2 连接合并的源一致性治理

## 题面

线上反向代理开启 HTTP/2 连接复用后，图片域名的请求开始间歇性返回 421，另有一小部分请求被转到了错误的源站，灰度回滚后现象消失；抓包显示同一个连接上先后出现过两个域名，其中一个并不在证书里。落点 gsb/batch51-go-safety-b-06 的 coalescing_origin_guard.go，类型 CoalescingOriginGuard，签名保持：NewCoalescingOriginGuard(cfg OriginPolicy) *CoalescingOriginGuard、RegisterConn(conn ConnInfo) error、CanCoalesce(conn ConnInfo, authority string) (bool, error)、Advertise(origins []string) (OriginFrame, error)、Install(connID string, frame OriginFrame) error、Check(conn ConnInfo, req Request) (Verdict, error)、RotateCert(connID string, cert Certificate) error、OriginsOf(connID string) []string、Stats() Stats、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的故障与边界：

一、连接登记以 ID 为唯一键，同一 ID 再次登记时整体替换证书、端口与对端，不新增条目；ID 为空或只有空白报 ErrBadConn，证书没有任何 SAN 报 ErrBadCertificate。

二、CanCoalesce 与 Check 都按 conn.ID 读取登记表里的当前连接状态，参数里的其余字段只在登记时生效；ID 未登记报 ErrUnknownConn。

三、authority 必须带 https 方案与主机名，缺方案、端口越界、主机含非 ASCII 字符或空主机一律报 ErrBadAuthority；主机名先转小写并去掉末尾的点再比较。

四、证书 SAN 覆盖才可合并：精确同名或通配符 *.父域 覆盖一级标签，而通配符既不覆盖父域本身、也不覆盖两级以上的子域；方案不在 AllowedSchemes 内、连接协议不是 h2、端口与 authority 的有效端口不一致时返回 false 且不报错。

五、未覆盖的 authority 返回 false、nil，不抛错；对端解析一致性通过 OriginPolicy.Resolve 注入，解析结果不含连接对端时同样不合并，即使证书覆盖。

六、Advertise 丢弃非法与重复候选，按规范形式（默认 443 省略）去重后升序排序，超过 MaxAdvertised 截断并置 Truncated；一条可用都没有报 ErrNoOrigin。

七、Install 只接受已登记连接，空帧报 ErrEmptyFrame，条数超过上限报 ErrOriginLimit，成功后 OriginsOf 返回声明列表的副本。

八、已装声明集合非空时，证书覆盖但未声明的源回 421，原因 reason_not_declared；声明集合为空表示该连接不限制目标源。回 421 之后连接必须继续服务原有源，客户端在原连接上重试原源请求仍要成功，不能把整条连接关掉。

九、Check 对非法 authority 回 400 且原因 reason_bad_authority，对明文请求回 400 且原因 reason_scheme_rejected，正常放行回 serve、状态 0、原因 reason_served，并把 ConnID 与规范源填进判定。

十、RotateCert 换证后按新证书重算覆盖，旧名字立刻失效；空证书或未知连接分别报 ErrBadCertificate、ErrUnknownConn，失败不得改动现网证书。

十一、Stats 汇总连接数、判定次数与三类结果计数，三类结果之和必须等于判定次数。

十二、快照覆盖连接、声明集合与计数器，Restore 要么整体生效要么整体不动，坏载荷报 ErrBadSnapshot。

硬约束：连接表、声明集合与计数器的读写必须并发安全，判定与换证可以同时发生且不得互相阻塞到错乱；快照只能看到全量旧状态或全量新状态，恢复后连接键、声明集合与计数继续可用。

验收：go test ./... 的 15 条用例全部通过，不得修改测试、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现，不接外部依赖。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-06`
- 实现文件：`coalescing_origin_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CoalescingOriginGuard`
