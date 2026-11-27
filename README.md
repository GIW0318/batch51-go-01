# 视频时域降噪的运动补偿历史与场景切重置

## 题面

旧片修复项目在时域降噪这一步翻车：静止镜头干净，一摇镜就出现大面积拖影，人物轮廓后面跟着一条半透明的尾巴；快速横摇时画面上出现成片新长出来的区域，强度档却自己跳到了最高，原本调好的颗粒感全被抹平。场景切换处上一镜头的残影被带进下一镜头，第一个画面糊成一片；连续切两个镜头时，第二个镜头又莫名干净得过分。运动矢量在山体边缘指向画面外，那一块直接黑掉；4K 素材一帧超过预算，整帧被丢弃，时间线整整短了两秒。转码进程重启之后，累积的帧数从零开始，前几秒的画质跟后面完全不一样。

落点在 gsb/batch51-go-game-c-09 的 video_temporal_denoiser.go，类型 VideoTemporalDenoiser，包名 task，模块路径 gsb/batch51-go-game-c-09。

公开接口保持：NewVideoTemporalDenoiser() *VideoTemporalDenoiser、SetStrength(level int) error、SetMotionVectors(mv MotionField) error、Denoise(frame Frame) (Frame, error)、NotifySceneCut() error、SetHistoryDepth(n int) error、Reset() error、SetBlockSize(px int) error、Stats() DenoiseStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 强度只接受一到五，越界报 ErrStrengthRange；历史权重按档固定为五分之一、零点三五、二分之一、零点六五、五分之四。
2) 块大小只接受八、十六、三十二，越界报 ErrBlockSize；历史深度只接受一到八，越界报 ErrHistoryDepth。
3) 帧的宽高必须为正，宽乘高超过 MaxPixels 报 ErrFrameTooLarge，样本数不等于宽乘高报 ErrBadFrame；校验顺序按尺寸、像素预算、样本长度依次进行。
4) 第一帧没有历史，输出与输入逐样本相同；此后每个块按当前帧与历史帧加权混合，历史按最近若干帧的均值累积，深度由 SetHistoryDepth 决定。
5) 运动矢量按块给出，块内每个像素到上一帧的偏移为 VX、VY，采样历史时使用偏移后的坐标；网格尺寸必须与块划分一致，分量绝对值超过 MaxMotion 或块大小不受支持报 ErrBadMotionField。
6) 运动矢量指向画面外时，该块退化为空域降噪，不采样越界历史。
7) 快速摇镜产生大面积新生区域时只降低历史权重，不能自行改变强度档。
8) 场景切换清空历史，下一帧不与上一镜头混合，不能把上一镜头的拖影带进来。
9) 连续两帧都判定场景切换时，第二帧不能复用第一帧刚建立的历史。
10) Stats 的帧计数按成功处理的帧递增，块计数给出本帧处理的块数，场景切换与重置分别计数。
11) Reset 清空历史与计数器，下一次 Denoise 按首帧处理。
12) 快照保存历史帧、累积计数与配置，恢复后继续累积；损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：Denoise、SetMotionVectors 与 Snapshot 会被采集线程和编码线程同时调用，历史帧的推进要么整帧生效要么整帧不生效，读方不能看到半新半旧的历史；单帧像素数超过预算按非法输入拒绝，块处理数另设预算，超预算时降低块粒度处理而不是丢弃整帧；重启后按快照恢复，历史帧与累积计数逐项一致。

验收：go test ./... 十五条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-c-09`
- 实现文件：`video_temporal_denoiser.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`VideoTemporalDenoiser`
