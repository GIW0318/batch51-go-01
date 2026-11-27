# OPC UA 安全通道分块重组与序列号治理

## 题面

产线的采集网关连上 OPC UA 服务器后，每到大报文就整条丢失；重连之后又出现重复的写入指令，同一条配方被下发两次；证书轮换之后会话还要重新登录，正在跑的批处理被迫中断。请补齐模块 gsb/batch51-go-safety-b-15 里 opcua_secure_channel.go 的 OpcuaSecureChannel，把分块重组、双向单调序列号与 nonce 轮换后的会话重激活做对。

对外签名保持：NewOpcuaSecureChannel(limits Limits) *OpcuaSecureChannel、Open(endpoint string, req OpenSecureChannel) (ChannelInfo, error)、Accept(channelID string, chunk []byte) ([]Message, error)、Renew(channelID string, req RenewRequest) (ChannelInfo, error)、CreateSession(channelID, user string, authToken []byte) (SessionInfo, error)、Reactivate(sessionID string, authToken []byte) (SessionInfo, error)、Close(channelID, reason string) error、Messages(channelID string) []Message、Stats(channelID string) (ChannelStats, bool)、Channel(channelID string) (ChannelState, bool)、Snapshot() ([]byte, error)、Restore(data []byte) error。

分块头部固定十个字节：第一个字节是消息类型，M 为普通消息、O 为打开、C 为关闭；第二个字节是块类型，C 为中间块、F 为末块、A 为中止块；随后两个小端四字节依次是序列号与请求号，再往后是负载。

要处理的故障与边界：

（1）端点去空白为空报 ErrBadEndpoint；安全策略只接受 None、Basic256Sha256、Aes128Sha256RsaOaep，否则报 ErrBadPolicy；策略要求加密而模式选了 None，或者策略是 None 却要求签名，报 ErrBadMode。

（2）Open 后令牌号为 1，发送序列号从 1 起、接收序列号从 0 起，两个方向各自独立维护，绝不合并成一条流水。

（3）块长度不足头部十个字节报 ErrShortChunk；消息类型或块类型不认识报 ErrBadChunkType；超过 MaxChunkSize 报 ErrChunkTooLarge。这些拒绝都不推进接收序列号。

（4）接收序列号必须连续：重复或回退的块丢弃并计入重复计数，报 ErrSequenceReplay；跳到期望值之后报 ErrSequenceGap 并计入缺口计数。

（5）中间块只进缓冲不产出消息；末块把缓冲与自身负载拼接后交出一条消息，带请求号与消息类型；中间块丢失导致后续末块到达时整条丢弃，不推进序列号。

（6）中止块必须终止当前消息的重组并清空缓冲，后续同请求号的新消息不能拼到旧的半截缓冲上。

（7）Renew 携带的令牌号必须等于当前值，否则报 ErrBadToken；轮换后令牌号加一、申请寿命按 MaxLifetime 截顶，收发序列号保持不变；旧令牌立刻不能再用，重叠期内仍放行已排队的旧密钥块。

（8）CreateSession 把用户与认证令牌绑到通道上，用户为空报 ErrBadUser，令牌为空报 ErrBadAuthToken，通道不存在报 ErrUnknownChannel。

（9）Reactivate 必须复用会话建立时登记的认证令牌：令牌不符报 ErrUserMismatch，不能因为通道换了新令牌就接受别的用户；会话不存在报 ErrUnknownSession。重激活后状态为 reactivated，令牌号取通道当前值。

（10）通道关闭后 Accept 报 ErrChannelClosed，重复关闭幂等成功，未知通道报 ErrUnknownChannel。

（11）同一通道的并发 Accept 必须保序：接收判定、缓冲推进与消息产出在同一把锁里完成，不能两个块同时被当成期望值。

（12）快照保存通道、令牌、双向序列号、缓冲、会话与丢弃计数；恢复后从原水位继续接收，会话仍可用原令牌重激活；损坏的快照报 ErrBadSnapshot 且不改动现网状态。

硬约束有两类。其一是并发与原子性：同一通道的接收判定与序列号推进串行化，重复块只丢弃一次并只计一次数；重连后按发送序列号重传未确认消息，不得重复入队。其二是容量与恢复：通道数、块大小与寿命都对硬上限截顶，超出拒绝而不是截断继续；快照与恢复整份成功或整份失败。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重连场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-15`
- 实现文件：`opcua_secure_channel.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`OpcuaSecureChannel`
