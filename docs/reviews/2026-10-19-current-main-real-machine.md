# 2026-10-19 当前 main 真机测试：生命周期通过，agent 包与 completion 上下文问题已定位

起始基线：`origin/main` `4f16915`（#68–#71 已合并）。修复后测试二进制为 `0d23189`
（#73，已合并为 `24f8e0c`）。本次用当前 HEAD 构建 console 与 agent，在真实 Proxmox
集群和 Qubes 管理入口上执行；生产 console 未改动、未重启、未迁移。

## 测试方式

- 从当前 HEAD 构建 linux/amd64 console（最终为 `revision=0d23189`、`tree=clean`）和前端资源。
- 在 console qube 内启动**独立实例**：新数据库、新 agent identity 目录、新 web root、
  独立 API token/加密密钥、仅监听 loopback 的独立端口；未替换生产二进制、未迁移生产数据库。
- 通过 Qubes 管理入口（qrexec RemoteDebug → `qvm-run`）在 console qube 内执行 HTTP 请求；
  PVE 侧读操作经其 HTTPS API 核对。
- 当前 agent 从 `0d23189` 构建（`0.0.0+qa4f16915`），上传到 artifact store 并由 guest 下载。

## 通过的路径

1. **当前 console 启动与迁移**：空数据库启动后健康检查 `status=healthy`、`revision=0d23189`、
   `audit_trail=ok`，schema 迁移到 6。
2. **credential + zone 创建/连接**：用 PVE 凭据创建 operator credential 与 Proxmox zone；
   `POST /zones/:id/connect` 返回 `connected`。
3. **容量读取**：`GET /zones/:id/capacity` 返回六个节点全部 online 及 CPU/内存读数，
   证明当前 provider 的认证、TLS、节点解析和读路径在真实集群上工作。
4. **job 日志（M2-8 路径）**：provision 运行中 `GET /jobs/:id/log` 返回增量输出、
   `log_state=available`、`log_created_at` 已写入。日志包含真实的 VM 创建、模板克隆、
   磁盘扩容和 qemu-guest-agent 等待过程。
5. **失败路径与销毁**：早期 provision 在 agent 不可达后以明确错误失败（qube `error`，
   `agent_health=unreachable` 带原因）；对 `error` 状态的 qube 执行 purge 被接受，
   destroy job 成功，PVE 上 compute 与 storage 两个 VM 都被删除，qube 变为 `purged`。
   销毁日志明确记录了 `qmstop` 后对 compute 与 storage 两个 VM 的 `qmdestroy`。
6. **job 日志保留（M2-8）**：把一份已完成 job 的日志文件 mtime 调到 31 天前并重启实例；
   启动清理删除了该文件，job 行写入 `log_pruned_at`，API 返回
   `log_state=expired` 与对应 note；同日期的另一份日志仍为 `available`，未被误删。
7. **修复后的完整生命周期（M0-5）**：最终测试 qube 在 `0d23189` 上完成
   provision → suspend → resume → suspend → purge：
   - provision job 成功（约 2m49s），`agent_health=healthy`，首次证书签发；
   - suspend 成功（约 8s），compute 实例被销毁、数据盘保留；
   - resume 成功（约 2m06s），旧证书以
     `compute instance rebuilt on resume from suspended` 撤销，新证书签发并恢复健康；
   - 再次 suspend 成功（约 6s）；
   - purge 的 destroy job 成功（约 3s），qube 状态 `purged`，PVE 上按测试名称匹配的
     VM 数量为 **0**，两条 agent 证书均为 revoked（purge / resume rebuild）。
   该链证明当前 HEAD 的成功路径、状态转换、证书轮换与销毁可以走通。

## 已定位并处理的问题

### 1. console 升级必须同步发布新 agent 包（部署前置条件）

最早一次 provision 使用环境里现有的 agent 包 `3d4fdad`（2026-09-22）失败：
bootstrap 报 `bootstrap peer public key does not match the pin issued with its token`，
job 在 4 分钟后以证书不受信任失败。`3d4fdad` 早于 token 派生密钥 pin 的提交 `f34f343`
（2026-09-26）。

**结论：升级 console 到包含 pin 校验的版本时，必须同时发布并用 `agent_package_url` /
`agent_package_sha256` / `agent_package_version` 指向同一 revision 构建的 agent 包。**
只升级 console 会让所有新 provision 在 bootstrap 阶段死亡。该顺序应写进升级/回滚文档。

### 2. completion hook 耗尽上下文导致 job 结果丢失（已修，#73）

在修复前的二进制上，provision 已到达健康 agent、qube 已是 `running`，但 job 永远停在
`running`：completion hook 把 30 秒预算全部花在 dom0 RemoteVM 注册调用上，随后
`finish()` 用同一个已过期的 context 写终态，写失败并丢失结果。

修复：completion hook 与 job 终态写入使用各自独立的 bounded context；回归测试使用一个
会耗尽预算的 hook 和一个拒绝已取消 context 的 store。变异验证：把持久化 context 换成
已取消的 context，测试报 `completion failure must persist as failed, got running`。

### 3. provision 可达性等待原为硬编码 4 分钟（已修）

`internal/orchestrator/native.go` 原先用编译期常量 `provisionProbeTimeout = 4 * time.Minute`。
本环境一次冷启动包含 apt 安装、agent 包下载、bootstrap 和证书签发，稳定通过时约 2m49s，
早期几次因启动/安装较慢在 4 分钟边界失败。现已改为
`QUBES_AIR_ORCHESTRATOR_PROVISION_WAIT_SECONDS`（默认 240s，0/负数回退到编译期默认），
由 `NativeExecutor.WithProvisionWait` 接线；须小于 job 总超时（UD-1e）。

### 4. RemoteVM 注册/注销未在本次独立实例中验证（测试环境限制）

独立实例的 dom0 `qubesair.RegisterRemoteVM` qrexec 调用超时，导致注册失败（provision 仍
成功）以及 destroy job 的 completion hook 失败、qube 状态停在 `deleting`。为完成隔离环境中
的生命周期验证，测试实例设置了 `QUBES_AIR_REGISTER_REMOTEVMREDACTED=false`。
生产 console 的 RemoteVM 注册/注销由既有 QA-01 记录覆盖，本次**没有**重新验证。

## 仍未覆盖

- Exec/FileCopy 的真机正反例；
- 数据盘内容跨 suspend/resume 的持久性；
- 生产 console 自身的升级部署（本次是独立实例）；
- RemoteVM 注册/注销（见上）；
- Proxmox 以外的 provider。

## 清理与环境状态

- 所有测试 qube（含最终 qube）均已 purge；PVE 侧按测试名称匹配的 VM 数量为 **0**。
- 测试 console 实例已停止，独立端口已关闭；独立数据目录已删除（不再保留 PVE 凭据副本）。
- 用于向 agent 提供可验签 revocation 文档的临时 artifact 已删除；刷新循环已停止。
- 临时 HTTP server、凭据文件与 PVE 登录 ticket 已删除。
- 生产 console 未重启、未迁移、未改配置。

## 结论

当前 HEAD 的 console 构建、数据库迁移、zone/credential 创建、真实 PVE 读路径、job 日志
写入与保留/过期、完整生命周期（provision→suspend→resume→suspend→purge）、证书轮换和
销毁均通过真机验证（测试二进制 `0d23189`，#73 已合并）。M0-5 的生命周期条件已满足；
Exec/FileCopy、数据持久性、生产部署升级与 RemoteVM 注册仍需单独验收。
