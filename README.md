# iSCSI 会话登录的 CHAP 互认证、LUN 掩码与命令序列恢复

## 题面

块存储接入层升级后，几台主机重登时拿不到原来的盘，另有一台主机的会话被别人的发起者抢了 LUN；链路抖动后重放的命令还被执行了两遍，业务侧看到重复写。落点在 gsb/batch51-go-safety-c-10 的 iscsi_session_guard.go，类型 IscsiSessionGuard 负责 iSCSI 会话的登录认证与目标侧 LUN 授权。登录分阶段推进：先建会话拿挑战，再按策略完成双向 CHAP 应答，认证通过才允许映射 LUN；映射要看发起者掩码，恢复要按命令行号与任务标记判断是不是重放，认证失败的主机要有明确终态，不能停在半认证状态。

签名保持：NewIscsiSessionGuard(cfg IscsiPolicy) *IscsiSessionGuard；Login(portal string, req LoginRequest) (Session, error)；Auth(sessionID string, chap ChapResponse) error；MapLun(sessionID string, lun int, initiator string) (LunGrant, error)；Recover(sessionID string, cmdSN int) (Session, error)；Logout(sessionID string) error；Sessions() []Session；LunMask(initiator string) []int；SetLunMask(initiator string, luns []int) error；MaskGeneration() int；Snapshot() ([]byte, error)；Restore(data []byte) error。

要处理的十二处：① 门户、发起者或目标为空白仍能登录，应报 ErrInvalidLogin；② 会话数或同发起者会话数超过上限仍放行，应报 ErrSessionLimit；③ 策略要求双向认证时缺少目标侧应答也能通过，应报 ErrMutualAuthRequired 且会话停在 pending；④ 应答里的挑战号与会话不符仍被接受，应报 ErrChallengeMismatch，已消费的挑战再答一次应报 ErrChallengeReused；⑤ 失败应答没有次数上限，超过 ChapWindow 后仍可重试，应报 ErrAuthLocked 且该会话不能授权；⑥ 未完成认证就能映射 LUN，应报 ErrNotAuthenticated；⑦ 映射不看发起者掩码，掩码外的 LUN 应报 ErrLunDenied；⑧ 用别的发起者映射本会话的 LUN 也能成功，应报 ErrInitiatorMismatch；⑨ 单会话映射的 LUN 数超过上限仍放行，应报 ErrLunLimit，重复映射同一 LUN 保持幂等且不重复计数；⑩ 命令行号重放会重复执行，同一 CmdSN 重放必须幂等、只推进一次窗口，更大的 CmdSN 才累加命令数并更新 ExpCmdSN；⑪ 退出后会话仍可恢复或映射，应报 ErrUnknownSession，重复退出同样报错；⑫ 快照恢复丢掉会话状态、掩码与命令水位，应完整恢复，损坏快照报 ErrBadSnapshot 且不动现网。

两条硬约束：会话表与掩码的读取更新必须并发安全，同发起者的登录配额在并发下只放行到上限，掩码换代与授权判定不能看到半新半旧；快照保存会话状态、掩码代数与命令水位，恢复后水位只能前进不能被回退，会话数、每会话 LUN 数与掩码条目数都是硬上限。另有三处按现场口径处理：发起者 IQN 的大小写与末尾空格要归一后匹配掩码，不能按原串比较；LUN 掩码收窄后已建立的会话在重登前仍能看到旧 LUN，重登时按新掩码；会话恢复时 CmdSN 回退说明发起者重启，要作废旧会话而不是继续复用。

验收：go test ./... 的 15 条用例必须全过；测试不得修改、跳过或按固定输入返回常量；并发登录与重启恢复自行写程序验证；只做本地实现。交付源码、测试输出与一页以内的会话状态说明，写清从 pending 到 active 的推进条件与授权判定顺序，以及重登和旧会话清理的先后关系。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-10`
- 实现文件：`iscsi_session_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`IscsiSessionGuard`，会话是 `Session`，授权结果是 `LunGrant`
