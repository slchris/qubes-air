#!/bin/bash
# Qubes Air - Zone / 远端 VM 销毁钩子 (阶段3, crypto-shredding)
# =====================================================================
# 销毁哲学 (评审确认): 云 SSD/快照上覆写无效, 远端盘从创建就 LUKS, 密钥只在本地。
#   销毁 = 丢本地密钥 (crypto-shred), 而非覆写云盘。
#
# 本脚本【只动本地】:
#   1. shred 删除本地该 Zone/VM 的 LUKS 密钥材料 (兑现"密钥只在本地" -> 云密文永久不可解);
#   2. 删除本地 vault 里该 Zone 的凭据文件 (若在本机可访问);
#   3. 打印运维需手动完成的云侧动作 (吊销 API key、terraform destroy) —— 脚本【不】碰云, 防误删。
#
# 用法:
#   decommission-zone.sh --zone <zone-name> [--shred-luks-key] [--cred <name> ...]
#   decommission-zone.sh --vm   <remote-vm-name> [--shred-luks-key]
#   DRY_RUN=1 decommission-zone.sh --zone pve-prod --shred-luks-key   # 只预览
#
# 环境变量:
#   LUKS_KEY_DIR   LUKS 密钥目录 (默认 ~/.qubes-air/keys/luks)
#   CRED_DIR       本地凭据目录 (默认 ~/.qubes-air/credentials); 通常在 vault-cloud 内, dom0 未必可见
# =====================================================================

set -euo pipefail

ZONE=""
VM=""
SHRED_LUKS=0
CREDS=()
DRY_RUN="${DRY_RUN:-0}"

LUKS_KEY_DIR="${LUKS_KEY_DIR:-$HOME/.qubes-air/keys/luks}"
CRED_DIR="${CRED_DIR:-$HOME/.qubes-air/credentials}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1" >&2; }

usage() {
    cat <<'EOF'
用法: decommission-zone.sh (--zone <name> | --vm <name>) [选项]
  --zone NAME        要下线的 Zone 名 (决定 LUKS 密钥文件名与凭据)
  --vm NAME          要销毁的单个远端 VM 名 (crypto-shred 其盘)
  --shred-luks-key   shred 删除对应 LUKS 密钥 (crypto-shredding 核心动作)
  --cred NAME        额外要删的本地凭据文件名 (可重复)
  -h, --help         帮助

环境变量: LUKS_KEY_DIR (默认 ~/.qubes-air/keys/luks), CRED_DIR (默认 ~/.qubes-air/credentials)
          DRY_RUN=1 只预览不删。
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --zone)           ZONE="${2:-}"; shift 2 ;;
        --vm)             VM="${2:-}"; shift 2 ;;
        --shred-luks-key) SHRED_LUKS=1; shift ;;
        --cred)           CREDS+=("${2:-}"); shift 2 ;;
        -h|--help)        usage; exit 0 ;;
        *) log_error "未知参数: $1"; usage; exit 1 ;;
    esac
done

TARGET="${ZONE:-$VM}"
if [ -z "$TARGET" ]; then
    log_error "必须指定 --zone 或 --vm"
    usage
    exit 1
fi

shred_or_rm() {
    # 优先 shred -u; 无 shred 则 rm -f。DRY_RUN 只打印。
    local f="$1"
    if [ ! -e "$f" ]; then
        log_warn "不存在, 跳过: $f"
        return 0
    fi
    if [ "$DRY_RUN" = "1" ]; then
        echo "  DRY_RUN> shred -u $f  (或 rm -f)"
        return 0
    fi
    if command -v shred >/dev/null 2>&1; then
        shred -u "$f" && log_info "已 shred 删除: $f"
    else
        rm -f "$f" && log_warn "无 shred, 已 rm 删除: $f"
    fi
}

# --- 1. crypto-shred: 删本地 LUKS 密钥 ---
if [ "$SHRED_LUKS" = "1" ]; then
    key="$LUKS_KEY_DIR/$TARGET.key"
    log_info "crypto-shred: 删除本地 LUKS 密钥 ($TARGET) -> 云上该盘密文永久不可解"
    shred_or_rm "$key"
else
    log_warn "未指定 --shred-luks-key: 跳过 LUKS 密钥销毁 (远端盘密文仍可被本地密钥解)。"
fi

# --- 2. 删本地凭据文件 (若本机可见; 通常在 vault-cloud 内执行) ---
if [ ${#CREDS[@]} -gt 0 ]; then
    for c in "${CREDS[@]}"; do
        shred_or_rm "$CRED_DIR/$c"
    done
elif [ -n "$ZONE" ]; then
    # 约定: Zone 凭据文件名以 zone 名或 provider 命名; 这里给提示而非猜删。
    log_warn "未用 --cred 指定凭据文件名; 请在 vault-cloud 内手动删除该 Zone 的凭据:"
    log_warn "  rm -f $CRED_DIR/<该Zone的token文件>"
fi

# --- 3. 云侧 + terraform: 提示手动 (脚本不碰云, 防误删) ---
cat <<EOF

$(log_info "=== 本地 crypto-shred 完成; 以下需手动完成 (脚本不自动执行) ===")
  [云侧吊销] 到云控制台吊销该 Zone/VM 的 API 凭据:
     Proxmox: pveum user token remove <user> <tokenid>
     GCP:     gcloud iam service-accounts keys delete <KEY_ID> --iam-account=<SA>
     AWS:     aws iam delete-access-key --access-key-id <ID>
  [terraform] 销毁云资源 (阶段1 模块, 本阶段不改):
     terraform destroy -target=<对应资源>
  [控制台] 删除凭据记录: DELETE /api/v1/credentials/{id}
  [远端信任] 撤销远端 authorized_keys 里对应 relay 公钥; dom0: qvm-remove ${VM:-<remote-vm>}

$(log_warn "为何不覆写云盘: 云 SSD 磨损均衡 + 快照 + 底层复制使 shred/dd 覆写不可靠;")
$(log_warn "唯一可靠的是从创建即 LUKS + 销毁时丢本地密钥 (crypto-shredding), 已在步骤 1 完成。")
EOF

# =====================================================================
# 待真机确认:
#   [D1] 远端盘 LUKS keyfile 的真实路径/命名 (阶段1 packer/terraform 定); 若非
#        $LUKS_KEY_DIR/<name>.key, 用 LUKS_KEY_DIR 环境变量或改约定。
#   [D2] 本脚本在 dom0 还是 vault-cloud 执行: LUKS 密钥在哪个 qube 就在哪跑步骤 1;
#        凭据文件在 vault-cloud, 步骤 2 通常在 vault-cloud 内跑。按你的密钥布局分别执行。
# =====================================================================
