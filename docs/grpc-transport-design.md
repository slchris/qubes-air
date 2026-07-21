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

### 0.6 开箱即用:端点自动下发 + 默认走 gRPC(2026-07,真机验收)

让新建 qube 无需任何手工步骤就能走这条链路:

- **端点自动下发(A)。** relay 不再靠手工 `qubesdb-write`。console 开一个只读服务
  `qubesair.RemoteEndpoints`(`cmd/list-endpoints`,只吐 `<名> <ip:port>`、不碰凭据),relay
  定时(`*:0/2`)+ 开机从 console 拉一次,写进**自己的** QubesDB `/remote-endpoint/<名>`
  (实测 VM 可写自身 QubesDB,`user` 亦可)。`qubesair.GrpcProxy` 从该键解析地址——console
  因此**不进每次调用的数据面**,只在刷新时被拉。重启自愈,新 qube 最多 2 分钟可达。
  端点键用专用的 `/remote-endpoint/`,与 SSHProxy 复用的 `/remote/`(值为远端原始名)区分。
- **默认走 gRPC(B)。** `config.jinja` 的 `remotevm.transport_rpc` 改成 `qubesair.GrpcProxy`;
  dom0 的 `qubesair.RegisterRemoteVM` 服务据此把新注册的 RemoteVM 直接设成
  `transport_rpc=qubesair.GrpcProxy` + `relayvm=<relay>`。真机验证:`register` 一个新名字,
  输出即 `... via mgmt-jump (qubesair.GrpcProxy)`。

新增 salt:`grpc-console` 加装 `list-endpoints` + `qubesair.RemoteEndpoints`;`grpc-csr-relay`
加 `refresh-endpoints.sh` + `endpoints.timer`(+ boot.sh 开机拉一次);`grpc-policy` 放行
relay→console 的 `qubesair.RemoteEndpoints`。

### 0.7 能干活:`qubesair.Exec`(2026-07,真机验收)

机器从「能探活」变成「能干活」。agent 加了 `qubesair.Exec`:stdin 收命令(像
`qubes.VMShell`),跑完回合并的 stdout+stderr;因为 invoker 在服务非零退出时会丢弃 stdout,
Exec **恒退 0**、把命令真实退出码以 `[qubesair.Exec: exit=N]` trailer 报告。经 unit 的
`--allow "${QUBESAIR_ALLOW}"`(默认 `qubesair.Ping,qubesair.Exec`)开启;agent 现在**空
allowlist 即拒绝启动**(空清单被 invoker 当放行一切)。dom0 policy 对 `qubesair.Exec` 默认
`ask`(每次远端执行弹窗确认),`remotevm.grpc.csr.exec_action=allow` 可对可信 caller 关掉弹窗。

**端到端(新机器,零手工):** console 建 `remote-exec1` → 自动注册 RemoteVM(GrpcProxy + 打
`remote-zone` tag)→ relay 自动学到端点 → `@tag:remote-zone` policy 覆盖 → 本地 qube
`qrexec-client-vm remote-exec1 qubesair.Ping` 回 pong;`qubesair.Exec` 经 relay 直连实测
`uname -a; id` 回真实输出、非零退出保留输出并报 `exit=7`。

RemoteVM 现在由 `qubesair.RegisterRemoteVM` 打 `remote-zone` tag,dom0 policy 用
`@tag:remote-zone` 覆盖所有 RemoteVM,新 qube 无需改 policy。

### 0.8 传文件:`qubesair.FileCopy`(2026-07,真机验收)

双向传文件。协议全走 stdin(qrexec 服务参数装不下 `/` 路径):头一行 `push <绝对路径>` 或
`pull <绝对路径>`,push 后面跟内容。push 原子写(temp+rename)回 `OK push <字节> <sha256>
<路径>`,pull 回文件内容。受 agent 一次应答 16 MiB / 2 分钟上限,适合配置/脚本类文件。默认经
`QUBESAIR_ALLOW` 开启,dom0 policy 默认 `ask`(可用 `filecopy_action` 调)。

