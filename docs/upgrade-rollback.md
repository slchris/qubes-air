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
- **构建身份**：`/health` 的 `version` / `revision` / `build_time` / `tree` 四个字段就是这次构建的
  自我标识，与制品 `--version` 打印的是同一组值（同一份链接期注入，同一份 `buildinfo.Get()`）；
  命令与实测输出见 §3.2。
- 二进制摘要：把 `bin_path` 上的文件算 sha256，与 `console_binary_sha256` 逐字比对。**两条都要看**：
  摘要证明盘上的**文件**是那份制品，构建身份证明**跑着的进程**是那一份——只对摘要分不清"制品换对了
  但服务还在跑旧二进制"（服务没重启，或重启后被 preflight 拦下）。
- 服务状态：`systemctl status qubes-air-console`；启动脚本每次开机从 `/rw/config/rc.local` 拉起，
  并且有一道 preflight 会**拒绝启动配置不全的控制台**（`console.sls` 第 14、18 行），所以
  "起了但立刻退出"通常就是配置缺键。
- 前端：浏览器强制刷新后确认页面能加载（旧前端配新后端会在 API 变更时表现为 400/404）。
  页眉右侧显示的版本应与 `/health` 的 `version` 逐字相同；页眉**没有**版本也是有效答案——说明服务没答上，或跑着的是一个没注入构建身份的二进制（§3.2）。

### 3.2 读构建身份：运行中的服务 / 发布制品

控制台二进制在**链接期**被写入三项元数据（包 [internal/buildinfo](../console/backend/internal/buildinfo/buildinfo.go)；
注入点是 `Makefile` 的 `build-backend` 与 [release.yml](../.github/workflows/release.yml) 的
Build console binary 步骤），`/health` 与 `--version` 报的是同一组：

| 字段 | 来源 | 读法 |
|---|---|---|
| `version` | `git describe --tags --always --dirty`，**原样**不重写 | tag 构建＝tag 本身（`v1.2.3`）；tag 之后＝`v1.2.3-4-gabcdef`；工作树有未提交改动＝结尾多一个 `-dirty` |
| `revision` | `git rev-parse HEAD` | 完整 commit，不是缩写 |
| `build_time` | 链接时刻，RFC 3339 UTC | 同一 commit 的两次构建靠它区分 |
| `tree` | 从 `version` 的 `-dirty` 后缀解析 | `clean` / `dirty`；**未注入时是 `unknown`**——不知道就不说成 `clean` |

**从运行中的服务读**（`/health` 无需 token，docker-compose 的 liveness probe 走的就是它）：

```console
$ curl -s http://127.0.0.1:8080/health
{"status":"healthy","database":"connected","worker":{"dispatcher":"disabled","queued":0,"running":0},"version":"a70df74-dirty","revision":"a70df74c78ee729aedc1eedeb59f0c5cb1811cbc","build_time":"2026-09-22T12:21:38Z","tree":"dirty"}
```

上面是本机实测（`make build-backend` 从**有未提交改动的工作树**构建，所以 `version` 以 `-dirty`
结尾、`tree` 是 `dirty`；`dispatcher` 是 `disabled`，因为复现环境按 compose 的默认关掉了编排）。

**从发布制品读**（不读配置、不连库，打印完即退出，可以在一台还没部署的机器上跑）：

```console
$ ./qubes-air-console --version
qubes-air-console version=v1.2.3 revision=a70df74c78ee729aedc1eedeb59f0c5cb1811cbc build_time=2026-09-22T12:30:00Z tree=clean
```

上面是 tag 构建的实测形状：`version` 就是 release 的 tag，`tree=clean`。两边的四个值逐字相同，
才说明跑着的确实是那份制品；`revision` 对得上发布页/`git log`、`build_time` 与制品构建时间不矛盾，
才排除"拿着旧二进制当新版本"。

发布流程自己会校验这件事：release.yml 的 Build console binary 步骤在打包前把制品的 `--version` 读回来，**逐字段**检查——`version` 必须等于本次 release 版本、`revision` 与 `build_time` 不得是 `unknown`、`tree` 必须是 `clean` 或 `dirty`，任一不满足就 `FATAL` 失败、不发版（每个字段是独立的 `-X`，只查 `version` 会漏掉兄弟 flag 的拼写错误）。所以"制品四个字段齐全"不是靠人记得。

页眉（Header）显示的版本就是同一份 `version`：它启动时读 `/health`，`unknown` 或服务不可达时**不显示**任何版本，而不是退回一个常量。

**未注入的二进制读得出来是未注入**：不带 `-ldflags` 的普通 `go build ./cmd/server` 三个字段都是
`unknown`（同一份代码的实测输出）：

```console
$ go build -o /tmp/qubes-air-console ./cmd/server && /tmp/qubes-air-console --version
qubes-air-console version=unknown revision=unknown build_time=unknown tree=unknown
```

`unknown` 的语义是"这个二进制没有构建身份"，不是"版本号叫 unknown"：看到它就别用 `version` 判断，
回到 sha256。反过来，改造前 `/health.version` 恒为编译期常量 `0.1.0`——那才是"看起来像版本"的假答案
（G-H8）。另外，`console/backend/Dockerfile.dev`（`docker compose` 的开发镜像）**故意**不带注入：
镜像与挂载里都没有 `.git`，它报 `unknown` 是实话，且它不是生产制品。

## 4. 回滚

| 情形 | 手段 | 验证 |
|---|---|---|
| schema **未**升过（新旧 `SchemaVersion` 相同） | 把两组 pin 恢复到上一组值 → 重新 `state.apply`（`source_hash` 会拒绝不匹配的制品）→ 重启服务 | `/health` healthy；二进制 sha256 等于旧 pin；且 `/health` 的 `version`/`revision` 等于**旧制品** `--version` 报的值（§3.2）——这一步才证明回滚的进程真的换回去了 |
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
  `SHA256SUMS` 是 workflow 的意图，不是已验证的产物；构建身份的实际形状在本地二进制上验证过
  （§3.2），release 制品上的那一条要等 M1-10 跑通才算。
