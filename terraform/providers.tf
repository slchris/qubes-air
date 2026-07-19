# Qubes Air - 根模块 Provider 配置
#
# Terraform 只加载工作目录下的 *.tf, 不会递归加载 providers/<name>/ 子目录,
# 所以真正生效的 provider 配置必须放在根模块 (这里)。
# providers/<name>/provider.tf 保留作为各 provider 的参考/文档骨架。
#
# 凭据注入 (评审红线):
#   - proxmox api_token 只经**环境变量或 gitignore 的 tfvars**注入, sensitive=true。
#     环境变量方式 (推荐, 不落盘):  export TF_VAR_proxmox_api_token='user@pam!tok=secret'
#   - 绝不硬编码, 绝不写进示例 tfvars 明文。

# ============================================
# Proxmox VE Provider (阶段1 真实实现)
# ============================================

provider "proxmox" {
  # endpoint 的唯一真源是 tfvars 里的 proxmox_config.endpoint。
  # var.proxmox_endpoint 仅作为环境变量逃生口 (TF_VAR_proxmox_endpoint), 留空则不生效。
  #
  # 历史坑 (已修): 此处曾直接写 var.proxmox_endpoint 且该变量 default 为
  # "https://pve.local:8006/"。于是 tfvars 里的 proxmox_config.endpoint 根本没接到
  # provider 上 —— 改 tfvars 毫无作用, provider 始终去连 pve.local, 而
  # `terraform output` 又照样回显你填的值。plan 阶段不建连接, 所以只在 apply 才炸。
  endpoint  = coalesce(var.proxmox_endpoint, var.proxmox_config.endpoint)
  api_token = var.proxmox_api_token
  insecure  = var.proxmox_insecure

  # bpg/proxmox 的部分操作 (如磁盘 import、file 上传) 需要 SSH 到 PVE 节点。
  # 使用本地 ssh-agent, 不在 Terraform 中经手 SSH 私钥。
  # 注意: clone 模板 + cloud-init 这条路径**不走 SSH**, 所以 PVE 只开放 443
  # (反代后无 22 端口) 时依然可用。一旦改用 file_id/import_from 导入磁盘才会需要 SSH。
  ssh {
    agent = true
  }
}

variable "proxmox_endpoint" {
  description = <<-EOT
    Proxmox VE API 端点覆盖项 (如 https://pve.example.com/)。
    留空 (默认) 则使用 proxmox_config.endpoint —— tfvars 才是唯一真源。
    注意 bpg/proxmox 不会自动补 :8006, 端点原样使用; 也不要带 /api2/json 后缀。
  EOT
  type        = string
  default     = null
}

variable "proxmox_api_token" {
  description = <<-EOT
    Proxmox API Token, 格式: user@realm!tokenid=token-secret
    只经环境变量 (TF_VAR_proxmox_api_token) 或 gitignore 的 tfvars 注入。
  EOT
  type        = string
  sensitive   = true
  default     = ""
}

variable "proxmox_insecure" {
  description = "是否允许不安全的 TLS 连接 (自签证书时设 true)"
  type        = bool
  default     = false
}

# ============================================
# GCP / AWS Provider —— 阶段1 为骨架, 暂不在根模块配置。
#
# GCP/AWS 子模块目前不建真实资源 (纯占位输出), 因此无需配置 provider,
# validate 也能通过。待阶段2 落地真实资源时, 在此启用对应 provider 块,
# 凭据同样只经环境变量注入:
#   GCP: GOOGLE_APPLICATION_CREDENTIALS / gcloud ADC
#   AWS: AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY / IAM Role
# ============================================
