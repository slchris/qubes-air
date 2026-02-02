# Qubes Air Pillar - Default Configuration
#
# 默认 Pillar 数据配置

# Zone 默认配置
zone_name: default
zone_type: local

# Qube 默认配置  
qube_type: generic

# WireGuard 默认配置
wireguard_enabled: false

# 加密配置
disk_encryption: true
kms_provider: local

# 日志级别
log_level: info

# Salt 配置
salt:
  minion:
    master: dom0
