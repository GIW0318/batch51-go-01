# 工控从站功能码探测：地址边界、异常码归因与写入复原

## 题面

产线巡检报告里，同一台 PLC 的保持寄存器区块，甲班组判成地址越界，乙班组却判成设备不支持，两份报告的处置建议完全相反；更严重的是，上一轮写入类功能码探测结束后没有把原值写回，第二天配料参数全被改成了 1，停线两小时。维修班只能靠人工逐台核对，一晚上核了三十多台从站。请在 gsb/batch51-go-safety-c-26 的 modbus_function_prober.go 中补全 ModbusFunctionProber，签名保持：NewModbusFunctionProber(dev Device, limits Limits) *ModbusFunctionProber、Plan(t Target, opts PlanOptions) (ProbePlan, error)、Probe(plan ProbePlan) (ProbeReport, error)、Classify(code uint8) (string, error)、AuthorizeWrites(units []uint8, budget int) error、WriteProbe(req WriteRequest) (WriteResult, error)、Writes() []WriteRecord、Findings() []Finding、UnitReport(unit uint8) (UnitReport, bool)、Snapshot() ([]byte, error)、Restore(data []byte) error、Stats() Stats。

要修的地方：地址区间 From 大于 To 报 ErrBadRange，跨度超过 MaxSpan 报 ErrSpanTooLarge，区块名未知报 ErrUnknownBank。Plan 的地址集合由区间两端、两端内侧各一个以及按 Stride 均匀采样的点组成，去重后升序；coils、discrete、holding、input 四类区块分别对应功能码 1、2、3、4，设备标识区块只发一条功能码 43 的请求，不按地址区间展开。异常码 0x02 判地址越界、0x01 判设备不支持、0x03 判访问被拒、0x04 判从站故障、0x06 判忙、0x0A 与 0x0B 判网关错误，其余异常码报 ErrUnknownException，前两类不能合并成同一个结论，它们的可利用性不同。Probe 先整批预占读预算，预算不足直接报 ErrReadBudget 且一条请求都不发。单条请求发生传输超时立即停止本轮，报告里 TimedOut 置真，Requests 记已发条数，Skipped 记未发条数。未经 AuthorizeWrites 授权的单元号写入报 ErrWriteNotAuthorized，全过程不得触碰设备。写入只接受功能码 5 与 6，其它功能码报 ErrUnsupportedFunction。每次写探测先读原值，写入候选值后回读，成功后再写回原值并二次回读，结果里带 Original、Written、ReadBack、Restored。总写入次数与单单元写入次数分别受 MaxWrites 与 MaxWritesPerUnit 约束，超限报 ErrWriteBudget，并发下也不能多放一条。回写确认失败报 ErrRestoreFailed，写记录标记 Restored=false，并立即进入停止态，后续写入一律报 ErrWriteHalted。网关把多个单元号聚合成一条连接时，某个单元的超时或丢弃要按单元号分别归因，不能污染其它单元的报告。写后回读被设备量程限幅或取整时，要按 ScaleMin 与 ScaleMax 判是否算复原，不能一律记失败。Findings 与 Writes 每次返回副本，按产生顺序排列。

硬约束：读预算与写预算的预占必须原子，同一进程内多协程同时探测时，放行条数严格等于配置值；发现项、写记录、预算消耗与停止态要能随快照整体保存和恢复，快照恢复后预算水位与停止态接着生效，不能靠重启清零，损坏的快照报 ErrBadSnapshot 且不得改动现网状态；Plan 的输出不依赖 map 遍历顺序，同一目标重复规划必须逐项一致。

验收：go test ./... 15 条必须全过，不得修改测试、跳过测试或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-26`
- 实现文件：`modbus_function_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ModbusFunctionProber`
