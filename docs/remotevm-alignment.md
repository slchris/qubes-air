# 对齐官方 RemoteVM (Qubes OS R4.3)

> 本文是路线图 **Phase 3** 的调研输出：搞清楚 R4.3 落地的官方 RemoteVM 机制，对照本项目自造的 `sys-remote + WireGuard` 方案，给出**可落地的对齐方案**。
>
> 结论先行：**应当放弃自造的私有隧道协议，转而在官方 RemoteVM 原语之上构建。** 本项目的价值应从"发明一套跨机机制"转向"用 Terraform/Salt/控制台，把官方 RemoteVM 的繁琐配置自动化"。

## 1. R4.3 到底落地了什么

官方 4.3 release notes 中与此相关的只有两条，且明确标记为 **experimental**：

- **Support for Qubes Air (#9015)**
- **qrexec protocol extension to support sending source information to destination (#9475)**

也就是说：R4.3 提供的是 **RemoteVM 这一 qube 类型 + qrexec 携带来源信息的能力**，而**不是**一个开箱即用的"云 Qube 管理器"。编排、provision、传输实现全部留给上层——**这正是本项目的空间**。

## 2. 官方 RemoteVM 机制（精确版）

来源：[qubes-core-qrexec / qrexec-remotevm](https://dev.qubes-os.org/projects/qubes-core-qrexec/en/stable/qrexec-remotevm.html)、`qubes-core-admin` 源码 `qubes/vm/remotevm.py` 与 `qubes/ext/relay.py`。

### 2.1 核心对象与属性

`RemoteVM` 是一个 qube 类（`class RemoteVM(BaseVM)`）。关键属性（源码确认）：

| 属性 | 类型 | 含义 |
|------|------|------|
| `relayvm` | VMProperty | 本地充当中继的 LocalVM（Relay），远程 qube 通过它可达 |
| `transport_rpc` | str | Relay 上负责转发的 RPC 服务名（下称 `TRANSPORT_RPC`，官方示例为 `qubesair.SSHProxy`） |
| `remote_name` | str | 远程侧该 qube 的原始名字（本地名可能不同，用 QubesDB `/remote/<name>` 建立映射） |
| `include_in_backups` | bool | 默认 `False` |

### 2.2 端到端调用流程

假设本地 `Local-Qube` 调用远程 `Remote-Qube` 的服务 `my_service+my_arg`：

```
Local-Qube ──qrexec──▶ Local-Relay ──TRANSPORT_RPC(如SSH)──▶ Remote-Relay ──qrexec-client-vm──▶ Remote-Qube
     (dom0 policy 决策)                                            (Remote dom0 policy 决策)
```

1. `Local-Qube` 发起对 `Remote-Qube` 的 RPC，**它并不知道对方是 RemoteVM**。
2. Local dom0 的 policy 引擎识别出 `Remote-Qube` 是 RemoteVM，其 `relayvm = Local-Relay`、`transport_rpc = TRANSPORT_RPC`。
3. 请求被改写为对 Relay 的一次 qrexec 调用，服务名形如：
   `TRANSPORT_RPC+Remote-Qube+my_service+my_arg`
4. `Local-Relay` 上的 `TRANSPORT_RPC` 服务把请求打包，**由该服务自己转发**（dom0 不直接管这条连接）。它读 QubesDB `/remote/<target>` 把本地名翻译成 `remote_name`。
5. 到达远端后，`Remote-Relay` 用 `qrexec-client-vm --source-qube=<Local-Qube>` 把请求投递给 `Remote-Qube`。
6. **Remote dom0 的 policy 再验一次**：`Local-Qube`（经 `Remote-Relay`）是否被允许执行 `my_service`。
   - ⚠️ 信任边界：Relay 能在其名下的 qube 之间伪造来源（如把 AppVM2 谎报为 AppVM1），但**不能伪造未走该 Relay 的 qube**。要更强保证需端到端验证（成本更高）。

### 2.3 官方示例的 SSH transport（`qubesair.SSHProxy`）

官方直接给出了一个用 **SSH** 做 transport 的示例脚本（这点很关键——**transport 可以就是 SSH，不需要自造 WireGuard 协议**）：

```bash
#!/bin/bash
set -euo pipefail
# $1 = "target+service"
IFS='+' read -r target service <<< "$1"
# 从 QubesDB 把本地名翻译成远端真实名
remote_qube="$(qubesdb-read "/remote/$target" 2>/dev/null || true)"
# 经 SSH 转发到 Remote-Relay 上的 qrexec-client-vm
ssh "$remote_qube" qrexec-client-vm --source-qube="$QREXEC_REMOTE_DOMAIN" "$remote_qube" "$service"
```

配套 `~/.ssh/config` 把目标 host 固定到 Remote-Relay 的 IP。

> **[已更正 2026-07] 原文称「非 Qubes 机器只要装上 `qrexec-client-vm` 即可接入」——这是错的，
> 且曾是本架构的地基假设。** `qrexec-client-vm` 来自 `qubes-core-agent-linux`，运行时依赖
> Xen vchan（`libvchan`）、`qubesdb` 与 dom0 的 `qrexec-daemon`。vchan 是 **Xen 单机域间**
> 共享内存原语，跨主机没有意义；一台跑在 KVM 上的普通 Debian 三样都没有，官方源也不提供此包。
>
> 非 Qubes 远端的接入方式见 [remote-agent-design.md](remote-agent-design.md)：实现一个
> qrexec **命令行接口**兼容、底层走 gRPC 的 agent，而非安装上游二进制。

## 3. 本项目现状 vs RemoteVM

| 维度 | 本项目当前设计 | 官方 RemoteVM | 差距 / 动作 |
|------|----------------|---------------|-------------|
| 跨机原语 | 自造 `sys-remote` ServiceVM | `RemoteVM` qube 类 + `relayvm`/`transport_rpc`/`remote_name` | **改用官方类与属性**，弃用 `sys-remote` 概念 |
| 传输 | 自造 WireGuard 隧道 + 自定义 `qubes-air.Remote`/`.Status` 服务 | Relay 上的 `TRANSPORT_RPC`（官方示例即 SSH） | **改为标准 `qubesair.SSHProxy`（或等价）**；WireGuard 降级为"可选底层链路加密"，而非协议本身 |
| Relay | （无明确对应） | `Local-Relay` LocalVM | **复用现有 `mgmt-jump`**（见 §5） |
| 授权 | 无（控制台无 policy 概念） | 两侧 dom0 qrexec policy 双重校验 | **新增 policy 管理**（本项目最该做的自动化） |
| 名称映射 | 无 | QubesDB `/remote/<name>` ↔ `remote_name` | 新增映射管理 |
| 控制台 qrexec 客户端 | `qrexec-client-vm`（已实现但未接入） | 正是 RemoteVM 底层用的同一命令 | **方向已对**，接上即可 |

**好消息**：本项目的 qrexec 客户端（`console/backend/internal/qrexec/client.go`）调用的就是 `qrexec-client-vm`——与 RemoteVM 底层一致，无需重写，只需接入 service 层并改为面向 RemoteVM 的目标。

## 4. 对齐方案（本项目应承担的角色）

RemoteVM 只提供"机制"，繁琐的手工配置正是本项目要自动化的：

1. **Terraform** — provision 远端主机（PVE/GCP/AWS/树莓派），装好 `qrexec-client-vm`，输出其可达地址 → 供 Relay 的 SSH config 使用。（替换掉目前只有 `random_id` 的空壳。）
2. **Salt** — 在 dom0 侧自动完成：
   - `qvm-create --class RemoteVM <name>` 并 `qvm-prefs` 设置 `relayvm` / `transport_rpc` / `remote_name`；
   - 在 Relay 上部署 `qubesair.SSHProxy` transport 服务 + `~/.ssh/config`；
   - 写入两侧 qrexec **policy**；
   - 维护 QubesDB `/remote/<name>` 映射。
3. **控制台** — 把上述状态可视化/可编排：
   - `Qube.Start()` 等不再只改 SQLite，而是**真正触发** Salt/Terraform（或经 qrexec 调 dom0）；
   - qrexec 客户端接入 service 层，面向 RemoteVM 目标做健康检查/调用。

> 迁移策略：保留现有 CRUD/UI，把"状态字段"逐个替换为真实动作；先打通**一条** RemoteVM（建议 Proxmox 或树莓派）端到端，再横向复制。

## 5. 复用现有 `qubes-salt-config` 的 `mgmt-jump`（关键协同）

Salt 模板维护在独立仓库 **[slchris/qubes-salt-config](https://github.com/slchris/qubes-salt-config)**（qusal 风格、config-driven、无 pillar，配置集中在 `salt/config.jinja`）。

该仓库已有 `salt/mgmt/remote-debug/` formula：一个网络化的 **`mgmt-jump` AppVM**，跑 sshd，经 qrexec + admin policy 到达 dom0。**这个 `mgmt-jump` 几乎就是 RemoteVM 架构里的 `Local-Relay`**，其现有 SSH 转发正好对应 `qubesair.SSHProxy`。

因此对齐工作可以**直接嫁接**到它上面，而不是从零搭 Relay：

- **Relay** = 复用 `mgmt-jump`（或新增一个 `sys-relay`，与调试用途隔离——更干净，推荐）。
- **SSH transport** = 复用其 sshd + `authorized_keys` + 端口转发（`config.jinja` 的 `remote_debug` 块已有 `ssh_port` / `netvm` / `lan_subnet` / `authorized_keys`）。
- **落点** = 在 `qubes-salt-config` 里新增一个 `salt/mgmt/remotevm/`（或 `salt/qubes-air/`）formula，沿用其 `config.jinja` 约定，把 §4.2 的 dom0 配置写成 states；本仓库（qubes-air）的 Terraform/控制台负责远端 provision 与编排，两仓协同。

配置约定（拟）——在 `config.jinja` 增加：

```jinja
"remotevm": {
  "relay": "mgmt-jump",              # 或 sys-relay
  "transport_rpc": "qubesair.SSHProxy",
  "targets": [
    {"local_name": "remote-dev", "remote_name": "dev", "host": "10.42.0.50"},
  ],
},
```

> 注意（安全）：`config.jinja` 属于用户私有环境配置，其中含真实 SSH 公钥等；若 `qubes-salt-config` 为公开仓库，建议复核不要混入敏感明文（如 WiFi 明文密码），或将这类值改为部署时注入。

## 6. 待确认 / 后续

- `qvm-create` 创建 RemoteVM 的**精确 class 名与 flags**：源码类名为 `RemoteVM`，需在 4.3 实机确认 `qvm-create --class RemoteVM ...` 的确切用法与 `qubes-prefs` 字段（文档未给命令示例）。
- `qubesair.SSHProxy` 是否已随发行版提供，还是需自带该 transport 服务脚本。
- policy 新语法下 RemoteVM 相关规则的**精确写法**（文档未给逐行示例，需实机验证）。
- 端到端来源验证（#9475 的 source info）在 4.3 的可用范围。

## 参考

- [Service call to a RemoteVM（官方机制）](https://dev.qubes-os.org/projects/qubes-core-qrexec/en/stable/qrexec-remotevm.html)
- [Qubes OS 4.3 release notes](https://doc.qubes-os.org/en/r4.3/developer/releases/4_3/release-notes.html)
- `qubes-core-admin`：`qubes/vm/remotevm.py`、`qubes/ext/relay.py`
- [新 qrexec policy 系统](https://www.qubes-os.org/news/2020/06/22/new-qrexec-policy-system/)
- [slchris/qubes-salt-config](https://github.com/slchris/qubes-salt-config)（Salt 模板与 `mgmt-jump`/remote-debug）
