# 代理媒体的档位规划、校验与断点续跑

## 题面

素材库升级代理生成之后，剪辑师在时间线上对不上原片：代理的时间码整体偏了两秒，套底时字幕全错位；一批二十九点九七的变帧率素材被转成恒定帧率，越到片尾漂得越多。夜里跑批还把磁盘写满，任务挂掉之后第二天只能从头重转，已经完成的分片全白做。

落点在 gsb/batch51-go-game-c-05 的 media_proxy_generator.go，类型 ProxyGenerator，包名 task，模块路径 gsb/batch51-go-game-c-05。

公开接口保持：NewProxyGenerator(limits ProxyLimits) *ProxyGenerator、Plan(asset Asset) (ProxyPlan, error)、Shards(asset Asset) (int, error)、Generate(plan ProxyPlan, budget int) (Progress, error)、Verify(proxy ProxyRef) (VerifyResult, error)、Resume(proxy ProxyRef) (Progress, error)、ProgressOf(proxyID string) (Progress, bool)、Status(proxyID string) string、Acquire(tenant string) error、Release(tenant string) error、SetDiskFree(bytes int64) error、Snapshot() ([]byte, error)、Restore([]byte) error、Limits() ProxyLimits。

需要落实的行为：

1) 限额缺省时租户并发四、磁盘预算八吉字节、分片上限四千零九十六、分片时长十秒。
2) 档位按源高度选择：不低于一千零八十走 1080p，不低于七百二十走 720p，其余走 480p，代理高度取档位值；分片数等于时长除以分片时长向上取整，超过上限报 ErrShardLimit，且不登记进度。
3) 编号、租户、宽高、时长、帧率分子分母、时间基分子分母、内容哈希为空或不正报 ErrBadAsset。
4) 计划必须带源时间码偏移与源时间基，代理编号由资产与档位确定；重复规划同一资产得到完全相同的计划，且不清空已有进度。
5) Generate 每次最多推进 budget 个分片，超出剩余量时停在总数上；budget 为零或负报 ErrBadBudget，计划缺少编号或分片数报 ErrBadPlan。
6) 生成完成后再调用不报错，完成数不倒退；进度里带上已写字节数。
7) Verify 必须报出缺失分片的下标并保持升序，内容哈希与计划不一致时内容不匹配，时间码偏移与计划不一致时时间码不通过，三项都通过才算整体通过；未知代理报 ErrUnknownProxy。
8) Resume 跳过已完成分片，返回已完成数、总数与跳过数，续跑后继续推进到总数。
9) 租户并发按租户独立计数，超过配额的 Acquire 报 ErrTenantQuota，Release 归还一个名额。
10) 状态只有 pending、running、complete、suspended 四态，未知代理返回 unknown。
11) 源文件元数据变化但内容哈希不变时仍要校验时间码偏移，不能直接复用代理。
12) 变帧率素材生成代理时保持源时间基，不得强制恒定帧率导致时间码漂移；磁盘预算不足时已生成分片保留、任务挂起，恢复后从断点续跑而不是删除重来。

硬约束：Plan、Generate、Verify、Resume 会被多个上传任务并发调用，同一代理的进度只能单调前进，快照要么看到整批提交前的进度要么看到提交后的进度；租户配额与磁盘预算都是硬上限。Snapshot 与 Restore 要跨进程还原计划、每片进度、租户名额、磁盘余量与限额，损坏快照报 ErrBadSnapshot 且不动现网状态。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-05`
- 实现文件：`media_proxy_generator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ProxyGenerator`
