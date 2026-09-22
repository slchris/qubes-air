# Runtime Context Snapshot — Sprint 1

> 生成时间：2026-09-22 14:52 (UTC+8)
> 生成者：kixpower Producer（Remy）— 本文件在 `/kixpower-import` 模式下由 Producer 做**初步收集**，
> Dev 启动时按 `templates/runtime-context-snapshot.md` 补全而非重写。
> 模板：`~/.dsh/.agent-presets/kixparadigm/skills/kixpower/templates/runtime-context-snapshot.md`
> 基线：branch `kixpower/sprint-1` @ `fae0aea5370cbd87247255022ab16d13ff7951df`
>
> **硬约束**：本文件只记 env var **key 名**，绝不写 value。已核对到的一切"值"均为代码默认值
> 或文档声明，非部署实例的真实配置。

## 1. 环境变量 keys

### 1.1 来源清单

| 来源文件 | 状态 | 说明 |
|---|---|---|
| `console/backend/config.example.yaml` | 存在（2056 B） | **YAML 结构，不是 env 文件**；只有 8 个顶层键（server/database/cors/security/auth/logging/orchestrator/provider 级别），不含 `QUBES_AIR_*` |
| `docker-compose.yml` | 存在 | 开发栈；`environment:` 段共 7 个 key（见 §1.3） |
| `.env` / `.env.example` / `console/*.env*` | **不存在**（`ls` 确认） | 本仓库没有 env 文件 |
| 代码 `os.Getenv` 面 | 权威来源 | `console/backend/internal/config/config.go` + `cmd/*` |

### 1.2 代码实际读取的 key（`grep -rhoE '"QUBES[A-Z_]*"' console --include=*.go`）

**控制台（`QUBES_AIR_*`）**

| 分组 | keys |
|---|---|
| server / 入口 | `QUBES_AIR_HOST`、`QUBES_AIR_PORT`、`QUBES_AIR_PRODUCTION`、`QUBES_AIR_WEB_ROOT`、`QUBES_AIR_MAX_BODY_BYTES`、`QUBES_AIR_RATE_LIMIT_PER_SEC`、`QUBES_AIR_RATE_LIMIT_BURST` |
| 数据库 / 密钥 | `QUBES_AIR_DATABASE_DSN`、`QUBES_AIR_ENCRYPTION_KEY`、`QUBES_AIR_ENCRYPTION_KEYS` |
| auth / CORS | `QUBES_AIR_API_TOKEN`、`QUBES_AIR_CORS_ORIGINS`、`QUBES_AIR_MCP_TOKEN` |
| TLS | `QUBES_AIR_TLS_ENABLED`、`QUBES_AIR_TLS_CERT`、`QUBES_AIR_TLS_KEY` |
| 编排 | `QUBES_AIR_ORCHESTRATOR_ENABLED`、`QUBES_AIR_ENCRYPT_DATA_DEFAULT`、`QUBES_AIR_REGISTER_REMOTEVM`、`QUBES_AIR_APT_MIRROR`、`QUBES_AIR_APT_SECURITY_MIRROR`、`QUBES_AIR_AGENT_SNIPPET_DATASTORE` |
| agent 投递 | `QUBES_AIR_AGENT_PACKAGE_URL`、`QUBES_AIR_AGENT_PACKAGE_SHA256`、`QUBES_AIR_AGENT_PACKAGE_VERSION`、`QUBES_AIR_AGENT_IDENTITY_DIR`、`QUBES_AIR_AGENT_LISTEN`、`QUBES_AIR_AGENT_REVOCATION_URL`、`QUBES_AIR_AGENT_ALLOWED_SERVICES` |
| agent 调度 | `QUBES_AIR_AGENT_PROBE_INTERVAL_SECONDS`、`QUBES_AIR_AGENT_PROBE_SETTLE_SECONDS`、`QUBES_AIR_AGENT_PROBE_TIMEOUT_SECONDS`、`QUBES_AIR_AGENT_BOOTSTRAP_INTERVAL_SECONDS`、`QUBES_AIR_AGENT_CERT_RENEW_INTERVAL_SECONDS`、`QUBES_AIR_AGENT_CERT_RENEW_THRESHOLD_PERCENT` |
| Proxmox SSH | `QUBES_AIR_PROXMOX_SSH_USERNAME`、`QUBES_AIR_PROXMOX_SSH_KEY_FILE`、`QUBES_AIR_PROXMOX_SSH_KNOWN_HOSTS_FILE` |
| transport | `QUBES_AIR_TRANSPORT_ENABLED`、`QUBES_AIR_TRANSPORT_RELAY_NAME`、`QUBES_AIR_TRANSPORT_REMOTE_NAME`、`QUBES_AIR_TRANSPORT_REMOTE_ENDPOINT`、`QUBES_AIR_TRANSPORT_KEEPALIVE_SECONDS`、`QUBES_AIR_TRANSPORT_RECONNECT_MIN_SECONDS`、`QUBES_AIR_TRANSPORT_RECONNECT_MAX_SECONDS`、`QUBES_AIR_TRANSPORT_CA_FILE`、`QUBES_AIR_TRANSPORT_CERT_FILE`、`QUBES_AIR_TRANSPORT_KEY_FILE`、`QUBES_AIR_TRANSPORT_REVERSE_LOCAL_TARGET`、`QUBES_AIR_TRANSPORT_VAULT_QUBE`、`QUBES_AIR_TRANSPORT_VAULT_CA_NAME`、`QUBES_AIR_TRANSPORT_VAULT_CERT_NAME`、`QUBES_AIR_TRANSPORT_VAULT_KEY_NAME`、`QUBES_AIR_TRANSPORT_VAULT_CERTS` |
| 备份 | `QUBES_AIR_BACKUP_PASSPHRASE` |

