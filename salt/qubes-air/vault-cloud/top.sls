# salt/qubes-air/vault-cloud/top.sls
# =====================================================================
# vault-cloud (凭据保险库, 阶段3) 的 top 文件。分层应用, 因模板 / AppVM 目标不同。
#
# 应用顺序 (实机 runbook 会逐条执行):
#   1. vault 模板:   qubesctl --skip-dom0 --targets <vault-template> \
#                       state.sls qubes-air.vault-cloud   (装 socat 进模板才持久)
#   2. vault AppVM:  qubesctl --skip-dom0 --targets vault-cloud \
#                       state.sls qubes-air.vault-cloud   (部署服务/目录/agent)
#
# 注意: vault-cloud 的创建 (netvm='' + tag) 由 dom0 create-vault-cloud.sh 完成,
#       不放在本 top (与 remotevm.dom0 不同, 无需 dom0 salt 建 qube)。
#       policy 追加在 dom0-scripts/policy.d/30-qubes-air.policy (单一来源), 由
#       remotevm/dom0.sls 的 file.managed 统一部署到 /etc/qubes/policy.d/。
#
# 用独立 top 而非塞进 salt/qubes-air/top.sls 通配 (评审否决 glob); 显式命名目标更安全。
# =====================================================================

base:
  'vault-cloud':
    - qubes-air.vault-cloud
