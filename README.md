# 混响预设切换的尾音交叉淡化与状态恢复

## 题面

场景混响预设在夜间演出的换场里出事：从大厅切到板式，上一段的尾音整块断掉，接口处咔的一声；连续切了三个预设之后，返送里叠出三四层尾音，声音糊成一团，越切越响。同一套工程在低配机器上加载，第四个预设直接加不进去，报出来的却是另一个错误；调音师把预制好的大厅预加载进去，等到真要切换时又要重新读盘，间隙里静音半拍。冻结按钮按下之后切到下一个场景，冻住的那层尾音被新预设顶掉，回来再取消冻结已经找不到原来的味道。夜里重启调音台，加载同一份会话状态，尾音从头开始，而不是接着上次的进度。

落点在 gsb/batch51-go-game-c-07 的 reverb_tail_manager.go，类型 ReverbTailManager，包名 task，模块路径 gsb/batch51-go-game-c-07。

公开接口保持：NewReverbTailManager(slotBudget int) *ReverbTailManager、LoadPreset(id string, ir ImpulseResponse) error、Preload(id string) error、ActivatePreset(id string, fadeMs int) error、SetWetDry(id string, wet, dry float32) error、Process(in []float32, frames int) ([]float32, error)、Freeze(on bool) error、TailRemaining() int、SetSlotBudget(n int) error、Stats() ReverbStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 预设号为空报 ErrBlankPresetID，重复报 ErrDuplicatePreset；衰减系数必须严格落在零与一之间，尾音长度落在一到 MaxTailFrames，越界报 ErrInvalidIR，被拒的加载不占槽位。
2) 加载占一个 IR 槽位，槽位满时按最近最少使用回收未激活预设；没有可回收对象时报 ErrSlotBudget。槽位预算只接受一到 MaxSlotBudget，越界报 ErrBadSlotBudget。
3) 激活不存在的预设报 ErrUnknownPreset；淡入长度只接受零到 MaxFadeMs 毫秒，越界报 ErrBadFade 且不改变当前激活项。
4) 干湿比各自只接受零到一，且两者之和不超过一，越界报 ErrWetDryRange；未加载的预设报 ErrUnknownPreset。
5) 处理公式固定：每帧先算尾音等于输入加上衰减乘上一帧尾音，输出等于干乘输入加上湿乘尾音；输入绝对值低于千分之一时尾音年龄加一，否则清零。
6) TailRemaining 返回尾音长度减去年龄，最小为零；年龄超过尾音长度后保持为零。
7) 交叉淡化按采样进行：淡入帧数等于毫秒数乘 48000 再除以 1000；切换时旧状态继续按原衰减推进，不能重启旧尾音；淡入结束后只保留新预设状态。
8) 连续快速切换只保留当前与上一套状态，更早的尾音按剩余能量快速淡出，不能无限叠加。
9) 冻结时尾音值不再衰减、年龄不再推进，干声照常通过；冻结状态下切换预设，冻住的尾音不能被新预设覆盖。
10) 预加载的 IR 未激活时不占处理槽位，激活时必须立即可用。
11) 处理输入必须同时给帧数与样本，两者不一致报 ErrFramesMismatch；帧数落在一到 MaxBlockFrames，越界报 ErrBlockRange。
12) 快照保存槽位、预设、两套处理状态与淡化进度，恢复后尾音接着走而不是从头开始；损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：Process 与 ActivatePreset、Freeze、Snapshot 会被音频线程与控制线程同时调用，切换要么整份生效要么整份不生效，读方不能看到半新半旧的链；IR 槽位与每块帧数受上限硬顶，超限报错而不是静默丢弃；重启后按快照恢复，两套状态与淡化进度逐项一致。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；块连续性与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-07`
- 实现文件：`reverb_tail_manager.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ReverbTailManager`
