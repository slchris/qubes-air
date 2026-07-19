#!/bin/bash
# Qubes Air - dom0 侧 RemoteVM 创建 / 配置脚本 (阶段2)
#
# 目标: 在 dom0 用官方 R4.3 原语创建一个 RemoteVM, 绑定本地 Relay, 设置 transport_rpc /
#       remote_name, 并把 QubesDB /remote/<name> 映射写到 Relay 里。
#
# 官方机制核对 (见文件末尾 "官方核对" 注释块):
#   - RemoteVM 是一个 qube 类 (class RemoteVM(BaseVM)), 无 template / netvm 属性,
#     且 start()/suspend()/shutdown()/kill() 在源码里直接 raise —— 它是"纯元数据 qube",
#     绝不能对它 qvm-start。它只是本地 policy 引擎用来把请求改写到 Relay 的一条记录。
#   - 属性: relayvm (VMProperty), transport_rpc (str), remote_name (str)。
#
# 使用:
#   sudo bash create-remotevm.sh \
#       --name remote-dev-1 \
#       --relay sys-relay-pve \
#       --remote-name dev \
#       [--transport-rpc qubesair.SSHProxy] \
#       [--label gray]
#
# 幂等: 已存在则跳过创建、只更新属性与 QubesDB 映射。

set -euo pipefail

# ---- 默认值 ----
REMOTEVM_NAME=""
RELAY_VM=""
REMOTE_NAME=""
TRANSPORT_RPC="qubesair.SSHProxy"
LABEL="gray"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1" >&2; }

usage() {
    cat <<'EOF'
用法: create-remotevm.sh --name <NAME> --relay <RELAY> --remote-name <REMOTE_NAME> [选项]

必填:
  --name NAME            本地 RemoteVM 名 (如 remote-dev-1)
  --relay RELAY          本地 Relay LocalVM 名 (如 sys-relay-pve), 必须已存在
  --remote-name NAME     远端主机上该 qube 的原始名 (QubesDB /remote/<NAME> 映射的值)

可选:
  --transport-rpc RPC    Relay 上的 transport 服务名 (默认: qubesair.SSHProxy)
  --label LABEL          标签颜色 (默认: gray)
  -h, --help             显示帮助
EOF
}

# ---- 参数解析 ----
while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)          REMOTEVM_NAME="${2:-}"; shift 2 ;;
        --relay)         RELAY_VM="${2:-}"; shift 2 ;;
        --remote-name)   REMOTE_NAME="${2:-}"; shift 2 ;;
        --transport-rpc) TRANSPORT_RPC="${2:-}"; shift 2 ;;
        --label)         LABEL="${2:-}"; shift 2 ;;
        -h|--help)       usage; exit 0 ;;
        *) log_error "未知参数: $1"; usage; exit 1 ;;
    esac
done

check_dom0() {
    if [ ! -f /etc/qubes-release ]; then
        log_error "此脚本必须在 Qubes OS dom0 中运行"
        exit 1
    fi
    if ! command -v qvm-create >/dev/null 2>&1; then
        log_error "找不到 qvm-create —— 不在 dom0?"
        exit 1
    fi
    log_info "dom0: $(cat /etc/qubes-release)"
}

validate_args() {
    local missing=0
    [ -z "$REMOTEVM_NAME" ] && { log_error "缺少 --name"; missing=1; }
    [ -z "$RELAY_VM" ]      && { log_error "缺少 --relay"; missing=1; }
    [ -z "$REMOTE_NAME" ]   && { log_error "缺少 --remote-name"; missing=1; }
    [ "$missing" -eq 1 ] && { usage; exit 1; }

    # Relay 必须已存在 (它是承载 transport 的本地 AppVM)
    if ! qvm-check --quiet "$RELAY_VM" 2>/dev/null; then
        log_error "Relay '$RELAY_VM' 不存在。请先创建 sys-relay (见 runbook 步骤 2)。"
        exit 1
    fi
}

create_remotevm() {
    if qvm-check --quiet "$REMOTEVM_NAME" 2>/dev/null; then
        log_warn "RemoteVM '$REMOTEVM_NAME' 已存在, 跳过创建, 仅更新属性。"
        return 0
    fi

    log_info "创建 RemoteVM: $REMOTEVM_NAME (class=RemoteVM, 无 template)"
    # 关键: --class RemoteVM; RemoteVM 无 template/netvm。--property 用 NAME=VALUE 形式。
    # relayvm / transport_rpc / remote_name 可在创建时一次性用 --property 设入。
    qvm-create \
        --class RemoteVM \
        --label "$LABEL" \
        --property "relayvm=$RELAY_VM" \
        --property "transport_rpc=$TRANSPORT_RPC" \
        --property "remote_name=$REMOTE_NAME" \
        "$REMOTEVM_NAME"

    log_info "RemoteVM 创建完成。"
}

