# 2026-09-25 QA-01 Proxmox 部分真机回归

本次通过当前已部署 Console 的 `infra-qa` Zone 创建了一个专用加密数据盘 QA Qube，完成了可逆生命周期
路径与 provider 资源核对。没有更新 Console/agent 部署，没有扩大 Exec/FileCopy allowlist，也没有 purge。
Console health 报告版本 `0.1.0`；本轮未取得正在运行二进制的源码 revision 与 digest，因此结果不绑定当前 checkout，
不构成完整 QA-01 sign-off。

## 结果

| 步骤 | 结果 |
|---|---|
| Provision | job succeeded，最终状态 running，agent healthy；早期启动阶段的 agent 证书探测曾暂时失败，等待 bootstrap 后健康检查成功 |
| Suspend | job succeeded；provider 侧 compute 被移除，storage holder 保留且所有权标记匹配 |
| Resume | job succeeded；原 storage holder 和数据卷身份未变，重建的 compute 重新挂接该数据卷，最终 agent healthy |
| Release | job succeeded；provider 侧 compute 不存在，storage holder 仍保留且所有权标记匹配 |
| Purge | 未执行；会销毁剩余数据卷，等待对该专用 QA 目标的显式确认 |

地址在 resume 后变化，符合 DHCP 语义。Console 状态和 PVE provider 资源都已交叉核对；本记录不保存真实地址、VMID、
Qube ID、凭据或密钥材料。

## 未覆盖与阻塞

- **数据内容持久性**：确认的是相同 provider 数据卷身份、resume 后重新挂接和 agent healthy；没有通过测试文件读回证明内容。
- **Exec/FileCopy**：当前 checkout 的权威部署配置中相关服务和路径 allowlist 为空。为真机正值测试扩大整个 Console 的
  guest 权限范围没有得到单独授权，因此未修改部署配置，也未执行正值/负值调用。
- **旧盘迁移**：没有独立的 legacy-format 盘样本，本轮未尝试迁移或更换 keyslot。
- **SSH known_hosts**：未从独立带外来源核对节点指纹。
- **版本绑定**：当前部署版本只有 `0.1.0` 标识，未能将其对应到源码 revision 与构建 digest。

下一步先处理离机归档和 Console allowlist 配置条件；测试资源目前处于 released 状态，只保留专用数据 holder。
purge 前必须核对并确认精确目标。
