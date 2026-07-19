# 链路自检清单 (RemoteVM + 零入站 SSH transport)

每步给出命令与**预期输出**。逐条打勾, 任何一条不符即停下排查。

## A. dom0 元数据与 policy

| # | 命令 (dom0) | 预期输出 |
|---|-------------|---------|
| A1 | `qvm-check --quiet remote-dev-1; echo $?` | `0` (RemoteVM 存在) |
| A2 | `qvm-prefs remote-dev-1 relayvm` | `sys-relay-pve` |
| A3 | `qvm-prefs remote-dev-1 transport_rpc` | `qubesair.SSHProxy` |
| A4 | `qvm-prefs remote-dev-1 remote_name` | `dev` |
| A5 | `qvm-tags remote-dev-1 list` | 含 `remote-zone` |
| A6 | `qvm-start remote-dev-1; echo $?` | **非 0 且报错** (RemoteVM 不可启动 —— 这是对的) |
| A7 | `test -f /etc/qubes/policy.d/30-qubes-air.policy && echo ok` | `ok` |
| A8 | `test -f /etc/qubes/policy.d/80-qubes-air.policy; echo $?` | `1` (旧非法 policy 已删) |
| A9 | `qubes-policy-lint /etc/qubes/policy.d/30-qubes-air.policy` (若有该工具) | 无语法错误 |

## B. Relay 状态与持久化

| # | 命令 | 预期输出 |
|---|------|---------|
| B1 | `qvm-prefs sys-relay-pve provides_network` | `False` (不做网关) |
| B2 | `qvm-tags sys-relay-pve list` | 含 `relay` |
| B3 | `qvm-run -p sys-relay-pve 'ls /etc/qubes-rpc/qubesair.SSHProxy'` | 路径存在 |
| B4 | 重启 Relay 后重跑 B3 | 仍存在 (bind-dirs 持久化生效) |
| B5 | `qvm-run -p sys-relay-pve 'qubesdb-read /remote/remote-dev-1'` | `dev` |
| B6 | `qvm-run -p sys-relay-pve 'systemctl is-active autossh-qubesair@dev'` | `active` |
| B7 | `qvm-run -p sys-relay-pve 'ss -tlnp | grep 127.0.0.1:2222'` | 回环 sshd 监听 (仅 127.0.0.1) |
| B8 | `qvm-run -p sys-relay-pve 'cat /rw/config/rc.local | grep qubesdb-write'` | 有重建映射逻辑 |

## C. 出站 SSH 隧道

| # | 命令 (在 Relay) | 预期输出 |
|---|-----------------|---------|
| C1 | `ssh -F ~/.ssh/config dev true; echo $?` | `0` (host key pinning 通过, 连得上) |
| C2 | `ssh -F ~/.ssh/config -O check dev` | `Master running` (ControlMaster 复用) |
| C3 | `pgrep -a autossh` | 有 `autossh -M 0 -N -F ... dev` 进程 |

## D. 正向端到端调用

| # | 命令 (在 work-1, tag=remote-work) | 预期输出 |
|---|-----------------------------------|---------|
| D1 | `qrexec-client-vm remote-dev-1 qubesair.Ping` | 远端服务响应 (如 `pong`) |
| D2 | `qrexec-client-vm remote-dev-1 qubesair.Exec` | dom0 弹 **ask** 对话框 |
| D3 | 从未打 remote-work tag 的 qube 调 D1 | policy A 兜底 **deny**, 调用失败 |

## E. 零入站

| # | 命令 | 预期输出 |
|---|------|---------|
| E1 | 远端 `sudo ss -tlnp` | 无面向公网的多余入站监听端口 |
| E2 | mgmt-air `nmap -Pn -p- <远端公网IP>` | 仅 22 (且 SG 锁定 Relay 出口 IP) 或全 filtered |
| E3 | 确认无 WireGuard | 远端 `ip link | grep wg` 为空 (旧方案已弃) |

## F. 反向调用 (远端 → 本地)

| # | 命令 | 预期输出 |
|---|------|---------|
| F1 | 远端 `ssh -p 22000 user@127.0.0.1 qubes.Gpg` | dom0 弹 **ask** (来源 @tag:relay, 目标 vault) |
| F2 | 远端经反向端口调白名单外服务 (如 `qubes.VMShell`) | Relay 回环 sshd ForceCommand **拒绝** |
| F3 | `qvm-run -p sys-relay-pve 'qrexec-client-vm dom0 admin.vm.List'; echo $?` | 非 0 (policy D 段 deny, Relay 绝不达 dom0) |

## G. 平面分离铁律 (回归防护)

| # | 检查 | 预期 |
|---|------|------|
| G1 | dom0 是否装了联网工具 | 否 (dom0 离线) |
| G2 | 本地工作 qube 的 netvm 是否被改指向 Relay | 否 (netvm 未变) |
| G3 | Relay 是否 provides_network / ip_forward | 否 (B1 已验) |
| G4 | policy 里是否存在 relay/remote -> @adminvm allow | 否 (只有 deny) |
