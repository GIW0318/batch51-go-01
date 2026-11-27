# 视频封面候选打分、相似帧去重与配额截断

## 题面

封面自动挑选上线之后，运营每天收到一堆几乎一样的图：同一个人物同一个姿势，只轻微抖了一下就占了三张，配额一截断，真正差异大的镜头反而被挤掉。夜戏素材更怪，纯黑的开场被当成普通低分帧处理，缺图时还被补上去，封面一片黑。还有一次，运营手工否掉一张带水印的图，第二天任务重跑，那张图又回到了候选池。同一批视频重跑两次，选出来的顺序偶尔还不一样，运营不敢直接发。

落点在 gsb/batch51-go-game-c-15 的 cover_frame_selector.go，类型 CoverFrameSelector，包名 task，模块路径 gsb/batch51-go-game-c-15。

公开接口保持：NewCoverFrameSelector(limits Limits) *CoverFrameSelector、AddCandidate(frame Frame, ts int64) error、SetWeights(w ScoreWeights) error、Weights() ScoreWeights、SetSceneCuts(cuts []int64) error、SetQuota(n int) error、Quota() int、Select() ([]CoverCandidate, error)、Reject(ts int64, reason string) error、ClearReject(ts int64) error、Explain(ts int64) ScoreBreakdown、Stats() CoverStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 缺省配额四张、候选池上限四千零九十六、一次选择的哈希预算四千零九十六、权重为清晰度零点五、主体占比零点三、色彩零点二。配额只接受一到五十，越界报 ErrBadQuota 并保留原值。
2) 候选帧编号不能为空、宽高必须为正、清晰度与主体占比与色彩都落在零到一之间，否则报 ErrBadFrame；时间戳为负报 ErrBadTS，重复时间戳报 ErrDuplicateTS；候选池满报 ErrCandidateLimit，被拒的帧不计数。
3) 权重必须非负且不能全为零，否则报 ErrBadWeights 并保留原值；场景切点必须非负且严格递增，否则报 ErrBadCuts。
4) 打分等于三个权重分别乘清晰度、主体占比、色彩后求和；同一输入必须得到同一分数与同一排序，不得引入随机或依赖 map 遍历顺序。Explain 对已知时间戳返回三个分量、总分与 scored，对未知时间戳只返回 unknown。
5) Select 先给全池打分，再做近似重复合并，再按分数降序截断到配额；同分按时间戳升序稳定排序，空池报 ErrNoCandidates。
6) 相邻且感知哈希汉明距离不超过四的两帧只保留分数高者，被丢弃的计入 Deduped；距离大于四的两帧必须都保留。
7) 纯黑与纯白帧走独立的黑帧规则，不按低分截断，也不占用普通低分的名额。
8) 同一场景内哈希相近但构图不同的两帧不能只留一帧，去重阈值要按场景切点分段，不能跨切点误删。
9) 被人工拒绝的帧不得自动复活，除非显式清除拒绝记录；拒绝原因为空报 ErrBadReason，时间戳不在池里报 ErrUnknownCandidate。
10) 候选数与哈希计算次数都是硬上限，一次选择要算的哈希超过预算报 ErrHashBudget，不截断也不降级。
11) 统计要给出候选数、上次选中的数量、拒绝数、去重数、哈希计算次数与黑帧数。

硬约束：AddCandidate、Reject、Select、Snapshot 会被上传任务与人工复核同时调用，同一时间戳只能落一个候选，快照要么看到整批候选加入前的池子，要么看到加入后的，不能读到写了一半的哈希索引。候选池、哈希预算与配额都是硬上限，超限报错而不是静默丢弃。Snapshot 与 Restore 要能跨进程还原候选池、拒绝记录、场景切点、权重、配额与全部计数，恢复后同样的输入必须选出同样的顺序，损坏的快照报 ErrBadSnapshot 且不动现网状态。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-15`
- 实现文件：`cover_frame_selector.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CoverFrameSelector`
