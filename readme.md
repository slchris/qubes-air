# Qubes Air

将 Qubes OS 的安全隔离能力扩展到云端和物理分离设备

## 项目概述

Qubes Air 是 Qubes OS 官方愿景的社区实现方案。官方 Qubes Air 概念由 Joanna Rutkowska 于 2018 年提出，旨在将 Qubes OS 的安全隔离模型从本地 Xen 虚拟化泛化到多种隔离技术和平台。

本项目通过 Terraform 和 SaltStack 实现 Qubes Air 的核心理念，让用户可以在 PVE、GCP、AWS 等平台上创建和管理远程 Qube，甚至可以在物理分离的设备(如 Raspberry Pi)上运行 Qube，突破本地硬件限制，同时保持 Qubes OS 的安全架构和使用体验。

### 官方 Qubes Air 愿景

根据 Qubes OS 官方文章，Qubes Air 的核心思想是:

**解决的问题:**
- 部署成本高: 难以找到兼容 Qubes OS 的硬件
- 单点故障: 过度依赖 Xen 虚拟机管理程序
- 硬件限制: 本地资源无法满足所有计算需求

**核心理念:**
- Qube 不等于 VM: Qube 是一个隔离的容器，可以是 Xen VM、云端 VM、容器，甚至是物理分离的设备
- 隔离技术多元化: 通过在不同平台运行 Qube，分散对单一隔离技术的依赖
- 统一管理接口: 无论 Qube 在哪里运行，都通过 Qubes Admin API 和 qrexec 统一管理

### 关于迁移功能的说明

根据 Qubes OS Summit 2024 的讨论，官方 Qubes Air 实现将不包含在线/离线迁移功能:

- **在线迁移**: 类似 VMware vMotion 的实时迁移技术，开发成本极高
- **离线迁移**: 关机后传输 VM 的功能，可能在长期规划中实现

目前，`qubes-backup` 可以满足在不同机器间转移 Qube 的基本需求。本项目侧重于远程 Qube 的创建和管理，而非迁移。

## 核心目标

- **扩展性**: 突破本地硬件限制，利用云端算力和存储
- **安全性**: 通过隔离技术多元化，降低单一技术漏洞的影响
- **一致性**: 与本地 Qube 相同的管理方式，使用 Salt 进行配置管理
- **灵活性**: 按需创建/销毁远程资源，优化成本

## 使用场景

| 场景 | 说明 |
|------|------|
| 高性能计算 | 将 GPU 密集型任务卸载到云端 VM |
| 大容量存储 | 云端 Qube 处理大文件，避免占用本地空间 |
| 地理分布 | 在不同区域部署 Qube 用于特定网络访问 |
| 临时扩展 | 临时任务使用云端一次性 VM，用完即销毁 |
| 灾备冗余 | 关键 Qube 在云端保持备份实例 |
| 隔离多元化 | 敏感 Qube 运行在不同云平台，分散风险 |
| 物理隔离 | 高敏感任务在物理分离设备上运行 (如 Raspberry Pi) |

## 系统架构

### Qubes Zones 概念

Qubes Air 引入了 "Zone" (区域) 的核心概念，每个 Zone 包含:

- **隔离技术**: 实现 Qube 的底层技术 (Xen VM、云 VM、容器、物理设备)
- **通信机制**: Zone 内 Qube 间的通信方式 (Xen Grant Tables、网络等)
- **本地 Admin Qube**: 管理 Zone 内所有 Qube，可以是 Master 或 Slave 模式
- **本地 GUI Qube** (可选): 聚合 Zone 内 Qube 的图形界面
- **存储技术**: Zone 内 Qube 的存储实现方式

```
+===========================================================================+
|                         Qubes OS 本地 Zone (Master)                        |
|  +---------------------------------------------------------------------+  |
|  |                    dom0 / AdminVM (Master)                          |  |
|  |  +------------------+  +------------------+  +--------------------+ |  |
|  |  | Qubes Manager    |  | Salt Master      |  | Terraform         | |  |
|  |  | (Master GUI)     |  | (配置管理)       |  | (基础设施编排)    | |  |
|  |  +------------------+  +------------------+  +--------------------+ |  |
|  +---------------------------------------------------------------------+  |
|       |                            |                            |         |
|  +----v----+                  +----v----+                  +----v----+    |
|  | AppVM   |                  | sys-net |                  | vault   |    |
|  | (Xen)   |                  | (Xen)   |                  | (Xen)   |    |
|  +---------+                  +---------+                  +---------+    |
+===========================================================================+
         |                           |
         | qrexec 代理               | qrexec 代理
         |                           |
+========v==========+      +=========v=========+      +==================+
| 云端 Zone (Slave) |      | 云端 Zone (Slave) |      | 物理 Zone (Slave)|
| (Proxmox VE)      |      | (GCP/AWS)         |      | (Raspberry Pi)   |
|                   |      |                   |      |                  |
| +---------------+ |      | +---------------+ |      | +---------------+|
| | Slave Admin   | |      | | Slave Admin   | |      | | Slave Admin   ||
| | (Salt Minion) | |      | | (Salt Minion) | |      | | (Salt Minion) ||
| +---------------+ |      | +---------------+ |      | +---------------+|
|        |          |      |        |          |      |        |         |
| +------v--------+ |      | +------v--------+ |      | +------v-------+ |
| | remote-work   | |      | | remote-gpu    | |      | | air-gapped   | |
| | remote-dev    | |      | | remote-build  | |      | | vault-backup | |
| +---------------+ |      | +---------------+ |      | +--------------+ |
+==================+       +==================+       +==================+
```

### 混合模式 (Hybrid Mode)

用户可以同时运行:
- 本地 Qube: 低延迟、高隐私需求的任务
- 云端 Qube: 高性能计算、大存储需求
- 物理分离 Qube: 极高安全需求 (如密钥管理)

