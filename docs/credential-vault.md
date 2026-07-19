# 凭据隔离下发 + 密钥轮换 (阶段3, 真机执行)

> 目标: 云 API 凭据 / relay 传输凭据 / age 私钥集中放无网络的 `vault-cloud`,
> 控制台 (`mgmt-air`) **不持有明文**, 用时经 qrexec 向 vault 要、用完即弃; 并给出
> 控制台加密密钥、age/WireGuard/传输凭据的轮换流程。
>
> 平面分离铁律 (同阶段2): **dom0 永远离线且是唯一授权决策点**; 编排在 `mgmt-air`;
> vault-cloud 无网络。
>
> **[TODO] relay 传输凭据口径**: 传输层目标已改为 **gRPC 双向流**, relay 出站建连的认证凭据将是
> **gRPC 通道的 mTLS 客户端证书/私钥**(仍存 vault-cloud、经 qrexec 用、不出 vault)。本文下述
> `split-ssh` / `autossh` 的 SSH 私钥流程是**现有骨架**(过渡参考); gRPC 版凭据与轮换流程待实现,
> 见 [roadmap-to-production.md](roadmap-to-production.md)。凭据隔离红线不变。

## 0. 角色与凭据红线

评审确立的红线 (违反算回归):

- 私钥永不进 git、永不进 pillar 明文、永不上云、永不进控制台明文长期存储。
- 控制台 (`mgmt-air`) 不持有云凭据明文 —— 用时经 qrexec 向无网络的 `vault-cloud` 要, 用完即弃。
- 云 KMS 不用来"防云厂商"; 要防云厂商的数据, 密钥留本地 vault。

凭据清单与流向:

| 凭据 | 存哪 | 谁用 | 下发方式 |
|---|---|---|---|
| 云 API key (Proxmox token / GCP SA / AWS IAM) | vault-cloud 文件 | terraform provision (在 mgmt-air) | `qubesair.GetCredential` ask -> 注入环境变量, 进程结束即失效 |
| relay 传输凭据 ([TODO] gRPC mTLS 证书; 现为 SSH 私钥 split-ssh 骨架) | vault-cloud `~/.ssh` (split-ssh) | sys-relay 经 agent socket 用 | `qubes.SshAgent` (私钥不出 vault) |
| age/SOPS 私钥 | vault-cloud / KEY_DIR | 解密 salt pillar | `qubesair.GetCredential+age-key` 或本地 SOPS |
| 控制台加密密钥 (AES-256) | mgmt-air 控制台 config | 控制台进程 | 环境变量 `QUBES_AIR_ENCRYPTION_KEY(S)` |

## 1. 创建 vault-cloud (dom0)

```bash
# 在 dom0
sudo bash dom0-scripts/create-vault-cloud.sh --name vault-cloud --template fedora-42
# 关键结果: netvm='' (无网络), tag=vault-cloud, label=black
qvm-prefs vault-cloud netvm      # 应为空
qvm-tags  vault-cloud list       # 应含 vault-cloud
```

## 2. 部署 policy (dom0)

policy 单一来源 = `dom0-scripts/policy.d/30-qubes-air.policy` (阶段3 已追加 E 段)。

> **目前只能手动复制。** 原先那条 `state.sls qubes-air.remotevm.dom0` 所在的骨架**已删除**
> (见 [salt/qubes-air/README.md](../salt/qubes-air/README.md)),而接替它的
> `qubes-salt-config` 里 **没有**部署本文件的 state:`mgmt.remotevm.policy` 写的是另一个文件
> (`/etc/qubes/policy.d/30-remotevm.policy`,从 `config.jinja` 渲染 RemoteVM 服务规则),
> 整个仓库不含下面 E 段的 vault-cloud 规则。补 state 之前,手动复制是唯一途径。

```bash
# 在 dom0
sudo cp dom0-scripts/policy.d/30-qubes-air.policy /etc/qubes/policy.d/30-qubes-air.policy

# 校验 E 段存在
grep -n 'qubesair.GetCredential\|qubes.SshAgent\|@tag:vault-cloud' /etc/qubes/policy.d/30-qubes-air.policy
```

E 段关键行 (5 列新格式, ask 原则):
```
qubesair.GetCredential  *   mgmt-air     @tag:vault-cloud   ask default_target=vault-cloud
qubes.SshAgent          *   @tag:relay   @tag:vault-cloud   ask default_target=vault-cloud
*                       *   @anyvm       @tag:vault-cloud   deny
```

