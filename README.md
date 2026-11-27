# WebDAV 锁令牌与条件请求治理

## 题面

办公网盘切到新的 WebDAV 网关后，前台的投诉基本都围绕锁：有人给整个目录加了深度无穷的写锁，同事在子目录里照样能新建文件；同一份文档在客户端缓存了旧令牌，删除请求明明带着锁却报资源被占用；还有一批同步脚本把令牌拼错，网关直接回四百，客户端把整批同步任务都置成失败。请把 gsb/batch51-go-safety-b-18 的 webdav_lock_guard.go 里 WebdavLockGuard 补好，对外签名保持：NewWebdavLockGuard(cfg LockPolicy) *WebdavLockGuard、SetClock(now time.Time)、Lock(path string, req LockRequest) (LockToken, error)、Refresh(token string, timeout time.Duration) (LockToken, error)、Unlock(path, token string) error、CheckIf(path string, conditions []IfCondition) (IfResult, error)、Move(src, dst, token string) error、LocksOn(path string) []LockToken、LockOf(token string) (LockToken, bool)、Tag(path, etag string) error、Expire(now time.Time) int、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）路径必须是绝对且干净的 dav 路径，空串与相对路径报 ErrBadPath；深度只认 0 与 infinity，其他值报 ErrBadDepth；作用域只认 exclusive 与 shared，其他值报 ErrBadScope；超时非正报 ErrBadTimeout。

（2）授予成功返回 opaquelocktoken 前缀的令牌，带出路径、持有者、深度、作用域与到期时刻（当前时钟加超时），每次授予推进修订号；同一路径要能通过 LocksOn 按授予先后读回。

（3）独占锁覆盖的范围内不允许第二把锁：深度无穷的锁覆盖整棵子树，子资源上再申请一律 ErrLockConflict 且不落任何记录。

（4）共享锁可以并存，共享锁存在时再申请独占锁报 ErrLockConflict；被拒的申请不得改动锁表。

（5）Refresh 把到期时刻改成当前时钟加新超时并推进修订号，超过 MaxTimeout 按 MaxTimeout 截断；令牌不存在报 ErrLockNotFound，已经过期报 ErrLockExpired。

（6）Unlock 成功后令牌与锁表记录一并消失，再次解锁报 ErrLockNotFound。

（7）CheckIf 支持令牌与 ETag 组合：条件全部为真且引用的令牌确实覆盖该路径时返回 OK 与状态 0；ETag 与 Tag 登记的值不符返回 412 且 reason 为 etag_mismatch；不带任何条件访问被独占锁覆盖的资源返回 423 且 reason 为 locked。

（8）Expire 推进时钟并回收过期锁，返回回收条数；过期锁不得再出现在 LocksOn 里。快照要带令牌、路径、深度、作用域与到期时刻，恢复后仍能按原令牌读到并按原时刻过期。

（9）深度 0 的锁只保护资源本身，子资源上的新建不能被父锁自动覆盖，但删除父资源必须被父锁阻塞。

（10）同一令牌在多个路径上被引用时只算一把锁，Unlock 必须带正确路径，且不能顺手把子路径上另一把独立锁一起释放。

（11）If 头里出现未知令牌时按条件为假处理，既不能直接报四百，也不能把这条条件忽略掉。

（12）Move 把随资源搬动的锁令牌改写到目标路径，深度保持不变；目标已被另一把独占锁覆盖时按冲突报错且源端锁表不动。

硬约束两条。其一是并发与原子性：同一路径的并发独占申请只能有一个成功，其余报 ErrLockConflict；锁表变更与资源搬动在同一临界区完成，不能出现先检查后解锁的窗口；过期锁惰性回收，但正在被 If 求值引用的锁不得在求值中途被回收。其二是容量与恢复：锁表条数受 MaxLocks 硬顶，满了报 ErrLimitReached 且不淘汰已有锁；快照必须带出令牌、深度、作用域、到期时刻与登记过的 ETag，损坏快照报 ErrBadSnapshot 且不改动现网。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-18`
- 实现文件：`webdav_lock_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`WebdavLockGuard`、`LockRequest`、`LockToken`、`IfCondition`、`IfResult`、`LockPolicy`
