# 2026-10-19 生产 console 部署与当前 HEAD 复验

起始基线：`origin/main` `b73cae1`（#73 completion 结果持久化、#75 cleanup 超时状态持久化、
#76 可配置 provision 等待）。当天在同一生产 console 上又部署了三次：`87ef8c1`
（#78 RemoteVM 调用预算）、`66325fb`（#79 resume 可达性循环记录地址）和 `d4eb48c`
（#82 purge 状态文案）。最终运行版本为 `v0.1.0-230-gd4eb48c`，schema 6。

## 部署内容

- 首次部署（`b73cae1`）：
  - console 二进制 `v0.1.0-218-gb73cae1`，revision `b73cae1`，`tree=clean`；
    前端为同一 revision 的 vite `dist`。
  - agent 包从同一 revision 构建并发布 `0.0.0+qab73cae1`，
    SHA-256 `01630fb9c63c535f161a688f2732e3da9d143bcdad148d7a76bd942ddc4b92fb`；
    console.env 的 `agent_package_url` / `_sha256` / `_version` 同步更新。
  - `QUBES_AIR_ORCHESTRATOR_PROVISION_WAIT_SECONDS=600`。
  - 部署前备份保留在 console qube 的 `/rw/config/qubesair/pre-deploy-20261019`
    （数据库 `.backup`、console.env、secrets.env、旧二进制、旧 web）。
  - 数据库从 schema 2 迁移到 6；迁移后 1 个 zone、13 个既有 qube、56 条 job 均保留，
    既有 released qube 与证书未改动。
- 后续三次是二进制替换，未改前端、未改 agent 包、未迁移 schema：
  - `87ef8c1`：备份在 `/rw/config/qubesair/pre-deploy-regtimeout`
    （数据库 `.backup`、console.env、secrets.env、旧二进制）。
  - `66325fb`：备份在 `/rw/config/qubesair/pre-deploy-66325fb`（内容同上）。
    二进制经 artifact store 下发：
    `<artifact-store>/local/qubes-air/qubes-air-console-66325fb`，
    SHA-256 `89c1689ed8ec750666ae8073af031286f1487eea072266421484cf141e90bd1f`；
    console 内回读校验一致后执行 stop → install → start。
  - `d4eb48c`：备份在 `/rw/config/qubesair/pre-deploy-d4eb48c`（内容同上）；
    二进制 `<artifact-store>/local/qubes-air/qubes-air-console-d4eb48c`，
    SHA-256 `c7d2ae2a0c36e8f596b3f7d00bb099274bb103e626dec7c064abdee23601a9d3`，
    console 内回读一致后 stop → install → start。
  - #78/#79/#82 未修改 agent 源码，agent 包保持 `0.0.0+qab73cae1`。

## 部署后验证

- `/health`（最终）：`status=healthy`、`revision=d4eb48c`、`tree=clean`、
  `audit_trail=ok`。
- 部署前旧 console 上偶发的 `/api/v1/zones` 500（`context canceled`）在升级后不再复现。
- `b73cae1` 上的 lifecycle（qube `qa-deploy-20261019-1`）：
  - provision 成功（约 4m13s，包含一次 30s 的 dom0 注册等待），agent healthy；
  - suspend 成功（约 6s）；
  - resume 成功（约 2m54s），旧证书以
    `compute instance rebuilt on resume from suspended` 撤销，新证书签发；
  - 再次 suspend 成功（约 4s）；
  - purge 的 destroy job 成功（约 3s），qube `purged`，PVE 匹配 VM 为 **0**，
    两条证书均为 revoked（resume rebuild / purge）。
- `87ef8c1` 上的 lifecycle（qube `qa-deploy-2-20261019`）：
  - provision 成功（约 2m21s）；RemoteVM `remote-qa-deploy-2-20261019` 完整配置
    （class RemoteVM、relayvm mgmt-jump、transport qubesair.GrpcProxy、
    remote_name qa-deploy-2-20261019），**无需手工重试**，#78 生效；
  - suspend 成功；resume 成功（约 3m13s），证书在 07:51:30Z 签发，旧证书撤销；
  - 再次 suspend 成功；purge 后 qube `purged`，PVE 匹配 VM 为 **0**，
    RemoteVM 不存在，两条证书均为 revoked（resume rebuild / purge）。
