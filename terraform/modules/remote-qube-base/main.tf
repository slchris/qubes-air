# Remote Qube Base Module
#
# 创建远程 Qube (虚拟机) 的基础模块 —— 存算分离 (compute/storage separation)。
#
# 核心设计 (阶段1 地基):
#   一台远程 Qube = "计算实例 (compute)" + "独立数据盘 (storage)" 两个**解耦**的部分。
#     - compute: 由布尔开关 `compute_running` 控制存在与否。
#         compute_running = true  -> 创建计算实例 (花钱)
#         compute_running = false -> 销毁计算实例 (省钱), 数据盘保留 (不丢数据)
#     - storage: 独立的持久数据盘, 带 lifecycle.prevent_destroy, 与 compute 生命周期解耦。
#         root/os 盘可随实例销毁重建; 独立 data 盘持久存活。
#
#   FinOps 语义: suspend = 销毁 compute + 保留 storage; resume = 重建 compute + 挂回同一 storage。
#
# 本模块把 provider 差异下沉到 providers/<name> 子模块, 三家 (proxmox/gcp/aws)
# 对外接口一致: 同样的 compute_running 开关, 同样的独立 data 盘, 同样的输出契约。
#   - proxmox: 真实可 apply (bpg/proxmox 真实 resource)
#   - gcp/aws: 结构对齐的骨架 (TODO), 接口与 proxmox 完全一致

terraform {
  required_version = ">= 1.5.0"

  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.60.0"
    }
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0"
    }
  }
}

# ============================================
# 变量定义
# ============================================

variable "qube_name" {
  description = "Qube 名称"
  type        = string
}

variable "qube_config" {
  description = "Qube 配置 (含存算分离开关)"
  type = object({
    zone = string # 所属 Zone
    type = string # Qube 类型 (work/dev/gpu/disp)

    # ---- 存算分离核心开关 ----
    compute_running = optional(bool, true) # true=计算实例存在(花钱); false=销毁计算保留数据(省钱)
    data_disk_gb    = optional(number, 50) # 独立持久数据盘大小(GB), 与 compute 解耦、prevent_destroy 保护

    # ---- 计算规格 ----
    # 注意: 这三个**故意不给默认值**。optional() 一旦带非 null 默认值, 值就永远不是 null,
    # 下面 local.final_config 里的 coalesce(var.qube_config.cpu, local.preset.cpu) 会永远
    # 短路在类型默认值上, 导致 local.qube_presets 整张表不可达 —— 写 type="gpu" 实际只拿到
    # 2c/4G。留空(null)才能让 preset 生效; 显式写值仍然覆盖 preset。
    cpu    = optional(number)
    memory = optional(number)
    disk   = optional(number) # os/root 盘大小, 可随实例销毁重建

    # ---- provider 特定 ----
    machine_type = optional(string)
    gpu_type     = optional(string)
    gpu_count    = optional(number)

    # ---- Proxmox 特定 ----
    node_name            = optional(string) # Proxmox 节点名; 留空则用 var.proxmox_default_node
    template_vm_id       = optional(number) # 用于 clone 的模板 VM ID
    template_node_name   = optional(string) # 模板所在节点 (与放置节点不同时必填)
    datastore_id         = optional(string, "local-lvm")
    network_bridge       = optional(string, "vmbr0")
    ssh_public_keys      = optional(list(string), []) # cloud-init 注入的**公钥** (绝不含私钥)
    agent_user_data_file = optional(string, "")       # agent 身份 user-data 的本地路径

    # ---- GCP 特定 ----
    # gcp_zone 是必填 (实例与数据盘必须同 zone 才挂得上), 但这里给 null 默认值:
    # 一个 proxmox qube 不该因为缺 GCP 字段而渲染失败。缺失在 GCP 子模块里报错。
    gcp_zone     = optional(string)
    source_image = optional(string, "debian-cloud/debian-12")
    # 身份文档经私有 GCS bucket 投递 —— 放 metadata 会把 agent 私钥写进 state。
    identity_bucket       = optional(string, "")
    service_account_email = optional(string, "")
    network               = optional(string, "default")
    subnetwork            = optional(string)
    # 默认不给公网 IP: 控制台经私有路径 (WireGuard) 拨 agent。
    assign_public_ip = optional(bool, false)
  })
}

variable "zone_id" {
  description = "所属 Zone ID"
  type        = string
}

variable "provider_type" {
  description = "该 Qube 落地的 provider (proxmox/gcp/aws)"
  type        = string
  default     = "proxmox"

  validation {
    condition     = contains(["proxmox", "gcp", "aws"], var.provider_type)
    error_message = "provider_type 必须是 proxmox, gcp 或 aws"
  }
}

variable "proxmox_default_node" {
  description = "Proxmox 默认节点名 (qube_config.node_name 未指定时使用)"
  type        = string
  default     = "pve"
}

# ============================================
# 本地变量: 预设 + 最终规格
# ============================================

