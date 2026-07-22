# Proxmox 网络暴露

现行 RemoteVM 数据面是 Relay 到 agent 的 mTLS gRPC；GUI/TCP 也复用该通道，不需要直接开放
VNC、RDP、Xpra 或应用端口。

“零入站”指不为每个远端服务额外暴露公网/LAN 端口，并不意味着当前 Proxmox bootstrap 在
所有拓扑下完全没有入站连接。Console 需要主动连接 guest agent 的 mTLS endpoint，具体网络
规则取决于 console 与 guest 是否在同一受控网络。

最低原则：

- agent 端口只允许 console/Relay 所在受控来源；
- 禁止公网开放 agent、VNC、RDP、Xpra 和临时调试端口；
- 管理 SSH 只用于明确的节点/snippet 运维，按来源限制并使用独立最小权限身份；
- guest 默认拒绝其他入站；
- provider 网络规则必须与实际 bootstrap/health/Relay 拨号方向一致；
- 用外部扫描和 guest 上的 `ss -lntp` 同时验收。

本仓库尚未提供完整的 provider firewall resource。把这项作为部署前置条件处理，不要因为
Terraform 中没有规则就假设底层网络默认安全。
