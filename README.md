# 限流绕过治理：键归一、两层配额与突发桶隔离

## 题面

投诉：网关侧按客户端维度做的限流，上周被抓到两个口子。第一个口子是大小写和百分号编码：同一个调用方把客户端标识写成 `App-Alice`、`app-alice`、`app%2Dalice` 三种形态，限流器当成三个不同客户端各建了一个桶，配额被放大三倍，一夜之间把下游的风控接口打满。第二个口子是租户配额和全局配额各算各的：某个租户自己的额度还早着，全站额度已经见底，实现却让这笔请求借了别家租户的余量过去，事后对账发现总量超了四成。还有一个说不清的地方是突发桶和稳态桶的关系，有人报白天第一秒能过一串、后面就卡死，也有人报反过来，值班换了几次参数都没调平。

落点在 `gsb/batch51-go-safety-r-08` 的 `rate_limit_bypass_guard.go`，类型 `RateLimitBypassGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-r-08`。

公开接口保持：`NewRateLimitBypassGuard(cfg LimitConfig) (*RateLimitBypassGuard, error)`、`NormalizeKey(tenant, client string) (string, error)`、`Reserve(tenant, client string, now int64) (Reservation, error)`、`Commit(res Reservation) error`、`Cancel(res Reservation) error`、`Allow(tenant, client string, now int64) (Decision, error)`、`Refill(now int64)`、`Quota(tenant string) QuotaState`、`Global() QuotaState`、`Buckets() []BucketState`、`Stats() LimitStats`、`Rejections() []Rejection`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `BurstTokens` 不得为负，`SteadyTokens`、`RefillPerSec`、`TenantQuota`、`GlobalQuota`、`MaxBuckets`、`MaxAudit` 任一不为正报 `ErrBadConfig`。
2. `NormalizeKey` 先对租户和客户端各做首尾去空白，再做大小写折叠，再做一次百分号解码，输出 `租户/客户端`；两端任一为空报 `ErrBlankKey`。
3. 归一之后的键才是桶的身份：同一个客户端换个写法必须落到同一个桶，不能因为写法不同多建桶；桶的统计与拒绝日志也按归一后的键记录，否则事后对不出是谁打满的。
4. `Reserve` 一次占两样东西：一个配额名额，以及桶里的一个令牌；桶里先扣突发再扣稳态。
5. 配额分两层：先看租户自己的，再看全局的；租户还有余量而全局已经满了要按全局拒绝，报 `ErrGlobalQuota`，不许挪用别家租户的余量。
6. 桶表达到 `MaxBuckets` 之后再建新桶报 `ErrBucketLimit`，已有的桶继续可用。
7. 突发与稳态都空时 `Reserve` 报 `ErrRateLimited`；`Allow` 把拒绝原因写进 `Decision`，被拒的 `Allow` 不得留下没有释放的配额占用。
8. `Commit` 关闭预约并释放配额名额，`Cancel` 关闭预约、释放名额并把令牌还回原来的桶；重复关闭报 `ErrReservationClosed`，未知预约报 `ErrUnknownReservation`，空预约号一样按未知处理。
9. `Refill` 按经过的秒数给每个桶补稳态令牌，补到稳态上限为止，突发额度不随时间回补。
10. `Stats` 给出请求数、放行数、拒绝数、桶数与在途预约数；`Rejections` 只记被拒的尝试，序号严格递增，保留最近 `MaxAudit` 条。
11. 快照带上桶、配额占用、在途预约、序号、拒绝日志与计数；恢复之后桶里剩下的令牌与配额占用都要接着算，损坏快照报 `ErrBadSnapshot` 且不动现网。
12. 并发 `Reserve` 对同一租户只能放行配额以内的条数，配额检查、令牌扣减与预约登记落在同一临界区里；并发 `Allow` 不能让同一个桶被超额扣减，计数器的自增也要在同一把锁里，不能出现请求数比放行加拒绝还少的情况。

验收：`go test ./...` 15 条全过；测试不得修改、不得跳过、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-r-08`
- 实现文件：`rate_limit_bypass_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RateLimitBypassGuard`、`LimitConfig`、`Reservation`、`Decision`、`QuotaState`、`BucketState`、`Rejection`、`LimitStats`
