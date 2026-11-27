# Ambisonics 声床编码与双耳解码

## 题面

演唱会直播的沉浸声返送这两周频繁翻车：观众席前排的鼓声从上往下压，转到左耳又突然跳到右耳；调音师把声床旋转九十度之后，正后方的和声干脆消失了。同一版工程在两台导播机上导出，一台声音偏亮，另一台侧向能量明显更弱，怀疑归一化模式被人改过。夜间重跑还冒出两个问题：切换归一化之后，前半段素材的电平比后半段低了一大截；换成更高精度的头相关数据集，前一轨的尾音会拖进新一轨的开头。

落点在 gsb/batch51-go-game-c-01 的 ambisonic_bed_encoder.go，类型 AmbisonicEncoder，包名 task，模块路径 gsb/batch51-go-game-c-01。

公开接口保持：NewAmbisonicEncoder(cfg AmbisonicConfig) *AmbisonicEncoder、Encode(source Source) ([]float64, error)、Rotate(q Quaternion) error、DecodeBinaural(bed []float64) ([]float32, error)、SetNormalization(mode NormMode) error、SetHRTF(name string, taps int) error、Snapshot() ([]byte, error)、Restore([]byte) error、Bed() []float64、Reset() error、Order() int、Channels() int、Norm() NormMode、Taps() int、BlockSize() int、SourceCount() int、Truncated() int、Rotation() Quaternion、ConvolutionCost() int。

需要落实的行为：

1) 阶数只允许一到三阶，配置越界按一阶处理；归一化模式为空按 SN3D 处理；抽头数、块长、每帧乘加预算、声源条数上限缺省时分别取十六、一百二十八、四千零九十六、六十四。
2) 通道数等于阶数加一的平方，ACN 排序；SN3D 基底下正前方单位声源得到 1、0、0、1，正左方得到 1、1、0、0，正上方得到 1、0、1、0。
3) 方位角取负一百八十到正一百八十度，仰角取负九十到正九十度；声源编号为空、增益为负、角度越界都报 ErrBadSource，且不改变已有声床。
4) Encode 把声源叠加进声床并返回叠加后的整条声床，源计数随之加一；同一方位两次半增益与一次整增益结果一致。
5) 单条声源可以自带较低阶数，只填前若干通道，其余通道保持零；自带阶数高于编码器阶数时按截止阶数截断，截断计数加一。
6) 归一化切换只接受 sn3d 与 n3d，其它字符串报 ErrBadNorm 且模式保持原值；N3D 相对 SN3D 在第 l 带乘根号下二 l 加一。
7) Rotate 只接受单位四元数，模长与一的偏差超过百万分之一报 ErrBadQuaternion；绕竖直轴转九十度后，正前方的声源应落到正左方，恒等旋转不改变声床。
8) DecodeBinaural 的床长度必须等于通道数，否则报 ErrBadBedLength；输出为左右交织样本，长度是块长的两倍，正前方声床的左右声道逐样本相等。
9) ConvolutionCost 返回通道数乘抽头数；SetHRTF 在抽头数越界或超出每帧预算时报错。
10) 声源位于正上方时方位角不唯一，旋转必须保持仰角连续，不能因为方位角跳变产生爆音。
11) 归一化模式在运行中切换时，已经编码进声床的通道必须整批重标定，不能新旧两种尺度逐帧混用。
12) 头相关数据集切换时卷积器状态必须清空并按新抽头长度重建，不能把上一轨的尾音带进新一轨。

硬约束：Encode、Rotate、Snapshot 会被音频线程与导出线程同时调用，快照要么看到整批提交前的声床，要么看到整批提交后的声床，不能拿到写了一半的通道；Reset 与 Encode 并发时不能把刚提交的声源写回空声床。声源条数受上限硬顶，超限报错不静默丢弃。Snapshot 与 Restore 必须能跨进程还原阶数、归一化、抽头、块长、声床、源计数与当前旋转，损坏的快照报 ErrBadSnapshot 且不动现网状态。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-01`
- 实现文件：`ambisonic_bed_encoder.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AmbisonicEncoder`
