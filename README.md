# 精灵图集矩形装箱、旋转内边距与增量重打包的 UV 稳定性

## 题面

换皮活动上线前，UI 图标整批错位：卡面图热更新后采到了隔壁图标的一角；美术补了十几张六十四见方的小图标，图集页数从两页涨到四页，新页却大片空白。更别扭的是一像素宽的进度条精灵开了旋转以后，右边缘总多采进相邻帧的一列。策划要求不重新打包也能继续加图，加图后老图标 UV 不许漂移。

落点在 gsb/batch51-go-game-b-12 的 sprite_atlas_packer.go，类型 SpriteAtlasPacker，包名 task，模块路径 gsb/batch51-go-game-b-12。

公开接口保持：NewSpriteAtlasPacker(cfg AtlasConfig) (*SpriteAtlasPacker, error)、Add(s SpriteReq) (Placement, error)、AddBatch(ss []SpriteReq) ([]Placement, error)、Remove(id string) error、Rebuild(opts RepackOptions) (RepackReport, error)、PageCount() int、SetPageBudget(n int) error、Placement(id string) (Placement, bool)、Sprite(id string) (SpriteReq, bool)、Stats() AtlasStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 构造校验：页宽页高为正，MaxPages 与 MaxSprite 为正，Padding 不小于零，MaxArea 为正，否则返回 ErrBadConfig。
2) Add 校验：空 id、宽或高不为正、任一边大于 MaxSprite 返回 ErrBadSprite；id 已存在返回 ErrDuplicateSprite，且原占位不动。
3) 占位用货架式布局：页内自左上角起排，同行的下一个精灵横坐标是上一个的横坐标加宽再加 Padding，行高取该行最高者；当前行放不下就换行，纵坐标是上一行顶加行高再加 Padding；整页放不下才开新页，页号升序扫描取第一个放得下的页。
4) 旋转：只有请求允许旋转且不旋转放不下、旋转后能放下时才旋转，Rotated 置真，页内宽高互换；不旋转能放下时不得旋转。
5) 新页受 MaxPages 与 MaxArea 双限：开新页会让页面积超过 MaxArea 时返回 ErrAreaBudget，页数达到 MaxPages 返回 ErrPageBudget，两者都不得留下半个占位。
6) 没有显式 Rebuild 时已有精灵的 UV 一律不变，新精灵只能落进旧页空洞或新开的页；删除留下的空洞按面积最接近者优先复用，且只能在原页内复用，不得把请求挪到别的页。
7) AddBatch 先整批校验再整批占位：批内重复 id、与已有 id 冲突、任一尺寸非法、或触到页数、面积上限，都返回 ErrBatchRejected 并且整批不生效，返回的切片顺序与请求顺序一致。
8) Remove 删除占位并把矩形记为空洞，精灵数与占用面积同步回落，页数不回收；未知 id 返回 ErrUnknownSprite。
9) Rebuild 按最初加入的顺序整体重排，页宽高、Padding 与旋转开关取自参数，参数非法返回 ErrBadConfig；重排需要的页数超上限返回 ErrPageBudget、面积超限返回 ErrAreaBudget，失败时现有占位一个都不许动；成功时整页原子切换，切换之前旧页仍可被采样，报告的 Pages 是新页数、Placed 是重排个数、Moved 是按 id 升序列出的页号或矩形有变化的精灵。
10) 占位的 U0、V0 等于横纵坐标除以页宽页高，U1、V1 等于右下角坐标除以页宽页高，矩形不得越出页面，任意两个同页矩形不得重叠。
11) SetPageBudget 只接受正整数且不得小于当前页数，否则分别返回 ErrBadConfig 与 ErrPageBudget。
12) 快照带配置、请求、加入顺序、页货架与占位；Restore 要么整体生效要么不改现状，载荷损坏或为空返回 ErrBadSnapshot，恢复后 PageCount、Placement 与 Stats 须与快照一致。

硬约束：Add、AddBatch、Remove、Rebuild、Snapshot 会被资源加载线程并发调用，占位串行提交、id 唯一，读者只能看到完整旧图集或完整新图集；页数、页面积、单边尺寸都是硬顶，超限返回错误而不是静默丢弃；1xN 与 Nx1 这类极端精灵旋转后连同内边距必须仍然落在页内，不能越界一个像素。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-12`
- 实现文件：`sprite_atlas_packer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SpriteAtlasPacker`