set_props() {
    # 幂等更新 (创建后或已存在时都跑一遍, 确保属性一致)。
    log_info "设置属性: relayvm=$RELAY_VM transport_rpc=$TRANSPORT_RPC remote_name=$REMOTE_NAME"
    qvm-prefs "$REMOTEVM_NAME" relayvm "$RELAY_VM"
    qvm-prefs "$REMOTEVM_NAME" transport_rpc "$TRANSPORT_RPC"
    qvm-prefs "$REMOTEVM_NAME" remote_name "$REMOTE_NAME"

    # 给 RemoteVM 打 tag, 供 policy 用 @tag:remote-zone 统一授权 (不用 glob)。
    qvm-tags "$REMOTEVM_NAME" add remote-zone 2>/dev/null || true
}

write_qubesdb_mapping() {
    # QubesDB /remote/<本地名> = <远端原始名>, 写在 *Relay* 的 QubesDB 里 (transport 脚本在
    # Relay 上执行 qubesdb-read "/remote/$target" 读取)。
    #
    # 注意: dom0 用 `qubesdb-write -d <vm>` 写目标 VM 的 QubesDB; Relay 必须处于 running。
    # QubesDB 内容不持久 —— Relay 重启后需重写。因此本映射也由 Relay 上的 qubes-air
    # 启动脚本 (见 salt remotevm.sls) 在开机时重建; 这里做一次即时写入以便当场验证。
    if ! qvm-check --running "$RELAY_VM" 2>/dev/null; then
        log_warn "Relay '$RELAY_VM' 未运行, 跳过即时 QubesDB 写入。"
        log_warn "映射将在 Relay 下次启动时由其启动脚本重建 (remote_map)。"
        return 0
    fi
    log_info "写 QubesDB (on $RELAY_VM): /remote/$REMOTEVM_NAME = $REMOTE_NAME"
    qubesdb-write -d "$RELAY_VM" "/remote/$REMOTEVM_NAME" "$REMOTE_NAME"
}

summary() {
    cat <<EOF

$(log_info "=== RemoteVM 配置完成 ===")
  名称:          $REMOTEVM_NAME
  relayvm:       $RELAY_VM
  transport_rpc: $TRANSPORT_RPC
  remote_name:   $REMOTE_NAME
  QubesDB:       /remote/$REMOTEVM_NAME -> $REMOTE_NAME (on $RELAY_VM)

下一步:
  1. 确认 policy 已就位: /etc/qubes/policy.d/30-qubes-air.policy
  2. 在 Relay 上部署 transport (salt: qubes-air.remotevm) 并起 autossh 出站隧道。
  3. 自检: 从本地 AppVM 调用 'qrexec-client-vm $REMOTEVM_NAME qubesair.Ping'
     (会经 Relay -> SSH -> Remote-Relay, 两侧 policy 各校验一次)。
EOF
}

main() {
    check_dom0
    validate_args
    create_remotevm
    set_props
    write_qubesdb_mapping
    summary
}

main "$@"

# =====================================================================
# 官方核对 (核实来源, 便于监工复查):
#   - RemoteVM 类与属性:
#       qubes-core-admin/qubes/vm/remotevm.py (master)
#       class RemoteVM(BaseVM); 属性 relayvm(VMProperty), transport_rpc(str),
#       remote_name(str), include_in_backups(bool, 默认 False)。
#       该类 start/suspend/shutdown/kill 均 raise -> RemoteVM 不可启动 (纯元数据)。
#   - qvm-create --class / --property NAME=VALUE:
#       dev.qubes-os.org core-admin-client qvm-create 手册:
#       "--class CLS (default AppVM)"; "--property NAME=VALUE ... Any property may be set"。
#       --template "when applicable", RemoteVM 无 template 属性故不传。
#   - 服务名改写格式 TRANSPORT_RPC+Remote-Qube+my_service+my_arg 与 SSH 示例:
#       dev.qubes-os.org qubes-core-qrexec / qrexec-remotevm.html。
#   - QubesDB /remote/<name>: 同上文档; transport 脚本读 qubesdb-read "/remote/$target"。
#
# 待真机确认 (Mac 无法验证):
#   [A1] qvm-create 是否接受在创建时一次性用 --property 设 relayvm/transport_rpc/remote_name,
#        还是必须先 create 再 qvm-prefs。本脚本两条路都走 (create 带 --property + 事后 qvm-prefs),
#        若创建时不认这些属性, 去掉 create_remotevm 里的三行 --property 即可, qvm-prefs 兜底。
#   [A2] `qvm-create --class RemoteVM` 是否需要 --label (BaseVM 可能不要求 label)。若报错, 去掉 --label。
#   [A3] `qubesdb-write -d <relay>` 在 dom0 写目标 VM QubesDB 的确切权限/语法 (R4.3)。
# =====================================================================
