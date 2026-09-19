# MCP 接入设计

让 LLM 客户端（Claude Code / OpenClaw 等）通过 MCP 操作 Qubes Air 控制面，
并且**不新增任何特权路径**。

## 非目标

- 不实现第二套授权引擎：MCP 的授权就是 Console API 的授权。
- MCP 进程不持有 CA 私钥、云 API 凭据、Relay 私钥或 LUKS 密钥。
- 不绕过 dom0 policy。数据面调用与浏览器 / CLI 走完全相同的路径。
- 不对外监听。transport 只有 stdio，以及后续阶段绑定 loopback / Tailscale 的 HTTP。

## 架构

```mermaid
flowchart LR
  Client["MCP 客户端 / LLM"] -->|"MCP（stdio）"| MCP["MCP Server<br/>console AppVM 内独立进程"]
  MCP -->|"loopback + Bearer token"| API["Console API /api/v1"]
  API --> Tofu["OpenTofu 编排"]
  API --> PKI["Console CA"]
  API --> Dom0["dom0 policy"]
  Dom0 --> Relay["Relay"]
  Relay -->|mTLS| Agent["远端 agent"]
  Tofu --> Proxmox["Proxmox"]
  MCP -.->|"computer-use（默认关闭）"| Desktop["桌面会话"]
  Relay -.->|StreamTCP| Desktop
  Agent -.->|"Xpra / RFB"| GUI["远端 GUI"]
```

## 关键决定

### 1. 收口到 Console API

MCP server 不直接调用 service 层，而是作为 Console API 的 loopback HTTP 客户端。

这样认证、授权、请求体上限和审计只有一条路径，MCP 不可能拿到比 API 更大的权限。
代价是多一次本机 HTTP 往返 —— 值得，因为替代方案是把授权逻辑复制一份。

### 2. 工具分层

| 层 | 内容 | 默认 |
|---|---|---|
| 只读 | qube / zone / job / 日志 / 监控概览 / 凭据**元数据** | 注册 |
| 控制 | create / start / stop / suspend / resume / ack alert | 需要 `--scope control` |
| computer-use | 应用清单、启动应用、桌面帧、输入注入 | **不注册**，需要 `--enable-computer-use` |

写端点已经带上阶段新增的 `MaxBodySize` 中间件；MCP 侧不重复实现，只做转发。

### 3. 凭据处理

- 凭据类端点只回元数据，永不回 secret 值 —— 这一点由 Console API 决定，MCP 不额外放宽。
- MCP server 只持有 Console API 的 Bearer token。
- token 从环境变量或配置文件读，不进命令行参数（避免进 `ps`）。

### 4. computer-use 红线

这是最难收口的一层：它是「以用户身份操作图形界面」，不是「读数据」。

- 默认关闭，注册表里压根没有这些工具。
- 开启后仍需 dom0 policy `ask` 逐次确认。
- 本地必须有可见的「接管中」提示，且随时可中断。
- 复用既有 `qubesair.ConnectTCP` / `StreamTCP+`（Xpra / RFB），不新增对外端口。

## 分阶段

### Phase 1（本次落地）

- `internal/mcp`：JSON-RPC 2.0 + stdio 传输、`initialize` / `tools/list` / `tools/call` / `ping`
- `cmd/qubes-air-mcp`：stdio 主循环
- 工具：只读 + 控制两类，全部映射到既有 `/api/v1` 端点
- 作用域：在 MCP 层强制（`--scope`）
- computer-use 工具组**不注册**
- 测试：协议编解码、工具清单、作用域拒绝、上游 4xx/5xx 透传、超时、未知工具

### Phase 2（本次）

**2a — 作用域下推到 API 层（分域 token）**

Phase 1 的作用域只在 MCP 层强制，那是「实现约束」不是「权限约束」：一个 read-only 的 MCP 进程
如果被换成一个直接打 API 的脚本，权限就没有了边界。因此把作用域做进 Console API：

- `AuthConfig` 增加 `tokens`：`[{name, token, scope}]`，`scope` 为 `read-only` 或 `control`。
- 中间件解析 Bearer token 得到作用域，挂到请求上下文；
- **按方法收口**：`GET` / `HEAD` / `OPTIONS` 只需要任意已认证作用域，其余方法一律需要 `control`。
  按方法而不是逐个路由标注，是为了让以后新加的写端点**默认就是受限的**（fail-closed）。
- 单一 `api_token` 继续可用，按 `control` 处理（它是管理员令牌，不是遗留兼容分支）。
- 未配置任何 token 时保持现状：认证关闭并按启动日志告警。

**2b — computer-use：应用清单与启动应用（真实动作）**

复用既有 qrexec 通路，不新造协议：

- `GET /api/v1/qubes/{id}/appmenus`（read-only）→ 触发 `qubes.GetAppmenus`
- `POST /api/v1/qubes/{id}/apps/{app}/launch`（control）→ 触发 `qubes.StartApp+<app>`
- app id 必须按 allowlist 校验（`remote/qubes-rpc/qubes.StartApp` 自己用的就是
  `[A-Za-z0-9._+-]`），不合法时**不得发出任何上游调用**。

MCP 侧把 `desktop_apps_list` / `desktop_app_launch` 从 stub 换成真实实现。

**2c — 帧与输入（仍未实现，明确记录）**

`desktop_frame_get` / `desktop_input_send` 需要一个 RFB / Xpra 客户端接上既有
`qubesair.StreamTCP+` 通道。这本身是一个独立的协议实现，不在本次范围内；两个工具
**保持显式失败**，不会假装可用。

### 仍未实现（Phase 3 候选）

- computer-use 的帧与输入（见 2c）
- HTTP transport：绑定 lo / Tailscale，复用同一套 Bearer 与作用域校验

## 验收

- `go test -race ./...` 通过
- `make BASE_REV=origin/main pre-commit` 通过
- 新增的安全控制都有失败路径测试：作用域拒绝、未知工具、上游错误、超时
