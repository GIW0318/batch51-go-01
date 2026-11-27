# 入站发件人授权求值与查询预算

## 题面

邮件网关升级后，来自合作伙伴域的正常邮件开始被判定为伪造，另一些邮件在 DNS 超时后反而被放行；同一发件域在很短时间内被重复查询，把上游递归服务器打满，同样的记录在不同节点上还给出不同结论，值班同事只能逐个域手工比对。落点 gsb/batch51-go-safety-b-09 的 spf_eval_engine.go，类型 SpfEvalEngine，签名保持：NewSpfEvalEngine(cfg SpfConfig) *SpfEvalEngine、SetResolver(r Resolver) error、Evaluate(ip netip.Addr, sender, domain string) (SpfResult, error)、Explain(domain string) (Trace, error)、ResolveRecord(domain string) (Record, error)、Record(domain string) (string, bool)、Expire(now time.Time) int、Stats() Stats、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的故障与边界：

一、记录必须以 v=spf1 开头；同域发布两条及以上 SPF 记录按 permerror；一条都没有按 none 且不报错；记录本身的 TXT 查询计入查询数。

二、机制按记录顺序求值，qualifier 缺省为 +；all 命中立即按 qualifier 收尾，all 之后的机制一律忽略；redirect 只能出现一次且只能在 all 之后，与 all 同时出现时 redirect 无效；没有机制命中又没有 redirect 时结果为 neutral。

三、ip4 与 ip6 按前缀匹配，地址族不同按不命中；a 与 mx 按机制里给出的域名（宏展开后）查 A，带 /N 时按前缀比较；mx 的每个交换器 A 查询都计入预算。

四、include 递归求值目标域：目标 pass 时当前机制命中并取该机制自己的 qualifier；目标 fail、softfail、neutral 时继续下一个机制；目标 temperror 必须整体返回 temperror，不得当成 fail 继续；目标 permerror 或 none 返回 permerror。

五、exists 查宏展开后的 A，有地址即命中；ptr 按 PTR 结果与目标域的后缀匹配，为控制预算不做正向确认。

六、宏支持 %{s}、%{l}、%{o}、%{d}、%{i} 与 %{dN}（取域名最右 N 个标签并反转）；未知宏字母、空展开或展开结果含空白、@、斜杠等非法字符按 permerror，不能当作 exists 不命中。

七、DNS 查询总数（含记录查询、include 与 mx 展开）超过 MaxDNSQueries 返回 permerror，不许返回 pass；机制触发的空回答计入 void，超过 MaxVoidLookups 同样返回 permerror。

八、递归深度超过 MaxRecursion、机制数超过 MaxMechanisms 都按 permerror 处理，不得静默截断记录。

九、SpfResult 要带回命中机制的规范文本（显式 qualifier）、qualifier、查询数、void 数与原始记录；redirect 命中时机制文本填 redirect=目标域，qualifier 留空；命中结果缓存的求值把 Cached 置真。

十、结果按地址、域与记录指纹缓存，TTL 到期或记录变更即失效；同域并发求值做单飞合并，缓存条目受 MaxCacheEntries 限制，满了按最久未使用淘汰，不允许无界增长。

十一、Explain 返回按顺序的机制链，Result 固定为 parsed，缺记录报 ErrNoRecord；ResolveRecord 返回解析后的机制与 redirect。

十二、快照覆盖记录缓存、结果缓存与计数器，Restore 要么全量生效要么全量不动，坏载荷报 ErrBadSnapshot。

硬约束：记录缓存、结果缓存、单飞合并与 TTL 过期必须并发安全；查询预算与 void 预算按单次求值累计，一次求值里的 include 与 mx 展开共享同一份预算，不得各算各的。

验收：go test ./... 的 15 条用例全部通过，不得修改测试、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现，不接外部依赖。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-09`
- 实现文件：`spf_eval_engine.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SpfEvalEngine`