不同 Zone 的 Qube 可以通过 qrexec 服务透明通信，用户无需关心 Qube 的实际位置。

### 隔离多元化的安全优势

通过将 Qube 分布在不同平台:

```
+-------------------+     +-------------------+     +-------------------+
|  本地 Xen Zone    |     |   GCP Zone        |     |   AWS Zone        |
|  (Xen 漏洞影响)   |     | (GCP 漏洞影响)    |     | (AWS 漏洞影响)    |
|                   |     |                   |     |                   |
| work-qube         |     | dev-qube          |     | build-qube        |
| personal-qube     |     | gpu-qube          |     | ci-qube           |
+-------------------+     +-------------------+     +-------------------+
```

如果某个平台 (如 Xen) 发现严重安全漏洞，只有该 Zone 内的 Qube 受影响，其他 Zone 的 Qube 保持安全。

## 与本地 Qube 的集成

### Qube 接口规范

根据官方 Qubes Air 设计，一个 Qube 应实现以下接口:

1. **vchan 端点**: 底层通信通道，可基于 Xen 共享内存、TCP/IP 等实现
2. **qrexec 端点**: 基于 vchan 的服务调用机制，确保 Qubes Apps (Split GPG、PDF 转换器等) 正常工作
3. **GUI 端点** (可选): 图形界面协议端点
4. **网络接口**: 一个上行接口，可选多个下行接口 (用于代理 Qube)
5. **存储卷**: 只读 root 卷 + 可读写 private 卷 (支持模板机制)

### 管理方式对比

| 功能 | 本地 Qube | 远程 Qube (Qubes Air) |
|------|-----------|----------------------|
| 创建/销毁 | qvm-create/qvm-remove | terraform apply/destroy + Admin API |
| 配置管理 | qubesctl (Salt) | qubesctl + 远程 Salt Minion |
| 服务调用 | qrexec (本地) | qrexec (跨 Zone 代理) |
| 网络策略 | Qubes Firewall | 云平台 Security Group + 本地策略 |
| 文件传输 | qvm-copy-to-vm | 通过 qrexec 代理传输 |
| 剪贴板 | Qubes 安全剪贴板 | 通过安全通道同步 (延迟较高) |
| GUI | Qubes GUI Protocol | GUI Protocol + RDP/VNC 聚合 |

### GUI 虚拟化考虑

Qubes GUI 协议为安全优化，牺牲了压缩等性能特性。在远程 Zone 中:

- Zone 内: 使用 Qubes GUI Protocol (快速，因为同 Zone 内通信快)
- Zone 间: 使用 RDP/VNC 等高效协议聚合到 Master GUI Qube
- 本地 Qube: 保持原有低延迟体验

## 远程 Qube 分类

| Qube 类型 | 用途 | 适合平台 | 网络策略 |
|-----------|------|----------|----------|
| remote-work | 远程办公、文档协作 | PVE/GCP | 通过 sys-remote 访问 |
| remote-dev | 代码开发、大型编译 | GCP/AWS | 按需访问 |
| remote-gpu | GPU 计算、AI/ML 任务 | GCP/AWS | 受限访问 |
| remote-data | 大容量数据处理 | PVE/AWS | 内网隔离 |
| remote-build | CI/CD、自动化构建 | GCP/AWS | 按需访问 |
| remote-disp | 一次性任务 | 任意 | 临时配置 |
| air-gapped | 物理隔离的高安全任务 | Raspberry Pi/USB Armory | 完全隔离 |

## 技术栈

### 核心组件

| 组件 | 技术选型 | 说明 |
|------|----------|------|
| 基础设施编排 | Terraform | 跨平台创建/管理云端 VM |
| 配置管理 | SaltStack | 与 Qubes OS 原生 Salt 集成，实现 qubesctl 统一管理 |
| 镜像构建 | Packer | 预构建远程 Qube 模板 |
| 辅助工具 | Ansible | 初始化引导、Salt Minion 部署 |
| 状态存储 | 本地加密 / Vault Qube | 状态文件安全存储 |
| 通信层 | qrexec over vchan | Zone 间 qrexec 代理，基于 TCP/IP 实现 vchan |

### 管理控制台技术栈

考虑到 Qubes OS 的 GPU 驱动限制和安全要求，管理控制台采用轻量级设计:

#### 后端

| 组件 | 技术选型 | 版本要求 | 说明 |
|------|----------|----------|------|
| 语言 | Go (Golang) | >= 1.22 | 高性能、静态编译、内存安全 |
| Web 框架 | Gin / Echo | 最新稳定版 | 轻量级 HTTP 框架 |
| API 风格 | REST + gRPC | - | REST 供前端调用，gRPC 供内部服务通信 |
| 认证 | OAuth2 / mTLS | - | 支持证书双向认证 |
| 数据库 | SQLite / BoltDB | - | 嵌入式数据库，无外部依赖 |

**Go 后端设计原则:**
- 静态编译，单二进制部署，减少依赖
- 最小化外部库依赖，降低供应链攻击风险
- 定期更新到最新 Go 版本，获取安全修复
- 使用 Go 原生加密库，避免 CGO

#### 前端

| 组件 | 技术选型 | 版本要求 | 说明 |
|------|----------|----------|------|
| 框架 | Svelte / SolidJS | >= 5.0 / >= 1.8 | 轻量级响应式框架，编译后体积小 |
| UI 库 | Skeleton UI / DaisyUI | 最新稳定版 | 轻量 CSS 框架，无重 JS 依赖 |
| 构建工具 | Vite | >= 5.0 | 快速构建，tree-shaking 优化 |
| 状态管理 | 框架内置 | - | 避免额外状态库开销 |

