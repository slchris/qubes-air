# salt/qubes-air/remotevm/relay.sls
# =====================================================================
# 【传输架构迁移 · 重要】
#   目标传输已改为 **gRPC 双向流**（relay 出站建连、长连接双向承载调用与回程、零入站）。
#   本 state 配置的是**现有 SSHProxy 骨架**（autossh + ssh -R），作为过渡参考保留，未真机验证。
#   [TODO] 新增 grpc-relay.sls 部署 gRPC client 单元 + mTLS 证书（替代 autossh/ssh -R）；
#          gRPC 落地见 docs/roadmap-to-production.md 阶段 T。此 state 届时标 DEPRECATED。
# =====================================================================
# 配置本地 Relay (sys-relay-<zone>) —— RemoteVM 架构里的 Local-Relay。
#
# 这是一个【本地普通 AppVM】:
#   - netvm = sys-firewall (能出站到 Remote-Relay), 但【不】ProvidesNetwork、【不】开 ip_forward。
#   - 承载 RemoteVM 的 transport 服务 qubesair.SSHProxy;
#   - 用 autossh 维持出站持久 SSH; 用 ssh -R 反向端口 + 回环 sshd 承接反向调用;
#   - 打 tag `relay` (供 dom0 policy 的 B/C 段用 @tag:relay)。
#
# 【持久化 (评审重点)】: AppVM 的根卷 (/, /usr) 每次重启重置, 只有 /rw 与 /home 持久。
#   因此:
#     - 需要持久的【配置/密钥/脚本】一律放 /rw/config/qubesair/ 或 ~/.ssh/ (都在持久卷)。
#     - 需要持久的【软件包】(autossh 等) 必须装进【模板】, 不能只在 AppVM 里 dnf install
#       (根卷重置即丢)。本 state 里 pkg.installed 只有在【对模板应用】时才真正持久;
#       若误对 AppVM 应用, 仅当次生效。见文末 [P1]。
#     - transport 服务脚本要在 /etc/qubes-rpc/ 生效, 但 /etc 属根卷 —— 用 bind-dirs 把
#       /etc/qubes-rpc/qubesair.SSHProxy 持久化 (或写进模板)。本 state 用 bind-dirs。
# =====================================================================

{% set relay_user = salt['pillar.get']('remotevm:relay_user', 'user') %}
{% set qubesair_dir = '/rw/config/qubesair' %}

# ---------------------------------------------------------------------
# 0. (仅对模板生效) 安装依赖包
#    实机: 对 Relay 用的模板 (如 fedora-42) 应用一次此段, 装进模板才持久。
# ---------------------------------------------------------------------
relay-packages:
  pkg.installed:
    - pkgs:
      - autossh
      - openssh-server
      - openssh-clients

# ---------------------------------------------------------------------
# 1. 持久目录 (都在 /rw, 天然持久)
# ---------------------------------------------------------------------
{{ qubesair_dir }}:
  file.directory:
    - user: root
    - group: root
    - mode: '0755'
    - makedirs: True

{{ qubesair_dir }}/cm:
  file.directory:
    - user: {{ relay_user }}
    - mode: '0700'
    - makedirs: True

# ---------------------------------------------------------------------
# 2. bind-dirs: 把 transport 服务持久化到 /etc/qubes-rpc/
#    (/etc 属根卷易失; bind-dirs 从 /rw/bind-dirs 绑定回来, 重启不丢)
# ---------------------------------------------------------------------
/rw/config/qubes-bind-dirs.d/50_qubesair.conf:
  file.managed:
    - makedirs: True
    - mode: '0644'
    - contents: |
        # Qubes Air: 持久化 transport 服务与回环 sshd 配置到根卷路径
        binds+=( '/etc/qubes-rpc/qubesair.SSHProxy' )

