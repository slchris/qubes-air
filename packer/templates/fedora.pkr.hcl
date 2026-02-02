# Qubes Air Fedora Template Builder
#
# 使用 Packer 构建用于远程 Qube 的 Fedora 镜像

packer {
  required_version = ">= 1.9.0"
  
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = ">= 1.0.0"
    }
    proxmox = {
      source  = "github.com/hashicorp/proxmox"
      version = ">= 1.1.0"
    }
  }
}

# ============================================
# 变量定义
# ============================================

variable "fedora_version" {
  type    = string
  default = "39"
}

variable "iso_url" {
  type    = string
  default = "https://download.fedoraproject.org/pub/fedora/linux/releases/39/Cloud/x86_64/images/Fedora-Cloud-Base-39-1.5.x86_64.qcow2"
}

variable "iso_checksum" {
  type    = string
  default = "sha256:xxxxx"  # 替换为实际校验和
}

variable "output_directory" {
  type    = string
  default = "output"
}

variable "ssh_username" {
  type    = string
  default = "fedora"
}

variable "ssh_password" {
  type      = string
  default   = "fedora"
  sensitive = true
}

# ============================================
# 本地变量
# ============================================

locals {
  build_timestamp = formatdate("YYYYMMDD-HHmmss", timestamp())
  image_name      = "qubes-air-fedora-${var.fedora_version}-${local.build_timestamp}"
}

# ============================================
# QEMU 构建 (本地测试)
# ============================================

source "qemu" "fedora" {
  accelerator = "kvm"
  
  iso_url      = var.iso_url
  iso_checksum = var.iso_checksum
  
  output_directory = "${var.output_directory}/qemu"
  vm_name         = "${local.image_name}.qcow2"
  
  format       = "qcow2"
  disk_size    = "20G"
  memory       = 2048
  cpus         = 2
  
  ssh_username = var.ssh_username
  ssh_password = var.ssh_password
  ssh_timeout  = "20m"
  
  shutdown_command = "sudo shutdown -P now"
  
  headless = true
}

# ============================================
# 构建步骤
# ============================================

build {
  name    = "qubes-air-fedora"
  sources = ["source.qemu.fedora"]
  
  # 基础系统更新
  provisioner "shell" {
    inline = [
      "sudo dnf update -y",
      "sudo dnf install -y wireguard-tools python3 python3-pip jq",
    ]
  }
  
  # 安装 Qubes Air Agent
  provisioner "shell" {
    script = "scripts/install-agent.sh"
  }
  
  # 清理
  provisioner "shell" {
    inline = [
      "sudo dnf clean all",
      "sudo rm -rf /var/cache/dnf/*",
      "sudo rm -rf /tmp/*",
      "sudo rm -f /var/log/*.log",
    ]
  }
  
  # 输出构建信息
  post-processor "manifest" {
    output     = "${var.output_directory}/manifest.json"
    strip_path = true
  }
}
