# 双 GPU 交替帧与分屏的亲和分配、跨卡共享与单卡回退

## 题面

双卡机器上的战斗演示在评审时炸了：帧在两张卡之间来回换，同一帧的两个 pass 被拆到不同卡，画面从中间裂开；跨卡共享的反照率图集时不时读到上一帧的旧内容，地面材质一直闪；副卡热拔以后画面黑掉一半，只能重启客户端。帧率统计还显示副卡的等待队列一直排着，卡却闲着一半。

落点在 gsb/batch51-go-game-b-13 的 multi_gpu_affinity.go，类型 MultiGpuAffinity，包名 task，模块路径 gsb/batch51-go-game-b-13。

公开接口保持：NewMultiGpuAffinity(cfg MGPUConfig) (*MultiGpuAffinity, error)、RegisterAdapter(a AdapterInfo) error、AssignFrame(frame FrameReq) (Assignment, error)、AssignViewports(vps []Viewport) ([]Assignment, error)、Submit(frame int64) error、Complete(frame int64) error、AcquireShared(key string, size Size) (SharedRes, error)、SyncCrossAdapter(tokens []FenceToken) error、DisableAdapter(id string) error、FallbackState() FallbackInfo、Stats() MGPUStats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：

1) 构造校验：MaxAdapters、QueueDepth、MaxSharedBytes、MaxCopyBytes 都为正，Mode 只能是 alternate 或 split，否则返回 ErrBadConfig。
2) RegisterAdapter：空 id 或队列深度为负返回 ErrBadAdapter，重复 id 返回 ErrDuplicateAdapter，超出 MaxAdapters 返回 ErrAdapterLimit；适配器按注册顺序编号。
3) AssignFrame 要求帧号与 pass 数都为正，否则 ErrBadFrame。alternate 模式下按帧号减一对启用适配器个数取模选卡；split 模式下选当前占用最少的一张，占用相同按注册顺序。
4) 每卡占用等于该卡上未完成的分配数，已分配未提交与已提交在飞都算在内；占用达到该卡队列深度时新帧不进队列，返回的 Deferred 为真并带原因、计入 Deferred 计数，腾出空位后重新分配即可成功。
5) Submit 把待提交的帧转为在飞，Complete 释放帧与队列位；两者对未知或已完成的帧返回 ErrUnknownFrame。已提交在飞的帧不得中途换卡，分配变更只对下一帧生效。
6) AssignViewports 在 split 模式下把视口按启用顺序轮转分给各卡，返回顺序与入参一致；空列表返回空切片与 nil 错误。
7) AcquireShared 按 key 去重，字节数等于宽乘高；同 key 不同尺寸返回 ErrSharedMismatch，空 key 或非正尺寸返回 ErrBadShared，累计字节超过 MaxSharedBytes 返回 ErrSharedLimit 且不记账。
8) 跨卡共享资源按围栏序号访问：同一 key 的序号必须严格递增，重复或非正序号返回 ErrFenceOrder；不存在的 key 返回 ErrUnknownShared；一批 token 要么全部生效要么一条都不生效；读到尚未完成的写入返回错误而不是等到超时。
9) 单卡回退：DisableAdapter 把卡置为禁用，禁掉最后一张启用卡返回 ErrNoAdapter 并保持其可用，未知 id 返回 ErrUnknownAdapter，重复禁用幂等。禁用后新工作只落在剩余卡上；FallbackState 的 Active 在启用卡少于注册卡时为真，Disabled 按 id 升序列出。
10) 副卡被禁用时，已分配但未提交的帧要整体迁回主卡，不能只迁一半 pass；已提交在飞的任务留在原卡上跑完而不是丢弃。交替帧模式下历史帧的资源跟随帧所在的卡，不能假设一定在主卡。
11) 每卡队列深度、共享字节与跨卡拷贝带宽都是硬顶，超限的新帧排到下一帧，不得静默丢弃，也不能因此打乱已提交帧的卡归属。
12) 快照保存适配器表、分配与在飞状态、共享台账、围栏水位与回退状态；Restore 要么整体生效要么不改现状，载荷损坏或为空返回 ErrBadSnapshot，恢复后不重复提交已有帧。

硬约束：AssignFrame、Submit、Complete、AcquireShared、SyncCrossAdapter、DisableAdapter、Snapshot 会被渲染与资源两条线程并发调用，分配表必须原子推进，读者只能看到完整旧表或完整新表；每卡队列深度是硬顶；单卡回退期间不得丢帧。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-b-13`
- 实现文件：`multi_gpu_affinity.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`MultiGpuAffinity`
