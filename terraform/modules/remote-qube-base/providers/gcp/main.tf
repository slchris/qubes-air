# Remote Qube — GCP 实现
#
# 接口与 proxmox 子模块**完全一致**: 同样的 compute_running 开关, 同样的独立 data 盘,
# 同样的 output "result" 契约。
#
# 存算分离在 GCP 上比 Proxmox 更自然:
#   - google_compute_disk     : 独立持久数据盘 (天生就是独立 resource), 带 prevent_destroy
#   - google_compute_instance : 计算实例, count = compute_running ? 1 : 0
#       boot_disk 随实例重建; attached_disk 引用上面的独立 data 盘。
#   suspend = 销毁 instance 保留 disk; resume = 重建 instance 挂回同一 disk。
#
# ---------------------------------------------------------------------------
# 红线: bootstrap token 不进入 terraform state
# ---------------------------------------------------------------------------
# 这条约束决定了本模块最不直观的一处设计。
#
# 最自然的写法是 `metadata = { user-data = file(var.agent_user_data_file) }` ——
# **不能这么写**。metadata 是 instance 的属性, terraform 会把它的值原样写进 state,
# 于是那份 bootstrap 文档连同一次性 token 一起明文躺在 state 里。Agent 私钥在 guest
# 内生成，从不在这份文档中；但 token 仍然是敏感的单次签发凭据。Proxmox 子模块为同一条
# 红线刻意传路径而非内容 (见其 agent_user_data_file 的注释)。
#
# GCP 侧的等价物是 google_storage_bucket_object 的 `source` 参数: 它和 bpg 的
# source_file 一样只把路径/哈希写进 state, 内容不进。所以身份文档先落进 GCS,
# 再由开机脚本取回 —— 开机脚本本身不含任何密钥, 进 state 无所谓。
#
# 顺带解决了另一个问题: GCP VM **够不到家里那台 artifact store** (10.31.0.2 是
# 局域网地址)。同一个 bucket 就是 GCP 侧的 artifact store, agent .deb 也放这儿,
# 与 PVE 侧 `local:snippets/` + 局域网 store 的结构一一对应。

terraform {
  required_version = ">= 1.5.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0"
    }
  }
}

variable "qube_name" { type = string }
variable "compute_running" { type = bool }
variable "cpu" { type = number }
variable "memory" { type = number }
variable "os_disk_gb" { type = number }
variable "data_disk_gb" { type = number }

variable "zone" {
  description = "GCP zone, 例如 asia-east1-b。实例与磁盘必须同 zone 才能挂载。"
  type        = string
}

variable "machine_type" {
  description = <<-EOT
    显式机型。留空则按 cpu/memory 拼一个 custom-<cpu>-<mem> 机型。

    custom 机型对内存有约束 (每 vCPU 0.9–6.5 GB, 且必须是 256 MB 的整数倍),
    不满足时 GCP 会直接拒绝 —— 与其在这里悄悄取整, 不如让调用方显式给 machine_type。
  EOT
  type        = string
  default     = null
}

variable "source_image" {
  description = <<-EOT
    boot 盘镜像。GCP 没有 "clone 一台模板 VM" 这个概念 (Proxmox 那边是 VMID 901),
    对应物是 image / image family。

    默认用官方 debian-12 family: 它自带 cloud-init 与 guest agent, 与本项目的
    bootstrap 假设一致, 且随上游打补丁。要固定版本或用自建镜像就显式传入。
  EOT
  type        = string
  default     = "debian-cloud/debian-12"
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
  description = "metadata 注入的 SSH **公钥** (绝不含私钥)"
  type        = list(string)
  default     = []
}

variable "agent_user_data_file" {
  description = <<-EOT
    本地路径, 指向 Console 渲染出的 cloud-init user-data，内含公开 CA、一次性 token
    和 agent artifact digest。留空则不投递 bootstrap 数据，agent 将无法取得身份。

    刻意传**路径**而非内容: 见文件头「红线」一节。内容经 GCS object 的 source
    参数投递, 不进 state。
  EOT
  type        = string
  default     = ""
}

