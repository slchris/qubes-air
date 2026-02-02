# sys-remote Gateway State
#
# 配置 sys-remote Qube 作为远程 Zone 的网关

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
