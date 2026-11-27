# 主体重命名的别名台账与旧名保留期

## 题面

集团账号中台把用户改名从人工提单改成自助之后，一周里出了三件事。市场部一位同事改名后，用旧邮箱登录的移动端仍能进，但审计主体记成了旧名字对应的那条例外记录，安全团队查了两天才对上人；客服组一个离职账号的旧名在保留期结束后被新人注册，新人登录时偶发被解析到离职员工的历史权限；还有一次，运营把某账号改成一个它自己两年前用过的旧名，系统直接建出一朵环形别名，之后解析这个账号的接口开始超时。三次事故的共同点是台账只记名字不记主体，事后没人能还原一次登录到底落在谁身上。落点 gsb/batch51-go-safety-a-10 的 principal_alias_ledger.go，类型 PrincipalAliasLedger，包名 task。

接口保持：NewPrincipalAliasLedger(cfg AliasConfig) *PrincipalAliasLedger、Register(principal, name string, at time.Time) error、Rename(principal, newName string, at time.Time) (RenameResult, error)、Resolve(name string) (Resolution, error)、Reserve(name string, until time.Time, owner string) error、Release(name string) error、History(principal string) ([]Alias, error)、Chain(principal string) ([]string, error)、Reservations(now time.Time) []Reservation、Prune(now time.Time) (int, error)、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：一、名字全局唯一，活动名、别名、预留名三者互斥，被占用时报 ErrNameTaken；二、改名后旧名进别名表并继续指向同一主体，保留期为改名时刻加 Retention，保留期内解析要返回别名命中标记与当前规范名，Chain 按从旧到新排列；三、令牌与审计按主体 ID 延续，保留期内用旧名登录成功时，审计主体必须记为新名对应的主体；四、同一主体连续改名形成别名链，解析要跳过多级但不能成环，新名恰好是自身某个历史别名时按 ErrAliasCycle 拒绝；五、改名要保证新名唯一并与旧名回收原子完成，并发抢同一新名只允许一个成功；六、别名链深度按改名次数计，超过 MaxChainDepth 报 ErrChainDepth，单主体历史条数将超过 MaxHistory 报 ErrHistoryLimit，超限拒绝改名而不是丢历史；七、保留期内旧名禁止他人注册，保留期结束后旧名释放可重新分配，原别名链不得把新主人的登录解析回旧主体；八、Reserve 可把名字预留给指定 owner，重复预留或名字已被占用报 ErrNameTaken，预留条数超过 MaxReserved 报 ErrReserveLimit，Release 不存在或已释放的预留报 ErrNotReserved；预留名在 Resolve 里返回 Reserved 标记且 Found 为假；九、Resolve 对活动名、别名、预留名分别给出标记，完全未知的名字报 ErrNameNotFound，主体或名字为空报 ErrBlankName；十、Prune 清理到期别名与到期预留并返回清理条数，重复执行返回零；十一、序号在改名与预留之间单调递增，跨重启不回退；十二、快照要带别名链、预留表与序号水位，损坏载荷回 ErrBadSnapshot 且现网不动。

硬约束两条：改名与旧名回收在同一临界区内完成，并发同名注册只允许一个成功，别名链解析不得出现环；别名链深度、单主体历史条数与保留条目总数设硬顶，超限拒绝改名而不是截断历史。快照与恢复要么全量生效要么完全不动。

验收：在模块目录执行 go test ./...，15 条必须全部通过；不得改动测试、跳过用例或按固定输入写死；并发与重启恢复场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-10`
- 实现文件：`principal_alias_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PrincipalAliasLedger`
