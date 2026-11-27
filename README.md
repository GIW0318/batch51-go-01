# 证书式初始认证的信任锚选择与票据签发前置核验

## 题面

证书式初始认证上线后，客服陆续收到证书登录失败的单子，但失败原因都被归成同一句认证失败，运维拿着同一个错误码查了三天也没分出是证书链不通、映射规则冲突还是账户被停用。更严重的一次出在信任锚轮换：旧根和新根同时在配置里，一张中间证书其实两条路径都连得上，服务按配置顺序取了旧锚，签出的票据带着旧代际，第二天旧锚下线，这批票据全部失效。还有一次是账户在映射之后被人禁用，签发环节沿用了映射时读到的状态，仍然把票据发了出去。请把这段从证书到票据的前置核验补完整。落点在 `gsb/batch51-go-safety-a-14` 的 `pkinit_anchor_guard.go`，类型 `PkinitAnchorGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-a-14`。

公开接口保持：`NewPkinitAnchorGuard(cfg AnchorConfig) *PkinitAnchorGuard`、`AddAnchor(a TrustAnchor) error`、`RetireAnchor(id string, at time.Time) error`、`Anchors() []TrustAnchor`、`AddRule(r MappingRule) error`、`Rules() []MappingRule`、`Verify(chain []Cert, at time.Time) (ChainResult, error)`、`Map(principal string, sans []string, at time.Time) (Mapping, error)`、`Issue(req IssueRequest, at time.Time) (Ticket, error)`、`SetAccount(st AccountState) error`、`Account(account string) (AccountState, bool)`、`Trace() []TraceEntry`、`Stats() AnchorStats`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`。

要修的地方：证书链为空或超过 `MaxChainLen` 报 `ErrBadChain`；相邻证书的签发者与主体必须首尾相接，最后一张的签发者必须命中一个未退役的锚，否则报 `ErrNoTrustPath`；叶子证书必须落在有效期内，过期报 `ErrExpiredCert`，尚未生效报 `ErrNotYetValid`；叶子的用途必须包含 clientAuth，否则报 `ErrKeyUsage`；已退役的锚不再参与校验，退役不存在的锚报 `ErrUnknownAnchor`；锚要标注代际，校验结果带回所选锚的编号与代际，信任路径按叶子到根的次序列出主体；锚编号为空、主体为空或重复注册报 `ErrBadAnchor`；映射规则分 dns、upn、principal 三类，通配符只允许以 `*.` 或 `*@` 开头；命中零条规则报 `ErrMappingMissing`，命中两条以上报 `ErrMappingConflict`，不许悄悄取第一条；规则不完整报 `ErrBadRule`；签发前要把链校验与映射重新走一遍，票据带回账户、规则编号、锚编号、锚代际、映射代际与全局递增序号；账户没有登记状态报 `ErrAccountUnknown`，账户被禁用报 `ErrAccountDisabled`；统计分别记链校验次数、拒绝、映射、冲突与签发。

另有三处只在事故复盘里提过、没有进入常规回归的边界：证书链里某张中间证书同时能连到新旧两个锚时，要按锚的优先级选一个，并把另一条可达路径记进决策轨迹；主体备用名为空但主体本身可以映射时，是否放行由映射规则决定，不能默认拒绝；账户在映射之后被禁用时，要在签发前重新核验，不能沿用映射时读到的状态。

硬约束：锚、规则与账户状态在并发增删时，一次签发只能看到完整的一套配置，不能读到半套；快照要带回锚、规则、账户状态与序号水位，损坏快照报 `ErrBadSnapshot` 且不改动现网，恢复后继续签发时序号必须大于此前任一张；链长与规则数按配置硬顶，超限拒绝而不是截断。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；锚轮换期间的并发签发与重启恢复自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-14`
- 实现文件：`pkinit_anchor_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PkinitAnchorGuard`