**前端设计原则 (针对 Qubes OS GPU 限制):**
- 禁用 CSS 动画和过渡效果 (`prefers-reduced-motion`)
- 不使用 WebGL、Canvas 复杂渲染
- 避免大量 DOM 操作和虚拟滚动
- 使用系统原生字体，不加载 Web 字体
- 静态资源本地化，不依赖 CDN
- 支持纯 HTML 降级模式

```
前端架构:

+--------------------------------------------------+
|                  Qubes Air Console               |
|  +--------------------------------------------+  |
|  |  轻量级 UI (Svelte/SolidJS)                |  |
|  |  - 无动画/无特效                           |  |
|  |  - 最小化 JS 体积                          |  |
|  |  - 系统原生字体                            |  |
|  +--------------------------------------------+  |
|              |                                   |
|              v                                   |
|  +--------------------------------------------+  |
|  |  Go 后端 (Gin/Echo)                        |  |
|  |  - 静态编译单二进制                        |  |
|  |  - 嵌入式前端资源                          |  |
|  |  - 嵌入式 SQLite                           |  |
|  +--------------------------------------------+  |
|              |                                   |
|              v                                   |
|  +--------------------------------------------+  |
|  |  系统集成层                                 |  |
|  |  - Terraform CLI 调用                      |  |
|  |  - Salt API 集成                           |  |
|  |  - qrexec 服务调用                         |  |
|  +--------------------------------------------+  |
+--------------------------------------------------+
```

#### 安全加固

| 措施 | 说明 |
|------|------|
| 依赖审计 | 使用 `govulncheck` 和 `npm audit` 定期扫描 |
| 最小依赖 | 仅使用必要的第三方库 |
| SRI 校验 | 静态资源完整性校验 |
| CSP 策略 | 严格的内容安全策略 |
| 输入验证 | 所有输入严格验证和转义 |
| 认证加固 | 支持 YubiKey / TOTP 二次认证 |

#### 部署方式

```bash
# 单二进制部署 (前端资源嵌入)
[user@dom0 ~]$ ./qubes-air-console serve --port 8443 --tls

# 或作为 Qube 内的服务运行
[user@admin-qube ~]$ qubes-air-console serve
```

### SaltStack 集成架构

```
+----------------------------------+
|  dom0 / AdminVM (Salt Master)    |
|  +----------------------------+  |
|  | /srv/salt/                 |  |
|  |   |-- base/               |  |
|  |   |-- qubes-air/          |  |  <-- 远程 Qube 专用 Salt States
|  |   |   |-- remote-work.sls |  |
|  |   |   |-- remote-dev.sls  |  |
|  |   |   |-- remote-gpu.sls  |  |
|  |   |-- pillar/             |  |
|  +----------------------------+  |
+----------------------------------+
         |
         | Salt 通信 (加密)
         |
+--------v---------+    +---------v--------+
| 本地 Qube        |    | 远程 Qube        |
| (Salt Minion)    |    | (Salt Minion)    |
| 通过 Xen 通信    |    | 通过 SSH/VPN     |
+------------------+    +------------------+
```

### 为什么需要 SaltStack + Ansible

| 工具 | 职责 | 原因 |
|------|------|------|
| SaltStack | 持续配置管理 | Qubes OS 原生使用 Salt，保持一致性 |
| Ansible | 初始引导 | Salt Minion 部署前的初始化配置 |
| Terraform | 基础设施 | 云平台 VM 生命周期管理 |

### 支持平台 (Zones)

| 平台/Zone 类型 | Provider/技术 | 适用场景 |
|----------------|---------------|----------|
| Proxmox VE | bpg/proxmox | 私有云/家庭实验室/自托管 |
| Google Cloud | hashicorp/google | 公有云、GPU 实例 |
| AWS | hashicorp/aws | 公有云、全球覆盖 |
| Azure | hashicorp/azurerm | 公有云、企业集成 |
| Raspberry Pi | SSH + Salt | 物理隔离 Zone，高安全需求 |
| USB Armory | SSH + Salt | 便携式物理隔离设备 |

### 远程 Qube 操作系统

- **推荐系统**: Fedora (与 Qubes OS 模板保持一致)
- **备选系统**: Debian、Ubuntu
- **轻量系统**: Alpine (适合一次性 Qube、物理设备)
- **匿名系统**: Whonix Gateway/Workstation
- **嵌入式**: Raspbian (Raspberry Pi Zone)

## 网络架构

### Zone 间通信: qrexec 代理

在 Qubes Air 中，Zone 间的 qrexec 服务调用通过代理实现:

```
+====================+                      +====================+
|  本地 Zone         |                      |  远程 Zone         |
|                    |                      |  (云端/物理设备)   |
|  +------------+    |                      |    +------------+  |
|  | AppVM      |    |                      |    | remote-qube|  |
|  | (请求方)   |    |                      |    | (服务方)   |  |
|  +-----+------+    |                      |    +-----^------+  |
|        | qrexec    |                      |          | qrexec  |
|  +-----v------+    |   TCP/IP + 加密      |    +-----+------+  |
|  | dom0       +----+----------------------+----+ Slave Admin|  |
|  | (Master)   |    |   qrexec 代理        |    | (代理)     |  |
|  +------------+    |                      |    +------------+  |
+====================+                      +====================+
```

### sys-remote: 远程 Zone 网关

在 Qubes OS 中创建专用的 `sys-remote` ServiceVM，作为所有远程 Zone 的网络网关：

