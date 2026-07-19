#!/bin/bash
# Qubes Air - dom0 侧 vault-cloud 创建脚本 (阶段3)
# =====================================================================
# 创建凭据保险库 qube `vault-cloud`。
#
# 【本 qube 的红线 (评审确立, 违反算回归)】
#   - 无网络: netvm = none (Qubes 里表示为空 netvm)。永不联网 -> 私钥/凭据无法外泄上云。
#   - 普通 AppVM: 有持久 /home + /rw。凭据文件存 /home/user (私钥永不进 git/pillar/云)。
#   - 打 tag=vault-cloud 供 dom0 policy 定位 GetCredential / split-ssh / split-gpg 目标。
#   - mgmt-air / sys-relay 只经 qrexec 向它"要用途", 私钥本身留在此 qube。
#
# 与阶段2 的关系: 阶段2 的反向调用 policy (C 段) 目标是名为 `vault` 的 qube (Split-GPG)。
#   本脚本创建的是 `vault-cloud` (云凭据 + relay SSH 私钥)。两者可以是同一个 qube,
#   也可以分开。默认分开 (职责单一); 若你想合并, 把 --name 设为 vault 即可。
#
# 用法:
#   sudo bash create-vault-cloud.sh [--name vault-cloud] [--template fedora-42] [--label black]
#
# 幂等: 已存在则跳过创建, 仅确保 netvm=none 与 tag。
# =====================================================================

set -euo pipefail

VAULT_NAME="vault-cloud"
TEMPLATE="fedora-42"
LABEL="black"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1" >&2; }

usage() {
    cat <<'EOF'
用法: create-vault-cloud.sh [选项]
  --name NAME        vault qube 名 (默认 vault-cloud)
  --template TPL     基于的模板 (默认 fedora-42)
  --label LABEL      标签颜色 (默认 black, 表示最高敏感)
  -h, --help         显示帮助
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)     VAULT_NAME="${2:-}"; shift 2 ;;
        --template) TEMPLATE="${2:-}"; shift 2 ;;
        --label)    LABEL="${2:-}"; shift 2 ;;
        -h|--help)  usage; exit 0 ;;
        *) log_error "未知参数: $1"; usage; exit 1 ;;
    esac
done

[ -f /etc/qubes-release ] || { log_error "必须在 dom0 运行"; exit 1; }
command -v qvm-create >/dev/null 2>&1 || { log_error "找不到 qvm-create —— 不在 dom0?"; exit 1; }
[ -n "$VAULT_NAME" ] || { log_error "缺少 --name"; usage; exit 1; }

if qvm-check --quiet "$VAULT_NAME" 2>/dev/null; then
    log_warn "'$VAULT_NAME' 已存在, 跳过创建, 仅确保 netvm=none 与 tag。"
else
    log_info "创建 vault AppVM: $VAULT_NAME (template=$TEMPLATE, 无网络)"
    # --property netvm='' 即无 netvm (无网络)。也可创建后 qvm-prefs netvm ''。
    qvm-create --class AppVM \
        --template "$TEMPLATE" \
        --label "$LABEL" \
        "$VAULT_NAME"
fi

# 关键: 强制无网络 (纵深防御, 即便模板/继承带了 netvm 也清空)
log_info "强制 netvm='' (无网络)"
qvm-prefs "$VAULT_NAME" netvm ''

# vault 不需要作为任何人的网关
qvm-prefs "$VAULT_NAME" provides_network False 2>/dev/null || true

# 打 tag 供 dom0 policy 用 @tag:vault-cloud 定位
log_info "打 tag: vault-cloud"
qvm-tags "$VAULT_NAME" add vault-cloud 2>/dev/null || true

log_info "完成。"
cat <<EOF

下一步:
  1. 部署 policy: 确保 /etc/qubes/policy.d/30-qubes-air.policy 已含 vault-cloud 段 (阶段3)。
  2. 对 $VAULT_NAME 应用 salt: salt-call --local state.apply qubes-air.vault-cloud
     (装 socat, 部署 qubesair.GetCredential 服务与 split-ssh agent 服务)。
  3. 往 vault 里存凭据 (见 runbook): 在 $VAULT_NAME 内
       mkdir -p ~/.qubes-air/credentials && chmod 700 ~/.qubes-air/credentials
       printf '%s' "\$PROXMOX_TOKEN" > ~/.qubes-air/credentials/proxmox-token
       chmod 600 ~/.qubes-air/credentials/proxmox-token
  4. relay SSH 私钥放 $VAULT_NAME 的 ~/.ssh/ 并起 ssh-agent (split-ssh, 见 salt)。
EOF

# =====================================================================
# 官方核对 (便于监工复查):
#   - "无网络" qube: 官方文档 "Firewall" / VM settings —— netvm 置空即无网络连接。
#     dev.qubes-os.org: qvm-prefs <vm> netvm '' (empty) 断开网络。vault 惯例 label=black。
#   - Split GPG / Split SSH 的 vault 惯例就是一个 netvm='' 的 AppVM (私钥留 vault)。
#     参见官方 Split GPG 文档与社区 Split SSH 指南 (forum.qubes-os.org/t/split-ssh/19060)。
#
# 待真机确认 (Mac 无法验证):
#   [V1] R4.3 默认模板名 (fedora-42 / fedora-41?); 按 qvm-template list 调整 --template。
#   [V2] `qvm-prefs <vm> netvm ''` 清空 netvm 的确切写法 (部分版本用 qvm-prefs -D netvm 删除)。
#        若 '' 报错, 改用: qubes-prefs 默认或 `qvm-prefs "$VAULT_NAME" netvm none` 视版本。
#   [V3] label=black 是否可用 (标准 label 集含 black); 若报错换 red。
# =====================================================================
