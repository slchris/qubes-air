# Runbook: RemoteVM + 零入站 SSH transport (阶段2, 真机执行)

> ⚠️ **传输层说明**: 本 runbook 描述的是 **`qubesair.SSHProxy` transport**(autossh 出站 + `ssh -R` 反向回程)。
> **目标架构的跨机传输已改为 gRPC 双向流**(relay 出站建 gRPC 长连接、双向承载 qrexec 转发与反向回程, 零入站)。
> gRPC 版 runbook 待真机验证后补充; 落地路线见 [roadmap-to-production.md](roadmap-to-production.md)。
>
> ⚠️ **本文照做不通了(§3 起)**: 它 apply 的 `qubes-air.remotevm.relay/.autossh` 骨架**已删除**
> (见 [salt/qubes-air/README.md](../salt/qubes-air/README.md))。接替它的
> `qubes-salt-config` 仓库 `mgmt.remotevm.relay` **只覆盖其中一部分**——装 transport 脚本
> 与 `~/.ssh/config`,**没有** autossh 单元、回环 sshd、反向 handler。
> 也就是说 §3 之后依赖这些的步骤(§5 起 autossh、§9 反向调用验证)**目前没有对应的 state**。
> 缺口逐项列在 §3。
>
> 持久化曾是第三个缺口(重启即丢 transport),**已在 qubes-salt-config 修复**
> (`fix(remotevm): persist the relay's transport instead of losing it at reboot`)。
> 同一个 bug 当时**两条路都有**——gRPC 那条声明了 bind-dirs 却把 unit 写到了真实路径,
> 看着更像已处理,其实一样丢——一并修了。本文 §3 描述的是修复后的行为,
> 需要 qubes-salt-config 含该提交。

> 目标: 从"terraform 建出远端 VM"到"本地 AppVM 里 `qrexec-client-vm remote-dev-1 <service>` 调通",
> 全链路可照做。含零入站验证与反向调用验证。
>
> 平面分离铁律: **dom0 永远离线**, 联网/编排在 `mgmt-air`, 跨机交互走 qrexec 语义,
> 网络层只承载 relay↔relay 点对点 SSH。本地 AppVM 永不改 netvm。

## 0. 角色与命名

| 角色 | 位置 | 说明 |
|------|------|------|
| `dom0` | 本地 | 唯一可信授权决策点 (policy)。离线。 |
| `mgmt-air` | 本地 AppVM (联网) | 跑 terraform / salt-ssh / 渲染 ssh config。不在 dom0。 |
| `sys-relay-pve` | 本地 AppVM | Local-Relay。netvm=sys-firewall, 不做网关, tag=relay。 |
| `remote-dev-1` | 本地 RemoteVM | 纯元数据 qube (不可 start), tag=remote-zone。 |
| `dev` | 远端主机 | Remote-Relay/Remote-Qube, 装 qrexec-client-vm。remote_name=dev。 |
| `work-1` (示例) | 本地 AppVM | 发起远程调用的工作 qube, tag=remote-work。 |

调用流 (两侧 policy 各校验一次):
```
work-1 ─qrexec→ dom0(policy A/B) ─改写→ sys-relay-pve[qubesair.SSHProxy]
       ─出站SSH(autossh)→ dev[Remote-Relay] ─qrexec-client-vm→ dev[Remote-Qube]
                                                        (远端 dom0/policy 再校验)
反向: dev ─ssh -R 回环→ sys-relay-pve[回环sshd] ─qrexec-client-vm→ vault (dom0 policy C: ask)
```

---

## 1. 阶段1: terraform 建远端 VM (在 mgmt-air)

```bash
# 在 mgmt-air
cd ~/qubes-air/terraform/environments   # 或你的 root module 目录
terraform init
terraform apply -var-file=production.tfvars
# 记录输出的 ip_address (compute_running=true 才有值)
terraform output -json
```

预期: 远端 VM 起来, `ip_address` 非空, `status=running`。
远端主机需要 qrexec 调用能力。

> **[已更正 2026-07]** 原文写「需装 `qrexec-client-vm`（由阶段1 cloud-init 或后续 ansible
> bootstrap-zone 完成）」。两处都不成立：cloud-init 目前只注入 ip_config 与 SSH 公钥；
> `ansible/playbooks/bootstrap-zone.yaml` 的 `hosts: zone_admins` 指向 **Proxmox 宿主机**
> 而非被创建的 guest，且它安装的 7 个包里没有 qrexec。
>
> 更根本的是这个包**装不上**——它依赖 Xen vchan，KVM guest 里不存在。
> 实际方案见 [remote-agent-design.md](remote-agent-design.md)：由 cloud-init 安装
> `qubes-air-agent`，它提供一个同名同接口的 `qrexec-client-vm`，底层走 gRPC 而非 vchan。
> 该 agent **已实现**（`console/backend/cmd/qubes-air-agent`），并以
> `qubes-air-agent_<version>_amd64.deb` 的形式发布到局域网 artifact store，
> 由 cloud-init 下载 + 校验 SHA256 后安装 —— **不烤进镜像**，
> 原因和残留风险见 [bootstrap-design.md](bootstrap-design.md) §6。

