# 文档索引

文档只描述当前实现和接下来的开发工作。状态以代码、测试和真机验收为准；设计文档中的设想
不能覆盖当前实现。

## 进度与工作清单

- [当前状态](roadmap-to-production.md)：已落地能力与未通过的验收边界。
- [后续 TODO](TODO.md)：按优先级维护任务、依赖和完成标准。
- [历史检查与验收](reviews/README.md)：当时的证据与限制，不作为部署入口。

## 操作入口

| 文档 | 何时阅读 |
|---|---|
| [快速入门](quickstart.md) | 第一次本地运行或准备真机部署 |
| [本地开发](local-dev.md) | 用 Docker Compose 开发控制台 |
| [RemoteVM runbook](runbook-remotevm.md) | 真机创建、验收和排错 |
| [Proxmox 回归 runbook](runbook-qa01.md) | 把源码/构建/环境绑定后跑 QA-01 并留记录 |
| [RemoteVM 自检](remotevm-selfcheck.md) | 逐层确认 dom0、Relay、agent 与服务 |

## 当前设计

| 文档 | 内容 |
|---|---|
| [架构](architecture.md) | 组件、数据流和信任边界 |
| [Bootstrap](bootstrap-design.md) | agent 投递、CSR、证书续期和 provider 差异 |
| [gRPC transport](grpc-transport-design.md) | RemoteVM 改写、Relay、mTLS 和服务契约 |
| [Remote agent](remote-agent-design.md) | 普通 Linux 远端怎样提供受限 qrexec 语义 |
| [MCP 接入](mcp-design.md) | MCP server 如何以非特权路径接入 Console API |
| [Provider 原生编排](provider-design.md) | 当前执行器、资源身份记录、对账行为与已知限制 |
| [RemoteVM 对齐](remotevm-alignment.md) | 本项目如何使用 Qubes OS R4.3 RemoteVM |

## 运维专题

| 文档 | 内容 |
|---|---|
| [生产部署安全要求](deployment-requirements.md) | TLS/loopback、生产模式、密钥保管、审计留存、snippet 与 bootstrap 窗口的硬要求与核对方式 |
| [P0 安全控制](security-controls.md) | 签名撤销源、Exec/FileCopy 请求边界与 provider 身份配置 |
| [凭据与轮换](credential-vault.md) | vault、控制台密钥、Relay/agent 证书 |
| [凭据销毁](credential-destruction.md) | Zone、VM 和整机事件处理 |
| [灾难恢复](disaster-recovery.md) | 故障域、加密备份/恢复、schema 版本与 CA 重建 |
| [运行期默认值与数据库结构](runtime-defaults.md) | 限流/超时/退避等默认值与 SQLite 表清单，逐条给 `文件:行号` |

Qubes Salt 配置位于
[qubes-salt-config](https://github.com/slchris/qubes-salt-config)，不在本仓库维护第二份。

## 包与工具说明

- [Agent Debian 包](../packaging/agent-deb/README.md)：安装内容、首次 bootstrap、诊断与构建。
- [离线加密工具](../crypto/README.md)：SOPS/age 工具的适用范围。

## 文档维护

专题文档描述当前行为；任务状态只在 TODO 更新，历史证据放 reviews 并注明版本和限制。
删除的架构与部署步骤不另建可执行的历史入口；需要追溯时查询 Git。文档移动后更新引用，
运行 `make docs-check`。个人笔记和 memory 不作为项目状态、验收或部署依据。

生命周期部分失败、重启及显式重试见[可靠性契约](reliability-design.md)。