```
+===========================================================================+
|                              Qubes OS                                      |
|                                                                            |
|  +------------------+     +------------------+     +------------------+    |
|  |    AppVM-1       |     |    AppVM-2       |     |   vault          |    |
|  |    netvm:        |     |    netvm:        |     |   netvm: none    |    |
|  |    sys-firewall  |     |    sys-firewall  |     |                  |    |
|  +--------+---------+     +--------+---------+     +------------------+    |
|           |                        |                                       |
|  +--------v-----------------------v---------+                              |
|  |              sys-firewall                 |                              |
|  +--------+---------------------------------+                              |
|           |                                                                |
|  +--------v---------+     +------------------+                              |
|  |    sys-net       |     |   sys-remote     |  <-- 新增: 远程 Zone 网关    |
|  |    (本地网络)    |     |   (Zone 隧道)    |                              |
|  +--------+---------+     +--------+---------+                              |
|           |                        |                                       |
+===========================================================================+
            |                        |
            v                        v
       本地网络               WireGuard/SSH 隧道
                                     |
                    +----------------+----------------+
                    |                |                |
            +-------v------+ +-------v------+ +-------v------+
            | PVE Zone     | | GCP Zone     | | Pi Zone      |
            | remote-work  | | remote-gpu   | | air-gapped   |
            | remote-dev   | | remote-build | | vault-backup |
            +--------------+ +--------------+ +--------------+
```

### 网络安全策略

1. **隧道加密**: 所有 Zone 间通信通过 WireGuard 或 SSH 隧道
2. **双重防火墙**: 云平台 Security Group + Qubes 防火墙规则
3. **最小暴露**: 远程 Qube 仅开放必要端口给 sys-remote
4. **证书认证**: 使用证书而非密码进行身份验证
5. **Zone 隔离**: 不同 Zone 间默认无法直接通信，必须通过 Master Admin 代理

## 安全设计

### 威胁模型

| 威胁 | 缓解措施 |
|------|----------|
| 云平台被入侵 | 远程 Qube 不存储长期敏感数据，使用后销毁 |
| 云平台数据泄露 | 所有数据加密存储，密钥由本地或 HSM 管理 |
| 单一隔离技术漏洞 | 隔离多元化: 敏感 Qube 分布在不同 Zone |
| 网络监听 | 全程加密隧道，证书双向认证 |
| 凭证泄露 | 临时凭证、短期 Token、硬件密钥 |
| 横向移动 | 每个远程 Qube 独立隔离，Zone 间通信受控 |
| Xen/KVM 漏洞 | 高敏感任务在物理隔离 Zone (如 Raspberry Pi) 运行 |
| 数据篡改 | 使用 Split GPG 签名验证数据完整性 |
| 密钥泄露 | 主密钥永不离开安全环境，使用 HSM 保护 |

### 隔离多元化策略

根据 Qube 的敏感程度分配到不同 Zone:

```
敏感度: 极高          高              中              低
        |             |               |               |
        v             v               v               v
+---------------+ +----------+ +------------+ +-------------+
| 物理隔离 Zone | | 私有云   | | 公有云     | | 公有云      |
| (Raspberry Pi)| | Zone     | | Zone A     | | Zone B      |
|               | | (PVE)    | | (GCP)      | | (AWS)       |
| - GPG 密钥    | | - vault  | | - work     | | - disposable|
| - 离线签名    | | - 开发   | | - dev      | | - 一次性    |
+---------------+ +----------+ +------------+ +-------------+
```

### 密钥管理

- 云平台凭证存储在本地 vault Qube 中
- SSH 密钥使用 split-ssh 架构，私钥不离开 vault
- 支持 YubiKey 等硬件密钥进行身份验证
- Terraform 状态文件加密存储

### HSM 与密钥安全架构

远程 Qube 中的数据不能以明文形式存储在云端，需要完整的加密和签名机制:

#### 密钥生成策略

| 策略 | 实现方式 | 适用场景 | 安全级别 |
|------|----------|----------|----------|
| 本地生成 | vault Qube 或物理隔离设备生成密钥 | 高敏感数据、长期密钥 | 极高 |
| 云端 HSM | AWS CloudHSM / GCP Cloud HSM / Azure Key Vault | 云端数据加密、合规需求 | 高 |
| 混合模式 | 本地主密钥 + 云端派生密钥 | 平衡安全与便利 | 高 |

#### 加密架构

```
+===========================================================================+
|                              密钥层级架构                                  |
+===========================================================================+
|                                                                           |
|  +-------------------+                                                    |
|  | 根密钥 (Master)   |  本地 vault Qube / 物理隔离设备 / YubiKey          |
|  | 永不离开安全环境  |                                                    |
|  +---------+---------+                                                    |
|            |                                                              |
|            | 派生/加密                                                    |
|            v                                                              |
|  +-------------------+     +-------------------+     +-------------------+ |
|  | Zone 密钥 (PVE)   |     | Zone 密钥 (GCP)   |     | Zone 密钥 (AWS)   | |
|  | 加密该 Zone 数据  |     | 可选用 Cloud HSM  |     | 可选用 CloudHSM   | |
|  +---------+---------+     +---------+---------+     +---------+---------+ |
|            |                         |                         |          |
|            v                         v                         v          |
|  +-------------------+     +-------------------+     +-------------------+ |
|  | Qube 数据密钥     |     | Qube 数据密钥     |     | Qube 数据密钥     | |
|  | 加密具体数据      |     | 加密具体数据      |     | 加密具体数据      | |
|  +-------------------+     +-------------------+     +-------------------+ |
+===========================================================================+
```

#### 云平台 HSM 集成

**AWS CloudHSM / KMS:**
```hcl
# terraform 配置示例
resource "aws_kms_key" "qubes_air_zone_key" {
  description             = "Qubes Air Zone encryption key"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = { AWS = var.qubes_air_role_arn }
        Action = [
          "kms:Encrypt",
          "kms:Decrypt",
          "kms:GenerateDataKey"
        ]
        Resource = "*"
      }
    ]
  })
}
```

**GCP Cloud HSM / KMS:**
```hcl
resource "google_kms_key_ring" "qubes_air" {
  name     = "qubes-air-keyring"
  location = var.region
}

resource "google_kms_crypto_key" "zone_key" {
  name            = "zone-encryption-key"
  key_ring        = google_kms_key_ring.qubes_air.id
  rotation_period = "7776000s"  # 90 days
  
  # 使用 HSM 保护级别
  version_template {
    algorithm        = "GOOGLE_SYMMETRIC_ENCRYPTION"
    protection_level = "HSM"
  }
}
```

