# 客户端证书链、吊销状态与固定指纹的联合准入

## 题面

网关侧的客户端证书准入连续出了三起事故：一家合作银行的中间证书轮换后，几台终端拿着残缺的链仍然进了内网，事后才发现请求里少带了一级签发证书；同一个业务主体名下有两张证书，一张已经进了吊销表，另一张还在有效期里，结果两张一起被挡在门外，业务方投诉说吊销不该按主体名连坐；还有一台换过证书的旧终端，指纹早就对不上，却因为有历史登记而照常通过。请把模块路径 gsb/batch51-go-safety-r-01、实现文件 mtls_chain_revocation_guard.go 里的 MtlsChainRevocationGuard 补齐，让每次准入都能说清依据。

对外签名保持：NewMtlsChainRevocationGuard(cfg GuardConfig) *MtlsChainRevocationGuard、Trust(anchor CertRecord) error、TrustedAnchors() []string、AddRevocation(rec RevocationRecord) error、SetRevocationSource(available bool)、IsRevoked(serial, issuer string) (bool, error)、CheckChain(chain []CertRecord, now int64) ([]CertRecord, error)、Decide(chain []CertRecord, now int64, peerFingerprint string) (Decision, error)、Pin(cert CertRecord, startAt, endAt int64) error、Pins() []PinEntry、Audit() []AuditRecord、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的故障与边界：

（1）证书记录缺序列号、主体、签发者、指纹或签名来源任一项，连信任锚在内一律按残缺记录拒绝，报 ErrBadCertificate，且不得留下半条登记。

（2）链必须按叶子在内、信任锚在最外排列；相邻两张的签名来源与指纹对不上、而该签名来源实际出现在链里更靠后的位置时，报 ErrChainOrder，不能顺手重排。

（3）链中间少一级签发证书，签名来源在链内和锚表里都找不到时报 ErrChainIncomplete，不能把叶子直接挂到最近的锚上放行。

（4）链尾证书的签名来源不在信任锚表里报 ErrUnknownAnchor；非叶子证书没有 CA 位报 ErrNotCA。

（5）有效期按判定时刻逐张检查，任何一张不在 NotBefore 到 NotAfter 之间报 ErrCertificateExpired；叶子证书的 KeyUsage 不含 clientAuth 报 ErrKeyUsage。

（6）链长度超过 MaxChainDepth 报 ErrChainDepth，截断后继续验等于放行伪造链，不允许。

（7）吊销按签发者与序列号这一对做精确查询，命中才拒绝，命中后裁决为拒绝、原因码 mtc_revoked。

（8）吊销源不可达时一律按拒绝处理，原因码 mtc_revocation_unavailable，不能因为查不到就当没吊销。

（9）登记固定指纹时校验起止时刻与表容量，超容量报 ErrPinLimit；已存在的指纹重复登记幂等。

（10）裁决时固定指纹只用来比对本次链的叶子证书，与叶子指纹不符即拒绝并给出 mtc_pin_mismatch。

（11）每次裁决无论放行还是拒绝都写一条审计，带自增序号、叶子序列号、链深度与结论码，审计表按 MaxAudit 封顶。

硬约束两类：一是并发与原子性，多个连接同时裁决时审计序号只能发一次，快照不得读到半更新的锚表、吊销表或指纹表；二是恢复与上限，快照要带出锚表、吊销表、指纹表与序号水位，恢复后裁决序号接着涨，损坏的快照报 ErrBadSnapshot 且不得改动现网状态，审计表与指纹表都有硬顶。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-r-01`
- 实现文件：`mtls_chain_revocation_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`MtlsChainRevocationGuard`、`CertRecord`、`RevocationRecord`、`PinEntry`、`Decision`、`AuditRecord`、`GuardConfig`
