# WAF 绕过变异规划：多层归一化差异、载荷等价变形与拦截反馈收敛

## 题面

护网前对搜索接口做复测，原始载荷在边缘就被 403 拦掉，值班同学手工改了几种编码，有两次请求看着是 200，把结果贴进报告说"绕过成功"，后端日志里却连一条查询都没执行；另一路把同一份变体拿到商品页重放，那边同样 200 但后端确实执行了，两个路径的结论被混在一起。这套变异与收敛逻辑要补到 `gsb/batch51-go-safety-c-45` 的 `waf_bypass_mutator.go`，类型名 `WafBypassMutator`，构造签名 `NewWafBypassMutator(probe Probe, limits Limits) *WafBypassMutator`。

接口：`Baseline(point InjectionPoint) (Blocked, error)`、`Variants(payload Payload) ([]PayloadVariant, error)`、`Submit(variant PayloadVariant) (Outcome, error)`、`Rank(variants []PayloadVariant) ([]PayloadVariant, error)`、`Stop(reason string) error`、`Throttled() bool`、`Tried() []string`、`Effective() []string`、`Stats() (MutatorStats, error)`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。

要落地的十二条：

1. 载荷为空报 `ErrEmptyPayload`，载荷没绑定注入点报 `ErrBadPoint`；注入点缺标识或缺路径报 `ErrBadPoint`；通道为 nil 报 `ErrNilProbe`。
2. 变体固定六种、按 `enc_url`、`enc_double`、`enc_unicode`、`sep_equiv`、`keyword_split`、`case_swap` 的顺序返回，标识为 `载荷标识|算子名`，同一载荷两次生成必须逐字段一致。
3. `enc_url` 把非字母数字字节按大写百分号编码；`enc_double` 在 `enc_url` 的结果上再来一遍，百分号变 `%25`。
4. `enc_unicode` 把每个字符写成四位小写十六进制转义；`sep_equiv` 把每个空格换成 `/**/`。
5. `keyword_split` 对每个长度超过 2 的空白分隔词，在第一个字符后插入 `/**/`，短词原样保留。
6. `case_swap` 只对字母生效，按字母出现次序把偶数位转大写、奇数位转小写，数字与符号不动。
7. `Baseline` 用注入点自带的原始载荷打一次：2xx 记 `reason_allowed`，403、406、429 记 `reason_waf_rule` 且置 `Blocked`，5xx 记 `reason_indeterminate` 且按拦截处理，其余 4xx 记 `reason_client_error` 并认为已到应用。
8. `Submit` 前必须对该注入点做过基线，否则报 `ErrNoBaseline`；同一变体标识只提交一次，重复报 `ErrDuplicateVariant`，重复的那次不许打到通道上。
9. 结果分类：状态码不在 403、406、429 里算放行（`Passed`），放行即 `Reached`；只有放行且观察到可观察副作用才算有效绕过，原因写 `reason_bypass`，否则写 `reason_blocked`。
10. 连续拦截累计到 `ThrottleAfter` 后进入降速状态，后续提交报 `ErrThrottled` 且不再打通道；一次放行把连续计数清零；`Stats` 要把已发送、已拦截、有效、连续拦截四个计数与降速状态一起给出。
11. `Rank` 按历史有效次数降序、同次数按算子固定序、再按标识升序稳定排序；从没提交过的变体排在后面，不得因为没数据被丢掉。`Stop` 原因为空白报 `ErrBadReason`，重复 `Stop` 以第一次的原因为准，停掉之后 `Submit` 报 `ErrStopped`。
12. 快照保存基线表、已尝试变体、有效变体与降速、停止状态；恢复后 `Tried`、`Effective` 与计数不得清零，基线仍可用；快照损坏报 `ErrBadSnapshot`，恢复失败不得改动现网状态。

另外三条容易判反：请求被放行但后端没执行等价语义时不算绕过，必须拿可观察副作用确认；WAF 的 403 与后端返回的业务错误要分开归因，后者说明载荷已经到达应用；同一变体在不同路径上的归一化层级不同，结论不能跨路径复用。

硬约束：`Submit`、`Stop` 与 `Snapshot` 并发调用时已尝试集合与降速状态要串行裁决，同一变体只落一次；拦截率连续上升时只能降速，不许加大发送节奏；同一批变体重复 `Rank`，顺序必须完全一致。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-45`
- 实现文件：`waf_bypass_mutator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`WafBypassMutator`
