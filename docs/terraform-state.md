# Terraform / OpenTofu State 管理（多机共享 + 云厂商不可信）

> 本文是"多台 Qubes 笔记本共享同一套远端基础设施"的 state 方案定稿。
> 核心结论：**S3 兼容 backend（原生锁）+ OpenTofu 客户端 PBKDF2 加密 + passphrase 存 vault-cloud**。

## 1. 为什么需要这套方案

- **多机共享** → 必须远程 backend：多台笔记本共用一套远端 Zone，state 必须集中，否则两台机器同时 `apply` 会把 state 写坏。远程 backend 提供**共享 state + 状态锁**。
- **云厂商不可信**（本项目威胁模型红线）→ state 不能让存储运营方看到明文。

这两条本来打架，因为——

### 关键事实：backend 层的服务端加密解决不了"运营方可见"

| 加密手段 | 密钥归谁 | 对"云厂商不可信"是否有效 |
|---|---|---|
| S3 `encrypt=true` / SSE-KMS | AWS | ❌ AWS 能解密 |
| GCS CMEK | Google | ❌ Google 能解密 |
| MinIO SSE | MinIO 运营方 | ❌ 运营方能解密 |
| OpenTofu 客户端加密 + **PBKDF2 passphrase** | **只在本地笔记本** | ✅ 存储方只拿到密文 |
| OpenTofu 客户端加密 + aws_kms/gcp_kms | 云厂商（KEK） | ❌ 回到云厂商手里，禁止用 |

**唯一干净解法 = OpenTofu 客户端 state 加密**：state 在离开笔记本前用 AES-GCM 加密，backend 只存密文。且 key provider 必须是本地 PBKDF2 或自托管 OpenBao。

> ⚠️ **必须用 OpenTofu，不是 HashiCorp Terraform**。客户端 state 加密是 OpenTofu 1.7+ 独有特性，HashiCorp 闭源版没有。OpenTofu 与 Terraform HCL 完全兼容，本仓库的 `.tf` 不用改，命令 `terraform` → `tofu`（Makefile 已用 `TF_BIN` 变量，默认 `tofu`）。

## 2. 文件与职责

| 文件 | 进 git? | 作用 |
|---|---|---|
| `terraform/main.tf` | 是 | 顶部有方案说明；backend/encryption **不写这里** |
| `terraform/backend.tf.example` | 是 | **S3 兼容** backend 模板（AWS S3 / 自托管 MinIO） |
| `terraform/backend-pg.tf.example` | 是 | **pg** backend 模板（自托管 Postgres；场景 A 首选 / MinIO 锁的退路） |
| `terraform/encryption.tf.example` | 是 | OpenTofu 加密块模板（方式 A 参考） |
| `terraform/backend.tf` | **否**（gitignore） | 你的真实 backend（从上面某个 `.example` 复制，二选一） |
| `scripts/tf-with-passphrase.sh` | 是 | 从 vault-cloud 取 passphrase（pg 再取 `PG_CONN_STR`）→ 注入 → 跑 tofu |

### 选哪个 backend

| | S3 兼容 | pg (Postgres) |
|---|---|---|
| **锁** | `use_lockfile`（S3 条件写）— MinIO 上**存疑，需实测** | advisory lock，可靠，会话断即释放 |
| **公有云参与** | 场景 B 是（AWS）；MinIO 否 | **否**（跑你自己的 Proxmox） |
| **适合** | 已有 S3/AWS，或想用对象存储 | 场景 A（自托管），或 MinIO 锁实测不通时 |
| **DB 密码** | 无 | conn_str 含密码，走 vault + `PG_CONN_STR` |

两者的**机密性一样**——都靠 OpenTofu 客户端加密让存储方只看到密文。差异只在锁机制和是否碰公有云。

## 3. 一次性设置

### 3.1 生成并存入 passphrase（每台笔记本都要有同一个）

passphrase 是"团队密钥"——所有笔记本共用同一个才能读写同一份加密 state。它是高价值机密，存进每台笔记本的 `vault-cloud`（阶段3 已建的无网络 qube），复用 `qubesair.GetCredential` 机制：

```bash
# 在第一台笔记本上生成一个强 passphrase（只生成一次，然后安全地带到其他笔记本）
openssl rand -base64 48

# 在每台笔记本的 vault-cloud 里存为命名凭据 tfstate-passphrase
qvm-run -p vault-cloud 'install -m 700 -d ~/.qubes-air/credentials'
printf '%s' '<上面生成的 passphrase>' | \
  qvm-run --pass-io vault-cloud 'cat > ~/.qubes-air/credentials/tfstate-passphrase && chmod 600 ~/.qubes-air/credentials/tfstate-passphrase'
```

> passphrase 在笔记本间的传递本身要走安全信道（如 Split GPG 加密后 qvm-copy，或手动在离线设备上抄写）——**不要**用明文邮件/IM。它一旦泄露，任何人拿到密文 state 就能解密。

### 3.2 配置 backend（S3 兼容 或 pg，二选一）

**S3 兼容：**
```bash
cp terraform/backend.tf.example terraform/backend.tf
# 编辑 backend.tf：填 bucket / region；自托管 MinIO 则取消 endpoints 那几行注释
```

**pg（自托管 Postgres）：**
```bash
cp terraform/backend-pg.tf.example terraform/backend.tf   # 注意也是复制成 backend.tf
# backend.tf 里 conn_str 留空，连接串（含密码）经 PG_CONN_STR 从 vault 注入
```

