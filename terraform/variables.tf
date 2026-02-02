# Qubes Air - Terraform Variables
#
# 定义所有可配置的变量

# ============================================
# 通用配置
# ============================================

variable "environment" {
  description = "部署环境 (dev/staging/production)"
  type        = string
  default     = "dev"

  validation {
    condition     = contains(["dev", "staging", "production"], var.environment)
    error_message = "environment 必须是 dev, staging 或 production"
  }
}

# ============================================
# Zone 启用配置
# ============================================

variable "enable_proxmox_zone" {
  description = "是否启用 Proxmox VE Zone"
  type        = bool
  default     = true
}

variable "enable_gcp_zone" {
  description = "是否启用 GCP Zone"
  type        = bool
  default     = false
}

variable "enable_aws_zone" {
  description = "是否启用 AWS Zone"
  type        = bool
  default     = false
}

# ============================================
# Proxmox VE 配置
# ============================================

variable "proxmox_config" {
  description = "Proxmox VE 连接配置"
  type = object({
    endpoint = string
    node     = string
    # 注意: 凭证应通过环境变量或 vault 注入
    # username 和 password 不在此定义
  })
  default = {
    endpoint = "https://pve.local:8006"
    node     = "pve"
  }
}

# ============================================
# GCP 配置
# ============================================

variable "gcp_config" {
  description = "Google Cloud 配置"
  type = object({
    project = string
    region  = string
    zone    = string
  })
  default = {
    project = ""
    region  = "us-central1"
    zone    = "us-central1-a"
  }
}

# ============================================
# AWS 配置
# ============================================

variable "aws_config" {
  description = "AWS 配置"
  type = object({
    region = string
  })
  default = {
    region = "us-east-1"
  }
}

# ============================================
# 远程 Qube 配置
# ============================================

variable "remote_qubes" {
  description = "远程 Qube 定义"
  type = map(object({
    zone     = string           # 所属 Zone
    type     = string           # Qube 类型 (work/dev/gpu/disp)
    cpu      = optional(number, 2)
    memory   = optional(number, 4096)
    disk     = optional(number, 50)
    template = optional(string, "fedora-39")
    
    # 云平台特定配置
    machine_type = optional(string)
    gpu_type     = optional(string)
    gpu_count    = optional(number)
  }))
  default = {}
}

# ============================================
# WireGuard 配置
# ============================================

variable "wireguard_config" {
  description = "WireGuard VPN 配置"
  type = object({
    listen_port = number
    network     = string
  })
  default = {
    listen_port = 51820
    network     = "10.200.0.0/16"
  }
}

# ============================================
# 加密配置
# ============================================

variable "encryption_config" {
  description = "数据加密配置"
  type = object({
    enable_disk_encryption = bool
    kms_provider          = string  # local/aws/gcp
  })
  default = {
    enable_disk_encryption = true
    kms_provider          = "local"
  }
}
