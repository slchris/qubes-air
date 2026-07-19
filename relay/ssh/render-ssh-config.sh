#!/bin/bash
# render-ssh-config.sh —— 在 mgmt-air 上运行, 把阶段1 terraform output 渲染成 Relay 的 ssh config
# =====================================================================
# 衔接: 阶段1 remote-qube-base 模块对外输出【扁平】字段 (不是 result 对象):
#     terraform output -json 会给出 ip_address / data_disk_id / status / qube_name ...
# 本脚本读取其中 ip_address, 结合参数, 用 relay/ssh/config.template 渲染出 ~/.ssh/config,
# 再由 salt/scp 投递到目标 Relay。
#
# 【平面分离】本脚本在 mgmt-air (联网管理 AppVM) 上跑, 绝不在 dom0。dom0 保持离线。
#
# 用法 (在 mgmt-air):
#   ./render-ssh-config.sh \
#       --tf-dir  /path/to/terraform/environments/prod \
#       --tf-output-name remote_dev_1   \  # 该 qube 在 terraform 里的 output 名 (module output)
#       --remote-name dev \
#       --ssh-user  qubesrelay \
#       [--ssh-port 22] [--reverse-port 22000] \
#       --out ./rendered/config.dev
#
# 产物: 一个可直接放到 Relay ~/.ssh/config 的文件片段 (每个远端一段, 可 cat 拼接)。
# =====================================================================

set -euo pipefail

TF_DIR=""
TF_OUTPUT_NAME=""
REMOTE_NAME=""
SSH_USER=""
SSH_PORT="22"
REVERSE_PORT="22000"
OUT=""
TEMPLATE="$(dirname "$0")/config.template"

usage() {
    grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//' | head -n 30
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tf-dir)         TF_DIR="${2:-}"; shift 2 ;;
        --tf-output-name) TF_OUTPUT_NAME="${2:-}"; shift 2 ;;
        --remote-name)    REMOTE_NAME="${2:-}"; shift 2 ;;
        --ssh-user)       SSH_USER="${2:-}"; shift 2 ;;
        --ssh-port)       SSH_PORT="${2:-}"; shift 2 ;;
        --reverse-port)   REVERSE_PORT="${2:-}"; shift 2 ;;
        --out)            OUT="${2:-}"; shift 2 ;;
        --template)       TEMPLATE="${2:-}"; shift 2 ;;
        -h|--help)        usage; exit 0 ;;
        *) echo "未知参数: $1" >&2; exit 1 ;;
    esac
done

for v in TF_DIR TF_OUTPUT_NAME REMOTE_NAME SSH_USER OUT; do
    if [[ -z "${!v}" ]]; then
        echo "缺少必填参数: --${v,,}" >&2
        usage
        exit 1
    fi
done

command -v terraform >/dev/null 2>&1 || { echo "找不到 terraform" >&2; exit 1; }
command -v jq        >/dev/null 2>&1 || { echo "找不到 jq" >&2; exit 1; }
[[ -f "$TEMPLATE" ]] || { echo "找不到模板: $TEMPLATE" >&2; exit 1; }

# ---- 从 terraform output 取 ip_address ----
# 兼容两种布局:
#   (1) 顶层直接暴露该 qube 的对象 output (含 .ip_address)
#   (2) 顶层是 map, key 为 qube 名
echo "读取 terraform output ($TF_DIR, name=$TF_OUTPUT_NAME) ..." >&2
raw_json="$(terraform -chdir="$TF_DIR" output -json "$TF_OUTPUT_NAME")"

# 先试对象里的 .ip_address; 取不到再试整个值就是字符串 ip
ip="$(echo "$raw_json" | jq -r 'if type=="object" then (.ip_address // .value.ip_address // empty) else . end' 2>/dev/null || true)"
if [[ -z "$ip" || "$ip" == "null" ]]; then
    echo "错误: 从 output '$TF_OUTPUT_NAME' 取不到 ip_address。" >&2
    echo "该 qube 的 compute 可能未运行 (status=suspended -> ip_address 为空)。" >&2
    echo "原始值: $raw_json" >&2
    exit 1
fi

echo "远端 IP: $ip" >&2

# ---- 渲染模板 ----
mkdir -p "$(dirname "$OUT")"
sed \
    -e "s|{{ REMOTE_NAME }}|$REMOTE_NAME|g" \
    -e "s|{{ REMOTE_RELAY_IP }}|$ip|g" \
    -e "s|{{ SSH_USER }}|$SSH_USER|g" \
    -e "s|{{ SSH_PORT }}|$SSH_PORT|g" \
    -e "s|{{ REVERSE_PORT }}|$REVERSE_PORT|g" \
    "$TEMPLATE" > "$OUT"

echo "已生成: $OUT" >&2
echo "下一步: 把 $OUT 内容合入目标 Relay 的 ~/.ssh/config (经 salt/qvm-copy), 并预置 known_hosts.d/$REMOTE_NAME (host key pinning)。" >&2

# =====================================================================
# 待真机确认:
#   [D1] 阶段1 terraform output 的确切名字/层级 —— 本脚本按 `output -json <name>` 且值含
#        .ip_address 处理 (module main.tf 里 output "ip_address" 是模块级; 环境层如何 re-export
#        需看 terraform/environments/*, 见报告"衔接"说明)。
#   [D2] compute_running=false 时 ip_address 为空 —— 脚本已 fail-fast 提示先 resume。
# =====================================================================