**真机验收(remote-fc1):** push 一个含中文的文件回 `OK push 40 da7dc857…`,pull 回来
byte-exact、sha 一致。

### 0.9 GUI 转发:`qubesair.ConnectTCP`(原始 TCP 隧道)

GUI(VNC/Xpra/X11)与 agent 的其它服务**结构不同**:它需要一条**持久双向 TCP 流**,而
agent 的 invoker 是缓冲式请求/应答,装不下。所以 GUI 不走 agent 的 gRPC,而是由 relay 直接
socat 到远端 IP:端口 —— qrexec 的 stdin/stdout 本身就是全双工管道。

- **`qubesair.ConnectTCP`(relay 侧)**:`qrexec-client-vm <relay> qubesair.ConnectTCP+<remote>+<port>`
  → 从端点表取 remote 的 IP,`socat` 双向接到 `IP:port`。**不经 RemoteVM 改写**(改写恒走
  GrpcProxy)。安全:端口白名单(默认只放 5900-5910 / 10000-10010,防止借道 relay 连 agent
  的 8443、ssh 等),remote 必须在端点表里,dom0 policy 把关调用方。
- **真机验收**:
  - 隧道承载任意 TCP:在远端起 `python3 -m http.server 10001`,本地 qube 经隧道 `GET /`
    回完整 HTTP 200;非白名单端口 22 被**拒(126)**。
  - **GUI 端到端(remote-gui1)**:远端装 `xvfb x11vnc xterm imagemagick`(经 Exec,详见
    §0.10 的沙箱修复),起 `Xvfb :99` + `xterm` + `x11vnc :5900`。① 本地 qube 经
    `qubesair.ConnectTCP+remote-gui1+5900` 读到 VNC 的 **`RFB 003.008` 握手**(交互传输通);
    ② 经 Exec 用 `import` 截 `:99` 桌面、`base64` 回拉,本地解出一张真实 PNG(xterm 显示远端
    host/内核 + xmessage 弹窗)—— 渲染与取帧端到端打通。用户接真 VNC/Xpra 客户端就是把本地
    端口 socat 桥到这条隧道(见上 recipe)。
    坑:x11vnc 要**前台**跑(别 `-bg`,否则 systemd 瞬态单元主进程退出、`--collect` 连带把
    Xvfb 都收了);VNC 是**服务器先说话**,客户端连上别急着关 stdin(`sleep N |` 保持), 否则
    socat 在 banner 到达前就关了。

**接客户端(recipe,务必带鉴权):**
1. 远端起 GUI server(经 `qubesair.Exec`)。**必须有鉴权**:
   - x11vnc:先 `x11vnc -storepasswd '<pw>' /root/.vnc/passwd`,再
     `x11vnc -display :99 -forever -shared -rfbauth /root/.vnc/passwd -rfbport 5900`;
   - 或 Xpra 带 TLS + 口令:`xpra start --bind-tcp=0.0.0.0:10000,auth=password --ssl=on ...`。
2. 本地 qube 把一个本地端口桥到隧道:
   `socat TCP-LISTEN:5900,fork,bind=127.0.0.1 EXEC:'qrexec-client-vm <relay> qubesair.ConnectTCP+remote-gui1+5900'`
3. `vncviewer 127.0.0.1:5900`(输入口令)/ 浏览器开 Xpra 的 HTML5 端口即见远端窗口。

**⚠️ 安全模型(重要,别照搬 -nopw):**
- ConnectTCP 隧道本身有三层门控(端口白名单 + 端点表 IP + dom0 policy 管调用方),但它做的是
  relay → 远端 `IP:port` 的**原始 TCP**,所以远端那个 GUI 端口是**暴露在 LAN(10.31.0.0/24)
  上**的。
