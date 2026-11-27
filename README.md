# 交换分区加密治理：每启动密钥、换出页代次标记与恢复后的旧密钥失效

## 题面

凌晨三点，一台机器从休眠恢复之后，交换分区上的旧页还能被读出来，内核日志里却成片报同一批页解密失败；同一天另一台机器上，运维做交换密钥轮换时把在途的换出页弄丢了，几个常驻进程当场被杀。事后核对还发现，某个卷上挂了两个交换区，一个区轮换的时候另一个区的排队页被一起清空，值班同学只能手工重建整个卷。

落点在 `gsb/batch51-go-safety-d-07` 的 `swap_encryption_guard.go`，类型 `SwapEncryptionGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-d-07`。

公开接口保持：`NewSwapEncryptionGuard(cfg GuardConfig) *SwapEncryptionGuard`、`Enable(device string, req SwapRequest) (SwapVolume, error)`、`Suspend(volumeID string) error`、`Resume(volumeID string) error`、`Rotate(volumeID string) (KeyEpoch, error)`、`Verify(volumeID string) (VolumeState, error)`、`SwapOut(volumeID string, pages int) (PageMark, error)`、`SwapIn(mark PageMark) error`、`FlushInFlight(volumeID string) (int, error)`、`AttachArea(volumeID, area string) error`、`AreaStats(volumeID, area string) (int, error)`、`EpochKey(volumeID string, epoch uint64) (string, error)`、`Volumes() []SwapVolume`、`Reboot(bootID string) []EpochEvent`、`Events() []EpochEvent`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. 设备或交换区名为空白时报 `ErrBlankDevice`、`ErrBlankArea`，同一设备重复启用报 `ErrDeviceBusy`，被拒的请求不许占用卷号。
2. `SwapOut` 的页数小于等于零报 `ErrBadPageCount`，未知卷报 `ErrUnknownVolume`，挂起中的卷报 `ErrVolumeSuspended`，被拒时页数计数一点都不能动。
3. `SwapIn` 带上不属于该卷的标记报 `ErrBadPageMark`，卷里从未出现过的代次同样报 `ErrBadPageMark`，不能把来路不明的页算进读计数。
4. `Rotate` 生成新一代并把上一代封存，返回的代次带卷号、状态与创建时间；在途页排空之前再调 `Rotate` 报 `ErrRotationInFlight`。
5. `FlushInFlight` 返回本次排空的页数并清掉在途计数，已经记在卷上的页数不因排空而减少，重复排空返回零。
6. `Suspend` 先把在途页排空再停写，重复挂起报 `ErrVolumeSuspended`；`Resume` 让卷恢复可写，对未挂起的卷报 `ErrVolumeNotSuspended`。
7. `Verify` 要能读出当前代次、封存代次列表、总页数、在途页数、读写批次数与明文写页数，明文写页数必须恒为零。
8. `EpochKey` 对同一卷同一代次稳定返回同一个十六进制串，新一代必须换钥，未知卷报 `ErrUnknownVolume`。
9. `AttachArea` 重复挂同一个区报 `ErrAreaExists`，`AreaStats` 按区读出各自页数，未知区报 `ErrUnknownArea`。
10. 启动标识变化之后，上一启动留下代次的密钥视为失效，残留页不能再凭旧代次解密，`Reboot` 要把这次失效记进代次事件。
11. 轮换期间新换出的页必须记到新代次上，上一代的只读路径不接受新的写页，写页的代次标签要和当前代次对得上。
12. 一个卷挂多个交换区时按区独立记账，某个区的代次轮换不能把另一个区还在途的页一起清掉。

硬约束：同一卷的并发轮换只能有一路成功，其余当场报 `ErrRotationInFlight`；挂起与恢复要保证在途页既不丢失也不被明文写出；快照只保存代次、页计数与启动标识，任何代次的密钥材料都不许出现在快照里，恢复后卷号只增不减，损坏快照报 `ErrBadSnapshot` 且不动现有状态。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发轮换与重启恢复自行构造程序验证；只做本地实现。交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-d-07`
- 实现文件：`swap_encryption_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SwapEncryptionGuard`