## 3. 配置 vault-cloud (salt)

```bash
# 3a. 先对 vault 模板装 socat (进模板才持久)
qubesctl --skip-dom0 --targets fedora-42 state.sls qubes-air.vault-cloud
# 3b. 再对 vault-cloud AppVM 部署服务/目录/agent
qubesctl --skip-dom0 --targets vault-cloud state.sls qubes-air.vault-cloud
# 3c. 首次 bind-dirs 生效需重启 vault-cloud 一次
qvm-shutdown --wait vault-cloud && qvm-start vault-cloud
```

部署后 vault-cloud 内应有:
- `/etc/qubes-rpc/qubesair.GetCredential` (经 bind-dirs, 0755)
- `/etc/qubes-rpc/qubes.SshAgent` (经 bind-dirs, 0755)
- `~/.qubes-air/credentials/` (0700)

## 4. 往 vault-cloud 存凭据 (在 vault-cloud 内)

```bash
# 在 vault-cloud 终端
mkdir -p ~/.qubes-air/credentials && chmod 700 ~/.qubes-air/credentials

# 云 API 凭据 (示例: Proxmox token) —— 文件名即凭据名, 权限 600
printf '%s' 'PVEAPIToken=user@pam!token=xxxxxxxx' > ~/.qubes-air/credentials/proxmox-token
chmod 600 ~/.qubes-air/credentials/proxmox-token

# GCP service account (多行 JSON 也可, cat 原样回传)
cp /path/to/sa.json ~/.qubes-air/credentials/gcp-sa.json
chmod 600 ~/.qubes-air/credentials/gcp-sa.json

# relay transport SSH 私钥 (split-ssh: 私钥留此, agent 持有)
cp /path/to/relay_transport ~/.ssh/relay_transport
chmod 600 ~/.ssh/relay_transport
# 开机由 rc.local -> vault-ssh-agent.sh 自动 ssh-add (或手动):
#   eval "$(cat ~/.ssh-agent-env)"; ssh-add ~/.ssh/relay_transport
```

可选加固: 用 `pass` (password-store + gpg) 作后端, 凭据在磁盘上是 gpg 加密的。
见 `qubesair.GetCredential` 脚本末尾的替换说明。

## 5. 一次 provision 的凭据流 (端到端)

从控制台点"创建"到 terraform 用完凭据消失:

```
[1] 用户在控制台 (mgmt-air) 点"创建 Zone / provision"
      │
[2] mgmt-air 调 qrexec 取凭据:
      qrexec-client-vm vault-cloud qubesair.GetCredential+proxmox-token
      │
[3] dom0 policy 命中 E1 -> 弹窗 ask (人工确认: mgmt-air 取 proxmox-token, 预选 vault-cloud)
      │  (人点"允许一次")
[4] vault-cloud 的 qubesair.GetCredential 校验凭据名白名单 -> cat 文件 -> stdout 回传
      │
[5] mgmt-air 把 stdout 注入【当前 provision 进程】的环境变量, 【不落盘】:
      export PROXMOX_TOKEN="$(qrexec-client-vm vault-cloud qubesair.GetCredential+proxmox-token)"
      terraform apply -var-file=production.tfvars   # 读 PROXMOX_TOKEN
      │
[6] terraform 进程结束 -> 环境变量随进程消失 -> 凭据明文不再存在于 mgmt-air。
```

在 mgmt-air 上的推荐封装 (确保用完即弃, 不写 shell 历史/文件):

```bash
# provision-with-cred.sh (在 mgmt-air; 凭据只存活在子 shell 环境, 结束即失效)
set -euo pipefail
run_terraform() {
  # 用 env 把凭据只注入这一次 terraform 调用的环境, 不 export 到父 shell
  local token
  token="$(qrexec-client-vm vault-cloud qubesair.GetCredential+proxmox-token)"
  env PROXMOX_TOKEN="$token" terraform "$@"
  # 函数返回后 token 局部变量与子进程环境一并消失
}
run_terraform apply -var-file=production.tfvars
```

> 注意: 不要 `echo "$token"`、不要写进文件、不要放进会被 history 记录的命令行位置参数。
> 用 `env VAR=... cmd` 或进程替换, 让凭据只经环境变量进入目标进程。

