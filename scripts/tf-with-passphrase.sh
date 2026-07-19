#!/bin/bash
# scripts/tf-with-passphrase.sh
# =====================================================================
# 在 mgmt-air 上运行 OpenTofu, 并从 vault-cloud 安全取回 state 加密 passphrase。
#
# 这是"多机共享 state + 客户端加密"方案 (方式 B) 的入口: passphrase 经
# qubesair.GetCredential 从无网络的 vault-cloud 下发 (dom0 policy=ask 人工确认),
# 注入 TF_ENCRYPTION 环境变量, 执行完由子进程退出自然清除 —— 全程不落盘、不进
# 任何 .tf 文件、不进 git、不进 shell 历史。
#
# 用法 (在 mgmt-air, terraform/ 目录的上层):
#   scripts/tf-with-passphrase.sh init
#   scripts/tf-with-passphrase.sh plan  -var-file=environments/dev.tfvars
#   scripts/tf-with-passphrase.sh apply -var-file=environments/dev.tfvars
#
# 依赖:
#   - OpenTofu (tofu) >= 1.7   (可用 TF_BIN 覆盖, 默认 tofu)
#   - vault-cloud 里存有命名凭据 tfstate-passphrase (见 docs/terraform-state.md)
#   - dom0 policy 允许 mgmt-air -> vault-cloud 的 qubesair.GetCredential (阶段3 E 段)
# =====================================================================

set -euo pipefail

TF_BIN="${TF_BIN:-tofu}"
VAULT_QUBE="${VAULT_QUBE:-vault-cloud}"
CRED_NAME="${TFSTATE_PASSPHRASE_CRED:-tfstate-passphrase}"
TF_DIR="${TF_DIR:-terraform}"
# BACKEND: s3 (默认) 或 pg。选 pg 时额外从 vault 取 pg 连接串注入 PG_CONN_STR。
BACKEND="${BACKEND:-s3}"
PG_CONN_STR_CRED="${PG_CONN_STR_CRED:-pg-conn-str}"

if [ "$#" -eq 0 ]; then
    echo "用法: $0 <tofu 子命令> [参数...]" >&2
    echo "例:   $0 apply -var-file=environments/dev.tfvars" >&2
    echo "pg backend: BACKEND=pg $0 apply -var-file=environments/dev.tfvars" >&2
    exit 2
fi

if ! command -v "$TF_BIN" >/dev/null 2>&1; then
    echo "错误: 找不到 '$TF_BIN'。本方案需要 OpenTofu (客户端 state 加密 HashiCorp Terraform 不支持)。" >&2
    echo "      安装 OpenTofu 或设 TF_BIN 指向 tofu 二进制。" >&2
    exit 3
fi

# 经 qrexec 从 vault-cloud 取回一个命名凭据 (dom0 会 ask 人工确认)。
# 用 qrexec-client-vm 的 '+ARG' 传凭据名; 只走 stdout, 不落盘。
# $1 = 凭据名。
fetch_cred() {
    if ! command -v qrexec-client-vm >/dev/null 2>&1; then
        echo "错误: 找不到 qrexec-client-vm。本脚本须在 Qubes 的 mgmt-air 内运行。" >&2
        echo "      (若要在非 Qubes 环境自测, 设对应的 TFSTATE_PASSPHRASE / PG_CONN_STR 环境变量绕过取回。)" >&2
        exit 4
    fi
    qrexec-client-vm "$VAULT_QUBE" "qubesair.GetCredential+$1"
}

# 允许用环境变量 TFSTATE_PASSPHRASE 直接提供 (非 Qubes 自测 / CI), 否则从 vault 取。
if [ -n "${TFSTATE_PASSPHRASE:-}" ]; then
    PASSPHRASE="$TFSTATE_PASSPHRASE"
else
    PASSPHRASE="$(fetch_cred "$CRED_NAME")"
fi

if [ -z "$PASSPHRASE" ]; then
    echo "错误: 取回的 passphrase 为空, 中止 (拒绝以空密钥加密 state)。" >&2
    exit 5
fi

# pg backend: 连接串含 DB 密码, 同样从 vault 取, 注入 PG_CONN_STR (OpenTofu pg
# backend 识别该环境变量), 绝不写进 backend.tf。
if [ "$BACKEND" = "pg" ]; then
    if [ -n "${PG_CONN_STR:-}" ]; then
        : # 已由调用方 (自测/CI) 提供, 直接用
    else
        PG_CONN_STR="$(fetch_cred "$PG_CONN_STR_CRED")"
        export PG_CONN_STR
    fi
    if [ -z "${PG_CONN_STR:-}" ]; then
        echo "错误: BACKEND=pg 但取回的 PG_CONN_STR 为空, 中止。" >&2
        exit 6
    fi
fi

# 构造 OpenTofu 加密配置 (等价于 encryption.tf, 但经环境变量注入, 不落盘)。
# TF_ENCRYPTION 是 OpenTofu 识别的环境变量, 接收一段 HCL 加密配置。
export TF_ENCRYPTION
TF_ENCRYPTION=$(cat <<EOF
key_provider "pbkdf2" "team" {
  passphrase = "${PASSPHRASE}"
}
method "aes_gcm" "primary" {
  keys = key_provider.pbkdf2.team
}
state {
  method   = method.aes_gcm.primary
  enforced = true
}
plan {
  method = method.aes_gcm.primary
}
EOF
)

# 立刻从普通变量清除明文 (环境变量里仍有, 但进程退出即随之消失; 不写磁盘)。
unset PASSPHRASE

# 在 terraform 目录里执行 tofu 子命令。子进程退出后 TF_ENCRYPTION 随之释放。
cd "$TF_DIR"
exec "$TF_BIN" "$@"
