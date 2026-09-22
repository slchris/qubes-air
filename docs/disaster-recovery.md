# 灾难恢复与备份

本文描述控制台的单点故障域、加密备份的创建与恢复流程，以及 CA 丢失时的处置。
实现见 `console/backend/internal/backup` 与 `console/backend/cmd/qubes-air-backup`。

## 故障域

控制台把下列状态放在**同一个 SQLite 数据库**里：

| 状态 | 表 / 位置 | 丢失后果 |
|---|---|---|
| qube 清单与 provider 资源身份 | `qubes`、`qube_infra` | 无法对账已有 VM/卷，可能重复创建 |
| zone 与凭据引用 | `zones` | 无法连接 provider |
| provider 凭据、**CA 私钥** | `credentials`（密文） | 无法签发/续期证书；provider 不可达 |
| 证书注册表与吊销状态 | `agent_certs` | 吊销状态丢失 |
| 一次性 bootstrap token 哈希 | `bootstrap_tokens` | 未兑换 token 失效（可接受） |
| 编排审计轨迹 | `jobs` | 审计历史丢失 |

凭据与 CA 私钥在库内是**密文**，密钥来自 keyring（`security.encryption_key` 或
`security.encryption_keys` / `QUBES_AIR_ENCRYPTION_KEYS`），**不在数据库里**。因此：

> 只备份数据库、不备份 keyring 密钥 = 恢复后凭据与 CA 私钥全部无法解密。

备份归档本身再用**独立口令**加密（与 keyring 无关），所以归档文件即使泄露也无法直接使用。

## 备份

前置：控制台可在运行中备份（使用 SQLite `VACUUM INTO` 取一致快照，含 WAL 内容）。

```bash
export QUBES_AIR_BACKUP_PASSPHRASE='...'      # 从环境变量读，绝不放在命令行
qubes-air-backup create \
  -db /rw/config/qubesair/qubes-air.db \
  -out /secure/offhost/qubesair-$(date +%Y%m%dT%H%M%S).qab
```

要点：

- 口令通过 `QUBES_AIR_BACKUP_PASSPHRASE` 传入；命令行参数会被同机其它进程通过 `ps` 看到。
- 输出用 `O_EXCL` 创建，不会静默覆盖已有备份；权限 `0600`。
- 归档是 SQLite 快照的 AES-256-GCM 密文，密钥由 scrypt(passphrase, salt) 派生。

**同时备份（分开存放）**：keyring 密钥、`/etc/qubes-air/agent.env` 等部署配置、本仓库的
`qubes-salt-config` 版本号。keyring 密钥必须与归档**分开保存**，避免同一次泄露同时暴露多个
保护层；归档口令也应单独保管。

归档会保留备份时的 per-Qube 数据密钥。随后 purge 删除当前库内的密钥，不会清除这些历史
副本。因此不能仅凭 purge 成功宣称所有盘快照均不可恢复；还需核验相关备份与密钥副本的
保留/销毁策略，见[凭据与密钥销毁](credential-destruction.md)。

## 恢复

恢复会**替换整个数据库**，是破坏性操作：

1. 停止控制台：`systemctl stop qubes-air-console`。
2. 确认目标与来源：归档路径、目标 `-db` 路径、以及确实要覆盖（`-force`）。
3. 恢复：

   ```bash
   export QUBES_AIR_BACKUP_PASSPHRASE='...'
   qubes-air-backup restore \
     -db /rw/config/qubesair/qubes-air.db \
     -in /secure/offhost/qubesair-<stamp>.qab \
     -force
   ```

4. 确保恢复后的进程持有**原来同一个** keyring 密钥（`QUBES_AIR_ENCRYPTION_KEYS`）；换一把
   密钥会让库内凭据与 CA 私钥无法解密。
5. 启动控制台：`systemctl start qubes-air-console`；确认 `/health` 返回 `status: healthy`。
   该检查会真的写入一行探测标记再读回（并单独对编排 dispatcher 的心跳判活）：能发现库文件被删、
   目录/文件只读、磁盘写满，以及 dispatcher 已死不再消费队列。它**不**证明 provider 可达或 agent
   在线（看第 6 步），也不做全库完整性校验（那是 `PRAGMA integrity_check` 的事）。
6. 抽查：列出 zone/qube（`GET /api/v1/zones`、`/qubes`）、对某 qube 触发一次 agent ping
   （`agent_health` 应为 `healthy`）、确认 `agent_certs` 中已有证书仍在其有效期内。

恢复的拒绝条件（都会明确报错而非静默）：

- 归档格式版本不认识 → `ErrBadFormat`；
- 口令错误或归档被篡改（GCM 校验失败）→ `ErrBadPassphrase`；
- 归档 schema **比当前控制台新** → `ErrSchemaTooNew`；
- 目标已存在且未传 `-force` → `ErrTargetExists`（已有文件保持不动）；
- 解密结果不是 SQLite 数据库 → `ErrNotSQLite`。

### schema 版本

数据库文件头用 SQLite 的 `PRAGMA user_version` 记录 schema 版本（当前
`database.SchemaVersion`）。控制台打开**比自身更新**的库时会在启动阶段拒绝，避免旧代码
写入新 schema 不认识的行。恢复同样校验归档版本，因此“新控制台的备份恢复到旧控制台”会被
明确拒绝，而不是产生损坏数据。

## CA 灾难恢复

CA 私钥在 `credentials` 表中，随数据库备份一起被保存（再被归档口令保护）。因此正常路径下，
“恢复数据库 + 恢复 keyring 密钥”即可恢复 CA，已有 agent 证书继续有效，无需重新 bootstrap。

**若 CA 私钥确实丢失且无备份**：无法继续签发或续期；只要原 CA 公共证书与信任配置仍在，
现有证书仍可在有效期内验证。若必须更换 CA，需要协调替换 Console、Relay 和 agent 的
信任材料，不能仅重建数据库中的私钥记录：

1. 生成新的 CA（控制台首次需要签发时会创建；必要时删除 `qubes-air-ca-key` 凭据触发重建）；
2. 对所有 agent 重新走 bootstrap（新的一次性 token）以获取新 CA 签发的证书；
3. 清理 `agent_certs` 中旧 CA 的注册行（新 CA 不会再验证它们）。

这正是必须把归档与 keyring 密钥分开、离机保存的原因：二者其一丢失，都会把上面的重建流程
从“不需要”变成“必须做”。

## 演练要求

在把它当作可用恢复流程之前：

- 至少演练一次 `create → restore` 到**单独的临时路径**，并用 `qubes-air-backup` 之外的方式
  打开验证（见 `internal/backup` 的 round-trip 测试）；
- 记录一次真实恢复的耗时与实际步骤，作为 RTO 的依据；
- 定期确认 keyring 密钥仍可按文档找回（否则恢复出来的库只是一团密文）。
