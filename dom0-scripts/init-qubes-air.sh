#!/bin/bash
# Qubes Air - 初始化脚本
#
# 在 dom0 中执行，用于初始化 Qubes Air 环境
# 使用方法: sudo bash init-qubes-air.sh

set -euo pipefail

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# 检查 dom0 环境
check_dom0() {
    if [ ! -f /etc/qubes-release ]; then
        log_error "This script must run in Qubes OS dom0"
        exit 1
    fi
    
    if [ "$(id -u)" -ne 0 ]; then
        log_error "This script must run as root"
        exit 1
    fi
    
    log_info "Running in Qubes $(cat /etc/qubes-release)"
}

# 创建本地 Relay (取代旧 sys-remote)
# =====================================================================
# 【已废弃 - 阶段2】旧 create_sys_remote_template 建的是 provides_network=true 的 AppVM
# (把 Relay 当本地网关, 违反平面分离)。阶段2 改为普通 Relay AppVM: 不做网关、不开 ip_forward。
# 创建逻辑收敛到 dom0-scripts/create-sys-relay.sh, 这里只做委派调用。
# =====================================================================
create_relay() {
    local relay_name="sys-relay-pve"
    # Declared and assigned separately (SC2155): `local x="$(cmd)"` returns
    # local's exit status, not the command's, so a failing command substitution
    # is invisible to `set -e`.
    local creator
    creator="$(dirname "$0")/create-sys-relay.sh"

    log_info "Creating local Relay via create-sys-relay.sh: $relay_name"
    if [ ! -f "$creator" ]; then
        log_error "找不到 create-sys-relay.sh"
        exit 1
    fi
    bash "$creator" --name "$relay_name"
}

# 配置 qrexec 策略
# =====================================================================
# 【已废弃 - 阶段2】原来这里内联的 policy 有多处非法/漏洞:
#   - 服务名 `qubes-air.*` 含非法 glob (新格式服务名不支持通配)
#   - source/target 写反 (sys-remote-* 作 source 却给 @adminvm allow -> Relay 直达 dom0 漏洞)
#   - @default 未带 target=、缺 argument 列
# 已收敛到单一权威来源: dom0-scripts/policy.d/30-qubes-air.policy
# (由 salt qubes-air.remotevm.dom0 或直接 cp 部署到 /etc/qubes/policy.d/)。
# 此函数改为部署那份正确 policy, 不再内联错误规则。
# =====================================================================
setup_qrexec_policy() {
    local src
    src="$(dirname "$0")/policy.d/30-qubes-air.policy"
    local policy_file="/etc/qubes/policy.d/30-qubes-air.policy"

    log_info "Deploying single-source qrexec policy -> $policy_file"

    if [ ! -f "$src" ]; then
        log_error "找不到权威 policy: $src"
        log_error "请从仓库 dom0-scripts/policy.d/30-qubes-air.policy 获取。"
        exit 1
    fi

    # 移除旧的非法 policy (若存在)
    if [ -f /etc/qubes/policy.d/80-qubes-air.policy ]; then
        log_warn "移除旧的非法 policy: 80-qubes-air.policy"
        rm -f /etc/qubes/policy.d/80-qubes-air.policy
    fi

    install -m 0644 "$src" "$policy_file"
    log_info "qrexec policy configured (single source: 30-qubes-air.policy)"
}

# 创建 Qubes Air Salt 状态目录
setup_salt() {
    local salt_dir="/srv/salt/qubes-air"
    local pillar_dir="/srv/pillar/qubes-air"
    
    log_info "Setting up Salt directories..."
    
    mkdir -p "$salt_dir"
    mkdir -p "$pillar_dir"
    
    # 创建符号链接到项目目录 (如果存在)
    # ln -sf /path/to/qubes-air/salt/qubes-air/* "$salt_dir/"
    
    log_info "Salt directories ready"
}

# 主函数
main() {
    log_info "=== Qubes Air Initialization ==="
    
    check_dom0
    
    # 用户确认
    read -p "This will modify your Qubes system. Continue? [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        log_warn "Aborted by user"
        exit 1
    fi
    
    create_relay
    setup_qrexec_policy
    setup_salt

    log_info "=== Qubes Air initialization complete ==="
    log_info "Next steps (阶段2 RemoteVM 链路, 详见 docs/runbook-remotevm.md):"
    log_info "  1. 对 Relay 模板与 sys-relay-pve 应用 salt: qubes-air.remotevm.relay/.autossh"
    log_info "  2. 创建 RemoteVM: bash create-remotevm.sh --name remote-dev-1 --relay sys-relay-pve --remote-name dev"
    log_info "  3. 在 mgmt-air 渲染 ssh config (消费 terraform output) 并投递到 Relay"
    log_info "  4. 从本地 AppVM 自检: qrexec-client-vm remote-dev-1 qubesair.Ping"
    log_warn "  注意: RemoteVM 不可 qvm-start (纯元数据 qube); WireGuard 方案已废弃, 改用 SSH transport。"
}

main "$@"
