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
- 定时执行时用 `-out-dir` 而不是 `-out`：归档名由 CLI 生成（`qubesair-<UTC 时间戳>.qab`），
  调度器里不需要 shell 做 `$(date)` 替换。见下文〈调度与留存〉。

**同时备份（分开存放）**：keyring 密钥、`/etc/qubes-air/agent.env` 等部署配置、本仓库的
`qubes-salt-config` 版本号。keyring 密钥必须与归档**分开保存**，避免同一次泄露同时暴露多个
保护层；归档口令也应单独保管。

归档会保留备份时的 per-Qube 数据密钥。随后 purge 删除当前库内的密钥，不会清除这些历史
副本。因此不能仅凭 purge 成功宣称所有盘快照均不可恢复；还需核验相关备份与密钥副本的
保留/销毁策略，见[凭据与密钥销毁](credential-destruction.md)。

## 调度与留存

备份是**两个动作，必须成对**：`create` 产出一份新归档，`prune` 把归档总数收敛到保留上限
（`console/backend/internal/backup/retention.go:74` 的 `Prune`，CLI 在
`console/backend/cmd/qubes-air-backup/main.go:136` 的 `runPrune`）。只做前者会让存放点悄悄写满，
只做后者会删到没有可恢复的副本，所以下面把它们放进同一个 unit 的两次 `ExecStart`。

### systemd unit 与 timer（文本要落在 `qubes-salt-config`，不在本仓库）