#### 本地密钥生成 (推荐高敏感场景)

```bash
# 在物理隔离 Zone (如 Raspberry Pi) 或 vault Qube 中生成主密钥
[user@vault ~]$ gpg --full-generate-key --expert
# 选择 ECC (Curve 25519) 或 RSA 4096

# 导出公钥供远程 Qube 使用
[user@vault ~]$ gpg --armor --export <key-id> > qubes-air-master.pub

# 使用 Split GPG 架构，私钥永不离开 vault
[user@remote-qube ~]$ qubes-gpg-client --encrypt --recipient <key-id> sensitive-data.tar
```

#### 数据加密工作流

```
1. 本地生成/获取密钥
   vault Qube -----> 生成主密钥 (GPG/age)
        |
        v
2. 派生 Zone 密钥 (可选使用云 HSM)
   主密钥 -----> 加密 Zone 密钥 -----> 安全传输到 Zone Admin
        |
        v
3. 远程 Qube 数据加密
   remote-qube: 数据 -----> Zone 密钥加密 -----> 加密存储
        |
        v
4. 数据签名验证
   vault Qube -----> Split GPG 签名 -----> 远程 Qube 验证签名
```

#### 支持的加密工具

| 工具 | 用途 | 集成方式 |
|------|------|----------|
| GPG + Split GPG | 文件加密、签名 | Qubes 原生支持 |
| age | 现代加密工具 | Salt State 配置 |
| LUKS | 磁盘全盘加密 | 远程 Qube 默认启用 |
| Vault (HashiCorp) | 密钥管理、动态凭证 | 可选部署在私有 Zone |
| SOPS | 加密配置文件 | 与 Terraform/Salt 集成 |

#### Salt Pillar 加密

敏感配置使用 GPG 加密存储:

```yaml
# /srv/pillar/qubes-air/credentials.sls
# 使用 GPG 加密的 Pillar 数据

#!yaml|gpg

cloud_credentials:
  aws:
    access_key: |
      -----BEGIN PGP MESSAGE-----
      hQEMA...加密内容...
      -----END PGP MESSAGE-----
    secret_key: |
      -----BEGIN PGP MESSAGE-----
      hQEMA...加密内容...
      -----END PGP MESSAGE-----
  
  gcp:
    service_account_key: |
      -----BEGIN PGP MESSAGE-----
      hQEMA...加密内容...
      -----END PGP MESSAGE-----
```

#### 远程 Qube 磁盘加密

```yaml
# /srv/salt/qubes-air/disk-encryption.sls

# 确保远程 Qube 磁盘加密
remote-qube-luks:
  cmd.run:
    - name: |
        # 检查是否已加密
        if ! cryptsetup isLuks /dev/vda2; then
          echo "ERROR: Disk not encrypted!"
          exit 1
        fi
    - unless: cryptsetup isLuks /dev/vda2

# 使用云 KMS 密钥加密 LUKS 密钥槽 (可选)
luks-key-escrow:
  file.managed:
    - name: /etc/qubes-air/luks-key-encrypted
    - contents_pillar: qubes_air:luks_key_encrypted
    - mode: 600
```

### 数据安全

- 远程 Qube 磁盘启用全盘加密 (LUKS)
- 云端数据使用 Zone 密钥加密，密钥由本地或云 HSM 保护
- 敏感数据处理完成后安全擦除 (`shred` 或 `srm`)
- 文件传输通过 qvm-copy 风格的安全通道，传输过程加密
- 配置文件 (Pillar/tfvars) 使用 GPG/SOPS 加密存储
- 支持数据签名验证，防止篡改

## 项目结构

