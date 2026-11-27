# 进程内存中动态注册组件的识别

## 题面

应急响应在一台 Web 主机上扫内存马，结果框架自己热加载的过滤器被逐个报成恶意组件，真正以无文件方式注册进去的那个反倒没进清单；扫到一半目标进程被重启，前面已经确认的结论也跟着丢了。现在要把识别逻辑补到 `gsb/batch51-go-safety-c-33` 的 `memory_shell_scanner.go`，类型名 `MemoryShellScanner`。

输入是一份进程快照：内存映射（基址、长度、权限位、读到的字节、重映射次数）、运行期注册项（来源类别、名称、目标链、起始地址）以及仅有定义没有注册的组件名。入口签名保持：`NewMemoryShellScanner(limits Limits) *MemoryShellScanner`、`Scan(snap *ProcessSnapshot) ([]Candidate, error)`、`Verify(snap *ProcessSnapshot, c Candidate) (Candidate, error)`、`Report(candidates []Candidate) string`、`LastCandidates() []Candidate`、`ChunkPlan(region MapRegion) []int`、`ClassifyRegion(perm string) string`、`MatchSignature(data, sig []byte) bool`、`IsHotReload(reg Registration) bool`、`SortCandidates(candidates []Candidate) []Candidate`、`RegisteredNames(regs []Registration) []string`、`ByteCost(regions []MapRegion) int`。

要落地的十二条：

1. 只有带读权限的映射参与扫描，只执行或只写的映射一律跳过，不能凭地址猜内容。
2. 单进程可读字节总量超过 `Limits.MaxBytesPerProcess` 时直接报 `ErrByteBudget`，不产出半份候选。
3. 映射按 `Limits.ChunkBytes` 分块读取，偏移从零起按块长递增，最后一块允许短于块长，偏移一律相对映射基址计算。
4. 快照为空指针报 `ErrNilSnapshot`；没有任何映射和注册项时返回空清单且不报错。
5. 进程已退出的快照报 `ErrTargetExited`，并且上一轮扫出来的候选要能原样取回，不得被清空。
6. 候选必须来自真实存在的注册项；注册项起始地址落不进任何映射时，不产生候选。
7. 框架自身热加载产生的注册要按良性判，运行期新注册的组件按确认判，两者不能一视同仁；判定依据要写进候选的证据条目里。
8. 组件名被随机化时，要按注册链的调用关系命中，不能只靠名字或关键字匹配。
9. 扫描期间映射被重新映射导致读取失败时，先重试一次，仍然读不到再判不确定，不要直接当良性。
10. 只存在定义、从未被注册过的组件不得计入候选。
11. 签名匹配按字节子串比较，空签名不匹配任何数据；确认态的候选必须带证据条目和大于零的评分。
12. 输出按映射基址升序，同基址再按名称升序，重复扫描同一快照顺序必须一致，两次结果要能逐条对上。

硬约束：并发扫描同一目标时，候选清单要么是上一轮完整结果，要么是本轮完整结果，不能出现两条记录混排；进程退出或字节超限导致中止时，已经确认的结论必须留在 `LastCandidates` 里；单进程字节预算是硬顶，超限即拒而不是截断后来一份残缺清单。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；并发扫描与中止后取回结论自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-33`
- 实现文件：`memory_shell_scanner.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`MemoryShellScanner`
