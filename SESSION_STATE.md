# iSCSI 会话状态说明

## 状态机

会话只有两个状态：`pending`（半认证）与 `active`（已认证）。不存在第三种中间态。

- `Login` 校验门户/发起者/目标非空白（发起者 IQN 先小写化并去首尾空白再比较），
  在持锁状态下检查全局会话数与同发起者会话数上限，通过后创建 `pending` 会话并签发
  一次性 CHAP 挑战号。任一项不满足即拒绝，不留会话。
- `Auth` 是 `pending → active` 的唯一推进路径，判定顺序：
  1. 会话不存在 → `ErrUnknownSession`；
  2. 失败次数已达 `ChapWindow`（会话锁定）→ `ErrAuthLocked`，终态，不可再授权；
  3. 挑战号已被消费 → `ErrChallengeReused`；
  4. 挑战号不符 → 计一次失败，报 `ErrChallengeMismatch`；
  5. 策略要求双向认证但缺目标侧应答 → 计一次失败，报 `ErrMutualAuthRequired`，会话停在 `pending`；
  6. 全部通过 → 消费挑战号，状态推进为 `active`。
  失败计数达到 `ChapWindow` 即锁定，锁定是不可逆终态。

## LUN 授权判定顺序（MapLun）

1. 会话不存在（含已退出）→ `ErrUnknownSession`；
2. 非 `active` → `ErrNotAuthenticated`；
3. 发起者归一化后不等于会话属主 → `ErrInitiatorMismatch`；
4. 该 LUN 已映射 → 幂等返回，不重复计数；
5. LUN 不在会话登录时快照的掩码内 → `ErrLunDenied`；
6. 已达 `MaxLunsPerSession` → `ErrLunLimit`；
7. 通过 → 计入会话 LUN 集合并返回授权（含登录时的掩码代数）。

掩码收窄只影响之后登录的会话：已建立会话在重登前仍按登录时快照的旧掩码判定，
重登（新会话）按新掩码。发起者 IQN 一律归一化后匹配掩码，不按原始报文串比较。

## 命令水位与重放（Recover）

- 仅 `active` 会话可恢复；`pending` 报 `ErrNotAuthenticated`，未知会话报 `ErrUnknownSession`。
- `CmdSN == 上次值`：判定为重放，幂等返回，窗口只推进过一次，命令数不重复累加。
- `CmdSN > 上次值`：命令数加一，`ExpCmdSN = CmdSN + 1`。
- `CmdSN < 上次值`：判定发起者重启，先作废旧会话（从会话表删除），再报
  `ErrUnknownSession`；发起者随后走完整重登流程建立新会话，旧会话绝不被复用。

## 重登与旧会话清理的先后关系

CmdSN 回退 → 先作废旧会话 → 发起者重登（新会话按当前掩码快照授权）。
`Logout` 删除会话后，任何 `Recover`/`MapLun`/重复 `Logout` 都报 `ErrUnknownSession`。

## 并发与快照

- 会话表与掩码共用一把互斥锁：登录配额检查与建会话、掩码换代与授权判定都在同一
  临界区内完成，并发下同发起者只放行到上限，授权不会看到半新半旧的掩码。
- 快照保存全部会话状态、掩码、掩码代数与命令水位；`Restore` 先完整校验
  （版本、上限、状态合法性），失败报 `ErrBadSnapshot` 且不动现网，成功才整体替换；
  同名会话的水位取较大者，只能前进不能回退。会话数、每会话 LUN 数、掩码条目数
  在登录、映射、设掩码、恢复四条路径上都是硬上限。
