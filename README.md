# WS-Trust 令牌签发续期撤销与持有证明密钥协商

## 题面

企业联合登录端点换到新版本后，值班同事连收三张单：一个业务系统在超时重发后拿到两张不同编号的令牌，审计里同一个请求出现两行签发；另有用户在凭据快照过期前刷新失败，报错写着续期窗口已关闭，可他从没改过受众；最严重的是撤销过的令牌在另一个业务系统里还能验签通过，只因为同族派生出来的旧令牌没有被一起作废。请把令牌服务补起来，模块路径 gsb/batch51-go-safety-a-03，实现文件 ws_trust_issuer.go，类型 WsTrustIssuer。

对外签名保持：NewWsTrustIssuer(cfg IssuerPolicy) *WsTrustIssuer、Issue(req RequestSecurityToken) (Response, error)、Renew(req RequestSecurityToken) (Response, error)、Cancel(tokenID string, proof ProofOfPossession) error、Validate(tokenID, appliesTo string) (Claims, error)、Explain(tokenID string) (Trace, error)、TokensFor(principal string) int、FamilyOf(tokenID string) (string, bool)、IsRevoked(tokenID string) bool、KeyMaterial(tokenID string) (string, bool)、DeriveKey(serverEntropy, clientEntropy []byte) ([]byte, error)、Stats() IssuerStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）主体或受众为空报 ErrMissingField；请求里的有效期留空取策略默认值，写进响应与到期时间。

（2）请求有效期超过策略上限报 ErrLifetimeTooLong，整份请求不落库，不许截断后照发。

（3）每主体活跃令牌数、单受众令牌数各有上限，超了分别报 ErrPrincipalQuota 与 ErrAudienceQuota，被拒请求不占名额。

（4）签发按主体与受众建立令牌族；同一请求标识因超时重发再次到达时，必须按指纹幂等返回同一张令牌，响应标出幂等标记，活跃令牌数不增加。

（5）持有证明密钥由服务端熵与客户端熵合成，客户端熵为空时只用服务端熵，服务端熵为空报 ErrEmptyEntropy，合成结果按 sha256 取定长密钥。

（6）续期只允许在到期前的续期窗口内进行，窗口外报 ErrRenewWindowClosed；续期不得更换受众，换了报 ErrAudienceChange。

（7）续期成功后旧令牌进入被取代状态，再验签报 ErrTokenSuperseded 并留下墓碑；新令牌继承同一个令牌族，续期次数加一。

（8）验签依次判定未知、已撤销、被取代、已过期与受众不符，未知报 ErrUnknownToken，过期报 ErrTokenExpired，受众不符报 ErrAudienceMismatch。

（9）撤销必须校验持有证明密钥，不符报 ErrProofMismatch 且不动令牌；校验通过要级联撤销同族全部派生令牌，并让验签立刻失败。

（10）撤销与续期产生的墓碑按原到期时间保留，墓碑条数有上限，超出按最旧淘汰。

（11）所有到期与窗口判断都用策略里给的时钟函数，不许直接读系统时间，否则注入时钟的用例无法复现。

（12）快照要带出令牌表、令牌族、墓碑与计数器，恢复后验签结论与撤销状态一致；损坏的快照报 ErrBadSnapshot 且不改动现网。

硬约束有两类。其一是并发与原子性：同一令牌族的签发与撤销要串行，令牌编号分配必须原子，并发的续期与撤销不能让同一张令牌既是活跃又是撤销。其二是恢复与容量：重启后令牌族与墓碑都要能续上，活跃令牌数、单受众令牌数与墓碑条数都按配置硬顶，不允许超限后靠淘汰活跃令牌凑数。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启由提交方自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-03`
- 实现文件：`ws_trust_issuer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`WsTrustIssuer`