---

## 2. 创建本地 Relay (在 dom0)

```bash
# 在 dom0
cd /path/to/qubes-air/dom0-scripts
sudo bash create-sys-relay.sh --name sys-relay-pve --template fedora-42
# 校验: 不做网关
qvm-prefs sys-relay-pve provides_network   # 期望 False
qvm-tags  sys-relay-pve list               # 期望含 relay
```

关键: `create-sys-relay.sh` 明确 `provides_network=False`、不开 ip_forward。
可选收敛: 用 `qvm-firewall sys-relay-pve add ...` 限制其只能出站到 `dev` 的 IP:22。

---

## 3. 部署 Relay 内配置 (salt, 经 mgmt-air / qubesctl)

> **states 在 `qubes-salt-config` 仓库**(`salt/mgmt/remotevm/`),配置走它的 `salt/config.jinja`,
> 调用式是 `state.apply mgmt.remotevm.<state>`(不是本仓库旧的 `state.sls qubes-air.remotevm.*`)。
> 该仓库的 `cfg.remotevm.relay` **默认是 `mgmt-jump`**,不是本文的 `sys-relay-pve`;
> 要沿用本文命名,得改 `config.jinja` 的 `remotevm.relay` **并同步改 `relay.top` 里的目标 qube**
> (Salt top 匹配字面量 qube 名)。

```bash
# 3a. 先对 Relay 用的模板装包: openssh-clients
#     (state 里有 apt/dnf 兜底, 但那是装在 AppVM 上、随根卷一起丢的, 只顶当前这次开机;
#      装进模板才持久)
qvm-run -u root fedora-42 'dnf install -y openssh-clients' && qvm-shutdown --wait fedora-42

# 3b. 再对 Relay AppVM 应用 transport 配置
sudo qubesctl --skip-dom0 --targets=sys-relay-pve state.apply mgmt.remotevm.relay
```

它装的只有两样: `/etc/qubes-rpc/qubesair.SSHProxy` 和 `/home/user/.ssh/config`
(按 `cfg.remotevm.targets` 每个目标一条 Host)。

**相对被删骨架的缺口** —— 下面这些本文后续步骤依赖、但接替方**没有实现**:

| 缺的东西 | 影响本文哪一步 |
|---|---|
| `autossh-qubesair@.service` 出站隧道单元 | §5 起隧道、§10 停隧道 |
| 回环 sshd + `reverse-qrexec-handler`(ForceCommand) | §9 反向调用验证 |

这两条目前**没有替代品**: 本文 §5 与 §9 现在没有 state 可 apply,要么手写单元,
要么改走 gRPC 那条路(`mgmt.remotevm.grpc-relay`,反向回程在同一条长连接里,不需要
回环 sshd)。

**持久化检查** (原先这里会失败,现已修复,见顶部横幅):

```bash
qvm-run -p sys-relay-pve 'ls -l /etc/qubes-rpc/qubesair.SSHProxy'          # 存在
qvm-run -p sys-relay-pve 'ls -l /rw/bind-dirs/etc/qubes-rpc/qubesair.SSHProxy'  # 真身在持久卷
qvm-run -p sys-relay-pve 'cat /rw/config/qubes-bind-dirs.d/50_qubesair.conf'
# 重启后仍在 (bind-dirs 生效):
qvm-shutdown --wait sys-relay-pve && qvm-start sys-relay-pve
qvm-run -p sys-relay-pve 'ls -l /etc/qubes-rpc/qubesair.SSHProxy'          # 应存在
```

最后一条是这条链路上最容易被漏掉的检查: transport 丢失不会报错,
症状是很久以后某次跨机调用失败,离原因很远。**别跳过重启这一步。**

---

## 4. 渲染 & 投递 SSH config (在 mgmt-air), 预置 host key pinning

```bash
# 在 mgmt-air: 消费阶段1 terraform output 的 ip_address
cd ~/qubes-air/relay/ssh
./render-ssh-config.sh \
    --tf-dir  ~/qubes-air/terraform/environments \
    --tf-output-name remote_dev_1 \
    --remote-name dev \
    --ssh-user  qubesrelay \
    --ssh-port 22 --reverse-port 22000 \
    --out ./rendered/config.dev

# 采集远端 host key (pinning), 存到该远端专用 known_hosts:
ssh-keyscan -p 22 <ip_address> > ./rendered/known_hosts.dev

# 投递到 Relay 持久卷 (经 qvm-copy 或 salt):
qvm-copy-to-vm sys-relay-pve ./rendered/config.dev
qvm-copy-to-vm sys-relay-pve ./rendered/known_hosts.dev
# 在 Relay 内合入 ~/.ssh/config 与 ~/.ssh/known_hosts.d/dev, 生成 id_ed25519_qubesair 密钥并把公钥装到远端。
```

