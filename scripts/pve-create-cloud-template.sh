#!/bin/bash
# Qubes Air - 在 Proxmox 节点上创建 cloud-init 模板 VM
#
# ⚠️ 这个脚本运行在 **PVE 节点上**, 不是 dom0, 也不是你的 Mac。
#    获取节点 shell 的方式: PVE Web UI -> Datacenter -> 展开选中**具体节点** -> Shell
#    (SSH 22 从外部不可达; 只有 443 经 ingress 通, Web Shell 走的是同一条 443)
#
# 为什么不用 Packer (2026-07 复核结论):
#   - Packer 需要从**运行 Packer 的机器**直连 build VM 的 22 端口; 那台 VM 在 PVE 内网,
#     Mac 侧没有路由。proxmox-iso / proxmox-clone 共用同一套 communicator, 两个都不行。
#   - proxmox-iso 的 http_directory 还需要 VM **反向连回** Packer 主机取 kickstart。
#   - Apple Silicon 上 `-accel kvm` 无效 (只有 tcg), x86_64 镜像要全软件模拟。
#   本脚本用 `qm` 在节点本地完成同样的事, 无需上述任何网络条件。
#
# 产出: 一个 terraform 模块可以直接 clone 的模板 VM。满足模块的硬要求:
#   systemd + cloud-init + qemu-guest-agent + **启动盘在 scsi0** + sshd 公钥登录
#
# 用法:
#   bash pve-create-cloud-template.sh                      # 用默认值 (Fedora 43, VMID 9000)
#   VMID=9001 TEMPLATE_NAME=fedora-43-minimal bash pve-create-cloud-template.sh
#   STORAGE=local-zfs OS=debian13 bash pve-create-cloud-template.sh
#
# ⚠️ 长命令建议挂在 tmux 里跑: ingress-nginx 默认 proxy-read-timeout 60s,
#    Web Shell 的 websocket 闲置会被掐断。
#      tmux new -s tmpl    然后在里面跑本脚本; 断线后 tmux attach -t tmpl

set -euo pipefail

# ============================================
# 可调参数
# ============================================

VMID="${VMID:-9000}"
OS="${OS:-fedora43}"            # fedora43 | fedora44 | debian12 | debian13
STORAGE="${STORAGE:-local-lvm}" # 放磁盘的 datastore
BRIDGE="${BRIDGE:-vmbr0}"
TEMPLATE_NAME="${TEMPLATE_NAME:-}"
WORKDIR="${WORKDIR:-/var/lib/vz/template/qcow}"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; BOLD=$'\033[1m'; NC=$'\033[0m'
info()  { echo "${GREEN}[INFO]${NC} $*"; }
warn()  { echo "${YELLOW}[WARN]${NC} $*"; }
err()   { echo "${RED}[ERROR]${NC} $*" >&2; }
die()   { err "$*"; exit 1; }

# ============================================
# 镜像定义
#
# 注意文件名里的 "Generic" —— Fedora 从 F40 起改了命名。仓库里旧的
# packer 模板固化的是 F39 老命名的 URL, 现在实测 404, 这就是原因之一。
# 校验和不硬编码, 从官方 CHECKSUM 文件动态取, 避免同样的腐烂。
# ============================================

case "$OS" in
  fedora43)
    IMG_FILE="Fedora-Cloud-Base-Generic-43-1.6.x86_64.qcow2"
    IMG_URL="https://download.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/x86_64/images/${IMG_FILE}"
    SUM_URL="https://download.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/x86_64/images/Fedora-Cloud-43-1.6-x86_64-CHECKSUM"
    DEFAULT_NAME="fedora-43-cloud" ; CI_USER="fedora" ;;
  fedora44)
    IMG_FILE="Fedora-Cloud-Base-Generic-44-1.7.x86_64.qcow2"
    IMG_URL="https://download.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/${IMG_FILE}"
    SUM_URL="https://download.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/Fedora-Cloud-44-1.7-x86_64-CHECKSUM"
    DEFAULT_NAME="fedora-44-cloud" ; CI_USER="fedora" ;;
  debian12)
    IMG_FILE="debian-12-genericcloud-amd64.qcow2"
    IMG_URL="https://cloud.debian.org/images/cloud/bookworm/latest/${IMG_FILE}"
    SUM_URL="https://cloud.debian.org/images/cloud/bookworm/latest/SHA512SUMS"
    DEFAULT_NAME="debian-12-cloud" ; CI_USER="debian" ;;
  debian13)
    IMG_FILE="debian-13-genericcloud-amd64.qcow2"
    IMG_URL="https://cloud.debian.org/images/cloud/trixie/latest/${IMG_FILE}"
    SUM_URL="https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS"
    DEFAULT_NAME="debian-13-cloud" ; CI_USER="debian" ;;
  *) die "未知 OS='$OS' (可选: fedora43 fedora44 debian12 debian13)" ;;