## 6. relay 用 split-ssh (sys-relay -> vault-cloud)

阶段2 的 autossh 用 SSH 私钥连远端。阶段3 把私钥挪进 vault-cloud, relay 只用 agent socket。

- vault 侧: 私钥在 `vault-cloud ~/.ssh`, agent 由 `vault-ssh-agent.sh` 持有 (见步骤 4)。
- relay 侧: 把 `salt/qubes-air/vault-cloud/files/relay-split-ssh-client.sh` 的内容【追加】到
  `sys-relay-<zone>` 的 `/rw/config/rc.local` (本阶段不改阶段2 autossh 文件, 只提供脚本):

```bash
# 在 sys-relay-<zone> 内, 追加到 /rw/config/rc.local (若尚无该桥接):
#   见 salt/qubes-air/vault-cloud/files/relay-split-ssh-client.sh
# 效果: 建立 ~/.SSH_AGENT_vault-cloud socket, 每次连接经 qrexec 转发到 vault 的 qubes.SshAgent
```

然后让 autossh 用这个 socket (阶段2 autossh 单元的接线点, 由运维在阶段2 文件里加一行环境):
```
Environment=SSH_AUTH_SOCK=/home/user/.SSH_AGENT_vault-cloud
```

调用时 dom0 policy 命中 E2 -> ask。**高频 autossh 场景权衡**: 每次重连都 ask 会打断隧道自愈;
可把 E2 由 `ask` 放宽为 `allow` (源已锁死 @tag:relay、目标 @tag:vault-cloud、私钥仍不出 vault)。
是否放宽由运维按威胁模型定; 默认保持 ask (评审红线倾向)。

## 7. 自检

```bash
# 7a. (在 mgmt-air) 取一个凭据 (会触发 dom0 ask 弹窗):
qrexec-client-vm vault-cloud qubesair.GetCredential+proxmox-token
#   预期: 弹窗确认后, stdout 打印凭据; 拒绝则无输出且非零退出。

# 7b. 非法凭据名被服务端拦截 (即便 policy 放行):
qrexec-client-vm vault-cloud 'qubesair.GetCredential+../etc/passwd'   # 应被服务端 exit 3

# 7c. 未定义来源/服务被 policy 兜底拒绝:
#   从一个未打 tag 的 qube 调 -> 命中 E4 deny。

# 7d. split-ssh: 在 sys-relay 内, SSH_AUTH_SOCK 指向 vault socket 后:
SSH_AUTH_SOCK=/home/user/.SSH_AGENT_vault-cloud ssh-add -l   # 列出 vault 里的 key 指纹, 但取不到私钥
```

---

## 控制台加密密钥轮换 (Go, `cmd/rotate-key`)

### 背景

控制台用单密钥 AES-256-GCM 加密 SQLite `credentials.encrypted_data`。原缺陷:
直接换 `QUBES_AIR_ENCRYPTION_KEY` 会让存量密文 GCM 认证失败、**全部不可用**。

阶段3 修复: 每行记录 `key_version`, 控制台持有【多版本密钥环】(keyring), 用哪版加密就标哪版,
解密按版本选密钥。轮换 = 加新版 -> 设为 primary -> 把旧版行重加密到新版。

### 迁移兼容性 (监工重点)

- credentials 表加 `key_version INTEGER NOT NULL DEFAULT 1` 列。
- 迁移是【向后兼容】的: `database.go` 的 `addColumnIfMissing` 先 `PRAGMA table_info` 查列,
  不存在才 `ALTER TABLE ADD COLUMN`, 幂等; DEFAULT 1 把存量行回填为 version 1 —— 正是当初加密它们的那把 key。
- 已验证: 用【旧 schema】(无 key_version) 的 DB + 存量行, 跑新代码 -> 列被加上、旧行标 v1、旧 secret 仍可解。

### 轮换流程 (无停机, 三步)

