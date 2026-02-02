# Qubes Air - Terraform Outputs
#
# 定义输出值供其他模块或脚本使用

# ============================================
# Zone 输出
# ============================================

output "proxmox_zone_info" {
  description = "Proxmox Zone 信息"
  value = var.enable_proxmox_zone ? {
    zone_name = "proxmox-zone"
    zone_type = "proxmox"
    endpoint  = var.proxmox_config.endpoint
  } : null
}

output "gcp_zone_info" {
  description = "GCP Zone 信息"
  value = var.enable_gcp_zone ? {
    zone_name = "gcp-zone"
    zone_type = "gcp"
    project   = var.gcp_config.project
    region    = var.gcp_config.region
  } : null
}

output "aws_zone_info" {
  description = "AWS Zone 信息"
  value = var.enable_aws_zone ? {
    zone_name = "aws-zone"
    zone_type = "aws"
    region    = var.aws_config.region
  } : null
}

# ============================================
# 远程 Qube 输出
# ============================================

output "remote_qubes" {
  description = "已创建的远程 Qube 列表"
  value = {
    for name, qube in module.remote_qubes : name => {
      zone       = qube.zone_id
      type       = qube.qube_type
      ip_address = qube.ip_address
      status     = qube.status
    }
  }
}

# ============================================
# WireGuard 输出
# ============================================

output "wireguard_config" {
  description = "WireGuard 配置信息"
  value = {
    network     = var.wireguard_config.network
    listen_port = var.wireguard_config.listen_port
  }
}

# ============================================
# 连接信息 (敏感)
# ============================================

output "connection_info" {
  description = "Zone 连接信息"
  sensitive   = true
  value = {
    zones = {
      proxmox = var.enable_proxmox_zone ? {
        wireguard_pubkey = module.proxmox_zone[0].wireguard_pubkey
        endpoint         = module.proxmox_zone[0].vpn_endpoint
      } : null
      gcp = var.enable_gcp_zone ? {
        wireguard_pubkey = module.gcp_zone[0].wireguard_pubkey
        endpoint         = module.gcp_zone[0].vpn_endpoint
      } : null
      aws = var.enable_aws_zone ? {
        wireguard_pubkey = module.aws_zone[0].wireguard_pubkey
        endpoint         = module.aws_zone[0].vpn_endpoint
      } : null
    }
  }
}
