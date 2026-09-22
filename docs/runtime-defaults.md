# 运行期默认值与数据库结构

更新：2026-09-22。本文件登记**代码里真实生效**的运维默认值与 SQLite 表结构，每条都给出
`文件:行号`。它回答的问题是"文档能对，但核查者无法从文档反查到代码位置"。

> 范围与判据：只写从当前代码读到的值。**凡本文件未列的默认值一律以代码为准**；本文件不描述
> 部署实例的真实配置（env / `config.example.yaml` 里的值），也不替代各专题文档的契约说明。
> 表中位置均相对仓库根；行号对应仓库当前提交，改代码后须在同一 commit 同步本文件。上次全量核对
> 的基线与 Sprint 1 一致（`fae0aea`）；其后 `internal/config/config.go`、`cmd/server/main.go`、
> 本次的 `internal/database/database.go` 与 `internal/orchestrator/runner.go` 行号发生位移，
> 均已逐条重算。
>
> 2026-09-22 M2-12 追加：新增 qube 规格上下限（§1.6）。该改动在 `config.go` 与 `main.go` 里插入
> 了代码，§1.1 中指向这两个文件的 `文件:行号` 已按本次工作树逐条重算（也修掉了 M1 之后遗留的
> 偏移）；本轮没有改动的文件**未重新核对**行号。
>
> 2026-09-22 合并后追加：M2-9 与 M2-12 分别改动了 `internal/config/config.go` 与 `cmd/server/main.go`，
> 两个分支合并后这两个文件的行号再次位移（§1.6 整体 +17）。§1.1 与 §1.6 中指向这两个文件的引用
> 已按**合并后**的工作树逐条重算并逐条验证（`DefaultMaxBodyBytes`、`configureTrustedProxies`、
> `healthProbeInterval`、`AgentExecAllow`、`LockFile`、`RateLimitPerSec/Burst`、`JobTimeoutSeconds`、
> §1.6 全部字段/默认值/env 绑定/`Validate`）。（注：M2-12 合并前给出的 §1.1 行号并不准确，
> 例如 UD-1e 的字段行在它自己的工作树上也对不上；本次一并纠正。）
>
> 均已逐条重算。M2-9 单实例锁再次改动 `internal/config/config.go` 与 `cmd/server/main.go`，
> 这两个文件的引用行号已按本次改动后的工作树重算（其余文件本轮未改动，沿用上次结果）。
>
> 2026-09-22 M2-10 追加：新增 console 构建身份（§1.1 UD-23，新增
> `internal/buildinfo` 包）。该改动删掉了 `cmd/server/main.go` 的 `appVersion` 常量并接入
> 链接期注入，§1.1 中指向 `cmd/server/main.go` 的四条引用（UD-1d/UD-1e/UD-1g/UD-1i）已按本次
> 工作树逐条重算并逐条验证（`configureTrustedProxies`、`JobTimeoutSeconds` 接线、
> `healthProbeInterval`、`bootLocked` 及其调用点与 `log.Fatalf`）；`internal/config/config.go`
> 等本轮未改动的文件沿用上次结果。
> 相关专题：[安全控制](security-controls.md)、[可靠性契约](reliability-design.md)、
> [灾难恢复](disaster-recovery.md)、[gRPC transport](grpc-transport-design.md)、
> [升级与回滚](upgrade-rollback.md) §3.2。

## 1. 运维默认值

### 1.1 控制台进程与请求边界

