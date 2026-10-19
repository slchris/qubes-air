# 2026-10-19 当前 main 真机测试：provider 读路径通过，生命周期被 agent 包版本阻塞

基线：`origin/main` `4f16915`（#68 job 日志保留、#69 告警前端、#70 预算测试、#71 TODO
同步均已合并）。本次用当前 HEAD 构建 console 与 agent，在真实 Proxmox 集群和 Qubes
管理入口上执行；生产 console 未改动。

## 测试方式

- 从 `4f16915` 构建 linux/amd64 console（`version=v0.1.0-205-g4f16915`，`revision=4f16915`，
  `tree=clean`）和前端资源。
- 在 console qube 内启动**独立实例**：新数据库、新 agent identity 目录、新 web root、
  独立 API token/加密密钥、仅监听 loopback 的独立端口；未替换生产二进制、未迁移生产数据库。
- 通过 Qubes 管理入口（qrexec RemoteDebug → `qvm-run`）在 console qube 内执行 HTTP 请求；
  PVE 侧读操作经其 HTTPS API 核对。

## 通过的路径

1. **当前 console 启动与迁移**：空数据库启动后健康检查 `status=healthy`、`revision=4f16915`、
   `audit_trail=ok`，schema 迁移到 6。
2. **credential + zone 创建/连接**：用 PVE 凭据创建 operator credential 与 Proxmox zone；
   `POST /zones/:id/connect` 返回 `connected`。
3. **容量读取**：`GET /zones/:id/capacity` 返回六个节点全部 online 及 CPU/内存读数，
   证明当前 provider 的认证、TLS、节点解析和读路径在真实集群上工作。
4. **job 日志（M2-8 路径）**：provision 运行中 `GET /jobs/:id/log` 返回增量输出、
   `log_state=available`、`log_created_at` 已写入。日志包含真实的 VM 创建、模板克隆、
   磁盘扩容和 qemu-guest-agent 等待过程。
5. **失败路径与销毁**：provision 在 agent 不可达后以明确错误失败（qube `error`，
   `agent_health=unreachable` 带原因）；对 `error` 状态的 qube 执行 purge 被接受，
   destroy job 成功，PVE 上 compute 与 storage 两个 VM 都被删除，qube 变为 `purged`。
  销毁日志明确记录了 `qmstop` 后对 compute 与 storage 两个 VM 的 `qmdestroy`。
6. **job 日志保留（M2-8）**：把一份已完成 job 的日志文件 mtime 调到 31 天前并重启实例；
   启动清理删除了该文件，job 行写入 `log_pruned_at`，API 返回
   `log_state=expired` 与对应 note；同日期的另一份日志仍为 `available`，未被误删。

## 未通过 / 未覆盖

### 1. 当前 console 拒绝旧 agent 包（可复现的部署前置条件）

第一次 provision 使用生产环境现有的 agent 包 `3d4fdad`（2026-09-22）失败：

- console 日志：bootstrap 拒绝该监听端，理由是
  `bootstrap peer public key does not match the pin issued with its token`；
- job 错误：4 分钟后 agent 不可达，`x509: certificate signed by unknown authority`。

原因可定位到版本关系：`3d4fdad` 早于 token 派生密钥 pin 的提交 `f34f343`（2026-09-26）。
**结论：升级 console 到包含 pin 校验的版本时，必须同时发布并用 `agent_package_url` /
`agent_package_sha256` / `agent_package_version` 指向同一 revision 构建的 agent 包。**
只升级 console 会让所有新 provision 在 bootstrap 阶段死亡。该顺序应写进升级/回滚文档，
不能把“console 升级成功”当成可 provision。

### 2. 当前 agent 包无法送达 guest（本次环境阻塞）

随后从 `4f16915` 构建了当前 agent `.deb`
（`0.0.0+qa4f16915`，SHA-256 `6f58ada9960f009b65c700f6db4968ead88b6c6fcbefe64bb77ff0f2838665f7`）
并更新测试实例的 package URL/SHA/version。第二次 provision 仍在 4 分 30 秒后失败：
agent unit 未运行、8443 连接被拒；本次测试环境中 guest 无法访问临时提供 deb 的节点
（HTTP 访问日志没有来自 guest 的请求）。artifact store 的上传端点要求
`MIRROR_USERNAME` / `MIRROR_PASSWORD`，本次会话没有该凭据。

因此以下路径**尚未得到真机证据**：

- 当前 agent 的首次 bootstrap 与 CA 签发；
- 成功 provision 后的 suspend → resume；
- 成功 provision 后的 purge（数据盘与身份销毁）；
- Exec/FileCopy、数据盘跨 suspend/resume 持久性；

当前 agent 包已构建并保存在本机临时目录；需要 artifact store 上传凭据或一个 guest
可达的 HTTP 位置后即可继续。

## 清理与环境状态

- 两个测试 qube 均已 purge；PVE 侧按测试名称匹配的 VM 数量为 **0**。
- 测试 console 实例已停止，独立端口已关闭。
- 临时 HTTP server 与临时凭据文件已删除；PVE 登录 ticket 已删除。
- 生产 console 未重启、未迁移、未改配置。
- 测试实例的数据目录（独立数据库、日志、测试密钥）保留在 console qube 内以便续测；
  其中的 PVE 凭据是密文，目录权限 0700、env 0600。

## 结论

当前 HEAD 的 console 构建、数据库迁移、zone/credential 创建、真实 PVE 读路径、job 日志
写入与保留/过期状态、服务端失败/销毁路径均通过。**生命周期冒烟（M0-5）仍不能标记完成**：
首次成功的 agent bootstrap 未发生，根因已定位为 agent 包版本与 console pin 校验不匹配，
第二个阻塞是本次会话缺少 artifact 上传凭据。记录保留为失败证据，不把部分通过写成全量通过。
