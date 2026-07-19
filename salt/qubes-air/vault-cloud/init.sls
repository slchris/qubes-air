# salt/qubes-air/vault-cloud/init.sls
# =====================================================================
# 配置 vault-cloud (凭据保险库 qube) —— 阶段3。
#
# 这是一个【无网络 AppVM】(netvm='' 由 dom0 create-vault-cloud.sh 设定):
#   - 承载 qrexec 服务 qubesair.GetCredential (按名下发云 API 凭据 / age 私钥);
#   - 承载 split-ssh 服务 qubes.SshAgent (持有 relay transport SSH 私钥, 只出签名);
#   - 打 tag=vault-cloud (dom0 policy 用 @tag:vault-cloud 定位)。
#
# 【持久化 (同 relay.sls 的约束)】
#   - AppVM 根卷 (/, /etc, /usr) 每次重启重置, 只有 /rw 与 /home 持久。
#   - qrexec 服务脚本要落在 /etc/qubes-rpc/ (根卷) -> 用 bind-dirs 从 /rw 绑回, 持久。
#   - socat 等软件包必须装进【模板】才持久 (对 AppVM dnf install 只当次生效)。
#   - 凭据文件 / SSH 私钥放 /home/user (持久卷), 权限 600/700。
#
# 应用:
#   # 1) 先对 vault-cloud 用的模板应用 (装 socat):  仅需一次
#   #    qubesctl --skip-dom0 --targets <template> state.apply qubes-air.vault-cloud
#   # 2) 再对 vault-cloud AppVM 应用 (部署服务/目录):
#   #    qubesctl --skip-dom0 --targets vault-cloud state.apply qubes-air.vault-cloud
# =====================================================================

{% set vault_user = salt['pillar.get']('vault_cloud:user', 'user') %}
{% set cred_dir = '/home/' ~ vault_user ~ '/.qubes-air/credentials' %}

# ---------------------------------------------------------------------
# 0. (仅对模板生效) 安装依赖包: socat (split-ssh + GetCredential 桥接需要)
# ---------------------------------------------------------------------
vault-cloud-packages:
  pkg.installed:
    - pkgs:
      - socat
      - openssh-clients

# ---------------------------------------------------------------------
# 1. 凭据目录 (持久卷 /home, 权限 700)
# ---------------------------------------------------------------------
{{ cred_dir }}:
  file.directory:
    - user: {{ vault_user }}
    - group: {{ vault_user }}
    - mode: '0700'
    - makedirs: True

/home/{{ vault_user }}/.ssh:
  file.directory:
    - user: {{ vault_user }}
    - group: {{ vault_user }}
    - mode: '0700'
    - makedirs: True

# ---------------------------------------------------------------------
# 2. bind-dirs: 把两个 qrexec 服务持久化到 /etc/qubes-rpc/
#    (/etc 属根卷易失; bind-dirs 从 /rw/bind-dirs 绑回, 重启不丢)
# ---------------------------------------------------------------------
/rw/config/qubes-bind-dirs.d/50_qubesair_vault.conf:
  file.managed:
    - makedirs: True
    - mode: '0644'
    - contents: |
        # Qubes Air vault-cloud: 持久化 qrexec 服务到根卷路径
        binds+=( '/etc/qubes-rpc/qubesair.GetCredential' )
        binds+=( '/etc/qubes-rpc/qubes.SshAgent' )

/rw/bind-dirs/etc/qubes-rpc/qubesair.GetCredential:
  file.managed:
    - source: salt://qubes-air/vault-cloud/files/qubesair.GetCredential
    - user: root
    - group: root
    - mode: '0755'
    - makedirs: True

/rw/bind-dirs/etc/qubes-rpc/qubes.SshAgent:
  file.managed:
    - source: salt://qubes-air/vault-cloud/files/qubes.SshAgent
    - user: root
    - group: root
    - mode: '0755'
    - makedirs: True

# ---------------------------------------------------------------------
# 3. ssh-agent 启动脚本 (split-ssh 私钥持有方) + rc.local 接线
# ---------------------------------------------------------------------
/rw/config/qubesair/vault-ssh-agent.sh:
  file.managed:
    - source: salt://qubes-air/vault-cloud/files/vault-ssh-agent.sh
    - user: root
    - group: root
    - mode: '0755'
    - makedirs: True

# rc.local: 开机启动 ssh-agent 并加载私钥 (仅对本无网络 vault)。
/rw/config/rc.local:
  file.managed:
    - user: root
    - group: root
    - mode: '0755'
    - contents: |
        #!/bin/sh
        # Qubes Air vault-cloud 开机初始化
        # 启动 split-ssh 私钥持有 agent (私钥留 vault, 只出签名)
        VAULT_USER={{ vault_user }} /rw/config/qubesair/vault-ssh-agent.sh || true

# =====================================================================
# 待真机确认:
#   [V4] pkg.installed 仅在【对模板应用】时持久; 部署流程必须先对模板 apply 装 socat, 再对
#        AppVM apply 部署服务/目录 (同 relay.sls [P1])。
#   [V5] bind-dirs 首次需重启 AppVM 或 `sudo /usr/lib/qubes/init/bind-dirs.sh` 才生效 (同 [P3])。
#   [V6] /rw/config/rc.local 以 root 执行 (官方机制); vault-ssh-agent.sh 内用 sudo -u 降权起 agent。
#   [V7] relay 侧 split-ssh 客户端桥接 (relay-split-ssh-client.sh) 需追加到 sys-relay 的
#        /rw/config/rc.local, 并让 autossh 的 SSH_AUTH_SOCK 指向 ~/.SSH_AGENT_vault-cloud。
#        这条接线属阶段2 autossh 文件范畴, 本阶段仅提供脚本, 不改阶段2 文件 (见 runbook)。
# =====================================================================
