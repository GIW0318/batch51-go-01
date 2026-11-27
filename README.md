# 位深削减的抖动、噪声整形与通道去相关

## 题面

母带交付前做位深削减，客户连续退了三版：十六位版本在高音区出现沙沙的颗粒，安静段落尤其明显；换成二十四位又听不出差别，但文件体积翻了一倍。立体声版本比单声道版本少了一层空气感，左右声道贴得极近，接上耳机几乎听不出宽度。把一段静音前奏单独导出，客户反馈开头两秒什么都没有，接上正片时反而有一个爆点；把位深从二十四改到十六再改回来，前半段残留的量化噪声比后半段明显高一截。同一份工程在两台机器上导出，哈希对不上，AB 对比时总能听出位置不同的抖动。

落点在 gsb/batch51-go-game-c-08 的 dither_noise_shaper.go，类型 DitherNoiseShaper，包名 task，模块路径 gsb/batch51-go-game-c-08。

公开接口保持：NewDitherNoiseShaper(bits int) *DitherNoiseShaper、SetTargetBits(bits int) error、SetShapeCurve(curve []float64) error、SetSeed(seed uint64) error、Process(in []float32, channels int) ([]int16, error)、ResetChannel(ch int) error、ErrorSpectrum(ch int) ([]float64, error)、SetClipGuard(on bool) error、Stats() DitherStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 目标位深只接受四到二十四，越界报 ErrBitsRange 并保持旧值；构造时给出窗口外的值按十六位处理。
2) 整形曲线长度不超过三十二，系数绝对值不超过一，越界分别报 ErrCurveLength 与 ErrCurveCoeff，被拒的曲线不生效；空曲线表示关闭整形。
3) 抖动必须是三角分布，每通道用不同子种子生成，同一种子整批可复现；换种子后序列必须变化。
4) 量化满刻度对应目标位深的最大正码值，输出按通道交织，样本数不是通道数的整数倍报 ErrRaggedInput，通道数不在 1 到 8 报 ErrChannelCount，帧数超过 MaxBlockFrames 报 ErrFramesRange。
5) 每通道误差反馈状态独立，整形按曲线对最近若干帧误差加权后加到量化输入上。
6) ErrorSpectrum 返回该通道保留的误差历史，长度等于曲线长度，最近一帧在末尾；通道号越界报 ErrUnknownChannel。
7) ResetChannel 清空该通道误差历史并把抖动子种子复位到初始位置，其它通道不受影响。
8) 目标位深变化要重置该通道的整形历史，不能把旧位深的量化误差带进新位深。
9) 静音段仍要输出抖动噪声，不能因为输入能量为零就跳过量化。
10) 削波保护默认开启，量化前把越界样本限幅到正负满刻度并逐样本累计 Clipped，量化后再夹到目标位深的表示范围，保证满刻度样本不会回绕；关闭保护时不限幅，越界样本按整型回绕，不计数。
11) 单声道与立体声在同一 seed 下不能产生完全相同的抖动序列，否则双声道听感会塌成单声道。
12) 快照保存位深、曲线、种子、各通道误差历史与随机状态，恢复后逐样本一致；损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：Process 会被多路离线渲染线程并发调用，同一通道的误差历史与随机状态必须串行推进，快照只能看到整批提交前或提交后的状态；输出缓冲按帧数硬顶，超限报错而不是截断；重启后按快照恢复，各通道误差历史与随机位置逐项一致。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-08`
- 实现文件：`dither_noise_shaper.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DitherNoiseShaper`
