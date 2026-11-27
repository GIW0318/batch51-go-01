# 播放端编解码能力协商、降级阶梯与探测缓存

## 题面

投屏试点上线之后，同一个片源在客厅电视上能播，在投影仪上就提示格式不支持；换成码率更高的片源，电视直接黑屏，日志里却写着协商成功、解码器初始化失败。排查时发现两边看到的能力表不一样：电视报的是四 K 硬解，实际跑到四 K 就掉帧，服务却一直按四 K 协商下去。缓存刚过期的那一刻，同一个设备上十几个请求一起打了进来，探测次数瞬间翻倍。HDR 片源更乱：机器支持 HDR 但只支持某个 profile，服务只看 codec 名就放过去了，画面一片灰。

落点在 gsb/batch51-go-game-c-13 的 codec_capability_negotiator.go，类型 CodecCapabilityNegotiator，包名 task，模块路径 gsb/batch51-go-game-c-13。

公开接口保持：NewCodecCapabilityNegotiator(limits Limits) *CodecCapabilityNegotiator、RegisterDevice(device DeviceID, caps []Capability) error、Probe(device DeviceID, codec string) (Capability, error)、Negotiate(req PlaybackReq) (Decision, error)、SetFallbackLadder(ladder []Rung) error、Ladder() []Rung、DefaultLadder() []Rung、SoftwareRung() Rung、Invalidate(device DeviceID) error、ReportOverflow(device DeviceID, width, height int) error、Explain(decision Decision) []Reason、SetProbeTTL(ttl time.Duration) error、ProbeTTL() time.Duration、Stats() NegotiationStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 缺省设备表六十四、每设备能力条目八、探测缓存三十秒。能力条目的 codec 只接受 h264、hevc、av1，level 与宽高必须为正，否则报 ErrBadCodec 或 ErrBadCapability；设备号为空或能力表为空报 ErrBadDevice；设备数与每设备条目超限报 ErrDeviceLimit 与 ErrCapabilityLimit，注册失败不得留下半张表。
2) 同一 codec 有多条能力时 Probe 返回 level 最高的一条；缓存窗口内重复探测只算一次并计入命中；TTL 只能为正，否则报 ErrBadTTL 并保留原值。
3) 未登记的设备、以及登记了但没报该 codec 的设备，探测失败报 ErrProbeFailed；不认识的 codec 报 ErrBadCodec。
4) Invalidate 清掉该设备的全部缓存条目，下一次探测重新计数；设备没有任何缓存条目时报 ErrUnknownDevice。
5) DefaultLadder 有三级：main 41 硬解、main 31 硬解、baseline 30 软解；SoftwareRung 是 baseline 30 软解；自备阶梯为空或含非法条目报 ErrBadLadder。
6) Negotiate 用请求里的 codec 逐级匹配：profile 相同、能力 level 不低于该级、宽高被覆盖、该级要硬解时能力必须带硬件标记。命中第一级时 Step 为下标、Code 为 exact；命中后面的级别时 Code 为 step_down、Degraded 为真、Software 取该级是否软解，并计入降级计数。
7) 阶梯全部走完仍不可播时降级到软件解码，Step 为负一、Code 为 software，不返回错误；探测失败同样走软件兜底而不是直接判不可播。
8) 宽高不为正报 ErrBadRequest，未知 codec 报 ErrBadCodec，被拒的请求不计数。
9) Explain 返回两条按序说明：第一条是 rung=下标，第二条是 source=hw 或 source=sw。
10) 设备报四 K 但运行时解码队列在四 K 溢出时，要回写能力并触发降级，不能继续按四 K 协商。
11) HDR 能力与 codec profile 联合判定，不能只看 codec 名。
12) 缓存过期瞬间多个请求同时到达，只能有一个重探，其余等待并复用同一结果。

硬约束：Probe、Negotiate、Invalidate、Snapshot 会被播放线程与后台巡检线程同时调用，同一设备的探测结果只能落一次账，快照要么看到整批注册前的设备表与缓存，要么看到整批提交后的，不能读到写了一半的缓存条目。每设备能力条目、设备数与缓存寿命都是硬上限，超限报错而不是静默截断。Snapshot 与 Restore 要能跨进程还原设备表、缓存条目、阶梯、TTL 与全部计数，损坏的快照报 ErrBadSnapshot 且不动现网状态。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-13`
- 实现文件：`codec_capability_negotiator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CodecCapabilityNegotiator`
