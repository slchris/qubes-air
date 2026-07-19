# 从这里开始：Qubes Air 落地路径

> 这份文档回答一个问题：**我从哪开始，怎么把 Qubes Air 用起来？**
>
> 一句话答案：**起点是你自己的 Qubes 笔记本**。你在本地建好几个 qube，把自己的云凭据
> （Google / Proxmox 的 AK/SK）放进一个**没有网络**的 vault qube，然后用控制台在云上或
> 树莓派上拉起远程 Qube，再经官方 RemoteVM 接回本地桌面。云只提供算力，凭据始终在你手里。

## 现状提示（先读这一段）

项目当前是**可运行骨架**：代码层面已打通并有测试，但**尚未在真实 Qubes R4.3 + 云上跑通端到端**。
本文按目标落地路径书写，标注 `[已实现]` / `[待真机验证]` / `[骨架]`。真机首跑请配合
[docs/runbook-remotevm.md](runbook-remotevm.md) 逐项确认带 `待真机确认` 的步骤。详见 [readme.md](../readme.md) 项目状态。

---

## 你需要准备什么

| 项目 | 说明 |
|---|---|
| 一台 Qubes OS 4.2+（推荐 4.3）笔记本 | 做控制中枢，不需要高配 |
| 至少一处远程算力 | Proxmox VE（本地机房/家用服务器）`[真机可 apply]`，或 GCP / AWS `[骨架]`，或一台树莓派 |
| 对应的云凭据 | GCP 服务账号密钥、或 Proxmox API token；以及一对 SSH 密钥 |
| 基本的 Terraform / Salt 常识 | 能看懂 `.tfvars` 与 `qvm-*` 命令 |

**不需要**：公网 IP、给云厂商额外授权、把密钥交给任何托管服务。

---

## 落地路径总览（四步）

```
① 本地初始化            ② 放云凭据              ③ 建远程 Qube          ④ 接回本地
   dom0 建三个 qube  →    AK/SK 存 vault-cloud →   控制台点"建"     →   经 RemoteVM 回桌面
   mgmt-air              （无网络，出不去）        Terraform 编排        qrexec 双侧校验
   sys-relay
   vault-cloud
```

起点是本地部署，云只提供算力。**你连的是自己的 Google / Proxmox 账号**——控制台和云厂商都拿不到明文。

---

## ① 本地初始化：在 dom0 建三个 qube `[已实现]`

在你的管理 Qube 里拿到代码，复制 dom0 脚本到 dom0：

```bash
git clone https://github.com/slchris/qubes-air.git
cd qubes-air
qvm-copy-to-vm dom0 dom0-scripts/
```

在 **dom0** 里初始化：

```bash
sudo bash /home/user/QubesIncoming/<管理Qube名>/dom0-scripts/init-qubes-air.sh
```

它会建好三个角色分明的 qube：

| qube | 角色 | 网络 |
|---|---|---|
| `mgmt-air` | 编排控制台（跑 Terraform / RemoteVM 编排、Go+Svelte 控制台） | 联网 |
| `sys-relay` | 出站回程中继，qrexec 请求经它转发 | 联网（仅出站） |
| `vault-cloud` | 凭据保险库 | **无网络**（netvm=none） |

> 单独建 vault-cloud：`sudo bash dom0-scripts/create-vault-cloud.sh`。
> 它是普通 AppVM（有持久 /home），但**没有网卡**——放进去的私钥无法外泄上云。

**铁律（评审确立）**：dom0 永远离线、只经 Admin API 被动配合；编排全在 `mgmt-air`；vault-cloud 无网络。

---

## ② 放云凭据：AK/SK 存进 vault-cloud `[已实现]`

这一步就是你问的"连 Google AK/SK"的地方——但**不是把密钥交给控制台或上云**，而是放进本地无网络的
vault-cloud，用时经 qrexec 询问按需下发。

在 `vault-cloud` 里（它没有网络，安全），把凭据放到 `/home/user/`：

```bash
# GCP：服务账号 JSON 密钥
cp ~/downloads/gcp-sa-key.json /home/user/creds/gcp-sa-key.json

# Proxmox：API token（或 Proxmox 的 AK/SK）
echo "PROXMOX_TOKEN=..." > /home/user/creds/proxmox-token

# relay 出站建连用的凭据
# [TODO] 目标：gRPC 通道的客户端认证凭据（mTLS 客户端证书/私钥）
# 现有骨架：relay 出站 SSH 私钥（过渡）
cp ~/.ssh/relay_ed25519 /home/user/creds/relay-ssh-key
```

凭据流向（见 [docs/credential-vault.md](credential-vault.md)）：

