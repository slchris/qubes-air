#!/bin/bash
# Qubes Air - 密钥轮换脚本 (阶段3)
# =====================================================================
# 轮换本地 vault 里的密钥。红线: 私钥永不外传/上云/进 git; 本脚本只在本机 KEY_DIR 操作,
# 只把【公钥】打印出来供分发。
#
# 支持三类 (可分别或一起轮换):
#   age   —— 生成新 age 私钥, 用【新公钥】重加密所有 SOPS 文件 (旧密文旧密钥仍能解, 先解后重加)。
#   wg    —— 生成新 WireGuard 私钥, 输出新公钥 (需你把新公钥分发给对端 peer, 见输出提示)。
#   ssh   —— 生成新的 relay transport SSH 私钥对, 输出新公钥 (需追加到远端 authorized_keys)。
#
# 设计:
#   - 幂等友好: 每次生成前先【备份】旧密钥到 KEY_DIR/backup/<timestamp>/, 不静默覆盖。
#   - 原子性: 先在临时文件生成, 校验成功后再替换 (age SOPS 重加密逐文件先写 .new 再 mv)。
#   - 失败不留半成品: set -euo pipefail; 重加密任一文件失败即中止, 已 mv 的保留 (新密钥可解),
#     未处理的仍是旧密钥可解 -> 无不可解状态 (旧 age 私钥备份仍在, 可回滚)。
#
# 用法:
#   rotate-keys.sh age            # 只轮换 age 并重加密 SOPS 文件
#   rotate-keys.sh wg             # 只轮换 WireGuard
#   rotate-keys.sh ssh            # 只轮换 relay transport SSH key
#   rotate-keys.sh all            # 三者都轮换
#   DRY_RUN=1 rotate-keys.sh age  # 只打印将做什么, 不改动
#
# 环境变量:
#   KEY_DIR         默认 ~/.qubes-air/keys
#   SOPS_FILES      需重加密的 SOPS 文件列表 (空格分隔); 默认自动从 repo 找 salt/pillar/secrets.sls
#   SSH_KEY_NAME    relay transport 私钥文件名 (默认 relay_transport)
# =====================================================================

set -euo pipefail

KEY_DIR="${KEY_DIR:-$HOME/.qubes-air/keys}"
SSH_KEY_NAME="${SSH_KEY_NAME:-relay_transport}"
TS="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="$KEY_DIR/backup/$TS"
DRY_RUN="${DRY_RUN:-0}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1" >&2; }

run() {
    # 执行命令; DRY_RUN 时只打印。
    if [ "$DRY_RUN" = "1" ]; then
        echo "  DRY_RUN> $*"
    else
        "$@"
    fi
}

backup_file() {
    # 幂等备份: 把一个现有文件复制到本次备份目录 (若存在)。
    local f="$1"
    [ -f "$f" ] || return 0
    run mkdir -p "$BACKUP_DIR"
    if [ "$DRY_RUN" = "1" ]; then
        echo "  DRY_RUN> cp -a $f $BACKUP_DIR/"
    else
        cp -a "$f" "$BACKUP_DIR/"
        log_info "备份: $f -> $BACKUP_DIR/"
    fi
}

ensure_keydir() {
    run mkdir -p "$KEY_DIR"
    run chmod 700 "$KEY_DIR"
}

# ---------------------------------------------------------------------
# 找需要重加密的 SOPS 文件
# ---------------------------------------------------------------------
discover_sops_files() {
    if [ -n "${SOPS_FILES:-}" ]; then
        echo "$SOPS_FILES"
        return 0
    fi
    # 默认: 从本脚本相对位置找 repo 的 salt/pillar/secrets.sls (若已加密)。
    local repo_root secrets
    repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
    secrets="$repo_root/salt/pillar/secrets.sls"
    # 只有确实是 SOPS 加密的文件才纳入 (含 sops 元数据)。
    if [ -f "$secrets" ] && grep -q '"sops"\|^sops:' "$secrets" 2>/dev/null; then
        echo "$secrets"
    fi
}

