# 文档索引

文档只描述当前实现和接下来的开发工作。状态以代码、测试和真机验收为准；设计文档中的设想
不能覆盖当前实现。

## 操作入口

| 文档 | 何时阅读 |
|---|---|
| [快速入门](quickstart.md) | 第一次本地运行或准备真机部署 |
| [本地开发](local-dev.md) | 用 Docker Compose 开发控制台 |
| [RemoteVM runbook](runbook-remotevm.md) | 真机创建、验收和排错 |
| [RemoteVM 自检](remotevm-selfcheck.md) | 逐层确认 dom0、Relay、agent 与服务 |

## 当前设计

| 文档 | 内容 |
|---|---|
| [架构](architecture.md) | 组件、数据流和信任边界 |
| [Bootstrap](bootstrap-design.md) | agent 投递、CSR、证书续期和 provider 差异 |
| [gRPC transport](grpc-transport-design.md) | RemoteVM 改写、Relay、mTLS 和服务契约 |
| [Remote agent](remote-agent-design.md) | 普通 Linux 远端怎样提供受限 qrexec 语义 |
| [RemoteVM 对齐](remotevm-alignment.md) | 本项目如何使用 Qubes OS R4.3 RemoteVM |
| [路线图](roadmap-to-production.md) | 已完成能力和剩余工作 |

## 运维专题

| 文档 | 内容 |
|---|---|
| [凭据与轮换](credential-vault.md) | vault、控制台密钥、Relay/agent 证书 |
| [凭据销毁](credential-destruction.md) | Zone、VM 和整机事件处理 |
| [OpenTofu state](terraform-state.md) | 多机共享与客户端加密 |

Qubes Salt 配置位于
[qubes-salt-config](https://github.com/slchris/qubes-salt-config)，不在本仓库维护第二份。