# transport 脚本的真身放持久卷, 由 bind-dirs 绑定到 /etc/qubes-rpc/
/rw/bind-dirs/etc/qubes-rpc/qubesair.SSHProxy:
  file.managed:
    - source: salt://qubes-air/remotevm/files/qubesair.SSHProxy
    - user: root
    - group: root
    - mode: '0755'
    - makedirs: True

# ---------------------------------------------------------------------
# 3. SSH client config (持久卷 ~/.ssh, 由 mgmt-air 渲染后投递; 这里保证目录/权限)
# ---------------------------------------------------------------------
/home/{{ relay_user }}/.ssh:
  file.directory:
    - user: {{ relay_user }}
    - mode: '0700'
    - makedirs: True

/home/{{ relay_user }}/.ssh/known_hosts.d:
  file.directory:
    - user: {{ relay_user }}
    - mode: '0700'
    - makedirs: True
    - require:
      - file: /home/{{ relay_user }}/.ssh

# ---------------------------------------------------------------------
# 4. 回环 sshd (反向调用入口) + 授权 key + 强制命令处理器
# ---------------------------------------------------------------------
{{ qubesair_dir }}/relay-loopback-sshd.conf:
  file.managed:
    - source: salt://qubes-air/remotevm/files/relay-loopback-sshd.conf
    - user: root
    - group: root
    - mode: '0644'
    - require:
      - file: {{ qubesair_dir }}

{{ qubesair_dir }}/reverse-qrexec-handler:
  file.managed:
    - source: salt://qubes-air/remotevm/files/reverse-qrexec-handler
    - user: root
    - group: root
    - mode: '0755'
    - require:
      - file: {{ qubesair_dir }}

# ---------------------------------------------------------------------
# 5. remote_map 启动脚本: 开机时把 QubesDB /remote/<name> 映射重建
#    (QubesDB 不持久, Relay 每次启动都要重写)。映射源存 /rw 持久卷。
# ---------------------------------------------------------------------
{{ qubesair_dir }}/remote_map:
  file.managed:
    - user: root
    - group: root
    - mode: '0644'
    - contents: |
        # 每行: <本地RemoteVM名> <远端原始名>
        # 由 dom0 create-remotevm.sh 及 mgmt-air 编排写入; 开机由 rc.local 重放到 QubesDB。
        {%- for t in salt['pillar.get']('remotevm:targets', []) %}
        {{ t.local_name }} {{ t.remote_name }}
        {%- endfor %}

# rc.local 在 AppVM 开机时跑 (Qubes 官方: /rw/config/rc.local 持久且开机执行)
/rw/config/rc.local:
  file.managed:
    - user: root
    - group: root
    - mode: '0755'
    - contents: |
        #!/bin/sh
        # Qubes Air Relay 开机初始化
        # 1) 重建 QubesDB /remote/<name> 映射 (QubesDB 不持久)
        while read -r local_name remote_name; do
            [ -z "$local_name" ] && continue
            case "$local_name" in \#*) continue ;; esac
            qubesdb-write "/remote/$local_name" "$remote_name"
        done < {{ qubesair_dir }}/remote_map
        # 2) 启动回环 sshd (反向调用入口)
        /usr/sbin/sshd -f {{ qubesair_dir }}/relay-loopback-sshd.conf
        # 3) autossh 出站隧道由 systemd 单元管理 (见步骤 6), 此处不重复起。

# =====================================================================
# 待真机确认:
#   [P1] pkg.installed 只在【对模板应用】时持久; 部署流程必须先对模板 state.apply relay
#        (装 autossh 等), 再对 AppVM 应用剩余配置。见 top.sls 分层。
#   [P2] /rw/config/rc.local 在 Qubes AppVM 开机确会执行 (官方机制); 确认其以 root 运行。
#   [P3] bind-dirs 首次需重启 AppVM 或 `sudo /usr/lib/qubes/init/bind-dirs.sh` 才生效。
#   [P4] 回环 sshd 用系统 sshd 二进制路径 (/usr/sbin/sshd) 是否随模板不同而变。
# =====================================================================
