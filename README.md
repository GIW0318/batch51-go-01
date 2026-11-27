# 多段编码码流的拼接、参数集兼容判定与时间戳重基

## 题面

直播间回放拼装上线后，片头和正片接起来就花屏：解码器在前三秒不断报参数集错误，播放器时间轴还冒出负的显示时间，进度条直接跳到末尾。运营把一段广告插进中间之后，正片前半段的声音被整体推后，字幕和口型对不上；把片头换成另一台机器压的版本，分辨率一样却依然解不出来。夜里批量拼装还撞上两个问题：段数一多服务就重启，重启后已拼好的段落全丢。

落点在 gsb/batch51-go-game-c-11 的 bitstream_splicer.go，类型 BitstreamSplicer，包名 task，模块路径 gsb/batch51-go-game-c-11。

公开接口保持：NewBitstreamSplicer(limits Limits) *BitstreamSplicer、AddSegment(seg Segment) (SegID, error)、CheckCompatibility(a, b SegID) (CompatReport, error)、PlanSplice(at int) (SplicePlan, error)、Splice(plan SplicePlan) (Bitstream, error)、SetTimebase(tb Timebase) error、Timebase() Timebase、SetGopAlign(on bool) error、GopAlign() bool、Stats() SpliceStats、Segments() []SegmentInfo、ParamSets() []ParamSet、Reset() error、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 上限缺省时段数取十六、总帧数取八千一百九十二、参数集缓存取八。编号为空按 seg-1、seg-2 递增补号，重复编号报 ErrDuplicateSegment；段种类只接受 video 与 audio，帧种类只接受 idr、p、b，否则分别报 ErrBadSegment 与 ErrBadFrame。
2) 帧的 PTS 必须非负且不递减，否则报 ErrBadTiming；段内没有帧报 ErrEmptySegment；视频段缺 idr 报 ErrNoIDR，音频段不要求 idr。
3) 视频段宽高必须为正，否则报 ErrBadParamSet；参数集按全部字段去重后进缓存，超出报 ErrParamSetLimit；段数、总帧数、总时长超限分别报 ErrSegmentLimit、ErrFrameBudget、ErrDurationLimit，超限的段一个帧都不落账。
4) CheckCompatibility 只对已加入的段作答，未知编号报 ErrUnknownSegment；种类、profile、level、色度、宽高全部相同才算兼容，否则 Compatible 为 false 且 NeedParamSet 与 NeedReset 都为 true；Reasons 给出升序排列的差异字段名，取值只能是 kind、profile、level、chroma、width、height。
5) PlanSplice(at) 以全局帧下标为准，越界报 ErrBadSplicePoint；GOP 对齐开启时把切点向后推到最近的 idr，Wait 记录推后的帧数，Dropped 记录被移除窗口内的非参考帧数，找不到 idr 报 ErrNoIDR；对齐关闭时切点就是 at，Wait 与 Dropped 都为零。
6) 计划带上规划时的修订号，段表变化后再提交报 ErrPlanStale；计划越界或与当前帧数不符报 ErrBadPlan。
7) Splice 输出 [0, at) 与 [cut, 末尾) 两段，首帧 PTS 归零，段内偏移乘时间基分子再整除分母向下取整，相邻段之间留一个刻度；DTS 与 PTS 相同，绝不出现负值；参数集按首次出现顺序输出，切点两侧参数集不同时在段首给出重置点，Splices、Resets、Dropped 累计。
8) SetTimebase 的分子与分母都必须为正，否则报 ErrBadTimebase 并沿用旧值；时间基只影响之后的新拼接。Segments 与 Stats 的计数必须和段表一致。
9) 音频段的参数集不进缓存，音频与音频之间只比较种类；音频与视频互不兼容，判定里要出现 kind。
10) 判断兼容不能只看宽高：分辨率一致但 profile 或 level 不同的两段按不兼容处理，必须插入新参数集并标解码器重置点。
11) 切点落在 B 帧重排窗口内时要等到下一个 idr，不能就地切下去，否则重排后的显示顺序会倒挂。
12) 音频段时间基独立，视频重基不得改动音频段的时间戳，音画对不齐时以音频为准。

硬约束：AddSegment、Splice、Snapshot 会被拼装线程与播放线程同时调用，快照要么看到整批提交前的段表，要么看到提交后的段表；Splice 与 AddSegment 并发时修订号单调递增，旧计划必须被拒绝。Snapshot 与 Restore 要能跨进程还原段表、参数集缓存、时间基、对齐开关、修订号与全部计数器，损坏的快照报 ErrBadSnapshot 且不动现网状态。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-11`
- 实现文件：`bitstream_splicer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`BitstreamSplicer`
