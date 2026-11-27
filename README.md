# 随机域名判定：字符分布打分、词典切分与短域名裁决

## 题面

凌晨两点，运营看板上一批像随机串的域名集中解析，值班同学肉眼判成随机生成域名，直接把整段后缀封掉，结果把一家品牌客户的短域名也一起掐了，投诉电话响了一整天；复盘时又发现另一类样本把随机标签放在三级子域上，注册域本身是正常词典词，规则却直接给了良性。请在 gsb/batch51-go-safety-c-27 的 dga_domain_classifier.go 中补全 DgaDomainClassifier，签名保持：NewDgaDomainClassifier(params Params) (*DgaDomainClassifier, error)、Classify(domain string) (Verdict, error)、Explain(domain string) (Explanation, error)、Reload(dict []string, params Params) error、Whitelist(domains []string) error、Generation() int64、CacheSize() int、ClearCache()、Snapshot() ([]byte, error)、Restore(data []byte) error、Stats() Stats。

要修的点：输入先去大小写与末尾的点，空标签、下划线、空格、超长标签或总长超过 253 一律报 ErrBadDomain；参数不合法、阈值倒挂、权重为负报 ErrBadParams。注册域取倒数第二个标签，子域取它前面的全部标签，顶级域不参与打分；每个标签独立算分后取最高者，Verdict 与 Explain 里的 Label 要写实际命中的标签，子域随机而注册域正常时结论必须落在子域上，不能把整串当成一个标签。单标签得分：n 为长度，n 不小于 LongLabelLen 加 BaseLongLabel，加 DigitWeight 乘数字个数再除以 n 的整数商，元音占比低于 VowelFloor 加 15，最长连续辅音超过 RunLimit 时每多一个加 RunPenalty，词典覆盖低于 CoverageFloor 且 n 不小于 MinDictLabel 加 20，连字符超过 HyphenLimit 加 HyphenPenalty，合计封顶 100。词典切分从标签左端做最长匹配，长度不足 3 的词忽略，覆盖率为被覆盖字符占比的整数百分比。分数达到 DGAThreshold 判 dga，达到 SuspiciousThreshold 判 suspicious，否则 benign。Explain 的 Features 按字典序给出命中的 digit_heavy、long_label、no_dict_match、vowel_low、long_consonant_run、hyphen_heavy，解释与判定必须来自同一次打分，特征要能复现每个加减分项。

品牌名本身没有词典词且数字偏多时，要按白名单精确命中与后缀流行度豁免，不能只靠分数；白名单只做整域精确匹配，不做后缀匹配；国际化域名要先经编码归一，不能拿原始字节算字符分布。初始代际为 1，每次 Reload 加一；Reload 一次发布词典与参数的新一代，替换期间进来的请求只能读到同一代，结果缓存键必须带代际，旧代分数不得污染新模型；缓存超过 MaxCache 按插入顺序淘汰并计数；ClearCache 只清当前代缓存，不改代际；Stats 分别累计判定、命中、未命中、淘汰与重载次数。Snapshot 要保存代际、词典、白名单与计数，损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：并发热替换与并发判定下，每个结果里的 Generation 与分数必须自洽；同一输入在同一代内重复判定得到完全相同的分数，不依赖 map 遍历顺序；Restore 之后 Generation、缓存与统计接着生效。

验收：go test ./... 15 条必须全过，不得修改测试、跳过测试或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-27`
- 实现文件：`dga_domain_classifier.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DgaDomainClassifier`
