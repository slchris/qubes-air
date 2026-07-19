# salt/qubes-air/remotevm/top.sls
# =====================================================================
# RemoteVM 链路 (阶段2) 的 top 文件。分层应用, 因 dom0 / 模板 / Relay-AppVM 目标不同。
#
# 应用顺序 (实机 runbook 会逐条执行):
#   1. dom0:            qubesctl --show-output state.sls qubes-air.remotevm.dom0
#                       (建 RemoteVM + 部署 policy + 打 tag)
#   2. Relay 模板:      qubesctl --skip-dom0 --targets <relay-template> \
#                          state.sls qubes-air.remotevm.relay   (装 autossh 等包进模板)
#   3. Relay AppVM:     qubesctl --skip-dom0 --targets sys-relay-<zone> \
#                          state.sls qubes-air.remotevm.relay,qubes-air.remotevm.autossh
#
# 用独立 top 而非塞进 salt/qubes-air/top.sls 的通配 (评审否决 glob 匹配); 显式分层更安全。
# =====================================================================

base:
  # dom0 侧: RemoteVM 元数据 + policy (单一来源)
  'dom0':
    - match: nodename
    - qubes-air.remotevm.dom0

  # Relay AppVM: transport + 反向 sshd + autossh
  'sys-relay-*':
    - qubes-air.remotevm.relay
    - qubes-air.remotevm.autossh
