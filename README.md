# 调音台发送图的块边界路由与延迟补偿

## 题面

巡演调音台的沉浸声返送这两周接连翻车：主扩一路正常，编组通道一插上返送就整段变哑；演出进行中调音师新增了一条发送，正在播的那一小节里所有轨道忽然掉到零，下一小节才恢复。把中间那条效果总线删掉以后，原本绕经它的一路声音仍然从别的路径冒出来，声像整整偏了三十度；同一版工程换到另一台机器载入，延迟补偿的数字对不上，鼓组和贝斯错开了一整块。夜里跑离线导出，十分钟的素材开头三秒静音，后面整段偏移，重叠发送的那条和声比主唱晚了十几毫秒。

落点在 gsb/batch51-go-game-c-06 的 audio_bus_graph.go，类型 AudioBusGraph，包名 task，模块路径 gsb/batch51-go-game-c-06。

公开接口保持：NewAudioBusGraph(limits GraphLimits) *AudioBusGraph、AddBus(id string, cfg BusConfig) error、RemoveBus(id string) error、Connect(src, dst string, send SendConfig) error、Disconnect(src, dst string) error、SetGain(id string, db float32) error、SetSidechain(dst, key string, depth float32) error、Bypass(id string, on bool) error、Render(frames int) ([]float32, error)、Latency(id string) (int, error)、Stats() BusStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 总线号为空报 ErrBlankBusID，重复报 ErrDuplicateBus，超过 MaxBuses 报 ErrBusLimit，源电平越出零到一报 ErrLevelRange，被拒的调用不得留下半条记录。
2) 总线增益与发送增益都只接受负六十到正十二分贝，越界报 ErrGainRange 并保持旧值。
3) 收发两端任一不存在报 ErrUnknownBus；自己发给自己报 ErrSelfSend；同一条发送重复建立报 ErrDuplicateSend；单条总线出向发送数超过 MaxSendsPerBus 报 ErrSendLimit，被拒之后的图与调用前完全一致。
4) 会成环的发送当场拒绝并报 ErrCycle，错误文本给出完整环路，形如 a -> b -> c -> a；同一块内连续新增、尚未提交的发送也参与判定。
5) 拓扑增删只在块边界生效：AddBus、RemoveBus、Connect、Disconnect 先进入待提交集合，Render 开始时整体切换。切换之前 Latency 与 Stats 里的已提交计数保持旧值，PendingEdits 给出待提交条数。
6) 正在进行的 Render 用进入时的旧图跑完，块内新增或删除发送不影响本块输出；删除总线时先摘除它的全部出向与入向发送，再移除节点。
7) 发送延迟只接受零到 MaxSendLatencyFrames，越界报 ErrLatencyRange；延迟按帧右移，块首不足部分补零。
8) 侧链深度只接受零到一，越界报 ErrSidechainDepth；侧链键必须是已存在的总线，否则报 ErrUnknownBus；一条目标总线只保留一个侧链键。
9) 侧链键所在总线被旁路时，对应侧链增益归零，不能保持上一次的值。
10) 旁路某条总线后，它自身声源与出向发送都输出零，取消旁路后立即恢复原值。
11) 同一目标被多条路径发送时按最长路径的累计延迟统一对齐，短路径补延迟，不能各补各的；删除中间总线后，原先绕经它的发送一并断开，不得把上下游自动接起来。
12) Render 的帧数必须落在一到 MaxBlockFrames，越界报 ErrFramesRange，输出长度等于帧数；快照保存总线、发送、增益、侧链、旁路与待提交集合，恢复后 Latency 与 Render 结果与快照前一致，损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：Render 与 Connect、SetGain、Snapshot 会被音频线程与控制线程同时调用，提交动作要么整份生效要么整份不生效，读方不能看到半张图；总线数与每条总线的发送数受上限硬顶，超限报错而不是静默丢弃；进程重启后按快照恢复，已提交拓扑与延迟表逐项一致。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发提交与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-06`
- 实现文件：`audio_bus_graph.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AudioBusGraph`
