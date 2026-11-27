# 相机震动分层：创伤平方映射、优先级压制不重置相位与暂停冻结

## 题面

Boss 战上线后，镜头在连续爆炸里抖到失控，玩家反馈画面直接翻出场景；同一个技能的多段伤害在弹幕里被念成一次，镜头却只轻轻推了一下；远处的迫击炮和贴在脚边的手雷抖起来一样猛，优先级像没写一样；切到第一人称瞄准时震动幅度反而放大到看不清准星；过场动画暂停期间镜头还在抖，恢复后又从零跳一下。

落点在 gsb/batch51-go-game-b-10 的 camera_shake_director.go，类型 ShakeDirector，包名 task，模块路径 gsb/batch51-go-game-b-10。

公开接口保持：NewShakeDirector(cfg ShakeConfig) *ShakeDirector、AddTrauma(source string, amount float64, priority int) error、Sample(camera Camera, at time.Time) (Offset, error)、SetPaused(paused bool)、RemoveSource(source string) error、SetCameraMode(mode string) error、Amplitude(source string) (float64, bool)、Sources() []string、Paused() bool、Decay(dt time.Duration) error、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) 配置校验：MaxAmplitude、Frequency、FirstPersonScale、ThirdPersonScale 必须为正，DecayPerSecond 不得为负，SuppressedWeight 落在 [0,1]，MaxSources 不得小于一，否则 AddTrauma 与 Sample 返回 ErrBadConfig。
2) AddTrauma 拒绝空白来源返回 ErrBadSource，创伤量非正或非有限返回 ErrBadAmount，优先级为负返回 ErrBadPriority；来源表满且是新来源返回 ErrSourceLimit，已存在的来源不受表满影响。
3) 每个来源的创伤值累加后钳到一；幅度按创伤的平方映射再乘当前相机基准系数，Amplitude 返回这个值，未登记的来源返回 false。
4) 相机基准：第三人称系数与第一人称系数各自乘上去；SetCameraMode 只接受 first 与 third，其他返回 ErrBadMode；切换时震动层全部保留，只按新基准重算幅度。
5) Sample 在同一时刻连续调用必须返回完全相同的偏移；没有任何创伤时返回全零偏移。
6) 暂停期间相位冻结，即使 at 继续前进偏移也保持不变；恢复后相位从冻结处继续推进，暂停期间流逝的时间不计入。
7) Decay 按 DecayPerSecond 每秒削减所有来源的创伤，削到零为止；dt 为负返回 ErrBadDuration。
8) 同一帧多次注入创伤时按平方和合成，而不是把创伤简单相加，避免连续爆炸把幅度顶爆。
9) 多层混合时优先级高的层压制优先级低的层，被压制的层按 SuppressedWeight 加权叠加；压制不重置被压制层的相位，高优先级结束后它必须从原相位继续。
10) 最终偏移的四个分量都受 MaxAmplitude 硬顶约束，任何时刻都不能越界。
11) RemoveSource 删除来源，未登记来源返回 ErrUnknownSource；Sources 按升序返回全部已登记来源。
12) 快照带各来源的创伤、优先级与相位、相机基准、暂停标志与相位时钟；Restore 要么整体生效要么不改现有状态，损坏载荷返回 ErrBadSnapshot。

硬约束：AddTrauma、Sample、Decay 会被战斗线程与相机线程并发调用，任何一次采样只能看到一整批注入之前或之后的状态；相位只能单调前进，暂停期间不得回拨；来源数量与幅度都受配置上限硬顶。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-10`
- 实现文件：`camera_shake_director.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ShakeDirector`
