# 真机验收清单：生命周期、备份恢复与 PVE 主机键

更新：2026-09-22。本页把**只有真机能回答**的三块检查写成一次可以跑完的清单：

1. provision → suspend → resume → purge 生命周期，含 Exec/FileCopy 正值与数据持久性（M1-2/3/4，即 G-B2/G-B3）；
2. 备份 create/prune/restore 演练，含一次真 timer 触发与一次**被比对过的恢复**（M1-5，即 G-C1/G-C2）；
3. PVE 节点 SSH 主机键指纹（M1-8 的指纹半边；PVE 集群版本半边已于 2026-09-22 经 API 带外核对）。

每一步只回答一件事：**命令、跑在哪一侧、预期输出、失败意味着什么**。本页不写"确认可用"这类
判据，也不保存结果——执行记录写到 `docs/reviews/<日期>-<环境>.md`（模板见
[Proxmox 回归 runbook](runbook-qa01.md) §7）。

本页不替代 [Proxmox 真机回归 runbook](runbook-qa01.md)（它给整套 QA-01 的绑定与环境快照）、
[可靠性契约](reliability-design.md)（部分失败与重试）、[安全控制](security-controls.md)（信任边界）、
[灾难恢复](disaster-recovery.md)（备份格式、留存论证与恢复步骤）。它与这些文档使用同一套命令与
术语：控制台 API 的驱动脚本是仓库里的 `scripts/qa-console.sh`，qrexec 侧命令来自
[RemoteVM runbook](runbook-remotevm.md) §5/§7，备份命令来自[灾难恢复](disaster-recovery.md)。

前置：

