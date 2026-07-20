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

  # SSH: snippet 上传唯一的通道, 见 ../../providers.tf 里那段更长的说明。
  #
  # 这里**曾经**是 `agent = true`, 那是错的, 而且是最难发现的那种错——
  # 这个文件是参考骨架 (terraform 不会递归进 providers/<name>/), 所以它自己
  # 从来不会失败, 但照抄它的人会。console 以 systemd 服务运行, 没有交互式会话,
  # 也就没有 ssh-agent, `agent = true` 必然在 apply 到一半 (VM 已 clone 完、
  # 身份还没送到) 的时候失败。
  #
  # 另外注意 bpg README 那句常被略过的话: snippet 上传要的是 **PAM 账号**,
  # 不是 API token —— 它是文件系统写入, 不是 API 调用。所以上面配了 api_token
  # 也**不能**替代这里的 SSH 凭证, 两者是不同的认证轴。
  ssh {
    agent       = false
    username    = var.proxmox_ssh_username
    private_key = var.proxmox_ssh_private_key
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

variable "proxmox_ssh_username" {
  description = <<-EOT
    节点 SSH 用户名。必须是**PAM 账号**（真实 Linux 账号），通常是 root——
    snippet 上传是文件系统写入，API token 无论权限多大都做不了这件事。
  EOT
  type        = string
  default     = "root"
}

variable "proxmox_ssh_private_key" {
  description = <<-EOT
    节点 SSH 私钥 (PEM)。经环境变量 TF_VAR_proxmox_ssh_private_key 注入，
    不写进 tfvars、不进 state——与 api_token 同一条规矩。

    留空会让 provider 回退到 ssh-agent，而 console 跑在 systemd 下没有 agent，
    于是失败发生在 VM 已经 clone 完、身份还没送到的时刻，留下一台半成品 qube。
  EOT
  type        = string
  sensitive   = true
  default     = ""
}
