# 凭据、密钥与轮换

## 当前存储模型

| 材料 | 当前保存位置 | 说明 |
|---|---|---|
| Provider API 凭据 | console 加密 credential store | 运行 OpenTofu 时按需解密并注入进程环境，不写 tfvars |
| Console CA 与 LUKS master | console 加密 credential store | CA 私钥和 master 不离开 console |
| Console credential 加密密钥 | console 部署 secret | 32 字节 AES-256 key；支持多版本轮换 |
| Agent 私钥 | remote guest | guest 生成，只提交 CSR |
| Relay 私钥 | Relay `/rw` | Relay 生成，经 qrexec 提交 CSR |
| State passphrase | 无网络 vault | `tf-with-passphrase.sh` 经受控 qrexec 读取 |
| age/SOPS 私钥 | 无网络 vault | 只用于仍采用 SOPS 的离线配置材料 |

Console 编排从自己的 AES-GCM credential store 读取 provider secret。离线 vault 保存 state
passphrase、恢复材料和人工备份。

## 红线

- 私钥、token、passphrase 和真实 credential 不进 Git；
- Provider credential 不作为 Terraform variable，避免写进 state；
- Relay/agent 私钥在持有方生成，不通过 vault 或 console 分发；
- `docker-compose.yml` 的 token/key 只用于本地开发；
- console 数据库备份必须连同加密 key 的恢复策略一起设计，但两者不要放在同一未加密位置；
- 日志和 job output 不打印 secret 或派生的 LUKS passphrase。

## Console credential store

Credential secret 使用 AES-256-GCM 加密，每行记录 `key_version`。列表和普通 GET 不返回
secret；只有编排和内部签发流程通过 `GetSecret` 读取。

生产环境至少设置：

```bash
QUBES_AIR_API_TOKEN=<random-token>
QUBES_AIR_ENCRYPTION_KEY=<exactly-32-byte-key>
QUBES_AIR_CORS_ORIGINS=https://<console-origin>
```

更推荐使用版本化 keyring：

```bash
QUBES_AIR_ENCRYPTION_KEYS='v1:<32-byte-key>'
```

配置错误会在启动时失败。不要依赖内置开发 key，也不要在真实环境把 API token 留空。

## Provider credential

通过 UI/API 创建 credential，再让 Zone 的 `credential_id` 引用它。当前根模块对 Proxmox 和
GCP 各只有一个 provider 实例；同类 provider 配置多个带 credential 的 Zone 会明确失败，
不会随机选择。

运行 job 时：

- Proxmox token 注入 `PROXMOX_VE_API_TOKEN`；
- GCP service-account JSON 注入 `GOOGLE_CREDENTIALS`；
- Proxmox snippet 上传所需 SSH key 从受限文件读取并注入子进程，不写入 tfvars；
- job 结束后 secret 不应保留在生成的 Terraform 配置里。

优先使用可单独吊销、最小权限、每环境独立的 token。

## Console 加密密钥轮换

轮换必须经历“同时配置旧/新 key → 原子重加密 → 验证 → 删除旧 key”四步。

1. 生成新的 32 字节 key，并同时配置：

   ```bash
   QUBES_AIR_ENCRYPTION_KEYS='v1:<old-32-byte-key>,v2:<new-32-byte-key>'
   ```

   最高版本自动成为 primary；新写入使用 v2，旧行仍可用 v1 解密。

2. 用与服务相同的环境和数据库运行：

   ```bash
   cd console/backend
   go run ./cmd/rotate-key -config /path/to/config.yaml
   ```

   重加密在一个事务内完成，可重复执行。

3. 验证所有行都在新版本：

   ```bash
   go run ./cmd/rotate-key -config /path/to/config.yaml -verify
   ```

4. 只有旧版本计数为 0 后，才从部署 secret 中删除 v1。

任何旧行仍引用 v1 时删除旧 key，都会永久失去解密能力。

## Relay 证书

Relay 证书不走通用 credential 下载：Relay 本地生成 key/CSR，通过受 dom0 policy 约束的
`qubesair.IssueRelayCert` 请求 console 签发。Timer 在到期前续期。检查重点是私钥 owner/mode、
caller 到 console 的 policy，以及 console 是否把 CN 钉死为真实 caller。

## Agent 证书

首次证书由单次 token + CSR 获得；后续在现有 mTLS 身份下续期。Console 保存签发记录和 CA，
不保存 agent 私钥。不要用复制 identity directory 的方式修复某台 VM。

## State passphrase

多台 Qubes 主机共享同一远程 state 时，passphrase 必须相同，但 backend 登录身份应每台主机
独立、可单独吊销。日常使用 `make tf-secure ...`，详见
[terraform-state.md](terraform-state.md)。

## 备份与恢复

至少分别备份：

- console 数据库；
- 当前及仍被引用的旧 encryption key version；
- console CA 恢复材料；
- `qubes-air-luks-master`；
- OpenTofu state passphrase 和 backend 凭据。

丢失 `qubes-air-luks-master` 会让所有由它派生密钥的加密数据盘不可恢复。删除前先按
[凭据销毁流程](credential-destruction.md)确认影响范围。
