# 应用内购买回执：验签分级、订单状态机、幂等发货与退款冲正

## 题面

礼包上线当晚客服后台炸了：同一笔六元礼包，有玩家连点两次支付，背包里多出一倍的钻石；另有玩家的回执在支付平台延迟三分钟才到，补单任务把它当成新订单又发了一次。更糟的是一批沙盒测试回执混进了生产队列，签名校验全部通过，道具直接发到了正式服账号上。财务对账时还发现，退款冲正之后个别账号的钻石余额是负数，账本上多出一条平不掉的负债分录。

落点在 gsb/batch51-go-game-a-19 的 purchase_receipt_validator.go，类型 PurchaseReceiptValidator，包名 task，模块路径 gsb/batch51-go-game-a-19。公开接口保持：NewPurchaseReceiptValidator(limits IAPLimits) *PurchaseReceiptValidator、SetProductCatalog(cat []Product) error、SetPlatformKey(platform, key string) error、SetSandbox(on bool) error、Submit(rcpt Receipt) (string, error)、Verify(orderID string) (VerifyResult, error)、Grant(orderID string) (GrantResult, error)、Refund(orderID string, reason string) (RefundResult, error)、Reconcile(from, to int64) (ReconcileReport, error)、Balance(playerID string) int64、Stats() IAPStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) SetProductCatalog 的商品号不能为空白、数量必须为正、价格不得为负、同一目录内不得重复，否则 ErrBadCatalog 且旧目录继续使用；SetPlatformKey 的平台名与密钥不能为空白，否则 ErrBadKey。
2) Submit 的平台、交易号、商品号、玩家号、签名不能为空白，金额与购买时间必须为正，否则 ErrBadReceipt；商品不在目录内返回 ErrUnknownProduct；通过后按 ord-1、ord-2 递增分配订单号并累计提交数。
3) 同一平台交易号重复提交必须返回原订单号，不得新建订单，也不得增加提交计数。
4) 回执环境必须与商店模式一致：沙盒回执只在沙盒模式受理，生产回执只在生产模式受理，不一致返回 ErrEnvironmentMismatch。
5) 签名按平台密钥对“平台|交易号|商品号|玩家号|金额|购买时间”做 HMAC-SHA256 并取小写十六进制比对；通过返回 verified 且可发货，十六进制格式非法返回 retryable，格式合法但与摘要不符返回 rejected 并计入永久失败。
6) 永久失败与可重试失败的结论要落盘区分，补单扫描只重试可重试与未校验的订单，永久失败不得再次进入补单队列。
7) Grant 只接受已校验订单，否则 ErrOrderNotVerified；首次发货按商品数量入账并返回 granted，重复 Grant 返回同一份结果且余额与计数都不再变化。
8) Refund 的原因不能为空白，否则 ErrBadReason；只有已发货且未退款的订单可退，否则 ErrUnknownOrder 或 ErrOrderNotGranted；冲正数量取订单数量与当前余额的较小值，返回 refunded 并累计冲正数量。
9) Reconcile 的起止时间倒置返回 ErrBadReconcile；扫描购买时间落在闭区间内的订单，按订单号升序推进，单次最多处理 MaxReconcileBatch 条，下一次从上次停下的位置继续；已终结订单只计跳过。
10) 扫描中已校验的订单直接发货，未校验的先补验再决定发货或失败，报告分别累计扫描、发货、跳过与失败数。
11) Balance 返回玩家当前道具余额；Stats 汇总提交、校验通过、发货、退款、永久失败、入账数量与冲正数量。
12) 快照保存商品目录、平台密钥、商店模式、订单状态、余额账本与补单游标；恢复后不重复发货、订单号继续递增；载荷损坏返回 ErrBadSnapshot 且不动现有状态。

硬约束：Submit、Verify、Grant、Refund 与快照会被支付回调和运营工具并发调用，同一订单的首次发货只能发生一次；余额账本与冲正记录必须原子更新；补单游标只在同一区间内推进，受 MaxReconcileBatch 与 MaxRetries 硬顶。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-19`
- 实现文件：`purchase_receipt_validator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PurchaseReceiptValidator`