**Agent 侧（`QUBESAIR_*`，注意无下划线）**

| key | 读它的位置 |
|---|---|
| `QUBESAIR_ALLOW` | `packaging/agent-deb/qubes-air-agent.service`:18,27 → `--allow "${QUBESAIR_ALLOW}"` |
| `QUBESAIR_EXEC_ALLOW` | `remote/qubes-rpc/qubesair.Exec`:60；`internal/agent/invoker.go`:292（枚举两个 key 用于提示） |
| `QUBESAIR_FILECOPY_ROOTS` | `remote/qubes-rpc/qubesair.FileCopy`（同一枚举路径）；`internal/agent/invoker.go`:292 |
| `QUBESAIR_AGENT_SECRET` | **仅出现在测试**：`internal/agent/invoker_test.go`:205 `t.Setenv(...)`，用于断言秘密不泄漏。生产代码无读取点 → 见 §6.3 U-2 |

> **命名不一致（登记，见 §6）**：控制台侧统一 `QUBES_AIR_*`，agent 侧统一 `QUBESAIR_*`（少一个下划线）。
> 写错前缀不会报错，只会静默取到空值。

### 1.3 `docker-compose.yml` 中显式设置的 7 个 key（仅 key 名）

`QUBES_AIR_HOST`、`QUBES_AIR_PORT`、`QUBES_AIR_DATABASE_DSN`、`QUBES_AIR_ENCRYPTION_KEY`、
`QUBES_AIR_API_TOKEN`、`QUBES_AIR_CORS_ORIGINS`、`QUBES_AIR_ORCHESTRATOR_ENABLED`；
另有前端侧 `VITE_API_TARGET`。

> 该文件同时含**开发用一次性凭据值**（在 git 里，见 `docs/local-dev.md`:36-42 自述）。
> 本文件不转载任何 value。

## 2. SQLite schema 实际结构

- **建表位置**：`console/backend/internal/database/database.go`（**无 migrations 目录**，无 `.sql` 文件；
  `find -iname "*migration*"` 与 `find -name "*.sql"` 均无命中）。
- **schema 版本机制**：`PRAGMA user_version`，当前 `SchemaVersion = 2`（`database.go`:97）；
  迁移由 `applySchemaVersion`（`:150`）与 `addColumnIfMissing`（`:188-200`，先 `PRAGMA table_info`）实现。
  `docs/reliability-design.md`:10 声明"不能把该数据库交给仅支持 schema 1 的旧程序"。
- **表清单（10 张，全部 `CREATE TABLE IF NOT EXISTS`）**

