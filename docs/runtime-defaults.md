# 运行期默认值与数据库结构

更新：2026-09-22。本文件登记**代码里真实生效**的运维默认值与 SQLite 表结构，每条都给出
`文件:行号`。它回答的问题是"文档能对，但核查者无法从文档反查到代码位置"。

> 范围与判据：只写从当前代码读到的值。**凡本文件未列的默认值一律以代码为准**；本文件不描述
> 部署实例的真实配置（env / `config.example.yaml` 里的值），也不替代各专题文档的契约说明。
> 表中位置均相对仓库根；行号对应仓库当前提交，改代码后须在同一 commit 同步本文件。上次全量核对
> 的基线与 Sprint 1 一致（`fae0aea`）；其后 `internal/config/config.go`、`cmd/server/main.go`、
> 本次的 `internal/database/database.go` 与 `internal/orchestrator/runner.go` 行号发生位移，
> 均已逐条重算。
> 相关专题：[安全控制](security-controls.md)、[可靠性契约](reliability-design.md)、
> [灾难恢复](disaster-recovery.md)、[gRPC transport](grpc-transport-design.md)。

## 1. 运维默认值

### 1.1 控制台进程与请求边界

| # | 默认值 | 取值 | 位置 |
|---|---|---|---|
| UD-1 | 每客户端限流 | **20 req/s，burst 40** | `console/backend/internal/config/config.go:505`（`RateLimitPerSec: 20`）、`:506`（`RateLimitBurst: 40`） |
| UD-1b | 请求体上限 | **1 MiB**（`1 << 20`） | `config/config.go:313`（`DefaultMaxBodyBytes`） |
| UD-1c | 浏览器会话 TTL | **12 小时** | `internal/middleware/session.go:18`（`DefaultSessionTTL`） |
| UD-1d | 可信代理 | **不信任任何代理**：`ClientIP()` 取对端地址，忽略 `X-Forwarded-For` | `cmd/server/main.go:921-922`（`configureTrustedProxies`）、`:932`（在 `setupRouter` 里调用） |
| UD-1e | 单个 orchestration job 超时 | **45 分钟**（`JobTimeoutSeconds: 2700`），env `QUBES_AIR_ORCHESTRATOR_JOB_TIMEOUT_SECONDS` | `config/config.go:306`（字段）、`:552`（默认值）、`internal/orchestrator/runner.go:186`（`DefaultJobTimeout`）、`cmd/server/main.go:536`（接线） |
| UD-1f | `/health` 的编排 dispatcher 心跳：空闲轮询间隔 / 判死阈值 | **5s / 15s**（阈值 = 3 次丢拍）；dispatcher 正在执行 job 时预算再加该 job 的超时（UD-1e）。**无配置键**（编译期常量） | `internal/orchestrator/health.go:11`（`DispatcherPollInterval`）、`:22`（`DispatcherStaleAfter`） |
| UD-1g | `/health` 数据库探测的最小间隔（未认证路由的写节流） | **2s**（窗口内的重复请求复用上次成功；失败不入缓存）；代价是库变为不可写最多晚一个窗口被发现 | `cmd/server/main.go:1192`（`healthProbeInterval`） |
| UD-1h | agent 的 Exec/FileCopy 路径白名单（随 qube 写入 `agent.env`） | **默认都为空 = 该服务在 guest 内禁用**（agent 对空列表直接 `reject(..., 77)`，不是"允许全部"）；Exec 是绝对程序路径、FileCopy 是绝对目录（`/` 被拒），冒号分隔，两侧各自校验 | `config/config.go:219`（`AgentExecAllow`）、`:224`（`AgentFileCopyRoots`）、`:732`/`:735`（env `QUBES_AIR_EXEC_ALLOW`/`QUBES_AIR_FILECOPY_ROOTS`，冒号分隔）；校验 `internal/qrexec/allowlist.go`；写入 `internal/service/cloudinit.go:299` |

### 1.2 transport（Relay ↔ console / agent）

| # | 默认值 | 取值 | 位置 |
|---|---|---|---|
| UD-2 | keepalive 心跳间隔 | **20s** | `internal/transport/grpc/client.go:61`（`withDefaults`） |
| UD-3 | 重连退避 | **min 500ms / max 30s**，指数翻倍 + 抖动 | `internal/transport/grpc/client.go:63-68`（下限/上限）、`:145-150`（`jitter(backoff)`、`backoff *= 2`、封顶 `ReconnectMax`） |

