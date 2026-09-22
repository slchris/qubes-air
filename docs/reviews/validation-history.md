# 已有真机验收记录汇总

整理日期：2026-09-20。内容来自整理前的路线图、provider 与 transport 文档，本次没有重跑真机。
除 transport 文档原标记为 2026-07 外，其他条目没有足够证据确定准确现场日期；不补造日期或版本。

| 已有记录 | 当时记录的结果 | 不能据此推断 |
|---|---|---|
| Qubes R4.3 + Proxmox transport | RemoteVM 改写、独立 Relay、端点同步、Ping/Exec/FileCopy/ConnectTCP、mTLS、续期与重连 | 当前全部 transport 改动都已按同一版本验收 |
| 原生 Proxmox provider 适配器 | storage/compute 创建、suspend、resume、purge | 单独适配器测试覆盖完整 agent bootstrap |
| Console 到 agent 的现场链路 | provision、CSR、healthy、suspend/resume；节点管理地址和 job log 问题修复 | 当前所有失败路径均自动回归通过 |
| 后续生命周期记录 | provision 可达性门通过、release/purge，provider VM 清空 | 数据密钥的所有备份副本均已销毁 |
| 后续结构化传输记录 | Exec exit 0 与 exit 7，stdout/stderr 分离；FileCopy push/pull | 整个桌面、断线、重启及所有服务均已验收 |

静态 IP 池、无缝桌面全流程、离机恢复、GCP/AWS 不在这些通过记录内。

下次现场验收使用 [runbook](../runbook-remotevm.md) 与[自检清单](../remotevm-selfcheck.md)，
至少记录已提交源码版本、构建 digest、环境版本、步骤结果、失败/重试与销毁证据。
精确现场地址和凭据保留在受控环境记录中，不提交到本仓库。
