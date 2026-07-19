# salt/qubes-air/remotevm/dom0.sls
# =====================================================================
# 在 dom0 上用 Salt 完成 RemoteVM 的创建与属性设置 (等价于 create-remotevm.sh 的 salt 版)。
# 对每个 pillar remotevm:targets 里的条目建一个 RemoteVM。
#
# 【只在 dom0 应用】: salt-call --local state.apply qubes-air.remotevm.dom0
# 注意: RemoteVM 无 template/netvm, start/suspend/shutdown 均 raise -> 绝不 qvm-start。
#
# 用 qubes.* salt 模块 (Qubes 提供的 dom0 salt modules): qvm.present / qvm.prefs / qvm.tags。
# 若某些 salt 版本不支持在 qvm.present 里设 RemoteVM 专有属性, 退化为 cmd.run qvm-prefs
# (见每个属性下的兜底注释)。
# =====================================================================

{% set relay = salt['pillar.get']('remotevm:relay', 'sys-relay-pve') %}
{% set transport = salt['pillar.get']('remotevm:transport_rpc', 'qubesair.SSHProxy') %}

# ---- policy: 部署单一来源 policy 文件到 dom0 ----
/etc/qubes/policy.d/30-qubes-air.policy:
  file.managed:
    - source: salt://qubes-air/remotevm/files/30-qubes-air.policy
    - user: root
    - group: root
    - mode: '0644'

# ---- 给 Relay 打 tag=relay (供 policy @tag:relay) ----
relay-tag-{{ relay }}:
  cmd.run:
    - name: qvm-tags {{ relay }} add relay
    - unless: qvm-tags {{ relay }} list | grep -qx relay

{% for t in salt['pillar.get']('remotevm:targets', []) %}
# ---- 创建 RemoteVM: {{ t.local_name }} ----
remotevm-create-{{ t.local_name }}:
  cmd.run:
    - name: >
        qvm-create --class RemoteVM --label {{ t.get('label', 'gray') }}
        --property relayvm={{ relay }}
        --property transport_rpc={{ transport }}
        --property remote_name={{ t.remote_name }}
        {{ t.local_name }}
    - unless: qvm-check --quiet {{ t.local_name }}
    # 兜底: 若 --property 建时不认这些属性, 去掉上面三行 --property, 靠下面 prefs。

remotevm-prefs-relayvm-{{ t.local_name }}:
  cmd.run:
    - name: qvm-prefs {{ t.local_name }} relayvm {{ relay }}
    - require:
      - cmd: remotevm-create-{{ t.local_name }}

remotevm-prefs-transport-{{ t.local_name }}:
  cmd.run:
    - name: qvm-prefs {{ t.local_name }} transport_rpc {{ transport }}
    - require:
      - cmd: remotevm-create-{{ t.local_name }}

remotevm-prefs-remotename-{{ t.local_name }}:
  cmd.run:
    - name: qvm-prefs {{ t.local_name }} remote_name {{ t.remote_name }}
    - require:
      - cmd: remotevm-create-{{ t.local_name }}

remotevm-tag-{{ t.local_name }}:
  cmd.run:
    - name: qvm-tags {{ t.local_name }} add remote-zone
    - unless: qvm-tags {{ t.local_name }} list | grep -qx remote-zone
    - require:
      - cmd: remotevm-create-{{ t.local_name }}
{% endfor %}

# =====================================================================
# 待真机确认:
#   [A4] Qubes 官方 salt 是否有原生 qvm.present 支持 --class RemoteVM 与这些属性;
#        若有, 优先用 qvm.present/qvm.prefs 替换 cmd.run (更幂等)。本 state 先用 cmd.run 保稳。
#   [A5] qubesdb-write 的 /remote/<name> 映射由 Relay 侧 relay.sls 的 rc.local 在 Relay 开机时
#        重建 (pillar remotevm:targets 是唯一来源), dom0 侧不必重复。
# =====================================================================