### 1.3 agent 侧（调用、探测、bootstrap、解锁、续期）

| # | 默认值 | 取值 | 位置 |
|---|---|---|---|
| UD-4 | 单次服务调用超时 | **2 分钟** | `internal/agent/invoker.go:35`（`DefaultCallTimeout`） |
| UD-4b | 子进程 `WaitDelay`（取消后等待 I/O 收尾） | **2s** | `internal/agent/invoker.go:228` |
| UD-4c | 单次服务响应上限 | **16 MiB** | `internal/agent/invoker.go:39`（`maxResponseBytes`） |
| UD-4d | 服务脚本目录 | `/etc/qubes-rpc` | `internal/agent/invoker.go:32`（`DefaultServiceDir`） |
| UD-5 | pending renewal（等待签发的私钥）TTL | **5 分钟** | `internal/agent/renewal.go:54`（`DefaultPendingRenewalTTL`） |
| UD-6 | job 日志 SSE 流最长时长 | **5 分钟**（到点干净结束，客户端按 offset 重连） | `internal/handler/job_handler.go:179`（`streamMaxDuration`） |
| UD-6b | SSE 单个事件的写窗口 | **30 秒**（每个事件重置；只界定一次写，不界定整条流） | `internal/handler/job_handler.go:188`（`streamWriteWindow`） |
| UD-9 | agent 探测超时 | **10s** | `internal/service/agentprobe.go:71`（`DefaultAgentProbeTimeout`） |
| UD-9b | agent 默认监听端口 | **8443** | `internal/service/agentprobe.go:75`（`defaultAgentPort`） |
| UD-9c | 新置备 qube 的 agent 就绪结算预算 / 重试间隔 | **5 分钟 / 15s** | `internal/service/agenthealth.go:27`、`:29`（`DefaultAgentSettleBudget`、`DefaultAgentSettleRetry`） |
| UD-10 | 数据盘解锁超时 | **60s** | `internal/service/agentunlock.go:48`（`DefaultDataUnlockTimeout`） |
| UD-10b | 解锁用 relay 证书寿命 | **5 分钟** | `internal/service/agentunlock.go:33`（`unlockCertLifetime`） |
| UD-11 | bootstrap 单次交换超时 | **60s** | `internal/service/agentbootstrap.go:66`（`DefaultBootstrapTimeout`） |
| UD-11b | bootstrap 重试退避 | **base 15s / max 10 分钟** | `internal/service/bootstrapsched.go:49`、`:50`（`bootstrapRetryBase`、`bootstrapRetryMax`） |
| UD-12 | 证书续期单次超时 | **30s** | `internal/service/certrenew.go:50`（`DefaultCertRenewalTimeout`） |
| UD-12b | 续期用 relay 证书寿命 | **1 小时** | `internal/service/certrenew.go:91`（`renewRelayCertLifetime`） |
| UD-12c | CA 允许的时钟回拨（NotBefore backdate） | **5 分钟** | `internal/service/certrenew.go:96`（`caClockSkewBackdate`） |
| UD-12d | 续期重试退避 | **base 15 分钟 / max 6 小时** | `internal/service/certrenewsched.go:125`、`:134`（`certRenewalRetryBase`、`certRenewalRetryMax`） |
| UD-12e | 续期时钟偏移余量 / 占窗口比例上限 | **24 小时 / 1/8** | `internal/service/certrenewsched.go:115`（`certRenewalClockSkewMargin`）、`:122`（`certRenewalMaxSkewFraction`） |
| UD-14 | agent 允许服务默认值 | **仅 `qubesair.Ping`**（Exec/FileCopy/UnlockData 需显式 opt-in） | `internal/config/config.go:206-211`（注释与 `AgentAllowedServices`）；包内默认落点为 `packaging/agent-deb/qubes-air-agent.service:18`（`QUBESAIR_ALLOW=qubesair.Ping`），可由 `:20` 的 `EnvironmentFile=/etc/qubes-air/agent.env` 覆盖 |

### 1.4 Proxmox provider

