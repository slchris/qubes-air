# 2026-10-19 生产 console 部署与当前 HEAD 复验

起始基线：`origin/main` `b73cae1`（#73 completion 结果持久化、#75 cleanup 超时状态持久化、
#76 可配置 provision 等待）。当天在同一生产 console 上又部署了两次：`87ef8c1`
（#78 RemoteVM 调用预算）和 `66325fb`（#79 resume 可达性循环记录地址）。最终运行版本为
`v0.1.0-224-g66325fb`，schema 6。

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
- 后续两次是二进制替换，未改前端、未改 agent 包、未迁移 schema：
  - `87ef8c1`：备份在 `/rw/config/qubesair/pre-deploy-regtimeout`
    （数据库 `.backup`、console.env、secrets.env、旧二进制）。
  - `66325fb`：备份在 `/rw/config/qubesair/pre-deploy-66325fb`（内容同上）。
    二进制经 artifact store 下发：
    `http://10.31.0.2/local/qubes-air/qubes-air-console-66325fb`，
    SHA-256 `89c1689ed8ec750666ae8073af031286f1487eea072266421484cf141e90bd1f`；
    console 内回读校验一致后执行 stop → install → start。
  - #78/#79 未修改 agent 源码，agent 包保持 `0.0.0+qab73cae1`。

## 部署后验证

- `/health`：`status=healthy`、`revision=66325fb`、`tree=clean`、`audit_trail=ok`。
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
    已返回 `ip_address=10.31.0.111`（此时 job 仍 `running`、agent 仍
    `unreachable`），证书在 08:25:56Z 签发——地址在 bootstrap 之前已经写入
    qube 行（#79 的目标行为）；
  - suspend 成功；purge 后 qube `purged`，PVE 匹配 VM 为 **0**，RemoteVM 不存在，
    三条证书全部 revoked（两次 resume rebuild + purge）。

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
已经能从 `10.31.0.108:8443` 的 TLS 错误中看出 agent 在那里。

修复：`NativeExecutor` 增加可选 `AddressRecorder`（接到 qube repository），
第一次从 provider 得到地址时用 5 秒独立超时写入 qube 行；写失败只记录日志，
不影响 job。修复后 `66325fb` 的第二次 resume 中，API 在 08:25:41Z 已返回新地址，
而 job 仍在 running、agent 仍 unreachable；bootstrap 在 08:25:56Z 完成签发。
这样 bootstrap 不再依赖健康监控的独立 sweep，强制 kill 或禁用它也不会让 resume
在地址已存在时卡住。

### 4. 对 running qube 执行 purge 的 409 文案未说明原因

对仍在 `running` 的 qube 直接调用 purge，API 返回
`409 {"error":"Conflict","message":"qube is busy: another operation is in progress"}`。
实际原因是 purge 只接受 released/suspended/stopped/error 状态，必须先 suspend。
该文案会让调用方以为存在并发操作而重试。建议在 `Purge` 前按状态返回明确错误
（例如 “suspend the qube before purging”）；本次未改。

## 重要未完成：Salt pin 尚未更新

三次都是手工部署；`qubes-salt-config` 仍钉着旧 console/agent 制品。再次执行
`qubesair.console` state 会用旧二进制覆盖当前 schema 6 数据库，而旧二进制无法打开
新 schema，导致 console 无法启动。**在下次 salt apply 之前，必须先把外部仓库的
`console_binary_*` pin 更新到 `66325fb` 的二进制；`console_web_*` 与
`agent_package_*` 保持 `b73cae1` 的制品即可（这两部分源码未变）。**
本仓库不复制第二套部署入口，所以没有改那个仓库。

## 未覆盖

- Exec/FileCopy 真机正反例；
- 数据盘内容跨 suspend/resume 的持久性；
- 生产 console 的 Salt 状态化重放（pin 未更新，见上）；
- RemoteVM 注册在强制超时/中断下的幂等行为（见发现 1）；
- 发现 4 的错误文案改进。

## 清理

三个测试 qube（`qa-deploy-20261019-1`、`-2`、`-3`）均已 purge；PVE 按名称匹配的
VM 为 **0**；对应 RemoteVM 均不存在；各 qube 的证书均为 revoked。临时测试脚本、
HTTP 转发和 PVE ticket 已清理。生产 console 保持运行在 `66325fb`；三个部署前备份
保留用于回滚。
