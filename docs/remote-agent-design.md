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
**[已实现]** `console/backend/cmd/qubes-air-agent` + `internal/agent`。

分发方式已改：不再烤进镜像，而是打成 `qubes-air-agent_<version>_amd64.deb`
发到局域网 artifact store，由 cloud-init 下载 + 校验 SHA256 后安装
（`packer/scripts/install-agent.sh` 随之废弃，现在会直接报错退出）。
决定的代价与新增的信任依赖见 [bootstrap-design.md](bootstrap-design.md) §6。

> **连接方向的修正**：本文档 §4.1 原写「agent 主动出站建连」。实际实现中
> **agent 是 gRPC 服务端、本地 relay 拨入**，与既有传输层一致
> （`internal/transport/grpc/client.go` 是本地 relay，`server.go` 是远端）。
>
> 顺带发现 `terraform/providers/proxmox/zero-inbound-firewall.md` **自相矛盾**：
> 它同时声称「连接方向永远是本地 Relay 主动出站到 Remote-Relay」和「远端不需要开放
> 任何入站端口」「NAT 后的树莓派无需端口映射即可接入」。本地拨向远端就意味着远端在监听、
> 必须开入站；而 NAT 后的设备根本收不到入站连接。**当前实现没有做到远端零入站。**
> 要做到需要反转方向，那是传输层改动而非 agent 改动，且代价是**本地 relay 转而需要监听**
> ——把入站暴露面从不可信的远端挪到可信的本地，是否划算需要单独决策。

### 4.1 出站建连（零入站）

agent 主动向 Console/Relay 的 gRPC 端点建立 `Tunnel` 长连接，
符合 `terraform/providers/proxmox/zero-inbound-firewall.md` 的零入站设计。
断线由 agent 侧重连（承担原 autossh 的保活角色），`KeepAlive` 帧探活。

### 4.2 服务发现与执行

沿用 Qubes 约定：服务实现放在 `/etc/qubes-rpc/<服务名>`。
收到 `RequestHeader` 后：

1. 校验 `qrexec_service` 名字合法。**服务名来自网络并成为路径元素**，所以拒绝
   `/`、`\`、`..` 与空名——否则调用方可以指名主机上任意可执行文件
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
  → cloud-init 送 {公开 CA + 单次 token}, agent 首启进 bootstrap 模式
  → console 拨过去, agent 自生密钥交 CSR, 换回签名证书 (§9)
  → agent 出站连到 Relay，Handshake 带上 remote_name
  → dom0-scripts/create-remotevm.sh 建立本地 RemoteVM 记录与 QubesDB 映射
```

**[已实现]** 签发在 `internal/pki` + `internal/service/certs.go`：

- **CA 存在加密凭据库里**，不落盘 —— 它是本 Console 最有价值的密钥，谁拿到谁就能伪造舰队里任意
  agent 身份。存储时的描述字段刻意写得刺眼，让浏览凭据的人知道这是什么。
- **签发与注册在续期/bootstrap 里是原子配对的**。签了但没注册的证书连接时会被拒，
  注册了但没签的证书不存在。注意 `IssueFor`（建 qube 时）**不再签发证书**——它只铸一个
  bootstrap token；证书由 `BootstrapMonitor` 拨到 agent 后才产生（§9）。
- **绝不含 CA 私钥**，且 agent 证书是 **client-auth only、不能签名**，所以泄露一张 agent
  证书只损失那一台，不会获得签发能力。
- **CA 半存在时拒绝启动**：只剩一半意味着写入或删除不完整，此时铸造新 CA 会静默作废
  已签发的所有证书 —— 所以报错让人来看，而不是自作主张。

> **取舍已经消除（2026-07，真机验证过）。** 这里原来写着「密钥对由 Console 生成后经
> cloud-init 送到远端，因此任何持有 `VM.Config.Cloudinit` 的人都能读到私钥」，并说
> 「agent 自己生成密钥、提交 CSR 需要一条引导通道，目前不存在」。
> **那条通道现在存在了**：`internal/agent/bootstrap.go` + `service.AgentBootstrapper` +
> `BootstrapMonitor`。cloud-init 只送公开 CA + 单次 token，agent 首启自生密钥、只交 CSR，
> **私钥从不过网**。详见 docs/bootstrap-design.md §9（含真机验证记录 §9.5）。

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
| `packer/scripts/install-agent.sh` | ✅ 已废弃并改为报错退出——agent 改由 `.deb` 在开机时安装（bootstrap-design.md §6） |
| `salt/qubes-air/top.sls:20-22` | 路由指向 `remote-qube.base` / `remote-qube.agent` 两个**不存在**的 state，需补齐或删除 |
| `internal/qrexec/client.go` | **不改**——接口兼容是设计目标 |