variable "identity_bucket" {
  description = <<-EOT
    存放 per-qube 身份文档的 GCS bucket 名。agent_user_data_file 非空时必填。

    这个 bucket 应当是**私有**的: 身份文档含**单次 bootstrap token**, 靠实例的服务
    账号读取。已经不含私钥 (2026-07, 见 docs/bootstrap-design.md §9), 所以泄露的后果
    从「等于泄露车队身份」降到「一个短时效、单次、绑定单机的 token」—— 但 token 仍是
    授予凭据的东西, 保持私有。(agent .deb 是另一回事: 不是秘密, 完整性由 SHA256 钉死。)
  EOT
  type        = string
  default     = ""
}

variable "service_account_email" {
  description = <<-EOT
    实例的服务账号。需要对 identity_bucket 的读权限, 否则开机脚本取不到身份文档。
    留空则用项目默认服务账号。
  EOT
  type        = string
  default     = ""
}

variable "network" {
  type    = string
  default = "default"
}

variable "subnetwork" {
  type    = string
  default = null
}

variable "assign_public_ip" {
  description = <<-EOT
    是否给实例分配外网 IP。

    默认 false: 控制台经私有路径 (WireGuard) 拨 agent 的 :8443, 不需要公网入口。
    置 true 会把 agent 的 mTLS 监听端口暴露到公网 —— 届时唯一的门禁只剩控制台 CA,
    且防火墙的源地址是家里 NAT 出口, 并不稳定。
  EOT
  type        = bool
  default     = false
}

locals {
  # custom-<cpu>-<memMiB>: memory 变量的单位是 MB, 与 proxmox 子模块一致。
  machine_type = coalesce(var.machine_type, "custom-${var.cpu}-${var.memory}")

  deliver_identity = var.agent_user_data_file != "" && var.identity_bucket != ""

  # 身份文档在 bucket 里的对象名。带 qube 名, 一台一份。
  identity_object = "identity/${var.qube_name}.yaml"
}

# ============================================
# 独立持久数据盘 (storage) —— 与 compute 解耦
# ============================================

resource "google_compute_disk" "data" {
  name = "${var.qube_name}-data"
  type = "pd-balanced"
  zone = var.zone
  size = var.data_disk_gb

  # 数据不丢红线: suspend/release 销毁的是 instance, 这块盘必须活下来。
  # 与 proxmox 子模块的 storage VM 同一意图。
  lifecycle {
    prevent_destroy = true
  }
}

# ============================================
# 身份文档 —— 经 GCS 投递, 内容不进 state
# ============================================

resource "google_storage_bucket_object" "agent_identity" {
  count = local.deliver_identity ? 1 : 0

  bucket = var.identity_bucket
  name   = local.identity_object

  # source (路径) 而非 content (内容): 见文件头「红线」一节。
  source = var.agent_user_data_file

  # 让这个对象依赖**内容**而不只是路径。proxmox 那边漏掉等价的 checksum 时,
  # 真机上出现过「console 重新渲染了身份、节点上还是旧文件、cloud-init 照旧文件
  # 跑完并报 done」的哑failure, 且证书轮换会永远传不下去。同样的坑这里用
  # detect_md5hash 堵住。
  detect_md5hash = filemd5(var.agent_user_data_file)
}

# ============================================
# 计算实例 (compute) —— 由 compute_running 控制
# ============================================