esac

TEMPLATE_NAME="${TEMPLATE_NAME:-$DEFAULT_NAME}"

# ============================================
# 0) 环境前置检查
# ============================================

command -v qm >/dev/null || die "找不到 qm —— 这个脚本必须在 Proxmox 节点上运行, 不是在 dom0 或你的 Mac 上"
[[ $EUID -eq 0 ]] || die "需要 root (Web Shell 默认就是 root)"

NODE="$(hostname)"
echo
echo "${BOLD}================================================================${NC}"
echo "${BOLD} 当前节点: ${GREEN}${NODE}${NC}"
echo "${BOLD}================================================================${NC}"
echo
warn "你的集群是**多节点**的 (已观测到 infra-node1/2/4/6, ingress 在轮询)。"
warn "而 local-lvm 是**节点本地存储** —— 在这台建的模板, 别的节点上不存在。"
warn "务必确认这就是你打算跑 VM 的那个节点, 并且 terraform 里的"
warn "  node_name 要写成: ${BOLD}${NODE}${NC}"
echo
read -rp "确认在 ${NODE} 上继续? [y/N] " ans
[[ "$ans" == "y" || "$ans" == "Y" ]] || die "已取消"

# VMID 占用检查 (幂等保护: 不覆盖已有 VM)
if qm status "$VMID" &>/dev/null; then
  die "VMID $VMID 已存在。换一个 VMID, 或先手动确认后销毁: qm destroy $VMID"
fi

# 存储可用性检查 —— tfvars 默认的 local-lvm 未必存在
info "本节点可用存储:"
pvesm status | awk 'NR==1 || $2=="lvmthin" || $2=="dir" || $2=="zfspool" || $2=="lvm" || $2=="rbd" || $2=="nfs" {print "      "$0}'
echo
pvesm status | awk '{print $1}' | tail -n +2 | grep -qx "$STORAGE" \
  || die "存储 '$STORAGE' 在本节点不存在。用 STORAGE=<名字> 重跑 (见上表)"

# 确认该存储能放虚拟磁盘 (content 需含 images)
if ! pvesm status --content images 2>/dev/null | awk '{print $1}' | tail -n +2 | grep -qx "$STORAGE"; then
  die "存储 '$STORAGE' 不支持 content type 'images' (放不了虚拟磁盘)。换一个存储。"
fi

# 网桥检查 —— 错的网桥会造出一台网卡静默不通的 VM
if ! ip link show "$BRIDGE" &>/dev/null; then
  warn "网桥 '$BRIDGE' 在本节点不存在。现有网桥:"
  ip -o link show type bridge | awk -F': ' '{print "      "$2}'
  die "用 BRIDGE=<名字> 重跑"
fi

# ============================================
# 1) 下载镜像并校验
# ============================================

mkdir -p "$WORKDIR"
cd "$WORKDIR"

if [[ -f "$IMG_FILE" ]]; then
  info "已存在 $IMG_FILE, 跳过下载 (要强制重下就先删掉它)"
else
  info "下载 $IMG_FILE (约 550MB, 慢的话去泡杯茶) ..."
  curl -fL --progress-bar -o "${IMG_FILE}.part" "$IMG_URL" || die "下载失败: $IMG_URL"
  mv "${IMG_FILE}.part" "$IMG_FILE"
fi

info "校验 SHA256 (从官方 CHECKSUM 动态取, 不信任本地硬编码值)..."
if [[ "$OS" == fedora* ]]; then
  EXPECTED="$(curl -sSL --max-time 180 "$SUM_URL" \
    | grep -F "SHA256 (${IMG_FILE})" | awk '{print $NF}')"
  ACTUAL="$(sha256sum "$IMG_FILE" | awk '{print $1}')"
else
  EXPECTED="$(curl -sSL --max-time 180 "$SUM_URL" \
    | grep -F " ${IMG_FILE}" | awk '{print $1}')"
  ACTUAL="$(sha512sum "$IMG_FILE" | awk '{print $1}')"
fi

[[ -n "$EXPECTED" ]] || die "没能从 $SUM_URL 取到校验和 —— 上游可能改了文件名/版本, 请核对后更新脚本"
if [[ "$EXPECTED" != "$ACTUAL" ]]; then
  err "校验和不匹配!"
  err "  期望: $EXPECTED"
  err "  实际: $ACTUAL"
  die "镜像可能损坏或被篡改。删掉 $WORKDIR/$IMG_FILE 重下。"
fi
info "校验通过 ✓"

VSIZE_BYTES="$(qemu-img info --output=json "$IMG_FILE" | grep -o '"virtual-size": *[0-9]*' | grep -o '[0-9]*')"
VSIZE_GB=$(( VSIZE_BYTES / 1024 / 1024 / 1024 ))
info "镜像虚拟大小: ${VSIZE_GB}G"

