# gRPC 双向流传输层设计（阶段 T）

> ## ⚠️ 安全模型更正（2026-07）
>
> **本文档多处描述「两侧 dom0 各校验一次」「正向过远端 dom0 policy」，这个假设不成立。**
>
> 非 Qubes 远端（PVE 上 clone 出的普通 Debian）**没有 dom0，也没有 vchan** ——
> 因此 `qrexec-client-vm` 装不上，正向调用的「第二道校验」并不存在。
>
> 正确的模型是：**授权只发生在本地 dom0**。远端 agent 内的白名单是纵深防御，
> 不是安全边界——被攻破的远端可以跳过自己的所有检查。反向方向尤其危险，
> 必须由本地 dom0 policy（`ask`）强制，而非信任远端自律。
>
> 完整分析与替代方案见 **[remote-agent-design.md](remote-agent-design.md)**。
> 本文档下方涉及"远端 dom0"的段落应按此理解，尚未逐句重写。
>
> 传输层设计本身（帧格式、双向流、零入站、mTLS）**不受影响且依然正确** ——
> 需要修正的只是"谁来做授权决策"这一点。

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

## 0. 2026-07 真机跑通：console-as-relay 每调用桥（`relay-call`）

> **状态：传输机制已在硬件上端到端验证（拿到 `pong`）。qrexec 接线是一个待人工监督执行的部署步骤（见 §0.4）。**

本节记录的是**实际已在真机跑通**的那条路，它比本文其余部分描述的「独立 Relay + 常驻
`relay-client` 守护进程 + vault 下发证书」的守护模型**更简单**，且**不需要**那套尚不存在的
Relay 证书下发设施。两条路可以并存；这条是当下能用的。

### 0.1 已经验证的事实（无需再假设）

- console 每隔数秒就对每个 qube 拨其 agent 的 mTLS gRPC 端口（`:8443`）调 `qubesair.Ping`
  做健康探测——即 `agent_health: healthy`。**传输机制本身早已在真机工作。**
  （反例佐证：探测器会把「没人监听」与「握手被拒」区分开，见 `remote-dev-1` 的
  `agent_last_error: nothing is listening ... connect: no route to host`。）
- 缺的从来不是机制，而是**一座 qrexec 桥**：本地 qube 的
  `qrexec-client-vm remote-reg2 qubesair.Ping` 会被 dom0 改写成对 RemoteVM 的
  `transport_rpc`（`qubesair.SSHProxy`）的调用，而 SSHProxy 会 `ssh` 到一台**没有 qrexec**
  的 KVM agent 宿主机——于是永远到不了那条能用的 gRPC 路。

### 0.2 `relay-call`：桥的内核（`console/backend/cmd/relay-call`）

在 **relay 上每次调用跑一次**的一次性客户端。它做的正是 console 健康探测做的事，只是
面向任意服务、且目标地址从 console 数据库解析而来：

1. 按名字在 console 库里解析目标 qube 的 `<ip>:8443`；
2. 用 console CA（库中加密存放）现签一张客户端证书——**复用 CA 是关键**：它绕开了独立
   `relay-client` 才需要、但尚不存在的 Relay 证书下发；
3. 拨远端 agent 的 mTLS gRPC，调一次服务，把**原始应答字节**写到 stdout 交给 qrexec 回传。

TLS 校验说明（回应安全评审）：`InsecureSkipVerify: true` 配合 `VerifyConnection` —— 校验
不是被关掉而是被**搬进回调**：`VerifyConnection` 拿 agent 证书对**钉死的 console CA 池**做完整
链校验，不链到该 CA 的一律拒。跳过的只是 SAN/主机名匹配，因为 agent 是按裸 IP 拨的、证书里
没有对应 SAN。这与 console 自身健康探测（`pingcheck`）**逐字一致**，含刻意用
`VerifyConnection`（而非会话复用时被跳过的 `VerifyPeerCertificate`）。

**真机结果（2026-07-20）：** 把 `relay-call` 交叉编译成 linux/amd64（CGO，因 sqlite），放到
`qubesair-console` qube 上执行 `relay-call remote-reg2 qubesair.Ping` →
`pong remote-reg2 <ts>`，exit 0。即：从 relay 位置用**每调用**客户端拨通了远端 agent。

> **owner 决定（2026-07）：走 b —— 独立 relay + 证书下发（§0.5）。** console-as-relay
> 作为**已在真机验证过的回退/对照**保留在 §0.3–0.4；`relay-call` 同时支持两种模式。

