#!/bin/bash
# Qubes Air - 管理脚本 —— **已废弃, 不要再用**
#
# 这个脚本的每一条命令 (list / start / stop / vpn-status / connect / disconnect /
# exec) 操作的都是 `sys-remote-<zone>` qube 和它里面的 `wg-quick@wg0`。
#
# 那套东西**被评审否决并已删除**:
#   - `sys-remote` 开 `ip_forward` + `provides_network` 把 Relay 当本地网关,
#     违反平面分离; `qubes-air.Remote` 是任意命令通道, 反模式。
#     现行架构见 docs/architecture.md。
#   - 对应的 salt states (sys-remote/wireguard.sls / gateway.sls / firewall.sls)
#     已随之删除。
#
# 所以这些命令操作的 qube 和服务**都不存在**。同目录的 init-qubes-air.sh 自己就写着
# 「WireGuard 方案已废弃, 改用 SSH transport」—— 两个脚本互相矛盾, 这次以那句为准。
#
# 为什么是报错退出而不是直接删掉:
# `vpn-status` 原来在 qube 不存在时打印 "WireGuard not configured" 然后**返回 0**,
# `disconnect` 更是整条带 `|| true`。也就是说凭肌肉记忆敲下来会「成功」, 而什么都没发生
# —— 正是这个项目一直在消灭的那类静默失败。留在这里硬失败, 是为了让人**立刻撞墙**
# 并看到下面该用什么。
#
# 现在的等价操作:
#   列出远端            qvm-ls --class RemoteVM
#   创建 RemoteVM       bash create-remotevm.sh --name <n> --relay <relay> --remote-name <rn>
#   自检可达            qrexec-client-vm <remotevm> qubesair.Ping
#   下线一个 zone       bash decommission-zone.sh
#   (RemoteVM 是纯元数据 qube, 不能 qvm-start)
#
# 传输不再是 WireGuard: 本地 relay 主动出站, 见 docs/roadmap-to-production.md 阶段 T
# 与 docs/remotevm-alignment.md。

set -euo pipefail

cat >&2 <<'EOF'
==============================================================
dom0-scripts/manage-qubes-air.sh 已废弃, 拒绝执行。

这个脚本的每条命令都操作 sys-remote-<zone> 及其中的 wg-quick@wg0。
sys-remote 方案已被评审否决 (违反平面分离), 相关 qube 与 salt states
都已删除 —— 这些命令的目标**不存在**。

改用:
  qvm-ls --class RemoteVM                     列出远端
  bash create-remotevm.sh --name ... --relay ...   创建
  qrexec-client-vm <remotevm> qubesair.Ping   自检可达
  bash decommission-zone.sh                   下线 zone

背景: docs/architecture.md, docs/remotevm-alignment.md
==============================================================
EOF

exit 1
