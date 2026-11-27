# 拍照模式的暂停冻结、实体隐藏与截图队列

## 题面

拍照模式上线后，美术和客服同时收到一批怪事：玩家按下拍照键后世界时钟还在走，怪物继续巡逻把摆好姿势的角色顶开；调好的镜头一推远就被塞回场景角落，朝向也跟着转了半圈；退出拍照再进来，屏幕下方还挂着一排上次没截完的图，存储配额却显示已满。请在 gsb/batch51-go-game-a-11 的 photo_mode_director.go 里把 PhotoModeDirector 补齐，调用方按下面的签名使用：NewPhotoModeDirector(cfg PhotoConfig) *PhotoModeDirector、Enter(playerID string, at time.Time) (Session, error)、SetCamera(sessionID string, pose CameraPose) error、Hide(sessionID string, kinds []EntityKind) error、Capture(sessionID string, req CaptureRequest) (Ticket, error)、Exit(sessionID string) error、Snapshot() ([]byte, error)、Restore([]byte) error；读取侧还用到 Session、Sessions、Pose、Hidden、Renderables、Pending、StorageUsed、Advance、Lighting、Complete。

需要落实的行为：

1) Enter 的玩家号空白返回 ErrBlankPlayer；同一玩家已经在拍照模式里返回 ErrAlreadyInPhotoMode；同时存在的会话达到 MaxSessions 返回 ErrSessionLimit。会话序号从 1 起按进入顺序递增，会话编号不得为空。
2) 进入后世界时钟冻结在进入时刻：Session 的 WorldTime 等于传入的 at，游戏循环调用 Advance 传任意时长都返回 at 且不改写已存时间。
3) 天气与光照按拍照的真实时间继续：Lighting 用传入的当前时刻减去 EnteredAt，返回的 Elapsed 精确等于该差值，不受世界时钟冻结影响。
4) SetCamera 先处理最小距离：相机与目标重合时沿 +Z 方向后退到恰好 MinDistance，其他情况沿目标指向相机的方向退到 MinDistance；随后位置按 Bounds 逐轴钳制进闭区间。回退只改位置，Yaw 与 Pitch 原样保留。
5) 相机落在 Obstacles 球体内时沿球心指向相机的方向推到球面，与球心重合时推到 +X 方向；球体检查在边界钳制之后进行。
6) Hide 记录要隐藏的实体种类，同一会话重复传入幂等，Hidden 按种类名升序返回去重结果；未知种类返回 ErrBadEntityKind 且不改动已有集合。Renderables 按种类名升序返回未隐藏的种类；CollisionKinds 返回全部种类，隐藏只作用于渲染与截图，战斗与碰撞判定不受影响。
7) Capture 先校验请求，宽、高与字节数任一非正返回 ErrBadCapture；未完成票据数达到 MaxPending 返回 ErrQueueFull，均不得产生票据。
8) 未完成票据的字节合计加上本次请求超过 MaxBytes 时返回 ErrStorageFull；Complete 成功后释放该票占用的字节。票据编号从 1 起单调递增，第 n 张落在 (n-1)/MaxPerFrame+1 帧，失败的调用不占用编号。
9) Pending 按编号升序返回该会话未完成的票据，StorageUsed 返回这些票据的字节合计。
10) Exit 释放会话并取消未完成票据，未知会话返回 ErrUnknownSession；对未知会话的读取与写入一律返回 ErrUnknownSession，退出后捕获与查询也按未知会话处理，配额随票据一起释放，重新进入不得沿用旧队列。
11) 快照包含会话、位姿、隐藏集合、票据与编号计数；Restore 要么整体替换要么完全不动，损坏载荷返回 ErrBadSnapshot。

硬约束：Enter、Capture、Exit、Complete 与快照会被多个协程并发调用，同一玩家只允许一个会话，票据编号不得重复或跳号；快照与状态写入互斥，读到的只能是完整旧状态或完整新状态；队列条数、单帧张数与存储字节都受配置硬顶。

验收要求：go test ./... 15 条用例必须全部通过，测试文件不得修改、不得跳过、不得按固定输入写死；并发进入与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-11`
- 实现文件：`photo_mode_director.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PhotoModeDirector`
