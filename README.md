# 目录查询过滤器注入的构造与差分判定

## 题面

目录服务改版后，账号查询页面对输入做了一层转义，安全同学拿老的探测串跑了一轮，全是"未发现注入"，结果一个真实的过滤器注入在上线两周后才被外部报出来。现在要把探测与判定这套逻辑补到 `gsb/batch51-go-safety-c-35` 的 `ldap_filter_injection_prober.go`，类型名 `LdapFilterInjectionProber`。

输入是一个查询入口（标识、基准过滤串、条数上限、是否走缓存）和一个能把过滤串打到目录上的应答者。入口签名保持：`NewLdapFilterInjectionProber(maxConcurrent int) *LdapFilterInjectionProber`、`Build(base string) ([]Payload, error)`、`Submit(t Target, r Responder, payload Payload) (ProbeResult, error)`、`Diff(baseline, injected ProbeResult) Diff`、`Nonce() string`、`WrapClosure(field string) string`、`WrapWildcard(field string) string`、`WrapLogic(field string) string`、`SortEntries(entries []string) []string`、`ClassifyCounts(baseline, injected, limit int) (string, bool)`。

要落地的十二条：

1. 载荷分三类：括号闭合、通配、逻辑组合，`Build` 按这个顺序返回三条；基准过滤串为空报 `ErrEmptyBase`。
2. 每轮载荷带一个新前缀，两轮前缀不得相同，前缀必须出现在过滤串里，便于在结果里认出是哪一轮打的。
3. 同一入口的提交必须串行，不同入口之间可以并行，整体并发上限由构造参数固定；排队时间过长的请求按失败返回，不要无限等下去。
4. 没有应答者报 `ErrNilResponder`，入口标识为空报 `ErrBadTarget`。
5. 查询失败要把底层错误包住返回，不能吞掉之后当成零条结果。
6. 返回的属性先去重再按字典序排列，比对也在排好序的列表上做；一条属性都没有时按空集合处理，不要当成查询失败。
7. 条数变化判确认；条数不变但属性集合变了也判确认；两者都一样才判未确认。
8. 探测本身失败时判 `error` 并写明原因，不能算成未确认。
9. 通配符被转义但括号没被转义时，只能靠逻辑组合载荷确认，不要因为通配没回显就收工。
10. 服务端对结果条数做截断时，真假条件的条数差要按截断上限标注，不要当成完整差分。
11. 查询被缓存导致第二轮直接命中旧结果时，要按带前缀的过滤串规避，两次结果一模一样不能当成未确认的证据。
12. 判定用的条数与属性必须来自同一次提交，不得把一轮的条数和另一轮的属性拼在一起；差分里两侧条数都要留下，便于事后回看。

硬约束：同一入口的探测串行执行，并发计数在入口维度上始终不超过一；并发上限是硬顶，超出的请求排队而不是直接丢弃；同一组结果重复比对，结论、说明与两侧条数必须完全一致，不许因为调用次数变化而改写结论。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；串行提交与多轮规避自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-35`
- 实现文件：`ldap_filter_injection_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`LdapFilterInjectionProber`
