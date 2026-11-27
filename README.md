# 启动度量日志重放与 PCR 核对

## 题面

扩容到第二批节点后，启动度量的一致性校验隔三差五报红：同一份度量日志在两台机器上重放出不同的 PCR，日志写了一半时校验工具还把半份结果判为通过。落点在 gsb/batch51-go-safety-c-07 的 event_log_replayer.go，类型 EventLogReplayer 负责解析启动度量日志、按 bank 重放 PCR，并与现场读取的 PCR 逐条比对。重放的产物是每个 PCR 在每个 bank 下的最终摘要，Compare 拿它与固件读出的现场值比对，Explain 则要能说清某个 PCR 是被哪几条事件一步步扩展出来的，方便值班同学定位分歧；同一份日志与同一个 bank 不该重算两遍。

签名保持：NewEventLogReplayer(cfg ReplayConfig) *EventLogReplayer；Parse(log []byte) (EventLog, error)；Replay(log EventLog, bank string) ([]PCRValue, error)；Compare(live []PCRValue) (Report, error)；Explain(pcr int) (Trace, error)；Events() []Event；Banks() []string；Fingerprint() string；CacheHits() int；Snapshot() ([]byte, error)；Restore(data []byte) error。日志是逐行文本，每条记录形如 pcr|type|alg|digestHex|dataHex，行尾必须有换行，type 取 EV_EXTEND、EV_ACTION、EV_NO_ACTION、EV_SEPARATOR，alg 支持 sha1 与 sha256。

要处理的十二处：① 空日志、字段数不足、数字或十六进制解析失败时只跳过该行，应报 ErrEmptyLog 或 ErrMalformedEvent；② 摘要字节数与声明算法不符仍接受，应报 ErrDigestLength；③ 最后一行没有换行或只有半条记录时返回已解析的部分，应报 ErrTruncatedLog；④ 事件条数、单事件数据长度、PCR 号超过配置上限仍接受，应分别报 ErrLogTooLarge、ErrEventTooLarge、ErrPCRRange；⑤ 重放时把 sha1 摘要扩进 sha256 的 PCR，不同 bank 的值互相污染，应按事件声明的 alg 分流，只扩展请求 bank 的同族事件；⑥ EV_NO_ACTION 也被计入扩展，应跳过其扩展但保留事件本身；⑦ EV_SEPARATOR 出现在同一 PCR 的扩展之后仍被接受，应报 ErrSeparatorOrder；⑧ 同一 PCR 上事件顺序变化后结果不变，扩展必须按日志顺序逐条进行；⑨ 重放结果没有缓存，或缓存被调用方改动，应按日志指纹与 bank 缓存并返回副本；⑩ Compare 只给总数，必须按 bank 给出匹配、不匹配、无法判定，并给出首个分歧事件序号；⑪ Explain 对未扩展过的 PCR 返回空链，应报 ErrUnknownPCR，链上每一步的 Before 与 After 必须首尾相接；⑫ 快照恢复丢掉解析结果与缓存计数，应完整恢复，损坏快照报 ErrBadSnapshot 且不动现网。

两条硬约束：重放缓存按日志指纹与 bank 只读共享，调用方改返回值不得污染缓存，同一日志同一 bank 重复重放必须逐字节一致；事件条数、单事件数据长度与 PCR 数量都是硬上限，超限报错，不允许截断后继续。另有三处按现场口径处理：EV_NO_ACTION 不扩展 PCR，但它的摘要字段仍要参与日志完整性校验；同一 PCR 出现多个分隔事件时只有启动阶段那个决定边界，后续不得重置；日志声明的算法不受支持时要报无法判定，不能跳过该条继续重放。

验收：go test ./... 的 15 条用例必须全过；测试不得修改、跳过或按固定输入返回常量；并发重放与重启恢复自行写程序验证；只做本地实现。交付源码、测试输出与一页以内的核对说明，写清日志指纹怎么算、截断怎么判定，以及缓存命中计数如何反映实际复用。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-07`
- 实现文件：`event_log_replayer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`EventLogReplayer`，输入是逐行度量日志，输出是 `PCRValue`、`Report` 与 `Trace`