```
qubes-air/
|-- terraform/
|   |-- modules/
|   |   |-- zone-base/             # Zone 基础模块
|   |   |-- remote-qube-base/      # 基础远程 Qube 模块
|   |   |-- remote-qube-work/      # 工作类型 Qube
|   |   |-- remote-qube-dev/       # 开发类型 Qube
|   |   |-- remote-qube-gpu/       # GPU 计算 Qube
|   |   |-- remote-qube-disp/      # 一次性 Qube
|   |   |-- networking/            # VPC/网络配置
|   |   |-- security/              # Security Group/防火墙
|   |   |-- kms/                   # 云 HSM/KMS 配置
|   |-- providers/
|   |   |-- proxmox/               # PVE Zone 配置
|   |   |-- gcp/                   # GCP Zone 配置
|   |   |-- aws/                   # AWS Zone 配置
|   |-- environments/
|   |   |-- home-lab/              # 家庭实验室环境
|   |   |-- production/            # 生产环境
|   |-- main.tf
|   |-- variables.tf
|   |-- outputs.tf
|-- salt/
|   |-- qubes-air/
|   |   |-- init.sls               # Salt 入口
|   |   |-- zone-slave-admin.sls   # Slave Admin Qube 配置
|   |   |-- remote-base.sls        # 远程 Qube 基础配置
|   |   |-- remote-work.sls        # 工作 Qube 配置
|   |   |-- remote-dev.sls         # 开发 Qube 配置
|   |   |-- remote-gpu.sls         # GPU Qube 配置
|   |   |-- salt-minion.sls        # Salt Minion 配置
|   |   |-- wireguard.sls          # WireGuard 隧道配置
|   |   |-- qrexec-proxy.sls       # qrexec 代理配置
|   |   |-- disk-encryption.sls    # 磁盘加密配置
|   |   |-- data-encryption.sls    # 数据加密配置
|   |-- pillar/
|   |   |-- qubes-air/
|   |   |   |-- init.sls
|   |   |   |-- zones.sls          # Zone 配置
|   |   |   |-- credentials.sls    # 加密凭证 (GPG)
|   |   |   |-- keys.sls           # Zone 密钥配置 (加密)
|-- ansible/
|   |-- playbooks/
|   |   |-- bootstrap.yml          # 初始引导 (安装 Salt Minion)
|   |   |-- wireguard-setup.yml    # WireGuard 初始化
|   |   |-- zone-init.yml          # Zone 初始化
|   |   |-- hsm-setup.yml          # HSM/KMS 配置
|   |-- roles/
|   |   |-- salt-minion/           # Salt Minion 部署
|   |   |-- base-hardening/        # 基础安全加固
|   |   |-- qrexec-agent/          # qrexec 代理安装
|   |   |-- encryption-tools/      # 加密工具部署
|   |-- inventory/
|-- packer/
|   |-- templates/
|   |   |-- fedora-remote-qube.pkr.hcl
|   |   |-- debian-remote-qube.pkr.hcl
|   |   |-- alpine-minimal.pkr.hcl     # 轻量级一次性 Qube
|-- dom0-scripts/
|   |-- qubes-air-ctl              # 主控制脚本
|   |-- zone-manage.sh             # Zone 管理脚本
|   |-- create-remote-qube.sh      # 创建远程 Qube
|   |-- destroy-remote-qube.sh     # 销毁远程 Qube
|   |-- sync-to-remote.sh          # 文件同步到远程
|   |-- sync-from-remote.sh        # 从远程同步文件
|-- sys-remote/
|   |-- wireguard/                 # WireGuard 配置
|   |-- ssh-config/                # SSH 配置
|   |-- qrexec-proxy/              # qrexec 代理配置
|   |-- firewall-rules/            # 防火墙规则
|-- physical-zones/
|   |-- raspberry-pi/              # Raspberry Pi Zone 配置
|   |-- usb-armory/                # USB Armory 配置
|-- crypto/
|   |-- gpg/                       # GPG 密钥模板和策略
|   |-- sops/                      # SOPS 配置
|   |-- age/                       # age 加密配置
|   |-- vault/                     # HashiCorp Vault 配置 (可选)
|-- console/
|   |-- backend/                   # Go 后端
|   |   |-- cmd/                   # 入口
|   |   |-- internal/
|   |   |   |-- api/               # REST API 处理
|   |   |   |-- grpc/              # gRPC 服务
|   |   |   |-- terraform/         # Terraform 集成
|   |   |   |-- salt/              # Salt API 集成
|   |   |   |-- auth/              # 认证模块
|   |   |-- go.mod
|   |   |-- go.sum
|   |-- frontend/                  # Svelte/SolidJS 前端
|   |   |-- src/
|   |   |   |-- components/        # UI 组件 (轻量级)
|   |   |   |-- routes/            # 页面路由
|   |   |   |-- lib/               # 工具库
|   |   |-- static/                # 静态资源
|   |   |-- package.json
|   |   |-- vite.config.js
|   |-- Makefile                   # 构建脚本
|-- docs/
|   |-- architecture.md
|   |-- zones.md                   # Zone 概念详解
|   |-- security.md
|   |-- encryption.md              # 加密方案详解
|   |-- hsm-integration.md         # HSM 集成指南
|   |-- console-dev.md             # 控制台开发指南
|   |-- user-guide.md
|   |-- salt-states.md
|-- README.md
```

## 快速开始

### 前置条件

**Qubes OS 环境:**
- Qubes OS 4.2+ (推荐 4.3，Qubes Air 部分功能可能在此版本引入)
- dom0 中安装 Terraform
- Salt Master 已配置 (Qubes 默认)

**目标平台:**
- 平台访问凭证 (存储在 vault Qube)
- 网络可达性

### 安装步骤

```bash
# 1. 在 dom0 中克隆仓库
[user@dom0 ~]$ cd /home/user
[user@dom0 ~]$ git clone https://github.com/slchris/qubes-air.git

# 2. 复制 Salt States 到 Qubes Salt 目录
[user@dom0 ~]$ sudo cp -r qubes-air/salt/qubes-air /srv/salt/
[user@dom0 ~]$ sudo cp -r qubes-air/salt/pillar/qubes-air /srv/pillar/

# 3. 创建 sys-remote ServiceVM (首次)
[user@dom0 ~]$ ./qubes-air/dom0-scripts/create-sys-remote.sh

# 4. 在 vault Qube 中配置云平台凭证
[user@dom0 ~]$ qvm-run -p vault 'cat > ~/.config/qubes-air/credentials.env' < credentials.env

# 5. 初始化 Terraform (在管理 Qube 中)
[user@dom0 ~]$ qvm-run -p admin-qube 'cd ~/qubes-air/terraform && terraform init'
```

### 创建第一个远程 Zone

```bash
# 创建 Proxmox VE Zone
[user@dom0 ~]$ ./qubes-air/dom0-scripts/zone-manage.sh create \
    --type proxmox \
    --name pve-zone-1 \
    --endpoint https://pve.local:8006

# 在 Zone 中创建远程 Qube
[user@dom0 ~]$ ./qubes-air/dom0-scripts/qubes-air-ctl create \
    --zone pve-zone-1 \
    --type work \
    --name remote-work-1

# 脚本会执行:
# 1. Terraform 在指定 Zone 创建 VM
# 2. Ansible 引导安装 Salt Minion
# 3. Salt 配置 Qube
# 4. 建立 WireGuard 隧道
# 5. 配置 qrexec 代理

# 查看所有 Zone 和远程 Qube 状态
[user@dom0 ~]$ ./qubes-air/dom0-scripts/qubes-air-ctl list --all

# 连接到远程 Qube (通过 qrexec 或 SSH)
[user@dom0 ~]$ ./qubes-air/dom0-scripts/qubes-air-ctl connect remote-work-1
```

