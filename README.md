# UI 图集合批与裁剪断批

## 题面

换皮之后主界面的按钮时不时压到弹窗上面，背包格子的高光整块糊掉；同一张图集的图标本来该一批画完，实际被拆成十几批，跑分掉了一半。最怪的是把一段聊天文本从第一页字体换到第二页之后，前半句跑到了后半句下面，客服截图里整段话的语序都是乱的。

落点在 gsb/batch51-go-game-b-17 的 ui_batch_builder.go，类型 UiBatchBuilder，包名 task，模块路径 gsb/batch51-go-game-b-17。

公开接口保持：NewUiBatchBuilder() *UiBatchBuilder、Reset(frame int)、SetCanvasSize(w, h int) error、SetBudget(budget UiBudget) error、PushClip(rect Rect, token uint32) error、PopClip(token uint32) error、AddQuad(q UiQuad) error、AddTextRun(r TextRun) error、Build() ([]UiBatch, error)、BatchCount() int、Stats() UiStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) Reset 清空本帧的图元、文本 run、裁剪栈与批次计数，帧号取入参，批次编号从零重新递增。
2) 画布边长必须为正，否则报 ErrBadCanvas，默认一千九百二十乘一千零八十。
3) 裁剪矩形宽高必须为正且整块落在画布内，令牌为零或与已入栈令牌重复报 ErrBadClipToken，矩形本身不合法报 ErrBadClip，裁剪栈深超过十六报 ErrClipDepth。
4) PopClip 在空栈上报 ErrClipUnderflow，令牌与栈顶不符报 ErrClipMismatch；配对出栈后 ClipDepth 立即回落。
5) 图元与文本 run 的顶点数、图层、图集编号逐条校验，非法值报 ErrBadQuad 或 ErrBadText；空 run、含零顶点字形或页号为负的 run 同样拒绝。
6) 合批键由合批类型、图集、材质、采样状态、图层与当前裁剪状态共同决定，键相同且相邻的图元并进同一批；同图集但采样状态不同的图元必须断批。
7) 批次顺序严格等于图元加入顺序，任何排序都不得把后加入的图元挪到前面；批内 Items 与 Verts 分别是成员数与顶点数之和。
8) 文本 run 按字形逐个入批，同一页的字形并进同一批，批与批之间以及批内的字形顺序都与 run 内的字符顺序一致；批的 Page 取该批第一个字形的页号，Kind 为 text，与图元批次分开统计。
9) 每帧批次数超过 MaxBatches 或顶点数超过 MaxVerts，Build 报 ErrBatchBudget 并且不返回半份批次；预算不能靠把后画的图元提前到前层来凑，MaxBatches 或 MaxVerts 不为正时 SetBudget 直接报 ErrBadBudget。
10) Build 时裁剪栈还有残留报 ErrClipUnbalanced；构建失败时待处理图元与裁剪栈保持不动，改正之后可以再构建；成功一次构建之后待处理图元清空，批次编号接着上次的数继续加。
11) 快照包含裁剪栈、画布、预算、帧号与批次编号水位，载荷损坏报 ErrBadSnapshot 且状态不变；恢复后编号与栈深都要接得上。

硬约束：AddQuad、AddTextRun、PushClip、PopClip、Build 会被渲染线程与交互线程并发调用，构建期间不得看到半帧状态，一次构建要么整份成功要么整份不产出；单帧待处理图元数受硬顶约束，超出只能整帧拒绝，不得静默丢掉尾部；快照与构建互斥，恢复只接受完整载荷，恢复失败的实例必须还能继续构建。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发构建与快照恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-17`
- 实现文件：`ui_batch_builder.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`UiBatchBuilder`
