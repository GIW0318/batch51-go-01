# 4:2:0 色度上采样与相位对齐

## 题面

短视频转码集群换成新的上采样库之后，成片里人脸边缘多出一圈紫边，字幕条附近出现横向色带。同一段素材在偶数宽高的机器上正常，导到一千零八十三行的竖版工程就露出底边一条彩色渐变。更麻烦的是标了 cosited 相位的那一批，人物轮廓整体偏了半个像素，抠像蒙版和画面永远对不齐。

落点在 gsb/batch51-go-game-c-02 的 chroma_upsampler.go，类型 ChromaUpsampler，包名 task，模块路径 gsb/batch51-go-game-c-02。

公开接口保持：NewChromaUpsampler(cfg ChromaConfig) *ChromaUpsampler、Upsample(frame YUVFrame) (YUV444, error)、Sample(frame YUVFrame, x, y int) (float64, float64, float64, error)、SetPhase(phase Phase) error、SetSubsampling(mode Subsampling) error、SetEdgeMode(mode EdgeMode) error、HandleOddSize(w, h int) (PadPlan, error)、Snapshot() ([]byte, error)、Restore([]byte) error、Subsampling() Subsampling、Phase() Phase、Edge() EdgeMode、BlockCount() int、Reset() error。

需要落实的行为：

1) 配置缺省时按 4:2:0、centered 相位、directional 边缘模式处理，像素预算取一千六百万，分块行数取十六。
2) 4:2:0 的色度网格宽高都是亮度的一半向上取整；4:2:2 的色度高度与亮度一致，只有宽度减半。
3) centered 相位下第 i 个色度样本落在亮度坐标 2i 加 0.5 的位置；横向线性插值后，四像素宽的零到十斜坡应得到零、二点五、七点五、十，两端按最近样本钳住。
4) 输出尺寸与输入亮度尺寸完全一致，亮度平面逐样本原样拷贝，不允许因为补齐改变宽高。
5) 亮度或色度平面长度与声明尺寸不符、宽高不是正数，都报 ErrBadFrame 并且不返回半成品。
6) 像素总数超过预算报 ErrPixelBudget，先判预算再分配，不能先分配再拒绝。
7) 切换子采样、相位、边缘模式只接受 420、422、centered、cosited、directional、bilinear，其它字符串分别报 ErrBadSubsampling、ErrBadPhase、ErrBadEdge，失败时原值不变。
8) HandleOddSize 返回输出宽高、色度网格宽高、右边界与下边界需要补齐的样本数以及分块数，偶数尺寸时补齐数为零。
9) 分块按配置行数推进，BlockCount 记录上一次上采样实际处理的块数，四行两块的结果必须是二。
10) 色度采样相位为 cosited 时上采样网格相对亮度偏移半个像素，边缘插值必须按该相位计算，不能沿用 centered 的坐标。
11) 奇数高度最后一行没有对应色度行时按镜像补齐，避免底部色带；奇数宽度同理。
12) 帧标记为 RGB 空间时，边缘导向插值前必须先转到 YUV，否则色度边缘判定会被亮度主导。

硬约束：同一实例的配置与计数器会被解码线程和预览线程同时读取，Snapshot 必须一次拿到完整的配置与计数器，不能读到切了一半的相位；Reset 与 Upsample 并发时计数只能归零一次。像素预算与分块数都是硬上限，超限报错而不是静默降级。Snapshot 与 Restore 要能跨进程还原子采样、相位、边缘模式、像素预算、分块行数与计数，损坏的快照报 ErrBadSnapshot 且保持现网配置。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-02`
- 实现文件：`chroma_upsampler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ChromaUpsampler`