### 0.3 架构取舍：为什么 relay 是 console qube 本身

- **console qube 是唯一同时持有 CA 私钥与 qube→IP 映射的地方。** 让它当 relay，`relay-call`
  就能现签证书、零额外下发。独立 Relay（mgmt-jump / sys-relay-*）要当 gRPC relay，必须先有
  一套把 Relay 证书下发到它的设施（vault `qubesair.GetCredential` handler 或 salt 下发
  cert）——**这套此前不存在，§0.5 正是把它建起来**。
- **代价 / 待 owner 确认：** 现有 salt-config 与 policy 刻意把 relay 与 console **分开**（console
  持 CA + PVE token + terraform state，见 `salt/qubesair/README.md`「Why not just reuse
  mgmt-jump」）。console-as-relay 让本地 qube 能经 dom0 policy 触发 console 去拨 agent。该
  handler **不泄露 CA/token**，只代拨一次 RPC；但这确实是对既有设计的偏离。**迁移到独立
  Relay 的前提就是把上面那套证书下发建起来。** 这是一个需要 owner 拍板的架构决定，因此
  §0.4 的接线没有在无人监督下擅自落到 dom0。

### 0.4 待人工监督执行的接线（reversible，逐条给命令）

桥的内核（`relay-call`）已验证；把它接成「本地 qube 一条 qrexec 就到远端」还差三步，都
**可逆**，建议有人看着做（会动到最敏感的 console qube 的 root 与 dom0 policy）：

1. **在 console qube 上装 handler + 二进制**（root）：
   `/usr/local/bin/relay-call`（0755）、`/etc/qubes-rpc/qubesair.GrpcProxy`
   （本仓库 `relay/transport/qubesair.GrpcProxy`，0755）。正式做法走 salt（见 §7 TODO：
   新增一个 console-as-relay 的 state，像 console 二进制一样按 SHA 钉 `relay-call`）。
2. **dom0 policy**（新建 `/etc/qubes/policy.d/25-qubes-air-grpc.policy`，排在 `30-` 的兜底
   deny 之前）：
   ```
   qubesair.Ping        *  <caller>    remote-reg2       allow   # A: 触发 RemoteVM 改写
   qubesair.GrpcProxy   *  <caller>    qubesair-console  allow   # B: 改写后落到 console relay
   qubesair.GrpcProxy   *  @anyvm      @anyvm            deny
   ```
3. **改 remote-reg2 的属性**（可逆，改回 `qubesair.SSHProxy` 即恢复）：
   `qvm-prefs remote-reg2 transport_rpc qubesair.GrpcProxy`、
   `qvm-prefs remote-reg2 relayvm qubesair-console`。

   验收：从 `<caller>` 跑 `qrexec-client-vm remote-reg2 qubesair.Ping` → 应回 `pong`。

**未决 [C1]：** 改写后 B 段调用的 SOURCE 是原始本地 qube 还是 dom0/relay，R4.3 实测才能定
（本文 §5、`30-qubes-air.policy` 的 [C1] 注同此）。先按原始 caller 写；若被拒，据实测放宽。

### 0.5 已选方案（b）：独立 relay + CSR 证书下发

owner 选了让 relay 与持凭据的 console **分开**。难点一直是：relay 不持 CA,怎么拿到一张
console CA 签发的客户端证书,而**私钥不出 relay、console 也不进数据面**。答案是复用 §9 agent
bootstrap 那套 **CSR 流程**,只是信道改成本地 qrexec,且**不用 token**——relay 是本地可信
qube,dom0 已经不可伪造地告诉 console 谁在调用,以此认证。

**控制面（一次性 + 定时续期）:**

- **`cmd/issue-relay-cert`（console 侧）** —— 读 stdin 的 CSR,把 CN **钉死**成
  `relay-<caller>`(caller = `$QREXEC_REMOTE_DOMAIN`,dom0 保证不可伪造),用 console CA
  经 `pki.SignAgentCSR` 签名,输出 JSON。已单测:签自身身份通过,冒充别的 relay / agent 身份
  一律拒,非法 caller 名拒。
- **`console/qrexec/qubesair.IssueRelayCert`（console 的 /etc/qubes-rpc/）** —— 薄封装,
  source `secrets.env` 后 exec 上面的 CLI,把 caller 传进去。dom0 policy 只放行 relay qube。