- **不能靠源 IP 防火墙收口**:relay/console 等 qube 的出站流量都经 Qubes netvm 链 NAT,远端看到
  的源地址都塌成同一个网关 IP,分不出是不是 relay。所以**唯一有效的边界是 GUI server 自身的
  鉴权**——VNC 口令(`-rfbauth`,RFB 安全类型 2)/ Xpra 口令 + TLS。`-nopw` 等于无密码敞开,
  真机验证过用 `-rfbauth` 后 RFB 握手回 `… 01 02`(要求 VNC Auth),这才是最低要求。
- **更彻底的做法(推荐后续):** 把 GUI 走 agent 的 mTLS 通道(远端 GUI server 只绑
  `localhost`,agent 里加一个把 gRPC 流代理到 `127.0.0.1:port` 的**流式** handler),这样 LAN
  上根本没有暴露端口、且认证+加密复用 agent 的 mTLS。代价是要给传输层加**流式**支持(现在的
  invoker 是缓冲式请求/应答)。见 §0.11。

至此这些机器能:探活(Ping)、跑命令(Exec)、传文件(FileCopy)、转 GUI/任意 TCP
(ConnectTCP)——覆盖了「用远端机器」的绝大多数场景。

### 0.10 关键修正:命令/文件操作要绕开 agent 沙箱(systemd-run)

一开始 `apt install` 报 `Read-only file system`(dpkg 写 `/usr` 失败),我**误判成内网镜像
快照不完整** —— 实际根因是 **agent unit 的 systemd 沙箱**:`ProtectSystem=full` 让 `/usr`、
`/etc` 对 agent **及其子进程**只读,`PrivateTmp` 给了私有 `/tmp`,`ProtectHome` 挡了
`/home`。而 `qubesair.Exec`/`qubesair.FileCopy` 跑的东西正是 agent 的子进程,继承了这套沙箱。
连带后果:一次 `apt install` 下载后在只读 `/usr` 上 unpack 失败,把 dpkg 状态搞成半损坏,
后续 apt 才报出 `gcc-12-base` 依赖冲突(是结果,不是原因)。

**修法:`Exec`/`FileCopy` 的 payload 经 `systemd-run --pipe --wait --collect --quiet` 交给
PID 1 在宿主命名空间跑**,绕开沙箱 —— 这是用户自己的机器,命令本就该有完整访问权;agent
自身仍保持 `ProtectSystem=full` 等加固,只有它启动的命令不受限。真机验证:`systemd-run` 版
可写 `/usr`、可读写真实 `/tmp`,`dpkg --configure -a && apt-get -f install` 修好半损坏状态后
`apt install cowsay` 成功(tux 打印 "it works")。退出码经 `systemd-run --wait` 正确透传。

(已损坏的 qube 需一次性 `dpkg --configure -a; apt-get -f -y install` 修复;用修好的 Exec 从
干净模板新建的 qube 首次 apt 即在可写 `/usr` 上跑,不会再损坏。)

### 0.11 GUI/流式走 agent mTLS(消除 LAN 暴露端口)—— 已实现并真机验证

§0.9 的 ConnectTCP 早期版把远端端口暴露在 LAN 上(靠 GUI server 口令兜底)。现在流式
(GUI/DB/任意 TCP)**改走 agent 的 mTLS 通道**,LAN 上零暴露端口:

- 远端 GUI server 只绑 `127.0.0.1:<port>`(不暴露)。
- **agent server 加了流式 handler**:请求服务名形如 `qubesair.StreamTCP+<port>` 时,不走缓冲式
  invoker,而是 dial `127.0.0.1:<port>`,把 gRPC `Tunnel` 的 DataChunk 帧与这条 TCP 双向对拷
  (复用协议里已有的 request_id/DataChunk/EOS)。端口在**白名单**内(GUI 段),白名单外的端口
  **直接拒**、绝不落到 qrexec。
- **client 加了 `CallStream`**(不缓冲、边收边转);**relay-call 加了 `-stream` 模式**(把
  stdin/stdout 直接对接);**`qubesair.ConnectTCP` 改成 exec `relay-call -stream`** 到 agent 的
  mTLS 端点,不再 socat 到 LAN。
