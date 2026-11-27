# 邀请绑定与里程碑奖励的分批解锁

## 题面

活动上线第二天客服台就炸了：有人拿自己注册时发的邀请码绑自己，系统照收；有玩家注册一周后才来绑定，页面提示已过期，后台却还是绑上了；同一个手机登录的两个号，奖励一起进了同一张余额；退款工单结掉后，已经领走的奖励没人追，余额也没变。邀请绑定与里程碑结算这套账本要做完，模块路径 gsb/batch51-go-game-a-08，实现文件 referral_bond_ledger.go，类型 ReferralBondLedger。

对外签名保持：NewReferralBondLedger(policy ReferralPolicy) *ReferralBondLedger、Register(accountID string, at time.Time) error、IssueCode(accountID string) (string, error)、Codes(accountID string) []string、Bind(inviteeID, code string, at time.Time) (Bond, error)、BindWithFingerprint(inviteeID, code string, fp Fingerprint, at time.Time) (Bond, error)、Accrue(inviteeID string, milestone Milestone, delta int64) error、Progress(inviteeID string) (Progress, error)、Claim(accountID string) (Grant, error)、Reverse(accountID, reason string) (Clawback, error)、Balance(accountID string) (int64, error)、BondOf(inviteeID string) (Bond, bool)、Quarantined() []string、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）账号号去空白后为空报 ErrEmptyAccount；重复注册报 ErrAccountExists。

（2）IssueCode 为账号生成邀请码，格式为账号加井号加四位补零序号，例如 alice#0001；每个账号最多 MaxCodesPerAccount 个，超出报 ErrCodeLimit；未知账号报 ErrUnknownAccount。Codes 按发放顺序返回。

（3）Bind 的邀请码不存在报 ErrUnknownCode；被邀请人绑自己的码报 ErrSelfReferral；已有绑定再绑报 ErrAlreadyBound 且原绑定不变。

（4）绑定时距被邀请人注册超过 BindWindowHours 报 ErrBindWindowClosed，恰好落在窗口内必须放行。

（5）邀请码绑定数达到 MaxBindingsPerCode 后，新绑定报 ErrCodeFull，错误里带出当前时刻加 RetryAfterSeconds 的可重试时刻；已经绑定的关系不受影响。

（6）同一设备或支付指纹已被其他账号的绑定占用时，新绑定照常记录 Code 与 BoundAt，但 Quarantined 为真并进入隔离队列；Quarantined 按账号字典序返回。

（7）Accrue 只接受已绑定且已注册的账号，否则报 ErrNotBound；增量非正报 ErrBadDelta；不识别的里程碑报 ErrUnknownMilestone。

（8）进度按里程碑分别累计；累计量达到 Thresholds 后解锁该里程碑，未达阈值不得提前解锁。

（9）绑定前已经达成的进度只补算绑定之后的增量，不回填绑定之前的部分。

（10）Claim 一次结清所有已解锁未领取的里程碑，Amount 为对应 Rewards 之和，Milestones 按里程碑固定顺序排列，Seq 每次成功 Claim 加一；重复 Claim 返回零金额与空列表，不报错，余额不变。

（11）Reverse 要求原因为非空，否则报 ErrBadReason；未绑定账号报 ErrNotBound，未知账号报 ErrUnknownAccount。已解锁未领取的奖励全额回收并标记为已撤销，累计进度保留；已经领取的部分按规则追偿，余额转负不得阻断玩家进入游戏，只在下一次结算时抵扣。

（12）快照带出账号、邀请码、绑定、进度、解锁与领取状态、余额与序号；恢复后能继续累计与领取，空快照或结构不合法报 ErrBadSnapshot 且不得改动现网状态。

硬约束两类。其一并发与原子性：同一账号的 Claim 并发只能结算一次，进度累加、解锁与余额写入必须原子，快照不得读到结算一半的账。其二容量与恢复：每个邀请码的绑定数与每个账号的邀请码数都是硬上限，达到上限只拒绝新增，不影响已有关系；重启后绑定关系、隔离队列与余额必须原样回来。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-08`
- 实现文件：`referral_bond_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ReferralBondLedger`
