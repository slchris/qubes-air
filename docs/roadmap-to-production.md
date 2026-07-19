# 落地路线图：从可运行骨架到真机端到端

> 这份文档回答一个问题：**Qubes Air 要真正跑起来，从现在到"真机端到端跑通"，还差哪些步骤、依赖、风险？**
>
> 定位：分阶段的可执行落地路线，每阶段有**目标 / 前置依赖 / 关键步骤 / 验收标准 / 风险**。诚实标注现状，不粉饰。
>
> 配合阅读：[readme.md](../readme.md)（项目状态）、[docs/getting-started.md](getting-started.md)（上手四步）、[docs/architecture.md](architecture.md)（架构全貌）。

---

## 传输架构决策（重要）

**跨机传输从 SSHProxy 改为 gRPC 双向流。**

- **旧（现有骨架）**：官方 RemoteVM 的 `qubesair.SSHProxy` transport —— autossh 出站 + `ssh -R` 反向回程。见 [docs/runbook-remotevm.md](runbook-remotevm.md)（现作为过渡参考）。
- **新（目标）**：**gRPC 双向流**。本地 `sys-relay` 作为 gRPC 客户端主动**出站**建连到远端 Remote-Relay，建立一条长连接**双向流**；qrexec 请求转发和反向回程都复用这条流。零入站（只出站），家庭 NAT 后无需公网入站端口。

**为什么改**：gRPC 双向流用一条应用层长连接同时承载正向调用与反向回程，天然满足"出站建连 + 双向 + 零入站"；相比裸 SSH 隧道，有结构化的服务定义、认证、流控与可观测性，也便于控制台统一接入（控制台已是 Go + REST，gRPC 同栈）。

> **gRPC 传输 Go 实现已落地并接进业务（编译 + 单测 + mTLS 端到端集成测试通过），未真机验证。** proto、client/server、QrexecInvoker、ReverseHandler、证书经 vault 下发、config + main 装配均已实现并有测试；**`QubeService.CheckReachable` 已真正消费 Transport**（跨机 qrexec 探活，HTTP `GET /qubes/:id/reachable`）。**仍待做**：远端提供 `qubesair.Ping`、Salt/dom0 部署 states、**真机验证**、证书轮换对齐。详见 [grpc-transport-design.md](grpc-transport-design.md) 状态段。旧 SSHProxy 骨架（`salt/qubes-air/remotevm/*.sls`）作为过渡参考保留。

---

## 现状快照（起点）

| 能力 | 状态 | 说明 |
|---|---|---|
| 管理控制台（Go + Svelte） | ✅ 已实现 | `/api/v1` Bearer 认证、CORS 收敛、坏密钥 fail-fast、单测 + CI |
| 存算分离 Terraform（Proxmox） | ✅ 已实现 | compute/storage 拆分、`compute_running` 开关、数据盘 `prevent_destroy`、`terraform validate` 通过 |
| 控制台接真实 Suspend/Resume | ✅ 已实现 | 先落地再改状态、executor 可注入、qubeName 白名单 |
| 凭据 vault + 密钥轮换 | ✅ 已实现 | vault-cloud 无网、qrexec ask 下发不落盘、AES-256-GCM、多版本原子轮换 |
| 多机加密 state backend | ✅ 已实现 | OpenTofu 客户端加密（PBKDF2）+ S3/pg 双 backend |
| 跨机传输（SSHProxy 骨架） | 🟡 骨架 | dom0 RemoteVM 创建、修正后的 qrexec policy、autossh/ssh -R 配置存在，**未真机验证** |
| **gRPC 双向流传输** | 🟢 真机跑通（单机） | proto + client/server + invoker/反向/证书下发 + 集成测试；**真机验证：** 交叉编译 linux/amd64 在真 Qubes AppVM(mgmt-jump) 上跑通 mTLS Tunnel，relay-client 读 salt 渲染的 relay.env、与 gRPC server 建立 ESTABLISHED 连接。待跨两台机验证 + Salt 真机 apply |
| 真机端到端 | 🔴 未验证 | 从未在真实 Qubes R4.3 + 云上跑通完整链路 |
| GCP / AWS 真实资源 | 🔴 骨架 | 接口对齐，未实现真实 compute/storage |
| 监控 / 账单 | 🔴 占位 | 显式标注 placeholder，未接真实源 |

---

## 阶段总览

```
阶段 0  真机环境就绪         →  一台 Qubes R4.3 + 一处 Proxmox，本地三 qube 建好
阶段 T  gRPC 双向流传输       →  relay 出站建连、双向流承载调用与回程（替代 SSHProxy）
阶段 1  第一条真机端到端       →  本地 AppVM 调通远端 Qube 的一个 qrexec 服务
阶段 2  存算分离真机验证       →  Proxmox 上 suspend 释放计算 / resume 挂回数据盘
阶段 3  凭据链真机闭环         →  vault 按需下发 + 密钥轮换在真机跑通
阶段 4  多云真实资源          →  GCP、AWS 的 compute/storage 落地
阶段 5  可观测与账单          →  监控、真实成本源接入
```

