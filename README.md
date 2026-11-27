# 输入按键重绑定与前缀冲突解析

## 题面

版本更新后键位页面被投诉了三次：把蹲伏绑到 ctrl 的玩家发现 ctrl+s 的快速存档没了；手柄插拔一次，原先绑在手柄上的动作跑到别的槽位，玩家怎么按都不对；还有玩家在暂停菜单里改了跳跃键，关掉菜单后改动一起消失。默认层、用户层、场景覆盖层这套解析逻辑要做完，模块路径 gsb/batch51-go-game-a-10，实现文件 input_binding_resolver.go，类型 BindingResolver。

对外签名保持：NewBindingResolver(defaults []Binding) *BindingResolver、Bind(scope Scope, action string, chord Chord) error、BindFor(device DeviceID, scope Scope, action string, chord Chord) error、Unbind(scope Scope, action string) error、UnbindFor(device DeviceID, scope Scope, action string) error、Resolve(device DeviceID, input InputEvent) (string, bool, error)、Validate(scope Scope) []Conflict、Commit(scope Scope) error、PushScene(sceneID string) error、PopScene(sceneID string) error、Bindings(scope Scope) []Binding、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的具体故障与边界：

（1）动作号去空白后为空报 ErrEmptyAction；组合键为空报 ErrEmptyChord；同一组合里出现重复键报 ErrBadChord；层名只认 default、user、scene，其余报 ErrBadScope。

（2）f10、f11、f12 是系统保留键，任何层都不许绑定，Bind 与 BindFor 立即报 ErrReservedKey，且不得进入待提交表。

（3）Bind、BindFor、Unbind、UnbindFor 只改本层待提交表，Commit 才生效；未提交的改动对 Resolve 不可见，Commit 成功后整层替换，待提交表清空。

（4）同一层同一组合键被两个动作占用属于冲突，Validate 返回 Kind 为 duplicate_chord 的条目，Action 与 Other 按字典序给出，Chord 为按键用加号连接；存在冲突时 Commit 报 ErrConflict 并丢掉本层待提交表，已生效绑定不受影响。

（5）把动作绑到某组合键的前缀键时必须给出 Kind 为 prefix_shadow 的提示，说明该组合会被阻塞；没有拆成互斥上下文就提交的按冲突处理。

（6）解析先按动作去重：同名动作在更高层有绑定则低层绑定完全遮蔽；同层内带设备号的绑定遮蔽同层不带设备号的绑定。

（7）再按最长前缀匹配：输入按键序列必须以绑定组合为前缀才算命中，取命中最长的绑定；长度相同按层序 scene 高于 user 高于 default，再相同按设备号特定优先，最后按动作名字典序。单键绑定不得吞掉更长组合。

（8）设备号为空按键盘处理；不带设备号的绑定对所有设备生效；手柄绑定按设备序列号存储，断连重连都不得改动，也不得迁移到默认槽位。

（9）场景层只在 PushScene 之后可用，没有活动场景时对 scene 层下单报 ErrNoActiveScene；已有活动场景再 Push 报 ErrSceneActive；PopScene 的 sceneID 与当前场景不符报 ErrUnknownScene，退出后本层全部绑定丢弃。

（10）场景覆盖层退出只撤销本层新增的绑定，用户层在此期间做的改动必须保留。

（11）每层生效绑定最多 MaxBindingsPerScope 条，超出报 ErrScopeLimit，但替换本层已有动作不占新名额。

（12）快照带出默认层、用户层、场景层、各层待提交表、设备绑定与当前活动场景；恢复后 Resolve 与 Bindings 与快照前一致，空快照或结构不合法报 ErrBadSnapshot 且不得改动现网状态。

硬约束两类。其一并发与原子性：Commit 与 Resolve 并发时只能看到提交前或提交后的整层绑定，快照不得读到半次提交。其二幂等与顺序：同一动作重复 Bind 以最后一次为准，Unbind 不存在的动作幂等成功，重复 Commit 没有改动时必须成功且不改变生效表。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重连场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-10`
- 实现文件：`input_binding_resolver.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`BindingResolver`
