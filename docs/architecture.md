# Qubes Air 架构设计文档

> **[DEPRECATED] 本文档描述的是早期 `sys-remote + WireGuard 网关` 方案，已弃用。**
> 该方案在官方 RemoteVM 落地**之前**设计（评审证明其方向错误：把 relay 当网络网关、违反平面分离）。当前架构改为对齐官方 **RemoteVM**，跨机传输为 **gRPC 双向流**（本地 relay 作为 gRPC 客户端出站建连、长连接双向承载 qrexec 转发与反向回程，零入站；[TODO] gRPC 传输尚未实现，现有骨架为 `qubesair.SSHProxy`）。
> 现行架构与传输层设计见 [readme.md](../readme.md)、[docs/remotevm-alignment.md](remotevm-alignment.md) 与 [docs/roadmap-to-production.md](roadmap-to-production.md)。本文保留作历史参考。

## 概述

Qubes Air 是一个 IaC (Infrastructure as Code) 项目，用于在 Qubes OS 中编排和管理远程计算资源，实现 Qubes Air 的愿景。

## 核心概念

### 1. Qubes Zones

Zone 是一个管理边界，代表一组在同一基础设施上运行的资源：

```
┌─────────────────────────────────────────────────────────────────┐
│                        Master Admin                              │
│                     (Your Qubes Machine)                        │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐   ┌─────────────┐   ┌─────────────┐           │
│  │  sys-remote │   │  sys-remote │   │  sys-remote │           │
│  │  (Proxmox)  │   │   (GCP)     │   │   (AWS)     │           │
│  └──────┬──────┘   └──────┬──────┘   └──────┬──────┘           │
├─────────┼─────────────────┼─────────────────┼───────────────────┤
          │ WireGuard       │ WireGuard       │ WireGuard
          │                 │                 │
┌─────────┴─────────┐ ┌─────┴─────────┐ ┌─────┴─────────┐
│   Proxmox Zone    │ │   GCP Zone    │ │   AWS Zone    │
│   (Private DC)    │ │   (Cloud)     │ │   (Cloud)     │
├───────────────────┤ ├───────────────┤ ├───────────────┤
│  - work-remote    │ │  - gpu-dev    │ │  - ml-train   │
│  - dev-remote     │ │  - disp-cloud │ │  - batch-job  │
│  - disp-remote    │ │               │ │               │
└───────────────────┘ └───────────────┘ └───────────────┘
```

### 2. sys-remote

sys-remote 是一个特殊的 AppVM，作为到远程 Zone 的安全网关：

- 运行 WireGuard VPN 客户端
- 提供 qrexec 服务接口
- 不存储敏感数据
- 每个 Zone 一个 sys-remote

### 3. 通信模型

```
Local Qube ──qrexec──> sys-remote ──WireGuard──> Remote Zone
                           │
                           └── Terraform/Salt 命令通过加密隧道执行
```

## 安全设计

### 信任边界

1. **dom0**: 最高信任，管理所有 Qube
2. **sys-remote**: 有限信任，仅作为网络网关
3. **Remote Zone**: 不信任，假设已被入侵

### 加密层

1. **传输层**: WireGuard (Always-on VPN)
2. **存储层**: LUKS 磁盘加密 + HSM 密钥管理
3. **配置层**: SOPS/age 加密敏感配置

## 组件交互

```
┌──────────────────────────────────────────────────────────┐
│                      Qubes OS (dom0)                      │
│  ┌────────────────┐    ┌────────────────┐                │
│  │ qubes-air CLI  │───▶│  Salt Master   │                │
│  └────────────────┘    └────────────────┘                │
│           │                    │                          │
│           ▼                    ▼                          │
│  ┌────────────────┐    ┌────────────────┐                │
│  │  Management    │    │  sys-remote    │                │
│  │     Qube       │───▶│   (per zone)   │                │
│  │  (Console UI)  │    │                │                │
│  └────────────────┘    └───────┬────────┘                │
└────────────────────────────────┼─────────────────────────┘
                                 │ WireGuard VPN
                                 ▼
┌────────────────────────────────────────────────────────┐
│                    Remote Zone                          │
│  ┌──────────────┐   ┌──────────────┐   ┌────────────┐  │
│  │ Terraform    │   │ Salt Minion  │   │  Remote    │  │
│  │   State      │   │              │   │   Qubes    │  │
│  └──────────────┘   └──────────────┘   └────────────┘  │
└────────────────────────────────────────────────────────┘
```
