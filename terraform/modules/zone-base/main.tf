# Zone Base Module
#
# 创建 Qubes Air Zone 的基础基础设施
# Zone 是一个管理边界，包含网络、安全组等资源
#
# 安全说明 (阶段1 评审红线):
#   本模块**不生成、不经手任何长期私钥**。
#   WireGuard / SSH 私钥必须在目标机 (远程 Qube / dom0) 本地生成，
#   只把**公钥**回传给控制端。Terraform 只允许接收公钥、端点等公开信息。
#   任何"用 external data source 调 wg genkey"或"把密钥种子写进 state"的做法
#   都已移除——因为 terraform state 会明文保存这些值，视同凭据泄露。

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

variable "wireguard_pubkey" {
  description = <<-EOT
    Zone 网关的 WireGuard **公钥** (仅公钥, 绝不接收私钥)。
    私钥在目标机本地生成 (wg genkey | wg pubkey), 只回传 pubkey。
    留空表示尚未纳管; 由后续阶段的 provisioning 回填。
  EOT
  type        = string
  default     = ""
}

variable "vpn_endpoint" {
  description = "Zone 网关的 VPN 可达端点 (host:port), 由 provisioning 回填"
  type        = string
  default     = ""
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
# WireGuard 密钥 —— 不在 Terraform 中生成
# ============================================
#
# 旧实现在此处有一个 random_id.wireguard_key_seed (byte_length = 32),
# 并留有"用 external data source 调 wg genkey"的注释。
# 这会把密钥/种子写进 terraform.tfstate 明文, 属评审红线, 已彻底移除。
#
# 正确流程 (由后续阶段的 salt/ansible 在目标机执行, 不属于本阶段):
#   1. 目标机: umask 077; wg genkey > privatekey; wg pubkey < privatekey > publickey
#   2. 私钥永远留在目标机 (受 LUKS / 文件权限保护), 绝不出机器
#   3. 只把 publickey 回传, 通过 var.wireguard_pubkey 注入本模块 (纯公开信息)

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
  description = "Zone WireGuard 公钥 (仅公钥, 由目标机本地生成后回传)"
  value       = var.wireguard_pubkey
}

output "vpn_endpoint" {
  description = "VPN 端点地址 (host:port)"
  value       = var.vpn_endpoint
}
