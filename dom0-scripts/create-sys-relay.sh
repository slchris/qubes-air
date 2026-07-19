#!/bin/bash
# Qubes Air - dom0 侧 sys-relay 创建脚本 (阶段2)
# =====================================================================
# 创建本地 Relay AppVM (RemoteVM 架构里的 Local-Relay)。
#
# 关键设计约束 (评审确立, 违反算回归):
#   - 普通 AppVM, netvm=sys-firewall (能出站到 Remote-Relay)。
#   - 【不】provides_network=true  —— Relay 不做本地 qube 的上游网关, 远程 Zone 拿不到本地路由。
#   - 【不】开 ip_forward         —— 平面分离, 只承载 relay<->relay 点对点 SSH。
#   - 打 tag=relay 供 dom0 policy @tag:relay。
#   - 与调试用 mgmt-jump 隔离 (更干净)。
#
# 对比旧 sys-remote (已废弃): 旧脚本设了 provides_network=true (违规做网关), 本脚本明确不设。
#
# 用法: sudo bash create-sys-relay.sh --name sys-relay-pve [--template fedora-42] [--netvm sys-firewall]
# =====================================================================

set -euo pipefail

RELAY_NAME=""
TEMPLATE="fedora-42"
NETVM="sys-firewall"
LABEL="gray"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1" >&2; }

usage() {
    cat <<'EOF'
用法: create-sys-relay.sh --name <NAME> [选项]
  --name NAME        Relay AppVM 名 (如 sys-relay-pve)   [必填]
  --template TPL     基于的模板 (默认 fedora-42)
  --netvm NETVM      上游 netvm (默认 sys-firewall)
  --label LABEL      标签 (默认 gray)
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)     RELAY_NAME="${2:-}"; shift 2 ;;
        --template) TEMPLATE="${2:-}"; shift 2 ;;
        --netvm)    NETVM="${2:-}"; shift 2 ;;
        --label)    LABEL="${2:-}"; shift 2 ;;
        -h|--help)  usage; exit 0 ;;
        *) log_error "未知参数: $1"; usage; exit 1 ;;
    esac
done

[ -f /etc/qubes-release ] || { log_error "必须在 dom0 运行"; exit 1; }
[ -n "$RELAY_NAME" ] || { log_error "缺少 --name"; usage; exit 1; }

if qvm-check --quiet "$RELAY_NAME" 2>/dev/null; then
    log_warn "Relay '$RELAY_NAME' 已存在, 仅确保属性正确。"
else
    log_info "创建 Relay AppVM: $RELAY_NAME (template=$TEMPLATE, netvm=$NETVM)"
    qvm-create --class AppVM \
        --template "$TEMPLATE" \
        --label "$LABEL" \
        --property "netvm=$NETVM" \
        "$RELAY_NAME"
fi

# 显式关掉网关能力 (纵深防御: 即便模板/继承带了也覆盖掉)
log_info "强制 provides_network=False (Relay 不做本地网关)"
qvm-prefs "$RELAY_NAME" provides_network False

# 打 tag 供 policy
log_info "打 tag: relay"
qvm-tags "$RELAY_NAME" add relay 2>/dev/null || true

log_info "完成。注意: 本脚本不设 ip_forward、不设 provides_network=true (平面分离)。"
log_info "下一步: 对 Relay 模板与本 AppVM 应用 salt (qubes-air.remotevm.relay/.autossh)。"

# =====================================================================
# 待真机确认:
#   [R1] 目标 Qubes R4.3 默认模板名 (fedora-42 / fedora-41?); 按实机 qvm-template list 调整。
#   [R2] 是否需要给 Relay 加 qvm-firewall 限制其只能出站到 Remote-Relay 的 IP:port
#        (进一步收敛; 建议在 runbook 里补 qvm-firewall 规则)。
# =====================================================================
