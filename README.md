# 抓包流重组：乱序填补、重叠取舍与双向流收束

## 题面

流量审计上线第一周就出了问题：同一份 HTTP 响应在上层解析器里被拼成了错位的内容，抓包文件回放显示前一个分片晚到、两个分片序号重叠，重叠部分还被重复交付了一次；另有一条连接已经发出结束标志，三秒后才到的最后一个分片被直接丢掉，审计日志里这条下载记录永远缺尾巴。抓包文件来自交换机镜像口，量很大，重组必须扛得住乱序与重传。请在 gsb/batch51-go-safety-c-30 的 pcap_flow_reassembler.go 中补全 PcapFlowReassembler，签名保持：NewPcapFlowReassembler(policy Policy) *PcapFlowReassembler、Feed(seg Segment) (FlowEvent, error)、Flush(key FlowKey) ([]byte, error)、Close(key FlowKey, reason string) error、Flow(key FlowKey) (FlowState, bool)、Active() []FlowKey、Expire(now int64) []FlowKey、Snapshot() ([]byte, error)、Restore(data []byte) error、Stats() Stats、Reset()。

要修的点：四元组里地址为空、端口为零或协议为零报 ErrBadFlow；既没有数据又不是结束标志的分片报 ErrEmptySegment；Flush 与 Close 遇到不存在的流报 ErrUnknownFlow。按四元组维护正反两条流，各自有独立的期望序号与缓存。按序号交付，落在下一个期望序号之后的先缓存，前面的缺口补上后把连续段一次性释放，事件里用 Buffered 与 Gap 反映缓存与空洞。同一个字节只能交付一次，重传重叠部分按已交付裁掉并计入 Duplicate；同一序号出现内容不同的重叠段时保留先到的版本，另记冲突次数，不能用后到的内容覆盖。落后超过 WindowSize 的老分片报 ErrSeqOutOfWindow 并累计 Rejected，序号从最大整数回绕到零时必须继续按连续处理。单流缓存超过 MaxBuffer 时从序号较大的一端截断，置 Truncated 并保留较小序号的部分。空闲超过 IdleTimeout 的流由 Expire 关闭并写 ReasonIdle。Close 收束流并记录原因，重复 Close 视为幂等且保留第一次的原因；收到结束标志要在流状态里留下记录；连接关闭后滞留窗口 Linger 内到达的迟到分片仍要接纳，超过窗口才报 ErrFlowClosed。只抓到单向流、反向从没出现过时，空闲收束要标记方向缺失。流表数量受 MaxFlows 约束，满时新流报 ErrFlowLimit，不能静默丢弃。Snapshot 保存每个流的期望序号、缓存、已交付字节与计数，损坏快照报 ErrBadSnapshot 且不动现网；Reset 清空流表与计数。统计要能区分正常注入、重复裁剪、越窗拒绝与超时收束，各项不能混成一个数字，值班同学要能按项排查。

硬约束：并发注入分片时同一字节不能交付两次，Flush 返回的字节流必须是最终顺序；快照恢复后流表、缓存与计数接着生效，不能靠重启重放；缓存与流表都有硬上限，超限时的行为必须确定且可解释。

验收：go test ./... 15 条必须全过，不得修改测试、跳过测试或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-30`
- 实现文件：`pcap_flow_reassembler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PcapFlowReassembler`
