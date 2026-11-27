# 可验证凭证签发的报价绑定与 deferred 事务轮询

## 题面

凭证签发服务上线 deferred 模式后，三个租户先后报障：一个租户的客户端在弱网下重试，同一张凭证被签出两次，审计里躺着两条互不相同的凭据流水；另一个租户已经撤销的申请在半小时后又收到了凭证邮件；还有租户反馈报价早就过期了，手里先前换出来的授权却还能继续提交持有证明。请把这条签发链路补齐，模块路径 gsb/batch51-go-safety-a-02，实现文件 vci_deferred_issuer.go，类型 DeferredCredentialIssuer。

对外签名保持：NewDeferredCredentialIssuer(cfg IssuerConfig) *DeferredCredentialIssuer、CreateOffer(req OfferRequest) (Offer, error)、Redeem(offerID, preAuthCode string) (Grant, error)、SubmitProof(grantID string, proof ProofJWT) (Issued, error)、Poll(transactionID string) (Issued, error)、Cancel(transactionID string) error、GrantFor(grantID string) (Grant, bool)、TransactionStatus(transactionID string) (string, error)、NonceConsumed(cnonce string) bool、TenantPending(tenant string) int、Offers(tenant string) []Offer、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）租户、主体、预授权码、绑定键任意一项为空都报 ErrMissingField，被拒的请求不占任何配额。

（2）批量条数留空按 1 处理，超过配置上限报 ErrBatchTooLarge，不许悄悄截断成上限值。

（3）每租户未兑换报价数与在途事务数都有上限，超了报 ErrTenantQuota，别的租户照常受理。

（4）报价只能兑换一次，重复兑换报 ErrOfferConsumed；预授权码不符报 ErrBadPreAuth，且不能把这张报价消耗掉。

（5）报价超过自身有效期兑换报 ErrOfferExpired，被拒的兑换不许改标记。

（6）授权有自己的有效期，报价后续过期不应连带已兑换的授权失效，能否提交持有证明只看授权自身还剩多久。

（7）持有证明里的 c_nonce 必须等于授权下发的那串，否则报 ErrNonceMismatch；证明的 jwk 指纹与授权绑定的不一致报 ErrBindingMismatch；aud 没指向本签发者报 ErrAudienceMismatch；以上三种拒绝都不得消费 nonce。

（8）同一份 proof 被并发提交两次只能成功一次，第二次要报 ErrNonceUsed，不能重新签一张。

（9）不需要人工审核的请求即时签发，状态 issued 且带凭证；需要审核的落成 pending 事务，响应里给出建议轮询间隔。

（10）轮询早于响应给出的间隔报 ErrPollTooSoon；单事务轮询次数到上限报 ErrPollExhausted；已经出凭证的事务再轮询只返回同一张凭证，不得重新签发。

（11）被取消的事务之后再轮询要报终态，不能因为后台审核完成又补发凭证。

（12）快照要带出未完成的事务、已消费的 nonce 与报价的兑换标记，恢复后能接着推进；损坏的快照报 ErrBadSnapshot 且不改动现网。

硬约束有两类。其一是并发与原子性：nonce 消费与事务状态迁移必须原子完成，同一事务的并发轮询只允许一个推进，其余请求不得看到半迁移状态。其二是容量与重启：每租户在途事务数、单事务轮询次数、批量签发条数都有硬上限；快照恢复后未完成事务与已消费 nonce 都要续上，已出凭证的事务不得再签一次。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发提交与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-02`
- 实现文件：`vci_deferred_issuer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DeferredCredentialIssuer`
