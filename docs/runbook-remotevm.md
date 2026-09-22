# RemoteVM 真机 runbook

适用范围：Qubes OS R4.3、独立 Relay、`qubesair.GrpcProxy`、远端 `qubes-air-agent`。

## 1. 部署来源

Qubes 模板、console、Relay、qrexec 服务和 dom0 policy 由
[qubes-salt-config](https://github.com/slchris/qubes-salt-config) 部署。

部署前确认配置至少包含：

- console AppVM 和 Relay 名称；
- console API token 与 32 字节加密密钥；
- Proxmox endpoint、credential 和目标 datastore/node；
- agent package URL、SHA256、version；
- RemoteVM transport 为 `qubesair.GrpcProxy`；
- Relay CSR、endpoint refresh 和 dom0 policy states 已启用。

## 2. 发布 agent

```bash
make release-agent VERSION=<version>
```

把命令输出的 `QUBES_AIR_AGENT_PACKAGE_URL`、`_SHA256` 和 `_VERSION` 原样写入 console
部署配置。先从 console 所在网络回读 URL 并校验 digest。

## 3. 创建基础设施对象

在 Web UI 中依次创建：

1. Infrastructure：Proxmox endpoint/节点信息；
2. Credential：与该 infrastructure 对应的最小权限凭据；
3. Zone：绑定 provider、infrastructure 和 credential；
4. Qube：选择模板、资源、Zone、是否加密数据盘。

创建后观察 Jobs 页的流式日志。成功的最低标准是 provider provision 完成（storage + compute），
随后 `agent_health` 在 bootstrap settle window 内变为 `healthy`。

## 4. 检查 bootstrap

Console 日志应能区分：

- 地址尚未可达；
- agent 未监听；
- TLS/证书错误；
- token 过期或已消费；
- agent package URL/SHA 不一致；
- bootstrap 成功但后续健康探测失败。

远端 guest 内确认：

```bash
systemctl status qubes-air-agent
journalctl -u qubes-air-agent -b
ss -lntp
```

不要把 agent 私钥复制回 console 排错。需要重建身份时，走新的 token/CSR 流程。

## 5. 检查 RemoteVM

在 dom0：

```bash
qvm-ls --class RemoteVM
qvm-prefs <remotevm> relayvm
qvm-prefs <remotevm> transport_rpc
qvm-prefs <remotevm> remote_name
qvm-tags <remotevm>
```

期望 `transport_rpc` 为 `qubesair.GrpcProxy`，并带用于 policy 的 `remote-zone` tag。RemoteVM
不可启动。

## 6. 检查 Relay

在 Relay：

```bash
systemctl --failed
qubesdb-read /remote-endpoint/<remote-name>
```

确认：

- Relay cert/key/CA 存在于部署约定的 `/rw` 目录；
- private key 权限只允许运行 handler 的用户读取；
- endpoint refresh timer 正常；
- QubesDB endpoint 是当前 agent 的 `ip:port`；

具体 unit 名随 qubes-salt-config 配置变化，以部署仓库渲染结果为准。

## 7. 端到端验收

从 policy 允许的本地 AppVM：

执行 Exec 前必须显式启用服务并允许 `/usr/bin/id`；FileCopy 也需启用服务及允许目标目录。
默认仅 Ping 可用。Exec 使用 JSON 参数列表并继承 agent 沙箱，配置与限制见[安全控制](security-controls.md)。

```bash
qrexec-client-vm <remotevm> qubesair.Ping

printf '%s\n' '["/usr/bin/id"]' |
  qrexec-client-vm <remotevm> qubesair.Exec
```

文件 push 示例：

```bash
{
  printf 'push /tmp/qubes-air-check.txt\n'
  printf 'hello from qrexec\n'
} | qrexec-client-vm <remotevm> qubesair.FileCopy
```

高风险服务弹出 `ask` 是预期行为。拒绝时调用必须失败且远端不执行。

## 8. 存算分离验收

通过控制台 start/stop（suspend/resume）流程操作。底层由 provider 适配器实现：

- suspend：停止并删除计算实例，`qube_infra` 保留 storage VM 与数据盘；
- resume：从模板重建计算实例，挂回同一数据盘。

确认 suspend 后持久数据盘仍在，resume 后挂回同一盘，agent 恢复 healthy，RemoteVM endpoint
更新，测试文件仍可读。数据盘有 `protected` 保护，purge 需显式确认。

## 9. 加密盘验收

对启用加密的 Qube，确认远端只看到已打开的 mapper，持久盘静态内容是 LUKS 密文；console
日志不得打印密钥。首次初始化与 resume 解锁都应通过 agent mTLS 服务完成。

旧盘（per-qube DEK 之前创建）首次解锁会经 `qubesair.RekeyData` 迁移到独立密钥：日志应出现
`migrated ... removed the legacy keyslot`，远端 `cryptsetup luksDump` 只剩一个 keyslot。
agent 需在 `QUBESAIR_ALLOW` 中允许该服务，否则迁移失败并在下次 resume 重试。逐项步骤见
[Proxmox 回归 runbook](runbook-qa01.md) §5。

## 10. 回滚与删除

- 单个 compute 故障：先 suspend/resume，不删除 data disk；
- agent 发布故障：恢复上一组 package URL/SHA/version 后重建 compute；
- RemoteVM 元数据错误：修正属性和 endpoint，不要先删云盘；
- 永久销毁：按[凭据销毁流程](credential-destruction.md)确认目标并调用 purge，再逐项核验
  资源、身份与备份策略；清理完成前需要保留有效的 provider 操作凭据。

## 11. agent 启动失败达到 start limit 后的恢复

包把 agent 安装为 `/lib/systemd/system/qubes-air-agent.service`（`packaging/agent-deb/README.md` 第 12 行）。
该单元自带启动速率限制：`StartLimitIntervalSec=300`、`StartLimitBurst=5`
（`packaging/agent-deb/qubes-air-agent.service`:10-11），并以 `Restart=on-failure`、
`RestartSec=5` 重启（`:32-33`）。agent 的启动失败路径全部在 `listen` 之前
（`console/backend/cmd/qubes-air-agent/main.go`:97-119：缺 mTLS 路径、身份文件不可用、
allowlist 为空），所以失败期间端口从未打开过。

**后果**：300 秒窗口内的第 5 次启动失败之后，systemd 不再自动重启这个单元；它停在
failed 状态，**本次开机内原始原因修好也不会自愈**，必须人工清除失败状态才会再次启动。
单元是 enable 的（`packaging/agent-deb/qubes-air-agent.service`:44-45 的
`[Install] WantedBy=multi-user.target`；`packaging/agent-deb/postinst`:28 调用
`systemctl enable`），所以下次开机会再尝试启动——但原因没修好只会再失败一遍，
不要把"重启一次"当恢复手段。

### 11.1 怎么看出来

| 层 | 现象 | 依据 |
|---|---|---|
| console | `agent_health=unreachable` 且 `agent_recovery=manual`，`agent_failing_since` 是这段失败的起点；Qubes 视图 Agent 列显示 `manual recovery` | `GET /api/v1/qubes`（同一字段在列表与详情里）；判定见 `console/backend/internal/models/qube.go`:137-153 |
| guest | `systemctl status qubes-air-agent` 是 failed/inactive 而不是 active | 与 §4 同一条命令 |
| guest | `journalctl -u qubes-air-agent -n 50` 末尾就是启动期的 `log.Fatalf` 文本 | `packaging/agent-deb/README.md`:38 列出的失败文本 |

console 的检测边界必须说清：**console 看不到单元状态**。它与 guest 之间只有 agent 的
mTLS 监听端口，没有命令通道，也没有告警推送。`agent_recovery=manual` 的含义是"这段失败
已经超过单元自身任何自动重启的预算"（最后一次探测与失败起点的间隔 ≥ 300 秒，
`console/backend/internal/models/qube.go`:127、`:150`），**不是**"单元命中了 start limit"：端口被过滤、包没装上、
地址错误都会给出同一个读数。是不是 start limit，只能在 guest 内看。

### 11.2 恢复

在 guest 内（与 §4 一样，需要远端 shell 访问；console 不会也不能替你做这一步）：

```bash
systemctl status qubes-air-agent
journalctl -u qubes-air-agent -b -n 50
```

按日志末尾的原因修复（缺文件、权限、`agent.env`、证书、allowlist 等），然后清除失败状态并启动：

```bash
systemctl reset-failed qubes-air-agent
systemctl start qubes-air-agent
```

这两条与 `packaging/agent-deb/README.md`:41-46 一致；`reset-failed` 是这里唯一能解除
start limit 锁定的动作，只 `start` 会被同一限制拒绝。

### 11.3 怎么确认恢复了

```bash
systemctl is-active qubes-air-agent
```

期望 `active`；`systemctl status` 不再显示 failed。然后在 console 上确认 Qubes 视图的
Agent 列在下一个探测周期内回到 `healthy`，`agent_recovery` 回到 `none`，并且
`agent_last_error` 与 `agent_failing_since` 都消失（成功探测会清空两者）。探测间隔见
[运行期默认值](runtime-defaults.md) §1.3（UD-9f 是周期探测间隔）。

更细的可选项：`systemctl show -p Result,NRestarts,ActiveState qubes-air-agent` 中
`Result` 为 `start-limit-hit` 是 systemd 对这次放弃的标记。这是 systemd 的标准属性，
但本仓库的验证环境没有 systemd，未在真机复验；`systemctl status` 与 `journalctl`
的输出已足够判定。

只改 console 侧不会让远端恢复。若要重建 compute（例如身份文件已损坏），按 §10 的流程，
并走 §4 的新 token/CSR 流程，不要把私钥复制回 console。

更细的检查项见[自检清单](remotevm-selfcheck.md)。
