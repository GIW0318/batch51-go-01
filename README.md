# 诱捕会话命令切分与时间线重放

## 题面

演练结束做复盘，把一段诱捕会话按时间线摊开时，边界全乱了：攻击者用退格改出来的命令只剩下后半截，目标程序自己回显的那行假提示符被当成了新命令，最后连接被掐断时停在半行的那条命令干脆消失。现在要把这套切分与重放逻辑落实到 `gsb/batch51-go-safety-c-32` 的 `honeypot_session_replayer.go`，类型名 `HoneypotSessionReplayer`。

输入是按块记录的原始流量，每块带方向、毫秒时刻和正文。入口签名保持：`NewHoneypotSessionReplayer(idleMs int64) *HoneypotSessionReplayer`、`Ingest(meta SessionMeta, chunks []Chunk) (string, error)`、`Sessions() []string`、`Segment(key string) ([]Command, error)`、`Timeline(key string) (Timeline, error)`、`Replay(key string) ([]Entry, error)`、`Stats(key string) (Stats, error)`、`Normalize(raw string) string`、`CollapseRepeats(lines []string) ([]string, int)`、`SessionKey(conn, identity string) string`、`IdleGaps(atMs []int64, thresholdMs int64) []int`、`DedupChunks(chunks []Chunk) []Chunk`。

要落地的十二条：

1. 会话按连接与身份分组，键由两者拼成；同一条连接换了身份，不得混进同一条时间线。
2. 块方向只认 `in` 与 `out`，其他值报 `ErrBadDirection`；连接号为空报 `ErrEmptyConn`。
3. 同一会话重复导入只保留一份，第二次导入返回同一个键，块数与命令数都不翻倍。
4. 命令只从入向数据切分并按换行成条；出向数据一律算作上一条命令的输出，不单独成条；同一条命令被拆成多个块到达时要拼回一条，不能按块各自成条。
5. 转义序列要清掉，CRLF 归一成换行；攻击者用退格和行编辑拼出来的命令，归一后必须还原成实际执行的那一条，不能留下残字。
6. 输出里出现长得像提示符的行，不得据此切出新的命令边界。
7. 会话断开时停在半行的那条命令要标记为不完整保留下来，不能当作没发生过直接丢掉。
8. 连续重复的命令折叠成一条并计数；中间夹了别的命令就不算重复，原样保留。
9. 时间线保留每条的原始时刻和相邻间隔，第一条的间隔恒为零，超过空闲阈值的间隔按阈值封顶。
10. 重放的每条偏移是封顶后间隔的累加，顺序与时间线完全一致。
11. 从未导入的会话取分段、时间线、重放、统计都报 `ErrUnknownSession`；只有出向数据的会话返回空命令集且不报错。
12. 空闲间隔函数返回后一条的索引，阈值按大于等于判定；统计给出块数、命令数、输出字节数与折叠条数。

硬约束：同一会话被多个协程同时导入时，最终只能存一份，返回的键也必须一致；重放与统计是只读操作，连续调用同一会话的结果逐字节相同；空闲阈值是硬顶，输入再长也不会把某一段间隔放大到阈值之上；归一结果只用于切分与重放，原始块必须保持原样可追溯，不得就地改写。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；并发导入与重复重放的稳定性自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-32`
- 实现文件：`honeypot_session_replayer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`HoneypotSessionReplayer`