| # | 默认值 | 取值 | 位置 |
|---|---|---|---|
| UD-1 | 每客户端限流 | **20 req/s，burst 40** | `console/backend/internal/config/config.go:628`（`RateLimitPerSec: 20`）、`:629`（`RateLimitBurst: 40`） |
| UD-1b | 请求体上限 | **1 MiB**（`1 << 20`） | `config/config.go:436`（`DefaultMaxBodyBytes`） |
| UD-1c | 浏览器会话 TTL | **12 小时** | `internal/middleware/session.go:18`（`DefaultSessionTTL`） |
| UD-1d | 可信代理 | **不信任任何代理**：`ClientIP()` 取对端地址，忽略 `X-Forwarded-For` | `cmd/server/main.go:1008`（`configureTrustedProxies`）、`:1020`（在 `setupRouter` 里调用） |
| UD-1e | 单个 orchestration job 超时 | **45 分钟**（`JobTimeoutSeconds: 2700`），env `QUBES_AIR_ORCHESTRATOR_JOB_TIMEOUT_SECONDS` | `config/config.go:337`（字段）、`:675`（默认值）、`internal/orchestrator/runner.go:186`（`DefaultJobTimeout`）、`cmd/server/main.go:623`（接线） |
| UD-1f | `/health` 的编排 dispatcher 心跳：空闲轮询间隔 / 判死阈值 | **5s / 15s**（阈值 = 3 次丢拍）；dispatcher 正在执行 job 时预算再加该 job 的超时（UD-1e）。**无配置键**（编译期常量） | `internal/orchestrator/health.go:11`（`DispatcherPollInterval`）、`:22`（`DispatcherStaleAfter`） |
| UD-1g | `/health` 数据库探测的最小间隔（未认证路由的写节流） | **2s**（窗口内的重复请求复用上次成功；失败不入缓存）；代价是库变为不可写最多晚一个窗口被发现 | `cmd/server/main.go:1300`（`healthProbeInterval`） |
| UD-1h | agent 的 Exec/FileCopy 路径白名单（随 qube 写入 `agent.env`） | **默认都为空 = 该服务在 guest 内禁用**（agent 对空列表直接 `reject(..., 77)`，不是"允许全部"）；Exec 是绝对程序路径、FileCopy 是绝对目录（`/` 被拒），冒号分隔，两侧各自校验 | `config/config.go:237`（`AgentExecAllow`）、`:241`（`AgentFileCopyRoots`）、`:751`/`:754`（env `QUBES_AIR_EXEC_ALLOW`/`QUBES_AIR_FILECOPY_ROOTS`，冒号分隔）；校验 `internal/qrexec/allowlist.go`；写入 `internal/service/cloudinit.go:299` |
| UD-1i | 单实例锁：同一数据库只允许一个 console 进程 | **默认 `<database.dsn>.lock`**（如 `./qubes-air.db` → `./qubes-air.db.lock`，由 DSN 派生，去掉 `?query` 与 `file:` 前缀）；启动时 `flock(2)` `LOCK_EX\|LOCK_NB` 取得并持有到进程退出，**取不到即拒绝启动**，错误文本给出锁文件路径与写入文件的持锁 pid；`lock_file` / env `QUBES_AIR_LOCK_FILE` 可显式覆盖（内存库没有可共享的文件，默认无锁，只能靠它加锁）。flock 是咨询锁、随进程死亡由内核释放 → 残留文件无害、不存在 stale-lock 判断 | `internal/config/config.go:46`（`LockFile` 字段）、`:665`（env）、`:1098`（`LockFilePath` 派生规则）、`internal/lockfile/lockfile.go:60`（`Acquire`）、`:73`（`syscall.Flock`）、`:105`（`Release`）、`cmd/server/main.go:127`（`bootLocked`：先取锁再 boot）、`:87`（main 的唯一调用点）、`:93`（失败即 `log.Fatalf`） |
| UD-23 | console 构建身份：`/health` 的 `version` / `revision` / `build_time` / `tree`，与 `--version`、启动日志报的是同一组值（M2-10 / G-H8） | 链接期由 `-ldflags -X` 注入：`version`＝`git describe --tags --always --dirty` 的**原样**输出（tag 构建＝tag 本身；tag 之后＝`v1.2.3-4-gabcdef`；工作树有未提交改动＝结尾多一个 `-dirty`）、`revision`＝`git rev-parse HEAD`（完整 commit）、`build_time`＝链接时刻（RFC 3339 UTC）；`tree` 由 `version` 的 `-dirty` 后缀解析成 `clean`/`dirty`。**未注入（如不带 `-ldflags` 的 `go build ./cmd/server`）时四个字段一律 `unknown`**——包括 `tree`，不知道就不说成 `clean`，也不报任何形似版本的常量 | `internal/buildinfo/buildinfo.go:39`（`Unstamped`）、`:69`（`TreeUnknown`）、`:95`（`Get`：空值→`unknown`）、`:108`（`String`：`--version` 的单行格式）；消费点 `cmd/server/main.go:1030`（`buildinfo.Get()` 读一次，`/health` 与 `/status` 共用）、`:1251`（`healthHandler` 入参）、`:1228`/`:1229`（`healthBody` 的 `build_time`/`tree` 键）、`:1348`（`statusHandler`）、`:69`（`--version`）、`:73`（启动日志）；注入点 `Makefile` 的 `build-backend` 与 `dev`、`.github/workflows/release.yml` 的 Build console binary 步骤（`version` 等于 release 版本、`revision`/`build_time` 非 `unknown`、`tree` 为 `clean`/`dirty`——逐字段校验，任一缺失即构建失败）；页眉显示的版本也取自这里（`unknown`/不可达时不显示）；读法与实测输出见[升级与回滚](upgrade-rollback.md) §3.2 |

### 1.2 transport（Relay ↔ console / agent）

