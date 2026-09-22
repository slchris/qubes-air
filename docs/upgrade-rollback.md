# 升级与回滚 runbook

更新：2026-09-22。本文回答一个问题：**手上有一套跑着的 Qubes Air，怎么换到新版本，怎么退回来。**
它同时补上 [G-C3](../docs/production-readiness-gaps.md) 缺的升级契约和 [G-G2](../docs/production-readiness-gaps.md)
缺的兼容矩阵。

前提：这是 **Qubes AppVM + Salt** 的部署路径。仓库里的 `docker-compose.yml` 是开发环境，
[明确不是部署方式](local-dev.md)（第 40 行）；真正的控制台是 `qubes-salt-config` 的 `salt/qubesair`
部署的 systemd 服务，本仓库不复制第二份部署入口。

## 1. 三个制品与它们的钉法

发布产物（[release.yml](../.github/workflows/release.yml) 第 10-19 行）与它们在 Salt 里的键：

| 制品 | 装在哪 | 钉在 `salt/config.jinja` 的 `qubesair` 块 |
|---|---|---|
| `qubes-air-console`（linux/amd64，cgo） | 控制台 AppVM 的 `bin_dir` | `console_binary_source` + `console_binary_sha256` |
| `qubes-air-console-web.tar.gz`（vite `dist/` 的单个归档） | 控制台 AppVM 的 `web_root` | `console_web_source` + `console_web_sha256` |
| `qubes-air-agent_<version>_amd64.deb` | 每个 provision 出来的远端 qube | `agent_package_url` + `agent_package_sha256` + `agent_package_version` |
| `SHA256SUMS` | 不作为制品安装 | 上面这些摘要的来源 |

`SHA256SUMS` 不是形式主义：投递到 guest 的是**无认证的明文 HTTP**，钉在 cloud-init 身份文档里的
摘要是那条链路上唯一的完整性控制（[bootstrap-design](bootstrap-design.md) §6）。

升级的动作就是把这两处（控制台、agent）的 pin 换成新值，然后应用状态：

```bash
sudo qubesctl --skip-dom0 --targets=<cfg.qubesair.qube> state.apply qubesair.console
```

控制台的升级机制是**文件替换 + 服务重启**，不需要重建模板或重启整个 AppVM（Salt 状态自己在
`console.sls` 第 33 行说明了这一点）；二进制经 `file.managed` + `source_hash: sha256=<pin>`
落地（同文件第 260-263 行），所以 pin 写错或制品被换掉会**拒绝写入**，而不是装上一个不明二进制。
web 归档同理（第 301-329 行），并且只在归档内容变化时才重新解包。

## 2. 兼容边界（兼容矩阵）

### 2.1 线协议：console ↔ relay/agent

协议版本与构建版本是**两个**东西，这是刻意的（[frames.go](../console/backend/internal/transport/grpc/frames.go) 第 22-26 行）：

- `protocolVersion`＝`"v1"`（第 26 行）是线协议版本，**不随每次发布变化**；
- `BuildVersion`（第 40 行）只用于握手时的可观测性，**从不参与兼容判定**；
- 服务端能服务的版本是一个**集合** `supportedProtocolVersions`（第 33 行），不是相等判断，
  目的正是"升级不需要 flag day"：一个同时会说 v1 和 v2 的构建，允许两侧以任意顺序升级。

| console 的协议集合 | 对端版本 | 结果 |
|---|---|---|
| 含对端版本 | `v1` | 握手通过，日志记录对端 build（`server.go:404-406`） |
| 不含对端版本 | 其它 | 拒绝，**先回一条带原因的 `CodeProtocolMismatch`** 再断流（`server.go:388-398`），日志给出"支持的版本"清单 |
| 对端版本为空 | — | 同上路径，提示信息按"未上报版本"处理 |

因此**协议不是升级顺序的约束**，只要新构建仍列出旧版本。真正的顺序约束来自下面两条。

### 2.2 数据库 schema：前向单向

`SchemaVersion` 是编译期常量（[database.go](../console/backend/internal/database/database.go) 第 232 行），
存在 SQLite 的 `user_version` 里（`UserVersion`，第 311 行）。打开一个**更新**版本的库会被拒绝：

> `database schema version N is newer than this console supports (M); upgrade the console before opening this database`
> （`applySchemaVersion`，第 290-305 行）

这条规则决定了一切：**升级过 schema 之后，回滚二进制不是回滚，而是让控制台起不来。** 所以
"回滚"在 schema 变更后只有一个手段——从备份恢复（见 §4）。

### 2.3 API 与前端

前端与二进制必须**同批**升级：前端只讲 `/api/v1`，两者来自同一次 release。

## 3. 升级顺序

1. **先备份**（schema 升级前必做，不是可选项）：
   ```bash
   export QUBES_AIR_BACKUP_PASSPHRASE='...'
   qubes-air-backup create \
     -db /rw/config/qubesair/qubes-air.db \
     -out /secure/offhost/qubesair-$(date +%Y%m%dT%H%M%S).qab
   ```
   口令从环境变量读（命令行会被同机 `ps` 看到），输出 `O_EXCL` + `0600`。
   细节与恢复步骤见[灾难恢复](disaster-recovery.md)。
