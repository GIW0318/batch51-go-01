# 文件上传绕过的变体构造与落点可执行性判定

## 题面

头像上传接口上线后没人管，直到运维在访问日志里看到一条 `/uploads/avatar.png` 的请求打到了脚本解析器上，返回的是脚本执行结果。安全同学复测时又发现两件事：把请求里的类型声明写成图片、内容其实是脚本，服务端照样收下；反过来只把扩展名改掉、内容照旧，反而被拦了，说明服务端按内容嗅探而不是按扩展名判类型。还有人把"上传成功、返回一个静态下载地址"也写进了报告，被评审判成误报。

落点在 `gsb/batch51-go-safety-c-38` 的 `upload_bypass_prober.go`，类型 `UploadBypassProber`，包名 `task`，模块路径 `gsb/batch51-go-safety-c-38`。

公开接口保持：`NewUploadBypassProber(cfg ProberConfig) *UploadBypassProber`、`SetTarget(t Target) error`、`Enumerate(form UploadForm) ([]UploadPoint, error)`、`Variants(point UploadPoint) ([]UploadVariant, error)`、`Submit(point UploadPoint, variant UploadVariant) (UploadResult, error)`、`Verify(result UploadResult) (ExecProof, error)`、`Judge(proof ExecProof) (UploadVerdict, error)`、`Cleanup() (int, error)`、`Uploaded() []UploadResult`、`Budget() (BudgetState, error)`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：

1. `Enumerate` 按表单顺序挑出文件域，`Accept` 按逗号切开并去掉首尾空白，`MaxBytes` 原样带出；表单动作为空报 `ErrBadForm`，一个文件域都没有报 `ErrNoUploadPoint`。
2. `Variants` 覆盖五个维度且顺序固定：双扩展名、扩展名大小写、尾随点、类型声明与内容嗅探冲突、图片头拼接。
3. 每个变体带一个唯一哨兵值，哨兵必须出现在 `Body` 里，同批变体的哨兵互不相同；同一上传点连调两次，编号、文件名与内容一致。
4. 没有枚举过的上传点调用 `Variants` 报 `ErrUnknownPoint`。
5. `Submit` 成功后把上传物登记进清理表，返回服务端给出的落点路径与状态；`Uploaded` 能列出全部登记。
6. 同一变体只提交一次，重复提交报 `ErrVariantSubmitted` 且不再发请求；多路协程同时提交同一变体，只能有一个请求真正落地。
7. 变体不属于该上传点报 `ErrVariantMismatch`，一个请求都不发。
8. 未清理的登记项达到 `MaxUploads` 报 `ErrStorageQuota`，总请求预算用满报 `ErrRequestBudget`，两条上限都是硬顶。
9. `Cleanup` 返回本次清理条数并把登记项标记为已清理，重复调用返回 0。
10. `Verify` 只认登记过的上传物，未知的报 `ErrUnknownUpload`；处置器在配置的脚本白名单里且回读内容含该变体哨兵，两条同时满足才算可执行。
11. `Judge` 只按证据给结论，可执行的算命中，理由不能为空；证据里变体编号为空的报 `ErrUnknownUpload`。
12. `Snapshot` 带上枚举出的上传点、登记表、已提交变体与预算消耗；恢复后同一变体不再重复提交，损坏快照报 `ErrBadSnapshot` 且不动现有状态。

另有三处要在实现里落实：上传成功但只返回一个静态下载链接时不算可执行，必须验证脚本解析器是否接管了该路径；服务端按内容嗅探判类型时，改扩展名无效但改文件头可能有效，两个维度要分开下结论；覆盖同名文件与随机重命名都会让落点变化，验证必须用响应里返回的真实路径，不能按提交时的文件名去猜。

硬约束：同一变体只提交一次，并发下也不许发第二次，登记与去重一起成功或一起失败；每个上传物都要登记且可清理，清理只减不增，不许把已清理的条目重新算成在用；存储配额与请求预算两条上限同时生效；快照要带上登记表与清理状态，续跑后既不重复提交也不把已清理的条目复活。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发提交与重启续跑自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-38`
- 实现文件：`upload_bypass_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`UploadBypassProber`
