# 取值链口径

## 1. 来源与层

- 来源固定为 `host`、`x_forwarded_host`、`request_line`，判定顺序固定为直连、转发、绝对请求行。
- 层固定为 `frontend`、`backend`、`cache`、`template`。重定向目标只看前端层，生成链接只看后端层，缓存键只看缓存层，邮件模板只看模板层。
- 每层独立判断：仅当该层配置的来源等于本次请求来源时，才读取本次来源对应的字段；否则该层使用 `app.internal`。不会从其它请求字段补值。

## 2. 各来源取值

- `host`：读取 `HostHeader`。
- `x_forwarded_host`：按逗号拆分并按层选择；前端取第一个非空值，其它层取最后一个非空值。
- `request_line`：从 `GET http://<host>/ HTTP/1.1` 的 authority 取值。
- 直连请求只填 `HostHeader`，转发请求只填 `Forwarded`，绝对请求行只填 `RequestLine`。

## 3. 结论与链路

- `Observation.Location = https://<前端值><RedirectPath>`。
- `Observation.Link = https://<后端值><LinkPath>`；启用 `EscapeLinks` 时只改变输出编码，不改变“值已进入链接”的污染结论。
- `Observation.CacheKey = v1:<缓存值>`。
- `Accepted` 表示至少一层实际取到注入值；`Layer` 取最靠前的生效层（前端、后端、缓存、模板），`UsedSource` 为本次来源。
- `Judge` 的三条 `SourceResult` 固定按来源顺序排列；接受时 `Value` 是该来源最靠前生效层的实际值，未接受时为回退值 `app.internal`。
- `Verdict.Layered` 在同一来源的不同层取到不同值，或不同来源分别驱动不同层时为真。典型例子：转发头为 `first, last`，前端得到 `first`、后端得到 `last`。
- 模板层单独生效时，页面重定向、链接与缓存键均回退，但判定仍标记模板层污染；`MailPreview` 可渲染模板链接作为取证。

## 4. 并发、预算与恢复

- 同一入口的整套判定串行执行；全局在途受 `MaxConcurrent` 约束。
- 每入口提交总数硬顶为 `MaxProbesPerEndpoint`，预算不足时 `Judge` 在提交前返回 `ErrProbeBudget`，不会留下半轮记录。
- 标记全局单调分配，提交后保持已用；回收只清空该入口的活动标记，旧标记不会再次使用。
- 快照包含策略顺序、各入口记录、来源结论、判定、计数、活动标记与序列水位；带校验和并严格解码。重载先完整验证再整体替换，失败返回 `ErrBadSnapshot` 且不改变现网状态。
