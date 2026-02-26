# Proxmox VE Provider 配置
#
# 用于连接 Proxmox VE 集群

terraform {
  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.38.0"
    }
  }
}

# ============================================
# Provider 配置
# ============================================

provider "proxmox" {
  endpoint = var.proxmox_endpoint

  # 认证配置 - 使用 API Token (推荐)
  api_token = var.proxmox_api_token

  # 或使用用户名密码 (不推荐)
  # username = var.proxmox_username
  # password = var.proxmox_password

  # TLS 配置
  insecure = var.proxmox_insecure

  ssh {
    agent = true
  }
}

# ============================================
# 变量定义
# ============================================

variable "proxmox_endpoint" {
  description = "Proxmox VE API 端点"
  type        = string
}

variable "proxmox_api_token" {
  description = "Proxmox API Token (格式: user@realm!tokenid=token-secret)"
  type        = string
  sensitive   = true
  default     = ""
}

variable "proxmox_insecure" {
  description = "是否允许不安全的 TLS 连接"
  type        = bool
  default     = false
}
