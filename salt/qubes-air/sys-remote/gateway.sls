# sys-remote Gateway State
#
# ############################################################################
# 【已废弃 - 阶段2】本 state 属于旧 sys-remote + WireGuard 网关方案, 已被评审否决:
#   - 开 ip_forward + provides_network 把 Relay 当本地网关 (违反平面分离);
#   - 部署 qubes-air.Remote (任意命令通道) 是反模式。
# 新链路 (RemoteVM + SSH transport) 见 salt/qubes-air/remotevm/。
# 保留此文件仅为历史参考; 不要在新部署里应用它。qubes-air.Remote 服务应删除。
# ############################################################################

include:
  - qubes-air.common.base

# ============================================
# 网关软件包
# ============================================

sys-remote-gateway-packages:
  pkg.installed:
    - pkgs:
      - wireguard-tools
      - iptables
      - nftables
      - socat

# ============================================
# IP 转发配置
# ============================================

net.ipv4.ip_forward:
  sysctl.present:
    - value: 1

net.ipv6.conf.all.forwarding:
  sysctl.present:
    - value: 1

# ============================================
# qrexec 服务目录
# ============================================

/etc/qubes-rpc:
  file.directory:
    - user: root
    - group: root
    - mode: 755

# ============================================
# qrexec 远程执行服务
# ============================================

/etc/qubes-rpc/qubes-air.Remote:
  file.managed:
    - contents: |
        #!/bin/bash
        # Qubes Air 远程命令执行服务
        # 通过 qrexec 安全通道接收命令
        
        exec /opt/qubes-air/bin/remote-handler
    - user: root
    - group: root
    - mode: 755
    - require:
      - file: /etc/qubes-rpc