```bash
# 前提: 旧 key = OLD_32B, 新 key = NEW_32B (各 32 字节)

# [1] 加新 key, 与旧 key 并存 (v2 为 primary, 新密文用 v2, 旧行仍能用 v1 解):
export QUBES_AIR_ENCRYPTION_KEYS="v1:${OLD_32B},v2:${NEW_32B}"
#     此时可安全重启控制台 (向后兼容: v1 行照常解密, 新建凭据用 v2)。

# [2] 用同一 env 跑轮换命令: 把所有 v1 行解密-重加密为 v2 (单事务, 原子, 可重入):
cd console/backend
go run ./cmd/rotate-key -config /path/to/config.yaml
#     或先看分布: go run ./cmd/rotate-key -config ... -verify

# [3] 确认无旧版本残留后, 丢弃旧 key:
go run ./cmd/rotate-key -config ... -verify   # 应显示所有行都在 v2, 无 "NOT rotated"
export QUBES_AIR_ENCRYPTION_KEYS="v2:${NEW_32B}"   # 或单 key 形式 QUBES_AIR_ENCRYPTION_KEY
#     重启控制台。旧 key 从此不再需要。
```

**切勿**在仍有行引用某旧版本时把它从 keyring 删除 —— 那些行会永久不可解。
`-verify` 就是用来在删旧 key 前确认"旧版本 0 行"。

### 原子性 / 可回滚 (监工重点)

- `RotateToPrimary` 在【单个数据库事务】里, 逐行 用旧版 key 解密 + 用新版 key 加密 + UPDATE。
- 任一行失败 (如 keyring 缺某版本 key、密文损坏) -> 整个事务 rollback, DB 与轮换前【一字节不差】,
  绝无半轮换状态 (已验证: `TestCredential_RotationFailClosedOnMissingKey`)。
- 已是 primary 版本的行被跳过 -> 中途中断后重跑只补完剩余 (幂等/可重入,
  已验证: `TestCredential_RotationIsIdempotent`)。
- 前置条件 (fail-closed): keyring 必须含表中【每个】在用版本的 key, 否则那些行解密失败, 整轮中止。

### 测试

```bash
cd console/backend && go build ./... && go test ./...
# 关键用例:
#   internal/repository  TestCredential_RotationBackwardCompatible   换 key 后旧行可解、新行用新 key
#                        TestCredential_RotationIsIdempotent         重跑无副作用
#                        TestCredential_RotationFailClosedOnMissingKey 缺 key 原子中止不破坏
#                        TestCredential_UpdateReencryptsWithPrimary  改 secret 重标 primary 版本
#   internal/keyring     多版本解析 / 最高版为 primary / 各类错误
#   internal/config      单 key / dev 回退 / 多版本 spec / 校验
```

---

## 本机 vault 密钥轮换 (age / WireGuard / SSH)

见 `crypto/scripts/rotate-keys.sh` 与 `crypto/README.md`。要点: 私钥留本机只出公钥, 有备份、幂等、失败不留半成品。

```bash
bash crypto/scripts/rotate-keys.sh age    # 换 age 并用新公钥重加密 SOPS 文件
bash crypto/scripts/rotate-keys.sh wg     # 换 WireGuard, 出新公钥
bash crypto/scripts/rotate-keys.sh ssh    # 换 relay transport SSH, 出新公钥
DRY_RUN=1 bash crypto/scripts/rotate-keys.sh all
```

---

## 待真机确认 (Mac 无法验证; 已尽量对照官方/社区文档)

- [E1] `mgmt-air` / `vault-cloud` 是本项目约定名; 与实际 qube 名/tag 对齐 (policy E 段 SOURCE/DEST)。
- [E2] `qvm-prefs vault-cloud netvm ''` 清空 netvm 的确切写法随版本可能是 `netvm none` 或 `-D netvm`。
- [E3] bind-dirs 首次生效需重启 vault-cloud (同阶段2 relay 的 [P3])。
- [E4] `qubes.SshAgent` 服务名与脚本内容对照社区 Split SSH 指南
      (forum.qubes-os.org/t/split-ssh/19060); 官方已从旧 doc 迁到此。
- [E5] `qubesair.GetCredential` 的 `$QREXEC_SERVICE_ARGUMENT` / `+ARG` 传参、"参数不含空格斜杠"、
      服务脚本路径 (`/etc/qubes-rpc/<SERVICE>` 模板 / `/usr/local/etc/qubes-rpc/` AppVM):
      对照官方 doc.qubes-os.org/en/latest/developer/services/qrexec.html (已核实)。
- [E6] 高频 autossh 是否把 E2 由 ask 放宽为 allow 属策略权衡 (见步骤 6)。
```
