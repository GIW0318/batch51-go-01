# 范围技能命中结算：几何判定、遮挡、距离衰减与目标上限

## 题面

赛季更新后，范围技能伤害结算出现一批异常：训练场里三个假人排成直线，一发火墙只有最近的掉血，两侧毫无反应；同一发技能换个朝向，站在扇形边上的假人有时算命中有时不算，录像里两帧的伤害数字还对不上。玩家对着同一片空地连按两次，第二次的伤害总值比第一次高，排行榜积分跟着上涨，客服只能反复回复“以服务器为准”。

落点在 gsb/batch51-go-game-a-16 的 aoe_target_resolver.go，类型 AoeTargetResolver，包名 task，模块路径 gsb/batch51-go-game-a-16。公开接口保持：NewAoeTargetResolver(limits AoeLimits) *AoeTargetResolver、SetShape(s AoeShape) error、SetMaxTargets(n int) error、SetFalloff(f FalloffCurve) error、SetLosMode(m string) error、SubmitCast(c CastReq) (CastID, error)、Resolve(ctx context.Context, id CastID, world WorldView) (AoeResult, error)、Explain(id CastID) []TargetTrace、Stats() AoeStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) SetShape 只接受圆形、扇形与矩形；半径必须为正，扇形张角必须大于零且不超过三百六十度，矩形长宽也必须为正；不满足返回 ErrBadShape，当前形状不动。
2) SetMaxTargets 只接受正数，否则 ErrBadTargets；SetFalloff 只接受 none 与 linear，linear 的保底千分比必须在零到一千之间，否则 ErrBadFalloff；SetLosMode 只接受 strict 与 penetrable_pass，否则 ErrBadLosMode。
3) SubmitCast 的施法者编号不能为空白、威力不得为负，否则 ErrBadCast 且不占序号；成功提交按从一开始的单调序号分配 CastID，并冻结当时的形状、原点、朝向、威力、目标上限、衰减与遮挡模式。
4) 圆形按原点与实体中心的距离不超过半径判定；矩形以原点为起点、朝向为前向，前向投影在零到长度之间且横向偏移不超过宽度一半才算命中；扇形以朝向为中心线，落入张角且距离不超过半径才算命中。
5) 命中集合按距离升序、同距离按实体编号升序稳定排序后按目标上限截断；被截掉的数量计入 Truncated，且不得出现在 Hits 里。
6) 衰减 none 时每名命中目标千分比恒为一千；linear 时在保底值与一千之间按距离线性插值并向下取整，伤害等于威力乘千分比再整除一千。
7) 遮挡按原点与目标中心的线段判定，压在线段上的矩形遮挡物按当前模式处理；被拦下的目标不进 Hits，轨迹里留一条 blocked_los 记录。
8) 同一 CastID 重复 Resolve 必须返回逐字段相同的结果，第二次起直接复用首次结算；实体移动、消失或新增一律忽略，只有首次结算计入已结算计数。
9) 取消的上下文在尚未结算的施法上返回取消错误且不计入结算；未知 CastID 返回 ErrUnknownCast，轨迹为空。
10) 候选实体数、遮挡射线数与在途施法数分别受 MaxCandidates、MaxRays、MaxActiveCasts 限制；前两者超限按距离优先保留最近的，在途施法超限时提交返回 ErrCastLimit。
11) Explain 按距离与编号顺序给出每个候选的命中与否与原因码；Stats 汇总提交数、结算数、候选数、命中数、伤害总量与仇恨总量。
12) 快照保存冻结的施法、已结算结果与计数；恢复后同一 CastID 再解析仍返回同一结果，新提交序号继续递增；载荷损坏返回 ErrBadSnapshot 且不动现有状态。

硬约束：Resolve、SubmitCast、Stats 与快照会被多个协程并发调用，读到的只能是完整旧状态或完整新状态；同一 CastID 的首次结算只能发生一次；集合遍历不得影响排序，同一输入无论结算多少次都逐字段一致。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-16`
- 实现文件：`aoe_target_resolver.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AoeTargetResolver`