| 凭据 | 存哪 | 谁用 | 怎么下发 |
|---|---|---|---|
| GCP / Proxmox 云凭据 | `vault-cloud` | `mgmt-air` 建机时 | qrexec `GetCredential`，dom0 弹窗 `ask`，用完即弃，**不落盘** |
| relay 出站凭据（[TODO] gRPC mTLS 证书；现为 SSH 私钥骨架） | `vault-cloud` | `sys-relay` 出站建连 | 同上 |
| 数据盘 LUKS 卷密钥 | `vault` | 数据盘加密 | **never-remote**：policy 硬编码永不下发到远端 |

**红线**：私钥永不进 git、永不进 pillar 明文、永不上云、控制台不长期持有明文。要防云厂商的数据，密钥就留在本地 vault。

---

## ③ 建远程 Qube：控制台点"建"，或用 Terraform `[已实现（Proxmox）/ 骨架（GCP/AWS）]`

### 方式 A：控制台（推荐，UI 优先）

打开控制台，点「建远程 Qube」→ 选 Zone、规格、数据盘 → 创建。控制台在 `mgmt-air` 上编排 Terraform，
计算与数据盘自动拆开。挂起/恢复也在这里点一下完成。

### 方式 B：Terraform 直接来

```bash
cd terraform
cp environments/dev.tfvars my-env.tfvars   # 填 zone、规格、数据盘大小
terraform init
terraform plan  -var-file=my-env.tfvars
terraform apply -var-file=my-env.tfvars
```

存算分离的关键：`compute_running` 开关切换"释放计算 / 重建挂回数据盘"，数据盘 `prevent_destroy`
保护，挂起时销毁昂贵的计算实例、保留便宜的数据盘。多机 state 走客户端加密后再进 backend，
云只见密文（见 [docs/terraform-state.md](terraform-state.md)）。

> Proxmox 为真机可 apply 实现；GCP / AWS 目前是接口对齐的骨架 TODO。`[待真机验证]`

---

## ④ 接回本地：经 RemoteVM 回到桌面 `[部分实现 / 待真机验证]`

远程 Qube 经官方 **RemoteVM** 与 **qrexec** 接回本地，像本地 qube 一样用：

- `sys-relay` 作为 **gRPC 客户端主动出站**建连到远端，维持一条**长连接双向流**；qrexec 请求经它转发，反向调用（远端→本地）复用**同一条流**回程。
- **家庭 NAT 后无需公网入站端口**（relay 只出站、不监听）。
- 两侧 dom0 各校验一次；**远端发起的调用永不自动放行**，破坏性操作在 dom0 弹窗 `ask`；Relay 不得直达 dom0。

> **[TODO] 传输层**：上面的 gRPC 双向流是**目标传输，尚未实现**。现有骨架用 `qubesair.SSHProxy`（autossh 出站 + `ssh -R` 反向回程），真机首跑可先按它验证——见 [docs/runbook-remotevm.md](runbook-remotevm.md)。gRPC 落地路线见 [docs/roadmap-to-production.md](roadmap-to-production.md)。

真机首跑按 [docs/runbook-remotevm.md](runbook-remotevm.md) 验证 `qvm-create` 用法、dom0 改写后看到的调用来源等 `待真机确认` 项；
自检见 [docs/remotevm-selfcheck.md](remotevm-selfcheck.md)。

---

## 常见疑问

**Q：是本地部署后连自己的 Google AK/SK 吗？**
是。起点是本地 Qubes，你连的是**自己的** Google / Proxmox 账号。凭据放进本地无网络的 vault-cloud，
用时经 qrexec 询问下发，控制台和云厂商都拿不到明文。没有任何托管服务替你保管密钥。

**Q：云厂商能看到我的数据吗？**
无机密计算（如 AMD SEV-SNP）时，云运营商仍能读 VM 内存。所以云 Zone 当前只承载**低敏感或一次性负载**，
高敏感任务留在本地或物理分离设备（如树莓派 Zone）。这一点在设计上诚实标注，不粉饰。

**Q：不用时会一直烧钱吗？**
不会。在控制台点「挂起」销毁计算实例、保留数据盘，账单只算用到的机时。下次点「恢复」挂回同一块盘。

**Q：需要给家里开公网端口吗？**
不需要。连接是本地 relay 发起的**出站** gRPC 长连接，反向调用复用同一条双向流回程，NAT 后无需入站端口。（[TODO] gRPC 为目标传输；现有骨架为出站 SSH + `ssh -R`。）

---

## 下一步

- 架构全貌：[docs/architecture.md](architecture.md)
- 凭据与销毁：[docs/credential-vault.md](credential-vault.md)、[docs/credential-destruction.md](credential-destruction.md)
- RemoteVM 真机 runbook：[docs/runbook-remotevm.md](runbook-remotevm.md)
- 多机加密 state：[docs/terraform-state.md](terraform-state.md)
- 项目状态与路线图：[readme.md](../readme.md)