- 目标 revision 上 `make pre-commit` 与 `make audit` 已通过（`AGENTS.md` §2）；
- Qubes 侧部署入口是外部仓库 [qubes-salt-config](https://github.com/slchris/qubes-salt-config)，
  本仓库不复制第二份（`AGENTS.md` 第 10 行）；本页引用它的 `v0.1.0`（`db1b68d`）与其中
  `salt/qubesair/README.md` 的部署顺序、`salt/qubesair/console.sls` / `backup.sls` 的行为；
- 本页不写真实基础设施地址：`<...>` 一律是占位符，从 `salt/config.jinja` 与执行记录里取值。

## 0. 使用方式

### 0.1 五个执行位置

| 简称 | 是什么 | 怎么进 |
|---|---|---|
| **dom0** | Qubes dom0：Salt master、`qubesctl`、`qvm-*` | 直接控制台 |
| **控制台 qube** | `cfg.qubesair.qube`（默认 `qubesair-console`），跑 console 服务、持 PVE token 与 CA | 你现有的 dom0 `qubes.RemoteDebug` / SSH 通道 |
| **PVE 节点** | 集群里任一节点，root shell | SSH（与控制台用的同一批节点） |
| **远端 VM** | provision 出来的 compute（PVE VM）。**不能直接登录**：命令经 `qrexec-client-vm <remotevm> …` 从 policy 允许的本地 AppVM 发出 | [RemoteVM runbook](runbook-remotevm.md) §7 |
| **本地工作站** | 有仓库 checkout、`gh`、docker 的机器 | 直接使用 |

`<remotevm>` 是 dom0 里 `qvm-ls --class RemoteVM` 列出的那个名字（本地地址壳为 `remote-<qube>`），
不是 Qube 名。

### 0.2 绑定表（执行前填，抄进记录）

| 项 | 值 |
|---|---|
| 仓库 revision（`git rev-parse HEAD`） | |
| release tag（`gh release list`） | |
| `qubes-air-console` sha256 | |
| `qubes-air-backup` sha256 | |
| console web tarball sha256 / agent deb version+sha256 | |
| Qube 名 / Zone / 节点 / datastore / 模板 VMID | |
| PVE 版本（`pveversion -v`） | |
| `qubes-salt-config` revision 与 tag | |
| 数据盘是否加密（`cfg.qubesair.encrypt_data_default`） | |
| 执行日期 / 执行者 | |

### 0.3 只有一侧时能完成什么

真机窗口不一定同时有 dom0 与 PVE 控制台。按侧拆分如下，**不要**把单侧能做的读数当成整项通过：

| 检查 | dom0 | 控制台 qube | PVE 节点 | 本地工作站 | 只有一侧时 |
|---|---|---|---|---|---|
| §A Salt 应用与重启持久化（步骤 2、5） | 必需 | 必需 | — | — | 缺 dom0 → 整块做不了（`qubesctl` 在 dom0） |
| §A 二进制/服务/health（步骤 3、4、6） | — | 必需 | — | 步骤 1 需工作站 | 有控制台 qube → 可做（重启由 dom0 触发，但可事后核对） |
| §B 生命周期（步骤 7-20） | 步骤 9、19 | 必需 | 步骤 9、15、19 | — | 缺 PVE 节点 → 只能拿到 console 侧读数，**不满足**"每一步都从 provider 侧复核"（runbook §3.6） |
| §B Exec/FileCopy（步骤 10-14、16） | — | — | — | — | 需要"policy 允许的本地 AppVM"这一侧（与控制台 qube 同属 Qubes 侧） |
| §C 备份与恢复（步骤 21-28） | 步骤 23 | 步骤 21-27 | — | 步骤 22 | 缺 dom0 → 可手工 `systemctl start`，但 timer/bind-dirs 那两条读不到 |
| §C 离机恢复（步骤 28） | — | 取口令需控制台 qube | — | — | 需要第二台 Linux 机器 |
| §D 指纹（步骤 29-33） | — | 步骤 30-33 | 步骤 29、31、33 | — | 只有 PVE 节点 → 步骤 29 可做并留档，**比对**（31-33）没有 `pve_known_hosts` 就做不了 |

### 0.4 公共约定

控制台 API 的驱动方式（`scripts/qa-console.sh` 的前提就是"在控制台 qube 里跑"）：

```bash
# 以下命令都在控制台 qube 内执行，除非步骤里另写了环境
API=http://127.0.0.1:8080/api/v1
TOKEN="$(grep -m1 '^QUBES_AIR_API_TOKEN=' /rw/config/qubesair/secrets.env | cut -d= -f2-)"
api() { curl -fsS -H "Authorization: Bearer $TOKEN" "$@"; }
```

走 `127.0.0.1` 而不是 `qubes.ConnectTCP+8080` 代理：QA-01 记录里经代理读 `GET /api/v1/qubes`
会 500/截断（本地代理通道限制，不是 console 缺陷），在 qube 内跑就没有这一层。

`scripts/qa-console.sh` 是仓库里的驱动脚本（它自己的注释就写着"run ON the console qube"）：用你现有的
文件通道把它放进控制台 qube（例如 `~/bin/qa-console.sh`，`chmod +x`），本页下面写作 `qa-console.sh
<子命令>`。它的子命令与下面的 curl 是同一批调用，本页在需要看 HTTP 状态码（例如 purge 的 400）时写
curl——脚本里的 `api()` 带 `-f`，会把预期的 4xx 变成非零退出。

## A. 制品与 Salt 应用

### 步骤 1 — 绑定 revision，并确认制品就是这一版

- **环境**：本地工作站（有仓库 checkout 与 `gh`）。
- **命令**：

  ```bash
  cd <qubes-air checkout>
  git rev-parse HEAD
  gh release view <tag> -R slchris/qubes-air --json tagName,assets -q '.tagName, (.assets[].name)'
  mkdir -p /tmp/rel && gh release download <tag> -R slchris/qubes-air -D /tmp/rel
  (cd /tmp/rel && sha256sum -c SHA256SUMS)      # macOS: shasum -a 256 -c SHA256SUMS
  chmod +x /tmp/rel/qubes-air-console /tmp/rel/qubes-air-backup
  docker run --rm --platform linux/amd64 -v /tmp/rel:/w -w /w debian:13 \
    sh -c './qubes-air-console --version; ./qubes-air-backup --version'
  git rev-parse <tag>^{commit}
  ```

- **预期输出**：`sha256sum -c` 对 `SHA256SUMS` 里列出的 4 个文件各打一行 `<文件>: OK`；两个
  `--version` 各打一行 `qubes-air-console|qubes-air-backup version=<tag> revision=<40 位 commit>
  build_time=<RFC3339 UTC> tree=clean`；`git rev-parse <tag>^{commit}` 与那两个 `revision=` 逐字相同。
  `v0.1.0` 的实测形状（2026-09-22，`docker` 内跑发布制品）：

  ```console
  qubes-air-console version=v0.1.0 revision=020b0860776949fef38f0d31c330e826cecabedd build_time=2026-09-22T13:29:34Z tree=clean
  qubes-air-backup version=v0.1.0 revision=020b0860776949fef38f0d31c330e826cecabedd build_time=2026-09-22T13:30:28Z tree=clean
  ```

  注意 GitHub 的发布制品**不带可执行位**：不 `chmod +x` 会得到 `Permission denied`，那不是制品问题。
- **失败含义**：`FAILED open or read` = 少了某个制品（`gh release download` 默认下载全部 asset，
  除非你只挑了其中几个）。`--version` 报 `unknown` = 这个二进制没被链接期注入（本地 `go build` 的
  产物就是这样，见[升级与回滚](upgrade-rollback.md) §3.2），即它**不是**发布制品，不能用来做本清单的绑定。
  `revision` 与 tag 不相等 = 制品与 tag 不同源，停止本次验收。

### 步骤 2 — 在 dom0 按顺序应用 Salt states

- **环境**：dom0。`salt/config.jinja` 已按 `qubesair/README.md` 与
  [升级与回滚](upgrade-rollback.md) §1 填好（三个 pin 指向步骤 1 的摘要）。
- **命令**（顺序即 `salt/qubesair/README.md` 的 Deploy 一节，跨 qube 的顺序不是装饰）：

  ```bash
  sudo qubesctl top.enable qubesair.clone  && sudo qubesctl state.apply qubesair.clone
  sudo qubesctl top.disable qubesair.clone
  sudo qubesctl --skip-dom0 --targets=tpl-qubesair state.apply qubesair.install
  sudo qubesctl top.enable qubesair.create && sudo qubesctl state.apply qubesair.create
  sudo qubesctl top.disable qubesair.create
  qvm-start <cfg.qubesair.qube>
  sudo qubesctl --skip-dom0 --targets=<cfg.qubesair.qube> state.apply qubesair.configure
  sudo qubesctl --skip-dom0 --targets=<cfg.qubesair.qube> state.apply qubesair.console
  sudo qubesctl state.apply mgmt.remotevm.register          # 缺它注册会静默失败，release/purge 才显形
  ```

- **预期输出**：每条 `qubesctl` 退出码 0，结尾是 `Succeeded: N (changed=M)` 形状的汇总；不带
  `Failed:` 行。`qubesair.console` 的最后一条 `cmd.run` 打出
  `qubes-air-console is serving on <cfg.qubesair.listen>`（`console.sls` 的
  `qubesair-console-start` 轮询 `/health` 后才打印）。上游 README 的命令用字面量
  `qubesair-console` 作 target；本页写成 `<cfg.qubesair.qube>`，值取你 `config.jinja` 里的 `qube`。
- **失败含义**：`state.apply qubesair.console` 报 `qubesair-console-binary-sha-required`（`failhard`）
  = `console_binary_sha256` 没设；`source_hash` 不匹配 = 盘上的二进制与 pin 不是同一份，**不要**绕过
  校验（[升级与回滚](upgrade-rollback.md) §5）。README 特别点出的那个假成功：漏掉 `qubesair.console`
  这一步，前面每一步都报成功而**根本没有 console**——所以本步骤把最后一条命令写在清单里，而不是留给记忆。

### 步骤 3 — 控制台 qube 里的二进制等于那份制品

- **环境**：控制台 qube。
- **命令**：

  ```bash
  sha256sum /rw/config/qubesair/bin/qubes-air-console
  /rw/config/qubesair/bin/qubes-air-console --version
  ```

- **预期输出**：第一行等于 `salt/config.jinja` 的 `console_binary_sha256`，也等于步骤 1 里
  `SHA256SUMS` 的 `qubes-air-console` 行；第二行与步骤 1 的 console `--version` **逐字**相同
  （同一份链接期注入，见[升级与回滚](upgrade-rollback.md) §3.2）。
- **失败含义**：摘要相同但 `--version` 不同 = 制品换对了而进程/文件不对（先看步骤 4 的进程身份）；
  `--version` 是 `unknown` = 这个文件是本地构建，pin 指错了对象。

### 步骤 4 — 控制台服务与 `/health`

- **环境**：控制台 qube。
- **命令**：

  ```bash
  systemctl is-active qubes-air-console
  curl -sS http://127.0.0.1:8080/health
  systemctl --failed
  ```

- **预期输出**：`active`；`/health` 返回 `status":"healthy"`、`"database":"connected"`、
  `worker.dispatcher` 为 `alive`（`cfg.qubesair.orchestrator_enabled: True` 时）或 `disabled`
  （编排关掉时——那是配置不是故障，`console.sls` 的通知原文就是
  `NOTE: orchestration is DISABLED … start/stop only flip database status; no provider is called`），
  以及步骤 1 那四个构建字段；`systemctl --failed` 无输出。
- **失败含义**：`inactive`/`failed` + `systemctl status` 里的
  `qubes-air-console preflight FAILED: …` = 启动前置不满足（缺 secrets.env、加密密钥不是 32 字节、
  `identity_dir` 权限不是 0700、数据库目录不可写）。`dispatcher: stale` = 调度器心跳过期，
  队列里的 job 不会被执行（[可靠性契约](reliability-design.md)）。
  `/health` 的 `status: unhealthy` 是数据库读写探测失败（磁盘满/只读/库被删），不是"服务还活着"。

### 步骤 5 — 重启之后服务仍在（bind-dirs 持久化）

- **环境**：dom0 触发重启，控制台 qube 内核对。**这是最容易坏的一步**：unit 写在
  `/rw/bind-dirs/etc/systemd/system/`，靠 bind-dirs 在每次开机投影回 `/etc/systemd/system/`；
  写到真实路径的版本会在重启后消失且不报错（`console.sls` 的注释记着这个已付出代价的缺陷）。
- **命令**：

  ```bash
  # dom0
  qvm-shutdown --wait <cfg.qubesair.qube> && qvm-start <cfg.qubesair.qube>

  # 控制台 qube（等它起来后）
  systemctl cat qubes-air-console | head -3
  mountpoint -q /etc/systemd/system/qubes-air-console.service && echo bound
  sha256sum /etc/systemd/system/qubes-air-console.service \
            /rw/bind-dirs/etc/systemd/system/qubes-air-console.service
  systemctl is-active qubes-air-console
  curl -sS http://127.0.0.1:8080/health
  grep -c 'qubesair-console' /rw/config/rc.local
  ```

- **预期输出**：`systemctl cat` 的第一行是 `# /etc/systemd/system/qubes-air-console.service`
  （systemd 找得到这个 unit），随后是模板里的第一行
  `# SPDX-License-Identifier: MIT — managed by qubesair.console` 与 `[Unit]`——模板里没有这个
  unit，能读出内容就说明 bind-dirs 投影生效；`bound`；两个 `sha256sum` 完全相同；`active`；
  `/health` 同步骤 4；`grep -c` ≥ 1（rc.local 里的 `# >>> qubesair-console >>>` 块存在）。
- **失败含义**：`Unit qubes-air-console.service could not be found` = bind-dirs 没投影，rc.local 里
  那句 `systemctl start qubes-air-console` 会以 `2>/dev/null || true` 静默吞掉——**这是"上次能用、
  这次没用"的典型形态**，按 `console.sls` 的 bind-dirs 说明修（写 `/rw/bind-dirs` 而不是 `/etc`）。
  两个 sha256 不同 = `/etc` 下是一个陈旧副本（上一次 `mount --bind` 留下的空文件也会这样），
  同样按 bind-dirs 处理。`mountpoint` 无输出 = 本次开机没有建立绑定。

### 步骤 6 — console 进程读到的就是配置里的值

- **环境**：控制台 qube。
- **命令**：

  ```bash
  grep -E '^(QUBES_AIR_ORCHESTRATOR_ENABLED|QUBES_AIR_EXEC_ALLOW|QUBES_AIR_FILECOPY_ROOTS|QUBES_AIR_AGENT_ALLOWED_SERVICES|QUBES_AIR_PROXMOX_SSH_KEY_FILE|QUBES_AIR_PROXMOX_SSH_KNOWN_HOSTS_FILE)=' \
    /rw/config/qubesair/console.env
  systemctl cat qubes-air-console | grep -E '^EnvironmentFile='
  ```

- **预期输出**：`QUBES_AIR_EXEC_ALLOW` / `QUBES_AIR_FILECOPY_ROOTS` **冒号**分隔（空值=guest 内禁用），
  `QUBES_AIR_AGENT_ALLOWED_SERVICES` **逗号**分隔，`QUBES_AIR_ORCHESTRATOR_ENABLED=true`，
  `QUBES_AIR_PROXMOX_SSH_KEY_FILE` / `…_KNOWN_HOSTS_FILE` 指向 `<data_dir>/ssh/` 下的文件；
  第二条打两行 `EnvironmentFile=`，指向 `console.env` 与 `secrets.env`（unit 里就是这两条，
  `systemctl show -p EnvironmentFiles qubes-air-console` 是等价的另一种读法）。
- **失败含义**：键不存在或为空 = 该能力在 guest 内**禁用**而不是"全部允许"（这是设计方向，
  `qubes-salt-config` 的 `console.env` 渲染与 `config.go` 的"空=未设置"守卫一致）。改了这些键之后
  必须重新 `state.apply qubesair.console`（state 自己会 `systemctl restart`）：console 在启动时读环境。
  **生效范围是之后新建/重建的 compute**，既有 qube 不会自己换身份文档（[升级与回滚](upgrade-rollback.md) §3）。

## B. 生命周期：provision → suspend → resume → purge

### 步骤 7 — 前置：Zone 存在，且本清单要用的白名单已下发

- **环境**：控制台 qube。Zone/credential 的建立属于部署（`qubesair/README.md` 的 Enabling
  orchestration 与 [RemoteVM runbook](runbook-remotevm.md) §3），本步骤只确认它们对本次验收可用。
- **命令**：

  ```bash
  qa-console.sh list
  ```

- **预期输出**：`== zones ==` 后是 `{"zones":[…],"total":N}`（`N` ≥ 1），`== qubes ==` 后是本清单
  开始前的既有 qube 清单（记录下来，purge 后要能指出本次新建的那一个）。
- **失败含义**：zones 为空 = 还没建 zone/credential，先按 runbook §3 建（控制台凭据用
  `POST /api/v1/credentials`，`console.sls` 的结尾通知给出示例命令）。zone 的 `status` 若是
  `disconnected`，**先连上**：`POST /api/v1/zones/<zone-id>/connect`——resume（`POST
  /qubes/{id}/start`）会先校验 zone 是 `connected`，不连就会以 `ErrZoneDisconnected` 失败；而这个
  connect 只翻状态位、不验凭据，所以凭据是否真的可用仍由步骤 8 的 provision 证明。zone 存在但
  provision 报 provider 认证/网络错误 = 凭据或 endpoint 问题，与本清单的生命周期结论无关，先修部署。

### 步骤 8 — provision：通过控制台创建一个 Qube

- **环境**：控制台 qube。
- **命令**（`scripts/qa-console.sh` 的 `create` 就是这一条，spec 里给了 2 GB 数据盘）：

  ```bash
  qa-console.sh create <zone-id> qa-accept-1
  # 记下响应里的 qube.id 与 job_id
  qa-console.sh job <job-id>                 # 轮询到 succeeded/failed/unknown
  qa-console.sh wait <qube-id> healthy
  qa-console.sh qube <qube-id>
  api "$API/qubes/<qube-id>/certs"                   # 记下 fingerprint，步骤 20 要用
  ```

- **预期输出**：`create` 先回显它发的 `POST /qubes {…}`，再打印响应 body，形如
  `{"qube":{"id":"…","name":"qa-accept-1","status":"…",…},"job_id":"…"}`（HTTP 状态码是 201，
  但脚本不打印状态码；要看状态码就用同一 body 直接 curl）；`job` 打印的 job JSON 最终
  `"state":"succeeded"`（`action":"provision"`）；
  `wait` 打印 `agent_health=healthy after <N>s`；`qube` 的 `"status":"running"` 且 `agent_health`
  为 `healthy`；`certs` 返回 `{"certs":[{…"fingerprint":"…"…}],"count":1}`。
- **失败含义**：job `failed` → 看 `api "$API/jobs/<job-id>"` 的 `error` 与 `…/jobs/<id>/log`；
  真机 QA-01 在这里踩过两类：snippet 上传需要 console→节点的 SSH（PVE API 没有这个端点，
  `qubesair/README.md` 专列一节），以及 RemoteVM 注册未应用导致后续 release/purge 失败
  （步骤 2 的那条 `mgmt.remotevm.register` 就是为此）。`agent_health` 停在 `unreachable` 超过
  bootstrap settle 窗口（默认 300 s）→ 按 [RemoteVM runbook](runbook-remotevm.md) §4/§11 在 guest
  内看 `qubes-air-agent`；`agent_recovery: manual` 表示已超出单元自身的重启预算。

### 步骤 9 — provision 的 provider 侧复核 + RemoteVM 出现

- **环境**：PVE 节点（`qm`）、dom0（`qvm-*`）。
- **命令**：

  ```bash
  # PVE 节点
  qm list | grep -E 'qa-accept-1'
  qm config <compute-vmid> | grep -E '^(name|tags|scsi1|description)'

  # dom0
  qvm-ls --class RemoteVM
  qvm-prefs <remotevm> transport_rpc
  qvm-prefs <remotevm> remote_name
  ```

- **预期输出**：`qm list` 里能看到 compute（名 `qa-accept-1`）与 holder（名 `qa-accept-1-storage`）；
  compute 的 `tags` 含 `qubes-air;compute;<type>`，`scsi1=` 是**数据盘卷 id**（形如
  `<datastore>:vm-<holder-vmid>-disk-0`——把它抄进记录，步骤 16 要比对）；holder 的
  `tags` 含 `qubes-air;storage;<type>`，`description` 里带 ownership 标记。
  dom0 里 RemoteVM 存在，`transport_rpc` 为 `qubesair.GrpcProxy`，`remote_name` 是裸 Qube 名
  （地址壳为 `remote-qa-accept-1`）。
- **失败含义**：compute 在但 `agent_health` 不 healthy = 走步骤 8 的失败分支。RemoteVM 不在 =
  `mgmt.remotevm.register` 未应用或注册被拒（注册是 quiet 失败，QA-01 记录里它到 release 才显形）。
  `transport_rpc` 不是 `qubesair.GrpcProxy` = dom0 policy/state 与本次部署不一致。

### 步骤 10 — Ping（链路通不通）

- **环境**：policy 允许的本地 AppVM（`qrexec-client-vm` 的调用侧）。
- **命令**：

  ```bash
  qrexec-client-vm <remotevm> qubesair.Ping
  echo "exit=$?"
  ```

- **预期输出**：单行 `pong <remote_name> <unix_ts>`，`exit=0`
  （契约见 `remote/qubes-rpc/qubesair.Ping`）。
- **失败含义**：`Request refused` = 该服务不在 agent 的 `QUBESAIR_ALLOW` 里（console 侧是
  `QUBES_AIR_AGENT_ALLOWED_SERVICES`），或 dom0 policy 未授权该源 qube；两者都不是"远端坏了"。

### 步骤 11 — Exec 正值（M1-2 的正例）

- **环境**：同步骤 10 的 AppVM。前置：`cfg.qubesair.agent_exec_allow` 至少含 `/usr/bin/id`
  与 `/usr/bin/df`，且该 qube 是**配置生效之后**创建的（步骤 6 的生效范围）。
- **命令**：

  ```bash
  printf '%s\n' '["/usr/bin/id"]' | qrexec-client-vm <remotevm> qubesair.Exec
  echo "exit=$?"
  printf '%s\n' '["/usr/bin/id","-u"]' | qrexec-client-vm <remotevm> qubesair.Exec
  echo "exit=$?"
  ```

- **预期输出**：第一行是 `uid=0(root) gid=0(root) groups=0(root)`（agent 以 root 运行：
  `packaging/agent-deb/qubes-air-agent.service` 的 `User=root`），第二行是 `0`，两次 `exit=0`。
  两个参数以**字面量**传递，第二个例子证明 argv 不被 shell 解释。
- **失败含义**：`qubesair.Exec: service disabled: no allowed executables configured`（退出码 77）
  = `QUBESAIR_EXEC_ALLOW` 空——白名单没有下发到 guest；`executable is not allowed`（126）=
  下发了但不含这个程序；两者的区别就是"控制台配置没生效"与"配置生效但配错了"。

### 步骤 12 — Exec 负值（拒绝对照）

- **环境**：同步骤 11。
- **命令**：

  ```bash
  printf '%s\n' '["/usr/bin/uptime"]' | qrexec-client-vm <remotevm> qubesair.Exec; echo "exit=$?"
  printf '%s\n' '["/usr/bin/id; /usr/bin/id"]' | qrexec-client-vm <remotevm> qubesair.Exec; echo "exit=$?"
  printf '%s\n' '["id"]' | qrexec-client-vm <remotevm> qubesair.Exec; echo "exit=$?"
  printf '%s\n' 'not-json' | qrexec-client-vm <remotevm> qubesair.Exec; echo "exit=$?"
  ```

- **预期输出**：四行 stderr 依次含 `qubesair.Exec: executable is not allowed`（126）、
  `qubesair.Exec: executable is not allowed`（126——`;` 那行是**一个**字符串，整串不等于任何白名单项；
  如果它被 shell 拆开会打印两行 `uid=`，所以"没有 `uid=` 输出"本身就是判据）、
  `qubesair.Exec: program must be a normalized absolute path`（64）、
  `qubesair.Exec: expected a JSON array of argument strings`（64）；四个 `exit=` 都非 0，且全程没有
  `uid=` 输出。
- **失败含义**：任何一条返回 0 = 白名单或参数校验没有生效（安全边界问题，不是功能问题），记 blocker。
  远端**没有执行**的判据是退出码与 stderr，不是"命令没输出"。

### 步骤 13 — FileCopy 正值：往数据盘写一个文件

- **环境**：同步骤 11；前置 `cfg.qubesair.agent_filecopy_roots` 含 `/data`（数据盘挂载点）。
- **命令**：

  ```bash
  {
    printf 'push /data/qa-accept.txt\n'
    printf 'qubes-air-acceptance %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } | qrexec-client-vm <remotevm> qubesair.FileCopy > /tmp/qa-push.out
  echo "exit=$?"
  cat /tmp/qa-push.out
  ```

- **预期输出**：`/tmp/qa-push.out` 一行 `OK push <字节数> <sha256> /data/qa-accept.txt`，`exit=0`
  （先重定向再 `cat`：`$?` 必须是 `qrexec-client-vm` 的退出码，管道末尾接 `tee` 时拿到的会是 `tee` 的）。
  **把这一行的 sha256 抄进记录**，步骤 16 与步骤 27 都要比对它。
- **失败含义**：`path outside allowed directories`（126）= `/data` 不在
  `QUBESAIR_FILECOPY_ROOTS` 里；`no allowed directories configured`（77）= 白名单为空。
  两者都是下发问题，不是磁盘问题。

### 步骤 14 — 确认 `/data` 真的是那块数据盘（防止假通过）

- **环境**：同步骤 11。这一步的存在理由：加密数据盘要等 console 通过 `qubesair.UnlockData` 打开之后
  才挂到 `/data`；在那之前 `/data` 可能只是**根盘上的空目录**。写进去的文件在 suspend/resume 后
  会消失，看起来像"数据持久性坏了"，实际是"盘从来没挂上"。
- **命令**：

  ```bash
  printf '%s\n' '["/usr/bin/df","-h","/data","/"]' | qrexec-client-vm <remotevm> qubesair.Exec
  ```

- **预期输出**：`/data` 与 `/` 两行的**设备名与容量不同**——`/data` 的容量等于创建时给的
  `data_disk_gb`（`qa-console.sh create` 里是 2，即约 2.0G），`/` 是模板/`spec.disk`
  的根盘大小。
- **失败含义**：两行同设备同容量 = `/data` 未挂载（数据盘没打开）。此时步骤 13 的"成功"是假的，
  不要继续 suspend/resume 的持久性判定；先看控制台日志里该 qube 的 unlock 记录，并确认
  `QUBES_AIR_AGENT_ALLOWED_SERVICES` 含 `qubesair.UnlockData`（加密盘必需，`agentunlock.go` 用它）。

### 步骤 15 — suspend：计算实例消失、holder 与数据盘留下

- **环境**：控制台 qube 发起，PVE 节点复核。
- **命令**：

  ```bash
  # 控制台 qube
  qa-console.sh suspend <qube-id>
  qa-console.sh job <job-id>
  qa-console.sh qube <qube-id>

  # PVE 节点
  qm list | grep -E 'qa-accept-1'
  qm config <holder-vmid> | grep -E '^(scsi0|tags)'
  ```

- **预期输出**：`suspend` 返回 202 与 `job_id`；job 最终 `"state":"succeeded"`（`action":"suspend"`）；
  `qube` 的 `"status":"suspended"`；PVE 节点上 `qm list` **只剩 `<qube>-storage`**（compute 不在），
  holder 的 `scsi0` 仍是步骤 9 记录的那个数据盘卷 id。
- **失败含义**：job `failed` 而 `status` 回到 `error` = 按[可靠性契约](reliability-design.md)重试
  suspend（不要先删盘）；holder 或 `scsi0` 消失 = 数据盘真的丢了，立即停止后续步骤并按
  [凭据销毁](credential-destruction.md)的边界报告，此时 resume 拿不回数据。

### 步骤 16 — resume：数据持久性（M1-3 的核心断言）

- **环境**：控制台 qube 发起，远端 VM 复核。
- **命令**：

  ```bash
  # 控制台 qube
  qa-console.sh resume <qube-id>
  qa-console.sh job <job-id>
  qa-console.sh wait <qube-id> healthy

  # PVE 节点：compute 回来了，且挂的是同一块数据盘
  qm list | grep -E 'qa-accept-1'
  qm config <new-compute-vmid> | grep -E '^scsi1'

  # 远端 VM（经 qrexec）
  printf '%s\n' '["/usr/bin/sha256sum","/data/qa-accept.txt"]' \
    | qrexec-client-vm <remotevm> qubesair.Exec; echo "exit=$?"
  printf 'pull /data/qa-accept.txt\n' \
    | qrexec-client-vm <remotevm> qubesair.FileCopy > /tmp/qa-pull.out
  sha256sum /tmp/qa-pull.out
  ```

- **预期输出**：job `succeeded`，状态 `running`，`agent_health` 回到 `healthy`（IP/端点变化是正常的：
  重建了 compute）；新 compute 的 `scsi1` 与步骤 9 记录的**逐字相同**；Exec 打印的 sha256 与步骤 13
  `OK push` 那行的 sha256 相同；`sha256sum /tmp/qa-pull.out` 的摘要也与它相同（`pull` 把文件内容
  写到 stdout，所以重定向进文件再算摘要，不要靠肉眼看终端）。
- **失败含义**：文件不存在（Exec 打印 `No such file or directory` 且退出码非 0）= 数据没跨过 suspend：
  **先回步骤 14 排除"盘没挂上"**，再按真丢数据报告（附步骤 15 的 provider 侧读数）。
  摘要不同 = 盘挂对了但内容变了，同样按数据问题报告。`agent_health` 不恢复 = 走步骤 8 的 guest 侧排查。

### 步骤 17 — release：可逆的一半

- **环境**：控制台 qube + PVE 节点 + dom0。
- **命令**：

  ```bash
  # 控制台 qube
  qa-console.sh release <qube-id>
  api "$API/jobs?qube_id=<qube-id>&limit=5"     # DELETE 不返回 job_id，用 job 列表看结果
  qa-console.sh qube <qube-id>

  # PVE 节点 / dom0
  qm list | grep -E 'qa-accept-1'
  qvm-ls --class RemoteVM
  ```

- **预期输出**：`release` 返回 202，body 是
  `{"message":"qube released: compute is being destroyed, the data disk is retained"}`
  （`DELETE /api/v1/qubes/{id}`）；`jobs?qube_id=…` 里最新一条 `"action":"release"`、
  `"state":"succeeded"`（列表按时间倒序，`count` 是条数）；状态 `released`；
  PVE 上 compute 消失、`<qube>-storage` 与数据盘保留；dom0 里 `remote-<qube>` 已注销。
- **失败含义**：`remove RemoteVM: deregister […]: qrexec call failed: exit status 126, stderr: Request
  refused`（2026-09-22 QA-01 第一轮的原话）= 注销 RemoteVM 被拒：dom0 的 `mgmt.remotevm.register`
  未应用，或名字不符合 `remote-*`（注册是 quiet 失败，所以到 release 才显形）。Qube 落 `error` 时
  **不要**重新 start（`purge_requested` 未置位，但 release 语义已声明盘要保留）；修好后重试 release，
  或直接进入步骤 18/19 的 purge。

### 步骤 18 — purge 的确认语义：名字不对必须 400

- **环境**：控制台 qube。**这一步必须用不带 `-f` 的 curl**：4xx 是这一步的通过条件，
  `scripts/qa-console.sh` 的 `api()` 带 `-f`，会把预期结果变成非零退出而看不出来。
- **命令**：

  ```bash
  curl -sS -o /tmp/purge-bad.json -w 'http=%{http_code}\n' \
    -X POST "$API/qubes/<qube-id>/purge" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"confirm":"not-the-name"}'
  cat /tmp/purge-bad.json
  qa-console.sh qube <qube-id>          # 目标必须还在
  ```

- **预期输出**：`http=400`，body 的 **`message`** 字段含
  `purge confirmation does not match the qube name: got "not-the-name", want "qa-accept-1"`
  （`service.ErrPurgeConfirmation` 被 `respondError` 放进 `message`，`error` 字段是 HTTP 短语）；
  随后 `qube` 的 `status` 不变（仍是 `released`），PVE 上数据盘仍在。
- **失败含义**：`http=202` = 确认语义失效，**立刻停止**（不可逆动作被一次拼错的名字触发），
  按安全缺陷报告。`http=409` = 该 Qube 还有 compute（先 release/suspend）。

### 步骤 19 — purge 正确执行，并逐项核对清理

- **环境**：控制台 qube 发起；PVE 节点与 dom0 复核。
- **命令**：

  ```bash
  # 控制台 qube
  qa-console.sh purge <qube-id> qa-accept-1
  api "$API/jobs?qube_id=<qube-id>&limit=5"   # 最新一条 action=destroy，轮询到 succeeded
  qa-console.sh qube <qube-id>

  # PVE 节点（datastore 名取自 zone 配置）
  qm list | grep -E 'qa-accept-1'
  pvesm list <datastore> | grep -E '<holder-vmid>'
  ls /var/lib/vz/snippets/ | grep -E '^qubes-air-qa-accept-1'

  # dom0
  qvm-ls --class RemoteVM
  ```

- **预期输出**：purge 返回 202 与
  `{"message":"purge started: the data disk and agent identity will be destroyed"}`
  （同样不返回 job_id，用 `jobs?qube_id=…` 看结果：最新一条 `"action":"destroy"` 最终
  `"state":"succeeded"`；job 没有逐步骤列表，失败时 `error` 字段说明停在哪一步）；状态 `purged`；
  PVE 上 compute、holder、数据盘**都不在**（三条 grep 无输出）；dom0 里没有对应的 `remote-*`。
- **失败含义**：job `failed` 且错误里列出"已完成/未完成的部分" = 部分失败：状态回 `error`、
  `purge_requested` 已置位，**只允许重试 purge，不允许 Start**（[可靠性契约](reliability-design.md)，
  `AGENTS.md` 第 66 行要求报告部分失败）。provider 侧还有残留 = 不要声称销毁完成；把
  `qm list` / `pvesm list` 的原始输出附进记录。

### 步骤 20 — 吊销与墓碑

- **环境**：控制台 qube。
- **命令**：

  ```bash
  qa-console.sh qube <qube-id>                     # 记录应保持 purged
  api "$API/qubes/<qube-id>/certs" | python3 -m json.tool  # 该指纹应带 revoked_at
  curl -sS http://127.0.0.1:8080/pki/revocations \
    | python3 -c 'import sys,json,base64; d=json.load(sys.stdin); s=json.loads(base64.b64decode(d["payload"])); print(s["version"], s["revoked"])'
  ```

- **预期输出**：`qube` 仍为 `purged`（墓碑保留）；`certs` 里步骤 8 记录的 `fingerprint` 带
  `revoked_at` 与非空 `revoked_reason`；`/pki/revocations` 返回 200，python 打出
  `1 ['<步骤 8 的 fingerprint>', …]`（文档结构见 `internal/pki/revocations.go`：
  `{"payload":<base64>, "signature":<base64>}`，payload 解码后含 `revoked` 列表）。
- **失败含义**：`/pki/revocations` 503 = CA 材料不可用（**不是**"没有吊销列表"，
  `revocation_handler.go` 对 nil/错误一律 503，fail closed）；指纹不在列表里 = 吊销没发生，
  这是安全缺陷。已 `purged` 的重复请求按幂等处理（[凭据销毁](credential-destruction.md) §4），
  但**部分失败**后的重试要先查明已完成步骤（[可靠性契约](reliability-design.md)）。

## C. 备份、留存与恢复（M1-5）

### 步骤 21 — 一次性创建备份口令文件

- **环境**：控制台 qube（root）。**只跑一次**：再跑会替换口令，旧归档只能用旧口令解开
  （`backup.sls` 明确不代管这个文件，就是为了避免"某次 apply 换掉口令"）。
- **命令**：

  ```bash
  # <backup_user> = cfg.qubesair.backup.user，默认取 service_user（再默认 user）
  install -m 0600 -o <backup_user> -g <backup_user> /dev/null /rw/config/qubesair/backup.env
  printf 'QUBES_AIR_BACKUP_PASSPHRASE=%s\n' "$(head -c 32 /dev/urandom | base64)" \
    | tee /rw/config/qubesair/backup.env >/dev/null
  stat -c '%a %U %G %s' /rw/config/qubesair/backup.env
  ```

- **预期输出**：`600 <backup_user> <backup_user> <非空字节数>`。口令文件里只有一行
  `QUBES_AIR_BACKUP_PASSPHRASE=…`（`backup.sls` 自己就是这么教的：`install` 建文件、
  `printf | tee` 写值，之后每次 apply 只校验属主与模式、`replace: False` 不动内容）。
- **失败含义**：`qubes-air-backup: set QUBES_AIR_BACKUP_PASSPHRASE …` = 环境文件没读到；
  `backup.sls` 侧的 `qubesair-backup-passphrase-present` 以
  `<env_file> is missing or empty: the archives have no key.` 失败（run 失败而不是渲染一个空跑的绿色
  timer）= 文件不存在或为空，或属主/属组与 `cfg.qubesair.backup.user` 不一致。
  **把口令抄到离机介质上再继续**：它不在归档里，丢了归档就解不开。

### 步骤 22 — 把 `qubes-air-backup` 按摘要钉进 Salt

- **环境**：本地工作站（取摘要）+ dom0（改配置、apply）。
- **命令**：

  ```bash
  # 本地工作站：用步骤 1 已经下载的 release 目录
  grep '  qubes-air-backup$' /tmp/rel/SHA256SUMS
  chmod +x /tmp/rel/qubes-air-backup
  docker run --rm --platform linux/amd64 -v /tmp/rel:/w -w /w debian:13 ./qubes-air-backup --version
  ```

  把 `SHA256SUMS` 里 `qubes-air-backup` 那一行的摘要写进 `salt/config.jinja`：
   `"backup": { "binary_source": "https://github.com/slchris/qubes-air/releases/download/<tag>/qubes-air-backup",
   "binary_sha256": "<该行摘要>", … }`（两个键的语义见[升级与回滚](upgrade-rollback.md) §1 的制品表）。
- **预期输出**：`grep` 打出 `ae0f423c…  qubes-air-backup` 形状的一行（`v0.1.0` 实测；
  实际值以你发布的 tag 为准）；`--version` 打 `qubes-air-backup version=<tag> revision=<tag 的 commit>
  build_time=… tree=clean`；`state.apply qubesair.backup` 不报
  `qubesair-backup-binary-sha-required`、不报 `source_hash` 不匹配。
- **失败含义**：`source_hash` 不匹配 = URL 上那份与摘要不是同一份（发布被替换或 pin 写错），
  不要绕过。若 Salt 不接受该 URL 源，退回 `qubesair/README.md` 的手工交叉编译路线
  （容器内 `CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build`，再放进
  `salt/qubesair/files/` 并把 `binary_source` 写成 `salt://qubesair/files/qubes-air-backup`）——
  **代价要写进记录**：手工构建不带链接期注入，它的 `--version` 报四个 `unknown`，
  所以不能用它证明"盘上跑的是发布制品"。

### 步骤 23 — 应用 `qubesair.backup`，确认 unit 与 timer 就位

- **环境**：dom0 应用，控制台 qube 核对。
- **命令**：

  ```bash
  # dom0（先确认 config.jinja 里 backup.enabled: True、offhost_dir 已挂载）
  sudo qubesctl --skip-dom0 --targets=<cfg.qubesair.qube> state.apply qubesair.backup

  # 控制台 qube
  ls -l /rw/bind-dirs/etc/systemd/system/qubes-air-backup.* \
        /rw/config/qubes-bind-dirs.d/50_qubesair_backup.conf
  mountpoint -q /etc/systemd/system/qubes-air-backup.service && echo bound
  systemctl list-timers qubes-air-backup.timer
  grep -n 'qubesair-backup' /rw/config/rc.local
  ```

- **预期输出**：apply 以 `Succeeded:` 结尾、退出码 0，并在结尾的通知区打出
  `qubesair.backup: binary, units and timer are in place; the timer is running and one backup also
  runs at every boot.`（`test.show_notification`）；
  三个文件存在；`bound`；`systemctl list-timers` 的 `UNIT` 是 `qubes-air-backup.timer`、
  `ACTIVATES` 是 `qubes-air-backup.service`、`NEXT` 是 `cfg.qubesair.backup.schedule`
  （默认 `*-*-* 03:30:00`）渲染出的下一个时刻；rc.local 里有
  `# >>> qubesair-backup >>>` 块，且含 `systemctl start qubes-air-backup.service`（`run_at_boot: True` 时）。
- **失败含义**：`RequiresMountsFor=<offhost_dir>` 的挂载点不存在 → unit 起不来，这正是设计
  （宁可失败也不留唯一副本在本机）。timer `NEXT` 是 `n/a` = timer 没在跑，
  回看 apply 输出里 `qubesair-backup-timer-running` 那一步。

### 步骤 24 — 手动跑一次 create + prune（先别等到出事的那个晚上）

- **环境**：控制台 qube。
- **命令**：

  ```bash
  systemctl start qubes-air-backup.service; echo "exit=$?"
  journalctl -u qubes-air-backup -n 50 --no-pager
  ls -lt <offhost_dir>/*.qab | head
  stat -c '%a %s %n' "$(ls -t <offhost_dir>/*.qab | head -1)"
  ```

- **预期输出**：`exit=0`；journal 里先是 `wrote encrypted backup to
  <offhost_dir>/qubesair-<UTC 时间戳>.qab`，随后是 prune 的一行：
  归档数 ≤ keep 时 `nothing to delete: <N> archive(s) in <offhost_dir>, keep=<K>`，
  否则 `deleted <N> of <M> selected archive(s), <K> kept`（措辞见 `internal/backup/retention.go`）；
  `ls -lt` 第一行是刚写的归档；`stat` 的模式是 `600`。
- **失败含义**：`Active: failed` + journal 里 `qubes-air-backup: set QUBES_AIR_BACKUP_PASSPHRASE`
  = `EnvironmentFile` 没读到（口令文件缺失/权限/属主）；`open output: open <path>: file exists`
  = 同一秒内的第二次运行（`O_EXCL` 拒绝覆盖），不是故障，隔一秒再跑；
  `no such file or directory: <offhost_dir>` = 离机介质没挂上（unit 的 `RequiresMountsFor` 已经替你
  挡了一半）。

### 步骤 25 — prune 的保留窗口与 mtime 陷阱

- **环境**：控制台 qube。
- **命令**：

  ```bash
  # 无效输入必须在任何 I/O 之前被拒绝
  QUBES_AIR_BACKUP_PASSPHRASE=x /rw/config/qubesair/bin/qubes-air-backup prune -dir <offhost_dir> -keep 0; echo "exit=$?"
  # 空跑：报告会删什么，但什么都不删
  QUBES_AIR_BACKUP_PASSPHRASE=x /rw/config/qubesair/bin/qubes-air-backup prune -dir <offhost_dir> -keep 1 -dry-run; echo "exit=$?"
  ls -lt <offhost_dir>/*.qab | head
  ```

- **预期输出**：第一条 stderr 是 `qubes-air-backup: backup: keep must be at least 1: got -keep 0`
  （`log` 前缀 + `internal/backup.ErrKeepTooSmall`；`v0.1.0` 制品实测就是这一行），`exit=1`，且
  `ls -lt` 不变（在任何 I/O 之前拒绝）；第二条每行 `dry run: would delete <path>`，随后
  `dry run: would delete <N> archive(s), <K> kept`，`exit=0`，第三条 `ls -lt` 与运行前**完全一致**
  （dry-run 没删东西）。
- **失败含义**：`-keep 0` 返回 0 或真的删了东西 = 保留策略的输入校验失效，记 blocker
  （它会删到没有可恢复副本）。把归档从离机介质搬回来时用 `cp -p`（或 `rsync -t`）：`prune` 按
  **mtime** 判新旧，丢了时间戳的副本会被当成"最新"的，下次 timer 反而把真正最新的备份挤出窗口
  （[灾难恢复](disaster-recovery.md) §调度与留存；搬回后先 `ls -lt` 核对顺序）。

### 步骤 26 — 让 timer 真的触发一次

- **环境**：dom0 改配置并 apply，控制台 qube 观察。**不要在这一步手动 `systemctl start`**：
  这一步要证的就是"调度器会自己跑"。
- **命令**：

  ```bash
  # 1) dom0：把 config.jinja 的 backup.schedule 临时改成 5 分钟后（OnCalendar 表达式，
  #    形如 *-*-* HH:MM:00，时刻取本地时区 `date +%H:%M` 加 5 分钟），然后重新 apply
  sudo qubesctl --skip-dom0 --targets=<cfg.qubesair.qube> state.apply qubesair.backup

  # 2) 控制台 qube：确认 NEXT 就是那个时刻，然后等它过去
  systemctl list-timers qubes-air-backup.timer

  # 3) 到点之后再读
  systemctl list-timers qubes-air-backup.timer      # LAST 应等于刚才那个时刻
  journalctl -u qubes-air-backup --since '-15 min' --no-pager
  ls -lt <offhost_dir>/*.qab | head
  ```

- **预期输出**：`NEXT` 是你设的那个时刻；到点后同一命令的 `LAST` 等于它，journal 里出现一次
  `wrote encrypted backup to …`（**没有**任何手动 start 记录），`ls -lt` 顶部是新的归档。
- **失败含义**：`LAST` 前进但 unit failed = 触发成功、执行失败，看 journal（这一步与步骤 24 的区别
  正是"谁触发的"）。`LAST` 不动 = timer 未生效（`systemctl status qubes-air-backup.timer` 看
  `Active: active (waiting)`）。**收尾**：把 `schedule` 改回 03:30 并重新 apply，在记录里写明这次
  多出的归档（`keep` 窗口会自然回收它）。
- **注意**：`Persistent=true` 的补跑戳在根卷上、随 AppVM 关机丢失，所以"跨重启的补跑"由
  `run_at_boot` 的 rc.local 那一行负责（`backup.sls` 的长注释解释了为什么不能只靠 timer）。
  本步骤只证明 timer 路径，开机补跑路径用 `journalctl -b -u qubes-air-backup` 在下次开机后核对。

### 步骤 27 — 恢复一份归档，并与现库逐项比对

- **环境**：控制台 qube。**不要**恢复进生产库路径：`restore` 会替换整个数据库，生产库要停服务才安全。
  这一步用临时目录，**不启动第二个 console**。
- **命令**：

  ```bash
  d="$(mktemp -d)"; b="$(ls -t <offhost_dir>/*.qab | head -1)"
  cp -p "$b" "$d/"
  export QUBES_AIR_BACKUP_PASSPHRASE="$(grep -m1 '^QUBES_AIR_BACKUP_PASSPHRASE=' /rw/config/qubesair/backup.env | cut -d= -f2-)"
  /rw/config/qubesair/bin/qubes-air-backup restore -db "$d/qubes-air.db" -in "$d/$(basename "$b")"
  sqlite3 "$d/qubes-air.db" 'PRAGMA integrity_check; PRAGMA user_version;'
  for t in zones credentials agent_certs qubes; do
    printf '%s live=%s restored=%s\n' "$t" \
      "$(sqlite3 /rw/config/qubesair/qubes-air.db "SELECT COUNT(*) FROM $t;")" \
      "$(sqlite3 "$d/qubes-air.db" "SELECT COUNT(*) FROM $t;")"
  done
  sqlite3 "$d/qubes-air.db" 'SELECT name FROM zones ORDER BY name;'
  ```

- **预期输出**：`restore` 打印 `restored database to <d>/qubes-air.db` 并退出 0；
  `PRAGMA integrity_check` 打 `ok`，`user_version` 等于当前 console 的 schema 版本（本版是 `2`，
  见 `internal/database.SchemaVersion`）；每个表的 `live=` 与 `restored=` **要么相等、要么差值可解释**
  （归档是某一时刻的快照，`.` 之后的写入不在里面）；`zones` 的名字列表与现库一致。
- **失败含义**：`qubes-air-backup: backup: wrong passphrase or corrupted archive`
  （`ErrBadPassphrase`）= 口令不是你写那份归档时用的那一代（轮换过就用旧的）；
  `qubes-air-backup: backup: restore target exists (use force to overwrite): <path>` = 目标已存在；
  `qubes-air-backup: backup: schema is newer than this console supports: archive schema <N>, this
  console supports <M>` = 归档来自更新的 console（**恢复不了**，
  与[升级与回滚](upgrade-rollback.md) §2.2 的前向单向一致）；`integrity_check` 不是 `ok` = 归档内容损坏。
  比对时**不要**拿 `jobs` 表计数当判据：它随 job 不断增长，备份之后必然不同。
  收尾：`rm -rf "$d"`，不要把恢复出来的库留在归档目录里。

### 步骤 28 — 离机恢复演练与实测 RTO（M1-4）

- **环境**：控制台 qube 取归档与口令；**第二台 Linux 机器**（amd64，能跑发布制品）做恢复。
  这一步才是 M1-4 要求的"归档经网络/介质到另一台机器、真实 keyring、记录 RTO 与人工步骤"。
- **命令**：

  ```bash
  # 0) 记开始时间（RTO 的起点）
  date -u +%Y-%m-%dT%H:%M:%SZ

  # 1) 控制台 qube：把最新归档与口令送到第二台机器（scp/介质随你，归档本身是密文）
  ls -lt <offhost_dir>/*.qab | head -1
  scp "<最新归档>" <user>@<second-host>:/tmp/
  #    口令走另一条通道（它不在归档里，也不要和归档放同一个地方）

  # 2) 第二台机器：取同一 tag 的发布制品并恢复（第二台机器需要 sqlite3）
  mkdir -p /tmp/restored && chmod +x qubes-air-console qubes-air-backup
  export QUBES_AIR_BACKUP_PASSPHRASE='<口令>'
  ./qubes-air-backup restore -db /tmp/restored/qubes-air.db -in /tmp/<归档名>
  sqlite3 /tmp/restored/qubes-air.db 'PRAGMA integrity_check; PRAGMA user_version;'

  # 3) 用**真实 keyring**（控制台 qube 的 secrets.env 里那把 32 字节密钥）与 API token 启动这份恢复库，
  #    只监听 loopback，关掉编排；不要指向生产库路径
  QUBES_AIR_DATABASE_DSN=/tmp/restored/qubes-air.db \
  QUBES_AIR_HOST=127.0.0.1 QUBES_AIR_PORT=8081 \
  QUBES_AIR_ORCHESTRATOR_ENABLED=false \
  QUBES_AIR_ENCRYPTION_KEY='<secrets.env 里的 QUBES_AIR_ENCRYPTION_KEY>' \
  QUBES_AIR_API_TOKEN='<同一个 token>' \
    ./qubes-air-console & console_pid=$!
  curl -sS http://127.0.0.1:8081/health
  curl -sS -o /dev/null -w 'revocations=%{http_code}\n' http://127.0.0.1:8081/pki/revocations
  curl -sS -H "Authorization: Bearer <token>" http://127.0.0.1:8081/api/v1/zones
  kill "$console_pid"                # 记下 PID 再 kill：不要 pkill -f qubes-air-console，
                                     # 控制台 qube 上那个生产进程同名
  date -u +%Y-%m-%dT%H:%M:%SZ        # RTO 的终点
  ```

- **预期输出**：`restore` 退出 0；`integrity_check` = `ok`、`user_version` = `2`；
  `/health` 是 `"status":"healthy","database":"connected"`；`revocations=200`（**这一条是 keyring 的判据**：
  吊销文档要用库里的 CA 私钥签名，密钥不对时 `revocation_handler.go` 一律 503）；
  `/api/v1/zones` 返回恢复出来的 zone；两个时间戳之间的差值就是**实测 RTO**，连同人工步骤数写进记录。
- **失败含义**：`/pki/revocations` 503 而 `/health` healthy = 数据库能打开但 **CA 材料解不开**：
  keyring 密钥不是加密这份库的那一把（或丢了）。这是 M1-4 要抓的核心失败，不要写成"恢复成功"。
  `/api/v1/zones` 401 = token 不对。**边界**：这台 console 会用自己的恢复副本跑启动对账
  （把 `queued` job 标 failed、`running` 标 unknown、Qube 置 error，不自动重放队列，见
  [可靠性契约](reliability-design.md)），它只写 `/tmp/restored/qubes-air.db`；**绝不要**把它指向
  生产库路径，也不要让第二台机器上的 console 与原 console 共用同一个 DSN/锁文件。

## D. PVE 节点 SSH 主机键指纹（M1-8 的指纹半边）

前提与理由（`docs/runbook-qa01.md` §2 与 G-B5）：主机键**不能**从 PVE API 取——
`/nodes/{node}/certificates/info` 只回 API 证书，几个 SSH 端点都是 HTTP 501 "not implemented"。
`ssh-keyscan` 的结果在比对之前**不是**信任依据（[安全控制](security-controls.md) 的 Proxmox 管理连接一节）。
控制台用它自己的 `known_hosts` 文件校验节点主机键（`internal/provider/proxmox/ssh.go` 的
`knownhosts.New(cfg.KnownHostsFile)`），路径来自 `QUBES_AIR_PROXMOX_SSH_KNOWN_HOSTS_FILE`
（salt 默认 `<data_dir>/ssh/pve_known_hosts`）。文件里的节点地址必须是控制台**实际拨号**的那个地址。

### 步骤 29 — 在节点上导出主机键指纹

- **环境**：PVE 节点（任一；集群每个节点都跑一遍）。
- **命令**：

  ```bash
  for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done
  ```

- **预期输出**：每个存在的公钥一行，形如
  `256 SHA256:<43 字符 base64> <comment> (ED25519)` 与 `3072 SHA256:<43 字符> <comment> (RSA)`
  （位数与键类型随该节点的键而定；ED25519 是 256，RSA 常见 3072，ECDSA 会打 `(ECDSA)`）。
  `<comment>` 通常是 `root@<hostname>`。示例（本机用 OpenSSH 10.3 生成的键，格式与节点侧相同）：

  ```console
  256 SHA256:ATVC4CC3XJzqgY2dCAq2aMyGYzgzoSllG8s5uA75MR4 nodeX (ED25519)
  3072 SHA256:gP0krTJTv4MmsPrSJwUBYs+TuFXguJjCjhR2yrheXNI nodeX (RSA)
  ```

- **失败含义**：**一行都没有**（glob 没匹配到）= 这个节点的 `ssh_host_*_key.pub` 不在那个路径或不是普通
  文件。**不要换一条命令猜**：如实记录 `ls -l /etc/ssh/ssh_host_*` 的输出（本仓库没有核实过 PVE 集群上
  这些路径是否是指向集群共享目录的符号链接），并在记录里标为"指纹未能导出"。

### 步骤 30 — 在控制台侧列出 `pve_known_hosts` 里的指纹

- **环境**：控制台 qube。
- **命令**：

  ```bash
  KH=/rw/config/qubesair/ssh/pve_known_hosts     # 以 console.env 里的取值为准
  ls -l "$KH"
  ssh-keygen -lf "$KH"                            # 整个文件的指纹清单
  ssh-keygen -l -F <node> -f "$KH"                # 单个节点（<node> 用控制台拨号的地址/名字）
  ```

- **预期输出**：`ssh-keygen -lf "$KH"` 对**每一条**记录打一行
  `256 SHA256:<43 字符> <comment> (ED25519)` 形状；`-F <node>` 先打 `# Host <node> found: line N`，
  再打 `<node> <TYPE> SHA256:<43 字符> <comment>`（每行一条键）。上面两行示例的格式在本机核实过
  （`ssh-keygen -l -F` 在**没有**匹配时退出码是 1，且不打印任何 `# Host … found` 行）。
- **失败含义**：文件不存在 = 还没做步骤 31（salt 只创建 `<data_dir>/ssh/` 目录，**不创建这个文件**，
  `console.sls` 有意如此：key 与 known_hosts 都不由 state 生成）。`-F` 退出码 1 = 这个地址在文件里
  没有记录，而 provision 会在写 snippet 的 SSH 连接上失败（`proxmox: ssh dial` / 主机键校验失败）。

### 步骤 31 — 逐条比对，并用核对过的键替换 TOFU 结果

- **环境**：控制台 qube（改文件）+ PVE 节点（取指纹）。
- **命令**：

  ```bash
  # 控制台 qube：先备份现有文件
  cp -p /rw/config/qubesair/ssh/pve_known_hosts \
        "/rw/config/qubesair/ssh/pve_known_hosts.bak-$(date -u +%Y%m%dT%H%M%SZ)"

  # 对每个节点：扫描只用来拿格式，之后逐条比对指纹
  ssh-keyscan -t ed25519,rsa <node> > /tmp/kh.<node>
  ssh-keygen -lf /tmp/kh.<node>
  #   ← 与步骤 29 该节点的输出逐条核对：**每条记录的 SHA256 必须能在节点侧清单里找到，
  #     且该节点每种键类型都要有对应记录**。
  #   全部一致后（<svc_user> = cfg.qubesair.service_user，默认 user）：
  cat /tmp/kh.<node> >> /tmp/kh.all
  install -m 0600 -o <svc_user> -g <svc_user> /tmp/kh.all /rw/config/qubesair/ssh/pve_known_hosts
  rm -f /tmp/kh.<node> /tmp/kh.all
  ```

  比对表（填进记录，一行一条键）：

  | 节点 | 键类型 | 节点侧 `ssh-keygen -lf` 的 SHA256 | `pve_known_hosts` 里同节点的 SHA256 | 一致？ |
  |---|---|---|---|---|
  | | | | | |

- **预期输出**：表中每一行"一致？"为"是"，且不存在"文件里有、节点上没有"的多余记录；
  替换后 `ssh-keygen -lf /rw/config/qubesair/ssh/pve_known_hosts` 的指纹集合等于步骤 29 全部节点输出的集合。
- **失败含义**：任何一条不一致 = 之前用的是 TOFU 结果（可能被中间人换过）。**不要**把不一致的键
  直接替换进去：先用带外通道（节点控制台/IPMI/已知良好的镜像）确认节点真的换了键，再替换；
  只扫描不比对的操作正是本项要消除的。替换不需要重启 console：`knownhosts.New` 在**每次拨号**时
  读这个文件（`internal/provider/proxmox/ssh.go` 的 `nodeSSHConfig`，由 `dialNode` 调用）——
  这一条是代码读出来的，未在真机上专门验证。

### 步骤 32 — 端到端：用同一份文件从控制台 qube SSH 到节点

- **环境**：控制台 qube。这一步是"文件对不对"的功能判据：控制台的 snippet 上传走的就是这条路。
- **命令**：

  ```bash
  ssh -i /rw/config/qubesair/ssh/pve_ed25519 \
      -o UserKnownHostsFile=/rw/config/qubesair/ssh/pve_known_hosts \
      -o StrictHostKeyChecking=yes \
      -o BatchMode=yes \
      root@<node> hostname
  echo "exit=$?"
  ```

- **预期输出**：打印节点的 hostname（与步骤 29 那台节点相符），`exit=0`。
- **失败含义**：`Host key verification failed` = 这个文件与节点实际主机键不一致（或地址写法与控制台
  拨号用的不一致）——在 provision 里它会表现为 snippet 上传失败；`Permission denied (publickey)` =
  控制台的公钥没装到节点上（`qubesair/README.md` 的 "Provisioning needs SSH to the PVE nodes"：
  集群里 `/root/.ssh/authorized_keys` 常经 `/etc/pve` 共享，但**要验证而不是假定**）。

### 步骤 33 — 覆盖调度可能选中的每个节点

- **环境**：PVE 节点 × N + 控制台 qube。
- **命令**：对 zone/集群里**每个**节点重复步骤 29-32；并在控制台侧确认无遗漏：

  ```bash
  # 控制台 qube
  ssh-keygen -lf /rw/config/qubesair/ssh/pve_known_hosts | wc -l
  awk '{print $1}' /rw/config/qubesair/ssh/pve_known_hosts | sort -u
  ```

- **预期输出**：`awk` 打出的地址集合等于集群节点集合（每条记录一个地址），
  `wc -l` 等于所有节点所有键类型的总数；每个地址都能通过步骤 32 的检查。若文件是哈希过的
  （`awk` 打出 `|1|…` 而不是地址），改用逐节点 `ssh-keygen -l -F <node> -f …` 确认（步骤 30 的形式）。
- **失败含义**：少一个节点 = 调度把 qube 放到那台机器上时 provision 会失败在半路（留下半成品）。
  `SSHConfig.KnownHostsFile` 的注释就是对这条的要求："pins the public keys of **every allowed
  cluster node**"。

## E. 失败上报：抓这些原始输出，不要写散文

任何一步失败，按下面命令抓原始输出（把输出贴进 `docs/reviews/` 的记录与 issue，不要转述）：

| 抓什么 | 在哪一侧 | 命令 |
|---|---|---|
| 控制台服务状态 | 控制台 qube | `systemctl status --no-pager --full qubes-air-console` |
| 控制台日志（含 preflight 与 job 决策） | 控制台 qube | `journalctl -u qubes-air-console -b --no-pager -n 200` |
| 健康与构建身份 | 控制台 qube | `curl -sS http://127.0.0.1:8080/health` |
| 失败 job 的错误与日志 | 控制台 qube | `api "$API/jobs/<job-id>"`；`api "$API/jobs/<job-id>/log"`；release/purge 不返回 job id，用 `api "$API/jobs?qube_id=<qube-id>&limit=5"` |
| 备份 unit | 控制台 qube | `systemctl status --no-pager --full qubes-air-backup.service`；`journalctl -u qubes-air-backup -b --no-pager -n 100` |
| 备份工具的身份与拒绝路径 | 控制台 qube | `/rw/config/qubesair/bin/qubes-air-backup --version`；`… prune -dir <dir> -keep 0`（预期被拒，附退出码） |
| 归档与留存现场 | 控制台 qube | `ls -lt <offhost_dir>/*.qab`；`stat -c '%a %s %y %n' <offhost_dir>/*.qab` |
| 吊销文档 | 控制台 qube | `curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/pki/revocations` 加 `journalctl` 里同时间的 `pki status read` 行 |
| agent 侧 | 远端 VM | console 不提供 guest shell：经 qrexec 用允许的程序取（先把它们加进 `agent_exec_allow`，例如 `["/usr/bin/systemctl","status","qubes-air-agent"]`、`["/usr/bin/journalctl","-u","qubes-air-agent","-b","-n","50"]`），或按 [RemoteVM runbook](runbook-remotevm.md) §11 的带外路径 |
| RemoteVM 与 provider 现场 | dom0 / PVE 节点 | `qvm-ls --class RemoteVM`；`qm list`；`qm config <vmid>`；`pvesm list <datastore>`；`ls /var/lib/vz/snippets/` |
| 主机键 | PVE 节点 / 控制台 qube | 步骤 29 的 `for f in …` 输出；`ssh-keygen -lf <pve_known_hosts>` |
| 绑定 | 本地工作站 | `git rev-parse HEAD`；`sha256sum -c SHA256SUMS`；两个 `--version` |

记录里请同时写：**期望看到什么、实际看到什么、哪一侧**（dom0 / 控制台 qube / PVE 节点 / 远端 VM）。
`docs/reviews/` 的记录是历史证据，不是本页的替代品；未通过项照实写，不把部分失败写成"全部通过"
（runbook §7 的要求）。

## Release checklist（打 tag 之前必须为真）

打 tag 之前的项先全部为真；后两项只能在 workflow 发布之后**复核**（tag → 制品是单向的，
`v0.1.0` 就是这么走的：workflow 在发布前逐字段读回 `--version`，人工再复核一遍）。

- [ ] **完整审计通过**：在当前 HEAD 上 `make audit` **退出码 0**（不是 `make pre-commit`：audit 扫全部
      存量代码与全部门禁；工具缺失时 `make check-tools` 会先失败，先把工具补齐）。
- [ ] **每个制品都与 `SHA256SUMS` 对上**：`gh release download <tag> -R slchris/qubes-air -D /tmp/rel`
      后 `cd /tmp/rel && sha256sum -c SHA256SUMS`，`SHA256SUMS` 里列出的四个文件全 `OK`；
      `SHA256SUMS` 本身在 release 的 asset 列表里（`release.yml` 的 `files:`）。
- [ ] **两个二进制的构建身份等于 tag**：`qubes-air-console --version` 与 `qubes-air-backup --version`
      都打出 `version=<tag>`、`revision=<tag 指向的 commit>`、`tree=clean`，且
      `revision` 与 `git rev-parse <tag>^{commit}` 逐字相同（`v0.1.0` 的实测形状见步骤 1）。
      这是可度量的，不是"记得就好"：workflow 里的 `scripts/build-release-binary.sh` 已在打包前逐字段
      校验（`tree` 只接受 `clean`/`dirty`，缺任一戳即 `FATAL`）；如果它没拦住，说明校验本身有问题。
- [ ] **部署侧的读数和它一致**：部署后 `/health` 的 `version`/`revision`/`build_time`/`tree` 与制品
      `--version` 相同（[升级与回滚](upgrade-rollback.md) §3.1/§3.2），页眉显示同一个 version。
- [ ] **release notes 点名未验证的真机项**：本页 §A-§D 里没跑过的项要在 release body 里逐条列出
      （`.github/workflows/release.yml` 的 `body:` 已固定写明"真机生命周期未验证"这一段；本次特有的
      未覆盖项仍需人工补），**不得**用"已完成/可用"暗示真机路径已经跑通。
      （该段落只影响**之后**的 release：`v0.1.0` 的 release body 已经发布，改动不会回写它。）
- [ ] `docs/` 入口同步：本页在 `docs/README.md` 的操作入口里，`production-readiness-gaps.md` 的 M1
      条目指向本页；`make docs-check`（链接与 workflow 门禁）退出码 0。

## 不确定项登记（本页无法给出确定预期输出的地方）

1. **`qubesctl` 的汇总行**：`Succeeded: N (changed=M)` 的具体数字随环境变化，本页只判定退出码与
   是否存在 `Failed:`。
2. **`systemctl list-timers` 的列值**：`LEFT`/`PASSED` 依赖当前时间；本页只判定 `UNIT`/`ACTIVATES`/
   `NEXT` 与配置一致。
3. **journal 的时间戳与行序**：`wrote encrypted backup to …` 与 prune 那两行的措辞来自代码
   （`cmd/qubes-air-backup/main.go`、`internal/backup/retention.go`），但 systemd 的 journal 前缀
   未在真机核对。
4. **PVE 节点上 `ssh_host_*_key.pub` 的实际形态**：可能是指向集群共享目录的符号链接（本仓库没有核实），
   所以步骤 29 在"glob 无匹配"时要求如实记录而不是换命令猜。
5. **holder VM 的状态/卷名形状**：`vm-<vmid>-disk-0` 形状取自 PVE 约定与代码路径，未在真机核对；
   本页只断言"与 suspend 前记录的值相同"。
6. **`qrexec-client-vm` 对远端退出码与 stderr 的回传**：runbook §7 只用它举例，没有测试；若看不到
   126/77 这类退出码，把 stdout/stderr 与 `echo $?` 一起记下来再判断。
7. **手工交叉编译的 `qubes-air-backup`**：`qubesair/README.md` 的 recipe 不带链接期注入，`--version`
   会报四个 `unknown`（这是可判定的），但它**不能**用来证明盘上跑的是发布制品。
8. **`backup.binary_source` 用 release URL 的取法**：qubes-air 侧文档（[升级与回滚](upgrade-rollback.md) §1）
   声明这条路线，`qubesair/README.md` 给的是手工交叉编译路线；若 Salt 拒绝 URL 源，按步骤 22 的
   回退路线执行并在记录里写明差异。
9. **第二台机器上 console 的启动对账行为**：来自[可靠性契约](reliability-design.md)与代码，未真机核对；
   它只写恢复出来的那份副本。
10. **跨重启的 timer 补跑**：由 rc.local 那条 `run_at_boot` 负责；本页只用 `journalctl -b` 核对，
    没有专门做"关机跨越调度时刻"的用例。
11. **release/purge 的 job id**：这两个 handler 只回 `{"message":…}`，不返回 `job_id`
    （`internal/handler/qube_handler.go` 的 `Delete`/`Purge`，代码核实），本页因此用
    `GET /api/v1/jobs?qube_id=…` 轮询。runbook 只写"核验状态变成 purged"，没写这一步怎么取 job——
    这是本页自己补的一处，若你们有更顺的既有做法，替换即可。
