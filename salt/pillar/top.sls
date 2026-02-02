# Qubes Air Pillar Top File
#
# 定义 Pillar 数据分发

base:
  # 所有 minion 获取默认配置
  '*':
    - default
  
  # sys-remote 获取 WireGuard 和 secrets 配置
  'sys-remote-*':
    - secrets
  
  # Zone admin 获取完整配置
  'zone-admin-*':
    - secrets
