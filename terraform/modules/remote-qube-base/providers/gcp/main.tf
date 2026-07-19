# Remote Qube — GCP 实现 (骨架 / TODO)
#
# 接口与 proxmox 子模块**完全一致**: 同样的 compute_running 开关, 同样的独立 data 盘,
# 同样的 output "result" 契约。本文件目前是骨架, 不建真实资源 (validate 通过即可)。
#
# 存算分离在 GCP 上比 Proxmox 更自然:
#   - google_compute_disk        : 独立持久数据盘 (天生就是独立 resource), 带 prevent_destroy
#   - google_compute_instance    : 计算实例, count = compute_running ? 1 : 0
#       其 boot_disk 随实例重建; attached_disk 引用上面的独立 data 盘 (source = disk.id)
#   suspend = 销毁 instance 保留 disk; resume = 重建 instance 挂回同一 disk。
#
# TODO(阶段2): 用真实 google_compute_disk + google_compute_instance 实现下述骨架。

terraform {
  required_version = ">= 1.5.0"
}

variable "qube_name" { type = string }
variable "compute_running" { type = bool }
variable "cpu" { type = number }
variable "memory" { type = number }
variable "os_disk_gb" { type = number }
variable "data_disk_gb" { type = number }
variable "machine_type" {
  type    = string
  default = null
}
variable "gpu_type" {
  type    = string
  default = null
}
variable "gpu_count" {
  type    = number
  default = null
}
variable "ssh_public_keys" {
  description = "cloud-init/metadata 注入的 SSH **公钥** (绝不含私钥)"
  type        = list(string)
  default     = []
}

# ============================================
# TODO: 独立持久数据盘 (storage) —— 与 compute 解耦
# ============================================
#
# resource "google_compute_disk" "data" {
#   name = "${var.qube_name}-data"
#   size = var.data_disk_gb
#   type = "pd-ssd"
#   lifecycle {
#     prevent_destroy = true   # 数据不丢红线
#   }
# }

# ============================================
# TODO: 计算实例 (compute) —— 由 compute_running 控制
# ============================================
#
# resource "google_compute_instance" "compute" {
#   count        = var.compute_running ? 1 : 0
#   name         = var.qube_name
#   machine_type = coalesce(var.machine_type, "e2-standard-2")
#
#   boot_disk {                       # os 盘, 随实例重建
#     initialize_params { size = var.os_disk_gb }
#   }
#
#   attached_disk {                   # 挂载独立 data 盘 (attach, 不新建)
#     source = google_compute_disk.data.id
#   }
#
#   dynamic "guest_accelerator" {     # 可选 GPU
#     for_each = var.gpu_type != null ? [1] : []
#     content {
#       type  = var.gpu_type
#       count = coalesce(var.gpu_count, 1)
#     }
#   }
#
#   metadata = {
#     ssh-keys = join("\n", [for k in var.ssh_public_keys : "qubes:${k}"])
#   }
# }

# ============================================
# 统一输出契约 (骨架占位, 字段名与 proxmox 一致)
# ============================================

output "result" {
  description = "存算分离统一输出 (GCP 骨架占位)"
  value = {
    # TODO: data_disk_id = google_compute_disk.data.id
    data_disk_id = "TODO-gcp-${var.qube_name}-data-disk"

    # TODO: ip_address = try(google_compute_instance.compute[0].network_interface[0].access_config[0].nat_ip, "")
    ip_address = ""

    status        = var.compute_running ? "running" : "suspended"
    storage_vm_id = null
    compute_vm_id = null
  }
}
