# salt/qubes-air/ — 已退役 (RETIRED)

> **本目录不再是可部署的 Salt formula。不要往这里加新 state。**
> Qubes 侧的 states 单一来源是 **`qubes-salt-config`** 仓库的 `salt/mgmt/`。

## 为什么退役

这里原本放的是「阶段 2」的骨架：`sys-remote` + WireGuard 网关，以及 RemoteVM 的
autossh / `ssh -R` SSHProxy 传输。三件事让它必须退役，而不是继续「保留作过渡参考」:

1. **传输已经换了。** 目标传输是 gRPC 双向流（relay 出站建连、长连接承载调用与回程、
   零入站），见 [docs/grpc-transport-design.md](../../docs/grpc-transport-design.md)。
   autossh / `ssh -R` 那条路不再是要落地的东西。
2. **它的 TODO 要求的东西已经存在了。** `remotevm/relay.sls` 与 `remotevm/autossh.sls`
   的头部写着「[TODO] 新增 grpc-relay.sls」——而 `qubes-salt-config` 里
   `salt/mgmt/remotevm/grpc-relay.sls` 和 `grpc-remote.sls` **早就写好了**。一个指向
   已完成工作的 TODO，只会让下一个人重写一遍。
3. **这里的内容是重复来源，而重复来源这个项目已经栽过两次**（重复的 systemd unit；
   「这个 agent 还活着吗」有两个互相矛盾的答案）。具体到本目录:
   - `remotevm/files/30-qubes-air.policy` 与 `dom0-scripts/policy.d/30-qubes-air.policy`
     **逐字节相同**——同一份 dom0 policy 存在两份，改一份另一份就悄悄过期；
   - `remotevm/dom0.sls` 与 `qubes-salt-config` 的 `salt/mgmt/remotevm/create.sls`
     做同一件事（建 RemoteVM、设 relayvm/transport_rpc/remote_name），且两者读的配置
     来源不同（前者读 pillar，后者读 `config.jinja`）——两边不一致时没有任何东西会报错，
     只会得到一个属性被悄悄改掉的 RemoteVM。

另外 `top.sls` 路由到 `remote-qube.base` / `remote-qube.agent` / `zone-admin.*` 四个
**从来不存在**的 state；[docs/remote-agent-design.md §6](../../docs/remote-agent-design.md)
已经把它列为「需补齐或删除」。这次是删除。

`sys-remote/` 早在头部注释里就自称已被评审否决（开 `ip_forward` + `provides_network`
把 Relay 当本地网关，违反平面分离；`qubes-air.Remote` 是任意命令通道，反模式），
同样删除。

## 搬到哪里去了

Qubes 上真正要跑的 states 全部在 **`qubes-salt-config`** 仓库，配置走它的
`salt/config.jinja`（该仓库刻意**不用 pillar**：Qubes dom0 不可靠地加载自定义 pillar，
放 pillar 的 top.sls 甚至会连带弄坏 pillar 加载）。对照表:

| 本目录（已删除） | 现在看这里（qubes-salt-config） |
|---|---|
| `top.sls` | 各 formula 自带 `*.top`，`salt/top.sls` |
| `common/base.sls` | 各模板的 `install.sls`（如 `salt/templates/dev/install.sls`） |
| `remotevm/dom0.sls` | `salt/mgmt/remotevm/create.sls` + `policy.sls` |
| `remotevm/relay.sls` | `salt/mgmt/remotevm/relay.sls`（SSHProxy，仍在，但**只覆盖一部分**，见下） |
| `remotevm/autossh.sls` | 无对应物 —— gRPC 传输不需要 autossh |
| `remotevm/files/relay-loopback-sshd.conf`、`reverse-qrexec-handler` | 无对应物（SSHProxy 的反向回程在接替方缺失） |
| `remotevm/files/30-qubes-air.policy` | `dom0-scripts/policy.d/30-qubes-air.policy`（本仓库，单一来源）；dom0 本地 policy 由 `salt/mgmt/remotevm/policy.sls` 从 `config.jinja` 渲染 |
| `remotevm/files/qubesair.SSHProxy` | `salt/mgmt/remotevm/files/qubesair.SSHProxy` |
| 无（当时缺） | `salt/mgmt/remotevm/grpc-relay.sls` / `grpc-remote.sls` ← 就是那个 TODO |
| 无（当时缺） | `salt/mgmt/remotevm/files/qubesair.Ping`、`teardown.sls` |
| `sys-remote/*` | 无对应物，且**不会有** —— `sys-remote` 概念已被官方 RemoteVM 原语取代，见 [docs/remotevm-alignment.md](../../docs/remotevm-alignment.md) |

被删掉的内容都在 git 历史里，需要时:

```sh
git log --oneline -- salt/qubes-air/
git show <commit>^:salt/qubes-air/remotevm/relay.sls
```

### SSHProxy 那条路不是等价搬迁

`mgmt.remotevm.relay` 装的只有 `/etc/qubes-rpc/qubesair.SSHProxy` 和
`/home/user/.ssh/config`。本目录删掉的 `relay.sls` + `autossh.sls` 还做了三件它**不做**的事:

