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
  description = "远程 Qube 定义 (存算分离)"
  type = map(object({
    zone = string # 所属 Zone
    type = string # Qube 类型 (work/dev/gpu/disp)

    # ---- 存算分离核心开关 ----
    # compute_running: true=计算实例存在(花钱); false=销毁计算保留数据(省钱)
    #   改为 false 再 apply = suspend (释放计算, 保留数据盘)
    #   改回 true  再 apply = resume  (重建计算, 挂回同一数据盘)
    compute_running = optional(bool, true)
    # data_disk_gb: 独立持久数据盘大小(GB), 与计算实例解耦、受 prevent_destroy 保护
    data_disk_gb = optional(number, 50)

    # ---- 计算规格 ----
    # 留空则套用 qube type 的 preset (work/dev/gpu/disp), 显式写值则覆盖 preset。
    # 不能给默认值 —— 见 modules/remote-qube-base/main.tf 里的说明。
    cpu    = optional(number)
    memory = optional(number)
    disk   = optional(number) # os/root 盘大小 (随实例销毁重建)

    # 注: 原有的 template = optional(string, "fedora-39") 已删除。它在全树没有任何消费者
    # (只有 template_vm_id 真正生效), 留着会让人误以为能靠它选 OS。

    # ---- 云平台特定配置 ----
    machine_type = optional(string)
    gpu_type     = optional(string)
    gpu_count    = optional(number)

    # ---- Proxmox 特定配置 ----
    node_name       = optional(string) # Proxmox 节点名; 留空用 proxmox_config.node
    template_vm_id  = optional(number) # clone 用的模板 VM ID
    datastore_id    = optional(string, "local-lvm")
    network_bridge  = optional(string, "vmbr0")
    ssh_public_keys = optional(list(string), []) # cloud-init 注入的**公钥** (绝不含私钥)
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
    kms_provider           = string # local/aws/gcp
  })
  default = {
    enable_disk_encryption = true
    kms_provider           = "local"
  }
}
