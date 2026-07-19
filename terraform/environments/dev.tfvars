# Development Environment Configuration
#
# 用于开发测试的 Terraform 配置

environment = "dev"

# ============================================
# Zone 启用配置
# ============================================

# 开发环境默认只启用 Proxmox
enable_proxmox_zone = true
enable_gcp_zone     = false
enable_aws_zone     = false

# ============================================
# Proxmox VE 配置
# ============================================

proxmox_config = {
  endpoint = "https://pve.local:8006"
  node     = "pve"
}

# ============================================
# 远程 Qube 配置 (存算分离示例)
# ============================================
#
# 存算分离用法 (FinOps 核心):
#   compute_running = true  -> 计算实例存在 (花钱)
#   compute_running = false -> 销毁计算实例、保留 data 盘 (省钱, 不丢数据)
#
#   一句话 suspend/resume (无需改文件):
#     make tf-suspend QUBE=dev-work   # 释放计算, 保留数据
#     make tf-resume  QUBE=dev-work   # 重建计算, 挂回同一数据盘
#   或直接: terraform apply -var-file=environments/dev.tfvars \
#             -var='remote_qubes={...compute_running=false...}'
#
# 注意 (Proxmox 真机 apply 前必须补):
#   - template_vm_id: 指向 PVE 上已存在的 cloud-init 模板 VM ID (如 9000)
#   - node_name / datastore_id / network_bridge: 按你的 PVE 环境改
#   - ssh_public_keys: 只放**公钥**; 私钥在 dom0 本地生成、绝不进此文件

remote_qubes = {
  # 存算分离示例: 一台常驻工作站
  #   把下面的 compute_running 改成 false 再 apply = 释放计算保留数据。
  "dev-work" = {
    zone            = "proxmox-zone"
    type            = "work"
    compute_running = true # <-- 改为 false 再 apply = suspend (省钱, 数据盘保留)
    data_disk_gb    = 100  # 独立持久数据盘 (prevent_destroy 保护, 不随 compute 销毁)
    cpu             = 4
    memory          = 8192
    disk            = 32 # os/root 盘 (随实例销毁重建)

    # --- Proxmox 真机字段 (apply 前按环境填写) ---
    template_vm_id  = 9000 # TODO: 改成你 PVE 上的模板 VM ID
    node_name       = "pve"
    datastore_id    = "local-lvm"
    network_bridge  = "vmbr0"
    ssh_public_keys = [] # 例: ["ssh-ed25519 AAAA... qubes-dom0"] (仅公钥)
  }

  # 一次性 disp VM: 计算 disp 语义天然短命, 数据盘也小
  "dev-disp" = {
    zone            = "proxmox-zone"
    type            = "disp"
    compute_running = true
    data_disk_gb    = 10
    cpu             = 2
    memory          = 4096
    disk            = 20

    template_vm_id = 9001 # TODO: 改成你 PVE 上的 minimal 模板 VM ID
    node_name      = "pve"
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
  kms_provider           = "local"
}