- 认证/加密复用 agent mTLS(relay 持 console 下发的证书),端口白名单**双处**(handler + agent)。

**真机验收(remote-gui2,stream agent):** 远端 `x11vnc -localhost -rfbauth`(只绑 127.0.0.1
+ 口令)。① 从本地 qube 经 `qubesair.ConnectTCP+remote-gui2+5900` 读到 VNC **`RFB 003.008`
握手**(GUI 经 mTLS 通了);② relay 直接 socat 到 `remote-gui2:5900/10001` **被拒(rc=1)** ——
LAN 上确实零暴露端口;③ `relay-call -stream` 直连回完整 HTTP 响应(流式对拷正确)。单测:
in-process CallStream ↔ StreamTCP handler ↔ 回环 echo 双向对通,白名单外端口被拒,缓冲式
Ping/Exec/FileCopy 不受影响。

交互式 GUI 会话是长连接:`-timeout` 给到 12h,VNC/Xpra 客户端经本地 socat 桥(§0.9 recipe)接
`qubesair.ConnectTCP`,server 只绑 localhost + 自带口令 —— 双重保险。

### 0.12 存算分离真机闭环:数据盘持久 + resume 可达 + /data 自动挂载(2026-07,真机验收)

计算与存储分离(`terraform/modules/remote-qube-base/providers/proxmox`):**storage VM**
(`<name>-storage`,带 `lifecycle.prevent_destroy`)独占 data 盘;**compute VM**(`count =
compute_running ? 1 : 0`)一次性,root 盘随实例销毁重建,data 盘经 `path_in_datastore` 挂回同一块。
release/suspend → compute 销毁、storage+data 盘留存;resume → compute 重建、挂回同一块 data 盘。

**① 数据盘持久(真机验收,remote-gui2):** 在 data 盘写标记后 release,再 resume。Proxmox 侧:
compute VM 117 被销毁 → resume 建出**新的 compute VM 122**(全新 ephemeral root `vm-122-disk-0`),
其 `scsi1` 仍是 `vm-115-disk-0`(storage VM 115 的盘);guest 侧 data 盘 UUID 与标记文件**逐字节不变**。
存算分离在硬件上成立。

**② 发现并修复:resume 后 qube 不可达(stale IP)。** compute VM 重建换了新 MAC → 新 DHCP 租约 →
**IP 变了**(实测 .150 → .129/.206),但 console 一直用旧 IP:`agenthealth.refreshAddress` 只在
`ip_address==""` 时才回读 terraform,于是老地址被**永远相信**,console 一直往死地址探测/bootstrap,
agent 卡在 bootstrap-pending、qube 永不可达。**修复**:compute 被销毁的动作(suspend/release/destroy,
`service.ComputeDestroyingAction`)后**清空 `ip_address`** —— 异步完成钩子(`cmd/server` `makeCompletionHook`)
与内联路径(`qube_service`)两处,resume 时健康监控自然回读新地址。

**③ 数据盘自动挂载(cloud-init)。** 之前 data 盘 reattach 回来是**空白未挂载**的,写在普通根盘的数据
resume 就丢。新增 `qubesair-mount-data` 脚本 + `qubes-air-data.service`(oneshot,每次启动都跑):按
**稳定的 scsi 地址** `/dev/disk/by-path/*-scsi-0:0:0:1` 解析 data 盘(**不认 `/dev/sdX`** —— 本硬件上
OS 盘会是 `sdb`,盘符不稳定),**仅当空白**才 `mkfs.ext4 -L qubesair-data`(在 resume 上永不重格),
`mount /data`。见 `console/backend/internal/service/cloudinit.go`。