控制台是 Qubes AppVM 里的 systemd 服务，由外部仓库
[qubes-salt-config](https://github.com/slchris/qubes-salt-config) 的 `salt/qubesair` 状态部署
（见[架构](architecture.md)第 23 行）。本仓库**不复制第二套 Qubes 侧部署入口**（`AGENTS.md` 第 10 行），
因此下面两个单元是**要加到那个仓库的文本**，本仓库里没有对应文件；文件路径与属主按该仓库
`salt/qubesair` 的现有约定放置。

`qubes-air-backup.service`：

```ini
[Unit]
Description=Qubes Air console backup (snapshot -> encrypt -> prune)
After=qubes-air-console.service
# 归档必须落在离机介质上：挂载点不在时宁可让这次备份失败，也不要只留本机副本。
RequiresMountsFor=/secure/offhost

[Service]
Type=oneshot
# 0600，内容只有一行 QUBES_AIR_BACKUP_PASSPHRASE=...（下面给出创建命令）。
EnvironmentFile=/etc/qubes-air/backup.env
# 与 console 服务相同的用户；console 若以非 root 运行，这里与口令文件属主要一起调整。
User=root
# 归档名由 CLI 生成：unit 里没有 shell，systemd 的 unit specifier 也没有日期/时间项。
ExecStart=/usr/bin/qubes-air-backup create -db /rw/config/qubesair/qubes-air.db -out-dir /secure/offhost
ExecStart=/usr/bin/qubes-air-backup prune -dir /secure/offhost -keep 14
```

`qubes-air-backup.timer`：

```ini
[Unit]
Description=Daily Qubes Air console backup

[Timer]
OnCalendar=*-*-* 03:30:00
# 关机/休眠期间错过的触发，开机后补跑一次；否则"每天备份"会随机器状态静默断档。
Persistent=true
Unit=qubes-air-backup.service

[Install]
WantedBy=timers.target
```

口令文件（一次性创建，口令另存离机介质——它丢了归档就解不开）：

```bash
install -m 0600 -o root -g root /dev/null /etc/qubes-air/backup.env
# 只跑一次：再次执行会替换口令，而用它写出的旧归档只能用旧口令解开。
printf 'QUBES_AIR_BACKUP_PASSPHRASE=%s\n' "$(head -c 32 /dev/urandom | base64)" \
  > /etc/qubes-air/backup.env
```

启用与核对（由 Salt 状态执行，以下是人工核对命令）：

```bash
systemctl enable --now qubes-air-backup.timer
systemctl list-timers qubes-air-backup.timer     # NEXT 应为下一个 03:30
systemctl start qubes-air-backup.service         # 先手动跑一次，不要等到明天
journalctl -u qubes-air-backup -n 50             # 应同时看到 create 与 prune 的输出
ls -lt /secure/offhost/*.qab | head              # 归档数量与 mtime
```

要点：

- **`-out-dir` 而不是 `-out`**：`ExecStart` 不经 shell，`$(date …)` 不会被展开，而 systemd 的 unit
  specifier 里**没有**日期/时间项（`%Y` 是"unit 文件所在目录"，见 systemd.unit(5) 的 Specifiers 表），
  所以时间戳名字由 `create` 自己生成：`qubesair-<UTC 时间戳>.qab`。同一秒内跑第二次会因 `O_EXCL`
  明确失败，而不是覆盖刚写好的归档。
- 一个 unit 里的多个 `ExecStart=` 按书写顺序执行：`create` 失败时 `prune` 不会运行，
  因此不会出现"没备份成功却把旧归档删了"。
- 口令只经 `EnvironmentFile` 进进程环境：不进 argv（`ps` 可见），也不写在 unit 文件里。
- `User=` 与 `RequiresMountsFor=` 是**待按部署核对的项**：以 `qubes-salt-config` 中 console 服务的
  实际用户和离机介质挂载点为准；两者写错会表现为备份读不到库、或读不到挂载点而直接失败
  （失败比"只留本机副本"好，但要在启用后按上面的 `journalctl` 确认一次）。

### 保留策略建议值：每日一次，留最近 14 份

- **窗口**：14 份 × 每天 1 次 = 两周回溯。要覆盖的不是"昨天写坏了"（当天就会发现），而是
  purge、误删、schema 迁移之后**过了一周多才发现的错误**，以及"归档从某天开始就一直失败/损坏"。
  归档是 `VACUUM INTO` 的完整快照（`internal/backup/backup.go:100-101`），体量与数据库同阶，
  14 份的磁盘代价可以忽略；真正的成本在**解密能力**，所以留多少由密钥寿命决定，不是由磁盘决定。
- **为什么不是留更多**：归档有两层保护，两层都会过期。
  1. 外层是 `QUBES_AIR_BACKUP_PASSPHRASE` 派生的密钥（scrypt + AES-256-GCM，
     `internal/backup/backup.go:207`）；口令**不在归档里**，恢复只接受一把口令
     （`internal/backup/backup.go:132`），所以换了口令之后，旧归档只有拿旧口令才解得开。
  2. 内层是库内的 provider 凭据与 CA 私钥，用 keyring 密钥加密，且每条 `credentials` 行记录
     加密它的 `key_version`（`internal/keyring/keyring.go:7`、`internal/database/database.go:541`）。
     `cmd/rotate-key` 把现有行重加密到新版本后才会丢掉旧密钥，而它的注释写明：
     任何行仍引用某版本时**不得**删除该版本（`cmd/rotate-key/main.go:25`）。
  3. 于是"这份归档还能不能用"= 旧口令还在 **且** 归档内各行引用的 keyring 版本还在。
     **保留窗口必须同时不超过这两样东西的可找回期限**：轮换时就调小 `-keep` 并把旧归档
     一并清掉，或把新旧两把口令/密钥都留到旧归档过期。留着一堆解不开的归档，等于把"有 14 份备份"
     变成"有 14 个占位文件"——而它只会在真正要恢复的那天才暴露。
- **更长留存（可选）**：要留超过一个季度，不要直接把 `-keep` 调到 90（那等于被动要求口令与旧
  keyring 版本都保留 3 个月），而是月末单独留一份并把**对应的那一代口令/keyring 密钥**一起归档；
  本期只落地每日 14 份，长期归档按需手工做。
- 这条策略正是[凭据与密钥销毁](credential-destruction.md)第 43 行要求的"历史数据库备份保留政策"
  在备份侧的可执行形式：purge 之后要声称数据不可恢复，就必须同时核对归档与密钥副本的保留期。

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

### 定期确认最新归档可恢复（轻量：建议每周一次，且每次轮换口令或 keyring 后必做）

上面第一条是**首次**验收；而"备份能恢复"是会随时间失效的性质——口令被换掉、归档内引用的
keyring 版本被清掉、某天的归档写到一半就损坏——所以它需要重复，而且重复的成本很低：

```bash
# 1. 复制一份最新的归档到临时目录；restore 会替换目标库，绝不能指向生产库。
d="$(mktemp -d)"
cp /secure/offhost/qubesair-<最新时间戳>.qab "$d/"
# 2. 用产生这份归档的那一代口令（轮换过就用旧的）
export QUBES_AIR_BACKUP_PASSPHRASE='...'
qubes-air-backup restore -db "$d/qubes-air.db" -in "$d/qubesair-<最新时间戳>.qab"
# 3. 用 qubes-air-backup 之外的方式打开：能解密只证明外层口令对，
#    库是否完整、表是否读得出来，要真的查一次。
sqlite3 "$d/qubes-air.db" 'PRAGMA integrity_check; PRAGMA user_version; SELECT COUNT(*) FROM zones;'
rm -rf "$d"
```

- 第 2 步成功只说明**外层口令**正确（口令错会明确报 `ErrBadPassphrase`，见上文"恢复的拒绝条件"）；
  第 3 步 `integrity_check` 返回 `ok`、`user_version` 等于当前 `database.SchemaVersion`，
  才说明归档内容是完整可用的库。
- **这仍不证明库内凭据与 CA 私钥能解密**：它们用 keyring 密钥加密、不在归档里。要覆盖这一层，
  按[生产可用性缺口清单](production-readiness-gaps.md)的 M1-4 做离机 + 真实 keyring 的完整演练，
  并在恢复后的控制台上抽查 zone/qube 与一次 agent ping（即上文"恢复"第 4-6 步）。
  **频率建议**：轻量检查每周一次、每次口令或 keyring 轮换后必做；带真实 keyring 的完整演练
  每季度一次，并记录耗时（RTO 依据）。
- 轻量检查需要读到归档口令，所以在持有口令的机器上执行；临时目录事后删除，不要把恢复出来的库
  留在归档目录里。