# ============================================
# 2) 创建 VM 骨架
#
# 关键点 (直接对应 terraform 模块的硬要求):
#   --scsihw virtio-scsi-single : clone 会继承。模块在两块盘上都设了 iothread=1,
#                                 而 iothread 只在 virtio-scsi-single 下才被 PVE 真正采纳。
#                                 在模板上设好, 顺带把模块那个 iothread 空转的问题解决了。
#   --ostype l26                : 与模块的 operating_system{type="l26"} 对齐
#   --agent enabled=1           : 模块设了 agent{enabled=true} 并等待 agent 回报 IP;
#                                 没有它 provider 会干等 (默认 15m) 才失败
#   --serial0 socket            : cloud 镜像普遍假设有串口; 保留 VGA 以便 Web 控制台仍可用
# ============================================

info "创建 VM $VMID ($TEMPLATE_NAME) ..."
qm create "$VMID" \
  --name "$TEMPLATE_NAME" \
  --description "Qubes Air cloud-init base template ($OS) — 由 scripts/pve-create-cloud-template.sh 生成" \
  --memory 2048 \
  --cores 2 \
  --cpu x86-64-v2-AES \
  --net0 "virtio,bridge=${BRIDGE}" \
  --scsihw virtio-scsi-single \
  --ostype l26 \
  --agent enabled=1 \
  --serial0 socket \
  --tags "qubes-air,template"

# ============================================
# 3) 导入磁盘并挂到 scsi0
#
# PVE 8.x 用 `qm disk import` (旧的 `qm importdisk` 仍是别名但已废弃)。
# 本集群实测 8.2.2。
#
# ⚠️ 故意**不** resize。模块的 os_disk_gb 会把盘撑大 (dev-work=32, dev-disp=20),
#    而 Proxmox **不能缩盘** —— 模板盘一旦大于 os_disk_gb, clone 时会硬报
#    "requested size is lower than current size"。保持镜像原生大小最安全。
# ============================================

info "导入磁盘到 $STORAGE ..."
qm disk import "$VMID" "$IMG_FILE" "$STORAGE" >/dev/null

# 导入后磁盘挂在 unused0; 解析出真实 volume id 再挂到 scsi0, 不猜命名
VOLID="$(qm config "$VMID" | awk -F': ' '/^unused0:/ {print $2}')"
[[ -n "$VOLID" ]] || die "导入后没找到 unused0, 请手动检查: qm config $VMID"
info "磁盘卷: $VOLID -> scsi0"

qm set "$VMID" --scsi0 "${VOLID},discard=on,iothread=1" >/dev/null

# cloud-init 驱动器 (模块的 initialization{} 依赖它)
qm set "$VMID" --ide2 "${STORAGE}:cloudinit" >/dev/null

# 从 scsi0 引导 —— 模块硬编码 scsi0, 错位会多出一块野盘
qm set "$VMID" --boot order=scsi0 >/dev/null

# ============================================
# 4) 转成模板
# ============================================

info "转换为模板 ..."
qm template "$VMID" >/dev/null

# ============================================
# 5) 结果与后续步骤
# ============================================

echo
echo "${BOLD}================================================================${NC}"
echo "${GREEN}${BOLD} 模板创建完成${NC}"
echo "${BOLD}================================================================${NC}"
qm config "$VMID" | grep -E '^(name|scsi0|scsihw|ide2|boot|agent|ostype|net0|serial0):' | sed 's/^/    /'
echo
echo "${BOLD}把这些值填进 terraform/environments/<你的>.tfvars:${NC}"
echo "    template_vm_id  = ${VMID}"
echo "    node_name       = \"${NODE}\"        # 多节点集群, 必须写死"
echo "    datastore_id    = \"${STORAGE}\""
echo "    network_bridge  = \"${BRIDGE}\""
echo "    ssh_public_keys = [\"ssh-ed25519 AAAA... your-key\"]   # 空数组会导致没有可登录账号"
echo
echo "${BOLD}并确保 os 盘大于模板盘 (${VSIZE_GB}G), 否则 clone 会因无法缩盘而失败:${NC}"
echo "    disk = 32     # 必须 > ${VSIZE_GB}"
echo
echo "${BOLD}provider 端点 (注意不带 :8006, 走 ingress 的 443):${NC}"
echo "    proxmox_config = { endpoint = \"https://pve.infra.plz.ac/\", node = \"${NODE}\" }"
echo
echo "${YELLOW}首次 apply 后建议验证 guest agent 真的在跑 (模块靠它拿 IP):${NC}"
echo "    qm agent <新VMID> ping    # 无输出即成功; 报错说明 agent 没起来"
echo
warn "cloud-init 默认用户是 '${CI_USER}'; 模块的 initialization.user_account 会另建 'qubes' 用户。"
warn "qrexec-client-vm **不在**本模板内 —— 这是已知的架构待决项, 见 docs/runbook-remotevm.md:48。"
