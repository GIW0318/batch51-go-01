# 4:2:0 色度上采样的 siting 对齐与边缘导向

## 题面

老片修复线做 4:2:0 到 4:4:4 的转换，彩色字幕边缘一直出问题：红色字幕边上有一圈绿边，蓝色裙摆在暗部溢成一片紫；同一帧里左边半幅色度像贴着亮度取样，右半边又像居中取样，横向拉出一条条色带。奇数宽高的镜头在右侧和底部整列发黑，最后一行直接补零；限幅素材切到全范围之后，画面整体偏青，换一条转码路径又偏红。4K 素材转一帧要卡十几秒，超过预算的帧被跳过，输出时间轴短了几帧。重启转码服务之后，同一段素材的边界处理跟之前不一样，接缝处出现一像素的错位。

落点在 gsb/batch51-go-game-c-10 的 chroma_upsampler_p5.go，类型 ChromaUpsamplerP5，包名 task，模块路径 gsb/batch51-go-game-c-10。

公开接口保持：NewChromaUpsamplerP5() *ChromaUpsamplerP5、SetSiting(s Siting) error、SetMatrix(colorspace ColorSpace) error、Upsample(y, uPlane, vPlane Plane) (Chroma444, error)、SetEdgeGuided(on bool) error、SetRange(full bool) error、SetBitDepth(bits int) error、Stats() UpsampleStats、Reset() error、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) siting 只接受 center、left、top_left，其它值报 ErrBadSiting 并保持旧值；整帧必须用同一个 siting，不能一半左对齐一半居中。
2) 矩阵只接受 bt709 与 bt601，越界报 ErrBadMatrix；位深只接受八到十六，越界报 ErrBadBitDepth；范围开关默认全范围。
3) 亮度与两路色度的宽高必须满足 4:2:0 布局，色度宽高等于亮度的一半向上取整，任何不符报 ErrPlaneMismatch。
4) 宽高非正或任一路没有样本报 ErrEmptyPlane；宽乘高超过 MaxPixels 报 ErrFrameTooLarge；校验顺序按宽高、像素预算、空样本、色度布局依次进行。
5) 上采样按 siting 确定色度样本在亮度网格上的位置，横向与纵向都做线性插值，边界样本按最近样本复制，不得外插出界。
6) 4:2:0 在奇数尺寸下最后一行或列没有配对样本，按最近样本复制而不是补零。
7) 输出 Y 与输入亮度逐样本相同，U、V 为全分辨率，长度都等于宽乘高。
8) 边缘导向开启时，按亮度梯度限制色度插值权重，防止彩色边缘过冲与溢色；关闭时按纯线性插值。
9) limited 与 full range 的转换顺序固定，先做范围归一再做矩阵系数，不能因路径不同产生色偏。
10) 每帧的插值采样次数设上限，超过预算按块降档处理，不能跳过整帧。
11) Stats 的帧计数按成功帧递增，样本计数累计宽乘高，Reset 只清计数不清设置。
12) 快照保存 siting、矩阵、位深、范围、边缘导向与计数，恢复后帧边界处理与快照前一致；损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：Upsample、SetSiting 与 Snapshot 会被解码线程和显示线程同时调用，设置切换要么整帧生效要么整帧不生效，读方不能看到半新半旧的配置；单帧像素数与插值采样次数受上限硬顶，超限报错或降档而不是静默截断；重启后按快照恢复，siting、位深与计数逐项一致。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；边界处理与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-10`
- 实现文件：`chroma_upsampler_p5.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ChromaUpsamplerP5`
