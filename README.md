# 下载入口的路径归一与根目录约束

## 题面

一周里运维转来三张下载入口的单子：有人把链接里的文件名换成 ../../ 之后，服务照样拼出根目录之外的绝对路径，把配置目录的文件读走了；有租户在公开目录下放了指向宿主机 /etc 的软链接，准入只看了链接自身位置，整片目录被放进来；站点根从 /srv/www 挪到 /srv/www2 后，旧的前缀判断把新根当成根内同级目录，越界请求整片放行。另有两条：大小写按错也能命中，审计里查不到按什么规则放行；同一文件的几种转义写法在日志里看着不一样。

落点在 gsb/batch51-go-safety-r-09 的 path_traversal_guard.go，类型 PathTraversalGuard，包名 task。公开接口保持：NewPathTraversalGuard(root string, opts GuardOptions) (*PathTraversalGuard, error)、Normalize(raw string) (string, error)、Decode(raw string) (string, int)、WithinRoot(abs string) (bool, error)、Resolve(raw string) (ResolvedPath, error)、Open(raw string, readOnly bool) (Handle, error)、SetLinkResolver(fn func(string) (string, error)) error、SetMaxPathLen(n int) error、Audit() []AuditEntry、Rejections() int、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的地方：

1. 空串或全空白报 ErrEmptyPath；以斜杠开头、盘符开头、双反斜杠开头一律报 ErrAbsolutePath；归一后长度超过 MaxPathLen 报 ErrPathTooLong，长度按归一结果算。
2. 归一折叠重复分隔符、单点段与末尾分隔符，正反斜杠都当分隔符，输出统一用正斜杠；不访问文件系统，也不改写调用方的大小写。
3. 父目录段把深度压到根以上报 ErrEscapeAttempt，靠段栈耗尽判断，不靠结果字符串前缀比较。
4. 判根在归一之后，用根目录加一个分隔符做前缀；根自身算根内，根为 /srv/www 时 /srv/www2/index.html 不算；传入非绝对路径报 ErrAbsolutePath。
5. 链接解析器逐跳跟随，结果与输入相同即停；用满 MaxLinkHops 仍未稳定报 ErrLinkLoop，调用次数不得超过 MaxLinkHops；未注入时按无链接处理。
6. 真实路径由根与归一结果以正斜杠拼接；放行时 ResolvedPath 填好请求原文、归一结果、真实路径、解码轮数、根内标记与原因码 reason_ok。
7. Open 只在放行时给句柄，句柄带归一结果、真实路径、只读标记与本次审计序号；被拒时返回零值句柄与错误，序号为 0。
8. 百分号转义与 Unicode 斜杠变体先解到不再变化的稳定形态再归一，各种等价写法必须收敛到同一归一结果，只解一层不算完成。
9. 链接按真实目标判根，链接自身在根内而目标在根外一律拒绝，原因码 reason_link_escape，不允许先放行再补记录。
10. 根内比较的大小写语义跟随根所在文件系统：不敏感时 A/B 与 a/b 等价，敏感时逐字节区分，两种模式都不得改写调用方传入的大小写。
11. 每次放行与拒绝都写审计，序号从 1 起严格递增，到 AuditCap 后只留最新若干条；拒绝另有独立计数，不因裁剪减少。
12. 快照带下一个序号、拒绝计数与审计内容，恢复后序号继续递增且不重号；损坏快照报 ErrBadSnapshot，现有状态不变。

硬约束：根为空报 ErrEmptyPath，MaxPathLen、MaxLinkHops、AuditCap 任一不为正报 ErrBadLimit；SetMaxPathLen 传非正数报 ErrBadLimit 并保持原上限，SetLinkResolver 传 nil 报 ErrBadResolver。并发调用 Normalize、Resolve 与 Open 时审计序号不重号不丢条，拒绝计数与实际拒绝一致；路径长度、链接跳数与审计容量都是硬上限，超限拒绝而非截断；根内判定与链接解析按全量状态决定，不允许半边新配置半边旧配置的中间结果。

验收：go test ./... 的 15 条用例全过；测试不得修改、跳过或按固定输入写死；并发与重启恢复自己造场景验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-r-09`
- 实现文件：`path_traversal_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PathTraversalGuard`，配置类型 `GuardOptions`，结果类型 `ResolvedPath`、`Handle`、`AuditEntry`
