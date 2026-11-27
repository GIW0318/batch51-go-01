# Webhook 签名校验：原始字节、时间窗、重放缓存与密钥换版

## 题面

投诉：支付回调这条链路周末出了两件互相矛盾的事故。第一件是网关给正文加了一层 JSON 规范化之后，同一笔回调在我们的服务上被判成签名不符，渠道方咬定是我们算错了；第二件是安全同学从流量里翻出一个上周抓到过的回调，原样重放了一遍，我们不但收了，还当成新事件又发了一次货。紧接着渠道开始换密钥，老 secret 和新 secret 有一段并行期，值班同学不知道该按哪一版验，索性两版都试了一遍，结果同一请求在两个版本上各验一次，重放记录被绕开。

落点在 `gsb/batch51-go-safety-r-05` 的 `webhook_signature_guard.go`，类型 `WebhookSignatureGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-r-05`。

公开接口保持：`NewWebhookSignatureGuard(cfg GuardConfig) (*WebhookSignatureGuard, error)`、`AddKey(version string, secret []byte) error`、`RetireKey(version string) error`、`Sign(version string, timestamp int64, body []byte) (string, error)`、`Verify(req SignedRequest) (Verdict, error)`、`PurgeExpired(now int64) int`、`ReplayEntries() int`、`KeyCount() int`、`CurrentVersion() string`、`Audit() []AuditRecord`、`Stats() GuardStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `WindowSeconds`、`MaxAudit`、`MaxReplay` 任一不为正报 `ErrBadConfig`；`Clock` 为空时退回系统 Unix 秒。
2. 版本号去空白后为空、secret 为空报 `ErrBlankKey`；同一版本重复登记同一 secret 幂等成功，换成别的 secret 报 `ErrKeyConflict`。
3. 签名用 HMAC-SHA256，密钥取该版本 secret，消息是版本号、十进制时间戳、原始正文字节三段以换行拼接，输出小写十六进制；版本未登记报 `ErrUnknownKey`。
4. `Verify` 的判定顺序固定：请求字段完整性报 `ErrBadRequest`、版本可解析、时间窗、签名比对、重放登记；时间戳与当前时间相差超出窗口即拒，早于窗口和晚于窗口一样处理，原因 `reason_timestamp_skew`。
5. 签名必须落在请求携带的原始字节上，正文被中间件改写过的部分不能拿解析后的结构重新序列化再算。
6. 同一签名在窗口内只接受一次，第二次报 `ErrReplay`，原因 `reason_replay`；重放缓存的键就是签名，同一时间戳下的不同签名是两件事。
7. 换版并行期新旧两版都可用，但一次请求只能占一个版本的重放名额，不能换个版本再验一次。
8. 重放缓存上限 `MaxReplay`，写入前先清掉已过期记录；清理之后仍然满就拒绝新请求并报 `ErrReplayCapacity`，不许为了腾位子丢掉还在窗口内的记录，缓存水位要能通过 `PurgeExpired` 与 `ReplayEntries` 观察到。
9. 每次判定都进审计，序号严格递增，只保留最近 `MaxAudit` 条；`Stats` 同时给出接受、拒绝、重放与密钥四个计数。
10. 退役不删记录，退役版本的签名与验签都报 `ErrKeyRetired`；`CurrentVersion` 取最近登记且未退役的版本，全部退役返回空串。
11. 快照要带上密钥、重放缓存、序号、审计与计数；恢复之后同一签名仍然拦住，序号继续往上走，审计与计数器不能归零；损坏快照报 `ErrBadSnapshot` 且不动现网。
12. 同一请求被多路并发投递时只允许一路验过，比对与登记落在同一临界区里完成，不得出现先比对后登记的空档，也不得写出两条接受记录。

验收：`go test ./...` 15 条全过；测试不得修改、不得跳过、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-r-05`
- 实现文件：`webhook_signature_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`WebhookSignatureGuard`、`GuardConfig`、`SignedRequest`、`Verdict`、`AuditRecord`、`GuardStats`
