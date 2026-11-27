# 租户密钥轮换与吊销：在途引用、旧钥清理与版本幂等

## 题面

投诉：租户签名密钥的轮换和吊销由同一套后台管，上周撞了两次。一次是运维在控制台点轮换、安全同时点吊销，两边几乎同时落库，结果吊销被后到的轮换覆盖，那把已经确认外泄的旧钥又被标成可用，下游网关照着新状态继续放行。另一次是轮换完就急着清旧钥，一批正在校验的请求拿不到签发时的版本，全量报签名不符，渠道那边直接停了一个小时。还有一个更小的坑：值班同学为了核对版本号，把同一个版本号连着点了两次轮换，后台生成了两条同号记录，引用计数各算各的。

落点在 `gsb/batch51-go-safety-r-06` 的 `key_rotation_revocation_guard.go`，类型 `KeyRotationRevocationGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-r-06`。

公开接口保持：`NewKeyRotationRevocationGuard(tenant string, cfg RotationConfig) (*KeyRotationRevocationGuard, error)`、`Rotate(version string, material []byte, now int64) (KeyVersion, error)`、`Resolve(version string) (KeyVersion, error)`、`Revoke(version, reason string, now int64) error`、`Acquire(version string) (Lease, error)`、`Release(lease Lease) error`、`Retire(version string) error`、`Current() string`、`Versions() []KeyVersion`、`RefCount(version string) int`、`Events() []RotationEvent`、`Stats() RotationStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. 租户号去空白后为空报 `ErrBadTenant`；`MaxVersions`、`MaxAudit`、`MaxInflight` 任一不为正报 `ErrBadConfig`。
2. 版本号去空白后为空报 `ErrBadVersion`，密钥材料为空报 `ErrBadMaterial`；同一个版本号换成另一份材料报 `ErrVersionConflict`，版本表与当前版本都不许变动。
3. 每次成功轮换把新版本置为 `active` 并成为当前版本，上一版转为 `rotating`；`Current` 取当前版本，`Versions` 按轮换先后返回，对外结构里不得出现密钥材料。
4. 版本表上限 `MaxVersions`：已满且没有可回收的旧版本时报 `ErrVersionLimit`，被拒绝的轮换不得改变当前版本与计数。
5. `Revoke` 必须带非空原因，否则报 `ErrBadReason` 且版本状态不变；吊销立即生效，之后的 `Resolve` 与 `Acquire` 都报 `ErrVersionRevoked`；吊销掉当前版本后 `Current` 返回空串。
6. 吊销幂等：同一版本重复吊销返回成功，`Stats` 里的吊销计数只加一次；吊销未知版本报 `ErrUnknownVersion`。
7. 轮换与吊销撞在一起时吊销优先，先落地的轮换如果赶在吊销之前，吊销仍然要把它覆盖掉，轮换的结果不能把已吊销的版本重新激活。
8. `Acquire` 给在途请求登记引用并返回带唯一标识的租约，超过 `MaxInflight` 报 `ErrInflightLimit`；`Release` 递减引用，同一租约重复释放报 `ErrLeaseClosed`，未知租约报 `ErrUnknownLease`。
9. 在途请求持旧版本期间，旧版本必须仍能解析并完成校验，引用归零之后才允许清理旧钥，清理之前不得提前把材料抹掉。
10. `Retire` 只作用于非当前版本，对当前版本报 `ErrCurrentVersion`；重复退役幂等，未知版本报 `ErrUnknownVersion`；引用为零的退役版本再 `Resolve` 报 `ErrVersionRetired`。
11. 同一版本号被重复轮换请求命中时幂等返回原版本，不能生成两个同号密钥，已有引用计数也不能被重置。
12. 快照带上版本状态、引用计数、租约、序号、事件与计数；恢复之后当前版本、各版本引用数、在途租约都要能接着用；损坏快照报 `ErrBadSnapshot` 且不动现网。并发 `Acquire` 与 `Rotate` 同时到达时，在途上限与版本上限都只放行配置条数。

验收：`go test ./...` 15 条全过；测试不得修改、不得跳过、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-r-06`
- 实现文件：`key_rotation_revocation_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`KeyRotationRevocationGuard`、`RotationConfig`、`KeyVersion`、`Lease`、`RotationEvent`、`RotationStats`