先在你的 Proxmox 上准备 Postgres（最小权限，每台笔记本一个独立角色）：
```sql
-- 一次性：建库 + 每台笔记本一个角色（各自独立密码，可单独吊销）
CREATE DATABASE tfstate;
CREATE ROLE tf_laptop_a LOGIN PASSWORD '<强密码A>';
CREATE ROLE tf_laptop_b LOGIN PASSWORD '<强密码B>';
-- 在 tfstate 库里：给它们 schema 权限（OpenTofu 会在 schema 内建表）
\c tfstate
CREATE SCHEMA qubes_air;
GRANT USAGE, CREATE ON SCHEMA qubes_air TO tf_laptop_a, tf_laptop_b;
```
Postgres 主机务必：**LUKS 全盘加密**（社区版 Postgres 无内建 TDE）+ 强制 `sslmode=verify-full`（防 MITM）。

把每台笔记本自己的连接串存进它的 vault-cloud（含该机专属密码）：
```bash
qvm-run -p vault-cloud 'install -m 700 -d ~/.qubes-air/credentials'
printf '%s' 'postgres://tf_laptop_a:<强密码A>@pg.home.lan:5432/tfstate?sslmode=verify-full' | \
  qvm-run --pass-io vault-cloud 'cat > ~/.qubes-air/credentials/pg-conn-str && chmod 600 ~/.qubes-air/credentials/pg-conn-str'
```

### 3.3 每台笔记本独立身份（不共享一把万能凭据）

- **AWS S3**：每台笔记本一个独立 IAM 用户 + 最小权限桶策略。
- **自托管 MinIO**：每台笔记本一个独立 MinIO 用户 + 桶策略。
- **pg**：每台笔记本一个独立 Postgres 角色（见上 `tf_laptop_a/b`）+ `sslmode=verify-full`。
- 共同点：身份「每机一份、可单独吊销」，任一台笔记本丢失单独吊销即可，不影响其他机器。凭据都从各自 vault 取，不写进 `backend.tf`。

## 4. 日常使用

用 `tf-secure` 入口（它会从 vault 取 passphrase、注入加密配置、再跑 tofu）：

```bash
# S3 兼容 backend：
make tf-secure ARGS="init"
make tf-secure ARGS="plan  -var-file=environments/dev.tfvars"
make tf-secure ARGS="apply -var-file=environments/dev.tfvars"

# pg backend：加 BACKEND=pg，脚本会额外从 vault 取 PG_CONN_STR 注入
make tf-secure BACKEND=pg ARGS="init"
make tf-secure BACKEND=pg ARGS="apply -var-file=environments/dev.tfvars"

# 存算分离 suspend/resume 也走同一入口
make tf-secure ARGS="destroy -var-file=environments/dev.tfvars -target=module.remote_qubes[\"dev-work\"].module.proxmox[0].proxmox_virtual_environment_vm.compute"
```

每次执行 dom0 会弹 `ask` 确认 mgmt-air 向 vault-cloud 取凭据（pg 会弹两次：passphrase + pg-conn-str）——这是有意的人工可见性。

> 纯本地 HCL 校验不需要 passphrase：`make tf-validate`（用 `-backend=false`，CI 可离线跑）。

## 5. 从明文 state 迁移到加密 backend

如果之前已有本地明文 `terraform.tfstate`：

1. 先在 `encryption` 里临时用 `fallback` 允许读明文（`enforced=false`），跑一次 `tofu init -migrate-state` 把 state 推到远程 backend 并加密。
2. 确认远程 state 可正常 `plan` 后，把 `enforced=true`（拒绝再读明文），删除本地明文副本（`shred`）。

## 6. 谁能看到什么（诚实边界）

| 角色 | 看得到 | 看不到 |
|---|---|---|
| 存储运营方（AWS / MinIO 管理员） | 密文 state blob、对象元数据、`.tflock` 锁文件 | 拓扑、资源属性、敏感变量、provider 凭据 |
| 云厂商（场景 B，你在 AWS 上真开了 VM） | 这些 VM 存在及其在 AWS 侧的配置（这是你真在用 AWS 的必然结果） | 你 state 文件的内容 |

**加密保护的是 state 文件内容**（谁连谁、密钥、变量），不是"隐藏你在用某云"——那是威胁模型的固有边界，加密逾越不了。

## 7. 待实测 / 存疑（不要当已定论）

1. **MinIO 上 `use_lockfile` 是否可靠**：MinIO 对 `If-None-Match: *` 条件写的支持与 AWS S3 不一致（官方 issue #20346 标记 "working as intended"，来源冲突）。选 MinIO 前**务必做并发 `apply` 冒烟测试**；若锁不可靠，改用 `pg`（自托管 Postgres）或 `http` backend 获得可靠锁。
2. **`.tflock` 锁文件内容是否被 OpenTofu 加密**：官方未明说，可能泄露"谁在何时操作"的元数据。需实测。
3. **OpenBao key provider 版本兼容**：若改用 OpenBao 而非 PBKDF2，注意它与 Vault ≥1.15（BUSL）不兼容。

## 8. 参考

- OpenTofu state 加密：https://opentofu.org/docs/language/state/encryption/
- S3 backend（`use_lockfile`、endpoints、SSE）：https://developer.hashicorp.com/terraform/language/backend/s3
- state 锁与 force-unlock：https://developer.hashicorp.com/terraform/language/state/locking
