# Production Environment Configuration
#
# 用于生产环境的 Terraform 配置
# 警告: 请确保已配置适当的安全措施

environment = "production"

# ============================================
# Zone 启用配置
# ============================================

# 生产环境可启用多个 Zone
enable_proxmox_zone = true
enable_gcp_zone     = true
enable_aws_zone     = false

# ============================================
# Proxmox VE 配置
# ============================================

proxmox_config = {
  endpoint = "https://pve.example.com:8006"
  node     = "pve-prod"
}

# ============================================
# GCP 配置
# ============================================

gcp_config = {
  project = "your-gcp-project-id"
  region  = "us-central1"
  zone    = "us-central1-a"
}

# ============================================
# AWS 配置 (如需启用)
# ============================================

aws_config = {
  region = "us-east-1"
}

# ============================================
# 远程 Qube 配置
# ============================================

remote_qubes = {
  # 私有云工作站
  "work-private" = {
    zone   = "proxmox-zone"
    type   = "work"
    cpu    = 8
    memory = 16384
    disk   = 200
  }

  # 云端 GPU 开发环境
  "gpu-dev" = {
    zone         = "gcp-zone"
    type         = "gpu"
    cpu          = 8
    memory       = 32768
    disk         = 500
    machine_type = "n1-standard-8"
    gpu_type     = "nvidia-tesla-t4"
    gpu_count    = 1
  }

  # 一次性 VM (用于敏感操作)
  "disp-secure" = {
    zone   = "proxmox-zone"
    type   = "disp"
    cpu    = 2
    memory = 4096
    disk   = 20
  }
}

# ============================================
# WireGuard 配置
# ============================================

wireguard_config = {
  listen_port = 51820
  network     = "10.200.0.0/16"
}

# ============================================
# 加密配置
# ============================================

encryption_config = {
  enable_disk_encryption = true
  kms_provider           = "gcp" # 使用 GCP Cloud HSM
}