- `66325fb` 上的 lifecycle（qube `qa-deploy-3-20261019`）：
  - provision 成功（约 2m22s），证书在 08:16:56Z 签发；
  - suspend 成功；resume 成功（约 1m36s），证书在 08:20:56Z 签发；resume 期间
    bootstrap 只在 23:19:56 记录一次 `has no IP address`，下一次 sweep 即完成签发；
  - 再次 suspend 成功；第二次 resume 成功（约 2m12s）。轮询 API 在 08:25:41Z
    已返回新地址的 `ip_address`（此时 job 仍 `running`、agent 仍
    `unreachable`），证书在 08:25:56Z 签发——地址在 bootstrap 之前已经写入
    qube 行（#79 的目标行为）；
  - suspend 成功；purge 后 qube `purged`，PVE 匹配 VM 为 **0**，RemoteVM 不存在，
    三条证书全部 revoked（两次 resume rebuild + purge）。

## Exec/FileCopy 与数据盘持久性（qa-deploy-4）

为覆盖剩余的真机缺口，临时在 console.env 中打开 agent 授权（测试后已还原）：

```
QUBES_AIR_AGENT_ALLOWED_SERVICES=qubesair.Ping,qubesair.UnlockData,qubesair.Exec,qubesair.FileCopy
QUBES_AIR_EXEC_ALLOW=/usr/bin/id:/usr/bin/cat:/usr/bin/echo:/usr/bin/sha256sum
QUBES_AIR_FILECOPY_ROOTS=/data:/tmp
```

重启 console 后 provision `qa-deploy-4-20261019`，确认这些值写入了 guest 的
`/etc/qubes-air/agent.env`。

- **dom0 policy 尚未授权高危服务**：从 console 和 mgmt-jump 调用
  `qrexec-client-vm remote-qa-deploy-4-20261019 qubesair.Exec` /
  `qubesair.FileCopy` 都返回 `Request refused`（rc 126）；只有从 console 发起的
  `qubesair.Ping` 成功。授权 Exec/FileCopy 需要改外部 `qubes-salt-config` 的
  dom0 policy，本次未改，因此 **qrexec transport 路径仍未验证**。
- **agent 侧逻辑**：用 PVE `qm guest exec` 在 guest 内加载 agent.env 后直接执行
  安装的 service 脚本：
  - `["/usr/bin/id"]` → `uid=0(root) gid=0(root) ...`，rc 0；
  - `["/bin/sh","-c","id"]` → `executable is not allowed`，rc 126；
  - FileCopy push `/data/qubes-air-check.txt` → `OK push 27 <sha256> ...`，rc 0；
  - FileCopy push `/etc/qubes-air-evil.txt` → `path outside allowed directories`，rc 126；
  - FileCopy pull 回读内容一致。
- **数据盘持久性**：写入 `/data/qubes-air-check.txt` 后 suspend（compute 销毁、
  storage VM 保留）→ resume；新 compute 中 `/data` 仍是
  `/dev/mapper/qubesair-data`（ext4），文件内容仍为
  `qubes-air-persist-20261019`。
- **resume 延迟异常**：这次 resume 用了 7m12s。agent 在 08:50:01Z 已启动并监听
  `*:8443`；08:56Z 从 PVE 节点连接该地址成功，但 console 在同一地址持续得到
  `connection refused`，直到约 08:55:5xZ 才恢复，证书在 08:55:53Z 签发。
  事后确认这个地址属于 K8s 基础设施节点，本次延迟与 qa-deploy-5 的地址冲突同源；
  600s 的 provision 等待吸收了延迟，但说明 resume 耗时可能由 console→guest
  可达性决定，而不是 bootstrap。

