# TLS 客户端指纹：顺序敏感归一、工具匹配与代理可信度

## 题面

边界网关上线后，同一款手机客户端在抓包里被认成了三种不同的程序：中间盒把扩展顺序重排过，指纹每次都变；另一个客户端每次握手随机塞一段保留填充，指纹也跟着变，风控把它当成新设备反复挑战。更麻烦的是，经两级代理转发的连接照样被标成可信来源，指纹库还没收录的新客户端被硬匹配到最像的已知项上，误封了一批内部工具。请在 gsb/batch51-go-safety-c-28 的 tls_client_fingerprint_matcher.go 中补全 TlsClientFingerprintMatcher，签名保持：NewTlsClientFingerprintMatcher(opts Options) (*TlsClientFingerprintMatcher, error)、Extract(hello ClientHello) (Fingerprint, error)、Match(fp Fingerprint) (MatchResult, error)、Canonical(fp Fingerprint) (string, error)、Compare(a, b Fingerprint) (int, error)、Reload(lib []LibraryEntry) error、LibraryVersion() int64、Snapshot() ([]byte, error)、Restore(data []byte) error、Stats() Stats。

要修的点：没有密码套件报 ErrEmptyHello，扩展或套件数超过 MaxExtensions 与 MaxCiphers 报 ErrHelloTooLarge。OrderHash 必须对顺序敏感，SetHash 必须对顺序不敏感，集合侧要排序去重；规范化串固定为 c=套件;e=扩展;cur=曲线;pf=点格式;alpn=协议，各列表用逗号连接，调用 Canonical 要返回这条原文。ServerName 不参与指纹，同一客户端访问不同站点必须得到同一组哈希；ALPN 按握手顺序进顺序指纹，集合指纹里再排序。保留类填充值要从两侧指纹里剔除，否则同一客户端每次握手都会得到不同哈希。扩展顺序被中间设备重排时，OrderHash 允许变化，SetHash 必须保持稳定。指纹库匹配只认精确命中，库中没有的客户端一律返回 unknown 与 no_entry，不能回退到最相似的已知项。代理链非空时 Trusted 置假、ProxyHops 记跳数，命中结果仍给出客户端名但 Reason 必须是 proxied。命中恶意工具时 Malicious 置真、Tool 写条目名、Client 留 unknown；命中普通客户端时 Client 写条目名、Tool 留空。结果里必须带命中的库版本号，回滚库时版本号继续递增，不复用旧号。命中依据要能落到具体字段，排障时一眼看出是顺序命中还是集合命中。Compare 供排障页面展示相似度：两侧哈希全等给 100，仅集合相同给 50，否则 0。初始版本号为 1，每次 Reload 加一；Reload 整代替换并让版本号自增，空名字或不是 64 位十六进制的哈希报 ErrBadLibrary，失败时旧库继续可用。Snapshot 要保存库内容、版本与计数，损坏快照报 ErrBadSnapshot 且不动现网。Stats 分别累计提取、匹配、重载与代理标注次数。指纹只做本地解析，不发起任何网络连接。

硬约束：热替换与并发匹配之间只能看到完整的旧库或完整的新库，不能出现一半旧一半新；同一份客户端问候重复提取必须逐字段一致，不依赖 map 遍历顺序；Restore 之后库内容、版本与统计接着生效。

验收：go test ./... 15 条必须全过，不得修改测试、跳过测试或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-28`
- 实现文件：`tls_client_fingerprint_matcher.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`TlsClientFingerprintMatcher`
