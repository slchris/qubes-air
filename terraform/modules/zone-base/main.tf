# Zone Base Module
#
# 创建 Qubes Air Zone 的基础基础设施
# Zone 是一个管理边界，包含网络、安全组等资源

terraform {
  required_version = ">= 1.5.0"
}

# ============================================
# 变量定义
# ============================================

variable "zone_name" {
  description = "Zone 名称"
  type        = string
}

variable "zone_type" {
  description = "Zone 类型 (proxmox/gcp/aws)"
  type        = string
}

variable "provider_config" {
  description = "Provider 特定配置"
  type        = any
}

variable "tags" {
  description = "资源标签"
  type        = map(string)
  default     = {}
}

# ============================================
# 本地变量
# ============================================

locals {
  zone_tags = merge(var.tags, {
    ZoneName = var.zone_name
    ZoneType = var.zone_type
  })
}

# ============================================
# WireGuard 密钥生成
# ============================================

resource "random_id" "wireguard_key_seed" {
  byte_length = 32
}

# 注意: 实际部署中应使用 external data source 调用 wg genkey
# 此处为示例配置

# ============================================
# 输出
# ============================================

output "zone_id" {
  description = "Zone ID"
  value       = var.zone_name
}

output "zone_type" {
  description = "Zone 类型"
  value       = var.zone_type
}

output "wireguard_pubkey" {
  description = "Zone WireGuard 公钥"
  value       = "" # 由具体 provider 实现生成
}

output "vpn_endpoint" {
  description = "VPN 端点地址"
  value       = "" # 由具体 provider 实现返回
}