- **`cmd/relay-bootstrap`（relay 侧,静态二进制,无 CGO）** —— 生成 P-256 密钥 + CSR(CN=
  `relay-<自身名>`,名字取自 `qubesdb-read /name`),经 `qrexec-client-vm <console>
  qubesair.IssueRelayCert` 发 CSR、收证书,原子写 `relay.key`(0600)/`relay.crt`/`ca.crt`。
  幂等,可由 systemd timer 定时续期。已单测:生成的 CSR 能被 CA 签、身份校验、密钥落盘 0600。

**数据面（每调用,console 不参与）:**

- **`cmd/relay-call` 新增 provisioned 模式** —— 给 `-cert/-key/-ca` 就**从磁盘加载**已下发
  证书、不碰数据库,端点由 `-addr` 传入(relay 的 transport handler 从 QubesDB 读)。mint
  模式(console-as-relay)保留,真机回归验证仍回 `pong`。
- `relay/transport/qubesair.GrpcProxy` 在独立 relay 上从 QubesDB `/remote/<target>` 取
  `<ip:port>`,以 `-cert/-key/-ca -addr` 调 `relay-call`(而非 console 上的 mint 模式)。

**端点下发:** dom0 在 RemoteVM 注册 / relay 开机时把 `qube→ip:port` 写进 relay 的 QubesDB
`/remote/<target>`(沿用 SSHProxy 的 `/remote/` 约定,只是值改成 `ip:port`),console 不进
数据面。

**已在真机跑通(2026-07-20,经 salt 管线部署 + 验收):**
`qrexec-client-vm remote-reg2 qubesair.Ping`(从 `qubesair-console` 发起)→ dom0 改写到独立
relay(mgmt-jump)→ `qubesair.GrpcProxy` → provisioned `relay-call` → 远端 agent → **`pong`**。
relay 持 console 下发的证书(`relay.key` 0600 `user:user`,私钥未出 relay),console 只签证书、
不进数据面。

salt 实现(qubes-salt-config):`mgmt.remotevm.grpc-console`(console 装 issuer + 服务)、
`.grpc-csr-relay`(relay 装 `relay-bootstrap`/`relay-call`/`qubesair.GrpcProxy` + 续期 timer,
全部 /rw 持久 + rc.local 每次开机重链并刷新证书)、`.grpc-policy`(dom0 的 25- policy)。

**真机踩到并已修的两个坑:**
- **改写附加空参 → 服务名尾随 `+`。** dom0 改写为 `transport_rpc+<target>+<service>+<原参>`,
  原参为空时留下尾随 `+`,handler 原来把「首个 + 之后全部」当 service 得到 `qubesair.Ping+`,
  agent 拒。改成取**第二个 `+` 字段**(`service="${rest%%+*}"`)。
- **改写后的调用不吃 policy 的 `user=root`,以 `user` 运行。** 因此把 relay 身份改成
  **`user` 属主**(relay-bootstrap 以 `user` 跑、`relay.key` 归 `user`),qrexec 服务(默认 `user`)
  才读得到,而不是依赖 `user=root`。

---

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
- SSHProxy 传输实体只在 shell/salt（`relay/transport/qubesair.SSHProxy`，以及 qubes-salt-config 的
  `salt/mgmt/remotevm/relay.sls`；本仓库的 `salt/qubes-air/remotevm/*` 已删除，见
  [salt/qubes-air/README.md](../salt/qubes-air/README.md)），**不在 Go 里**。gRPC 传输是一条**全新 Go 路径**。
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

## 7. 部署（Salt / dom0）

> **states 在 `qubes-salt-config` 仓库，不在本仓库。** 本仓库的 `salt/qubes-air/`
> 已退役，见 [salt/qubes-air/README.md](../salt/qubes-air/README.md)。

- **[已实现]** `salt/mgmt/remotevm/grpc-relay.sls`（qubes-salt-config）：在 relay 部署 gRPC client 单元（systemd），配置出站端点、证书路径（指向 vault 下发的内存挂载）、保活/重连参数。用 bind-dirs 持久化配置。配置读 `salt/config.jinja` 的 `remotevm.grpc`。
- **[已实现]** `salt/mgmt/remotevm/grpc-remote.sls`（qubes-salt-config）：在 Remote-Relay 部署 gRPC server 单元。
- **[已删除]** 旧 `relay.sls` / `autossh.sls` 的本仓库骨架已删除（而非标 DEPRECATED 保留）——留着就是第二份来源。SSHProxy 那条路的 states 在 qubes-salt-config 的 `salt/mgmt/remotevm/relay.sls`，与 `grpc-relay.sls` 二选一部署。
- **[TODO]** 真机验证。
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
