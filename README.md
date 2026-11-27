# 描边合成的边缘检测、遮挡分层与线宽预算

## 题面

版本验收时描边整个糊了：选中敌方单位以后，两只怪并排站着的位置，脚底的描边叠成了第三种颜色，线宽也变成两倍；被前景掩体挡住的目标，轮廓从掩体上面透出来，背景楼房的边缘还被一起描了一道。战斗里同时命中六个目标，描边像素涨到接近全屏，帧率从六十掉到三十，程序这边干脆把一部分目标整块丢掉，画面上只剩半截描边。

落点在 gsb/batch51-go-game-b-14 的 outline_composer.go，类型 OutlineComposer，包名 task，模块路径 gsb/batch51-go-game-b-14。

公开接口保持：NewOutlineComposer(cfg OutlineConfig) (*OutlineComposer, error)、SetStyle(id string, st OutlineStyle) error、RemoveStyle(id string) error、SetSelective(id string, on bool) error、Selective(id string) bool、Priority(id string) int、SetWidthPx(px float32) error、SetOcclusionMode(mode OcclusionMode) error、Compose(depth DepthTex, normal NormalTex, mask MaskTex) (OutlineImage, error)、Stats() OutlineStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 构造校验：MaxStyles、MaxWidthPx、MaxPixels、MaxTexPixels 为正，DepthThreshold 为正，NormalThreshold 落在负一到一之间，否则返回 ErrBadConfig。
2) SetStyle：空 id、样式自带的 ID 与 id 不一致、线宽不为正或大于 MaxWidthPx、颜色任一分量不在零到一之间、优先级不在零到九十九之间，返回 ErrBadStyle；样式表满返回 ErrStyleLimit，替换已有 id 不占用新槽位。
3) SetSelective 与 RemoveStyle 对未知 id 返回 ErrUnknownStyle，RemoveStyle 删除样式时连同它的选择性标记一起移除；Selective 对未知 id 返回 false，Priority 对未知 id 返回负一。
4) 边缘检测逐像素比较上下左右四个邻接，画面边界按钳制处理；邻接遮罩 id 与本像素不同即为目标轮廓；遮罩 id 相同但深度差超过 DepthThreshold，或法线点乘低于 NormalThreshold，同样算轮廓。
5) 只有开启 selective 的目标参与合成，背景像素（遮罩为零）永不描边；每次合成后 Stats 的 Targets 是参与合成的目标数，Pixels 是写入的描边像素数，Samples 是邻接比较次数。
6) 颜色与线宽按目标唯一生效：一个像素最多写入一个目标的颜色，不允许两种颜色混合，也不允许两条线宽相加。
7) 遮挡模式：OcclusionMasked 下被更高优先级目标且更近处遮挡的轮廓像素不下笔，OcclusionNone 下全部下笔；未知模式返回 ErrBadOcclusion。
8) 线宽：全局宽度由 SetWidthPx 设置（默认一），必须为正且不超过 MaxWidthPx，否则 ErrBadWidthPx；样式自带正线宽时以样式为准；线宽按像素半径向目标外侧扩张，扩张不得覆盖遮罩为零以外的更高优先级目标。
9) 像素预算：每帧描边像素数受 MaxPixels 硬顶，超限先把线宽逐档降到一像素，仍然超限才按屏幕占比丢弃优先级最低的目标；降档次数计入 Downgraded；线宽降档时遮挡分层的先后顺序不得反转。
10) 纹理校验：深度、法线、遮罩三者宽高一致且为正，载荷长度等于宽乘高，总像素数不超过 MaxTexPixels，否则返回 ErrBadTexture。
11) 遮挡描边必须靠掩码分层：不能把背景边缘一起描出，也不能让低优先级描边穿透高优先级；目标只有部分轮廓压到屏幕边缘时，按裁剪后的轮廓描边，不能整块丢弃。
12) 快照保存样式表、选择性集合、线宽、遮挡模式与预算计数；Restore 要么整体生效要么不改现状，载荷损坏或为空返回 ErrBadSnapshot。

硬约束：SetStyle、SetSelective、Compose、SetWidthPx、Snapshot 会被 UI 与渲染两条线程并发调用，样式表必须整表原子替换，读者只能看到完整的旧表或完整的新表；每帧描边像素数与检测采样次数都是硬顶，超限不得跳成空白；样式表槽位是硬顶。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-14`
- 实现文件：`outline_composer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`OutlineComposer`
