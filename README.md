# IES 配光曲线解析与图集化：角度约定、旋转对称合并与槽位搬迁原子更新

## 题面

展厅灯光换成实时配光之后，同一盏射灯在墙面上的光斑一半亮一半暗，转个角度看又反过来；换了型号之后光斑直接消失，日志里没有任何记录；切换灯具预设时全场灯会闪一帧全黑，紧接着光斑跳到另一个位置。美术还发现曲线在 0 度与 360 度两侧的强度对不上，绕一圈能看到一条硬缝。

落点在 gsb/batch51-go-game-b-08 的 ies_profile_atlas.go，类型 IesAtlas，包名 task，模块路径 gsb/batch51-go-game-b-08。

公开接口保持：NewIesAtlas(limits AtlasLimits) *IesAtlas、Parse(raw []byte) (Profile, error)、Resample(p Profile, hRes, vRes int) (LUT, error)、Pack(luts []LUT) (Atlas, error)、Sample(slot SlotID, h, v float64) (float64, error)、BindLight(lightID string, slot SlotID) error、HandleFor(lightID string) (SlotID, bool)、Release(slot SlotID) error、Compact(budget int) (int, error)、Quarantine() []QuarantineEntry、Snapshot() ([]byte, error)、Restore(data []byte) error。

配光文件是文本格式，前五行固定：第一行 IESLUT 加版本号，第二行 TYPE 声明 A、B、C 之一，第三行 HMAP 水平角，第四行 VMAP 垂直角，第五行 TOTALS 数值个数，后面是按垂直角分行、逗号分隔的坎德拉值。角度表必须严格递增，C 型水平角落在 [0,360]、垂直角落在 [0,90]，B 型水平角落在 [-90,90]，A 型水平角落在 [-180,180]，A、B 两型垂直角都落在 [-90,90]。

需要落实的行为：

1) 解析失败的文件一律进入隔离区并保留原始哈希，哈希取 SHA-256 十六进制的前 16 个字符，同时保留失败原因；隔离区按 MaxQuarantine 只保留最新的若干条。
2) 头部不是 IESLUT 加受支持的版本号返回 ErrBadFormat；TYPE 不是 A、B、C 返回 ErrBadType；角度表不严格递增或超出该型范围返回 ErrBadAngles；TOTALS 与两张角度表条目数之积不符、或实际数值个数与 TOTALS 不符返回 ErrBadCount；出现负数或非有限值返回 ErrBadValue。
3) 同一灯具存在多种角度约定时以文件里的 TYPE 声明为准，不能按来源文件名或扩展名猜。
4) C 型曲线每一行数值都相同时标记为旋转对称；Resample 对对称曲线只存一列，LUT 的 HRes 固定为一，Symmetric 为真。
5) Resample 要求 hRes 至少为一、vRes 至少为二，非对称曲线 hRes 至少为二，否则返回 ErrBadResolution；输出网格在角度表覆盖范围内等距铺开并含两端，数值双线性插值、按垂直角分行存放。
6) Sample 的角度超出覆盖范围时钳到最近的端点值，不报错；槽位未打包或已释放返回 ErrNoSlot。
7) Pack 按尺寸分桶，桶宽取不小于水平与垂直分辨率较大者的最小 2 的幂，同桶里优先复用编号最小的空槽；整份 Atlas 的字节数为各桶宽平方乘八之和，槽数超过 MaxSlots 或字节数超过 MaxBytes 返回 ErrAtlasFull，不做静默淘汰。
8) 相同输入重复 Pack 必须得到逐字段相同的槽位布局，槽位编号从零开始递增。
9) BindLight 拒绝空白句柄返回 ErrBadLight，指向未打包槽位返回 ErrNoSlot；Release 之后指向该槽位的灯光句柄必须解绑。
10) Compact 把空槽往尾部收，每搬一次算一次预算，budget 为负返回 ErrBadBudget；碎片整理搬迁槽位时，引用该槽位的灯光句柄必须在同一帧原子更新，不能出现旧槽位空读。
11) 0 度与 360 度接缝处数值不一致时按周期插值，不能直接钳制到端点。
12) 快照带槽位表、曲线数值、灯光句柄与隔离区；Restore 要么整体生效要么不改现有状态，损坏载荷返回 ErrBadSnapshot。

硬约束：Pack、BindLight、Sample 会被灯具更新线程与渲染线程并发调用，任何一帧里灯光句柄只能指向搬迁前或搬迁后的完整槽位；槽位表、字节数与隔离区都受配置上限硬顶。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-08`
- 实现文件：`ies_profile_atlas.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`IesAtlas`
