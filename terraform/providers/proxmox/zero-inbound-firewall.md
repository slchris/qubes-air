# 云侧网络: 零入站防火墙说明 (阶段2, 占位 + TODO)

> 边界声明: 本文件是**文档 + TODO 占位**, 不改动阶段1 已定稿的 `terraform/modules/remote-qube-base/`。
> 若要落地为真实资源, 请在本 `providers/proxmox/` 目录新增独立 `.tf` 文件, 不要改模块。

## 为什么远端不需要任何入站端口

阶段2 采用 **RemoteVM + 零入站 SSH transport** 链路 (见 `docs/runbook-remotevm.md`):

- 连接方向永远是**本地 Relay 主动出站** SSH 到 Remote-Relay (autossh 维持)。
- 正向调用 (本地→远端) 走这条出站连接。
- 反向调用 (远端→本地) 走**同一连接的 `ssh -R` 反向端口转发**, 不需要新建入站连接。

因此远端主机 (家庭 NAT / 云 VM) **不需要开放任何公网入站端口** (连 22 都不必对公网开)。
这大幅收敛攻击面, 也让家庭 NAT 后的树莓派/PVE 无需端口映射即可接入。

## 各 provider 的零入站防火墙策略 (TODO 占位)

### Proxmox (本目录)
- Proxmox VM 防火墙 (`proxmox_virtual_environment_firewall_rules`) 建议:
  - **入站 (IN): 默认 DROP, 无 ACCEPT 规则**。
  - **出站 (OUT): 允许到本地 Relay 的公网出口 IP、端口 22 (或自定义)**;
    但由于连接是 Relay→远端方向, 远端只需允许该 SSH 会话的**回程** (established/related),
    对纯出站方向可保持宽松或按需收紧。
  - 关键: **不要**为了"让本地连进来"而开入站 22 —— 那正是本方案要避免的。
- TODO: 新增 `firewall.tf`, 用 bpg/proxmox 的 firewall resource 实现上述 IN=DROP 策略。

### GCP (../gcp)
- TODO: `google_compute_firewall` —— **不创建任何 INGRESS allow 规则**;
  仅保留默认拒绝入站。EGRESS 允许到 Relay 出口 IP。
- 若 Remote-Relay 在 GCP 且 Relay 在家庭网络, GCP 侧完全零入站规则即可
  (出站 SSH 由 GCP VM 主动发起? 否 —— 本方案是 Relay 主动出站, 故 GCP VM 作为 SSH 服务端时
  仍需入站。见下方"谁是 SSH 服务端"澄清)。

### AWS (../aws)
- TODO: Security Group —— ingress 留空 (零入站) 的前提同上, 视 SSH 服务端位置而定。

## ⚠️ 澄清: "谁是 SSH 服务端"决定入站需求

"零入站"成立的严格条件是: **需要被保护的一侧是 SSH 客户端 (主动出站)**。

- 本方案里**本地 Relay 是 SSH 客户端**, 主动连 Remote-Relay。
- 因此 **Remote-Relay 是 SSH 服务端**, 它在技术上需要一个可达的监听端口。

真正的"零公网入站"实现取决于拓扑:
1. **Remote-Relay 有公网可达地址** (云 VM 场景): 它仍需开入站 SSH 端口给 Relay 连入 →
   这不是严格零入站, 但可用 SG 把入站源 IP 锁死为 Relay 出口 IP (单点白名单)。
2. **Remote-Relay 在 NAT 后** (家庭树莓派场景): 反过来让 **Remote-Relay 主动出站**连到
   一个双方都可达的会合点 (Relay 若有公网), 或用第三方跳板 —— 此时远端才真正零公网入站。

> 结论 / TODO: 阶段2 runbook 默认按 (1) 实现 (远端云 VM 作 SSH 服务端, SG 白名单锁定
> Relay 出口 IP), 并在文档标注"若要严格零入站需拓扑 (2)"。SG/firewall 的真实 terraform
> 资源留作 TODO, 按最终拓扑落地, 避免与阶段1 模块冲突。
