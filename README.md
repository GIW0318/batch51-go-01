# 客户端完整性证明：一次性挑战、模块哈希清单、版本钉扎与降级放行

## 题面

周五大版本更新后，登录队列里出现一批奇怪的掉线：玩家客户端明明刚校验通过，进对局三分钟就被踢回登录界面，日志里写着同一个挑战号被用了两次；另一批玩家只是显卡驱动多挂了一个覆盖层模块，就被整份判成篡改，账号直接进了安全队列。运维还发现，清单切到新版本之后，更新到一半的客户端按旧版本校验也被拒，玩家重登三次才偶然成功。

落点在 gsb/batch51-go-game-a-18 的 client_integrity_attestation.go，类型 ClientIntegrityAttestation，包名 task，模块路径 gsb/batch51-go-game-a-18。公开接口保持：NewClientIntegrityAttestation(limits AttestLimits, clock func() time.Time) *ClientIntegrityAttestation、IssueChallenge(session string) (Challenge, error)、Verify(session string, proof Proof) (Verdict, error)、SetManifest(ver string, manifest Manifest) error、SetPolicy(p AttestPolicy) error、MarkModuleDirty(module string) error、Revoke(session string) error、Explain(session string) []Reason、Stats() AttestStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) IssueChallenge 的会话号不能为空白，否则 ErrBadSession；被撤销的会话返回 ErrSessionRevoked；每次签发返回不同的挑战号与随机串，并记录签发时刻、过期时刻与签发时的清单版本。
2) 未消费且未过期的挑战数达到 MaxChallenges 时，新签发返回 ErrChallengeLimit，不得驱逐仍有效的挑战。
3) Verify 的会话号或证明归属会话不一致返回 ErrBadProof；挑战不存在或不属于该会话返回 ErrChallengeNotFound；已消费返回 ErrChallengeUsed；超过过期时刻返回 ErrChallengeExpired。
4) 挑战一次性使用：第一次 Verify 无论放行还是拒绝都记为已消费，之后的重复提交一律拒绝。
5) 清单按模块名匹配，哈希或大小不一致返回拒绝，原因码 module_mismatch，并在 Explain 里记下模块名；全部一致返回放行与 verified。
6) MarkModuleDirty 标记的模块在后续校验中直接拒绝，原因码 module_dirty；模块名空白返回 ErrBadModule；标记对已签发未校验的挑战同样生效。
7) 证明里缺少清单中的模块或该模块标记为读取失败时，按 OnReadFailure 策略处置，不得一律判为篡改。
8) SetManifest 的版本号不能为空白、条目不能为空、模块名与哈希不能为空白、同一版本内不得重名、单模块大小不得超过 MaxModuleSize，否则返回 ErrBadManifest 或 ErrManifestLimit；超限不截断，旧版本继续服务已签发的挑战。
9) SetPolicy 的挑战有效期必须为正，OnMismatch 与 OnReadFailure 只能是放行、限功能与拒绝之一，否则 ErrBadPolicy；要求签名时证明签名为空返回 ErrBadProof。
10) Revoke 幂等，重复调用返回成功；撤销后的会话既不能签发也不能校验，错误均为 ErrSessionRevoked。
11) Explain 返回该会话最后一次判定按原因码与模块名排序的原因列表；Stats 汇总签发数、消费数、校验数、放行数、限功能数与拒绝数。
12) 快照保存挑战表、清单各版本、策略、会话判定与计数；恢复后已消费的挑战仍然不可重放，新挑战按恢复后的清单版本签发；载荷损坏返回 ErrBadSnapshot 且不动现有状态。

硬约束：IssueChallenge、Verify、Stats 与快照会被登录网关与对局网关并发调用；同一挑战只能产生一个判定，其余并发提交必须拿到已消费而不是第二次放行；挑战表、清单表与判定表的读写要互斥，快照只能看到完整状态。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-18`
- 实现文件：`client_integrity_attestation.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ClientIntegrityAttestation`
