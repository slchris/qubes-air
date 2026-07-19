# salt/qubes-air/remotevm/autossh.sls
# =====================================================================
# 【传输架构迁移 · 重要】
#   目标传输是 gRPC 双向流；出站保活/重连将由 gRPC 客户端承担（替代 autossh）。
#   [TODO] gRPC 落地后本 state 标 DEPRECATED，见 docs/roadmap-to-production.md 阶段 T。
#   以下为现有 SSHProxy 骨架的 autossh 出站保活，作为过渡参考保留。
# =====================================================================
# 在 Relay 上安装并启用 autossh 出站持久隧道的 systemd 单元 (每个远端一个实例)。
# 依赖 relay.sls 已备好 ~/.ssh/config (含 RemoteForward) 与 autossh 包。
#
# 单元与依赖必须持久 (systemd 单元文件写 /etc/systemd/system 属根卷 -> 用 bind-dirs 或
# 每次开机由 rc.local 落地)。这里把单元也走 bind-dirs 持久化。
# =====================================================================

include:
  - qubes-air.remotevm.relay

# systemd 模板单元 (实例名 = 远端原始名), 经 bind-dirs 持久到 /etc/systemd/system
/rw/config/qubes-bind-dirs.d/51_qubesair_autossh.conf:
  file.managed:
    - makedirs: True
    - mode: '0644'
    - contents: |
        binds+=( '/etc/systemd/system/autossh-qubesair@.service' )

/rw/bind-dirs/etc/systemd/system/autossh-qubesair@.service:
  file.managed:
    - source: salt://qubes-air/remotevm/files/autossh-tunnel.service
    - user: root
    - group: root
    - mode: '0644'
    - makedirs: True

# 为每个 pillar 里声明的 target 启用一个 autossh 实例
{% for t in salt['pillar.get']('remotevm:targets', []) %}
autossh-qubesair@{{ t.remote_name }}:
  service.enabled:
    - name: autossh-qubesair@{{ t.remote_name }}.service
{% endfor %}

# =====================================================================
# 待真机确认:
#   [P5] bind-dirs 对 /etc/systemd/system 的单元需 `systemctl daemon-reload` + enable;
#        首次可能需重启 AppVM 让 bind-dirs 挂载生效后再 enable。
#   [P6] service.enabled 在 AppVM 里对模板单元实例 (@) 的行为; 若 salt 不认实例名,
#        改为 rc.local 里 `systemctl start autossh-qubesair@<name>`。
# =====================================================================
