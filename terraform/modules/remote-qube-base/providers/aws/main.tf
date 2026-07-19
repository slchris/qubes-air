# Remote Qube — AWS 实现 (骨架 / TODO)
#
# 接口与 proxmox 子模块**完全一致**: 同样的 compute_running 开关, 同样的独立 data 盘,
# 同样的 output "result" 契约。本文件目前是骨架, 不建真实资源 (validate 通过即可)。
#
# 存算分离在 AWS 上同样自然:
#   - aws_ebs_volume            : 独立持久数据盘 (独立 resource), 带 prevent_destroy
#   - aws_instance              : 计算实例, count = compute_running ? 1 : 0
#   - aws_volume_attachment     : 把 EBS 卷挂到 instance (attach, 不新建)
#       关键: aws_volume_attachment 默认在销毁 instance 时**分离而非删除** EBS 卷,
#       且不要设 skip_destroy 之外的强删选项 -> 销毁 compute 不丢数据。
#   suspend = 销毁 instance + attachment 保留 volume; resume = 重建后重新 attach。
#
# TODO(阶段2): 用真实 aws_ebs_volume + aws_instance + aws_volume_attachment 实现下述骨架。

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
variable "ssh_public_keys" {
  description = "注入的 SSH **公钥** (绝不含私钥)"
  type        = list(string)
  default     = []
}

# ============================================
# TODO: 独立持久数据盘 (storage) —— 与 compute 解耦
# ============================================
#
# resource "aws_ebs_volume" "data" {
#   availability_zone = "..."          # 与 instance 同 AZ
#   size              = var.data_disk_gb
#   type              = "gp3"
#   lifecycle {
#     prevent_destroy = true            # 数据不丢红线
#   }
# }

# ============================================
# TODO: 计算实例 (compute) —— 由 compute_running 控制
# ============================================
#
# resource "aws_instance" "compute" {
#   count         = var.compute_running ? 1 : 0
#   instance_type = coalesce(var.machine_type, "t3.medium")
#   ami           = "..."              # 见 providers/aws/provider.tf 的 data.aws_ami.fedora
#
#   root_block_device { volume_size = var.os_disk_gb }   # os 盘, 随实例重建
#
#   # 只注入公钥 (通过 key_name 引用已导入的 aws_key_pair, 私钥不经手)
# }
#
# resource "aws_volume_attachment" "data" {
#   count       = var.compute_running ? 1 : 0
#   device_name = "/dev/sdf"
#   volume_id   = aws_ebs_volume.data.id
#   instance_id = aws_instance.compute[0].id
#   # 默认: 销毁 attachment 时只分离, 不删除 volume -> 数据保留
# }

# ============================================
# 统一输出契约 (骨架占位, 字段名与 proxmox 一致)
# ============================================

output "result" {
  description = "存算分离统一输出 (AWS 骨架占位)"
  value = {
    # TODO: data_disk_id = aws_ebs_volume.data.id
    data_disk_id = "TODO-aws-${var.qube_name}-data-volume"

    # TODO: ip_address = try(aws_instance.compute[0].public_ip, "")
    ip_address = ""

    status        = var.compute_running ? "running" : "suspended"
    storage_vm_id = null
    compute_vm_id = null
  }
}
