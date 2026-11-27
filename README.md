# 视差遮挡贴图的高度场步进、切线空间采样与距离淡出

## 题面

石墙材质的视差在通关镜头里明显穿帮：镜头贴近墙面时砖缝的凹凸是对的，一转到掠射角，整面墙的砖缝开始左右抽动，最后干脆退回一张平贴图；贴着墙角的位置还能看到隔壁地砖的花纹渗进来；远处墙面在 LOD 切换的一瞬间视差幅度突然翻倍，像整面墙在呼吸。更费解的是同一条材质被三个物件同时请求构建变体，构建耗时直接叠了三倍。

落点在 gsb/batch51-go-game-b-15 的 parallax_occlusion_mapper.go，类型 ParallaxOcclusionMapper，包名 task，模块路径 gsb/batch51-go-game-b-15。

公开接口保持：NewParallaxOcclusionMapper(cfg POMConfig) (*ParallaxOcclusionMapper, error)、SetHeightMap(id string, tex HeightMap) error、SetSteps(n int) error、SetMaxSteps(n int) error、Steps() int、SetDepthScale(s float32) error、DepthScale() float32、SetFadeRange(near, far float32) error、HeightAt(m MaterialRef, uv Vec2) (float32, error)、Sample(m MaterialRef, uv Vec2, viewTS Vec3) (SampleResult, error)、BuildVariant(lod int) (POMVariant, error)、Variant(lod int) (POMVariant, bool)、Stats() POMStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 构造校验：MaxSteps、MaxTexSize、MaxVariants、MaxLOD、ParallelZ 为正，MaxDepthScale 为正且不大于一，FadeNear 不小于零，FadeFar 大于 FadeNear，否则返回 ErrBadConfig。
2) SetHeightMap：空 id 返回 ErrBadMaterial；宽高或图集列行为零、载荷长度不等于宽乘高、任一样本不在零到一之间、单边超过 MaxTexSize，返回 ErrBadHeightMap；同一 id 再次设置是整体替换。
3) SetSteps 只接受一到 MaxSteps，越界返回 ErrBadSteps；SetMaxSteps 只接受正整数，并且把当前步数同步钳到新上限，越界返回 ErrBadSteps。
4) SetDepthScale 只接受大于零且不超过 MaxDepthScale 的值，否则 ErrBadScale；SetFadeRange 要求 near 不小于零且 far 大于 near，否则 ErrBadFade。
5) HeightAt 先按图集网格与格号定位样本所属的那一格，再在格内对基础 uv 做双线性采样，uv 与边缘都钳到格内；未知材质返回 ErrUnknownMaterial，格号超出网格返回 ErrBadMaterial。
6) Sample 先校验视线向量长度大于零，否则返回 ErrBadView。淡出系数按材质距离在 near 到 far 之间线性从一降到零，near 以内为一，far 以外为零。
7) 位移公式：结果 uv 的 U、V 分量等于基础 uv 的 U、V 分别减去 viewTS 的 X、Y 除以 viewTS 的 Z，再乘 depthScale、淡出系数与基础 uv 处的高度；结果的 Height 是基础 uv 处的采样高度，Fade 是淡出系数。
8) 步数自适应：全权重时的步数取当前设置，实际步数等于该步数乘淡出系数四舍五入后钳到至少一，近处不少于远处；Stats 的 StepTotal 累加实际步数，Samples 累加采样次数。
9) 视线几乎与表面平行，也就是 Z 的绝对值低于 ParallelZ 时，必须提前终止：直接返回基础 uv 与基础高度，不做位移，不能一路步进到上限。
10) 轮廓裁剪：位移后的 uv 越出所属图集格时钳回格内并把 Clipped 置真，越界那段样本丢弃而不是外插，绝不能采到相邻材质的图集内容。
11) 变体：BuildVariant 按 LOD 唯一缓存，lod 必须在零到 MaxLOD 之间否则 ErrBadLOD，尚未注册任何高度图返回 ErrBadMaterial；同一 LOD 的并发构建合并成一次，其余调用只等结果，合并次数进 Merged，构建期间读方拿到旧变体；LOD 切换时深度缩放要随 mip 调整，否则远处视差幅度会突变。
12) 快照保存高度图、步数与上限、深度缩放、淡出区间、平行阈值与变体缓存；Restore 要么整体生效要么不改现状，载荷损坏或为空返回 ErrBadSnapshot，恢复后步数、缩放与采样结果必须一致。

硬约束：单像素步进数、高度图尺寸与变体缓存条数都是硬顶，超限返回错误而不是静默丢弃；Sample、BuildVariant、Snapshot 会被材质与流式加载两条线程并发调用，变体缓存与参数表必须整表原子替换，读者只能看到完整旧表或完整新表。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-15`
- 实现文件：`parallax_occlusion_mapper.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ParallaxOcclusionMapper`
