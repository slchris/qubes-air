#!/bin/bash
# scripts/publish-agent-deb.sh
# =====================================================================
# 把构建好的 qubes-air-agent .deb 上传到局域网 artifact store, 并打印
# console 可以直接粘贴的 URL + SHA256。
#
# 为什么这一步单独成脚本, 而不是 build 脚本顺手做:
#   artifact store **没有任何认证**, 而且是明文 HTTP。局域网上任何人都能
#   POST 覆盖同名文件。所以「我上传了什么」和「它现在对外发什么」是**两个
#   不同的断言**, 不能互相替代。本脚本上传后会**重新下载再算一次哈希**,
#   两个哈希对不上就失败 —— 这不是偏执, 这是这条链路上唯一的完整性检查。
#
#   最终防线在 console: 它把这里打印的 SHA256 写进每台 qube 的身份文件
#   (走 console -> terraform SFTP -> PVE snippet -> cloud-init 这条可信路径),
#   cloud-init 下载完 .deb 先比对哈希再 dpkg -i。下载通道不可信, 但哈希
#   是从可信通道来的, 所以整体成立。详见 docs/bootstrap-design.md §6。
#
# 用法:
#   scripts/publish-agent-deb.sh                          # 自动用 dist/ 里唯一的 .deb
#   scripts/publish-agent-deb.sh dist/qubes-air-agent_0.1.0_amd64.deb
#   DRY_RUN=1 scripts/publish-agent-deb.sh                # 只做本地校验, 不联网
#   OVERWRITE=true scripts/publish-agent-deb.sh           # 覆盖同名 artifact (见下)
#
# 输出约定: **配置行走 stdout, 其余全部走 stderr**, 所以可以直接
#   scripts/publish-agent-deb.sh >> console.env
# =====================================================================

set -euo pipefail

ARTIFACT_BASE="${ARTIFACT_BASE:-http://10.31.0.2}"
ARTIFACT_DIR="${ARTIFACT_DIR:-qubes-air}"
DIST_DIR="${DIST_DIR:-dist}"
# 默认 false: 覆盖是有代价的动作。已经建好的 qube 不受影响 (它们早下载完了),
# 但任何还没跑 cloud-init 的 qube 会拿到新字节、对不上 console 里钉的旧哈希,
# 然后启动失败。要发新版就换 version, 不要就地覆盖。
OVERWRITE="${OVERWRITE:-false}"
DRY_RUN="${DRY_RUN:-0}"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; BOLD=$'\033[1m'; NC=$'\033[0m'
info() { echo "${GREEN}[INFO]${NC} $*" >&2; }
warn() { echo "${YELLOW}[WARN]${NC} $*" >&2; }
err()  { echo "${RED}[ERROR]${NC} $*" >&2; }
die()  { err "$*"; exit 1; }

