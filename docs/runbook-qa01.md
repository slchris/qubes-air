# Proxmox 真机回归与记录（QA-01）

本页给真机执行者使用：把已提交源码、构建 digest 和环境版本绑定，跑完
provision → healthy → suspend → resume → release → purge，并覆盖 Exec/FileCopy、
数据盘迁移和吊销状态。每次执行的记录写到 `docs/reviews/`，本页不保存结果。

前置：SEC-01～03、REL-01～02 的代码已提交且 `make pre-commit`、`make audit` 通过；
本页不替代[可靠性契约](reliability-design.md)与[安全控制](security-controls.md)。

## 1. 绑定与环境快照

不要在未提交工作区上做验收：

```bash
git status --porcelain        # 应只剩个人笔记等未跟踪文件，或完全干净
git rev-parse HEAD            # 记录 revision
```

构建并记录 console digest：

```bash
mkdir -p /tmp/qa01 && cd console/backend
go build -o /tmp/qa01/qubes-air-console ./cmd/server
shasum -a 256 /tmp/qa01/qubes-air-console    # Linux: sha256sum
go version -m /tmp/qa01/qubes-air-console | head
```

agent 包用 `make release-agent VERSION=<version>` 发布，记录输出的
`QUBES_AIR_AGENT_PACKAGE_URL`、`_SHA256`、`_VERSION`，并确认与 console 配置一致。

写入记录的绑定表：

| 项 | 值 |
|---|---|
| 仓库 revision | |
| console digest | |
| agent deb version + sha256 | |
| 模板名称 / VMID / 内容摘要 | |
| PVE 版本（`pveversion -v`） | |
| Qubes / Relay / dom0 policy 版本 | |
| Zone / 节点 / datastore | |
| 是否存在未迁移的旧加密盘 | 是 / 否 |

## 2. 前置配置核验

2026-09-22 的真机回归暴露过以下缺口，执行前逐项确认：

- console 必须配置 `QUBES_AIR_PROXMOX_SSH_KNOWN_HOSTS_FILE`（SEC-03 起 provisioning
  必需），内容为集群节点的**带外核对过**的主机键；salt 默认指到
  `<data_dir>/ssh/pve_known_hosts`。
- dom0 必须已应用 `mgmt.remotevm.register`（服务 + policy），否则注册静默失败、release
  与 purge 会在注销 RemoteVM 时失败。
- `QUBESAIR_REVOCATION_URL` 必须从 guest 可达（可用临时 LAN 转发，QA 后撤销）。
- `agent.env` 的 `QUBESAIR_ALLOW` 至少含 Ping；本轮要验证 Exec/FileCopy；
  若存在旧加密盘，必须同时包含 `qubesair.UnlockData` 和 `qubesair.RekeyData`。
- provider credential、Proxmox CA 按[安全控制](security-controls.md)配置。

## 3. 生命周期回归

按 [RemoteVM runbook](runbook-remotevm.md) §3 与 §8 操作，逐项记录 job 结果：

1. provision（加密盘）：create → job succeeded → 状态 running → agent healthy → Ping 通。
2. suspend：compute 删除，storage holder 与数据盘保留，`qube_infra` 保留 VMID。
3. resume：挂回同一数据盘，写入的测试文件仍在；agent healthy；endpoint 更新。
4. release：compute 销毁，盘保留，状态 released。
5. purge（请求体确认 Qube 名）：核验 compute、holder、数据盘、当前 snippet 均消失，
   证书撤销，RemoteVM/端点清理完成，状态 purged。
6. 每一步都从 provider 侧复核，不只信 API 返回。

## 4. Exec / FileCopy 回归

每个 case 记录命令、退出码和 stdout/stderr 摘要：

- Exec 正例：允许的程序成功，argv 字面量（含空格、分号）不被解释为 shell 语法。
- Exec 负例：未允许的程序、shell 文本、超量参数、超时、非零退出码分别失败。
- FileCopy：push/pull 成功；目录不存在、越界路径、符号链接、超量输入被拒绝；
  超量 push 不覆盖原文件。

协议与限制见[安全控制](security-controls.md)，命令示例见
[runbook §7](runbook-remotevm.md)。

## 5. 数据盘迁移核验（DATA-01）

仅当环境里有 per-qube DEK 之前创建的加密盘时需要：

- console 日志应出现该 Qube 的 `migrated ... removed the legacy keyslot`；
- 远端 `cryptsetup luksDump <dev>` 只剩一个 keyslot，用旧派生键执行
  `cryptsetup open --test-passphrase` 应失败；
- 日志报告旧 keyslot 未能移除时，记录迁移标记仍在并在下次 resume 复测；
- master 缺失导致迁移失败记录为 blocker，不重建 master；
- 迁移完成后再走 purge，确认 crypto-shred 路径。

## 6. 吊销与秘密检查

- purge 后 `GET /pki/revocations` 含该 agent 指纹，被撤销身份的调用失败；
- job log 与 console 日志不含 LUKS 密钥、API token、私钥或 provider secret；
- `gitleaks detect` 没有新增真实凭据命中。

## 7. 记录模板

复制到 `docs/reviews/<日期>-qa01-<环境>.md` 并填实际命令与原始输出摘要；未通过项写清阻塞
原因，不要把部分失败写成“全部通过”。

```md
# <日期> QA-01 Proxmox 回归记录

## 绑定与环境
（§1 表）

## 结果
| 步骤 | 入口/命令 | 预期 | 实际 | 结果 |
|---|---|---|---|---|
| provision | | | | |
| suspend | | | | |
| resume | | | | |
| release | | | | |
| purge | | | | |
| Exec 正例/负例 | | | | |
| FileCopy 正例/负例 | | | | |
| 数据盘迁移 | | | | |
| 吊销状态 | | | | |

## 失败与重试
（原始错误、修复、重跑结果）

## 未覆盖与 blocker
```

## 8. 失败注入（推荐）

- 错误 CA、错误 known_hosts、被撤销的 agent 证书必须拒绝连接或调用；
- Exec 取消与超时返回可识别错误，不当作成功；
- purge 中途失败后只允许重试 purge，不允许 Start；
- Console 重启后未完成 job 变 unknown，按[可靠性契约](reliability-design.md)对账。

OPS-01 的离机恢复与 CA 演练见[灾难恢复](disaster-recovery.md)；单机预演记录见
[本地隔离恢复](reviews/2026-09-21-ops01-restore.md)。完成后更新 [TODO](TODO.md) 与
[路线图](roadmap-to-production.md)。