resource "google_compute_instance" "compute" {
  count = var.compute_running ? 1 : 0

  name         = var.qube_name
  zone         = var.zone
  machine_type = local.machine_type

  boot_disk {
    initialize_params {
      image = var.source_image
      size  = var.os_disk_gb
      type  = "pd-balanced"
    }
  }

  # 同一块盘在 suspend/resume 之间被重新挂回来。
  attached_disk {
    source      = google_compute_disk.data.id
    device_name = "qubesair-data"
  }

  network_interface {
    network    = var.network
    subnetwork = var.subnetwork

    # 只有显式要求时才给公网 IP。access_config 块存在与否就是「有没有外网地址」。
    dynamic "access_config" {
      for_each = var.assign_public_ip ? [1] : []
      content {}
    }
  }

  dynamic "guest_accelerator" {
    for_each = var.gpu_type != null && var.gpu_count != null ? [1] : []
    content {
      type  = var.gpu_type
      count = var.gpu_count
    }
  }

  # GPU 实例不能实时迁移, GCP 要求显式声明。
  scheduling {
    on_host_maintenance = var.gpu_type != null ? "TERMINATE" : "MIGRATE"
    automatic_restart   = var.gpu_type == null
  }

  dynamic "service_account" {
    for_each = local.deliver_identity ? [1] : []
    content {
      email = var.service_account_email != "" ? var.service_account_email : null
      # 取身份文档要读 GCS。devstorage.read_only 是够用的最小 scope。
      scopes = ["https://www.googleapis.com/auth/devstorage.read_only"]
    }
  }

  metadata = merge(
    length(var.ssh_public_keys) > 0 ? {
      # GCP 的 key 是 "ssh-keys", 每行 "<user>:<key>"。
      ssh-keys = join("\n", [for k in var.ssh_public_keys : "qubesair:${k}"])
    } : {},
    local.deliver_identity ? {
      # 这里放的是**取件脚本**, 不是身份本身 —— metadata 会进 state, 身份不能进。
      # 脚本用实例自己的服务账号令牌把身份文档从私有 bucket 取回, 交给 cloud-init。
      user-data = <<-EOT
        #cloud-config
        # Qubes Air: 取回该 qube 的身份文档并交给 cloud-init 执行。
        # 本文件不含任何密钥 —— 身份在 gs://${var.identity_bucket}/${local.identity_object},
        # 只有本实例的服务账号读得到。
        runcmd:
          - |
            set -eu
            TOKEN="$(curl -sf -H 'Metadata-Flavor: Google' \
              'http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token' \
              | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
            [ -n "$TOKEN" ] || { echo 'qubesair: no metadata token' >&2; exit 1; }
            curl -sf -H "Authorization: Bearer $TOKEN" \
              -o /run/qubesair-identity.yaml \
              'https://storage.googleapis.com/${var.identity_bucket}/${local.identity_object}'
            # 硬失败而不是继续: 没有身份的 agent 认证不了, 一台"看起来正常、
            # agent 是死的"机器比一台明确失败的机器难查得多。
            [ -s /run/qubesair-identity.yaml ] || { echo 'qubesair: identity empty' >&2; exit 1; }
            cloud-init single --name cc_scripts_user --frequency once \
              --file /run/qubesair-identity.yaml || \
              cloud-init devel schema --config-file /run/qubesair-identity.yaml
      EOT
    } : {}
  )

  # 身份文档必须先在 bucket 里, 实例开机才取得到。
  depends_on = [google_storage_bucket_object.agent_identity]
}

# ============================================
# 统一 output 契约 (与 proxmox 子模块一致)
# ============================================

output "result" {
  value = {
    data_disk_id = google_compute_disk.data.id

    # 私有部署 (assign_public_ip=false) 时这里是 VPC 内网地址 —— 控制台经
    # WireGuard 才拨得到。给了公网 IP 才用公网地址。
    ip_address = var.compute_running ? (
      var.assign_public_ip
      ? google_compute_instance.compute[0].network_interface[0].access_config[0].nat_ip
      : google_compute_instance.compute[0].network_interface[0].network_ip
    ) : ""

    status        = var.compute_running ? "running" : "suspended"
    storage_vm_id = null
    compute_vm_id = var.compute_running ? google_compute_instance.compute[0].id : null
  }
}