| # | 默认值 | 取值 | 位置 |
|---|---|---|---|
| UD-2 | keepalive 心跳间隔 | **20s** | `internal/transport/grpc/client.go:59-60`（`withDefaults`：`KeepAlive = 20 * time.Second`） |
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
| UD-14 | agent 允许服务默认值 | **仅 `qubesair.Ping`**（Exec/FileCopy/UnlockData 需显式 opt-in） | `internal/config/config.go:225-230`（注释与 `AgentAllowedServices`）；包内默认落点为 `packaging/agent-deb/qubes-air-agent.service:18`（`QUBESAIR_ALLOW=qubesair.Ping`），可由 `:20` 的 `EnvironmentFile=/etc/qubes-air/agent.env` 覆盖 |

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

### 1.6 qube 规格上下限（M2-12 / G-H10）

校验点在 service，取值来自配置；越界在**入队与 provider 调用之前**拒绝（`Create` 与 `Update`
共用同一个校验器）。两端都是**闭区间**：min 与 max 本身允许，min-1 / max+1 拒绝。**0 不是尺寸而是
"未设置"**：create 时由 `applyDefaultSpec` 换成类型默认值（`internal/service/qube_service.go:424`），
data disk 未设置时由 provider 落 `defaultDataDiskGB = 10`（`internal/provider/proxmox/adapter.go:55`），
所以下限只作用于真正给了值的字段。

| # | 默认值 | 取值 | 位置 |
|---|---|---|---|
| UD-15 | vCPU 上下限 | **1..32**（0 = 未设置） | `internal/config/config.go:363-364`（字段）、`:691-692`（默认值）；服务侧同值 `internal/service/specbounds.go:84-85`；依据：表单自身 `console/frontend/src/components/QubeList.svelte:471`（create）与 `:579`（edit）的 `min="1" max="32"` |
| UD-16 | 内存上下限（MB） | **512..262144**（256 GiB） | `config.go:372-373`（字段）、`:693-694`（默认值）；服务侧 `specbounds.go:86-87`；下限依据：表单 `QubeList.svelte:476` 的 `min="512"` 与 holder VM 自身的 `memory=512`（`internal/provider/proxmox/adapter.go:306`）；**上限无仓库依据，是判断值**（注释已写明） |
| UD-17 | 根盘上下限（GB） | **10..16384**（16 TiB） | `config.go:380-381`（字段）、`:695-696`（默认值）；服务侧 `specbounds.go:88-89`；下限依据：表单 `QubeList.svelte:483` 的 `min="10"`；**上限是判断值** |
| UD-18 | 数据盘上下限（GB） | **1..16384**（16 TiB） | `config.go:387-388`（字段）、`:697-698`（默认值）；服务侧 `specbounds.go:90-91`；下限依据：表单 `QubeList.svelte:488` 的 `min="1"`；**上限是判断值**（这张盘 PVE 不能缩回） |
| UD-19 | GPU 卡数上下限 | **1..8** | `config.go:393-394`（字段）、`:699-700`（默认值）；服务侧 `specbounds.go:92-93`；**无仓库依据**（当前没有任何 provider 读 `Spec.GPU`），纯判断值 |
| UD-20 | 上述 10 个键的 env 绑定 | `QUBES_AIR_QUBE_SPEC_{MIN,MAX}_{VCPU,MEMORY_MB,DISK_GB,DATA_DISK_GB,GPU_COUNT}`；缺失或非法取值保留默认（不会解析成 0） | `config.go:971-980`（逐个绑定）、`:986`（`intFromEnv`：空值与解析失败都回退到当前值） |
| UD-21 | 非法 bounds 的处置 | **启动即失败**：`min < 1` 或 `max < min` 拒绝启动，而不是关掉校验；服务侧另有兜底（非法集合被忽略、保留默认） | `config.go:420`（`QubeSpecConfig.Validate`）、`:1046`（`Config.Validate` 中调用）；兜底 `specbounds.go:209`（`WithSpecBounds`） |
| UD-22 | 越界错误的形状 | `invalid qube spec: <字段> <值><单位> is above the maximum <上限><单位> (qube_spec.max_<键>)`；低于下限同理。前端显示 `message` 字段（此前只显示 `error` 里的 "Bad Request"） | `specbounds.go:146`（`validateSpec`）；HTTP 400 映射 `internal/handler/qube_handler.go:341-342`；前端 `console/frontend/src/lib/api.ts:103`（`errorMessage`） |

> 依据强度分级（不要混用）：UD-15 的上限与 UD-16/17/18 的下限来自仓库里已有的表单约束或代码
> 常量；**UD-16/17/18 的上限、以及 UD-19 整行没有仓库依据**，是按"单机自托管不应被自己绊倒"
> 取的保守值，因此刻意做成可配置键（`AGENTS.md` 要求不得把判断值写成实测值）。
> 相关测试：`internal/service/specbounds_test.go`（边界双向、配置生效、非法配置不关闭校验、
> provider 未被调用）、`internal/config/config_test.go:620` 起（默认值/env/非法值）、
> `cmd/server/specbounds_test.go:19`（配置默认值与服务兜底同值）。

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
