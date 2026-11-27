# 次表面散射的厚度感知模糊与剖面分组

## 题面

换成蜡质材质之后，角色的脸边缘像贴了一张透明胶：该透出来的暖红被抹成一片灰，背景的暗色顺着脖子渗进来。把质量档从最高降到最低，整个画面像被人往下压了两档亮度，皮肤发灰，美术只能重新调曝光。厚度图里薄到接近零的耳廓直接变成了硬边剪影，另一处则是同屏出现四种材质之后，边缘那些混色像素一律按其中一种材质处理，接缝处颜色跳变。

落点在 gsb/batch51-go-game-b-20 的 subsurface_scattering_pipeline.go，类型 SubsurfaceScatteringPipeline，包名 task，模块路径 gsb/batch51-go-game-b-20。

公开接口保持：NewSubsurfaceScatteringPipeline(limits SssLimits) *SubsurfaceScatteringPipeline、RegisterProfile(id string, profile DiffusionProfile) error、SampleProfile(materialID uint16) (DiffusionProfile, error)、ProfileCount() int、SetMaterialMap(tex MaterialIDMap) error、SetThicknessMap(tex ThicknessMap) error、SetQuality(level int) error、SetKernelBudget(n int) error、Blur(src Image, depth DepthTex) (Image, error)、Stats() SSSStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) 剖面注册时标识与名称不能为空、材质编号不能为零、扩散半径必须为正，否则报 ErrBadProfile；材质编号或标识重复报 ErrProfileExists；剖面数超过 MaxProfiles 报 ErrProfileLimit。
2) SampleProfile 按材质编号取剖面，零号报 ErrNoMaterial，未注册报 ErrUnknownMaterial；ProfileCount 只数已注册的剖面。
3) 材质编号图的槽位数必须等于宽乘高、主材质权重必须落在零到一之间，否则报 ErrBadMaterialMap；厚度图槽位不符或出现负值报 ErrBadThickness。
4) 质量档只接受零到三，越界报 ErrBadQuality；四档的模糊核半径取 KernelRadii 里的四、三、二、一，Stats 里的核半径要随时反映当前档位。
5) 核预算必须为正且不超过 MaxKernelBudget，否则报 ErrBadBudget。
6) Blur 缺少材质图报 ErrNoMaterialMap，缺少厚度图报 ErrNoThicknessMap，没有任何剖面报 ErrNoProfile，源图与材质图或深度图尺寸不符报 ErrBadImage。
7) 每个像素取邻域内半径由核半径决定的方形采样点做加权平均；邻域深度与中心深度相差超过 MaxDepthDelta 的点不参与，保证前后景不串色；一个点都没入选时保留原像素。
8) 采样点数等于像素数乘核边长的平方，核边长是二乘半径加一；总数超过核预算时先降一档质量再重算，降档次数记入 Degrades 并且不得跳过整帧；降到最低档仍然超预算才报 ErrFrameBudget。
9) 同屏出现的剖面数超过 MaxGroups 时按屏幕覆盖面积合并成不超过 MaxGroups 组，逐像素只按所在组取核，不得每个像素各挑一个剖面；Stats 里的组数反映本次实际使用的组数。
10) 一个像素同时混了两种材质时按覆盖权重混合两者的剖面，而不是取其中一种；厚度接近零的像素要缩小扩散范围但不能退化成硬边；降档之后核变小必须补偿亮度，否则整帧会变暗。
11) 快照包含剖面表、材质图、厚度图、质量档、核预算与计数器，载荷损坏报 ErrBadSnapshot 且状态不变；恢复后 SampleProfile 与 Stats 里的档位、核半径都要与原来的实例一致。

硬约束：Blur、SetQuality、SetMaterialMap、RegisterProfile、Snapshot 会被渲染线程与资源加载线程并发调用，一次模糊只能看到同一套材质图、厚度图与质量档，不允许半新半旧；每帧采样数与临时缓冲字节数是硬顶，超限只能降档或报错，不得静默丢像素；快照只能整份生效，恢复后剖面表与档位必须自洽。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发降档与快照恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-20`
- 实现文件：`subsurface_scattering_pipeline.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SubsurfaceScatteringPipeline`
