# Google Cloud Provider 配置
#
# 用于管理 GCP 资源

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = ">= 5.0.0"
    }
  }
}

# ============================================
# Provider 配置
# ============================================

provider "google" {
  project = var.gcp_project
  region  = var.gcp_region
  zone    = var.gcp_zone

  # 认证: 使用 GOOGLE_APPLICATION_CREDENTIALS 环境变量
  # 或 gcloud auth application-default login
}

provider "google-beta" {
  project = var.gcp_project
  region  = var.gcp_region
  zone    = var.gcp_zone
}

# ============================================
# 变量定义
# ============================================

variable "gcp_project" {
  description = "GCP 项目 ID"
  type        = string
}

variable "gcp_region" {
  description = "GCP 区域"
  type        = string
  default     = "us-central1"
}

variable "gcp_zone" {
  description = "GCP 可用区"
  type        = string
  default     = "us-central1-a"
}

# ============================================
# GPU 可用性数据源
# ============================================

data "google_compute_zones" "available" {
  project = var.gcp_project
  region  = var.gcp_region
}

# 输出可用的 GPU 类型和区域
output "available_zones" {
  description = "可用的计算区域"
  value       = data.google_compute_zones.available.names
}
