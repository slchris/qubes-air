#!/bin/sh
# relay 侧 split-ssh 客户端桥接 —— 追加到 sys-relay 的 /rw/config/rc.local。
# =====================================================================
# 在 sys-relay 开机时建立一个本地 UNIX socket, 每次连接都经 qrexec 转发到
# vault-cloud 的 qubes.SshAgent 服务。ssh/autossh 把 SSH_AUTH_SOCK 指向这个 socket,
# 就能用 vault 里的私钥做签名, 而【永远拿不到私钥】。
#
# 对照社区 Split SSH 指南 (forum.qubes-os.org/t/split-ssh/19060) 的 rc.local 段, 仅把
# 目标 vault VM 名换成 vault-cloud。
#
# 之后 relay 上运行 ssh/autossh 的进程需要:
#     export SSH_AUTH_SOCK="/home/user/.SSH_AGENT_vault-cloud"
# (autossh systemd 单元里用 Environment= 或 .ssh/config 前置; 见 salt autossh.sls 待接线点)。
# =====================================================================
set -eu

SSH_VAULT_VM="vault-cloud"
RELAY_USER="${RELAY_USER:-user}"
SSH_SOCK="/home/$RELAY_USER/.SSH_AGENT_$SSH_VAULT_VM"

rm -f "$SSH_SOCK"
sudo -u "$RELAY_USER" /bin/sh -c \
    "umask 177 && exec socat 'UNIX-LISTEN:$SSH_SOCK,fork' 'EXEC:qrexec-client-vm $SSH_VAULT_VM qubes.SshAgent'" &