**全链路真机闭环(remote-stage2,新 console 二进制 SHA `4b97e82b…`):** 建 qube → 首启 `/data`
**自动格式化+挂载**(`qubesair-data` 标记,`Result=success`)→ 写标记 → suspend(`ip_address` 被清空)→
resume → console **回读到新 IP**(.206 → .207)、**agent 变 healthy**(不再像修复前那样卡死)→ `/data`
**自动挂回、标记逐字节还在** —— 全程零手工。**且这次 data 盘回来是 `/dev/sda`(上次是 `/dev/sdb`)** ——
盘符真的在两次启动间翻了,而 by-path 解析照样挂对:这正是当初不认 `/dev/sdX` 的原因。单测:
`ComputeDestroyingAction`、`SuspendClearsStaleIP`(suspend 清 IP)、`DataDiskMountIsDelivered`(by-path 解析 +
mkfs 只在空白分支内 + runcmd 起用)。

> 旁证:`remote-dev-1` 在部署前就 `unreachable`,极可能是**修复前**某次 resume 留下的 stale-IP 受害者
> (console 记的 IP 已不是它现在的地址)。本次修复不会追溯修好它,但对它做一次 suspend→resume 即可自愈。

### 0.13 数据盘 LUKS 加密:密钥永不到远端(2026-07)

存算分离让数据在 release/resume 间存活,但盘上是**明文 ext4**,不可信的 Proxmox/Ceph 管理员能直接读
RBD 卷。0.13 把数据盘做成 **LUKS 容器**,而**密钥永不落到远端**。

**威胁边界(诚实):** 挡得住盘级读取 —— Ceph RBD、盘/备份被偷、cloud-init snippet(经 Proxmox API 可读)。
**挡不住** dump 活 VM 内存的 Proxmox root(密钥与明文都在 guest RAM)—— 那需要 SEV/TDX 机密计算,另记。

**密钥管理(`internal/pki/luks.go` + `internal/service/datakey.go`):** console 在自己的加密凭据库里
(与 CA 私钥同等保护,AES-256-GCM)存**一个** 256-bit master secret,**永不离开 console**。每个 qube 的
LUKS 口令 = `HKDF-SHA256(master, qube_id)` —— 确定性(同 id 永远同口令,所以 resume 出的新 compute VM
解得开同一个容器)、且互相隔离(拿到一个 qube 的派生口令推不出别人的)。

**下发(console → agent,`internal/service/agentunlock.go`):** bootstrap 在**首次 provision 和每次 resume**
都会跑(resume 重签身份),所以在 **bootstrap 成功后**(`BootstrapMonitor.WithAfterBootstrap` 钩子)console
经**已验证**的 mTLS(校验 agent 证书 CN=agent-<qube>,不同于 bootstrap 那条不校验的通道 —— 秘密绝不走不校验的通道)
调 agent 的 **`qubesair.UnlockData`**,把派生口令放 stdin 推过去。

**agent 端(`remote/qubes-rpc/qubesair.UnlockData`):** 口令只在 RAM(shell 变量 + 进程替换管道,**绝不落盘**);
按 by-path 定位数据盘;`cryptsetup isLuks` 判断:已是 LUKS 就 `luksOpen`,空白盘才 `luksFormat`(luks2,
pbkdf2 最小迭代 —— 口令本就是 256-bit,慢 KDF 无意义)。**只格式化真正空白的盘**,带非 LUKS 文件系统的盘一律
**拒绝、不覆盖**;幂等(已开已挂 → no-op);经 `systemd-run` 逃 agent 沙箱。cloud-init 对加密 qube 装 `cryptsetup`
且**不发**明文自动挂载(盘上无钥,开机挂载只会失败或误判空白),/data 由 console 解锁后才出现。

**加固 TODO:** agent 目前接受任何 CA 签的客户端证书,真正的授权是"持正确派生口令"(错口令 luksOpen 直接失败)。
唯一残余竞争:fleet 内某方在 console 之前对一块**空白**盘 `luksFormat`。把 `qubesair.UnlockData` 限定只接受
console 客户端证书的 CN 即可关闭 —— 待办。