2. **控制台**：改 `console_binary_*` 与 `console_web_*` 两组 pin → `state.apply qubesair.console`。
   两个制品必须同批改，理由见 §2.3。
3. **验证**（见 §3.1）。
4. **agent/relay 侧**：改 `agent_package_url/sha256/version` → 重建 compute。注意它们的生效路径：
   这三个键由 Salt 渲染进**控制台的环境**（`QUBES_AIR_AGENT_PACKAGE_*`，经 `EnvironmentFile=` 注入，
   见 `salt/qubesair/console.sls` 第 365-367 行），控制台在**生成身份文档时**把它们交给新 provision
   的 VM。所以改完必须重新 `state.apply`（否则控制台进程里还是旧值），而**既有 qube 不会自己换
   agent**——它们的"生效范围"是之后新建/重建的 compute（[runbook](runbook-remotevm.md) 第 139-145 行
   的回滚指示也正是"重建 compute"）。按 §2.1，它与控制台升级的先后顺序不影响兼容性。

### 3.1 怎么确认升级成功（今天可用的手段）

- `/health`：`status` 是否为 `healthy`、`database` 是否为 `connected`、`worker.dispatcher` 是否为
  `alive`（`disabled` 表示编排被关掉，那是配置不是故障）。该检查会真的写一行探测标记再读回，
  并对调度器心跳判活（[灾难恢复](disaster-recovery.md) 第 69-72 行）。
- 二进制摘要：把 `bin_path` 上的文件算 sha256，与 `console_binary_sha256` 逐字比对——这比任何
  自报版本都可靠。
- 服务状态：`systemctl status qubes-air-console`；启动脚本每次开机从 `/rw/config/rc.local` 拉起，
  并且有一道 preflight 会**拒绝启动配置不全的控制台**（`console.sls` 第 14、18 行），所以
  "起了但立刻退出"通常就是配置缺键。
- 前端：浏览器强制刷新后确认页面能加载（旧前端配新后端会在 API 变更时表现为 400/404）。

> **不要用 `/health` 的 `version` 或 `--version` 判断跑的是哪个构建。** 控制台二进制从不携带
> 构建版本：`appVersion` 是编译期常量 `"0.1.0"`，Makefile 和 `release.yml` 都不注入
> （缺口 G-H8，计划在 M2-10 修）。在那之前，能证明"装的是哪个构建"的只有二进制 sha256。

## 4. 回滚

| 情形 | 手段 | 验证 |
|---|---|---|
| schema **未**升过（新旧 `SchemaVersion` 相同） | 把两组 pin 恢复到上一组值 → 重新 `state.apply`（`source_hash` 会拒绝不匹配的制品）→ 重启服务 | `/health` healthy；二进制 sha256 等于旧 pin |
| schema **已**升过 | **只能**从备份恢复：`qubes-air-backup restore -db … -in … -force`（[灾难恢复](disaster-recovery.md) 第 58-65 行），并确保进程持有**同一把** keyring 密钥 | `/health` healthy，且能真的提交一个 job |
| 单个 compute 故障 | 先 suspend/resume，不要删 data disk（[runbook](runbook-remotevm.md) 第 139-145 行） | — |
| agent 发布故障 | 恢复上一组 `agent_package_*` 后**重建** compute（同上） | 新 compute 上的 agent 能完成 bootstrap 与探测 |
| 协议不匹配 | 两侧版本集合无交集：把有交集的那一侧升（或降）回去 | 握手日志出现 `relay %q connected (protocol …)` |

回滚的顺序与控制台升级相反：**先恢复 pin 再重启服务**，不要先停服务再慢慢找旧制品——停机期间
任何正在跑的 job 都不会自己恢复（runner 重启后不重新装载排队 job）。

## 5. 失败模式速查

| 现象 | 多半是 | 处理 |
|---|---|---|
| `state.apply` 报 `source_hash` 不匹配 | pin 与实际制品不一致（写错摘要、或制品被替换） | 用发布页的 `SHA256SUMS` 重新核对摘要，不要绕过校验 |
| 服务启动后立刻退出 | preflight 拒绝：`config.jinja` 缺键或路径不存在 | 看 `systemctl status` / journal 里的 preflight 输出 |
| 控制台报 schema 更新而拒绝启动 | 回滚错了方向（二进制旧、库新） | 按 §4 第二行处理：恢复备份，或把二进制升回 |
| 远端 qube 连不上 | agent 版本/协议不在 console 的集合里 | 看握手日志的 `rejecting relay …`（带支持版本清单） |
| 页面能开但接口全 404/400 | 前端与二进制不同批 | 两组 pin 一起改 |

## 6. 尚未闭合

- **没有真机演练过**：本机没有 dom0 入口，§3、§4 的步骤是按 Salt 状态与代码读出来的，未在真机
  上跑过一遍升级 + 回滚。真机项（M1-2~M1-4、M1-9）阻塞中。
- **备份调度与留存策略**还没有（M1-5）：本文只写了"升级前手动备份一次"，没有"多久备一次、留多久"。
- **首次 release 还没跑通**（M1-10）：`release.yml` 从未产出过制品，所以上表里的制品名与
  `SHA256SUMS` 是 workflow 的意图，不是已验证的产物。
- **版本注入未做**（G-H8 / M2-10）：所以 §3.1 只能用 sha256 认构建，不能用自报版本。
