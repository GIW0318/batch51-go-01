# 扩展属性命名空间治理：security 与 trusted 权限边界、ACL 存储与跨文件系统复制语义

## 题面

运维在给容器镜像打安全标签时踩了两个坑：一个镜像层复制到备份盘之后 `getfattr -d` 显示扩展属性少了一半，另一个镜像往符号链接上写属性，写到了链接指向的目录，回滚脚本按链接本身的属性判断，直接把线上配置改坏。复盘时还发现，给文件加了访问控制列表之后 `ls -l` 显示的权限位和 ACL 对不上，卸载授权和真实权限出现了两套说法。

落点在 `gsb/batch51-go-safety-d-08` 的 `xattr_namespace_guard.go`，类型 `XattrNamespaceGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-d-08`。

公开接口保持：`NewXattrNamespaceGuard(cfg GuardConfig) *XattrNamespaceGuard`、`Mount(path string, opts FSOptions) error`、`Create(path string, mode uint32, owner int) error`、`Set(path string, attr Attribute, cred Cred) (AttrState, error)`、`Get(path, name string, cred Cred) (Attribute, error)`、`List(path string, cred Cred) ([]string, error)`、`Remove(path, name string, cred Cred) error`、`Copy(src, dst string, mode CopyMode) ([]AttrChange, error)`、`Chmod(path string, mode uint32, cred Cred) error`、`Capabilities(path string) []string`、`FileVersion(path string) (int, error)`、`Attrs(path string) []Attribute`、`FileStats(path string) (FileStats, error)`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. 路径为空白报 `ErrBlankPath`，未挂载的路径下建文件报 `ErrNoFilesystem`，重复挂载或重复建文件报 `ErrPathExists`，模式超过 07777 报 `ErrBadMode`。
2. 属性名没有 user、trusted、security 或 system.posix_acl 前缀时一律报 `ErrBadNamespace`，字符大小写不对也算没有前缀。
3. user 命名空间按属主判定，非属主写入报 `ErrPermission`；trusted 需要特权，security 只接受受信组件写入，只有普通特权的组件同样报 `ErrPermission`。
4. 单个值超过 `MaxValueBytes` 报 `ErrValueTooLarge`，单文件属性数超过 `MaxAttrsPerFile` 报 `ErrAttrLimit`，被拒的写入一个字节都不许落库。
5. `system.posix_acl_access` 的值必须能解出 user、group、other 与 mask 四段，缺段或权限字符非法报 `ErrBadACLValue`，ACL 生效后文件模式要按 ACL 重算，组位取 mask 段。
6. `Chmod` 改模式时同步改写 ACL 的 mask 段，模式与 ACL 必须落在同一个版本号上，不许出现模式变了 ACL 没变的中间态。
7. `Get` 未知属性报 `ErrAttrNotFound`、未知路径报 `ErrUnknownPath`；`Remove` 遵循同样的规则，非属主删除 user 属性报 `ErrPermission`。
8. `List` 只列出调用方有权看到的命名空间：普通调用方看不到 trusted 与 security，特权调用方看得到 trusted，受信组件看得到 security，结果按名字升序。
9. `Copy` 把源文件属性整体抄到目标，返回的每条变更带名字、动作与版本号；目标文件系统只读报 `ErrReadOnlyFS`。
10. 目标文件系统不支持的命名空间整条丢弃，变更动作记 dropped，原因记 unsupported_namespace，不许换个命名空间塞进去。
11. `security.capability` 写入之后要按内容重算能力集，删除之后能力必须回落，不能留着上一份能力继续生效。
12. 同一属性在符号链接上的读写是跟随还是拒绝由 `CopyMode` 的 `FollowLinks` 决定，默认不穿透。

硬约束：同一文件的属性变更原子提交，ACL 与权限位不许出现一个变一个没变的中间态；跨文件系统复制按目标支持集裁剪并逐条留痕；快照保存属性集合与版本号，恢复后新写入的版本必须大于此前任何一条，损坏快照报 `ErrBadSnapshot` 且不动现有状态。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发写同一文件与重启恢复自行构造程序验证；只做本地实现。交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-d-08`
- 实现文件：`xattr_namespace_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`XattrNamespaceGuard`