| # | 默认值 | 取值 | 位置 |
|---|---|---|---|
| UD-7 | API ticket 复用 TTL | **90 分钟**（**同名常量两处重复定义**，见开放问题 O-1） | `internal/provider/proxmox/client.go:49`、`internal/scheduler/proxmox.go:53`（均为 `const ticketTTL = 90 * time.Minute`） |
| UD-8 | REST 超时 | **30s** | `internal/provider/proxmox/client.go:77` |
| UD-8b | 任务轮询间隔 | **2s**（调度器构造 provider 时另传 15s REST 超时：`internal/scheduler/proxmox.go:58`） | `internal/provider/proxmox/client.go:281`、`internal/provider/proxmox/adapter.go:80` |
| UD-8c | 默认 cloud-init snippet datastore | `local` | `internal/provider/proxmox/adapter.go:77` |

### 1.5 MCP 接入

| # | 默认值 | 取值 | 位置 |
|---|---|---|---|
| UD-13 | Console API 调用超时 | **15s** | `internal/mcp/client.go:19`（`DefaultAPITimeout`） |
| UD-13b | 上游响应体上限 | **8 MiB** | `internal/mcp/client.go:22`（`DefaultMaxResponseBody`） |
| UD-13c | 单条 JSON 消息上限 | **4 MiB** | `internal/mcp/protocol.go:40`（`DefaultMaxMessageSize`） |

> 已知有文档描述该取值但未给常量名或行号的：
> 请求体上限 1 MiB 见 `docs/mcp-design.md:32`（"API 的 BodyLimit 默认 1 MiB"）；
> 会话 TTL 12 小时见 `docs/roadmap-to-production.md:21`（"session TTL 默认 12h"）。
> 本节的价值是把**常量名与行号**钉住，便于从文档反查代码。

## 2. SQLite 结构（D-6）

数据库没有 migrations 目录，也没有 `.sql` 文件：建表语句是 Go 常量，在打开数据库时执行
`CREATE TABLE IF NOT EXISTS`。版本号存在文件自身的 `PRAGMA user_version` 里。

| 项 | 事实 | 位置 |
|---|---|---|
| 当前 schema 版本 | `SchemaVersion = 2` | `console/backend/internal/database/database.go:232` |
| 版本写入 / 拒绝更新库 | 读到更高版本即报错拒绝打开；否则把 `user_version` 盖成当前值 | `database.go:290-308`（`applySchemaVersion`）、`:311-317`（`UserVersion`） |
| 加列迁移 | `addColumnIfMissing`：先 `PRAGMA table_info` 再 `ALTER TABLE ADD COLUMN`，可重复执行；非空列必须给确定性默认值（`key_version` 回填 1） | `database.go:319-330`（说明）、`:332`（实现） |
| 备份/恢复侧的版本校验 | `ErrSchemaTooNew`；"新控制台备份恢复到旧控制台"会被拒绝 | `docs/disaster-recovery.md:80`、`:84-89` |
| 无外键设计（`qube_infra`） | 删除 qube 行不删除基础设施，只有 `DestroyStorage` 会；`protected` 默认 1 用于挡住不可逆删除 | `database.go:415-422`（注释与建表） |
| 无外键设计（`jobs`） | job 是审计轨迹而非轮询目标，不随 qube 释放级联删除 | `database.go:456-463` |

### 2.1 表与索引清单（10 张表 / 7 个索引）

| # | 表 | 建表位置 | 主键 | 关键列（节选） |
|---|---|---|---|---|
| 1 | `zones` | `database.go:373` | `id` | name, type, status（默认 `disconnected`）, config(JSON), created_at, updated_at |
| 2 | `qubes` | `database.go:392` | `id` | zone_id, status（默认 `stopped`）, ip_address, agent_health（默认 `unknown`）, agent_last_probed_at, agent_last_healthy_at, agent_last_error |
| 3 | `qube_infra` | `database.go:422` | `qube_id` | provider, node, storage_vmid, compute_vmid, data_volume, identity_vol, observed_state, **protected（默认 1）**, observed_at, created_at, updated_at |
| 4 | `infrastructure` | `database.go:438` | `id` | name, type, status, region, config(JSON), resource_count |
| 5 | `jobs` | `database.go:463` | `id` | qube_id, qube_name, action, state, error, enqueued_at, started_at, finished_at |
| 6 | `agent_certs` | `database.go:491` | `fingerprint`（SHA-256 DER） | qube_id, subject_cn, issued_at, expires_at, **revoked_at**, revoked_reason, last_seen_at |
| 7 | `bootstrap_tokens` | `database.go:522` | `secret_hash`（存 hash，不存 token） | qube_id, qube_name, created_at, not_after, **redeemed_at**（单次使用） |
| 8 | `credentials` | `database.go:534` | `id` | name, type, description, **encrypted_data**, **key_version（默认 1）**, last_used |
| 9 | `settings` | `database.go:547` | `key` | value, updated_at |
| 10 | `_health_probe` | `database.go:208` | `id`（`CHECK (id = 1)`，恒定单行） | marker（每次探测新随机值）, checked_at |

