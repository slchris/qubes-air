# Qubes Air - Main Terraform Configuration
# 
# 这是 Qubes Air 项目的主 Terraform 配置文件
# 用于编排多 Zone 的远程 Qube 基础设施

terraform {
  required_version = ">= 1.5.0"

  required_providers {
    # Proxmox VE Provider
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.38.0"
    }
    # Google Cloud Provider
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0.0"
    }
    # AWS Provider
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0.0"
    }
    # Random Provider (用于生成随机值)
    random = {
      source  = "hashicorp/random"
      version = ">= 3.5.0"
    }
  }
}

# 本地变量
locals {
  # 项目标签
  common_tags = {
    Project     = "qubes-air"
    ManagedBy   = "terraform"
    Environment = var.environment
  }
}

# Zone 模块实例化
# 根据配置创建各个 Zone

# Proxmox VE Zone (私有云)
module "proxmox_zone" {
  source = "./modules/zone-base"
  count  = var.enable_proxmox_zone ? 1 : 0

  zone_name    = "proxmox-zone"
  zone_type    = "proxmox"
  provider_config = var.proxmox_config
  
  tags = local.common_tags
}

# GCP Zone (公有云)
module "gcp_zone" {
  source = "./modules/zone-base"
  count  = var.enable_gcp_zone ? 1 : 0

  zone_name    = "gcp-zone"
  zone_type    = "gcp"
  provider_config = var.gcp_config
  
  tags = local.common_tags
}

# AWS Zone (公有云)
module "aws_zone" {
  source = "./modules/zone-base"
  count  = var.enable_aws_zone ? 1 : 0

  zone_name    = "aws-zone"
  zone_type    = "aws"
  provider_config = var.aws_config
  
  tags = local.common_tags
}

# 远程 Qube 实例
module "remote_qubes" {
  source   = "./modules/remote-qube-base"
  for_each = var.remote_qubes

  qube_name   = each.key
  qube_config = each.value
  zone_id     = each.value.zone
  
  depends_on = [
    module.proxmox_zone,
    module.gcp_zone,
    module.aws_zone
  ]
}
