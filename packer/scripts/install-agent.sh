#!/bin/bash
# Qubes Air Agent 安装脚本 —— **已废弃, 不要再用**
#
# 2026-07: agent 不再烤进镜像。改成开机由 cloud-init 从局域网 artifact store
# 下载 qubes-air-agent_<version>_amd64.deb, 校验 SHA256 后 dpkg -i。
# 决定的来龙去脉、赔掉了什么、新增的信任依赖: docs/bootstrap-design.md §6
#
# 这个脚本装到 /opt/qubes-air/bin/, 并自己写了一份
# /etc/systemd/system/qubes-air-agent.service。.deb 装的是:
#     /usr/bin/qubes-air-agent
#     /lib/systemd/system/qubes-air-agent.service
# 两套路径完全对不上。**两份 unit 同时生效的机器是最难查的那种**:
# systemctl 显示 active, 但跑的是哪个二进制取决于 unit 加载顺序。
#
# 为什么是报错退出而不是直接删掉:
# 删掉的话, 还挂着这个脚本的 packer 模板会「构建成功」, 产出一个没有 agent
# 的镜像 —— 又一次静默失败, 而这整件事就是为了消灭这类失败才做的。
# 留在这里硬失败, 是为了让人**立刻撞墙**并看到上面这段说明。

set -euo pipefail

cat >&2 <<'EOF'
==============================================================
packer/scripts/install-agent.sh 已废弃, 拒绝执行。

agent 不再烤进镜像。现在的分发路径:

  1. make agent-deb              构建 .deb (Docker 内交叉编译 amd64)
  2. make publish-agent-deb      上传到 artifact store, 回读校验,
                                 打印 URL + SHA256
  3. 把打印的两行填进 console 配置; console 按 qube 钉死版本+哈希,
     经 cloud-init 下发, qube 开机自己装。

  (1+2 可以一步: make release-agent)

镜像里现在只需要 debian-12 + qemu-guest-agent。

背景与残留风险: docs/bootstrap-design.md §6
==============================================================
EOF

exit 1
