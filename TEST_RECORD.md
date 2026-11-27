# 测试记录

## 验收测试

```text
$ GOCACHE=/tmp/gsb-go-cache GOPATH=/tmp/gsb-go-path go test -race -count=1 ./...
?   	gsb/batch51-go-safety-c-25	[no test files]
ok  	gsb/batch51-go-safety-c-25/tests	1.012s
```

现有 `tests/test_host_header_poison_prober_test.go` 的 15 条测试全部通过，未修改、未跳过。

## 补充自构造验证

临时测试覆盖以下场景，全部通过后已删除，不进入交付：

- 转发头 `first.test, attack.test` 时，前端位置取 `first.test`、后端链接取 `attack.test`，判定 `Layered=true`。
- 仅模板层读取注入值时，页面位置/链接回退，但判定命中模板层；`MailPreview` 能渲染污染链接。
- `EscapeLinks=true` 时注入值被百分号编码，仍判定为污染。
- `MaxProbesPerEndpoint=2` 时 `Judge` 返回 `ErrProbeBudget`，探测数不超过硬顶。
- 并发判定下每入口 `PeakInFlight=1`；标记回收后下一轮不复用旧标记。
- 快照跨实例恢复记录、计数、活动标记与序列；同实例重载后再次保存与首次快照逐字节一致。
- 截断、篡改校验和及非法结构快照均返回 `ErrBadSnapshot`，且原状态保持不变。

```text
$ (cd /tmp/gsb-verify && GOCACHE=/tmp/gsb-go-cache GOPATH=/tmp/gsb-go-path go run .)
adhoc verification: PASS
```

## 静态检查

```text
$ GOCACHE=/tmp/gsb-go-cache GOPATH=/tmp/gsb-go-path go vet ./...
（无输出）
```
