# cgroup 子树冻结的层级聚合与超时回滚

## 题面

批量离线作业上线租户隔离的冻结功能后，值班同学发现同一棵 cgroup 子树在两个窗口里给出两种答案：一处报冻结完成，实际还有几个进程在跑；另一处报整棵失败，可兄弟层明明已经停住了。更麻烦的是解冻脚本，重试一次，本该停住的层反而又冻上了。落点在 gsb/batch51-go-safety-c-12 的 cgroup_freezer_coordinator.go，类型 FreezerCoordinator 负责对一棵 cgroup 子树做冻结与解冻，并把逐层结果如实汇总成完全冻结、部分冻结与超时三种结论。置位自上而下，确认自下而上，父层没有完全冻结就不许宣称成功。这套逻辑只在发布窗口里用，窗口很窄，事后只能靠审计快照复盘，所以每次调用的结论必须能自解释，说清哪几层真的停了、哪几层还悬着。

签名保持：NewFreezerCoordinator(cfg FreezerConfig) *FreezerCoordinator；AddCgroup(path, parent string, processes int) error；RemoveCgroup(path string) error；Freeze(root string) (FreezeResult, error)；Thaw(root string) (ThawResult, error)；Status(path string) (Node, error)；Tree(root string) []Node；MarkStuck(path string) error；Wake(path string) error；Covered(root string) []string；Snapshot() ([]byte, error)；Restore(data []byte) error。

要处理的十一处：① 路径为空白或带 `..` 仍能登记，应报 ErrInvalidPath；② 同名重复登记顶掉旧节点，应报 ErrDuplicateCgroup，父路径不存在仍能挂上去，应报 ErrUnknownCgroup；③ 节点数超过 MaxCgroups 仍放行，应报 ErrCgroupLimit，层级超过 MaxDepth 仍放行，应报 ErrDepthLimit；④ 还有子层的节点被当成叶子删掉，应报 ErrHasChildren 且子树原样保留；⑤ 冻结时先确认后置位、父子顺序颠倒，必须自上而下置位、自下而上确认；⑥ 父层还停在 freezing 就报完全冻结，只有整棵子树每一层都确认才算 Outcome frozen；⑦ 已完全冻结的子树再冻一次重新置位或多耗轮次，应幂等返回 frozen 且不再走满轮次；⑧ 整棵子树没有任何一层确认冻结时仍报部分成功，应按 Outcome timeout 处理，把已置位的层按层级反序回滚并写进 RolledBack；⑨ 解冻按冻结顺序正序执行，应严格反序（自下而上），Thawed 按实际解冻次序排列；⑩ 对本来就没冻结的子树解冻返回成功，应报 ErrNotFrozen 且不改任何状态；⑪ 快照恢复丢掉父子关系、冻结状态与轮次计数，必须完整恢复，损坏快照报 ErrBadSnapshot 且不动现网。

两条硬约束：同一子树的 Freeze 与 Thaw 必须串行化，任何调用都看不到一半置位一半回滚的中间态，并发进入时后到者要么看到全冻结要么看到全解冻；快照只能呈现全量旧状态或全量新状态，恢复失败原样保留现网，节点数、层级深度与确认轮次都是硬上限，超限报错不允许截断继续。

另有三处按现场口径处理：某个进程处在不可中断睡眠导致本层长期待定时，不能因为这一层就把整棵子树判为失败并放弃已经冻结的兄弟层，结论要落在部分冻结上并列出待定层；冻结过程中并发创建的子 cgroup 要么一并纳入本轮，要么在结果的 Uncovered 里明确列出未覆盖范围；解冻超时后重试不得把已经解冻的层再次置为冻结。

验收：go test ./... 的 15 条用例必须全过；测试不得修改、跳过或按固定输入返回常量；并发冻结与重启恢复自行写程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-12`
- 实现文件：`cgroup_freezer_coordinator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过 `gsb/batch51-go-safety-c-12` 导入本模块
- 主要类型：`FreezerCoordinator`，节点类型 `Node`，结果类型 `FreezeResult` 与 `ThawResult`
