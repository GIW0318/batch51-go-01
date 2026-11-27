# 聊天内容审核：字符归一化、变体规避、词库热更与分级处置

## 题面

上线三天后，世界频道开始刷满“低价出金”“代练上分”，运营把词库拉出来一看，这些消息全是全角字母加空格拼出来的，行内工具一条都没拦住；反过来，一位外语玩家正常聊天却连续被禁言，申诉里贴出的原文一个脏词都没有。更麻烦的是词库热更那半小时，同一句广告被判成两个不同等级，会长看到的是警告，普通成员看到的却是封禁。

落点在 gsb/batch51-go-game-a-17 的 chat_content_moderator.go，类型 ChatContentModerator，包名 task，模块路径 gsb/batch51-go-game-a-17。公开接口保持：NewChatContentModerator(limits ModLimits, p Policy) *ChatContentModerator、SetLexicon(ver string, entries []LexEntry) error、Check(msg ChatMsg) (Verdict, error)、Explain(msg ChatMsg) []Hit、SetPolicy(p Policy) error、Appeal(msgID string, reason string) error、SetRateLimit(n int, window time.Duration) error、Stats() ModStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：

1) SetLexicon 的版本号不能为空白，条目不能为空，词条不能为空白，等级必须为正，同一版本内不得重复词条，否则返回 ErrBadLexicon 且旧词库继续使用；条目数超过 MaxTerms 返回 ErrLexiconTooLarge，不做截断。
2) 词库整版原子生效：热更期间的审核要么整份用旧版跑完，要么整份用新版；结论里的版本号必须与判定所用的等级来自同一版，不能出现新版本号配旧等级。
3) Check 的消息编号或发送者编号为空白返回 ErrBadMessage；审核前先归一化：全角转半角、全角空格转半角、大小写折叠、连续空白折叠，西里尔同形字按拉丁字母归一，零宽字符剔除，拆字插空先还原；正常外文单词不得被同形字规则改写。
4) 命中多条词条时取最高等级，Matched 按词条升序去重；达到封禁线返回 block，达到禁言线返回 mute，达到警告线返回 warn，未命中返回 allow 与 clean。
5) 白名单短语优先于词库：命中白名单的消息一律放行，原因码 whitelisted，即使同一段文本里还有词库词也不降级。
6) 同一消息编号重复审核必须返回逐字段相同的结论，只计一次审核数与一次处置数。
7) 归一化长度按剔除零宽字符后的可见字符计；超过 MaxNormalizedLen 的消息降级为警告，原因码 oversize，并累加超长计数，不得直接封禁。
8) 同一发送者在窗口内超过 SetRateLimit 设定的条数时，后续消息返回 mute 与 rate_limited 并累加限流计数；不同发送者互不影响；条数或窗口非正返回 ErrBadRateLimit。
9) Appeal 对未知消息返回 ErrUnknownMsg，原因为空白返回 ErrBadReason，同一消息重复申诉幂等，只累加一次申诉计数。
10) Explain 返回归一化文本里命中的词条、分类与等级，按词条升序；Explain 不改动审核计数。
11) Stats 汇总审核数、放行数、警告数、禁言数、封禁数、超长数、申诉数与限流数。
12) 快照保存词库版本与条目、策略、结论记录与全部计数；恢复后同一消息重复审核仍返回同一结论，新消息按恢复后的词库判定；载荷损坏返回 ErrBadSnapshot 且不动现有状态。

硬约束：Check、SetLexicon、Stats 与快照会被多个聊天线程并发调用，读到的词库只能是完整旧版或完整新版；同一消息编号的首次审核只能发生一次；限流窗口按消息时间推进，窗口条目数受 MaxWindowEntries 硬顶。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-game-a-17`
- 实现文件：`chat_content_moderator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ChatContentModerator`
