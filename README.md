# 代理链转发头剥离与真实客户端还原

## 题面

风控同事发现限流名单里的地址和审计日志对不上：日志里的客户端地址全是入口负载均衡器的地址，有人手工在请求里塞一段 X-Forwarded-For 就绕过了按 IP 的限流；还有一次链路中间出现内网地址，还原逻辑一路向左找，最后把公网代理当成了客户端。服务重启后缓存整表清空，短时间解析压力翻倍，入口延迟明显上升。落点在模块路径 gsb/batch51-go-safety-b-05、文件 forwarded_chain_resolver.go，类型 ForwardedChainResolver，请把可信跳剥离、头选择与结果缓存补完整。

关键接口保持：NewForwardedChainResolver(cfg TrustConfig) *ForwardedChainResolver、TrustCIDR(listener, cidr string) error、Resolve(remote netip.AddrPort, hdr http.Header, listener string) (ClientInfo, error)、Explain(listener string, hdr http.Header) (Trace, error)、Snapshot() ([]byte, error)、Restore([]byte) error；另外提供 Trusted(listener string, addr netip.Addr) bool、Listeners() []string、SetHeaderPolicy(listener, header string) error、Invalidate() int、CacheStats() CacheStats 与 Stats() ResolverStats。

要修的行为：一、TrustCIDR 按 listener 登记可信网段，CIDR 解析失败报 ErrBadCIDR；Listeners 返回有可信网段的 listener 并保持稳定顺序。二、远端地址不在该 listener 的可信网段内时，整段转发头一律忽略，客户端就是远端地址，Hops 为 0，原因记 untrusted_peer；尚未登记任何可信网段的 listener 按全部不可信处理，不能报配置错误。三、远端可信时按 listener 选定的头族从右往左剥离：跳过可信代理，遇到的第一个不可信地址就是客户端；即使这个地址是私网，只要它不在可信网段内也必须停下，不能继续往左找公网地址。四、Forwarded 与 X-Forwarded-For 同时出现时只使用 listener 配置选定的那一种，另一种不得参与判定；两者结论冲突时以选定头为准，并在 Trace 里置 Conflict、累加 Conflicts。五、X-Forwarded-For 按逗号切分后逐项去空白，出现空元素按不可解析报 ErrEmptyElement，不能跳过空项继续；_ 开头的混淆标识在未允许时报 ErrObfuscated；未知 token 报 ErrUnknownToken。六、每个地址做归一：去端口、去 IPv6 方括号、IPv4-mapped IPv6 还原为 IPv4；地址非法报 ErrBadAddress。七、Forwarded 解析 for= 与 proto=，允许带引号；proto 写入 ClientInfo.Proto，缺省为 http；其他参数视为未知 token。八、链长度超过 MaxChain 报 ErrChainTooLong，头值总字节超过 MaxHeaderBytes 报 ErrHeaderTooLarge。九、解析结果按 (listener, 远端地址, 头指纹) 缓存，第二次命中时 Cached 为真并累加 Hits；可信网段或头族策略一变就整表失效并累加 Invalidations；缓存条目数受 MaxCacheEntries 限制。十、ClientInfo 的 Hops 填头里实际参与判定的元素个数，Header 填选中的头族名；Explain 返回逐跳的原始值、归一地址、是否可信与角色。十一、快照要覆盖 listener 的可信网段与头族策略，损坏快照报 ErrBadSnapshot 且不改动现网；Requests、Rejected、Conflicts、Invalidations 都要计数。

硬约束：TrustCIDR、Resolve、Explain 与快照必须并发安全，信任表变更与解析不能交错出半新半旧的结果；Snapshot 与 Restore 要么看到全量旧状态、要么看到全量新状态，可信网段、头族策略与计数跨重启可恢复。

验收：go test ./... 的 15 条必须全过，不得修改或跳过测试、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-05`
- 实现文件：`forwarded_chain_resolver.go`
- 包名：`task`；`tests/` 下的测试同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ForwardedChainResolver`