# 仓库根目录, 这样从哪里调用都能找到 dist/。
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# 统一清理: 单个 EXIT trap 管所有临时文件。分散在各处的 trap 会互相覆盖,
# 而且 die 是 exit 不是 return, 函数级的 RETURN trap 在出错路径上根本不触发。
CLEANUP_PATHS=()
cleanup() { [ ${#CLEANUP_PATHS[@]} -gt 0 ] && rm -rf "${CLEANUP_PATHS[@]}"; return 0; }
trap cleanup EXIT

# ============================================
# 1. 选定要发布的 .deb
# ============================================

DEB="${1:-}"

if [ -z "$DEB" ]; then
    # 刻意**不**自动挑「最新的那个」: dist/ 里躺着两个版本时, 按时间戳猜
    # 等于把「发错版本」变成一个静默失败。宁可让人显式指定。
    # DIST_DIR 可以是绝对路径, 也可以是相对仓库根的路径。
    case "$DIST_DIR" in
        /*) search_dir="$DIST_DIR" ;;
        *)  search_dir="$REPO_ROOT/$DIST_DIR" ;;
    esac

    shopt -s nullglob
    candidates=("$search_dir"/qubes-air-agent_*_amd64.deb)
    shopt -u nullglob

    case "${#candidates[@]}" in
        0) die "$search_dir/ 里没有 qubes-air-agent_*_amd64.deb。先跑 'make agent-deb'。" ;;
        1) DEB="${candidates[0]}" ;;
        *)
            err "$search_dir/ 里有多个 .deb, 拒绝替你猜发哪个:"
            for c in "${candidates[@]}"; do err "    $(basename "$c")"; done
            die "显式指定: scripts/publish-agent-deb.sh <path-to-deb>"
            ;;
    esac
fi

[ -f "$DEB" ] || die "找不到文件: $DEB"
[ -s "$DEB" ] || die "文件是空的: $DEB"

DEB_NAME="$(basename "$DEB")"
# 转绝对路径: 架构自检要 cd 进临时目录再 'ar x', 相对路径到那里就失效了。
DEB="$(cd "$(dirname "$DEB")" && pwd)/$DEB_NAME"

# 文件名就是版本的唯一记录 —— console 钉的是 URL, URL 里只有这个名字。
# 名字不规范 = 事后没人能从 console 配置反查出跑的是哪次构建。
if [[ ! "$DEB_NAME" =~ ^qubes-air-agent_([A-Za-z0-9.+~-]+)_amd64\.deb$ ]]; then
    die "文件名不符合约定 qubes-air-agent_<version>_amd64.deb: $DEB_NAME"
fi
DEB_VERSION="${BASH_REMATCH[1]}"

# ============================================
# 2. 架构自检 —— 本项目最想消灭的那类静默失败
#
# 构建机是 Apple Silicon (arm64), 目标 VM 是 amd64 Debian 12。二进制若误编成
# arm64, VM 上 exec 直接 "exec format error"; 而 cloud-init 里那句 install 是
# **软失败**的 —— qube 看起来一切正常, agent 是死的。这正是当初真机验证踩到的
# 坑的同一形状 (那次是 unit 根本不存在, 同样在日志里几乎不可见)。
#
# 所以在**发布前**卡住, 而不是等 qube 起来再查。发布是最后一道闸: 一个错架构
# 的包配一个完全正确的哈希, 是校验得无懈可击的垃圾。
# ============================================

check_architecture() {
    # 首选 dpkg-deb: 它同时能看 control 里的 Architecture 字段 (元数据)。
    if command -v dpkg-deb >/dev/null 2>&1; then
        local ctrl_arch
        ctrl_arch="$(dpkg-deb --field "$DEB" Architecture 2>/dev/null || true)"
        [ "$ctrl_arch" = "amd64" ] || die "control 里 Architecture=$ctrl_arch, 期望 amd64"
        info "control Architecture: amd64 ✓"
    else
        warn "本机没有 dpkg-deb, 跳过 control 元数据检查 (macOS 上正常)"
    fi

    # 元数据说 amd64 不代表**里面的二进制**是 amd64 —— control 的 Architecture
    # 只是一个字符串, 谁都能写对。真正会炸的是 ELF header, 所以直接读它。
    if ! command -v ar >/dev/null 2>&1; then
        warn "本机没有 ar, 无法检查内嵌二进制的 ELF 架构"
        return 0
    fi

    local tmp; tmp="$(mktemp -d)"
    CLEANUP_PATHS+=("$tmp")

    # .deb 是标准 ar 归档, 所以 macOS 自带的 ar 也能拆 —— 不需要装 dpkg。
    ( cd "$tmp" && ar x "$DEB" ) 2>/dev/null || { warn "解不开 .deb (ar x 失败), 跳过 ELF 检查"; return 0; }

    local data; data=""
    local f; for f in "$tmp"/data.tar*; do [ -f "$f" ] && { data="$f"; break; }; done
    [ -n "$data" ] || { warn "包里没找到 data.tar*, 跳过 ELF 检查"; return 0; }

    tar -xf "$data" -C "$tmp" ./usr/bin/qubes-air-agent 2>/dev/null \
        || tar -xf "$data" -C "$tmp" usr/bin/qubes-air-agent 2>/dev/null \
        || { warn "包里没有 usr/bin/qubes-air-agent, 跳过 ELF 检查"; return 0; }

    local bin="$tmp/usr/bin/qubes-air-agent"
    [ -f "$bin" ] || { warn "解出来没有二进制, 跳过 ELF 检查"; return 0; }

    # ELF header: e_machine 在偏移 0x12, 小端 2 字节。x86-64 = 0x3e, aarch64 = 0xb7。
    local machine; machine="$(od -An -tx1 -j18 -N2 "$bin" | tr -d ' \n')"
    case "$machine" in
        3e00) info "内嵌二进制 ELF 架构: x86-64 ✓" ;;
        b700) die "内嵌二进制是 aarch64 —— VM 上会 'exec format error'。用 GOOS=linux GOARCH=amd64 重编。" ;;
        *)    die "内嵌二进制 ELF e_machine=0x$machine, 不是 x86-64。用 GOOS=linux GOARCH=amd64 重编。" ;;
    esac
}

check_architecture

# ============================================
# 3. 本地哈希 —— 「我上传了什么」
# ============================================

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        die "既没有 sha256sum 也没有 shasum, 算不出哈希"
    fi
}

LOCAL_SHA="$(sha256_of "$DEB")"
LOCAL_SIZE="$(wc -c < "$DEB" | tr -d ' ')"

info "包:     $DEB_NAME (version $DEB_VERSION, ${LOCAL_SIZE} bytes)"
info "本地哈希: $LOCAL_SHA"

DEB_URL="$ARTIFACT_BASE/local/$ARTIFACT_DIR/$DEB_NAME"

# ============================================
# 4. 上传
#
# URL 是**按约定拼出来**的, 不解析上传接口的返回体: 反正下一步要重新下载验证,
# 拼错了会在验证那步炸掉。少一个对返回格式的依赖。
# ============================================

if [ "$DRY_RUN" = "1" ]; then
    warn "DRY_RUN=1 —— 不上传、不验证。会执行的是:"
    echo "  curl -fsS -X POST '$ARTIFACT_BASE/api/artifacts' \\" >&2
    echo "       -F 'file=@$DEB' \\" >&2
    echo "       -F 'directory=$ARTIFACT_DIR' \\" >&2
    echo "       -F 'overwrite=$OVERWRITE'" >&2
    warn "预期 URL: $DEB_URL"
    exit 0
fi

command -v curl >/dev/null 2>&1 || die "找不到 curl"

# ============================================
# 3.5 登录
#
# 写入端点要认证 (未登录 401), 读取端点不要。凭证只从环境读, 绝不落进脚本或
# 命令行 —— 命令行参数在 /proc 里对同机所有用户可见。
# cookie jar 建在 umask 077 下并在退出时删除: 它是一个可重放的会话凭据。
# ============================================

: "${MIRROR_USERNAME:?未设置。请 export MIRROR_USERNAME / MIRROR_PASSWORD}"
: "${MIRROR_PASSWORD:?未设置。请 export MIRROR_USERNAME / MIRROR_PASSWORD}"

COOKIE_JAR="$(umask 077; mktemp)"
CLEANUP_PATHS+=("$COOKIE_JAR")

info "登录 $ARTIFACT_BASE ..."
# 密码经 stdin 传给 curl (--data @-), 不进 argv。
if ! printf '{"username":"%s","password":"%s"}' "$MIRROR_USERNAME" "$MIRROR_PASSWORD" \
     | curl -fsS -c "$COOKIE_JAR" -X POST "$ARTIFACT_BASE/api/auth/login" \
            -H 'Content-Type: application/json' --data @- >/dev/null 2>&1; then
    die "登录失败 —— 检查 MIRROR_USERNAME / MIRROR_PASSWORD"
fi

info "上传到 $ARTIFACT_BASE/api/artifacts (directory=$ARTIFACT_DIR, overwrite=$OVERWRITE) ..."
if ! curl -fsS -X POST "$ARTIFACT_BASE/api/artifacts" \
        -F "file=@$DEB" \
        -b "$COOKIE_JAR" \
        -F "directory=$ARTIFACT_DIR" \
        -F "overwrite=$OVERWRITE" >&2; then
    err "上传失败。"
    err "若是同名文件已存在: 优先换 version 重新构建, 而不是 OVERWRITE=true —— "
    err "覆盖会让所有已钉旧哈希、但还没执行 cloud-init 的 qube 启动失败。"
    exit 1
fi
echo >&2

# ============================================
# 5. 回读验证 —— 「它现在对外发什么」
#
# 这一步不是走过场。至少三种情况会让 4 成功而 5 失败, 且只有 5 能发现:
#   - overwrite=false 且同名文件已存在 -> 服务端留着**旧文件**, 我们却打印新哈希
#   - 别人在这两步之间覆盖了同名 artifact (需凭证, 但凭证不止一人持有)
#   - 上传被截断 / 落盘出错
# 打印一个连不上服务器的哈希, 比不打印更糟: console 会照着它去钉。
# ============================================

VERIFY_TMP="$(mktemp)"
CLEANUP_PATHS+=("$VERIFY_TMP")

info "回读验证 $DEB_URL ..."
# no-cache: 中间缓存返回的 200 验证的是**没有 qube 会收到的那份拷贝**。
curl -fsS -H 'Cache-Control: no-cache' -H 'Pragma: no-cache' \
     -o "$VERIFY_TMP" "$DEB_URL" \
    || die "下载不回来。文件没落到预期路径, 或 directory/文件名与 URL 约定不一致。"

SERVED_SHA="$(sha256_of "$VERIFY_TMP")"
SERVED_SIZE="$(wc -c < "$VERIFY_TMP" | tr -d ' ')"

if [ "$SERVED_SHA" != "$LOCAL_SHA" ]; then
    err "哈希不一致 —— 服务端发的不是刚上传的那个文件。"
    err "  本地:   $LOCAL_SHA (${LOCAL_SIZE} bytes)"
    err "  服务端: $SERVED_SHA (${SERVED_SIZE} bytes)"
    err ""
    err "最常见原因: 同名 artifact 已存在而 overwrite=$OVERWRITE, 服务端保留了旧文件。"
    err "也可能是别人在这中间覆盖了它 —— 该接口无认证, 局域网上任何人都能写。"
    err "**不要**把上面任何一个哈希填进 console, 先把服务端状态弄清楚。"
    exit 1
fi

info "服务端字节与本地一致 ✓ (${SERVED_SIZE} bytes)"

# ============================================
# 6. 输出 console 配置
# ============================================

warn "提醒: 上传要认证, 但**分发是明文 HTTP、无认证**。下面这个 SHA256 是这条链路上"
warn "      唯一的完整性控制 —— 它必须原样进 console 配置, 不能事后凭记忆重打。"
echo >&2
info "${BOLD}把下面两行填进 console 配置:${NC}"
echo >&2

# 变量名必须与 console 的 config 一致 (internal/config/config.go 的
# Orchestrator.AgentPackage*, yaml 键 agent_package_url / agent_package_sha256 /
# agent_package_version)。名字对不上不会报错 —— console 只会当成没配, 然后
# 每台新 qube 都装不上 agent, 又回到那个「看起来正常、agent 是死的」状态。
#
# VERSION 是**说明性**的 (console 以哈希为准), 但它是事后从 console 配置反查
# 「这批 qube 跑的是哪次构建」的唯一线索 —— 镜像 ID 已经不再承载这个信息了。
echo "QUBES_AIR_AGENT_PACKAGE_URL=$DEB_URL"
echo "QUBES_AIR_AGENT_PACKAGE_SHA256=$LOCAL_SHA"
echo "QUBES_AIR_AGENT_PACKAGE_VERSION=$DEB_VERSION"
