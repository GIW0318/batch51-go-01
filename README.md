# 说话人分离片段的槽位与归并账本

## 题面

会议转写上线后，两个人抢话的那几段只剩一个人的名字，客户投诉记录不完整；另一处是主持人清了清嗓子说了半秒，那半秒在时间轴上直接变成空白，字幕出现断口。跨文件续会更乱：同一个法务顾问在新文件里被拆成两个陌生说话人，客户以为中途换了人。

落点在 gsb/batch51-go-game-c-04 的 speaker_diarization_ledger.go，类型 DiarizationLedger，包名 task，模块路径 gsb/batch51-go-game-c-04。

公开接口保持：NewDiarizationLedger(limits DiarLimits) *DiarizationLedger、Assign(segment Segment) (Assignment, error)、Merge(segmentIDs []string) error、Split(segmentID string, at time.Duration) error、Finalize(fileID string) (Transcript, error)、Remove(segmentID string) error、SetThreshold(t float64) error、Snapshot() ([]byte, error)、Restore([]byte) error、Slots() int、Pending() int、Segments(fileID string) []Segment、Limits() DiarLimits、Threshold() float64。

需要落实的行为：

1) 限额缺省时说话人上限八、静音合并间隔三百毫秒、最短片段两百毫秒、声纹匹配距离零点二五、灰区带宽零点一、置信度门限零点六。
2) 片段编号或文件编号为空、结束不晚于开始、起始为负、置信度超出一律报 ErrBadSegment，且不写进账本。
3) 片段声纹维度与已有槽位不一致报 ErrBadFingerprint；与最近槽位的欧氏距离不超过匹配距离时复用该槽位，否则新建槽位并从一号开始编号，槽位用满报 ErrSlotLimit。
4) 置信度低于门限的片段进入待定区，槽位为零、不计入槽位数，待定计数随之加一。
5) 同一片段编号重复提交返回首次结果，不重复建槽、不重复计数。
6) Merge 只接受两段以上、同一说话人、静音间隙不超过阈值且时间相邻的片段，合并后保留最早片段编号、跨度取两端；说话人不同报 ErrMergeMismatch，间隙过大报 ErrGapTooLarge，编号不存在报 ErrUnknownSegment。
7) Split 的切点必须严格落在片段内部，零点与终点都报 ErrBadSplit；切完左半保留原编号，右半取派生编号，两段首尾相接。
8) SetThreshold 只接受零到一之间的值，其它报 ErrBadThreshold 且门限不变。
9) Finalize 按开始时刻升序输出条目，时长取该文件最大结束时刻，说话人数取去重后的槽位；重复调用结果一致，不重复追加。
10) 两个说话人同时说话的重叠段必须保留双归属，不能按置信度高者截断。
11) 最短片段阈值触发删除时只删除标注不删除音频区间，时间轴不得留洞。
12) 跨文件身份延续遇到声纹距离处于灰区时创建新槽位并标记疑似，不自动合并。

硬约束：Assign、Merge、Split、Snapshot 会被多个上传通道并发调用，同一声纹只能落在一个槽位，快照要么是整批提交前的账本要么是提交后的账本；待定区与槽位表都有硬上限。Snapshot 与 Restore 要跨进程还原槽位质心、片段、待定数、门限与计数，损坏快照报 ErrBadSnapshot 且不动现网账本。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-04`
- 实现文件：`speaker_diarization_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DiarizationLedger`
