# API 版本降级探测：载体展开、响应模式对齐与鉴权差异判定

## 题面

灰度回滚之后，老客户端仍在打 `/v1` 的订单接口。安全评审发现同一个资源在 v1 上不带令牌也能读到数据，而当前的 v2 一律返回 401；更麻烦的是网关会忽略版本头，只在路径前缀上分版本，上一版巡检脚本把三种入口的结果混成一张表，结论因此对不上。这套探测逻辑要补到 `gsb/batch51-go-safety-c-41` 的 `api_version_downgrade_prober.go`，类型名 `ApiVersionDowngradeProber`，构造签名 `NewApiVersionDowngradeProber(tr Transport, limits Limits) *ApiVersionDowngradeProber`。

其余接口：`Discover(base string, spec Schema) ([]VersionSurface, error)`、`Probe(surface VersionSurface, req Request) (Observation, error)`、`Align(obs []Observation) ([]VersionDiff, error)`、`Judge(diff VersionDiff) (DowngradeVerdict, error)`、`Budget() (BudgetState, error)`、`SetRateLimited(bool)`、`Surfaces() []VersionSurface`、`Observation(surfaceID, resource string) (Observation, bool)`、`Snapshot() ([]byte, error)`、`Restore([]byte) error`。

要落地的十二条：

1. 版本候选来自路径前缀、请求头与查询参数三种载体，`Discover` 按资源×版本×载体展开，路径载体拼成 `base/版本/资源`，另两种取 schema 里的头名与参数名。
2. 版本面标识为 `资源|版本|载体`，返回结果按标识升序且不得重复；没指定默认版本时取版本列表最后一项，指定了却不在清单里报 `ErrBadSchema`。
3. 空或全空白的 base 报 `ErrEmptyBase`；资源清单为空报 `ErrNoResources`，版本清单为空报 `ErrNoVersions`。
4. 探测通道为 nil 报 `ErrNilTransport`；版本面缺标识、资源、版本报 `ErrBadSurface`；载体不认识报 `ErrUnknownCarrier`。
5. 响应按状态码分桶：2xx 记 `ok`，401 与 403 记 `auth_gate`，404 记 `not_found`，其余记 `server_error`，桶名写进 `Observation.Class`，原始状态码一并保留。
6. 响应字段先去重再按字典序排序，空错误码不补默认值。
7. 同一版本面只做一次有效探测，重复探测回放上次结果并置 `Cached`，不占额度；有效探测数超 `MaxProbes` 报 `ErrProbeBudget`。
8. 限速开关打开时报 `ErrRateLimited` 且不扣额度、不打通道；连续 `server_error` 达到 `MaxConsecutiveServerErrors` 后熔断，后续探测报 `ErrCircuitOpen`，一次非 5xx 探测把连续计数清零。
9. `Align` 按资源分组，每组以默认版本的面为基准，其余面各出一条差异，输出按资源名、候选面标识升序；没有观测报 `ErrNoObservations`，某资源缺基准报 `ErrNoBaseline`。
10. 差异里缺字段与多字段各自排序；两侧错误码不同、或一侧有码一侧为空时置 `PatternMismatch`。
11. 旧版本返回 404 不能直接当成资源不存在，可能只是路由前缀不同，要结合响应模式判断；旧版本少几个字段也可能只是默认裁剪，不能据此认定鉴权缺失。
12. 快照保存已发现的版本面、探测台账、额度用量与熔断状态，恢复后额度不得重置、已探测面继续回放；快照损坏报 `ErrBadSnapshot`，恢复失败不得改动现网状态。

`Judge` 的优先级固定：基准 `auth_gate` 而候选 `ok`，判 `downgraded`、原因 `reason_auth_gap`，`AuthGap` 与 `Conclusive` 都为真；候选 `server_error` 判 `undetermined`、原因 `reason_server_error`、不结论；字段有增删或模式不一致判 `undetermined`、原因 `reason_field_cut`；两侧都是 `auth_gate` 判 `consistent`、原因 `reason_auth_parity`；其余判 `consistent`、原因 `reason_stable`。差异缺候选标识报 `ErrBadDiff`。

硬约束：同一版本面的并发探测只能触发一次通道调用，其余调用等结果回来直接复用；探测与快照互斥，读到的要么是全量旧状态要么是全量新状态；同一组观测重复比对，差异列表与判定逐字段一致。

验收：`go test ./...` 的 15 条用例全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-c-41`
- 实现文件：`api_version_downgrade_prober.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ApiVersionDowngradeProber`