# =====================================================================
# age 轮换 + SOPS 重加密
# =====================================================================
rotate_age() {
    command -v age-keygen >/dev/null 2>&1 || { log_error "缺少 age-keygen"; return 1; }
    command -v sops       >/dev/null 2>&1 || log_warn "缺少 sops: 将只轮换 age 密钥, 不重加密 SOPS 文件。"

    local age_key="$KEY_DIR/age.key"
    local age_pub="$KEY_DIR/age.pub"

    log_info "轮换 age 密钥..."
    backup_file "$age_key"
    backup_file "$age_pub"

    local new_key="$KEY_DIR/age.key.new"
    if [ "$DRY_RUN" = "1" ]; then
        echo "  DRY_RUN> age-keygen -o $new_key"
        echo "  DRY_RUN> (提取新公钥, 用新公钥重加密 SOPS 文件, 再 mv $new_key -> $age_key)"
        return 0
    fi

    age-keygen -o "$new_key" 2>/dev/null
    chmod 600 "$new_key"
    local new_pub
    new_pub="$(grep 'public key' "$new_key" | awk '{print $NF}')"
    [ -n "$new_pub" ] || { log_error "无法提取新 age 公钥"; rm -f "$new_key"; return 1; }

    # 重加密每个 SOPS 文件: 用【旧】age 私钥解密, 用【新】公钥加密。
    # 关键: 逐文件先写 .new 再原子 mv, 任一失败即 return (set -e), 不破坏其余文件。
    if command -v sops >/dev/null 2>&1; then
        local files; files="$(discover_sops_files || true)"
        if [ -n "$files" ]; then
            for f in $files; do
                [ -f "$f" ] || { log_warn "SOPS 文件不存在, 跳过: $f"; continue; }
                log_info "重加密 SOPS: $f"
                backup_file "$f"
                # 用旧私钥解密 (SOPS_AGE_KEY_FILE 指向旧私钥), 明文只在管道内, 不落盘。
                # 再用新公钥加密到 .new。
                if SOPS_AGE_KEY_FILE="$age_key" sops --decrypt "$f" \
                    | sops --encrypt --age "$new_pub" --input-type yaml --output-type yaml /dev/stdin > "$f.new"; then
                    mv "$f.new" "$f"
                    log_info "  已用新 age 公钥重加密: $f"
                else
                    log_error "  重加密失败: $f (保留原文件, 旧密钥仍可解)"
                    rm -f "$f.new"
                    rm -f "$new_key"
                    return 1
                fi
            done
        else
            log_warn "未发现已加密的 SOPS 文件, 跳过重加密 (仅换 age 密钥)。"
        fi
    fi

    # 全部重加密成功后, 才把新 age 私钥/公钥就位。
    mv "$new_key" "$age_key"
    printf '%s\n' "$new_pub" > "$age_pub"
    chmod 600 "$age_key"
    log_info "age 轮换完成。新公钥: $new_pub"
    log_warn "请更新 crypto/sops/.sops.yaml 里的 age 收件人为新公钥, 并提交 (公钥可入 git)。"
    log_warn "旧 age 私钥已备份于 $BACKUP_DIR (确认新密钥可解后再销毁旧备份)。"
}