### 配置示例

```hcl
# terraform/environments/home-lab/terraform.tfvars

# Zone 定义
zones = {
  pve-zone-1 = {
    type     = "proxmox"
    endpoint = "https://pve.local:8006"
    node     = "pve-node1"
  }
  gcp-zone-1 = {
    type    = "gcp"
    project = "my-qubes-air"
    region  = "us-central1"
  }
}

# 远程 Qube 配置
remote_qubes = {
  remote-work-1 = {
    zone     = "pve-zone-1"
    type     = "work"
    cpu      = 2
    memory   = 4096
    disk     = 50
    template = "fedora-39"
  }
  remote-gpu-1 = {
    zone         = "gcp-zone-1"
    type         = "gpu"
    machine_type = "n1-standard-4"
    gpu_type     = "nvidia-tesla-t4"
    gpu_count    = 1
    disk         = 100
    template     = "fedora-39"
  }
}

# Zone 间通信配置
wireguard_config = {
  listen_port = 51820
  network     = "10.200.0.0/16"
  # 每个 Zone 分配一个 /24 子网
}
```

### Salt State 示例

```yaml
# /srv/salt/qubes-air/zone-slave-admin.sls
# Slave Admin Qube 配置 - 管理 Zone 内的所有 Qube

include:
  - qubes-air.remote-base

# qrexec 代理服务
qrexec-proxy:
  pkg.installed:
    - name: qubes-air-qrexec-proxy
  service.running:
    - name: qrexec-proxy
    - enable: True

# Salt Minion - 连接回 Master
salt-minion-config:
  file.managed:
    - name: /etc/salt/minion.d/qubes-air.conf
    - contents: |
        master: {{ pillar['qubes_air']['salt_master_ip'] }}
        id: {{ grains['zone_name'] }}-admin
        # Zone 内 Qube 作为 sub-minions
        syndic_master: {{ pillar['qubes_air']['salt_master_ip'] }}

# Zone 内 Qube 管理
zone-qube-management:
  file.managed:
    - name: /etc/qubes-air/zone.conf
    - contents: |
        zone_name: {{ grains['zone_name'] }}
        zone_type: {{ grains['zone_type'] }}
        master_endpoint: {{ pillar['qubes_air']['master_endpoint'] }}
```

```yaml
# /srv/salt/qubes-air/remote-work.sls

include:
  - qubes-air.remote-base

# 工作 Qube 专用配置
remote-work-packages:
  pkg.installed:
    - pkgs:
      - libreoffice
      - thunderbird
      - firefox

# 防火墙配置 - 仅允许必要出站
remote-work-firewall:
  firewalld.present:
    - name: work-zone
    - services:
      - ssh
    - ports:
      - 443/tcp
      - 993/tcp   # IMAPS
      - 587/tcp   # SMTP

# Salt Minion 配置 - 连接回 dom0
salt-minion-config:
  file.managed:
    - name: /etc/salt/minion.d/qubes-air.conf
    - contents: |
        master: {{ pillar['qubes_air']['salt_master_ip'] }}
        id: {{ grains['id'] }}
```

## 运维管理

### 日常操作

| 操作 | 命令 |
|------|------|
| 列出所有 Zone | `qubes-air-ctl zone list` |
| 列出远程 Qube | `qubes-air-ctl list` |
| 启动远程 Qube | `qubes-air-ctl start <name>` |
| 停止远程 Qube | `qubes-air-ctl stop <name>` |
| 销毁远程 Qube | `qubes-air-ctl destroy <name>` |
| 更新配置 | `qubesctl --targets=<name> state.apply qubes-air` |
| 跨 Zone 文件传输 | `qubes-air-ctl copy-to <name> <file>` |
| 调用远程 qrexec 服务 | `qrexec-client -d <remote-qube> <service>` |

### Salt 管理

```bash
# 在 dom0 中使用 qubesctl 管理远程 Qube
[user@dom0 ~]$ sudo qubesctl --targets=remote-work-1 state.apply

# 查看所有 Zone 的 Salt Minion 状态
[user@dom0 ~]$ sudo salt '*-admin' test.ping   # Zone Admin
[user@dom0 ~]$ sudo salt 'remote-*' test.ping  # 所有远程 Qube

# 批量更新指定 Zone 的所有 Qube
[user@dom0 ~]$ sudo salt -C 'G@zone_name:pve-zone-1' state.apply

# 通过 Salt Syndic 管理嵌套结构
[user@dom0 ~]$ sudo salt 'pve-zone-1-admin' salt.cmd 'remote-work-*' test.ping
```

### 监控与日志

- Zone Admin Qube 收集 Zone 内所有 Qube 的状态
- Salt 事件日志记录所有配置变更
- 可选: 集成 Prometheus 监控远程资源使用
- Zone 健康检查自动化

### 备份策略

- 远程 Qube 设计为无状态或短期状态
- 重要数据应同步回本地 Qube
- Terraform 状态文件加密备份到 vault Qube
- 注意: 本项目不实现在线/离线迁移，使用 `qubes-backup` 进行本地 Qube 备份

## 工作流示例

### 场景: GPU 加速的 AI 开发 (混合模式)

```
本地 Zone                              GCP Zone
+-----------+                         +------------+
| dev-qube  |  -- 代码同步 -->        | remote-gpu |
| (编写代码)|                         | (训练模型) |
+-----------+                         +------+-----+
                                             |
                                      模型文件同步
                                             |
+-----------+                         +------v-----+
| work-qube |  <-- 结果同步 --        | 训练完成   |
| (使用模型)|                         | 销毁 Qube  |
+-----------+                         +------------+
```

### 场景: 隔离多元化的敏感工作