每阶段可独立验收；阶段 T 是阻塞阶段 1 的关键路径。

---

## 阶段 0 · 真机环境就绪

**目标**：具备可执行真机验证的最小环境。

**前置依赖**：
- 一台兼容 Qubes OS 4.3 的机器（[HCL](https://www.qubes-os.org/hcl/)）
- 一处远端算力：Proxmox VE（本地机房/家用服务器，推荐首选，因为 Terraform 已是真机可 apply）
- 你自己的 Proxmox API token / SSH 密钥

**关键步骤**：
1. 装 Qubes OS 4.3，确认 `qvm-*`、Admin API 可用
2. 在 dom0 跑 `dom0-scripts/init-qubes-air.sh`，建 `mgmt-air`、`sys-relay`、`vault-cloud`（见 [getting-started.md](getting-started.md) ①）
3. 云凭据放进 `vault-cloud`（无网络，见 getting-started.md ②）
4. Proxmox 侧准备好一个可 API 访问的节点

**验收标准**：
- [ ] `mgmt-air` 能联网、能跑 `terraform`
- [ ] `vault-cloud` netvm=none（无网络），凭据文件就位
- [ ] dom0 policy 目录（`dom0-scripts/policy.d/`）已就位
- [ ] Proxmox API 从 mgmt-air 可达（凭据经 vault 下发）

**风险**：兼容硬件难找（Qubes 老问题）；Proxmox `path_in_datastore` 是 experimental，首测前须验证挂载语义。

---

## 阶段 T · gRPC 双向流传输（关键路径）

**目标**：用 gRPC 双向流替代 SSHProxy，作为 relay 的跨机传输。

**前置依赖**：阶段 0；Go 工具链（控制台已是 Go，同栈）。

**关键步骤**：
1. **[TODO] 定义 gRPC 服务**：`proto` 定义双向流 RPC（承载 qrexec 请求/响应帧、反向回程帧）。含请求 ID、方向、qrexec 服务名、payload。
2. **[TODO] Remote-Relay 服务端**：远端跑 gRPC server，接受本地 relay 的出站连接；把收到的 qrexec 请求转给远端 `qrexec-client-vm`，响应回写流。
3. **[TODO] 本地 relay 客户端**：`sys-relay` 跑 gRPC client，主动出站建连、维持长连接双向流；dom0 policy 改写后的 qrexec 请求经它编码进流；反向回程帧解码后经 qrexec 交回本地（policy C：ask）。
4. **[TODO] 认证**：双向流用 mTLS（客户端证书 + 服务端证书），证书/私钥存 vault-cloud，用时经 qrexec ask 下发（替代原 relay SSH 私钥）。
5. **[TODO] 断线重连 / 保活**：客户端出站重连、心跳保活（替代 autossh 的角色）。
6. **[TODO] Salt states**：新增 `salt/qubes-air/remotevm/grpc-*.sls` 部署 relay client / remote server 单元；旧 `autossh.sls`/`relay.sls` 标 DEPRECATED 保留。
7. **[TODO] dom0 policy**：确认 gRPC 路径下 policy A/B/C 语义不变（Relay 不得直达 dom0、破坏性操作 ask）。

**验收标准**：
- [ ] 本地 relay 能出站建立 gRPC 双向流到 Remote-Relay，长连接稳定
- [ ] 一个 qrexec 请求经双向流到达远端并拿到响应
- [ ] 反向回程帧能经同一条流回到本地、经 dom0 policy C（ask）确认
- [ ] 零入站验证：远端无需任何入站端口，家庭 NAT 后可用
- [ ] 断线后自动重连，不丢在途语义

**风险**：这是**从零实现的新传输层**，无真机难以完整测试；mTLS 证书轮换要和现有 vault 轮换机制对齐；要确保 gRPC 流的 qrexec 语义映射不破坏"两侧 dom0 各校验一次"的安全模型。

> **过渡策略**：阶段 T 未完成前，可先用 SSHProxy 骨架（[runbook-remotevm.md](runbook-remotevm.md)）跑通阶段 1 的真机端到端，验证除传输外的其余链路；gRPC 就绪后切换传输、复测。

---

## 阶段 1 · 第一条真机端到端

**目标**：本地 AppVM 里发起一次跨机 qrexec 调用，调通远端 Qube 的一个服务。

**前置依赖**：阶段 0；传输层（阶段 T 的 gRPC，或过渡期的 SSHProxy 骨架）。

**关键步骤**：
1. Terraform 在 Proxmox 建一台远端 VM（`terraform apply`）
2. dom0 建 RemoteVM 元数据 qube（`qvm-create` + `relayvm`/`transport_rpc`/`remote_name`）——**[待真机确认] R4.3 的确切用法**
3. 从本地 work qube 发起 `qrexec-client-vm remote-dev-1 <service>`
4. 经 dom0 policy A/B → relay → 传输 → 远端 → 远端 policy 再校验 → 远端 Qube 执行
5. 观察 dom0 改写后看到的调用来源 —— **[待真机确认]**

**验收标准**：
- [ ] 一次正向 qrexec 调用端到端调通、拿到结果
- [ ] 两侧 dom0 各弹一次/校验一次（policy 生效）
- [ ] Relay 不得直达 dom0（policy 拒绝验证）
- [ ] 反向调用（远端→本地 vault）触发 dom0 policy C 的 ask 弹窗

**风险**：`qvm-create` RemoteVM 确切用法、dom0 改写后的调用来源，都需 R4.3 实机确认（runbook 内已逐项标 `待真机确认`）。

---

## 阶段 2 · 存算分离真机验证

**目标**：在 Proxmox 上真机验证 suspend 释放计算 / resume 挂回数据盘。

**关键步骤**：
1. 控制台点「挂起」→ Terraform 销毁计算实例、数据盘 `prevent_destroy` 保留
2. 控制台点「恢复」→ 拉起新实例、挂回同一数据盘
3. 验证数据盘挂载语义（Proxmox `path_in_datastore` experimental）、storage VM 常关机

**验收标准**：
- [ ] 挂起后计算实例销毁、账单归零，数据盘留存
- [ ] 恢复后数据原样回来、远端 Qube 可再次接回本地
- [ ] 数据盘 LUKS 加密、密钥只在本地 vault（never-remote）

**风险**：`path_in_datastore` experimental，挂载语义需首测验证。

---

## 阶段 3 · 凭据链真机闭环

**目标**：凭据 vault 按需下发 + 密钥轮换在真机跑通。

**关键步骤**：
1. mgmt-air 建机时经 qrexec 向 vault-cloud 要云凭据（dom0 ask）→ 用完即弃、不落盘
2. gRPC mTLS 证书（阶段 T）经 vault 下发
3. 用 `crypto/scripts/rotate-keys.sh` / `cmd/rotate-key` 真机轮换，验证旧值失效、在途不受影响

**验收标准**：
- [ ] 云凭据经 qrexec ask 下发、内存使用、不落盘
- [ ] gRPC mTLS 证书轮换与 vault 轮换机制对齐
- [ ] `never-remote` 凭据（LUKS 卷密钥）在 policy 层确实永不下发到远端

---

## 阶段 4 · 多云真实资源

**目标**：GCP、AWS 的 compute/storage 从骨架变真实资源。

**关键步骤**：
1. **[TODO]** 实现 GCP compute/storage Terraform 真实资源（当前是接口对齐骨架）
2. **[TODO]** 实现 AWS compute/storage 真实资源
3. 每个云 Zone 复用同一存算分离模型（compute_running 开关 + 数据盘 prevent_destroy）
4. 诚实边界：无机密计算（如 AMD SEV-SNP）时云运营商仍能读 VM 内存 → 云 Zone 只承载低敏感/一次性负载

**验收标准**：
- [ ] GCP 上能建/挂起/恢复远程 Qube，端到端接回本地
- [ ] AWS 同上
- [ ] 三云 Zone 在控制台统一管理、状态一致

---

## 阶段 5 · 可观测与账单

**目标**：把监控、真实成本源从占位变真实。

**关键步骤**：
1. **[TODO]** 成本页接真实云账单 API（当前是占位估算）
2. **[TODO]** 监控接真实指标源
3. **[TODO]** 前端支持发送 Bearer token（控制台 API 已有认证，前端待接）
4. **[蓝图]** gRPC（含传输）+ OAuth2/mTLS 更强身份 + 多租户 RBAC

**验收标准**：
- [ ] 成本页显示真实机时/存储账单，去掉"占位"标注
- [ ] 监控显示真实远程 Qube 状态与资源用量

---

## 关键路径与优先级

```
阶段 0 ──┬──→ 阶段 T（gRPC 传输，关键路径）──→ 阶段 1（端到端）──→ 阶段 2/3（并行）
         └──→ （过渡：SSHProxy 骨架先跑阶段 1，验证非传输链路）
                                                              阶段 4（多云）──→ 阶段 5（可观测）
```

- **最优先**：阶段 0 + 阶段 T + 阶段 1 —— 这三步决定"能不能真的跑通一条链路"
- **过渡加速**：阶段 T 未完成时，用 SSHProxy 骨架先验证阶段 1 的其余部分，降低串行阻塞
- **可并行**：阶段 2、3 在阶段 1 通后可并行；阶段 4、5 靠后

---

## 诚实说明

- 本路线图的**阶段 T（gRPC 传输）是从零实现**，工作量最大、风险最高，且无真机难以完整验证。
- 现有 SSHProxy 骨架**未经真机验证**，作为过渡参考保留，不代表已可用。
- 所有 `[TODO]` / `[蓝图]` 标记项均**尚未实现**。
- 端到端从未在真实环境跑通 —— 这份路线图是"怎么走到那一步"的计划，不是"已经走到了"的记录。
