# 加密 swap 的每启动随机密钥、挂起恢复与休眠前密钥处置

## 题面

机房一批节点在扩容内存后开始丢数据：休眠唤醒的机器上来就报交换区校验错，另有一台因为误换了盘，旧密钥被套到新盘上，整块交换区成了明文。落点在 gsb/batch51-go-safety-c-08 的 swap_key_guard.go，类型 SwapKeyGuard 负责加密交换区的密钥生命周期与设备状态联动，密钥只在内存里生成和保留，从不落盘。启用、挂起、恢复、休眠、禁用这几步是串起来的：每块盘启用时绑定硬件标识生成一把卷密钥，挂起时确认密钥还能用，恢复后继续用同一把，休眠前按设备策略决定丢弃还是保留。

签名保持：NewSwapKeyGuard(cfg SwapPolicy) *SwapKeyGuard；Enable(dev string, policy DevicePolicy) (SwapState, error)；Suspend() error；Resume() error；Hibernate(prepare bool) error；Unlock(dev string) error；Disable(dev string) error；State(dev string) (SwapState, bool)；Devices() []SwapState；KeyEpoch(dev string) (int, error)；Snapshot() ([]byte, error)；Restore(data []byte) error。

要处理的十二处：① 设备名为空白仍能启用，应报 ErrInvalidDevice；② 元数据版本不在支持列表里的策略仍被接受，应报 ErrUnknownVersion；③ 同一设备重复启用会重新生成密钥，应幂等返回当前状态，密钥标识与代数都不变；④ 同一设备换了硬件标识仍被当成同一块盘，应报 ErrDeviceReplaced 且原记录不变；⑤ 在管设备数超过 MaxDevices 仍放行，应报 ErrDeviceLimit；⑥ 挂起时没有校验密钥是否还在有效期内，超期应报 ErrKeyExpired 并把设备标为 expired、清掉密钥标识；⑦ 挂起与恢复会换掉密钥，恢复必须继续用同一把；⑧ 还有设备处于 active 时也能休眠，应报 ErrBusy 且不动任何设备；⑨ 休眠丢弃密钥后恢复不重新生成，应换代生成新密钥并把代数加一；⑩ 休眠保留密钥后恢复直接可用，应先报 ErrLocked，经 Unlock 回到 suspended 再 Resume；⑪ 快照把密钥带出去，恢复后设备直接可用，快照必须不含密钥，恢复后设备为 locked，重新 Enable 才换代可用；⑫ 禁用不存在的设备返回成功，重复禁用也成功，应统一报 ErrUnknownDevice。

两条硬约束：状态迁移与设备操作串行，同一设备的启用、挂起、休眠不能交错观察到中间态，设备配额在并发启用时只放行到上限；密钥只在内存，快照与重启后不得残留可用密钥，同时在管设备数、密钥驻留时长与元数据副本数都由配置硬顶。另有三处按现场口径处理：休眠唤醒后若密钥被丢弃，旧交换区内容必须整体作废而不是部分可读；同一设备并发启用只能生成一把密钥，后到的返回同一状态；挂起失败回滚后设备必须仍在可用状态，不能停在半启用。

验收：go test ./... 的 15 条用例必须全过；测试不得修改、跳过或按固定输入返回常量；并发启用与重启恢复自行写程序验证；只做本地实现。交付源码、测试输出与一页以内的状态迁移说明，写清每种状态之间的合法迁移与需要人工介入的分支，让值班脚本能按状态机直接驱动而不必猜当前停在哪一步。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-08`
- 实现文件：`swap_key_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SwapKeyGuard`，设备状态是 `SwapState`，策略是 `DevicePolicy`
