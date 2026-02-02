# Qubes Air Salt States
#
# 这是 Qubes Air 的 SaltStack 状态配置入口
# 用于配置 sys-remote 和远程 Qube

# 状态顶层文件
# 按 Qube 类型应用不同的状态

base:
  '*':
    - qubes-air.common.base

  # sys-remote 网关配置
  'sys-remote-*':
    - qubes-air.sys-remote.gateway
    - qubes-air.sys-remote.wireguard
    - qubes-air.sys-remote.firewall

  # 远程 Qube 配置
  'remote-*':
    - qubes-air.remote-qube.base
    - qubes-air.remote-qube.agent

  # 物理 Zone 管理主机
  'zone-admin-*':
    - qubes-air.zone-admin.salt-master
    - qubes-air.zone-admin.monitoring