| # | 表 | 定义行 | 主键 | 关键列（节选） |
|---|---|---|---|---|
| 1 | `zones` | `database.go`:239 | `id` | name, type, status(默认 `disconnected`), config(JSON), created_at, updated_at |
| 2 | `qubes` | `:258` | `id` | zone_id, status(默认 `stopped`), ip_address, agent_health(默认 `unknown`), agent_last_probed_at, agent_last_healthy_at, agent_last_error |
| 3 | `qube_infra` | `:288` | `qube_id` | provider, node, storage_vmid, compute_vmid, data_volume, identity_vol, observed_state, **protected(默认 1)**, observed_at |
| 4 | `infrastructure` | `:304` | `id` | name, type, status, region, config(JSON), resource_count |
| 5 | `jobs` | `:329` | `id` | qube_id, qube_name, action, state, error, enqueued_at, started_at, finished_at + 索引 `idx_jobs_qube_id` / `idx_jobs_enqueued_at` / `idx_jobs_state` |
| 6 | `agent_certs` | `:357` | `fingerprint`(SHA-256 DER) | qube_id, subject_cn, issued_at, expires_at, **revoked_at**, revoked_reason, last_seen_at + 2 索引 |
| 7 | `bootstrap_tokens` | `:388` | `secret_hash`（存 hash，不存 token） | qube_id, qube_name, created_at, not_after, **redeemed_at**（单次使用） |
| 8 | `credentials` | `:400` | `id` | name, type, description, **encrypted_data**, **key_version**(默认 1), last_used |
| 9 | `settings` | `:413` | `key` | value, updated_at |

> **设计事实（代码注释明示，文档未复述）**：`qube_infra` 与 `jobs` **刻意不设指向 `qubes` 的外键**
> （`database.go`:283-287, :320-325 注释）。理由：job 创建的数据盘在 Qube 释放后仍须可被接管；
> job 是审计轨迹而非轮询目标。

- **漂移检查**：无文档给出过表清单，因此**不存在"docs 说 X 表 / 实际是 Y 表"的漂移**；
  风险在于文档从未描述 schema（见 §6 漂移 D-6）。

## 3. 上游 API 实际 shape

### 3.1 Proxmox provider（REST over `/api2/json`）

- 认证：先取 ticket —— `POST /api2/json/access/ticket`（`provider/proxmox/client.go`:229,245）；ticket TTL **90 分钟**
  （`client.go`:49 与 `scheduler/proxmox.go`:53 **各定义一份同名常量**）。
- 生产代码实际使用的端点（`grep -noE '"/api2/json[^"]*"' adapter.go client.go`，已排除测试）：

| 用途 | 路径 | 位置 |
|---|---|---|
| 取下一个 VMID | `GET /api2/json/cluster/nextid` | `adapter.go`:122 |
| 集群状态 | `GET /api2/json/cluster/status` | `adapter.go`:512 |
| 列出/创建 VM | `GET|POST /api2/json/nodes/%s/qemu` | `adapter.go`:315 |
| 克隆 | `POST /api2/json/nodes/%s/qemu/%d/clone` | `adapter.go`:375 |
| 读/改配置 | `GET|PUT /api2/json/nodes/%s/qemu/%d/config` | `adapter.go`:156,174,419 |
| 扩容盘 | `PUT /api2/json/nodes/%s/qemu/%d/resize` | `adapter.go`:193 |
| 当前状态 | `GET /api2/json/nodes/%s/qemu/%d/status/current` | `adapter.go`:141,564,610,669 |
| 启动 / 停止 | `POST .../status/start`、`POST .../status/stop` | `adapter.go`:571,617 |
| 删除 | `DELETE /api2/json/nodes/%s/qemu/%d` | `adapter.go`:598,658 |
| guest 网络（取 IP） | `GET .../agent/network-get-interfaces` | `adapter.go`:705 |

- 响应 envelope 约定：`{"data": ...}`（`adapter.go` 各处解析）。
- 默认网络参数：REST 超时 **30s**（`client.go`:77；SSH 同值 `ssh.go`:58），任务轮询 **2s**
  （`client.go`:281、`adapter.go`:80），单响应体上限 **8 MiB**（`client.go`:174 注释）。

### 3.2 gRPC transport（`console/backend/proto/relay_transport.proto`）