**真机验收(remote-enc1,console SHA `e945cdac…` + agent `0.0.0+luks+f56b42e`):** 建加密 qube → 首启
**console 自动下发密钥**、agent `luksFormat`+`luksOpen`+`mkfs`+挂载,**零手工**:裸盘 `/dev/sdb` blkid=
**`crypto_LUKS`**,映射 LUKS2/aes-xts-plain64/512-bit,`/data` ← `/dev/mapper/qubesair-data`(ext4)。写标记
`qubes-air-luks-proof-Z9` → suspend(IP 清空)→ resume:新 compute VM、新 IP(.189→.184)、**console 用同一
派生密钥重开同一容器**(裸盘 UUID `e0e6d7d5` 不变、仍 `crypto_LUKS`,这次回来是 `/dev/sda` —— 盘符又翻了)、
`/data` 自动挂回、**标记逐字节还在**。全程裸盘只有密文。三层(IP 回读 + 存算分离 + LUKS)叠加成立。

单测:`DeriveDataKey`(确定性/隔离/拒弱输入)、`DataKeyManager`(master 只铸一次、跨实例稳定)、
`EncryptedQubeDeliversNoPlaintextMount`(加密 qube 不发明文挂载、装 cryptsetup)、`UnlockDataSkipsNonEncryptedQube`
(明文 qube 绝不派生/下发密钥)。

### 0.14 无缝桌面:remote 应用进启动菜单 + 单击即开(Xpra seamless,设计中)

目标(用户原话):在 Qubes 启动菜单里看到自己的 remote qube、看到它的应用、**点一下直接就打开**。

**为什么天然不通:** Qubes 启动菜单是**每 qube 的 appmenus**——dom0 用 `qubes.GetAppmenus`
(`qvm-sync-appmenus`)问 qube 要 `.desktop`,生成菜单项;点击跑 `qvm-run <vm> <app>`,窗口由
`qubes-guid` 经**本地 Xen 共享内存**渲染。RemoteVM 有两处断:(1) 远端是普通 Debian 云镜像,没有
qubes-core-agent,也就没有 `qubes.GetAppmenus`;(2) 窗口没法走本地 Xen 共享内存,得**跨网络**。

**利好:** dom0 侧 RemoteVM 是 **R4.3 原生 class**(`qvm-create --class RemoteVM`,带 `relayvm`/
`transport_rpc`/`remote_name`,见 qubesair.RegisterRemoteVM),qrexec 调 `qubes.*` 会经 transport_rpc
改写到 relay→agent。所以只要 agent 会说 `qubes.GetAppmenus`/`qubes.StartApp`,Qubes 自己的菜单机器就能用。

**三阶段(全走已建的 mTLS 传输):**
- **Stage 1 应用进菜单:** agent 的 `qubes.GetAppmenus` 按**原生格式**(`<file>:Key=Value`,Exec 改写成
  `qubes-desktop-run <file>`,跳过 NoDisplay/Screensaver)枚举 `.desktop`。`qvm-sync-appmenus <remote>`
  即把远端应用填进启动菜单。远端无 qubesagent,故另配一个 `qubes-desktop-run` 垫片。
- **Stage 2 无缝 GUI(核心/最难):** 远端跑 **Xpra server**(每用户、只绑 localhost);`qubes.StartApp`
  把应用**启到该 server 的 DISPLAY 下**;本地跑 **Xpra client 的 seamless/rootless 模式**,经
  `qubesair.ConnectTCP`(§0.11 的 mTLS 流)连远端 Xpra 端口——于是**每个远端应用是本地一扇原生窗口**
  (剪贴板/缩放齐全),而非整桌面一块矩形。端口取 ConnectTCP 白名单段(10000-10010)。
- **Stage 3 单击即开:** 菜单项 = 「确保本地 Xpra client 已连 + 启动该 app」。dom0 侧 appmenu 同步 + policy
  (remote-zone 放行 GetAppmenus/StartApp),本地每-remote 的 Xpra-attach helper,agent-deb/cloud-init
  投放 Xpra server + app 服务。

