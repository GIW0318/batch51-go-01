# 客户端证书主体到账号的映射求值与缓存失效

## 题面

mTLS 接入网关切到新的映射服务后，客服和安全组各报了一串问题：几台机器换了证书主题里的 OU，服务账号全部掉线，日志只写没有可用的映射；一个业务证书的三条 SAN 里写着不同的邮箱，被映射到了其中第一个邮箱对应的账号，实际持有人对不上；运维把一条旧规则删掉之后，某个已经解析过的证书还在用旧账号进系统，直到重启网关才恢复；还有租户抱怨规则一多就报错，没人说得清到底卡在哪个上限。请把这个从证书身份到内部账号的映射组件补齐，模块路径 gsb/batch51-go-safety-a-05，实现文件 cert_principal_mapper.go，类型 CertPrincipalMapper。

对外签名保持：NewCertPrincipalMapper(cfg MapperConfig) *CertPrincipalMapper、AddRule(tenant string, rule MappingRule) error、RemoveRule(tenant, ruleID string) error、Rules(tenant string) []MappingRule、Resolve(tenant string, cert CertView) (Principal, error)、Explain(tenant string, cert CertView) (Trace, error)、ResolveMany(tenant string, certs []CertView) ([]Principal, error)、Invalidate(certFP string) error、Revoked(certFP string) bool、CacheSize() int、Stats() MapperStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）租户、规则编号、匹配值、账号任意为空都报 ErrMissingField；证书没有指纹也报 ErrMissingField；租户为空时按配置里的默认租户解析。

（2）匹配器只认完整 DN、CN、SAN-DNS、SAN-email、SAN-URI 与颁发者指纹六种，其它值报 ErrBadMatcher；优先级为负报 ErrBadPriority；同租户重复的规则编号报 ErrRuleExists。

（3）每租户规则条数有上限，超了报 ErrRuleQuota；删除不存在的规则报 ErrRuleNotFound。

（4）规则按优先级数字从小到大返回，同优先级按编号排；数字小的先参与匹配。

（5）归一化口径：DN 与 CN 去首尾空白后转小写，SAN 里的 DNS 名再去掉尾部点，邮箱只把域名部分转小写、本地部分保持原样，SAN-URI 去空白后整体转小写，颁发者指纹大小写不敏感。

（6）没有任何规则命中时报 ErrNoMapping，绝对不许拿 CN 直接当账号回退。

（7）同一张证书同时命中两条不同优先级的规则时取优先级数字最小的那条；优先级相同又同时命中两条，判冲突报 ErrMappingConflict。

（8）证书携带的 SAN 条数超过上限报 ErrSANQuota，超限的证书不参与任何匹配。

（9）SAN 里出现多个邮箱、且它们被不同规则映射到不同账号时整张证书拒绝，不能取第一个命中的了事。

（10）解析结果按租户加证书指纹缓存，命中缓存只累加命中计数，不计入解析计数；缓存条目数有上限，满了按最旧淘汰。

（11）吊销某个指纹后，该证书再解析一律报 ErrCertRevoked，缓存里对应的条目同时丢弃，重复吊销必须幂等。

（12）规则增删要让该租户已缓存的结论失效；快照要带出规则、吊销集合与计数器，恢复后解析结论一致，损坏的快照报 ErrBadSnapshot 且不改动现网。

硬约束有两类。其一是并发与一致性：规则增删与解析并发进行时，解析方只能看到增删前的整套规则或增删后的整套规则，不能读到半套；同一指纹的并发解析只允许一次真正求值，其余走缓存。其二是容量与恢复：每租户规则数、缓存条目数与单证书 SAN 数都按配置硬顶，超限必须报错而不是静默丢弃；重启后规则、吊销集合与计数都要能续上。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发解析与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-05`
- 实现文件：`cert_principal_mapper.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CertPrincipalMapper`
