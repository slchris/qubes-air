# Remote Qube Base Module
#
# 创建远程 Qube (虚拟机) 的基础模块
# 支持不同 Zone 类型的抽象层

terraform {
  required_version = ">= 1.5.0"
}

# ============================================
# 变量定义
# ============================================

variable "qube_name" {
  description = "Qube 名称"
  type        = string
}

variable "qube_config" {
  description = "Qube 配置"
  type = object({
    zone         = string
    type         = string
    cpu          = optional(number, 2)
    memory       = optional(number, 4096)
    disk         = optional(number, 50)
    template     = optional(string, "fedora-39")
    machine_type = optional(string)
    gpu_type     = optional(string)
    gpu_count    = optional(number)
  })
}

variable "zone_id" {
  description = "所属 Zone ID"
  type        = string
}

# ============================================
# 本地变量
# ============================================

locals {
  # Qube 类型映射到资源配置
  qube_presets = {
    work = {
      cpu    = 4
      memory = 8192
      disk   = 100
    }
    dev = {
      cpu    = 8
      memory = 16384
      disk   = 200
    }
    gpu = {
      cpu    = 8
      memory = 32768
      disk   = 500
    }
    disp = {
      cpu    = 2
      memory = 4096
      disk   = 20
    }
  }

  # 应用预设或使用自定义值
  final_config = {
    cpu    = coalesce(var.qube_config.cpu, lookup(local.qube_presets, var.qube_config.type, {}).cpu, 2)
    memory = coalesce(var.qube_config.memory, lookup(local.qube_presets, var.qube_config.type, {}).memory, 4096)
    disk   = coalesce(var.qube_config.disk, lookup(local.qube_presets, var.qube_config.type, {}).disk, 50)
  }
}

# ============================================
# 输出
# ============================================

output "qube_name" {
  description = "Qube 名称"
  value       = var.qube_name
}

output "zone_id" {
  description = "所属 Zone ID"
  value       = var.zone_id
}

output "qube_type" {
  description = "Qube 类型"
  value       = var.qube_config.type
}

output "ip_address" {
  description = "Qube IP 地址"
  value       = "" # 由具体实现返回
}

output "status" {
  description = "Qube 状态"
  value       = "pending" # 由具体实现更新
}
