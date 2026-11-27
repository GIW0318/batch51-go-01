# 文件服务导出准入与身份映射

## 题面

文件服务器的 NFS 服务扩容后收到两类投诉：同一台客户端访问 /data/team 时被套用了 /data 的只读选项，另一些机器用 root 进来后写盘报权限错；切到 Kerberos 认证后，整批请求又被直接拒绝。落点 gsb/batch51-go-safety-b-08 的 nfs_export_guard.go，类型 NfsExportGuard，签名保持：NewNfsExportGuard(cfg ExportPolicy) *NfsExportGuard、AddExport(path string, spec ExportSpec) error、Authorize(client netip.Addr, path string) (Export, error)、MapIdentity(export string, cred Credential) (MappedID, error)、Revoke(path string) error、Exports() []Export、Get(path string) (Export, bool)、Stats() Stats、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的故障与边界：

一、路径必须是干净的绝对路径，空串、相对路径、尾部带斜杠、含 .. 段一律报 ErrBadPath；导出表按路径去重，同路径再次添加整体替换规格并把版本加一，已在途的请求按进入时的旧规格走完。

二、客户端条目支持网段、单主机与主机名，非法网段报 ErrBadClient；客户端列表为空报 ErrNoClients，超过 MaxClientsPerExport 报 ErrClientLimit；新增路径超过 MaxExports 报 ErrExportLimit。

三、授权按最长路径前缀匹配，且只在路径段边界上匹配；命中多个导出时只取最长前缀那一个，选项不叠加。请求路径带 .. 段报 ErrPathEscapes，相对路径报 ErrBadPath。

四、路径命中但客户端不在任何网段或主机内报 ErrClientDenied；一条导出都不命中报 ErrNoExport；授权结果要带回该导出的只读标志与版本号，成功与拒绝分别计入 Stats 的 authorized 与 denied。

五、root_squash 把 uid 0 映射成匿名身份，all_squash 对全部用户生效；all_squash 与 no_root_squash、root_squash 与 no_root_squash 同时出现报 ErrConflictingSquash。

六、no_root_squash 必须显式给出白名单，只有白名单内的客户端保留 uid 0，白名单外仍按匿名处理；保留 uid 0 不代表放开写权限，导出上的只读选项照旧生效。

七、Kerberos 导出只接受 krb5 方法，主体必须在该导出的主体映射表里，映射缺失报 ErrIdentityUnmapped，绝不许回落到 AUTH_SYS；映射成功要把映射的 uid 与组列表带回结果。

八、补充组按目录服务结果补齐：GroupSource 可用时，非匿名映射的 gid 集合要并入目录服务给出的组，不能只有主组一份。

九、匿名映射的 uid 与 gid 取导出里配置的值，未配置时用 65534；匿名结果必须带 Anonymous 标记，方法名照实填。

十、身份映射结果按导出路径、版本与身份键缓存，缓存条目数受 MaxIdentityCache 限制，满了按最久未使用淘汰；导出被替换后旧版本的缓存不得再命中。

十一、Exports 按路径升序返回副本，Get 只做精确路径查询；Revoke 删除条目，未知路径报 ErrNoExport。

十二、快照覆盖导出表、版本与计数器，Restore 要么全量生效要么全量不动，坏载荷报 ErrBadSnapshot，失败不得影响现网导出。

硬约束：导出表变更必须原子，授权与在途请求看到的是同一份规格快照，不能一半新一半旧；导出条目、每导出的客户端条目与身份映射缓存都有硬上限，超限只能按第二条与第十条报错或淘汰，不允许静默扩容。

验收：go test ./... 的 15 条用例全部通过，不得修改测试、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现，不接外部依赖。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-08`
- 实现文件：`nfs_export_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`NfsExportGuard`
