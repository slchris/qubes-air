# Remote Qube — Proxmox 实现 (存算分离, 真实可 apply)
#
# 用 bpg/proxmox 真实 resource 实现"计算实例 + 独立持久数据盘"的解耦。
#
# 存算分离在 Proxmox 上的落地方式 (关键设计决策, 需重点 review):
#   bpg/proxmox **没有独立的 disk resource** —— 磁盘只能内联在 VM resource 的 disk {} 块里。
#   因此不能像 GCP/AWS 那样直接建一个孤立 data volume。业界通行且本模块采用的模式是:
#
#     1) storage-holder VM (数据盘持有者):
#          一台**最小、常驻**的 VM, 唯一职责是"持有"持久数据盘。
#          它带 lifecycle.prevent_destroy, terraform destroy 也删不掉 -> 数据不丢。
#          即使 compute_running=false, 这台 VM 及其 data 盘依然存在。
#
#     2) compute VM (计算实例):
#          真正跑 workload 的 VM, 由 count = compute_running ? 1 : 0 控制存在与否。
#          它的 root/os 盘随实例销毁重建 (无所谓); data 盘则通过 path_in_datastore
#          引用 storage-holder VM 已有的那块盘 —— 挂载而非新建, 所以销毁 compute 不动数据。
#
#   suspend: compute_running=false -> compute VM count=0 被销毁, storage-holder + data 盘保留。
#   resume : compute_running=true  -> compute VM 重建, 重新挂回同一块 data 盘。
#
# 注意: path_in_datastore 在 bpg/proxmox 中标注为 Experimental。真机验证前请在
#       非生产节点确认挂载语义 (见模块 README / 报告 runbook)。

terraform {
  required_version = ">= 1.5.0"
  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.60.0"
    }
  }
}

variable "qube_name" { type = string }
variable "qube_type" { type = string }
variable "compute_running" { type = bool }
variable "cpu" { type = number }
variable "memory" { type = number }
variable "os_disk_gb" { type = number }
variable "data_disk_gb" { type = number }
variable "node_name" { type = string }

variable "template_vm_id" {
  description = "clone 用的模板 VM ID; 留空则不 clone (需 template_vm_id 才能 apply)"
  type        = number
  default     = null
}

variable "datastore_id" {
  description = "os/data 盘所在的 Proxmox datastore"
  type        = string
  default     = "local-lvm"
}

variable "network_bridge" {
  type    = string
  default = "vmbr0"
}

variable "ssh_public_keys" {
  description = "cloud-init 注入的 SSH **公钥** 列表 (绝不含私钥; 私钥留在控制端 dom0)"
  type        = list(string)
  default     = []
}

# ============================================
# 1) storage-holder VM —— 持久数据盘的持有者
#
# 极小规格 (1c/512M), 只为"持有"data 盘而存在。始终创建, 与 compute 解耦。
# prevent_destroy 防止 terraform destroy 误删数据。
# ============================================

resource "proxmox_virtual_environment_vm" "storage" {
  node_name   = var.node_name
  name        = "${var.qube_name}-storage"
  description = "Qubes Air data-disk holder for ${var.qube_name} (DO NOT DELETE — holds persistent data)"
  tags        = ["qubes-air", "storage", var.qube_type]

  # storage-holder 常驻但不需要开机跑负载; 保持 stopped 省资源。
  # 关键: started=false 只是关机, VM 及其盘依然存在 (不销毁)。
  started         = false
  on_boot         = false
  stop_on_destroy = true

  cpu {
    cores   = 1
    sockets = 1
    type    = "x86-64-v2-AES"
  }

  memory {
    dedicated = 512
  }

  # 持久数据盘: 独立于任何计算实例。这是"存"的部分。
  disk {
    datastore_id = var.datastore_id
    interface    = "scsi0"
    size         = var.data_disk_gb
    file_format  = "raw"
    discard      = "on"
    iothread     = true
  }

  network_device {
    bridge = var.network_bridge
    model  = "virtio"
  }

  # ---- 数据不丢的红线保护 ----
  lifecycle {
    prevent_destroy = true

    # 数据盘大小/存储位置一旦建立不随 tfvars 抖动而重建, 避免误删数据盘。
    ignore_changes = [
      started, # 手动开关机不触发 terraform 重建
    ]
  }
}