**验证法:** 无头环境用 mgmt-jump(有显示的 Qubes AppVM)跑 Xpra client,`scrot` 截屏拉回来肉眼确认窗口。
选 Xpra 而非整桌面 VNC / 移植原生 qubes-gui-over-net:前者是唯一务实的「单应用窗口融进本地桌面」方案,
且复用现成传输。

**进度(2026-07-21,真机):**
- Stage 1 agent 核心 ✓:`qubes.GetAppmenus`(纯 bash,不依赖 gawk —— 原生版用 gawk-only 的
  BEGINFILE/ENDFILE,而 Debian 是 mawk,会**静默返回空菜单**;这是踩到的坑)在 remote-gui-dev 上正确
  按原生格式枚举 galculator/xterm/vim/xpra。另配 `remote/bin/qubes-desktop-run` 垫片。
- Stage 2 **全链路 ✓(真机,2026-07-21)**:两侧统一到 **xpra 6.5.1**(远端 bookworm 原本 v3.1.3、
  mgmt-jump trixie 默认源无 xpra —— 都换成 xpra.org 的 6.5.1)。远端跑 seamless server(`:100`,只绑
  `127.0.0.1:10000`),galculator 启入;mgmt-jump 用 `socat` 桥到 `relay-call -stream`(§0.11 的 mTLS 流)接
  xpra client `xpra attach`,DISPLAY=:0。**截屏确认:galculator 以一扇本地原生窗口出现在 mgmt-jump 桌面上**
  ——运行在远端、渲染在本地,全程走 agent mTLS,LAN 零暴露端口。xpra 版本在两侧一致是硬前提(v3↔v6 不通)。
- **交付踩坑(值得记):** 折腾很久其实是一颗**截断的 deb**——第一次下载超时,`xpra-common`/`xpra-server`
  只下了一部分(1.3MB/7.7MB),而「文件已存在就跳过」的逻辑把半截文件一路带了下去;镜像、FileCopy、
  mTLS 流都**忠实地**把这半截文件传到了远端(端到端 SHA 一致),apt 才在 data.tar 上炸。**更正:FileCopy 与
  流都是二进制干净的**(先前「FileCopy 损坏二进制」的判断是错的);教训是**按 Packages 的 Size 校验下载完整性**。
- Stage 1 + Stage 3 launch **原语真机 ✓(2026-07-21)**:agent 加 `qubes.GetAppmenus`/`qubes.StartApp` 进
  allowlist(远端 runtime 改 agent.env + 重启;**注意不能在 Exec 里重启 agent —— 会自杀**,那条 Exec 走的就是
  agent);dom0 policy(`grpc-policy.sls`)加两服务的 allow(remote-zone 段 + `@adminvm` 给 qvm-sync-appmenus)。
  经 mTLS 实测:`qubes.GetAppmenus` 列出全部应用;**`qubes.StartApp+debian-xterm` 经传输启动 → xterm(`debian@
  remote-gui-dev`)以本地原生窗口弹出**(截屏为证)——这正是「菜单点一下」调的原语。
  (旁注:relay(mgmt-jump)本身不是合法 caller,`qrexec-client-vm <remote> <svc>` 从 relay 发会 `Request refused`;
  真正的 caller 是 console/UI qube 和 dom0。)
- 待办:把 GetAppmenus/StartApp/qubes-desktop-run 收进 **agent-deb**(remote/qubes-rpc/ 自动打包 + service unit 的
  allowlist);dom0 跑 `qvm-sync-appmenus <remote>` 把应用**真进启动菜单**;投放 **Xpra server systemd unit** +
  本地每-remote 的 attach helper;把 **xpra 6.5.1 交付生产化**(远端 cloud-init / 本地 template);Stage 3 单击闭环。
- 待办:agent-deb/allowlist 收编 GetAppmenus/StartApp;dom0 policy + qvm-sync-appmenus(remote-zone);
  投放 Xpra server systemd unit;本地 template 装 xpra client + 每-remote attach helper;Stage 3 单击闭环。

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
