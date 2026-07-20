# Qubes Air - Main Terraform Configuration
# 
# 这是 Qubes Air 项目的主 Terraform 配置文件
# 用于编排多 Zone 的远程 Qube 基础设施

# ============================================================================
# 安全说明 (评审红线 —— state 视同凭据) + 多机远程 backend 方案 (定稿)
#
#   Terraform/OpenTofu state 会明文保存所有 resource 属性 (含 provider 传入的
#   敏感值)。凡属长期凭据 (API token / 私钥) **绝不**写进 state:
#     - provider api_token 经环境变量 / gitignore 的 tfvars 注入, sensitive=true
#     - WireGuard/SSH 私钥在目标机本地生成, 只接收公钥 (见 zone-base)
#
#   【多机共享 state 的定稿方案】(详见 docs/terraform-state.md)
#   需求: 多台 Qubes 笔记本共享同一套远端基础设施 -> 必须远程 backend (共享
#   state + 锁)。约束: 本项目威胁模型里**存储运营方 / 云厂商不可信**。
#
#   关键事实: backend 层的服务端加密 (S3 SSE-KMS / GCS CMEK / MinIO SSE) 密钥
#   都在存储运营方手里, 运营方能解密 -> 对"云厂商不可信"无效。唯一干净解法是
#   **OpenTofu (非 HashiCorp Terraform) 1.7+ 的客户端 state/plan 加密**: state
#   在离开笔记本前用 AES-GCM 加密, backend 只拿到密文。key_provider 必须是本地
#   **PBKDF2 (passphrase)** 或自托管 OpenBao —— **绝不用 aws_kms/gcp_kms**
#   (那样 KEK 回到云厂商手里, 前功尽弃)。
#
#   本仓库据此采用: **S3 兼容 backend (use_lockfile 原生锁) + OpenTofu 客户端
#   PBKDF2 加密**。passphrase 是团队密钥, 存各笔记本的 vault-cloud, 经
#   qubesair.GetCredential+tfstate-passphrase 注入 TF_ENCRYPTION, 不落盘/不进 git。
#
#   两种 backend (二选一, 见 docs/terraform-state.md):
#     - S3 兼容 (AWS S3 / 自托管 MinIO): backend.tf.example, use_lockfile 原生锁
#     - pg (自托管 Postgres on Proxmox): backend-pg.tf.example, advisory lock
#       —— 场景 A 首选; 也是 MinIO use_lockfile 实测不可靠时的可靠退路。
#
#   backend {} 与 encryption {} **不写在此 (会进 git 且含环境值/密码)**:
#     - backend 配置放 gitignore 的 terraform/backend.tf (从对应 .example 复制)
#     - encryption 配置经环境变量 TF_ENCRYPTION 注入 (见 tf-with-passphrase.sh)
#     - pg 连接串 (含 DB 密码) 经环境变量 PG_CONN_STR 注入, 同样从 vault 取
#   这样 `tofu init -backend=false` (CI validate) 与多环境切换都不受影响。
# ============================================================================

terraform {
  required_version = ">= 1.5.0"

  required_providers {
    # Proxmox VE Provider
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.38.0"
    }
    # Google Cloud Provider
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0.0"
    }
    # AWS Provider
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0.0"
    }
    # 没有 random provider: 它唯一的历史用途是 zone-base 里那个
    # random_id.wireguard_key_seed, 而那个已作为评审红线删除 (密钥种子会明文进 state)。
  }
}

# 本地变量
locals {
  # 项目标签
  common_tags = {
    Project     = "qubes-air"
    ManagedBy   = "terraform"
    Environment = var.environment
  }

  # zone 名 -> provider_type 映射 (决定每个 Qube 落到哪家 provider)
  zone_provider = {
    "proxmox-zone" = "proxmox"
    "gcp-zone"     = "gcp"
    "aws-zone"     = "aws"
  }
}

# Zone 模块实例化
# 根据配置创建各个 Zone

# Proxmox VE Zone (私有云)
module "proxmox_zone" {
  source = "./modules/zone-base"
  count  = var.enable_proxmox_zone ? 1 : 0

  zone_name       = "proxmox-zone"
  zone_type       = "proxmox"
  provider_config = var.proxmox_config

  tags = local.common_tags
}

# GCP Zone (公有云)
module "gcp_zone" {
  source = "./modules/zone-base"
  count  = var.enable_gcp_zone ? 1 : 0

  zone_name       = "gcp-zone"
  zone_type       = "gcp"
  provider_config = var.gcp_config

  tags = local.common_tags
}

# AWS Zone (公有云)
module "aws_zone" {
  source = "./modules/zone-base"
  count  = var.enable_aws_zone ? 1 : 0

  zone_name       = "aws-zone"
  zone_type       = "aws"
  provider_config = var.aws_config

  tags = local.common_tags
}

# 远程 Qube 实例 (存算分离)
module "remote_qubes" {
  source   = "./modules/remote-qube-base"
  for_each = var.remote_qubes

  qube_name     = each.key
  qube_config   = each.value
  zone_id       = each.value.zone
  provider_type = lookup(local.zone_provider, each.value.zone, "proxmox")

  proxmox_default_node = var.proxmox_config.node

  depends_on = [
    module.proxmox_zone,
    module.gcp_zone,
    module.aws_zone
  ]
}
