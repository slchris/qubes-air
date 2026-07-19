# RemoteVM Agent 设计：在非 Qubes 远端提供 qrexec 语义

> 状态：设计草案，尚未实现。
> 关联：[grpc-transport-design.md](grpc-transport-design.md)、[remotevm-alignment.md](remotevm-alignment.md)、[runbook-remotevm.md](runbook-remotevm.md)

## 1. 问题

`console/backend/internal/qrexec/client.go` 通过 `exec.Command("qrexec-client-vm", target, service)` 发起调用；
`salt/qubes-air/remotevm/files/reverse-qrexec-handler` 用同一个二进制处理反向回程。
[runbook-remotevm.md](runbook-remotevm.md) 曾声称远端主机「装上 `qrexec-client-vm` 即可」。

**这条路走不通，而且不是打包问题。**

`qrexec-client-vm` 来自 `qubes-core-agent-linux`，运行时依赖三样东西：

| 依赖 | 作用 | 在 PVE 上的普通 Debian 里 |
|---|---|---|
| `libvchan`（Xen vchan） | 域间共享内存通道 | 不存在（KVM 客户机，无 Xen） |
| `qubesdb` | qube 元数据与名字解析 | 不存在 |
| dom0 的 `qrexec-daemon` | 策略裁决与连接建立 | 不存在（远端无 dom0） |

vchan 是 **Xen 单机域间**通信原语。它不是网络协议，跨主机没有意义。
Fedora/Debian 官方源没有此包，Qubes 上游也不发通用源。

因此 [remotevm-alignment.md](remotevm-alignment.md) 中那句「非 Qubes 机器只要装上
`qrexec-client-vm` 即可接入」**是错的**——而它曾是整个远端架构的地基假设。
该文档与 [runbook-remotevm.md](runbook-remotevm.md) 已就地更正并指回本文。

## 2. 决策

**在远端实现一个 qrexec 兼容的 agent，底层走已有的 gRPC 双向流，不依赖 vchan。**

被否决的替代方案：

- **远端跑真 Qubes OS**（嵌套虚拟化）。qrexec 语义完整，但：Qubes 对硬件挑剔、嵌套虚拟化开销大，
  且**与已跑通的存算分离模块冲突**——不能把 cloud 模板 clone 成一台 Qubes 主机。远端只能带外装机，
  Console 退化为「只能管理、不能创建」。
- **砍掉 qrexec 抽象，直接调远端服务**。最省事，但丢掉了策略模型——而那正是 Qubes Air
  相对于「一堆云 VM」的核心差异。

**关键认识**：qrexec 的价值不在 vchan。vchan 只是 Qubes 在单机上的传输实现。
真正有价值的是**「哪个源 qube 能调目标 qube 的哪个服务」由 dom0 集中裁决**这套语义，
它与传输无关，完全可以跑在 gRPC 上。

## 3. 信任边界（本文档最重要的一节）

现有 `proto/relay_transport.proto` 的注释里写着：

> `LOCAL_TO_REMOTE = 1;  // 正向：本地 → 远端。本地 dom0 已校验；远端 dom0/policy 再校验后执行。`

**这句话在方案 B 下必须修正：远端没有 dom0。**

### 3.1 远端是不可信的

远端是一台跑在他人硬件（或至少是非 Qubes 隔离环境）上的 VM。它可能：

- 被攻破，agent 被替换成恶意实现
- 谎报执行结果
- 尝试发起任意反向调用

由此得出**一条不可让步的规则**：

> **远端 agent 内的任何检查都是纵深防御，不是安全边界。**
> 凡是本地需要保证的，必须由本地 dom0 policy 强制执行。

一个被攻破的远端可以跳过它自己的所有白名单。所以：
- agent 里的服务白名单 → 防止**误配置**和**误操作**，不防攻击者
- 本地 dom0 policy → **唯一**的授权边界

### 3.2 两个方向的风险完全不对称

**正向（本地 → 远端）：风险可控。**
本地 dom0 已裁决「允许调用」。远端拿到的只是一次它本来就被授权接收的调用。
远端作恶的上限是：不执行、或谎报结果。它**得不到**任何本来没有的权限。

**反向（远端 → 本地）：这是真正危险的方向。**
一个被攻破的远端会尝试调用本地服务——读 vault、执行本地命令、访问 dom0。
必须由本地 dom0 policy 严格限制。现有 `30-qubes-air.policy` 已有正确的骨架：

```
qubes.Gpg               *   @tag:relay    vault      ask default_target=vault
qubesair.VaultRead      *   @tag:relay    vault      ask default_target=vault
qubes.VMShell           *   @tag:relay    @anyvm     deny
*                       *   @tag:relay    @adminvm   deny
*                       *   @tag:relay    @anyvm     deny
```

注意 `ask` 而非 `allow`：**远端发起的凭据读取需要人确认**。这是刻意的，不要为了自动化改成 `allow`。

`docs/remotevm-selfcheck.md` 的 F2 项（远端经反向端口调白名单外服务须被拒）
就是这个边界的验收测试，必须在 agent 落地后仍然通过。

### 3.3 agent 不持有长期凭据

