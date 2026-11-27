# IPv6 扩展头链的逐跳解析与入口策略

## 题面

数据面节点启用 IPv6 后，一台机器在收到带路由头的报文时丢包率明显抬升，另一些报文在扩展头链里夹带未知选项却仍被放行；抓包还看到有人用分片头把后续扩展头藏起来，绕过了入口检查。落点 gsb/batch51-go-safety-b-10 的 ipv6_ext_chain_guard.go，类型 Ipv6ExtChainGuard，签名保持：NewIpv6ExtChainGuard(cfg ChainPolicy) *Ipv6ExtChainGuard、Parse(pkt []byte) (Chain, error)、Check(chain Chain, policy Policy) (Verdict, error)、Explain(pkt []byte) (Trace, error)、SetPolicy(iface string, p Policy) error、Policy(iface string) (Policy, bool)、SetDefault(p Policy) error、Inspect(iface string, pkt []byte) (Verdict, error)、Stats(iface string) Counters、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的故障与边界：

一、固定头不足 40 字节报 ErrShortPacket，版本号不是 6 报 ErrBadVersion；扩展头声明长度超出包长、选项长度越界都报 ErrTruncatedChain，解析失败时不得返回半条链。

二、扩展头按 next header 链式解析：逐跳头与目的选项头按 (len+1)*8 计算长度，路由头额外带类型与剩余段数，分片头固定 8 字节并带偏移、MF 与标识，ESP 与 no_next 结束链条；解析一律以实际切片长度为边界，不按 payload length 字段截断，该字段只留给第十条做交叉校验。

三、上层协议识别 tcp、udp、icmpv6；未知 next header 记为 unknown，由策略决定放行还是丢弃，不能默认放过。

四、逐跳头只能出现在第一个扩展头位置，出现在其后整包丢弃，即使选项本身合法。

五、路由头 Type 0 一律丢弃；其它类型按接口白名单，未列入白名单的丢弃。

六、分片头只允许出现一次；原子分片（偏移为 0 且 MF 为 0）按 AllowAtomicFragments 处理，非原子分片交给重组逻辑放行。

七、分片头之后再出现路由头或逐跳头属于非法顺序，整包丢弃，不能继续按链解析。

八、选项按 Pad1 与 PadN 规则解析，未知 critical 选项丢弃，未知非 critical 选项忽略；选项长度按自身总字节数计。

九、扩展头数与选项数分别受 MaxHeaders 与 MaxOptions 约束，超限丢弃；链在到达上层协议前就超长时同样丢弃并计数，不允许截断链条后按合法包放行，丢弃的包计入 Dropped。

十、Jumbo Payload 选项只在 payload length 字段为 0 时有效，且四字节值与固定头之后的实际长度相等；未开启 AllowJumbo 丢弃，长度不符也丢弃。

十一、Explain 返回每段扩展头与上层协议的步骤、解析出的链与最终判定；解析失败时把错误文本回填到结果里。

十二、每接口策略独立，未配置的接口回落到默认策略，接口名为空报 ErrBadIface；Stats 按接口统计放行、丢弃与解析错误，未登记接口返回零值计数；快照覆盖每接口策略与计数。

硬约束：解析时不得按包内声明的长度分配超出包长的缓冲，任何越界立刻报错而不是部分接受；策略表、每接口计数与快照可以并发读写，单包的扩展头数与选项数都有硬顶，超限只能丢弃。

验收：go test ./... 的 15 条用例全部通过，不得修改测试、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现，不接外部依赖。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-10`
- 实现文件：`ipv6_ext_chain_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`Ipv6ExtChainGuard`
