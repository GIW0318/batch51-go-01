# NPC 商店的供需定价、价格钳制与往返套利防护

## 题面

上周维护后，主城杂货商被玩家薅了羊毛：两个号在同一个柜台前一买一卖，来回几十趟，仓库里的金币反而比开始时多；客服复查日志，发现库存见底时收购价被顶到了天上，卖价也跟着涨，一进一出还能赚差价。另一处故障在停机后暴露：服务器凌晨重启，重建的商店一次补了四周的货，物价直接崩到地板价；还有玩家拿着几分钟前的报价单来成交，系统照样按旧价结算。

落点在 gsb/batch51-go-game-a-22 的 dynamic_price_engine.go，类型 DynamicPriceEngine，包名 task，模块路径 gsb/batch51-go-game-a-22。

公开接口保持：NewDynamicPriceEngine(cfg PriceConfig) *DynamicPriceEngine、SetBasePrice(item string, base int64) error、SetTarget(item string, target int) error、SetStock(item string, stock int) error、SetBand(item string, min, max int64) error、SetElasticity(item string, elasticity float64) error、Deposit(player string, amount int64) error、Balance(player string) int64、Quote(item string, side Side, qty int) (Quote, error)、Buy(player string, item string, qty int) (TradeResult, error)、Sell(player string, item string, qty int) (TradeResult, error)、Restock(now int64) (RestockReport, error)、Stats() PriceStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 商品编号不能为空，基准价必须为正，否则报 ErrBadItem、ErrBadBase；目标库存必须为正，当前库存不得为负也不得超过 MaxStock，分别报 ErrBadTarget、ErrBadQty、ErrStockFull。
2) 价格带下限必须为正且上限不小于下限，否则报 ErrBadBand；弹性系数限制在零到四之间，越界报 ErrBadElasticity；对未登记的商品做任何设置报 ErrUnknownItem。
3) 报价按基准价乘一加弹性乘目标库存减当前库存再除以目标库存计算；买价向上取整，卖价为该价格乘上 100 减 SellMarginPercent 后向下取整，且不得高于买价减一。
4) 算出的买价超出价格带时钳到带内，并把 Clamped 计数加一；卖价同样只允许落在带内。
5) 报价要带数量、单价、总额、当前库存、价格版本与到期时刻，到期时刻等于当前时刻加 QuoteTTL；未登记商品、数量不为正、方向不是 buy 或 sell 分别报 ErrUnknownItem、ErrBadQty、ErrBadSide。
6) 买入先扣钱后减库存，余额不足报 ErrInsufficientGold，库存不足报 ErrInsufficientStock。
7) 卖出先加库存后加钱，超过 MaxStock 报 ErrStockFull；成交结果要回带成交价、总额、成交后库存与余额。
8) 同一账号先买后卖、数量相同时，账户余额不得高于开始时，任何时候都不允许靠往返买卖获利。
9) 补货按 RestockPeriod 的周期推进，到期时只补一个周期的 RestockQty，补到 MaxStock 为止，并把锚点推进到最近一次到期的时刻。
10) 补货报告按商品逐条列出周期数、实际补入数量、补后库存与锚点；锚点晚于传入时刻报 ErrBadClock。
11) 同一商品的买入与卖出要串行化定价与扣减，不能出现两个人同时按同一份库存成交。
12) 快照保存商品表、库存、价格、锚点与玩家余额，恢复后报价与余额逐字段一致；载荷损坏报 ErrBadSnapshot 且不改变现有状态。

硬约束：Quote、Buy、Sell、Restock、Snapshot 会被多个柜台请求并发调用，报价到成交之间价格若已变化必须按成交时的价格重算并在结果里标明，不能沿用旧价；单商品库存与价格历史长度都是硬顶，补货量不会突破 MaxStock，超限拒绝而不是截断。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-22`
- 实现文件：`dynamic_price_engine.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DynamicPriceEngine`