```proto
package qubesair.transport.v1;
service RelayTransport { rpc Tunnel(stream Frame) returns (stream Frame); }   // :34-37

message Frame {                       // :45-57
  string request_id = 1;              // 串起一次调用的所有帧
  oneof kind { Handshake|RequestHeader|DataChunk|EndOfStream|CallError|KeepAlive }
}
message Handshake     { protocol_version=1, relay_name=2, remote_name=3, build_version=4 }  // :62-70
message RequestHeader { direction=1, qrexec_service=2, source_qube=3, target_qube=4, deadline_unix_ms=5 }
message DataChunk     { stream_id=1, payload=2 }   // stream_id: 0=请求体, 1=响应体, 2=stderr
message EndOfStream   { stream_id=1, exit_code=2 } // exit_code 仅在 stream_id=1 有意义
enum Direction        { LOCAL_TO_REMOTE=1, REMOTE_TO_LOCAL=2 }
```

- **关键契约**：`protocol_version`（线协议版本）与 `build_version`（仅可观测性）**严格区分**——
  proto 注释 `:62-66` 明确"把两者混为一谈会导致每次发版都断连"。
- 授权只在本地 dom0；远端 agent allowlist 是纵深防御（proto 文件头 `:15-18`、`Direction` 注释 `:76-86`）。
- 生成代码：`internal/transport/relaypb/relay_transport.pb.go`（760 行，覆盖率 0.0%，属生成物）。
- 客户端默认值：keepalive **20s**、重连退避 **500ms → 30s**（指数翻倍 + 抖动）
  （`internal/transport/grpc/client.go`:61,63-68,145-150）。

### 3.3 qrexec 服务接口形状

**远端 agent 侧（`remote/qubes-rpc/`，7 个）**

| 服务 | stdin | stdout | 退出码语义 |
|---|---|---|---|
| `qubesair.Ping` | 无 | 单行 `pong <remote_name> <unix_ts>`（`:10` 注释、`:22` printf） | 0 |
| `qubesair.Exec` | JSON 参数数组（`["/usr/bin/id","-u"]`） | 命令输出（stderr/exit code 独立传递） | 非零 = 结果，非传输错误 |
| `qubesair.FileCopy` | 首行 `push <abs>` 或 `pull <abs>` | 成功含字节数 + SHA256 | 错误写 stderr + 非零 |
| `qubesair.RekeyData` | — | — | 见 `docs/runbook-remotevm.md`:136 |
| `qubesair.UnlockData` | — | — | 含 hardening TODO（`:26`：agent 接受任意 CA 签名客户端证书） |
| `qubes.GetAppmenus` | — | `.desktop` 列表 | — |
| `qubes.StartApp+<app-id>` | — | — | 带 `+arg` |

**Relay 侧（`relay/transport/`，3 个）**：`qubesair.GrpcProxy`（RemoteVM `transport_rpc` 目标）、
`qubesair.SSHProxy`、`qubesair.ConnectTCP`。

**dom0 policy 实际授权的服务名（`dom0-scripts/policy.d/30-qubes-air.policy`）**：
`qubesair.Ping`:45、`qubesair.Status`:48、`qubesair.Exec`:51、`qubesair.Deploy`:54、
`qubesair.SSHProxy`:68,71、`qubesair.VaultRead`:88、`qubesair.GetCredential`:121。

> **不一致**：policy 授权的 `qubesair.Status` / `qubesair.Deploy` / `qubesair.VaultRead` /
> `qubesair.GetCredential` 在 `remote/qubes-rpc/` **没有对应服务脚本**；`qubesair.SSHProxy`/
> `GrpcProxy` 在 `relay/transport/`。前两个（Status/Deploy）未在 `remote` 或 `relay` 任一处找到脚本文件
> → 标为**未核对**（可能由 `qubes-salt-config` 部署，本仓库不含）。

## 4. Health 端点与当前运行状态

### 4.1 端点

