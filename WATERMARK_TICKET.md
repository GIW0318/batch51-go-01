# 水位与票据对照说明

`ClockStepGuard` 把每一次时钟调整落成一条可回放的水位（`Watermark`），把每一次越限跳变的授权落成一张一次性票据（`Ticket`）。两者共用一把互斥锁提交，因此对账时总能把"谁批准的、跳了多少、跳之前水位停在哪里"对上。

## 水位（Watermark）

- 每次 `Apply` 真正生效才追加一条，序号 `Seq` 从 1 开始严格递增；被拒绝的调整（来源不可信、单调倒退、缺票据、位移不符）不写水位、不计步数。
- 字段对照：`Wall`/`Mono` 是跳变**后**的读数，`Delta` 是本次墙钟位移（向后跳为负），`Source` 是跳变来源（`ntp`/`manual`）。
- 跳变前的水位就是 `Seq-1` 那条（或 `SetWall` 基线），所以"跳之前水位停在哪里" = 上一条水位的 `Wall`/`Mono`。
- 历史只保留最近 `MaxWatermarks` 条，淘汰最旧的，保留段内序号仍严格递增。
- 下游算耗时必须用 `Mono`：墙钟向后跳时 `Mono` 不允许倒退（`ErrMonoRegression`），保证耗时不为负。

## 票据（Ticket）

- `Request(delta, reason, actor)` 串行发券：`Seq` 与 `ID`（`ticket-<seq>`）全局唯一，并发申请不会撞号；`reason`/`actor` 为空分别报 `ErrBadReason`/`ErrBadActor`。
- 在途（`issued` 且未过期）票据超过 `MaxTickets` 报 `ErrTicketLimit`；超过 TTL（2 分钟）未使用的票据自动作废为 `revoked`，释放在途额度。
- 票据只认申请时的位移：`Apply` 的实际 `Delta` 与票据不一致报 `ErrTicketMismatch` 且**不消耗**票据；撤销过的票据再用报 `ErrApprovalRequired`。
- 一张票据只应用一次：命中即置 `used`，重复 `Apply` 因找不到匹配的在途票据而报错。

## 对照关系

| 事件 | 票据侧 | 水位侧 |
| --- | --- | --- |
| 人工/NTP 越限跳变获批 | 一张 `issued` → `used` 的票据，`Delta` 与水位一致 | 新增一条 `Source` 对应、`Delta` 相同的水位 |
| 越限但无票据 | 无变化 | 无新水位，`Steps` 不变 |
| 位移与票据不符 | 票据保持 `issued` | 无新水位 |
| NTP 平滑累计越限 | 需匹配票据，命中后累计量清零 | 命中才写水位 |
| 越限跳变生效 | — | 订阅方同锁置 `paused` 且 `Pauses+1`，回到阈值内置 `active` |

## 快照与恢复

`Snapshot` 把墙钟、单调值、累计量、水位历史、全部票据（含状态与过期时间）和订阅暂停状态一起序列化；`Restore` 先校验再整体替换，损坏快照报 `ErrBadSnapshot` 且不动现网。恢复后水位序号、票据序号从快照值继续递增，可直接继续 `Apply`/`Request` 而对得上账。
