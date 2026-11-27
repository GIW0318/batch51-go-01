# IMAP 邮箱同步与 QRESYNC 窗口对齐

## 题面

邮件归档系统上线增量同步以后，法务部的取证邮箱每天多出一批重复邮件，客户端明明只收到一次投递；另有一台笔记本休眠两天再唤醒，同步窗口直接错位，把已经删除的邮件又拉回本地，还漏掉了期间的新邮件。请补齐模块 gsb/batch51-go-safety-b-14 里 imap_mailbox_guard.go 的 ImapMailboxGuard，把 UID 空间、CONDSTORE 修改序号与 QRESYNC 窗口对齐做对。

对外签名保持：NewImapMailboxGuard(limits Limits) *ImapMailboxGuard、Select(user, mailbox string) (MailboxView, error)、Append(user, mailbox string, msg RawMessage) (UID, error)、StoreFlags(user, mailbox string, uids []uint32, modseq uint64) ([]FlagChange, error)、Expunge(user, mailbox string, uids []uint32) ([]uint32, error)、FetchSince(user, mailbox string, modseq uint64) (Delta, error)、Fetch(user, mailbox string, uidValidity uint32, uids []uint32) ([]RawMessage, error)、UIDValidity(user, mailbox string) (uint32, error)、BumpUIDValidity(user, mailbox string) (uint32, error)、Changes(user, mailbox string) []FlagChange、Tombstones(user, mailbox string) []uint32、Snapshot() ([]byte, error)、Restore(data []byte) error。

要处理的故障与边界：

（1）用户或邮箱去空白后为空报 ErrBadOwner；Select 对尚未建立的邮箱按需建立，首个 UIDVALIDITY 为 1，UIDNEXT 为 1。

（2）Append 的正文去空白后为空报 ErrBadMessage；成功时分配 UIDNEXT 并自增，正文与初始标志一并落库，同时追加一条 append 变更。

（3）UID 在邮箱生命周期内单调且绝不复用：EXPUNGE 掉的号码进墓碑集合，后续 APPEND 只往后发，不能回收墓碑上的号。

（4）StoreFlags 把给定 UID 的标志集合整体置为已读，逐条返回变更，Added 与 Removed 列出前后差集，变更按 UID 升序排列。

（5）StoreFlags 携带的 modseq 大于邮箱当前修改序号时报 ErrModSeqAhead，避免客户端用未来的视图覆盖；不存在的 UID 幂等跳过且不推进修改序号。

（6）Expunge 返回实际删除的 UID 升序列表，不存在的 UID 幂等跳过；每次删除写一条 expunge 变更。

（7）同一个 UID 先被 EXPUNGE 又收到针对它的 STORE 时，变更日志要按发生顺序保留 expunge 在前、flags 在后的两条记录，QRESYNC 回放不能把它们折叠成一条。

（8）FetchSince 只返回修改序号大于入参的变更；请求落在保留窗口之内时按增量返回，FullResync 为假。

（9）变更日志按 MaxChangeLog 裁剪，只保留最新的若干条；裁剪之后仍要能回答比最早保留序号更老的请求，用重同步标记加原因说明，不能伪造一段连续区间糊过去。

（10）Fetch 必须带客户端手里的 UIDVALIDITY：与邮箱当前值不一致报 ErrStaleValidity，不能返回空结果让调用方误以为邮箱已经清空。

（11）BumpUIDValidity 让 UIDVALIDITY 加一、清空消息与墓碑、UIDNEXT 归 1，此后所有旧同步窗口一并失效，客户端只能重新 SELECT 再全量拉取。

（12）快照保存 UIDVALIDITY、UIDNEXT、修改序号、消息、墓碑与变更日志；恢复后 APPEND 接着原 UIDNEXT 往后发，增量回放与恢复前一致；损坏的快照报 ErrBadSnapshot 且不改动现网状态。

硬约束有两类。其一是并发与原子性：并发 APPEND 与 EXPUNGE 下 UID 分配唯一，修改序号与变更日志在同一把锁里推进，读方看到的是完整的一份日志。其二是容量与恢复：变更日志条数与邮箱数都是硬上限，超出按最旧淘汰而不是拒绝写入；快照与恢复整份成功或整份失败。

验收：go test ./... 的 15 条用例必须全过，不得修改测试、跳过用例或按固定输入写死结果；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-b-14`
- 实现文件：`imap_mailbox_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ImapMailboxGuard`