| 端点 | 认证 | 语义 | 位置 |
|---|---|---|---|
| `GET /health` | **无**（刻意：liveness probe） | 返回 `status` + `db` 字段；DB `HealthCheck` 失败 → `unhealthy` | `cmd/server/main.go`:912（注册）、`:1066-1073`（handler）；`docker-compose.yml` healthcheck 用它 |
| `GET /api/v1/status` | 需 token（`/api/v1` 中间件链） | 控制台状态 | `main.go`:948 |
| `GET /pki/revocations` | 注册在**根 router**（非 `/api/v1`） | 撤销状态源 | `main.go`:913；`handler/revocation_handler.go`:22 |
| `GET /api/v1/monitoring/metrics` | 需 token | 进程指标 | `handler/monitoring_handler.go`:22 |
| `GET /` | 无 | 静态前端（`WebRoot` 空则 no-op） | `main.go`:974、`registerWebUI` |

**中间件链（`/api/v1` 全体，`main.go`:930-937 顺序即生效顺序）**：
`BodyLimit(MaxBodyBytes)` → `ScopedAuth` → `RateLimit` → `RequireControl` → `Audit` → `RequireZones`。
注释明示 Audit 在 RequireZones **之前**，以便 zone 拒绝也带 subject 与 zone scope 落审计。

### 4.2 本机实际进程状态（2026-09-22 14:52 实测）

| 端口 | 监听者 | 结论 |
|---|---|---|
| 8080 | PID 30624，`go-build/.../server run` | **不是 qubes-air**（`curl /health` → HTTP 404 `404 page not found`） |
| 8098 | 空闲 | qubes-air 本地 compose 栈**未运行**（`docs/local-dev.md` 的宿主端口） |
| 5173 | PID 31007，`node .../ops-platform/web/.../vite.js --host 127.0.0.1 --port 5173` | **是另一个项目的 vite**（`~/.codex/worktrees/00ab/ops-platform`），HTTP 200 |
| 8443 | 空闲 | agent mTLS 监听端口未占用 |

> **结论**：qubes-air 的后端与前端**当前都没有在运行**；8080/5173 被无关进程占用。
> Dev 若要起本地栈，**先确认不误连到 5173 上的另一个项目**——`docs/local-dev.md`:28 已经解释
> 后端改用 8098 正是为避开 8080 占用，但**前端 5173 的冲突在该文档里没有提示**。

## 5. git 状态

| 项 | 值 |
|---|---|
| branch | `kixpower/sprint-1` |
| HEAD | `fae0aea5370cbd87247255022ab16d13ff7951df` |
| 最近 3 个 commit | `fae0aea docs(qa01): record the real Proxmox regression and its findings` / `1dbc87f fix(remotevm): register the dom0 addressing shell as remote-<qube>` / `3d4fdad feat(auth): restrict named tokens to an explicit zone allowlist` |
| staged | 0 |
| modified（tracked） | 0 |
| untracked | `.kixpower/`、`DREAMS.md`、`IDENTITY.md`、`SOUL.md`、`USER.md`、`memory/`、`docs/.kixpower-current-sprint`、`kix-discipline/`（+ 本轮 Producer 新增的 `PROJECT_BRIEF.md`、`docs/sprint-1/*`） |
| `git stash list` | 空 |
| 处置约定 | `DREAMS.md`/`IDENTITY.md`/`SOUL.md`/`USER.md`/`memory/` 是用户既有未跟踪文件，**不得 `git add`、不得修改** |
| 构建产物 | `console/backend/coverage.out`（未跟踪，既有本地产物）；本报告生成时已用 `make test-race` 等价命令刷新为 HEAD 的实测值 |

## 6. 文档漂移登记

> 每条都由 Producer **实际读两侧原文**核对，给出文件与行号。
> 只能在 `docs/` 与代码之间证实"不一致"的才登记为漂移；只能证明"文档未覆盖"的登记为缺口（gap）。

### 6.1 已核实漂移（confirmed）

