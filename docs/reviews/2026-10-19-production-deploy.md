# 2026-10-19 生产 console 部署与当前 HEAD 复验

基线：`origin/main` `b73cae1`（#73 completion 结果持久化、#75 cleanup 超时状态持久化、
#76 可配置 provision 等待）。本次把当前 HEAD 部署到实际运行的 `qubesair-console`，
并在同一集群上重新走了一遍生命周期。

## 部署内容

- console 二进制：`v0.1.0-218-gb73cae1`，revision `b73cae1`，`tree=clean`；
  前端为同一 revision 的 vite `dist`。
- agent 包：从同一 revision 构建并发布 `0.0.0+qab73cae1`，
  SHA-256 `01630fb9c63c535f161a688f2732e3da9d143bcdad148d7a76bd942ddc4b92fb`；
  console.env 的 `agent_package_url` / `_sha256` / `_version` 同步更新。
- `QUBES_AIR_ORCHESTRATOR_PROVISION_WAIT_SECONDS=600`。
- 部署前备份保留在 console qube 的 `/rw/config/qubesair/pre-deploy-20261019`
  （数据库 `.backup`、console.env、secrets.env、旧二进制、旧 web）。
- 数据库从 schema 2 迁移到 6；迁移后 1 个 zone、13 个既有 qube、56 条 job 均保留，
  既有 released qube 与证书未改动。

## 部署后验证

- `/health`：`status=healthy`、`revision=b73cae1`、`audit_trail=ok`。
- 部署前旧 console 上偶发的 `/api/v1/zones` 500（`context canceled`）在升级后不再复现。
- 生产 lifecycle（qube `qa-deploy-20261019-1`）：
  - provision 成功（约 4m13s，包含一次 30s 的 dom0 注册等待），agent healthy；
  - suspend 成功（约 6s）；
  - resume 成功（约 2m54s），旧证书以
    `compute instance rebuilt on resume from suspended` 撤销，新证书签发；
  - 再次 suspend 成功（约 4s）；
  - purge 的 destroy job 成功（约 3s），qube `purged`，PVE 匹配 VM 为 **0**，
    两条证书均为 revoked（resume rebuild / purge）。
- 测试后 RemoteVM `remote-qa-deploy-20261019-1` 已不存在。

## 发现

### 1. RemoteVM 注册调用慢，30s 超时会留下半配置的 shell

首次 register 时 dom0 service 创建了 `remote-qa-deploy-20261019-1`，但 console 的
qrexec 调用在 30s 后超时；shell 的 `transport_rpc` / `remote_name` 仍为空。
手工再次执行 `register` 是幂等的，补齐了属性；resume 时的 register 再次超时，
但 provision job 仍成功（注册失败只告警）。destroy 的 deregister 在 3s 内完成。
风险：超时的 register 可能留下一个地址壳存在但属性不完整的 RemoteVM。这属于 dom0
service/调用超时的部署问题，建议提高该调用的预算或在 service 侧把属性写入与创建
做成可重试的完整事务；本次未改 qubes-salt-config。

### 2. 一次性 qrexec HTTP 请求会取消 POST 的请求 context

用 `qa.py`（`qrexec-client-vm` + 一次 stdin 写入）发 POST 时，console 收到
`context canceled`（CA/credential 查询被取消），GET 正常。改用 mgmt-jump 上持久的
`qvm-connect-tcp 18080:qubesair-console:8080` 转发后，POST 与轮询均正常。这是测试
harness 的半关闭行为，不是 console 行为。

## 重要未完成：Salt pin 尚未更新

本次是手工部署；`qubes-salt-config` 仍钉着旧 console/agent 制品。再次执行
`qubesair.console` state 会用旧二进制覆盖当前 schema 6 数据库，而旧二进制无法打开
新 schema，导致 console 无法启动。**在下次 salt apply 之前，必须先把外部仓库的
`console_binary_*` / `console_web_*` / `agent_package_*` pin 更新到本次制品。**
本仓库不复制第二套部署入口，所以没有改那个仓库。

## 未覆盖

- Exec/FileCopy 真机正反例；
- 数据盘内容跨 suspend/resume 的持久性；
- 生产 console 的 Salt 状态化重放（pin 未更新，见上）；
- RemoteVM 注册的完整幂等行为（见发现 1）。

## 清理

测试 qube 已 purge，PVE 匹配 VM 为 0；RemoteVM 已删除；临时部署文件、测试脚本与
PVE ticket 已删除。生产 console 保持运行在新版本；部署前备份保留用于回滚。