密钥: Relay 生成 `~/.ssh/id_ed25519_qubesair`, 把**公钥**装到远端 `dev` 的 authorized_keys。私钥不出 Relay。

---

## 5. 创建 RemoteVM (在 dom0) + 部署 policy

```bash
# 在 dom0
sudo bash create-remotevm.sh \
    --name remote-dev-1 --relay sys-relay-pve --remote-name dev \
    --transport-rpc qubesair.SSHProxy

# 校验属性
qvm-prefs remote-dev-1 relayvm         # sys-relay-pve
qvm-prefs remote-dev-1 transport_rpc   # qubesair.SSHProxy
qvm-prefs remote-dev-1 remote_name     # dev
qvm-tags  remote-dev-1 list            # 含 remote-zone

# 部署单一来源 policy
sudo install -m 0644 policy.d/30-qubes-air.policy /etc/qubes/policy.d/30-qubes-air.policy
sudo rm -f /etc/qubes/policy.d/80-qubes-air.policy   # 删旧的非法 policy
# 给工作 qube 打 tag 使其能发起远程调用:
qvm-tags work-1 add remote-work
```

---

## 6. 启动隧道 (在 Relay)

```bash
qvm-run -p sys-relay-pve 'sudo systemctl daemon-reload && sudo systemctl start autossh-qubesair@dev'
# 确认 QubesDB 映射已重建 (Relay 开机 rc.local 会做; 手动确认):
qvm-run -p sys-relay-pve 'qubesdb-read /remote/remote-dev-1'   # 期望输出: dev
```

---

## 7. 端到端自检 (正向调用)

```bash
# 在本地工作 qube work-1
qrexec-client-vm remote-dev-1 qubesair.Ping
# 期望: 经 dom0 policy(A: allow) -> 改写 qubesair.SSHProxy(B: allow) -> Relay -> 出站SSH
#       -> dev qrexec-client-vm -> 远端 qubesair.Ping 服务返回 "pong" 或类似。

# 破坏性服务应弹窗 (ask):
qrexec-client-vm remote-dev-1 qubesair.Exec
# 期望: dom0 弹出确认对话框 (policy A: ask), 拒绝则调用失败。
```

---

## 8. 零入站验证 (远端无监听入站端口)

```bash
# 在远端主机 dev (经 mgmt-air ssh 进去, 或控制台):
sudo ss -tlnp        # 检查监听端口
# 期望: 除了 SSH 服务端端口(22, 且被 SG/防火墙锁定源IP=Relay出口), 无其它对公网监听的入站端口。
#       transport / 反向调用都不需要远端额外开入站端口。

# 从外部扫描远端公网 IP (在 mgmt-air):
nmap -Pn -p- <远端公网IP>
# 期望: 仅 22 开放(或按 SG 白名单对 Relay 出口 IP 才可见), 其余 filtered/closed。
# 若拓扑为"远端在 NAT 后主动出站", 则远端公网无任何入站端口 (严格零入站)。
```

参考 `terraform/providers/proxmox/zero-inbound-firewall.md` 的拓扑澄清。

---

## 9. 反向调用验证 (远端 → 本地, 经 ssh -R)

```bash
# 前提: autossh 隧道含 RemoteForward 127.0.0.1:22000 -> Relay 127.0.0.1:2222 (config.template)
# 在远端主机 dev, 经反向端口调本地 Split-GPG:
ssh -p 22000 -o StrictHostKeyChecking=accept-new user@127.0.0.1 'qubes.Gpg'
# 数据经 ssh -R 反向隧道 -> Relay 回环 sshd(2222) -> ForceCommand reverse-qrexec-handler
#   -> qrexec-client-vm vault qubes.Gpg -> dom0 policy(C: ask) 弹窗。
# 期望: 本地 dom0 弹出 ask 对话框 (来源显示为 sys-relay-pve/@tag:relay, 目标 vault)。
#       拒绝则远端调用失败; 远端永远拿不到 allow。

# 验证 dom0 绝不被 Relay 直连:
# (尝试从 Relay 调 admin API 应被 D 段 deny)
qvm-run -p sys-relay-pve 'qrexec-client-vm dom0 admin.vm.List' ; echo "rc=$?"
# 期望: rc 非 0 (policy deny)。
```

---

## 10. 回滚 / 清理

```bash
# dom0
qvm-run -p sys-relay-pve 'sudo systemctl stop autossh-qubesair@dev'
qvm-remove remote-dev-1        # RemoteVM 可直接 remove (纯元数据)
# 保留 sys-relay-pve 供其它 zone 复用; 或 qvm-remove sys-relay-pve
```
