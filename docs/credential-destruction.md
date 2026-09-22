# 凭据与密钥销毁

云 SSD、快照和底层复制使覆写不可靠。退役应同时确认云资源、身份与解密能力的最终状态。
本页描述当前代码行为，不代表完成了所有备份和部分失败场景的现场演练。

## 当前密钥模型

新 Qube 使用独立随机 256-bit DEK，存放在控制台加密凭据库的
`qubes-air-luks-key-<id>` 中，通过 agent mTLS 用于数据盘操作。purge 会删除当前库内的该密钥。

旧盘不再静默使用 master 派生密钥解锁：首次解锁时 Console 取已有 `qubes-air-luks-master`
派生出旧键，把容器原子重加密到该 Qube 的 DEK（失败则保留旧键可用并重试），此后 master
与该盘无关。在迁移完成前，该盘仍可被 master 打开，删除当前库内 DEK 不构成 crypto-shred；
`qubes-air-luks-master` 只读、只用于迁移，可在确认没有未迁移盘后删除，且不会自动重建。

数据库归档可能含有 DEK 或 master 的历史副本。删除当前库内记录不会清除这些副本，也不等于
安全擦除了 SQLite/WAL 或存储快照中的历史内容。不可恢复性必须同时考虑所有密钥副本与备份。
`dom0-scripts/decommission-zone.sh --shred-luks-key` 也不能作为现行单 Qube 销毁的独立凭据。

## 单个 Qube 退役

1. 确认目标 Qube 名称、ID、provider 资源、数据盘、RemoteVM 和证书；决定数据保留范围。
2. 如需保留数据，先备份并验证恢复；记录保留备份意味着仍保留相应解密能力。
3. release/suspend 只删除 compute、保留数据盘。彻底销毁使用
   `POST /api/v1/qubes/{id}/purge`，需要 control scope 和请求体 `{"confirm":"<qube 名>"}`。
4. purge 接受 released/suspended/stopped/error，先原子记录不可逆意图、撤销证书和 bootstrap token（与 claim 同一写事务），
   再入队 destroy job；解除保护、删除当前库内数据密钥是这个 job 的第一步，**入队被拒时不会执行**（磁盘保护未解除、DEK 未删、无 provider 调用，
   但意图与授权撤销已生效，只能重试 purge）；正常完成后 Qube 保留 `purged` 记录并清理端点/RemoteVM。若该盘尚未迁移到
   独立 DEK，删除库内记录不会让保留副本不可恢复，需先迁移或明确接受这一点。
5. 检查 job log 和 provider：分别核验 compute、storage holder、数据盘、证书和 RemoteVM，
   按保留政策处置已知快照与密钥备份。

流程可能部分失败，不能只看 API 接受请求。失败后保留 purge 意图，禁止重新启动；
失败矩阵和显式重试步骤见[生命周期可靠性](reliability-design.md)。重试前先查明已完成步骤；尤其是密钥
已删除但 provider 销毁失败时，不应假定资源已经清空或数据仍可恢复。已 purged 的重复请求
按幂等处理。完整恢复与销毁演练仍是[路线图](roadmap-to-production.md)中的待办。

## Zone 退役

1. 明确 Zone 及其 Qube 的数据保留计划，用有效 provider 凭据完成资源核验与删除。
2. 吊销该 Zone 的 provider API token/service-account key，再删除 console credential 与离线副本。
3. 清理 Zone、Infrastructure、RemoteVM 和对应 policy/tag。
4. 仅在 Relay 专属于该 Zone 时撤销其证书并删除 identity，避免影响共享 Relay。
5. 核验历史数据库备份、快照与密钥副本的保留政策。

若凭据疑似泄露，应优先吊销或隔离，再使用可信的新凭据完成清理。

## 控制台或整机事件

当 Qubes 主机丢失或 console 可能被攻破：从可信设备吊销相关 provider 凭据，隔离 console，
评估并撤销 Relay/agent 信任，轮换 API token 和凭据加密材料。评估泄露范围应覆盖 CA、独立
DEK 和派生 master；仅轮换包装密钥不能撤回攻击者已经复制的数据密钥。

CA 处置与恢复见[灾难恢复](disaster-recovery.md)。恢复后不要复用可疑主机上的私钥，也不要
把旧备份中的证书吊销状态直接当作事件后的最新状态。

## 高价值密钥的影响

| 材料 | 丢失或销毁的影响（假设无其他副本） |
|---|---|
| Provider token | 对应 API 访问需更换凭据；让泄露副本失效必须在 provider 侧吊销 |
| Console encryption key | 仅由该版本加密的 credential 行无法解密 |
| Console CA private key | 无法继续签发/续期；已有证书仍可用原公共 CA 在有效期内验证 |
| Per-Qube DEK | 对应加密数据盘无法解锁 |
| `qubes-air-luks-master` | 未迁移的旧盘无法解锁/迁移；已迁移的盘不受影响 |
| Relay / agent private key | 对应身份无法使用，需要受控重建或重新 bootstrap |

执行前明确确认目标、备份保留政策与依赖范围。根密钥销毁不应由模糊的 Zone 操作自动完成。
