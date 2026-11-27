# 权限复核活动的快照冻结与逐项幂等裁决

## 题面

上一季度的权限复核活动结束后，审计翻出两件事：同一批授权项被两位复核人各点了一次，系统按后一次的结果回收，前一次的留痕丢失，事后没法解释为什么放行了本该回收的权限；活动截止时还有十几条裁决在提交途中，系统直接把它们当成未裁决项丢弃，既没有回收也没有留下记录，下一季度这批权限原样又回到名单里。还有人反馈，活动关闭之后新建的授权被带进了本季度的结果，复核结论和快照对不上。请把这套复核活动补成可用的实现。落点在 `gsb/batch51-go-safety-a-15` 的 `access_review_campaign.go`，类型 `AccessReviewCampaign`，包名 `task`，模块路径 `gsb/batch51-go-safety-a-15`。

公开接口保持：`NewAccessReviewCampaign(cfg CampaignConfig) *AccessReviewCampaign`、`Grant(account, permission string) error`、`RevokeGrant(account, permission string) error`、`Open(campaignID string, at time.Time) (CampaignState, error)`、`Decide(campaignID, itemID, reviewer, decision string, at time.Time) (DecisionResult, error)`、`Close(campaignID string, at time.Time) (CloseResult, error)`、`Progress(campaignID string) (Progress, error)`、`Items(campaignID string) ([]Item, error)`、`Campaign(campaignID string) (CampaignState, bool)`、`Abort(campaignID string) error`、`Audit(campaignID string) ([]AuditEntry, error)`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：活动编号为空或纯空白报 `ErrBadCampaign`，重复打开同一编号报 `ErrCampaignExists`；打开时把当时账上的授权整份冻结成快照，账号与权限归一为去空格小写，并按账号、权限升序排列；打开之后新增的授权不得进入本轮快照；裁决只接受 keep 与 revoke，其它值报 `ErrBadDecision` 且不改变任何状态；同一授权项只接受一次有效裁决，相同裁决重复提交要幂等成功并返回 `Applied` 为 false，返回的序号与第一次一致，不同裁决报 `ErrAlreadyDecided`；不存在的授权项报 `ErrUnknownItem`，不存在的活动报 `ErrUnknownCampaign`；关闭时把未裁决项按默认策略处理，`DefaultRevoke` 为真则回收、否则保留，`Defaulted` 列出这些项，`Kept` 与 `Revoked` 按编号升序给出；关闭要一次提交回收动作与留痕，重复关闭报 `ErrAlreadyClosed`；关闭后进度的待裁决数归零，保留数、回收数与总数要和结果一致；裁决过程中进度如实反映总数、已裁决数与待裁决数；审计按活动记录 opened、decided、closed 与递增序号；快照要导出活动、冻结项、裁决与序号水位，损坏快照报 `ErrBadSnapshot` 且不改动现网，恢复后继续裁决的序号必须大于此前任一条。

另有三处只在事故复盘里提过、没有进入常规回归的边界：裁决期间授权被上游撤销时，该项要以撤销为准，不能按复核人的裁决继续保留；截止时刻仍有裁决在途时，要按已受理未完成处理，不能直接丢弃；活动被中止后重开，必须沿用原快照而不是重新采集。

硬约束：同一活动内的裁决必须串行提交，同一授权项在并发下只能产生一次有效裁决，不同活动之间不得互相阻塞；关闭与快照必须互斥，快照只能看到全量已结算或全量未结算的活动，不能读到结算到一半的中间态；冻结项数按 `MaxItems` 硬顶，超限时拒绝开新活动而不是截断快照。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发裁决、关闭与重启恢复自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-15`
- 实现文件：`access_review_campaign.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AccessReviewCampaign`