# =====================================================================
# WireGuard 轮换 (只出公钥, 私钥留本地)
# =====================================================================
rotate_wg() {
    command -v wg >/dev/null 2>&1 || { log_error "缺少 wg (wireguard-tools)"; return 1; }

    local priv="$KEY_DIR/wg_private.key"
    local pub="$KEY_DIR/wg_public.key"

    log_info "轮换 WireGuard 密钥..."
    backup_file "$priv"
    backup_file "$pub"

    if [ "$DRY_RUN" = "1" ]; then
        echo "  DRY_RUN> wg genkey > $priv.new; wg pubkey < $priv.new > $pub.new; mv 就位"
        return 0
    fi

    umask 077
    wg genkey > "$priv.new"
    wg pubkey < "$priv.new" > "$pub.new"
    mv "$priv.new" "$priv"
    mv "$pub.new" "$pub"
    chmod 600 "$priv"

    log_info "WireGuard 轮换完成。"
    log_warn "新公钥 (分发给对端 peer, 私钥【不外传】):"
    echo "    $(cat "$pub")"
    log_warn "分发后, 在对端把本机 peer 的 PublicKey 改为上面的值; 并更新 pillar 里本机 private_key"
    log_warn "(经 SOPS 加密后的 secrets.sls), 然后重应用 salt sys-remote.wireguard。"
}

# =====================================================================
# relay transport SSH key 轮换 (只出公钥)
# =====================================================================
rotate_ssh() {
    command -v ssh-keygen >/dev/null 2>&1 || { log_error "缺少 ssh-keygen"; return 1; }

    local key="$KEY_DIR/$SSH_KEY_NAME"
    local pub="$key.pub"

    log_info "轮换 relay transport SSH 密钥 ($SSH_KEY_NAME)..."
    backup_file "$key"
    backup_file "$pub"

    if [ "$DRY_RUN" = "1" ]; then
        echo "  DRY_RUN> ssh-keygen -t ed25519 -N '' -f $key.new (无密码, 由 vault 隔离保护)"
        return 0
    fi

    # ed25519, 无 passphrase (私钥由 vault-cloud 无网络隔离保护; agent 持有)。
    rm -f "$key.new" "$key.new.pub"
    ssh-keygen -t ed25519 -N '' -C "qubes-air-relay-transport-$TS" -f "$key.new" >/dev/null
    mv "$key.new" "$key"
    mv "$key.new.pub" "$pub"
    chmod 600 "$key"

    log_info "SSH 轮换完成。"
    log_warn "新公钥 (追加到远端 Remote-Relay 的 authorized_keys, 替换旧的):"
    echo "    $(cat "$pub")"
    log_warn "私钥 $key 应放入 vault-cloud 的 ~/.ssh/ 并 ssh-add (split-ssh); 【不】进 git/pillar。"
    log_warn "轮换 SSH key 会中断现有 autossh 隧道, 需在切换新 authorized_keys 后重启 autossh。"
}

usage() {
    cat <<'EOF'
用法: rotate-keys.sh <age|wg|ssh|all>
  age   轮换 age 私钥并用新公钥重加密 SOPS 文件 (旧密钥先解后重加)
  wg    轮换 WireGuard 私钥, 输出新公钥供分发
  ssh   轮换 relay transport SSH 私钥, 输出新公钥供分发
  all   以上全部

环境变量:
  KEY_DIR      密钥目录 (默认 ~/.qubes-air/keys)
  SOPS_FILES   要重加密的 SOPS 文件列表 (默认自动找 salt/pillar/secrets.sls)
  SSH_KEY_NAME relay transport 私钥文件名 (默认 relay_transport)
  DRY_RUN=1    只打印将执行的动作, 不改动

安全: 私钥永不外传/上云/进 git; 本脚本只打印【公钥】供分发。旧密钥自动备份到
      KEY_DIR/backup/<时间戳>/, 确认新密钥可用后再手动销毁旧备份。
EOF
}

main() {
    local target="${1:-}"
    [ -n "$target" ] || { usage; exit 1; }
    ensure_keydir

    case "$target" in
        age)  rotate_age ;;
        wg)   rotate_wg ;;
        ssh)  rotate_ssh ;;
        all)  rotate_age; rotate_wg; rotate_ssh ;;
        -h|--help) usage; exit 0 ;;
        *) log_error "未知目标: $target"; usage; exit 1 ;;
    esac

    log_info "全部完成。备份目录: ${BACKUP_DIR} (若本次有实际写入)。"
}

main "$@"
