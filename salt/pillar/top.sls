# Qubes Air Pillar Top File
#
# 定义 Pillar 数据分发

base:
  # 所有 minion 获取默认配置
  '*':
    - default
  
  # [已退役] sys-remote-* minion 类已不存在 (sys-remote 方案被删)。
  # 这段和它引用的 secrets.sls WireGuard 块都是残留, 保留仅为不扩大改动范围。
  'sys-remote-*':
    - secrets
  
  # Zone admin 获取完整配置
  'zone-admin-*':
    - secrets
