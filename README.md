# 服务主体密钥表的版本轮换与重启一致性

## 题面

年初做服务主体密钥轮换时，值班同事按旧流程先把新密钥写进密钥表、再删掉旧密钥，结果在途票据立刻验不过，两个小时里业务全线报认证失败。复盘又翻出三个问题：同一个版本号被两个脚本各导了一次，后导的把先导的密钥材料覆盖掉，票据校验时好时坏；新旧密钥交替期间只保留了最高版本，低版本被整份丢掉，跨版本签发的票据全部失效；轮换写到一半进程被重启，磁盘上留下一半新一半旧的密钥表，服务再也起不来。请把这份密钥表管理补完整。落点在 `gsb/batch51-go-safety-a-13` 的 `keytab_rotation_guard.go`，类型 `KeytabRotationGuard`，包名 `task`，模块路径 `gsb/batch51-go-safety-a-13`。

公开接口保持：`NewKeytabRotationGuard(cfg KeytabConfig) *KeytabRotationGuard`、`Install(entry KeyEntry, at time.Time) (InstallResult, error)`、`Promote(principal string, version int, at time.Time) error`、`Active(principal string) (KeyEntry, error)`、`Retire(principal string, version int, at time.Time) error`、`Verify(t Ticket) (KeyUse, error)`、`Entries(principal string) []KeyEntry`、`Stats() KeytabStats`、`Generation() uint64`、`WriteFile(path string) error`、`LoadFile(path string) error`、`Snapshot() ([]byte, error)`、`Restore(data []byte) error`，以及包级函数 `Seal(material, payload string) string`。

要修的地方：条目不完整（主体或材料为空、版本小于一、加密类型不认识）报 `ErrBadEntry`；版本号只能单调上升，低于现有最高版本报 `ErrVersionRegression`，同一版本下不同加密类型各占一条记录、互不覆盖；新装的密钥一律先进入待启用状态，未启用的密钥不得验票，票面指到待启用版本报 `ErrStagedKey`；`Promote` 只能启用比当前活动版本更新的版本，否则报 `ErrStalePromotion`；一个版本都没启用过时查活动密钥报 `ErrNoActiveKey`；验票时票面版本为零表示由校验方按版本号从高到低自行尝试，命中后返回实际使用的版本与加密类型，所有活动密钥都打不开报 `ErrNoMatchingKey`；票面指定了不存在的版本报 `ErrUnknownVersion`；已退役版本验票报 `ErrRetired`，活动版本与宽限期内未退役的旧版本仍须可验；退役只能发生在后继密钥启用满宽限期之后，宽限期从后继密钥启用时刻起算，不足报 `ErrGraceNotElapsed`；活动密钥本身不能退役，报 `ErrActiveKey`；`Entries` 按版本降序、同版本按加密类型升序；每次成功变更都让代际计数加一，统计分别记安装、启用、退役、验票与未命中。

另有三处只在事故复盘里提过、没有进入常规回归的边界：仍在使用旧密钥签发的票据，若在宽限期前把旧密钥退役，这些在途票据会验不过，所以宽限期内必须继续接受旧版本；同一版本号被重复导入但密钥材料不同时，必须拒绝而不是覆盖已有材料；服务在轮换中途重启，要能从磁盘恢复到一致代际，而不是恢复出半套密钥。

硬约束：写盘必须整文件原子替换，先写临时文件再改名，任何一步失败时磁盘上的旧密钥表保持原样且仍可加载，损坏文件在 `LoadFile` 时报 `ErrBadKeytab` 且不改动内存表；快照要带上全部代际、启用与退役标记以及代际计数，损坏快照报 `ErrBadSnapshot` 且整体不动现网，恢复后代际计数不得回退；同一主体的安装、启用、退役在并发下必须串行，代际计数与统计不得出现半更新，版本数与在途写盘都按配置硬顶。

验收：`go test ./...` 15 条必须全过，测试不得修改、跳过、按固定输入写死；轮换中途重启与并发变更自行构造程序验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch51-go-safety-a-13`
- 实现文件：`keytab_rotation_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`KeytabRotationGuard`
