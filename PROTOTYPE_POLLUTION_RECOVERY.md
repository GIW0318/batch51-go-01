# 原型链污染判定与恢复口径

## 载荷与路径

- `proto`：`{"__proto__":{"键":"值"}}`，路径 `__proto__.键`。
- `constructor`：`{"constructor":{"prototype":{"键":"值"}}}`，路径 `constructor.prototype.键`。
- `array`：`[{"__proto__":{"键":"值"}}]`，路径 `[0].__proto__.键`。
- `Generate` 负责构造上述 JSON；`Inject` 会重新解析 `Body`，路径、键和值不一致时按坏载荷拒绝。

## 判定口径

- `vulnerable`：三条路径都落入目标共享配置，允许后续新建对象读到。
- `safe`：请求返回成功，但不写入原型键，探测结论为 `not_inherited`。
- `blacklist_proto`：阻止 `proto` 与 `array` 路径，但允许 `constructor.prototype`；后者命中时记 `blacklist_bypassed`，不能因为拦住了 `__proto__` 就判为安全。
- `WorkerIsolated`：写入只进入隔离副作用层。本轮 `Response.Merged` 为空，`Verify`/`ResidualKeys` 仍能观察到；结论记 `side_effect_only`，通道记 `side_effect`。
- 普通共享配置命中记 `inherited`，通道记 `response`。只有实际读回值且相对基线发生变化才确认新污染；原本存在且未变化的默认值不算命中。

## 基线与恢复

- 每目标有单调序号。首次探测自动取注入前快照；同轮后续探测复用该快照，不因已发生污染而漂移。显式 `Baseline` 才会刷新快照。
- 恢复删除本目标的覆盖项与隔离项；若覆盖了默认键，则删除覆盖后恢复原默认值，未被动过的默认键不删。`Removed` 为删除清单，正常恢复后 `Residual` 为空。
- 无法完整恢复时，残留键进入失败账本并封顶到 `MaxResidualKeys`；账本只追加去重，不覆盖已有记录。此后自动基线排除残留键，避免把污染当作下一轮基准。
- 同一键重复注入只保留一条残留记录，但每次实际落地的污染都累计到 `Polluted`。探测次数由 `MaxProbesPerTarget` 硬顶，超出返回 `ErrProbeBudget`。

## 一致性与快照

- 每目标一把互斥锁，注入、验读、恢复与基线取数串行；全局信号量限制并发。共享映射只通过持锁读写和深拷贝暴露，避免读到半更新状态。
- `Export` 快照包含目标配置、共享值、覆盖/隔离层、基线、结论和计数。`Import` 先完整解析、校验并构造新状态，成功后一次性替换；截断 JSON、非法模式、重复目标或配置不一致均返回 `ErrBadSnapshot`，现网状态不变。