## purge 文案修复的部署与 qa-deploy-5（`d4eb48c`）

`d4eb48c` 部署后 `/health` 为 `v0.1.0-230-gd4eb48c`。为复验 #82 的 409 文案，
创建 `qa-deploy-5-20261019`；provision 在 09:16:31Z 开始，10m0s 后失败：

```
agent did not become reachable within 10m0s ... nothing is listening on
<guest-ip>:8443 ... connection refused (check ... whether the address is
already used by another host)
```

- guest 内 agent 从启动起就监听 `*:8443`，eth0 就是 DHCP 分配的地址；
- PVE 节点上 `ip neigh show <guest-ip>` 指向 `<conflicting-mac>`，而 VM 的
  `net0` MAC 是 `<vm-mac>`；占用 MAC 经 bridge FDB 追到物理上联口，说明占用者
  在 PVE 桥之外；
- console 与 PVE 节点发往 `<guest-ip>:8443` 的连接都被错误主机拒绝，没有到达 VM。

排查占用者：对该地址的 node_exporter（9100/tcp）读取 `node_uname_info` 与
`node_network_info`，确认它是一个 Debian 12（cloud 内核）Kubernetes 基础设施
节点，带 Calico 接口；扫描同网段 9100 端口后发现一个 **3 master + 5 worker**
的 K8s 集群，其静态地址段正好覆盖 DHCP 最近分配给测试 VM 的多个地址
（qa-deploy-3/4/5 先后拿到过属于 2 个 master 和 2 个 worker 的地址）。对新客户端做
DHCP DISCOVER，服务器返回的 offer 也是该 K8s 集群中一个 worker 的地址。

根因：**LAN 的 DHCP 池与 K8s 基础设施集群的静态地址段重叠**，且没有排除项或
保留。provider 现在只用 DHCP（静态 IP 池已移除），因此池里任何被其他主机静态
占用的地址都会被租给新 qube；console 的连接被真正的占用者拒绝，而 guest 内
agent 正常。qa-deploy-4 的 7m12s resume 与 qa-deploy-5 的 10 分钟失败都是这个
冲突的不同 ARP 时序表现。

修复方向在 DHCP 服务器侧（不是 console）：把全部静态基础设施地址段
（K8s 节点、Ceph、PVE 节点、网络服务地址）加入排除/保留，或把池限制在一个
专用未占用段；改完用 `nmap --script broadcast-dhcp-discover` 确认 offer 落在
静态段之外。本次未改 DHCP/路由器配置。

由于 qa-deploy-5 未能 provision，**#82 的 409 文案未做生产复验**（单元测试
覆盖错误类型、状态不变与 409 映射）。qube 已 purge，PVE、RemoteVM、证书均无
残留。

## 发现

### 1. RemoteVM 注册调用慢，30s 超时会留下半配置的 shell（已修，#78）

在 `b73cae1` 上首次 register 时 dom0 service 创建了 `remote-qa-deploy-20261019-1`，
但 console 的 qrexec 调用在 30s 后超时；shell 的 `transport_rpc` / `remote_name`
仍为空。手工再次执行 `register` 是幂等的，补齐了属性；resume 时的 register
再次超时，但 provision job 仍成功（注册失败只告警）。destroy 的 deregister 在 3s
内完成。风险：超时的 register 可能留下地址壳存在但属性不完整的 RemoteVM。

修复：`service.RemoteVMCallTimeout` 提高到 2 分钟，completion hook 预算提高到
150 秒（#78）。`87ef8c1` 上 provision 的注册在预算内完成，四个属性齐全，无需
手工重试；purge 后 shell 被删除。**强制超时下的幂等行为仍未单独演练。**

### 2. 一次性 qrexec HTTP 请求会取消 POST 的请求 context

用 `qa.py`（`qrexec-client-vm` + 一次 stdin 写入）发 POST 时，console 收到
`context canceled`（CA/credential 查询被取消），GET 正常。改用 mgmt-jump 上持久的
`qvm-connect-tcp 18080:qubesair-console:8080` 转发后，POST 与轮询均正常。这是测试
harness 的半关闭行为，不是 console 行为。

