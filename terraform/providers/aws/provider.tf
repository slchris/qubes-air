# AWS Provider 配置
#
# 用于管理 AWS 资源

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0.0"
    }
  }
}

# ============================================
# Provider 配置
# ============================================

provider "aws" {
  region = var.aws_region

  # 认证: 使用以下方式之一
  # 1. AWS_ACCESS_KEY_ID 和 AWS_SECRET_ACCESS_KEY 环境变量
  # 2. ~/.aws/credentials 文件
  # 3. IAM Role (EC2/ECS)

  default_tags {
    tags = {
      Project   = "qubes-air"
      ManagedBy = "terraform"
    }
  }
}

# ============================================
# 变量定义
# ============================================

variable "aws_region" {
  description = "AWS 区域"
  type        = string
  default     = "us-east-1"
}

# ============================================
# 数据源
# ============================================

# 获取当前 AWS 账户信息
data "aws_caller_identity" "current" {}

# 获取可用区
data "aws_availability_zones" "available" {
  state = "available"
}

# 获取最新 Fedora AMI
data "aws_ami" "fedora" {
  most_recent = true
  owners      = ["125523088429"] # Fedora 官方

  filter {
    name   = "name"
    values = ["Fedora-Cloud-Base-39-*"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }

  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
}

# ============================================
# 输出
# ============================================

output "account_id" {
  description = "AWS 账户 ID"
  value       = data.aws_caller_identity.current.account_id
}

output "available_azs" {
  description = "可用的可用区"
  value       = data.aws_availability_zones.available.names
}

output "fedora_ami_id" {
  description = "最新 Fedora AMI ID"
  value       = data.aws_ami.fedora.id
}
