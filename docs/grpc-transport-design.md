# gRPC 双向流传输层设计（阶段 T）

> **状态：Go 实现完整、真机（单机）跑通。** 这是路线图 [阶段 T](roadmap-to-production.md#阶段-t--grpc-双向流传输关键路径) 的设计文档。
>
> **真机验证（2026-07）：** 交叉编译 linux/amd64 的 `grpc-server` + `relay-client` 在真实 Qubes AppVM（mgmt-jump）上跑通——`relay-client` 读 `mgmt.remotevm.grpc-relay` salt state 渲染出的 `relay.env`，与 `grpc-server` 建立 mTLS 双向流 Tunnel（`ss` 确认 ESTABLISHED、零重连），`grpc-smoke` 完成一次 `qubesair.Ping` Call 往返。dom0 policy 在真机上确实拒绝 relay→dom0 直连（安全红线生效）。待做：跨两台机验证、Salt 在真 dom0 apply、远端提供 `qubesair.Ping`。
>
> **已实现（编译 + `go test` 通过）：**
> - proto 已生成（`internal/transport/relaypb`）；`Transport` 接口 + Noop/Fake（`internal/transport`）
> - gRPC **client**（出站 dial + Tunnel + 多路复用 + 保活 + 重连）与 **server**（mTLS listen + Tunnel handler + 反向帧转发）
> - **client↔server mTLS 端到端集成测试通过**（`integration_test.go` 起真实 mTLS server、拨号、正向 Call 走完整条 Tunnel）
> - **`QrexecInvoker`**（`invoker.go`，server 端 post 远端 dom0 再校验后 shell 到 `qrexec-client-vm`，复用可测的 `qrexec.Client`）
> - **`ReverseHandler`**（`reverse.go`，把远端反向调用交本地固定 target 经 dom0 policy C: ask）
> - **mTLS 证书经 vault 下发**（`vaultcerts.go`，`qubesair.GetCredential+<name>` 内存取 cert/key/CA 建 tls.Config，不落盘）
> - `qrexec.Client` 重构为可注入 Runner（弃用自造协议 `qubes-air.Remote`/`.Status`）
> - `config.TransportConfig`（含 vault 证书 / 反向 target）+ `main.go` 装配（默认 Noop）；`NewServerWithQrexec` 便捷构造
> - 上述均有单测（invoker / reverse / vaultcerts / qrexec / config）
>
> - **业务消费 Transport 已接通**：`QubeService.CheckReachable`（`qube_service.go`）经 `transport.Call` 向远端 Qube 发 qrexec 探活（`qubesair.Ping`），HTTP `GET /qubes/:id/reachable`；默认 NoopTransport 时 fail loudly（`ErrUnreachable`）。有单测（通/隧道错/未配置/未找到）。
>
> **已补齐：** `qubesair.Ping` 服务、`grpc-relay.sls` + `grpc-remote.sls`（含远端部署 bundle）、`grpc-server -qrexec` 生产模式、**证书轮换对齐**（`TLSProvider` 每次重连从 vault 取新证书，有单测）。
>
> **仍待做（[TODO]）：** **跨两台机真机验证**（server 在远端云 VM、client 在 relay，穿真实 NAT）；Salt 在真 dom0 apply（mgmt-jump 非 dom0）。
>
> 现有 SSHProxy 骨架（[runbook-remotevm.md](runbook-remotevm.md)）作为过渡参考保留。契约见 [`console/backend/proto/relay_transport.proto`](../console/backend/proto/relay_transport.proto)。

## 1. 目标与约束

把本地 `sys-relay` 与远端 `Remote-Relay` 之间的跨机传输，从 SSHProxy（autossh + `ssh -R`）改为 **gRPC 双向流**。

**硬约束（不可违反）：**

| 约束 | 说明 |
|---|---|
| 零入站 | relay 只**出站**建连，远端**不监听任何入站端口**；家庭 NAT 后可用 |
| 两侧 dom0 各校验一次 | 传输层**只搬运帧、不做授权**；正向过远端 dom0 policy，反向过本地 dom0 policy C（ask） |
| Relay 不得直达 dom0 | relay 是普通 AppVM，经 qrexec 交互，无 Admin API 权限 |
| 凭据不外泄 | mTLS 证书/私钥存无网络的 vault-cloud，用时经 qrexec ask 下发，不落盘 |
| 对齐 RemoteVM 语义 | `remote_name` 等对齐 Qubes RemoteVM 属性；qrexec 服务名不变 |

**为什么用 gRPC 双向流而非裸 SSH 隧道：**
- 一条应用层长连接同时承载正向调用与反向回程（`Tunnel` stream），天然满足"出站 + 双向 + 零入站"
- 结构化服务契约（proto）、内建流控、mTLS、可观测性
- 与控制台同栈（Go + gRPC），便于统一接入与测试

## 2. 服务契约

单服务 `RelayTransport`，单方法 `Tunnel(stream Frame) returns (stream Frame)`：

- **client** = 本地 `sys-relay`，**主动出站**建立 `Tunnel`
- **server** = 远端 `Remote-Relay`
- 一条 `Tunnel` 用 `request_id` **多路复用**多个 qrexec 调用（正向 + 反向共用同一条流）

**帧类型（`Frame.kind` oneof）：**

| 帧 | 方向 | 用途 |
|---|---|---|
| `Handshake` | 双向首帧 | 交换协议版本、relay_name、remote_name |
| `RequestHeader` | 发起方 | 一次调用的头（direction、qrexec_service、源/目标 qube、deadline） |
| `DataChunk` | 双向 | payload 分片（stream_id：0=请求体 / 1=响应体 / 2=stderr） |
| `EndOfStream` | 双向 | 某 stream_id 数据结束 |
| `CallError` | 双向 | 调用级错误（不影响整条 Tunnel） |
| `KeepAlive` | 双向 | 心跳保活，保持 NAT 映射 |

**一次调用的帧序列：**
```
RequestHeader(request_id=R, dir=LOCAL_TO_REMOTE, service=qubesair.Foo)
DataChunk(R, stream_id=0, payload=…)   # 请求体，可多帧
EndOfStream(R, stream_id=0)
   ── 远端 dom0 policy 再校验后执行 qrexec-client-vm ──
DataChunk(R, stream_id=1, payload=…)   # 响应体，可多帧
EndOfStream(R, stream_id=1)
```

## 3. 连接生命周期

```
本地 relay（client）                          远端 Remote-Relay（server）
  │                                                │
  │─── 出站 TCP + mTLS 握手 ───────────────────────▶│  (远端只出示证书，不主动连本地)
  │─── Tunnel: Handshake(v1, relay, remote) ──────▶│
  │◀── Handshake(ack) ─────────────────────────────│
  │                                                │
  │  ┄ KeepAlive 每 N 秒 ┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄▶│
  │◀┄ KeepAlive(ack) ┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄│
  │                                                │
  │  （多路 request_id 正向/反向复用此流）          │
  │                                                │
  ✕  断线 → client 侧检测 → 指数退避重连 → 重建 Tunnel
```

- **建连**：client 主动出站；失败退避重试。取代 autossh 的"维持出站"角色。
- **保活**：`KeepAlive` 周期心跳，保持 NAT 映射与探活。
- **重连**：断线由 client 检测并重连（指数退避 + 抖动）。在途 `request_id` 视为失败（`CallError code=UNAVAILABLE`），由上层决定是否重试幂等操作。
- **优雅关闭**：收发端 half-close 后清理该 Tunnel 的所有 request_id。

## 4. 认证：mTLS + 证书经 vault 下发

- 传输层用 **mTLS**：client 证书（relay 身份）+ server 证书（远端身份），双向校验。
- 证书/私钥**存 vault-cloud（无网络）**，`sys-relay` 启动/重连时经 **qrexec ask** 向 vault 请求，**内存使用、不落盘**（替代原 relay SSH 私钥的同款流程）。
- **证书轮换**复用现有 vault 密钥轮换机制（`crypto/scripts/rotate-keys.sh` / `cmd/rotate-key`）：新证书原子生效、旧证书吊销；在途连接下次重连时用新证书。
- **信任根**：私有 CA，CA 私钥留本地 vault，不上云。远端 server 证书由该 CA 签发。

> mTLS 只证明"连接双方是谁"，**不代表授权**——具体某次 qrexec 调用是否放行，仍由两侧 dom0 policy 独立决定（见 §5）。

## 5. qrexec 语义映射（安全模型如何保持）

**核心原则：传输层只搬运帧，授权始终在两侧 dom0。**

**正向（本地 → 远端）：**
```
work-1 ─qrexec→ 本地 dom0 (policy A/B 校验 + 改写) ─▶ sys-relay
   sys-relay 把已放行的调用编码为 Frame(dir=LOCAL_TO_REMOTE) 送入 Tunnel
   Remote-Relay 收帧 → 【远端 dom0/policy 再校验】→ qrexec-client-vm → 远端 Qube 执行
```

**反向回程（远端 → 本地，如取 vault 凭据）：**
```
远端 Qube 发起反向调用 → Remote-Relay 编码为 Frame(dir=REMOTE_TO_LOCAL) 送入 Tunnel
   sys-relay 收帧 → 交给本地 dom0 → 【本地 dom0 policy C：ask 弹窗确认】→ 才执行（如向 vault 取凭据）
```

**要点：**
- `RequestHeader.direction` 决定落地后过哪一侧 dom0，确保**每个方向都恰好过一次授权**。
- `source_qube` / `target_qube` 仅用于日志与远端 policy **匹配**，**不是授权凭据**——授权由 dom0 独立决定。
- Relay（两侧）都不得直达 dom0；所有跨 qube 交互经 qrexec。
- 破坏性/敏感操作在 dom0 走 `ask`（弹窗确认），与现有 policy 一致。

## 6. 与现有代码的接入点

模块：`console/backend/`，module path `github.com/slchris/qubes-air/console`，Go 1.24。

**现状（探查结论）：**
- Go 后端里**目前没有 transport / qrexec 传输抽象**。跨机 Suspend/Resume/Start/Stop 走的是
  `orchestrator.Executor` → **terraform CLI**（`internal/orchestrator/`），**不经 qrexec 传输**。
- `internal/qrexec/client.go` 是**孤立死代码**（未被任何包 import），且调用的 `qubes-air.Remote`/
  `.Status` 是**被评审否决的自造协议**——**不要**把它当"现有传输路径"。但它的
  `Call(ctx, target, service string, input []byte) ([]byte, error)` 签名是很好的**帧语义原型**。
- SSHProxy 传输实体只在 shell/salt（`relay/transport/qubesair.SSHProxy` + `salt/qubes-air/remotevm/*`），
  **不在 Go 里**。gRPC 传输是一条**全新 Go 路径**。
- go.mod **没有 grpc**（`protobuf` 是 gin 带入的 indirect）；需 `go get google.golang.org/grpc`
  + `protoc-gen-go` / `protoc-gen-go-grpc` 工具链。

**gRPC 传输层应实现的抽象（新建）：**
gRPC 传输**不实现** `orchestrator.Executor`（那是 terraform 编排语义 suspend/resume）。它承载的是
**qrexec 请求/响应帧转发**，语义接近 `qrexec.Client.Call`。新建包 `internal/transport`：

```go
// internal/transport/transport.go
type Transport interface {
    // 正向：本地已过 dom0 policy 的调用，经 Tunnel 送到远端执行，取回响应。
    Call(ctx context.Context, target, service string, in []byte) ([]byte, error)
}
// 反向回程由 gRPC client 收到 REMOTE_TO_LOCAL 帧后，经回调交本地 dom0（policy C: ask）。
type ReverseHandler func(ctx context.Context, service string, in []byte) ([]byte, error)
```

沿用 `orchestrator` 的**注入式四件套**范式（探查确认）：
- `Transport` interface + `NoopTransport`（默认）+ `FakeTransport`（测试记录调用）
- 名字白名单校验复用 `orchestrator.ValidQubeName` / `qrexec.validQrexecArg`（`[A-Za-z0-9._-]`）
- 注入模式照抄 `service.WithExecutor(...)` → `main.go` 的 `buildExecutor(cfg)`

**挂载点：**
| 内容 | 位置 |
|---|---|
| proto 生成 | `internal/transport/relaypb`（`go_package` 已设） |
| gRPC client（relay 端）/ server（remote 端） | `internal/transport/grpc/` |
| `Transport` 接口 + Noop/Fake | `internal/transport/` |
| 装配/注入 | `cmd/server/main.go`（`initDependencies` / 仿 `buildExecutor`） |
| 配置（端点/证书/保活/退避） | `internal/config`（仿 `OrchestratorConfig` + `TLSConfig`） |
| mTLS 证书经 vault 下发 | 复用 `qubesair.GetCredential`（qrexec，`salt/qubes-air/vault-cloud/files/`）——Go 侧客户端调用当前**缺口**，需新写（仿 `qrexec.Client.Call(target="vault-cloud", service="qubesair.GetCredential+<name>")`） |

## 7. 部署（Salt / dom0）—— 均为 [TODO]

- **[TODO]** 新增 `salt/qubes-air/remotevm/grpc-relay.sls`：在 `sys-relay` 部署 gRPC client 单元（systemd），配置出站端点、证书路径（指向 vault 下发的内存挂载）、保活/重连参数。用 bind-dirs 持久化配置。
- **[TODO]** 新增远端 `grpc-remote.sls`：在 Remote-Relay 部署 gRPC server 单元。
- **[TODO]** 旧 `relay.sls` / `autossh.sls` 已标 gRPC 迁移注释；gRPC 落地后标 DEPRECATED，保留作过渡参考。
- **[TODO]** dom0 policy：确认 gRPC 路径下 policy A/B/C 语义不变（已在 proto 注释固化），无需为传输方式改 policy——因为授权仍在 qrexec 层。

## 8. 验收标准（对齐路线图阶段 T）

- [ ] 本地 relay 出站建立 gRPC 双向流，长连接稳定、KeepAlive 正常
- [ ] 一次正向 qrexec 调用经 Tunnel 到远端、拿到响应
- [ ] 反向回程帧经同一条流回本地、过 dom0 policy C（ask）确认
- [ ] 零入站：远端无入站端口，家庭 NAT 后可用
- [ ] 断线自动重连；在途调用以 `CallError` 收尾，不泄漏、不悬挂
- [ ] mTLS 证书经 vault ask 下发、不落盘；证书轮换与 vault 轮换对齐

## 9. 风险

- **从零实现的新传输层**，无真机难以完整验证 qrexec 语义映射与 policy 交互。
- mTLS 证书轮换需与现有 vault 轮换机制严格对齐，避免轮换时断连或旧证书残留。
- 必须确保 gRPC 帧到 qrexec 的映射**不破坏"两侧 dom0 各校验一次"**——这是安全回归的红线。
- 过渡期 SSHProxy 与 gRPC 并存时，dom0 policy 与 relay 配置勿冲突。