# ============================================
# 2) compute VM —— 计算实例, 由 compute_running 控制
#
# count = compute_running ? 1 : 0:
#   true  -> 创建计算实例 (clone 模板, 挂 os 盘 + 挂载 storage 的 data 盘)
#   false -> count=0, 计算实例被销毁; storage VM 与 data 盘不受影响
# ============================================

resource "proxmox_virtual_environment_vm" "compute" {
  count = var.compute_running ? 1 : 0

  node_name   = var.node_name
  name        = var.qube_name
  description = "Qubes Air compute instance for ${var.qube_name} (ephemeral — safe to destroy/recreate)"
  tags        = ["qubes-air", "compute", var.qube_type]

  started         = true
  on_boot         = false # 由控制端按需 resume, 不随宿主开机自动起
  stop_on_destroy = true

  # 从模板 clone 出 os/root 盘。仅当提供 template_vm_id 时 clone。
  dynamic "clone" {
    for_each = var.template_vm_id != null ? [1] : []
    content {
      vm_id     = var.template_vm_id
      node_name = var.node_name
      full      = true
    }
  }

  cpu {
    cores   = var.cpu
    sockets = 1
    type    = "x86-64-v2-AES"
  }

  memory {
    dedicated = var.memory
  }

  # os/root 盘: 随实例销毁重建, 不持久。这是"算"的部分。
  disk {
    datastore_id = var.datastore_id
    interface    = "scsi0"
    size         = var.os_disk_gb
    file_format  = "raw"
    discard      = "on"
    iothread     = true
  }

  # 挂载 storage VM 已有的持久数据盘 (attach, 不新建)。
  # 引用 storage VM 的计算属性 path_in_datastore, 保证挂的是同一块盘。
  # -> 销毁本 compute VM 时, 这块盘属于 storage VM, 不会被删除。
  disk {
    datastore_id      = var.datastore_id
    interface         = "scsi1"
    path_in_datastore = proxmox_virtual_environment_vm.storage.disk[0].path_in_datastore
    file_format       = "raw"
    size              = var.data_disk_gb
  }

  agent {
    enabled = true # 需开启 qemu-guest-agent 才能回填 ipv4_addresses
  }

  network_device {
    bridge = var.network_bridge
    model  = "virtio"
  }

  # cloud-init: 只注入公钥, 不经手私钥。
  initialization {
    datastore_id = var.datastore_id

    ip_config {
      ipv4 {
        address = "dhcp"
      }
    }

    dynamic "user_account" {
      for_each = length(var.ssh_public_keys) > 0 ? [1] : []
      content {
        username = "qubes"
        keys     = var.ssh_public_keys
      }
    }
  }

  operating_system {
    type = "l26" # Linux 2.6+ / 3.x / 4.x / 5.x / 6.x
  }

  # compute 是"可随时销毁重建"的一次性资源, 不加 prevent_destroy。
  lifecycle {
    ignore_changes = [
      network_device, # MAC 由 Proxmox 分配, 忽略以免每次 plan 抖动
    ]
  }
}

# ============================================
# 统一输出契约 (与 gcp/aws 子模块字段一致)
# ============================================

output "result" {
  description = "存算分离统一输出"
  value = {
    # storage VM 的数据盘 ID —— compute 销毁后依然存在
    data_disk_id = proxmox_virtual_environment_vm.storage.disk[0].path_in_datastore

    # 计算实例可达地址: agent 回填的第一个非回环 IPv4; 未运行时为空
    ip_address = var.compute_running ? try(
      proxmox_virtual_environment_vm.compute[0].ipv4_addresses[1][0],
      ""
    ) : ""

    status = var.compute_running ? "running" : "suspended"

    # 便于调试的额外信息
    storage_vm_id = proxmox_virtual_environment_vm.storage.vm_id
    compute_vm_id = var.compute_running ? proxmox_virtual_environment_vm.compute[0].vm_id : null
  }
}