agent 只持有自己的 mTLS 客户端证书（用于建立出站连接，可撤销、可轮换）。
它**不**持有：vault 密钥、SSH 私钥、Proxmox 凭据。
需要凭据时走反向调用，由本地 vault 在 `ask` 确认后提供，用完即弃。

## 4. Agent 职责

`qubes-air-agent`，单个静态链接 Go 二进制，跑在远端。

**当前 `packer/scripts/install-agent.sh` 里那个 `exec sleep infinity` 占位符就是它的位置——
真实实现一行都还没写。**

### 4.1 出站建连（零入站）

agent 主动向 Console/Relay 的 gRPC 端点建立 `Tunnel` 长连接，
符合 `terraform/providers/proxmox/zero-inbound-firewall.md` 的零入站设计。
断线由 agent 侧重连（承担原 autossh 的保活角色），`KeepAlive` 帧探活。

### 4.2 服务发现与执行

沿用 Qubes 约定：服务实现放在 `/etc/qubes-rpc/<服务名>`。
收到 `RequestHeader` 后：

1. 校验 `qrexec_service` 名字合法（字符集与 `internal/qrexec.ValidArg` 一致）
2. 查 `/etc/qubes-rpc/<service>` 是否存在且可执行
3. 对照本地白名单（**纵深防御，非边界**，见 §3.1）
4. 执行，stdin 接 `stream_id=0`，stdout 回 `stream_id=1`，stderr 回 `stream_id=2`
5. 以 `EndOfStream` 收尾，失败则 `CallError`

### 4.3 提供 `qrexec-client-vm` 兼容入口

远端上的服务脚本可能需要发起**反向**调用（例如取本地 vault 凭据）。
为此 agent 额外提供一个二进制 `qrexec-client-vm`（同名、同 argv、同 stdio 语义），
它不走 vchan，而是把调用编码成 `Direction=REMOTE_TO_LOCAL` 的帧塞进已有 Tunnel。

**这样 `console/backend/internal/qrexec/client.go` 和 `reverse-qrexec-handler` 一行都不用改。**
接口兼容是刻意的：它让 qrexec 的传输实现成为可替换项。

## 5. 用户视角的完整链路

### 5.1 创建

```
UI 建 qube
  → 调度器选节点（见 internal/scheduler）
  → terraform clone 模板 901，挂回数据盘
  → cloud-init 安装并启动 qubes-air-agent，注入 mTLS 证书
  → agent 出站连到 Relay，Handshake 带上 remote_name
  → dom0-scripts/create-remotevm.sh 建立本地 RemoteVM 记录与 QubesDB 映射
```

注意 cloud-init 要注入的是**客户端证书**，不是 CA 私钥。证书签发应由 Console 在创建 qube 时完成，
每台一份、可单独撤销。

### 5.2 使用（对用户透明）

```
本地 qube 执行 qvm-copy-to-vm remote-dev file
   │
   ├─ dom0 policy 裁决（唯一授权边界）
   │
   ├─ 请求改写到 sys-relay-<zone>
   │
   ├─ gRPC 双向流（出站，长连接）
   │
   └─ 远端 qubes-air-agent 执行 /etc/qubes-rpc/qubes.Filecopy
```

用户感知上与本地 qube 无差别。**policy 始终由 dom0 裁决，绝不下放到远端。**

## 6. 需要修改的现有代码

| 位置 | 改动 |
|---|---|
| `proto/relay_transport.proto` | 修正 `LOCAL_TO_REMOTE` 注释：远端无 dom0，远端校验是纵深防御 |
| `docs/remotevm-alignment.md` | ✅ 已更正「装上 qrexec-client-vm 即可」这句错误论断 |
| `docs/runbook-remotevm.md` | ✅ 已更正；并指出 cloud-init/ansible 两条声称的安装路径都不成立 |
| `packer/scripts/install-agent.sh` | 用真 agent 替换 `sleep infinity` 占位符 |
| `salt/qubes-air/top.sls:20-22` | 路由指向 `remote-qube.base` / `remote-qube.agent` 两个**不存在**的 state，需补齐或删除 |
| `internal/qrexec/client.go` | **不改**——接口兼容是设计目标 |

## 7. 非目标

- **不实现完整 Qubes RPC 生态**。先支持项目实际用到的服务（`qubesair.Ping`、`qubesair.Status`、
  `qubesair.VaultRead`、`qubes.Gpg`），而非 `/etc/qubes-rpc/` 全集。
- **不在远端实现 policy 引擎**。远端无 dom0，也不该有裁决权（§3.1）。
- **不追求与 Qubes 上游的 wire 兼容**。我们兼容的是 `qrexec-client-vm` 的**命令行接口**，
  不是 vchan 协议。

## 8. 待决问题

1. **证书签发与轮换**：Console 自建 CA？每 qube 一证？撤销走 CRL 还是短期证书？
2. **agent 升级**：远端 agent 版本落后于 Console 时如何协商？`Handshake.protocol_version` 已预留字段但无策略。
3. **可观测性**：远端执行失败时，操作者从哪看到原因？`CallError` 已有 code/message，
   但需接入 jobs 审计表那类持久记录。
4. **`qubesair.Ping` 尚不存在**（`internal/service/qube_service.go` 的 `pingService` 常量已引用它）。
   agent 落地时需一并提供，否则 `CheckReachable` 永远失败。