| ID | docs 说 X | 代码是 Y | 证据（文件:行号） | 严重度 |
|---|---|---|---|---|
| D-1 | `Ping` 返回 `pong` | 返回 `pong <remote_name> <unix_ts>` | `docs/quickstart.md`:63 vs `remote/qubes-rpc/qubesair.Ping`:10,:22 | low |
| D-2 | `qubesair.UnlockData` "仍有派生密钥回退路径" | master 只读、只用于迁移、**永不自动创建**；缺 master 即报错拒绝迁移 | `docs/architecture.md`:83 vs `internal/service/datakey.go`:19-24,:72-81,:230-231 | **high** |
| D-3 | 服务清单（8 行表 / 5 小节） | policy 另授权 5 个未列服务：`qubesair.Status`/`Deploy`/`SSHProxy`/`VaultRead`/`GetCredential` | `docs/architecture.md`:74-83、`docs/grpc-transport-design.md`:62-92 vs `dom0-scripts/policy.d/30-qubes-air.policy`:48,54,68,88,121 | medium |
| D-4 | 单一 Node 版本（20） | `release.yml` 用 Node **22** | `.github/workflows/lint.yml`:15、`build.yml`:15、`dependency.yml`:23 vs `release.yml`:90 | low |
| D-5 | 文档普遍写 `README.md` | 根文件实际是 `readme.md`（全小写）；macOS 上 doc-link 检查大小写不敏感故不报警 | `git ls-files`；`scripts/check-doc-links.mjs`:55-62（`existsSync`） | low |

### 6.2 核对后撤回 / 判定为不成立（保留痕迹，防止重复假设）

| ID | 原假设 | 核对结果 |
|---|---|---|
| X-1 | `docs/grpc-transport-design.md`:57 声称 Relay 使用 QubesDB `/remote-endpoint/<name>`，疑为无实现 | **不成立**：该键在本仓库被实际读取——`relay/transport/qubesair.GrpcProxy`:73-75（`qubesdb-read`）、`relay/transport/qubesair.ConnectTCP`:43；写入方（console 发布端点）在外部仓库，但键本身有效。文档与代码一致。 |

### 6.3 文档缺口（gap，非矛盾但会误导）

| ID | 缺口 | 证据 |
|---|---|---|
| D-6 | **SQLite schema 从未在 docs 中描述**（10 张表、`user_version=2`、刻意无外键的设计理由都只在代码注释里） | `database.go`:97,239-420；`docs/` 全目录 grep 无表名清单 |
| D-7 | **14 个运维默认值未文档化**（限流 20/40、keepalive 20s、重连 500ms→30s、agent 调用超时 2min、job 流 5min、bootstrap 60s、探测 10s、解锁 60s、续期 30s 等） | 见 `docs/sprint-1/drift-check.md` §5 UD-1~UD-14 |
| D-8 | **agent env 前缀命名不一致**（控制台 `QUBES_AIR_*` vs agent `QUBESAIR_*`）从未在 docs 说明；写错前缀静默取空值 | `config.go` 的 `QUBES_AIR_*` 全集 vs `packaging/agent-deb/qubes-air-agent.service`:18,27 与 `remote/qubes-rpc/qubesair.Exec`:60 |
| D-9 | `docs/local-dev.md`:28 只解释了后端避让 8080，**未提示前端 5173 也常被其他项目占用**（本机实测即被占用） | `docs/local-dev.md`:22-32 vs §4.2 实测 |

### 6.4 未核对项（显式声明，不当作已核实）

| ID | 项 | 为何未核对 |
|---|---|---|
| U-1 | `qubesair.Status` / `qubesair.Deploy` 的脚本实现 | 本仓库 `remote/`+`relay/` 无脚本；权威来源是外部 `qubes-salt-config` |
| U-2 | `QUBESAIR_AGENT_SECRET` 的完整读取路径 | 仅确认 key 被读取，未逐行定位消费点 |
| U-3 | `docs/security-controls.md`:33 的"最多 10,000 撤销指纹 / 1 MiB" | 未逐行定位对应常量（本轮未取证，不作为 gate 判据） |
| U-4 | 真实部署实例的 env 值 | 本机无部署，且按硬约束不采集 value |

## 7. Dev 启动检查清单（本文件消费方式）

1. 读 §6 漂移登记**全部条目**后再动手；任何"文档与代码冲突"以**代码为准**。
2. 确认 §4.2 的端口冲突：不要误连 5173 上的其他项目。
3. 起本地栈前确认 8080（本机被无关 Go 进程占用）与 8098 的归属。
4. 需要 env 时只看 §1 的 **key 名**；值从 `config.example.yaml` 或 `docker-compose.yml` 的开发默认取，
   真实环境值不得写入仓库。
5. 发现**新的** runtime 漂移 → 追加到 §6 + `.kixpower/memory/repo/lessons-learned.md`，只写已验证事实。
