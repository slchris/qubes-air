# sys-remote Firewall State
#
# 配置 sys-remote 防火墙规则

# ============================================
# nftables 配置
# ============================================

/etc/nftables.conf:
  file.managed:
    - source: salt://qubes-air/sys-remote/files/nftables.conf.j2
    - template: jinja
    - user: root
    - group: root
    - mode: 600

nftables:
  service.running:
    - enable: True
    - watch:
      - file: /etc/nftables.conf

# ============================================
# Qubes 防火墙集成
# ============================================

# 自定义防火墙脚本
/rw/config/qubes-firewall-user-script:
  file.managed:
    - contents: |
        #!/bin/bash
        # Qubes Air sys-remote 防火墙自定义规则
        
        # 允许 WireGuard 流量
        nft add rule qubes custom-input udp dport {{ pillar.get('wireguard', {}).get('listen_port', 51820) }} accept
        
        # 允许从远程 Zone 到本地 Qube 的流量 (通过 WireGuard)
        nft add rule qubes custom-forward iifname "wg0" accept
        nft add rule qubes custom-forward oifname "wg0" accept
        
        # 日志丢弃的流量 (调试用)
        # nft add rule qubes custom-input log prefix "DROP-IN: "
        # nft add rule qubes custom-forward log prefix "DROP-FWD: "
    - user: root
    - group: root
    - mode: 755
    - makedirs: True