### 3. resume 时地址只存在于可达性循环内存中（已修，#79）

suspend 会清空 qube 的 `ip_address`（compute 已销毁，旧地址失效）。resume 的
可达性循环从 provider 读到新地址后只写进内存，bootstrap 调度器读的是数据库行，
因此只能等健康监控的另一次 provider 读取把地址落库。在 `b73cae1` 上观测到：
resume 于 07:48:19Z 开始，07:48:30Z 和 07:49:30Z 两次 bootstrap 都报
`has no IP address`，直到 07:51:30Z 才签发证书；而可达性探针在 07:50:56Z
已经能从 `<guest-ip>:8443` 的 TLS 错误中看出 agent 在那里。

修复：`NativeExecutor` 增加可选 `AddressRecorder`（接到 qube repository），
第一次从 provider 得到地址时用 5 秒独立超时写入 qube 行；写失败只记录日志，
不影响 job。修复后 `66325fb` 的第二次 resume 中，API 在 08:25:41Z 已返回新地址，
而 job 仍在 running、agent 仍 unreachable；bootstrap 在 08:25:56Z 完成签发。
这样 bootstrap 不再依赖健康监控的独立 sweep，强制 kill 或禁用它也不会让 resume
在地址已存在时卡住。

### 4. 对 running qube 执行 purge 的 409 文案未说明原因（已修）

对仍在 `running` 的 qube 直接调用 purge，API 返回
`409 {"error":"Conflict","message":"qube is busy: another operation is in progress"}`。
实际原因是 purge 只接受 released/suspended/stopped/error 状态，必须先 suspend。
该文案会让调用方以为存在并发操作而重试。

修复：`Purge` 在状态为 `running` 时返回
`ErrQubeNotPurgeable`（`qube is running; suspend or release it before purging`），
handler 映射为 409；其他状态仍由原子 claim 处理，因此真正的并发状态变化依旧返回
transition conflict。单元测试覆盖 running 拒绝、状态保持不变且不调用 provider。
修复随 `d4eb48c` 部署到生产；**409 文案的生产复验被 qa-deploy-5 的地址冲突阻塞**
（见上），单元测试通过。

## 重要未完成：Salt pin 尚未更新

四次都是手工部署；`qubes-salt-config` 仍钉着旧 console/agent 制品。再次执行
`qubesair.console` state 会用旧二进制覆盖当前 schema 6 数据库，而旧二进制无法打开
新 schema，导致 console 无法启动。**在下次 salt apply 之前，必须先把外部仓库的
`console_binary_*` pin 更新到 `d4eb48c` 的二进制；`console_web_*` 与
`agent_package_*` 保持 `b73cae1` 的制品即可（这两部分源码未变）。**
本仓库不复制第二套部署入口，所以没有改那个仓库。

## 未覆盖

- Exec/FileCopy 的 qrexec transport/dom0 policy 路径（agent 侧脚本已验证，见上）；
- 发现 4 的 409 文案生产复验（被 qa-deploy-5 的地址冲突阻塞）；
- 生产 console 的 Salt 状态化重放（pin 未更新，见上）；
- RemoteVM 注册在强制超时/中断下的幂等行为（见发现 1）。

## 清理

五个测试 qube（`qa-deploy-20261019-1`、`-2`、`-3`、`-4`、`-5`）均已 purge；PVE
按名称匹配的 VM 为 **0**；对应 RemoteVM 均不存在；有证书的 qube 均为 revoked
（`-5` 未完成 bootstrap，没有证书）。Exec/FileCopy 测试用的 console.env 授权已从
`/rw/config/qubesair/pre-exec-test/console.env` 还原。临时测试脚本、HTTP 转发和
PVE ticket 已清理。生产 console 保持运行在 `d4eb48c`；部署前备份保留用于回滚。
