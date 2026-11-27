# 网络绑定磁盘解锁的指纹钉扎与重键

## 题面

机房搬迁之后一批加密卷在启动阶段解不开，值班同事把恢复口令当成常规手段用了整整两周；复盘时才发现解锁客户端连了一台没有登记过的网络绑定服务，拿回来的广告里混进了陌生密钥，客户端照单全收，响应里的卷标识也没有核对，于是把一台机器的卷密钥写到了另一台上。这台机器上周刚做过一次服务端密钥轮换，旧广告还没下线、新广告已经上线，客户端没有区分两者的指纹，直接把两套密钥合成了一份。更麻烦的是这批卷的恢复口令由同一个账号统一保管，一台机器泄露就等于全部暴露，审计要求把绑定关系、指纹来源和重键历史都留成可回放的记录。落点在 `gsb/batch51-go-safety-c-03` 的 `nbde_unlock_guard.go`，类型 `NbdeUnlockGuard`，模块路径 `gsb/batch51-go-safety-c-03`，包名 `task`，需要把绑定点、广告校验与重键窗口这条链路补齐。

公开接口保持：`NewNbdeUnlockGuard(cfg UnlockConfig) *NbdeUnlockGuard`、`AddServer(server string) error`、`PublishAdv(server string, adv Advertisement) error`、`Bind(volume string, adv Advertisement, pin Thumbprint) (Binding, error)`、`Unlock(volume, server string) (VolumeKey, error)`、`Rotate(volume string, req RotateRequest) error`、`FinishRotate(volume string) error`、`VerifyAdv(adv Advertisement) error`、`SetEscrow(volume, key string) error`、`Binding(volume string) (Binding, bool)`、`RevokeBinding(volume string) error`、`FallbackPassword(volume string) (string, error)`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。配置里的服务器列表、`MaxKeys`、`MaxServers`、`MaxBindings` 与 `RotateWindow` 都要生效。

要修的地方：卷名为空或绑定指纹为空都要拒绝，分别报 `ErrBadVolume` 与 `ErrBadThumbprint`；广告没有密钥、广告指纹为空报 `ErrBadAdvertisement`，密钥数超过 `MaxKeys` 报 `ErrAdvKeyLimit`；广告指纹与绑定指纹不一致报 `ErrThumbprintMismatch`；重复绑定同一卷报 `ErrAlreadyBound`，绑定数超过 `MaxBindings` 报 `ErrBindingLimit`；解锁只允许发往配置过的服务器，别的一律报 `ErrServerNotAllowed`；服务器没有可用广告按不可达处理，报 `ErrServerUnreachable`，并回退到恢复口令而不是静默跳过；广告缺少已固定密钥里的任意一个报 `ErrPinnedKeyMissing`，本地没有登记 escrow 时拒绝解锁并报 `ErrNoEscrow`；服务器返回的广告指纹正确但密钥集合新增了未固定密钥时仍要按已固定子集解锁，不能因为多出密钥就拒绝；解锁响应里的卷标识与本地不一致时要拒绝，即使密钥能解开；重键必须写操作人，缺了报 `ErrBadActor`，新指纹与旧指纹相同报 `ErrSameThumbprint`，没有在重键中的卷调结束动作报 `ErrNotRotating`；重键窗口内新旧密钥都能解锁，窗口结束后旧绑定必须彻底失效；撤销绑定之后解锁报 `ErrNotBound`，快照恢复要带回绑定、指纹与 escrow，损坏快照报 `ErrBadSnapshot` 且不动现网。

硬约束：绑定与重键必须原子，同卷并发解锁只允许一个在途，后到的请求要么排队要么直接失败，不能两个请求同时改写同一份绑定；绑定与 escrow 跨重启恢复，恢复后旧指纹不能复活。服务器列表、广告密钥数与重键窗口都按配置封顶，广告密钥数超限要当场拒绝而不是截断。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发解锁与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-03`
- 实现文件：`nbde_unlock_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`NbdeUnlockGuard`