## 7. 非目标

- **不实现完整 Qubes RPC 生态**。先支持项目实际用到的服务（`qubesair.Ping`、`qubesair.Status`、
  `qubesair.VaultRead`、`qubes.Gpg`），而非 `/etc/qubes-rpc/` 全集。
- **不在远端实现 policy 引擎**。远端无 dom0，也不该有裁决权（§3.1）。
- **不追求与 Qubes 上游的 wire 兼容**。我们兼容的是 `qrexec-client-vm` 的**命令行接口**，
  不是 vchan 协议。

## 8. 待决问题

1. ~~**agent 身份认证方式**~~ **[已决定：mTLS，且吊销已实现]**

   mTLS 在原理上确实更强：私钥**从不传输**，服务端从没见过它，因而不会被日志误记，
   也不会在 TLS 终止的代理处以明文出现。token 是持票即用凭据，这几点都相反。

   曾考虑改用 token 以简化吊销，最终否决。但采纳了当时那个判断里唯一站得住的部分：
   **「一个你没真正实现的 CRL，提供的安全性是零」**——所以吊销必须现在做，不能留作 TODO。

   **实现方式：证书指纹注册表（`agent_certs` 表），而非 CRL/OCSP。**
   关键在于本项目里**验证方、签发方、数据库拥有者是同一个进程**——CRL 需要发布、分发、
   并指望验证方取到新副本，而这里吊销只是一次行更新，下一次握手直接读到，
   没有任何可能静默失败的分发环节。

   两个刻意的设计点：
   - **CA 签名本身不等于准入**。未在注册表中的证书即使 CA 签过也拒绝，
     且「未注册」与「已吊销」是**不同的错误**——前者可能是攻击者持有一张签发过的证书，
     把它混进普通吊销的日志里就看不见了。
   - **吊销必须能触达已建立的连接**。隧道是长连接，只在握手时检查意味着被吊销的 agent
     会无限期保持既有连接。因此活跃隧道每分钟重新授权一次，失败即断开。

   注意一个诚实的限制：agent 的私钥必须以文件形式躺在**不可信远端**的磁盘上，
   所以 mTLS「密钥不可导出」这个最大优势在此架构下并不成立——远端被攻破时，
   拿到私钥和拿到 token 是一样的。选 mTLS 的理由是传输与服务端侧的暴露面更小，不是密钥更安全。
2. ~~**agent 升级协商**~~ **[已实现]** 关键是把**线协议版本**与**构建版本**分开：
   - `protocol_version` 参与兼容性判断，服务端用**支持集合**（而非相等判断）比对，
     使得同时支持 v1/v2 的构建可以让两侧按任意顺序升级，不需要 flag day
   - `build_version` **仅用于可观测性**，绝不参与判断——否则每次发版都会踢掉所有在网 agent
   - 版本不匹配时先发一个带 `PROTOCOL_MISMATCH` 码、指明双方版本的 `CallError` 再断开，
     而不是让对端只看到「流断了」（那和网络故障无法区分，会把人引去查防火墙）
3. **可观测性**（部分实现）：`CallError` 的错误码已固化为契约
   （`PROTOCOL_MISMATCH`/`DENIED`/`TIMEOUT`/`UNAVAILABLE`/`INTERNAL`，有测试钉住，
   因为调用方会 switch 它们），握手拒绝与连接建立都会记日志并带上对端 build 版本。
   **仍待做**：把远端调用失败接进 jobs 审计表，让操作者在 UI 上看到原因而不是只在日志里。
4. ~~**`qubesair.Ping` 尚不存在**~~ **[已实现]** 服务脚本在 `remote/qubes-rpc/qubesair.Ping`，
   由 agent 部署到远端 `/etc/qubes-rpc/`。契约（`CheckReachable` 依赖，改动即破坏调用方）：
   stdout 单行 `pong <remote_name> <unix_ts>`，exit 0 表示可达。
   刻意保持最小——探针回答的是「链路通不通」，不是「远端健不健康」；
   掺进磁盘/负载检查会让一次探测有多种失败原因，反而说不清哪里断了。
   深度检查应是独立的 `qubesair.Status`。
