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

variable "template_node_name" {
  description = <<-EOT
    模板 VM 所在的节点。留空则回落到 var.node_name。

    这与 var.node_name **是两件事**: 模板住在哪台 (template_node_name) 与新 VM 跑在哪台
    (node_name)。clone API 必须在**模板所在节点**上调用, 由 target 参数指定落到哪台;
    在错误的节点上调用会得到 "unable to find configuration file for VM <id>"。

    两者可以不同**仅当模板的盘在共享存储上** —— qemu-server 明确规定
    "target: Only allowed if the original VM is on shared storage"。
    在 local-lvm 上模板只能在自己那台 clone。
  EOT
  type        = string
  default     = ""
}

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

variable "agent_user_data_file" {
  description = <<-EOT
    本地路径, 指向 Console 渲染出的 cloud-init user-data (内含该 agent 的 mTLS 身份)。
    留空则不投递身份 —— agent 将无法认证。

    刻意传**路径**而非内容: bpg 的 source_file 只把卷 ID、文件名、大小写进 state,
    内容不进 state。若改用 source_raw 传内容, 私钥会明文落进 state ——
    而 main.tf 开头的红线是「私钥绝不写进 state」。
  EOT
  type        = string
  default     = ""
}

variable "agent_user_data_volume_id" {
  description = <<-EOT
    共享存储上身份 snippet 的 Proxmox 卷 ID, 形如
    `cephfs:snippets/qubes-air-<name>-<hash>.yaml`。

    非空时**取代** agent_user_data_file: 文件已经躺在所有节点都能读到的共享存储上,
    terraform 只需要引用它, 不需要上传 —— 于是 provider 的 ssh 块在置备路径上
    不再是必需的。这正是这条路存在的理由 (见 docs/bootstrap-design.md §4.4)。

    **文件名里的哈希是承重的, 不是装饰。** 走上传那条路时, 是资源上的
    `checksum = filesha256(...)` 让 terraform 依赖内容; 这里没有资源可挂 checksum,
    换成内容变→文件名变→卷 ID 变, 而 user_data_file_id 在 VM 上是 ForceNew。
    如果哪天有人把哈希从文件名里拿掉, 身份更新会**静默传不下去**, 而每次 apply
    都报成功 —— §7 记的就是这个故障。
  EOT
  type        = string
  default     = ""
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
# 1b) agent 身份 snippet
#
# 上传 Console 渲染的 cloud-init user-data, 内含该 agent 的证书与私钥。
# 走 SFTP (bpg 对 snippets 的唯一上传方式), 因此 provider 的 ssh 块必须能连到节点。
#
# 与 compute 同生命周期: compute 被销毁 (suspend) 时这个文件也随之删除, 下次 resume
# 重新上传。这是刻意的 —— 一台不存在的 compute 不该在节点上留着自己的私钥。
# ============================================

resource "proxmox_virtual_environment_file" "agent_identity" {
  # 共享存储模式 (agent_user_data_volume_id 非空) 下**不建这个资源**: 文件已经在位,
  # 再上传一份等于把节点 SSH 又请回置备路径。
  count = var.compute_running && var.agent_user_data_file != "" && var.agent_user_data_volume_id == "" ? 1 : 0

  content_type = "snippets"
  datastore_id = "local" # snippets 需要文件系统型存储; RBD 只支持 images
  node_name    = var.node_name

  # checksum 让这个资源依赖**内容**, 而不只是路径。
  #
  # 没有它, terraform 只跟踪 path —— 同一路径下内容改了它完全看不见, apply 报成功
  # 而节点上还是旧文件。真机实测踩到过: console 重新渲染了身份文档 (新增 agent 包的
  # 安装脚本), 节点上的 snippet 却还是几小时前那份, cloud-init 照着旧文件跑完、
  # 报 done, 结果是一台"看起来正常、agent 根本没装"的 qube —— 且没有任何报错。
  #
  # 影响远不止那一次: **证书轮换同样传不下去**。重新签发的证书会永远到不了 qube,
  # 而每次 apply 都告诉你成功了。
  source_file {
    path     = var.agent_user_data_file
    checksum = filesha256(var.agent_user_data_file)
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
      vm_id = var.template_vm_id
      # 模板所在节点, 不是放置目标。provider 在这台上发起 clone,
      # 并把外层 node_name 作为 target 传给 Proxmox。
      node_name = coalesce(var.template_node_name, var.node_name)
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

  # cloud-init
  initialization {
    datastore_id = var.datastore_id

    # agent 身份经 user_data 投递。两条路都只传卷 ID, 内容不进 state。
    #
    # 共享存储上的卷 ID 优先: 那条路里文件已经在位, 上面那个资源根本没建。
    user_data_file_id = var.agent_user_data_volume_id != "" ? var.agent_user_data_volume_id : (
      length(proxmox_virtual_environment_file.agent_identity) > 0 ? (
        proxmox_virtual_environment_file.agent_identity[0].id
      ) : null
    )

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

      # node_name: 计算实例实际所在节点可能被 terraform 之外的力量改变 ——
      # Proxmox HA 故障转移、运维手动迁移、或 CRS 重平衡。共享存储(Ceph)让这些
      # 都是合法且无损的操作。若不忽略, 下一次 apply 会认为"位置漂移了"并把 VM
      # 迁回原节点 —— 而这恰恰会撤销 HA 刚做的救援。
      #
      # 注意语义: 这意味着**初始放置**由调度器决定并写进 tfvars, 之后的移动交给
      # 集群自己管。要强制换节点, 改 tfvars 后需显式 taint/replace。
      node_name,
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
