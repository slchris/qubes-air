# sys-remote WireGuard State
#
# 配置 WireGuard VPN 连接到远程 Zone

# ============================================
# WireGuard 配置目录
# ============================================

/etc/wireguard:
  file.directory:
    - user: root
    - group: root
    - mode: 700

# ============================================
# WireGuard 配置文件
# ============================================

/etc/wireguard/wg0.conf:
  file.managed:
    - source: salt://qubes-air/sys-remote/files/wg0.conf.j2
    - template: jinja
    - user: root
    - group: root
    - mode: 600
    - require:
      - file: /etc/wireguard

# ============================================
# WireGuard 服务
# ============================================

wg-quick@wg0:
  service.running:
    - enable: True
    - watch:
      - file: /etc/wireguard/wg0.conf

# ============================================
# WireGuard 密钥生成脚本
# ============================================

/opt/qubes-air/bin/wg-keygen.sh:
  file.managed:
    - contents: |
        #!/bin/bash
        # WireGuard 密钥生成脚本
        
        set -euo pipefail
        
        KEY_DIR="/etc/wireguard/keys"
        mkdir -p "$KEY_DIR"
        chmod 700 "$KEY_DIR"
        
        if [ ! -f "$KEY_DIR/private.key" ]; then
            wg genkey > "$KEY_DIR/private.key"
            chmod 600 "$KEY_DIR/private.key"
        fi
        
        if [ ! -f "$KEY_DIR/public.key" ]; then
            cat "$KEY_DIR/private.key" | wg pubkey > "$KEY_DIR/public.key"
        fi
        
        echo "Public Key: $(cat $KEY_DIR/public.key)"
    - user: root
    - group: root
    - mode: 755
    - require:
      - file: /opt/qubes-air/bin

# ============================================
# 健康检查脚本
# ============================================

/opt/qubes-air/bin/wg-health.sh:
  file.managed:
    - contents: |
        #!/bin/bash
        # WireGuard 连接健康检查
        
        if wg show wg0 &>/dev/null; then
            HANDSHAKE=$(wg show wg0 latest-handshakes | awk '{print $2}')
            NOW=$(date +%s)
            AGE=$((NOW - HANDSHAKE))
            
            if [ $AGE -lt 180 ]; then
                echo "OK: Last handshake ${AGE}s ago"
                exit 0
            else
                echo "WARNING: No recent handshake (${AGE}s)"
                exit 1
            fi
        else
            echo "ERROR: WireGuard interface not found"
            exit 2
        fi
    - user: root
    - group: root
    - mode: 755
    - require:
      - file: /opt/qubes-air/bin
