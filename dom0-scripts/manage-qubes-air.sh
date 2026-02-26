#!/bin/bash
# Qubes Air - 管理脚本
#
# 用于管理 sys-remote 和远程 Qube 的快捷命令
# 使用方法: sudo bash manage-qubes-air.sh [command] [args]

set -euo pipefail

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# 命令: 列出所有 sys-remote
cmd_list() {
    log_info "Listing sys-remote Qubes:"
    qvm-ls --tags sys-remote 2>/dev/null || qvm-ls | grep -E "^sys-remote" || echo "No sys-remote found"
}

# 命令: 启动 sys-remote
cmd_start() {
    local name="${1:-sys-remote-pve}"
    log_info "Starting $name..."
    qvm-start "$name"
}

# 命令: 停止 sys-remote
cmd_stop() {
    local name="${1:-sys-remote-pve}"
    log_info "Stopping $name..."
    qvm-shutdown "$name"
}

# 命令: 检查 VPN 状态
cmd_vpn_status() {
    local name="${1:-sys-remote-pve}"
    log_info "VPN status for $name:"
    qvm-run -p "$name" "wg show" 2>/dev/null || echo "WireGuard not configured"
}

# 命令: 连接远程 Zone
cmd_connect() {
    local zone="${1:-}"
    if [ -z "$zone" ]; then
        log_error "Usage: $0 connect <zone-name>"
        exit 1
    fi
    
    local sys_remote="sys-remote-$zone"
    
    log_info "Connecting to zone: $zone via $sys_remote"
    
    # 启动 sys-remote
    qvm-start "$sys_remote" 2>/dev/null || true
    
    # 启动 WireGuard
    qvm-run -p "$sys_remote" "sudo systemctl start wg-quick@wg0"
    
    # 检查连接
    sleep 2
    qvm-run -p "$sys_remote" "wg show wg0 | head -5"
    
    log_info "Connected to $zone"
}

# 命令: 断开远程 Zone
cmd_disconnect() {
    local zone="${1:-}"
    if [ -z "$zone" ]; then
        log_error "Usage: $0 disconnect <zone-name>"
        exit 1
    fi
    
    local sys_remote="sys-remote-$zone"
    
    log_info "Disconnecting from zone: $zone"
    qvm-run -p "$sys_remote" "sudo systemctl stop wg-quick@wg0" || true
    log_info "Disconnected"
}

# 命令: 执行远程命令
cmd_remote_exec() {
    local zone="${1:-}"
    shift || true
    local command="$*"
    
    if [ -z "$zone" ] || [ -z "$command" ]; then
        log_error "Usage: $0 exec <zone-name> <command>"
        exit 1
    fi
    
    local sys_remote="sys-remote-$zone"
    log_info "Executing on $zone: $command"
    
    # 通过 qrexec 执行远程命令
    qrexec-client -d "$sys_remote" "DEFAULT:QUBESRPC qubes-air.Remote" <<< "$command"
}

# 帮助信息
cmd_help() {
    cat << EOF
Qubes Air Management Script

Usage: $0 <command> [args]

Commands:
  list                    List all sys-remote Qubes
  start [name]            Start a sys-remote (default: sys-remote-pve)
  stop [name]             Stop a sys-remote
  vpn-status [name]       Show VPN status
  connect <zone>          Connect to a remote zone
  disconnect <zone>       Disconnect from a remote zone
  exec <zone> <command>   Execute command on remote zone
  help                    Show this help

Examples:
  $0 list
  $0 start sys-remote-gcp
  $0 connect proxmox
  $0 exec proxmox "terraform plan"
EOF
}

# 主入口
main() {
    local command="${1:-help}"
    shift || true
    
    case "$command" in
        list)        cmd_list "$@" ;;
        start)       cmd_start "$@" ;;
        stop)        cmd_stop "$@" ;;
        vpn-status)  cmd_vpn_status "$@" ;;
        connect)     cmd_connect "$@" ;;
        disconnect)  cmd_disconnect "$@" ;;
        exec)        cmd_remote_exec "$@" ;;
        help|--help|-h) cmd_help ;;
        *)
            log_error "Unknown command: $command"
            cmd_help
            exit 1
            ;;
    esac
}

main "$@"