```
物理 Zone (Raspberry Pi)    本地 Zone (Xen)     云端 Zone (PVE)
+------------------+        +-------------+      +---------------+
| GPG 密钥签名     |        | 文档编辑    |      | 代码编译      |
| 离线操作         |        | 日常工作    |      | CI/CD         |
+------------------+        +-------------+      +---------------+
       |                          |                    |
       +----------+---------------+--------------------+
                  |
          统一通过 dom0 管理
          不同平台漏洞不会相互影响
```

### 场景: qrexec 服务跨 Zone 调用

```bash
# 在本地 Qube 中调用远程 Zone 的 Split GPG
[user@work-qube ~]$ qubes-gpg-client --sign document.txt
# 请求透明路由到远程 Zone 的 GPG Qube

# 在远程 Qube 中调用本地 vault 的密钥
[user@remote-dev ~]$ ssh-add-qube  # 通过 qrexec 代理访问本地 vault
```

### 场景: 加密数据处理工作流

```
1. 准备加密数据上传
   [user@vault ~]$ qubes-gpg-client --encrypt sensitive-data.tar
   
2. 上传加密数据到远程 Qube
   [user@dom0 ~]$ qubes-air-ctl copy-to remote-gpu-1 sensitive-data.tar.gpg
   
3. 远程处理 (数据始终加密存储)
   [user@remote-gpu-1 ~]$ qubes-gpg-client --decrypt sensitive-data.tar.gpg
   # 解密后处理，结果重新加密
   [user@remote-gpu-1 ~]$ qubes-gpg-client --encrypt result.tar
   
4. 下载并验证
   [user@dom0 ~]$ qubes-air-ctl copy-from remote-gpu-1 result.tar.gpg
   [user@vault ~]$ qubes-gpg-client --verify result.tar.gpg.sig
```

### 场景: 使用云 HSM 保护 Zone 密钥

```
1. 在 GCP Zone 创建 HSM 保护的密钥
   [user@admin-qube ~]$ terraform apply -target=module.gcp-hsm
   
2. Zone 内 Qube 使用 HSM 密钥加密数据
   # 数据密钥由 HSM 加密，HSM 密钥永不导出
   [user@remote-gpu-1 ~]$ gcloud kms encrypt \
       --keyring=qubes-air-keyring \
       --key=zone-encryption-key \
       --plaintext-file=data-key.bin \
       --ciphertext-file=data-key.enc
   
3. 本地主密钥可以额外加密 HSM 密钥标识
   # 双重保护: 本地主密钥 + 云 HSM
```

## 路线图

### Phase 1 - Zone 基础架构 (MVP)

- [x] 项目架构设计 (基于官方 Qubes Air 愿景)
- [ ] sys-remote ServiceVM 模板
- [ ] Zone 管理框架 (Terraform modules)
- [ ] Proxmox VE Zone 实现
- [ ] Salt States 基础框架
- [ ] WireGuard Zone 间隧道
- [ ] qubes-air-ctl 控制脚本
- [ ] 基础数据加密框架

### Phase 2 - 多 Zone 支持与加密

- [ ] GCP Zone 实现
- [ ] AWS Zone 实现
- [ ] Raspberry Pi 物理 Zone 实现
- [ ] Packer 镜像模板
- [ ] Zone Admin (Slave) 自动配置
- [ ] 云 HSM/KMS 集成 (AWS/GCP)
- [ ] Salt Pillar GPG 加密

### Phase 3 - qrexec 集成

- [ ] vchan over TCP/IP 实现
- [ ] qrexec 代理服务
- [ ] 跨 Zone 服务调用 (Split GPG、文件传输等)
- [ ] Qubes Apps 兼容性测试

### Phase 4 - 管理控制台

- [ ] Go 后端 API 开发 (Go 1.22+)
- [ ] Svelte/SolidJS 轻量级前端
- [ ] Terraform/Salt 集成层
- [ ] Zone/Qube 管理界面
- [ ] 认证与权限管理
- [ ] 前端无动画/低资源模式

### Phase 5 - GUI 与深度集成

- [ ] Slave GUI Qube 实现
- [ ] RDP/VNC GUI 聚合
- [ ] Qubes Manager Zone 视图
- [ ] 远程一次性 Qube (DispVM) 支持

### Phase 6 - 高级特性

- [ ] 多用户/多租户支持
- [ ] 成本监控与优化建议
- [ ] 隔离多元化策略自动化
- [ ] Whonix 远程 Zone

## 与官方 Qubes Air 的关系

本项目是社区对官方 Qubes Air 愿景的实现尝试。根据 Qubes OS Summit 2024 的信息:

- 官方 Qubes Air 的部分功能可能在 Qubes OS 4.3 中引入
- 本项目关注实用性，优先实现核心功能
- 随着官方实现的推进，本项目将尽量与官方 API 保持兼容

## 参考资料

- [Qubes Air: Generalizing the Qubes Architecture](https://www.qubes-os.org/news/2018/01/22/qubes-air/) - 官方愿景文章
- [Qubes Air 迁移讨论](https://forum.qubes-os.org/t/qubes-air-will-not-support-online-offline-migration-true-meaning-of-it-plans/29314) - 社区讨论
- [Qubes OS Salt 文档](https://www.qubes-os.org/doc/salt/)
- [Qubes Admin API](https://www.qubes-os.org/doc/admin-api/)
- [Qubes Core Stack](https://www.qubes-os.org/news/2017/10/03/core3/)
- [Terraform 官方文档](https://developer.hashicorp.com/terraform/docs)
- [SaltStack 官方文档](https://docs.saltproject.io/)

## 相关项目

- [Qubes OS](https://www.qubes-os.org/) - 基于安全隔离的操作系统
- [qubes-core-admin](https://github.com/QubesOS/qubes-core-admin) - Qubes Admin API 实现
- [split-ssh](https://www.qubes-os.org/doc/split-ssh/) - SSH 密钥隔离方案

## 许可证

MIT License
