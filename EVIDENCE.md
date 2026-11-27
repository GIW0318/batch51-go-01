# XXE 探测证据归并口径与验证记录

## 归并主键
- 探测编号是唯一关联键。载荷中的实体名、回显标记 `GSB-<编号>`、回连 URL 的 `http://collector.gsb/<编号>` 都携带同一编号。
- 回连归属只按编号查探测记录，`SourceAddr` 仅作证据元数据。多个探测共用出口地址时，不会按来源地址猜测归属。
- 同一编号的多次回连全部保留在 `Callbacks` 中，`Correlate` 只输出一条，`Callbacks` 次数累加。

## 三路证据
- `echo`：响应体出现完整 `GSB-<编号>`，状态为 `confirmed`，`Echoed=true`，理由 `entity_expanded`。
- `oob`：能解析外部实体的入口自动登记同编号回连；回连命中后状态为 `confirmed`，理由 `callback_correlated`。
- `error`：入口拒绝 DOCTYPE 时响应标记 `Rejected`，记录状态为 `refused`，理由 `parse_rejected`；它不会产生回连。
- 响应体被 `MaxBodyBytes` 截断时只标记 `Truncated`，绝不标记 `Rejected`。若截断后看不到完整回显标记，本次不确认回显，理由 `body_truncated`。

## 状态与时间
- 完全未展开实体的待定探测保持 `pending`，理由 `entity_not_expanded`，既不算确认也不算不存在。
- 回连时间 `At > Submitted + CallbackWindow` 时标记 `Late`，回填原探测记录，不新建关联。
- 逻辑时钟严格超过截止时间后，`Expire` 只把仍为 `pending` 的探测改为 `uncertain`，理由 `callback_missing`；迟到回连仍可把原记录回填为 `confirmed`。
- 参数实体不走通用实体回显，只由 `ExpandsParameter` 触发的带外回连确认。

## 封顶、并发与快照
- 每个端点已提交探测数受 `MaxProbesPerEndpoint` 硬限制，超出返回 `ErrProbeBudget`；不驱逐既有探测。
- `MaxConcurrent` 用条件变量限流，`PeakInFlight` 记录每端点峰值；回连表达到 `MaxCallbacks` 后拒绝新回连，不覆盖已有记录。
- 快照包含端点、探测记录、回连、逻辑时钟、序号与统计；`Load` 先在临时状态完成解析和校验，全部通过后一次交换，损坏时返回 `ErrBadSnapshot` 且不触碰现网。

## 验证记录（2026-11-28）
- `GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod-cache go test ./...`：通过，现有 15 条验收全过。
- `GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod-cache go test -race -count=1 ./...`：通过。
- `GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod-cache go test -count=20 ./tests`：通过。
- `go vet ./...` 与 `git diff --check`：通过。
- 独立临时程序验证通过：参数实体仅 OOB、截断非拒绝、预算与回连表不覆盖、共享来源按编号归属、重启后可继续回填、120 路并发按编号隔离且峰值等于配置上限 3。
- 并发预算复核：100 路同时提交、单端点上限 10，结果严格为 10 条接受、90 条 `ErrProbeBudget`、记录数 10。