locals {
  # Qube 类型映射到资源配置
  qube_presets = {
    work = { cpu = 4, memory = 8192, disk = 100 }
    dev  = { cpu = 8, memory = 16384, disk = 200 }
    gpu  = { cpu = 8, memory = 32768, disk = 500 }
    disp = { cpu = 2, memory = 4096, disk = 20 }
  }

  preset = lookup(local.qube_presets, var.qube_config.type, { cpu = 2, memory = 4096, disk = 50 })

  # 应用预设或使用自定义值
  final_config = {
    cpu             = coalesce(var.qube_config.cpu, local.preset.cpu)
    memory          = coalesce(var.qube_config.memory, local.preset.memory)
    disk            = coalesce(var.qube_config.disk, local.preset.disk)
    data_disk_gb    = var.qube_config.data_disk_gb
    compute_running = var.qube_config.compute_running
  }
}

# ============================================
# Provider 分派 (dispatch)
#
# 三家 provider 子模块接口一致; 用 count 按 provider_type 选一个落地。
# 只有 proxmox 是真实实现; gcp/aws 是接口对齐的骨架 (TODO)。
# ============================================

module "proxmox" {
  source = "./providers/proxmox"
  count  = var.provider_type == "proxmox" ? 1 : 0

  qube_name       = var.qube_name
  compute_running = local.final_config.compute_running
  cpu             = local.final_config.cpu
  memory          = local.final_config.memory
  os_disk_gb      = local.final_config.disk
  data_disk_gb    = local.final_config.data_disk_gb

  node_name            = coalesce(var.qube_config.node_name, var.proxmox_default_node)
  template_vm_id       = var.qube_config.template_vm_id
  # NOT coalesce(). coalesce() errors when every argument is empty, and "" is
  # itself empty — so `coalesce(x, "")` cannot succeed when x is unset. It only
  # ever returned x, and failed the whole apply otherwise, with "Error in
  # function call" pointing at this line rather than at the missing setting.
  # Empty is a legitimate value here: it means "the template lives on the node
  # the clone is called against".
  template_node_name   = var.qube_config.template_node_name == null ? "" : var.qube_config.template_node_name
  datastore_id         = var.qube_config.datastore_id
  network_bridge       = var.qube_config.network_bridge
  ssh_public_keys      = var.qube_config.ssh_public_keys
  agent_user_data_file = var.qube_config.agent_user_data_file
  qube_type            = var.qube_config.type
}

module "gcp" {
  source = "./providers/gcp"
  count  = var.provider_type == "gcp" ? 1 : 0

  qube_name       = var.qube_name
  compute_running = local.final_config.compute_running
  cpu             = local.final_config.cpu
  memory          = local.final_config.memory
  os_disk_gb      = local.final_config.disk
  data_disk_gb    = local.final_config.data_disk_gb
  machine_type    = var.qube_config.machine_type
  gpu_type        = var.qube_config.gpu_type
  gpu_count       = var.qube_config.gpu_count
  ssh_public_keys = var.qube_config.ssh_public_keys

  # The agent identity was never passed to GCP at all, so a GCP qube had no way
  # to authenticate even once the module built something. Same value the proxmox
  # branch gets; how it reaches the VM differs (GCS object vs snippet) because
  # putting it in GCP instance metadata would write the private key into state.
  zone                  = var.qube_config.gcp_zone
  source_image          = var.qube_config.source_image
  agent_user_data_file  = var.qube_config.agent_user_data_file
  identity_bucket       = var.qube_config.identity_bucket
  service_account_email = var.qube_config.service_account_email
  network               = var.qube_config.network
  subnetwork            = var.qube_config.subnetwork
  assign_public_ip      = var.qube_config.assign_public_ip
}

module "aws" {
  source = "./providers/aws"
  count  = var.provider_type == "aws" ? 1 : 0

  qube_name       = var.qube_name
  compute_running = local.final_config.compute_running
  cpu             = local.final_config.cpu
  memory          = local.final_config.memory
  os_disk_gb      = local.final_config.disk
  data_disk_gb    = local.final_config.data_disk_gb
  machine_type    = var.qube_config.machine_type
  ssh_public_keys = var.qube_config.ssh_public_keys
}

# ============================================
# 统一输出契约 (三家一致)
# ============================================

locals {
  # 从被选中的 provider 子模块聚合输出 (未选中的返回 null / 默认值)
  active = coalesce(
    one(module.proxmox[*].result),
    one(module.gcp[*].result),
    one(module.aws[*].result),
  )
}

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

output "compute_running" {
  description = "计算实例是否在运行 (存算分离开关的当前值)"
  value       = local.final_config.compute_running
}

output "data_disk_id" {
  description = "独立持久数据盘 ID (compute 销毁后依然存在)"
  value       = local.active.data_disk_id
}

output "ip_address" {
  description = "计算实例可达地址 (供后续 SSH transport 使用; compute 未运行时为空)"
  value       = local.active.ip_address
}

output "status" {
  description = "Qube 状态 (running / suspended)"
  value       = local.final_config.compute_running ? local.active.status : "suspended"
}