> 表清单按 `database.go` 里 `migrate()` 的建表顺序列出；`_health_probe` 不在该清单内，由
> `HealthCheck` 惰性创建（`CREATE TABLE IF NOT EXISTS`），因此列在末尾。下面的二级索引建在同批
> `Exec` 中。
> **计数口径**：`database.go` 中 `CREATE TABLE IF NOT EXISTS` 命中 **10** 处、
> `CREATE INDEX IF NOT EXISTS` 命中 **7** 处（`grep -c`）。
> `runtime-context.md:68` 的"表清单（10 张）"与实际代码**不一致**（其自身表格也只列了 9 行），
> 以代码为准；差异登记为开放问题 O-2。应用 schema 仍是 **9 张表**，`_health_probe` 是探测用表、
> 不属于应用 schema；`zones`、`qubes`、`qube_infra`、`infrastructure`、
> `credentials`、`settings` 未见显式二级索引。

| 索引 | 表 | 位置 |
|---|---|---|
| `idx_jobs_qube_id` | `jobs` | `database.go:475` |
| `idx_jobs_enqueued_at` | `jobs` | `database.go:476` |
| `idx_jobs_state` | `jobs` | `database.go:477` |
| `idx_agent_certs_qube_id` | `agent_certs` | `database.go:502` |
| `idx_agent_certs_revoked` | `agent_certs` | `database.go:503` |
| `idx_bootstrap_tokens_qube_id` | `bootstrap_tokens` | `database.go:531` |
| `idx_bootstrap_tokens_not_after` | `bootstrap_tokens` | `database.go:532` |

## 3. CI 工具链版本（D-4 的落点）

工作流里的 Node 版本**不统一**，且没有任何 `docs/*.md` 声明过版本号：

| 工作流 | Node | 位置 |
|---|---|---|
| `Lint` / `Build` / `Dependencies` | `env.NODE_VERSION: '20'` | `.github/workflows/lint.yml:15`、`build.yml:15`、`dependency.yml:23`（使用点 `:91`、`:67`、`:70`） |
| `Docs and Gates` | `'20'`（内联） | `.github/workflows/docs.yml:29` |
| `Release` | **`"22"`（内联）** | `.github/workflows/release.yml:88-90` |

Go 版本统一走 `env.GO_VERSION: '1.26'` 或 `go-version-file: console/backend/go.mod`
（`dependency.yml:22`、`release.yml:85`）。Node 的这处分叉是**已知不一致**，见开放问题 O-3
（改 `release.yml` 属 release 构建行为变更，本机不可验证，未在本轮改动）。

## 4. 开放问题（本轮登记，不由本文件擅自"解决"）

| ID | 问题 | 为什么不在本轮改 |
|---|---|---|
| O-1 | `ticketTTL` 在 `provider/proxmox/client.go:49` 与 `scheduler/proxmox.go:53` 各定义一份，改一处不会同步另一处 | 属存量重构（drift-check TD 类），需行为等价性验证，超出 T4/T5 范围 |
| O-2 | `runtime-context.md:68` 写"表清单（10 张）"，代码实际只有 9 个 `CREATE TABLE IF NOT EXISTS` | 该文件是 Producer 的 sprint 工件，本分区不得修改（见分区报告）；本文件按代码登记 9 张，差异留在开放问题 |
| O-3 | `release.yml` 用 Node 22，其余工作流用 20；无文档说明这是有意为之 | release 构建行为变更需 release 环境验证；QA/Sprint 2 处置 |
| O-4 | `go-licenses check` 在 `dependency.yml` 上是非阻塞步骤（原为 `\|\| true`，本轮改为显式 `continue-on-error`） | 本机离线无法验证该命令是否通过；见分区报告 T5 开放问题 |
| O-5 | `qubesair.UnlockData:5` 的注释仍写"console derives it (HKDF over its master secret + this qube's id)"，与 `internal/service/datakey.go` 的 DEK 语义不符 | 该脚本在 `remote/**`，不在本分区写集合内 |
