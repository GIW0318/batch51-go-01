# 无障碍配置的三层合并与版本迁移

## 题面

客服转来一组无障碍投诉：某玩家在手机上把字幕调到最大，平板上的高对比度却跟着丢了；另一位把减少动态效果打开，屏幕震动自动关掉，等他把减少动态关掉，震动还是关着，只能手动再开一次；还有人从内测版本升级上来，设置页里本来有的触感强度滑块一保存就变回默认，客服查了半天说不清是谁覆盖的。三层配置合并这套逻辑要做完，模块路径 gsb/batch51-go-game-a-09，实现文件 accessibility_profile_guard.go，类型 AccessibilityProfile。

对外签名保持：NewAccessibilityProfile(defaults Profile) *AccessibilityProfile、Defaults() Profile、Apply(accountID string, p Profile) error、Override(deviceID string, fields map[string]Value) error、ClearOverride(deviceID, field string) error、Resolve(accountID, deviceID string) (EffectiveProfile, error)、Migrate(raw []byte, fromVersion int) (Profile, error)、Save(accountID string) ([]byte, error)、Reset(accountID string) error、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）字段名只认 subtitle_size、high_contrast、reduce_motion、screen_shake、color_blind_mode、hud_scale、hold_to_confirm 这七个，其余报 ErrUnknownField；值分 bool、int、str 三类，Kind 不认识报 ErrBadValue。

（2）账号号去空白后为空报 ErrEmptyAccount；Apply 传入的档位版本必须等于 CurrentVersion，否则报 ErrUnsupportedVersion，被拒的档不得改动已有预设。

（3）Override 是增量合并，只写入传入字段，空表报 ErrBadValue，设备号去空白后为空报 ErrBadValue；ClearOverride 只删指定字段，未知字段报 ErrUnknownField。

（4）Resolve 对每个字段按设备覆盖、账号预设、全局默认取第一个显式值，Sources 记录来源 default、account、device；某一层缺失不报错。

（5）subtitle_size 只接受 100 到 250，hud_scale 只接受 50 到 200，超出报 ErrOutOfRange。

（6）两条互斥关系固化为规则：reduce_motion 为真时 screen_shake 强制为假；color_blind_mode 不为 none 时 high_contrast 强制为假。被强制关闭的字段按字段名字典序列入 EffectiveProfile.Conflict；强制只作用在合并结果上，不得改写存储层里的显式值。

（7）互斥项被强制关闭后，用户把依赖项改回原值时必须恢复先前的显式设置，不能永久停在关闭状态。

（8）迁移按版本链逐级转换：v1 的 text_size 更名为 subtitle_size，contrast 更名为 high_contrast；v2 的 motion 取反写入 reduce_motion；迁移结果 Version 一律为 CurrentVersion。

（9）负载不是合法 JSON 报 ErrBadPayload；fromVersion 小于 1 报 ErrUnsupportedVersion；高于当前版本的负载按未来版本处理，其中已知字段照常解析。

（10）未来版本才有的字段必须原样保留并标记为只读，Save 写回时不得清空，也不得因只读字段而丢掉整份配置。

（11）整档切换必须原子生效；Snapshot 要带出默认层、账号预设、设备覆盖与只读标记，恢复后 Resolve 结果与恢复前一致，空快照或结构不合法报 ErrBadSnapshot 且不得改动现网状态。

（12）Reset 删除账号预设且幂等，删除后该账号回到默认层。

硬约束两类。其一并发与原子性：同一账号的 Apply 与 Resolve 并发时只能看到整档旧值或整档新值，设备覆盖合并与快照互斥，快照不得读到半套字段。其二快照恢复与重启：默认层、账号预设、设备覆盖与只读标记必须一起持久化，重启后 Resolve 的每个字段与来源都要与停机前一致，恢复失败必须整份回退。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-09`
- 实现文件：`accessibility_profile_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AccessibilityProfile`
