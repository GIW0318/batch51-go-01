# HTTP 消息签名验签与 nonce 防重放

## 题面

API 网关切到 HTTP Message Signatures 之后，支付通道连续出现两类工单。一类是正常重试被拒：签名基重建出来和客户端算的不一致，细看是 header 里的连续空格与制表符被网关原样带入，客户端却按规范把它们折叠成了单个空格；另一类是重复请求被放行，同一个 nonce 在两台网关上各消费一次，事后对账多出一笔。更麻烦的是某把密钥前一天已经吊销，当天早上补发的签名因为 created 早于吊销时刻被一并拒掉，业务方认为按签名时刻判定就该放行；运维还发现 @query 组件被重新排过序，签名自然对不上。三类工单叠在一起，网关侧只能看到一句签名不匹配，排障全靠人肉比对原始报文；联调时同一批重放请求在两台网关上结果不同，说明判定和记账没有放在一处。落点 gsb/batch51-go-safety-a-09 的 http_signature_verifier.go，类型 HttpSignatureVerifier，包名 task。

接口保持：NewHttpSignatureVerifier(cfg VerifierConfig) *HttpSignatureVerifier、Verify(req SignedRequest) (Signer, error)、RegisterKey(keyID string, key KeyMaterial) error、RevokeKey(keyID string, at time.Time) error、Explain(req SignedRequest) (Trace, error)、Keys() []string、SigningBase(req SignedRequest) (string, error)、NonceCount(keyID string) int、Revoked(keyID string) (time.Time, bool)、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：一、签名基按 signature-input 的组件顺序逐行拼出，每行为引号包住的组件名加冒号加空格加值，行间用换行符连接，末尾不留空行；二、@method 取大写方法名，@path 只取问号前的路径，@query 保持线上原序不重排，@authority 取 host 头；三、header 组件名一律小写，同名头多值按出现顺序用逗号加空格连接，值去掉首尾空白并把连续空白折叠成一个空格；四、引号内的内容不做空白折叠，原样保留；五、组件重复报 ErrDuplicateComponent，header 组件名出现大写报 ErrComponentCase，未覆盖 @method 报 ErrMissingMethodComponent，未知组件报 ErrUnknownComponent；六、created 与 expires 同注入时钟比对，created 偏离超过 MaxSkew 报 ErrClockSkew，expires 已过期报 ErrSignatureExpired；七、密钥在签名时刻必须有效，尚未生效报 ErrKeyNotYetValid，已过期报 ErrKeyExpired；八、密钥在验签时已吊销、但签名时刻早于吊销时刻的，按签名时刻判定放行并在 Signer 里标注审计信息，签名时刻等于或晚于吊销时刻一律拒绝；九、content-digest 被覆盖时要核对正文摘要，不一致报 ErrDigestMismatch 且不消费 nonce，流式读取要在读完之前中止；十、nonce 按 keyID 加 nonce 一次性消费，重复报 ErrNonceReplay，超过 MaxNoncesPerKey 报 ErrNonceCapacity；十一、Keys 按 keyID 升序返回，签名比对用常量时间比较，不一致报 ErrBadSignature，摘要与签名都通过后才登记 nonce，Signer 带回 keyID、主体、签名时刻与按序组件列表；十二、快照要带密钥环、吊销水位与 nonce 表，恢复后旧的 nonce 仍要拒绝。

硬约束两条：nonce 的判定与写入在同一临界区内完成，同一 nonce 的并发请求只放行一个；密钥环、吊销水位与 nonce 表并发安全，单 keyID 的 nonce 容量、签名基长度与组件数设硬顶，越界报错而不是截断。快照与恢复要么全量生效要么完全不动，损坏载荷回 ErrBadSnapshot 且现网状态不变。

验收：在模块目录执行 go test ./...，15 条必须全部通过；不得改动测试、跳过用例或按固定输入写死；并发与重启恢复场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-09`
- 实现文件：`http_signature_verifier.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`HttpSignatureVerifier`