1. **autossh 出站隧道单元**（`autossh-qubesair@.service`）;
2. **回环 sshd + `reverse-qrexec-handler`**（`ssh -R` 反向回程的落点）;
3. **bind-dirs 持久化** —— 旧 `relay.sls` 把 transport 脚本放 `/rw/bind-dirs/` 再绑回
   `/etc/qubes-rpc/`；`mgmt.remotevm.relay` 直接写 `/etc/qubes-rpc/`，而 AppVM 的 `/etc`
   属根卷，**Relay 一重启就没了**。

前两条使 [docs/runbook-remotevm.md](../../docs/runbook-remotevm.md) §5 / §9 目前无 state 可用，
第三条使该 runbook 原有的持久化检查必然失败。三条都已写进该 runbook 的 §3 与顶部横幅。
根治第三条: 照同仓库 `mgmt.remotevm.grpc-relay` 的
`/rw/config/qubes-bind-dirs.d/50_qubesair_grpc.conf` 给 `relay.sls` 补上。
**gRPC 那条路没有这个问题**——`grpc-relay.sls` 三件事都做了（除 autossh，它本就不需要）。

## 还留在这里的东西

`vault-cloud/` **没有被取代**，所以留着。`qubes-salt-config` 里目前没有 vault 对应物，
而这两份文档正指着它的 `files/` 当参考实现:

- [docs/credential-vault.md](../../docs/credential-vault.md) 让运维把
  `vault-cloud/files/relay-split-ssh-client.sh` 的内容追加到 relay 的 ssh 配置；
- [docs/grpc-transport-design.md](../../docs/grpc-transport-design.md) 把
  `vault-cloud/files/qubesair.GetCredential` 当作 mTLS 证书下发的既有服务。

**但它和上面被删的东西是同一代产物，同样未经真机验证**，`init.sls` 还在读 pillar
（在 Qubes dom0 上不可靠，见上）。把它当作 **qrexec 服务脚本的参考实现**，不要当作
「另一个能 apply 的 formula」。真要上真机，正确做法是把它搬进 `qubes-salt-config`
的 `salt/mgmt/`、改读 `config.jinja`，然后**连本目录一起删掉**。

## 仍然指向本目录的陈旧引用

下面这些引用还没改，指的是已删除的路径。列在这里是为了下一个人不必重新发现:

- `readme.md` 第 610 / 747 / 840 / 875 行 —— `cp -r qubes-air/salt/qubes-air /srv/salt/`
  以及几个 `/srv/salt/qubes-air/*.sls` 的示例路径；
- `dom0-scripts/init-qubes-air.sh` 第 86–95 行 —— `salt_dir="/srv/salt/qubes-air"`；
- `docs/remote-agent-design.md` 第 9 / 144 / 201 行 —— `remotevm/files/reverse-qrexec-handler`
  与 `top.sls:20-22`（后者本次已按该文档的建议删除）；
- `docs/runbook-remotevm.md` 第 230 行 —— 反向隧道里的 `reverse-qrexec-handler`。

### 已修: 两处可直接复制粘贴的命令

这两处不是注释或链接，是运维会照着敲、然后撞上「state 不存在」的 `qubesctl` 命令，
所以随本次改动一起修了:

- `docs/runbook-remotevm.md` §3 —— 原 `state.sls qubes-air.remotevm.relay,...autossh`。
  **不是简单改名**: 已按上面「不是等价搬迁」一节改写，逐项写明接替方缺什么，
  并在顶部横幅标明 §5 / §9 目前没有对应 state。
- `docs/credential-vault.md` §2 —— 原 `state.sls qubes-air.remotevm.dom0`，还标着「(推荐)」。
  **接替方不存在**: `mgmt.remotevm.policy` 写的是 `/etc/qubes/policy.d/30-remotevm.policy`，
  与本文的 `30-qubes-air.policy`（含 vault-cloud E 段）无关，整个 qubes-salt-config
  不含 E 段规则。已改为「目前只能手动 `cp`」。

**下面这几处是「下一步」提示，会教运维去 apply 一个已经不存在的 state**
（`qubes-air.remotevm.*` 现在应改为 qubes-salt-config 的 `mgmt.remotevm.*`），
但都是脚本尾部的提示文字/注释，不是可直接执行的命令，优先级次之:

- `dom0-scripts/create-remotevm.sh` 第 151 行 —— 「在 Relay 上部署 transport (salt: qubes-air.remotevm)」；
- `dom0-scripts/create-sys-relay.sh` 第 74 行 —— 「应用 salt (qubes-air.remotevm.relay/.autossh)」；
- `dom0-scripts/init-qubes-air.sh` 第 59 / 120 行 —— 同上，外加 `salt_dir="/srv/salt/qubes-air"`；
- `ansible/playbooks/setup-sys-remote.yaml` 第 58 / 65 行 —— 「由 salt qubes-air.remotevm.dom0 部署 policy」。

对照关系: `qubes-air.remotevm.dom0` → `mgmt.remotevm.create` + `mgmt.remotevm.policy`；
`qubes-air.remotevm.relay` → `mgmt.remotevm.relay`；`qubes-air.remotevm.autossh` → 无
（gRPC 传输用 `mgmt.remotevm.grpc-relay`，不需要 autossh）。

同一代的 `salt/pillar/`（`default.sls` / `secrets.sls` / `top.sls` /
`remotevm.sls.example`）现在也成了孤儿: `remotevm.sls.example` 只被本次删除的
`remotevm/*.sls` 消费，`pillar/top.sls` 只路由 `sys-remote-*` 与 `zone-admin-*`。
留着是因为它超出本次改动范围，**不是因为它还有用**。
