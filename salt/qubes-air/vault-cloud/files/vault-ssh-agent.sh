#!/bin/sh
# vault-cloud 内 ssh-agent 启动 (split-ssh 私钥持有方) —— 由 /rw/config/rc.local 调用。
# =====================================================================
# 在 vault-cloud 开机时:
#   1. 启动一个 ssh-agent, 其 socket 路径固定 (供 qubes.SshAgent 服务 socat 连接)。
#   2. ssh-add 加载 relay transport 私钥 (私钥文件在持久卷 ~/.ssh, 永不出 vault)。
#
# 固定 socket 路径写入 /home/user/.ssh-agent-sock; qubes.SshAgent 从此读 $SSH_AUTH_SOCK。
# =====================================================================
set -eu

VAULT_USER="${VAULT_USER:-user}"
SSH_DIR="/home/$VAULT_USER/.ssh"
AGENT_ENV="/home/$VAULT_USER/.ssh-agent-env"
SOCK="/home/$VAULT_USER/.ssh-agent-sock"

# 若 agent 已在跑 (socket 存在且可用) 则不重复起 (幂等)。
if [ -S "$SOCK" ] && SSH_AUTH_SOCK="$SOCK" ssh-add -l >/dev/null 2>&1; then
    exit 0
fi

rm -f "$SOCK"
# -a 指定固定 socket 路径, 便于 qubes.SshAgent 服务连接。
sudo -u "$VAULT_USER" /bin/sh -c "umask 177 && ssh-agent -a '$SOCK' > '$AGENT_ENV'"

# 加载 relay transport 私钥 (以及其它需要的私钥)。私钥留在 vault, 只被 agent 读。
for key in "$SSH_DIR"/id_ed25519 "$SSH_DIR"/id_rsa "$SSH_DIR"/relay_transport; do
    [ -f "$key" ] || continue
    sudo -u "$VAULT_USER" env SSH_AUTH_SOCK="$SOCK" ssh-add "$key" >/dev/null 2>&1 || true
done
