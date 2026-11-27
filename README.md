# 审计日志哈希链的断点、重放与截断识别

## 题面

审计日志上了哈希链之后，合规审计第一次抽查就退回了：一季度的日志能被截断后重新追加，链头还能对上，序号却从别的地方续起；同一段记录被搬到另一个位置，校验仍判成完整；还有人趁夜里并发写入的时刻跑校验，报告里全是断链，第二天复跑又干净。合规口径也不接受一句链坏了，要求指出哪一段、哪一条最先出问题，并能把这一段单独取出来做证明。请把模块路径 gsb/batch51-go-safety-r-04、实现文件 audit_log_chain_verifier.go 里的 AuditLogChainVerifier 补齐，让每次校验都能指出第一条出问题的记录。

对外签名保持：NewAuditLogChainVerifier(cfg ChainConfig) *AuditLogChainVerifier、ComputeDigest(r Record) string、ProofRoot(digests []string) string、Append(actor, action, detail string, at int64) (Record, error)、Import(r Record) (Record, error)、Load(records []Record) error、Verify() (VerifyReport, error)、VerifyRange(from, to int64) (VerifyReport, error)、RangeProof(from, to int64) (RangeProof, error)、VerifyProof(p RangeProof) error、Seal(at int64) (Checkpoint, error)、Checkpoints() []Checkpoint、VerifyAgainst(cp Checkpoint) error、Truncate(seq int64) error、Head() (Checkpoint, error)、Record(seq int64) (Record, bool)、Len() int64、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的故障与边界：

（1）记录缺操作者或动作、序号非正时报 ErrBadRecord，不得入链，也不得占用序号。

（2）序号必须紧接链头加一，前驱摘要必须等于链头摘要，两条都查；只对上一条时分别报 ErrSeqGap 与 ErrPrevMismatch，不能用同一个码糊过去。

（3）摘要按序号、操作者、动作、细节、前驱与时间点重算，与记录里的值不符报 ErrDigestMismatch；Import 要拒收伪造摘要，Load 只做原样装载，供事后检查损坏的日志。

（4）校验要定位到第一条断点，报告里带 BreakAt 与结论码，可选值为 alc_gap、alc_prev_mismatch、alc_digest_mismatch、alc_truncated、alc_ok，不能只说链坏了。

（5）VerifyRange 的窗口超出链头时按截断报 alc_truncated，BreakAt 给第一个缺失序号；窗口倒置报 ErrBadRange。

（6）日志被截断后重新追加时，链头哈希与序号都要与 checkpoint 对上，只对哈希不够。

（7）同一记录被搬到另一个位置时按序号与哈希双匹配拒绝，不能只比哈希。

（8）并发追加时序号分配与链哈希更新必须原子，校验读到半写状态要重试而不是报断链。

（9）RangeProof 只覆盖闭区间 from 到 to，Count 与 Digests 长度一致，Root 由窗口摘要折叠；VerifyProof 用 Root 重算，篡改任意一条报 ErrProofMismatch。

（10）单次证明窗口受 MaxProof 硬顶，超了报 ErrProofTooLarge，不允许悄悄截断窗口凑数。

（11）Seal 记下当前序号、链头摘要与封存时刻；VerifyAgainst 与 checkpoint 的序号或摘要任一不符报 ErrCheckpointMismatch，两条都要比。

（12）记录数按 MaxRecords 硬顶，超了报 ErrCapacity，不淘汰旧记录；快照要带出记录与 checkpoint 表，恢复后序号接着涨，损坏快照报 ErrBadSnapshot 且不得改动现网状态。

硬约束两类：一是并发与原子性，多路写入同时追加时序号只能发一次，链头摘要随记录一起提交，任何时刻读到的链头都能与已有记录自洽；二是容量与恢复，记录表、证明窗口、checkpoint 表都有硬顶，重启后从快照恢复的记录必须能通过全量校验。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-r-04`
- 实现文件：`audit_log_chain_verifier.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AuditLogChainVerifier`、`Record`、`Checkpoint`、`RangeProof`、`VerifyReport`、`ChainConfig`
