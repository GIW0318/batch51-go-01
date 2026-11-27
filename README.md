# 包安装钩子的解码与能力组合评分

## 题面

内网私服上某个常用包的版本被人重新发布过，装完之后机器上多了一个没人认识的常驻进程。翻包清单才发现，安装后的钩子脚本是一段 base64，解开后又是一层压缩体，值班同学当初只 grep 了几个关键字就放行了。现在要把这套判定补到 `gsb/batch51-go-safety-c-34` 的 `package_install_hook_analyzer.go`，类型名 `PackageInstallHookAnalyzer`。

输入是包清单：名称、版本、文件表（路径到正文）、声明的钩子（阶段、脚本、目标平台、正文是否编码）。入口签名保持：`NewPackageInstallHookAnalyzer(budget Budget) *PackageInstallHookAnalyzer`、`Extract(m Manifest) ([]Hook, error)`、`Score(m Manifest, hooks []Hook) (Assessment, error)`、`Explain(assessment Assessment) string`、`DecodeLayers(blob string, budget Budget) (string, int, bool, error)`、`DetectCapabilities(source, text string) []Evidence`、`FollowScripts(m Manifest, entry string, maxDepth int) ([]string, error)`、`ResolvePath(base, ref string) (string, bool)`、`CombineScore(evidence []Evidence) int`、`SortedEvidence(evidence []Evidence) []Evidence`、`Stages(hooks []Hook) []string`、`MergeEvidence(left, right []Evidence) []Evidence`。

要落地的十一条：

1. 只认 `preinstall` 与 `postinstall` 两个阶段，其他阶段报 `ErrBadStage`；脚本路径不在包文件表里报 `ErrMissingScript`。
2. 解码按层推进，每层解一次 base64；层数达到 `MaxDepth` 或展开后正文超过 `MaxBytes` 时停止继续解，并把截断标记置真。
3. 空载荷报 `ErrEmptyBlob`，不要让空串走进评分。
4. 能力分五类：联网下载、进程执行、凭据读取、环境改写、文件写入；同一文件里同一能力只记一条证据，证据要带上命中的那一行原文，方便人工复核。
5. 评分按能力组合而非单个关键字：联网下载 25、进程执行 20、凭据读取 30、环境改写 10、文件写入 5；联网与凭据同现加 40，联网与进程执行同现加 30，凭据与环境改写同现加 20；总分封顶 100。
6. 分数到 80 判恶意，到 25 判可疑，其余判良性；正文解不开时判不可判定，不能因为没看清就按零风险放过。
7. 钩子调用同包内的辅助脚本时要递归跟进，但不能跨出包目录，引到包外的路径直接忽略，不要跟着解析；跟进深度同样受预算约束，超过深度就停下，遇到互相引用的脚本不能绕圈。
8. 脚本只在特定平台分支里执行，也要把这个分支算进能力，不能被平台判断短路掉。
9. 引用的相对路径按所在文件解析，`./` 与 `../` 归一到包内路径；同一个脚本被多个钩子引用时只解析一遍，不要重复计数。
10. `Explain` 首行固定为 `score=<分> level=<级别> verdict=<结论>`，之后每条证据占一行。
11. 证据列表按能力名、来源文件排序，并去掉完全重复的条目；合并两个证据列表时不得就地改动任一侧的切片。

硬约束：解码层数与字节预算都是硬上限，超限就停止并在结果里标注，不许把整段载荷一次性摊开；同一份清单重复评分，分数、级别、结论与证据顺序必须完全一致，重复调用不得让证据越攒越多；证据的来源路径只允许落在包目录内，任何指向包外的引用都不进结果。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；多层载荷与递归跟随自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-34`
- 实现文件：`package_install_hook_analyzer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PackageInstallHookAnalyzer`
