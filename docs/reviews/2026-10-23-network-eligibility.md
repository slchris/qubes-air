# 2026-10-23 节点网络资格检查

阶段基线：Air PR #90 已合入 `7a5fc9a`，Salt PR #3 已合入 `cc8c11c`，两个合并提交的
CI 均通过。本阶段修复已在现场复现的“按容量选中缺少 zone bridge 的节点”问题。

## 行为与验收条件

- 自动调度只从满足 zone 网络要求的在线节点中比较内存；每个排除项记录原因。
- 显式节点、默认节点回退、重试及 resume 均需在 provider 创建、克隆或修改资源前重新检查。
- 检查仅使用已经验证 TLS/CA 的 Proxmox 只读 API；不自动修改 bridge、VLAN 或宿主网络。
- 缺 bridge、未激活、VLAN 不满足、待应用的网络配置、读取失败或输入非法时失败关闭。
- 网络资格是节点配置的检查，不证明 DHCP、上游交换机、路由或 agent 的端到端连通性；
  实际连通性继续由 provision 的 agent 探针和真机生命周期验收确认。
- 回归先证明不合格节点被选中，以及显式节点/恢复路径在拒绝前已产生 provider 写入；
  修复后这些路径应拒绝且无分配、checkpoint 或 provider 写入。

## 状态

先在旧代码上复现四项失败：缺 bridge 的空闲节点被选中、无合格节点仍返回成功、显式节点
创建 storage、resume 创建 compute。修复后相同测试通过；新增 bridge 类型/active、VLAN
范围和缺省拒绝、待应用配置、畸形/超量响应、取消/超时、非法节点/bridge，以及不产生
provider 写入、VMID 分配或 checkpoint 的测试。

节点网络验证由 `internal/proxmoxnetwork` 共用，自动调度和 provider 的每次 ensure 均调用。
Proxmox API 的待应用配置在顶层 `changes` 中报告，不能只解包 `data`：参见
[上游网络 API](https://github.com/proxmox/pve-manager/blob/master/PVE/API2/Network.pm)。
本阶段要求 tagged Linux bridge 显式开启 VLAN-aware 并声明允许的 `bridge_vids`；
不会自动启用宿主网络配置，也不宣称支持未验收的传统动态 VLAN bridge 或 SDN vnet。

四个受影响 Go 包的 race 测试和改动文件复杂度检查通过。首次完整门禁因系统 Node 22.14
不满足现有前端依赖要求而失败；补齐 Node 22.22.2 后，完整审计又发现新测试重复定义 PEM
类型字符串，改为复用现有编码函数。最终以 Go 1.26.10、Node 22.22.2 重跑
`make pre-commit` 与 `make audit` 均通过，未跳过失败项：Go race 总覆盖率 74.7%，
双平台 lint/security、入口 mTLS 冒烟、前端检查/构建/测试、Shell/YAML/文档检查均通过。

真实集群的只读探针使用本分支同一校验和调度代码，经现有 Console 凭据与严格 TLS 验证：
六个节点在线，只有一个具有 zone 要求的 active bridge/VLAN，自动选择该节点，其余五个
因缺 bridge 排除。改为不存在的测试 bridge 时全部拒绝。探针只认证并 GET，资源变更为 0，
结束时删除临时程序；凭据未传回工作站。该结果不代替部署新 Console 后的生命周期验收。

本阶段由 PR #91 合入 `d7a9dbd`，合并提交的 CI 全部通过。部署 Console `06cfff3` 后，
新专用对象 `qa-cleanup-20261023` 自动选中了唯一合格节点，无需手动指定节点。
首次 bootstrap 因 Console 重启丢失入站规则失败；保留失败记录，并通过
[持久入站修复](2026-10-23-console-ingress.md)后完成 resume、完整 qrexec 正反例和
1 MiB 原文件跨 suspend/resume 验证。2026-10-24 在 Console `2b62128` 上最终 purge 成功，
资源残留和重复请求核验通过，详见[自动清理验收](2026-10-23-purge-residual-cleanup.md)。
这些结果证明本次真实网络条件下的选择与生命周期，不扩大为所有 VLAN/上游网络的保证。
