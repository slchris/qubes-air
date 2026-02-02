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

# 创建 sys-remote 模板
create_sys_remote_template() {
    local template_name="fedora-39"
    local sys_remote_name="sys-remote-pve"
    
    log_info "Creating sys-remote: $sys_remote_name"
    
    # 检查是否已存在
    if qvm-check "$sys_remote_name" &>/dev/null; then
        log_warn "sys-remote '$sys_remote_name' already exists"
        return
    fi
    
    # 创建 sys-remote AppVM
    qvm-create --class AppVM \
        --template "$template_name" \
        --property netvm=sys-firewall \
        --property provides_network=true \
        --property memory=1024 \
        --property maxmem=2048 \
        --label orange \
        "$sys_remote_name"
    
    log_info "sys-remote created successfully"
}

# 配置 qrexec 策略
setup_qrexec_policy() {
    local policy_file="/etc/qubes/policy.d/80-qubes-air.policy"
    
    log_info "Setting up qrexec policy..."
    
    cat > "$policy_file" << 'EOF'
# Qubes Air qrexec Policy
# 
# 控制 Qubes Air 相关服务的访问权限

# 允许指定 Qube 访问 sys-remote 远程执行服务
qubes-air.Remote * sys-remote-* @default allow

# 允许 sys-remote 请求网络操作
qubes-air.Network sys-remote-* @adminvm allow

# 拒绝其他所有 Qubes Air 相关请求
qubes-air.* * * deny
EOF
    
    chmod 644 "$policy_file"
    log_info "qrexec policy configured"
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
    
    create_sys_remote_template
    setup_qrexec_policy
    setup_salt
    
    log_info "=== Qubes Air initialization complete ==="
    log_info "Next steps:"
    log_info "  1. Start sys-remote: qvm-start sys-remote-pve"
    log_info "  2. Configure WireGuard in sys-remote"
    log_info "  3. Apply Salt states: qubes-salt --all state.apply"
}

main "$@"
