# Cross-Sprint Drift Check — Sprint 1（baseline）

> 生成时间：2026-09-22 14:50 (UTC+8)
> 生成者：kixpower Producer（Remy）
> 基线：branch `kixpower/sprint-1` @ `fae0aea5370cbd87247255022ab16d13ff7951df`
> `verification_fidelity: baseline`
> 说明：Sprint 1 是该仓库首次 kixpower 导入（mode 0），**没有前序 Sprint**。
> 因此本次不传 `-PrevSprint 0`（该参数会把不存在的 Sprint 当作可比较对象），
> 而是在此建立 baseline 供 Sprint 2 起对比。

## 0. 工具适配声明（无 pwsh）

| 项 | 状态 |
|---|---|
| `scripts/verification-fidelity-check.ps1` | **未执行**——本机 macOS arm64 无 `pwsh`，kixpower 的 `.ps1` hooks/scripts 一律不可执行 |
| 等价替代 | `grep` / `read` / `sed` / `go test` / `go tool cover` / `Makefile` 与 workflow 文本核对 |
| `Sprint 1 特例` | 无前序 Sprint → 不计算"fidelity 趋势"，只冻结 baseline 指标 |
| 影响 | fidelity 的**量化脚本通道**缺失；本报告的 fidelity 指标全部由人工等价统计得出，已逐条标注命令 |
| CodeGraphy | 本部署无 CodeGraphy MCP → 依赖/影响面分析降级为 `grep` + `read`，无 index 步骤 |

## 1. context drift（上下文漂移）

> 定义：仓库文档、注释、契约描述与当前代码行为不一致，且**尚未登记**。

### 1.1 已核实的漂移（5 条）

| ID | 文档说 | 代码是 | 证据 | 严重度 |
|---|---|---|---|---|
| CD-1 | `docs/quickstart.md`:63「`Ping` 应返回 `pong`」 | 返回单行 `pong <remote_name> <unix_ts>` | 文档 `docs/quickstart.md`:63；代码 `remote/qubes-rpc/qubesair.Ping`:10 注释、:22 `printf 'pong %s %s\n'` | low |
| CD-2 | `docs/architecture.md`:83 把 `qubesair.UnlockData` 的服务表描述为「仍有派生密钥回退路径」 | master **只读、只用于迁移、永不自动创建**；缺 master 时 `LegacyKeyFor` 明确报错拒绝 | 文档 `docs/architecture.md`:83；代码 `internal/service/datakey.go`:19-24（"It is only ever"）、:72-81（"never mints one" + error path）、:230-231（错误文案） | **high**（安全语义：文档暗示存在回退路径，实际不存在） |
| CD-3 | `docs/architecture.md` §远端服务 与 `docs/grpc-transport-design.md` §服务契约 的服务清单 | `dom0-scripts/policy.d/30-qubes-air.policy` 实际授权了 5 个两份文档都未列出的服务：`qubesair.Status`:48、`qubesair.Deploy`:54、`qubesair.SSHProxy`:68,71、`qubesair.VaultRead`:88、`qubesair.GetCredential`:121 | policy 文件行号 vs `docs/architecture.md`:74-83（8 行表格）、`docs/grpc-transport-design.md`:62-92（5 个小节） | medium |
| CD-4 | `.github/workflows/*.yml` 隐含单一 Node 版本（`lint.yml`:15 / `build.yml`:15 / `dependency.yml`:23 = Node 20） | `release.yml`:90 使用 Node **22** | workflow 原文 | low |
| CD-5 | `AGENTS.md` 与多处文档写作 `README.md`；`Makefile` help 亦无此引用 | 仓库根实际文件名是 **`readme.md`**（全小写） | `git ls-files` → `readme.md`；`scripts/check-doc-links.mjs` 在 macOS 上不区分大小写，所以不会报警 | low |

### 1.2 已核对的"无漂移"面（反向证据，避免把"没查"当"没问题"）

| 文档面 | 核对结论 | 证据 |
|---|---|---|
| `docs/local-dev.md` 端口 / token / 编排默认关 | **一致** | `docker-compose.yml` backend `QUBES_AIR_ORCHESTRATOR_ENABLED: "false"`、`BACKEND_PORT:-8098`、`FRONTEND_PORT:-5173`、`QUBES_AIR_API_TOKEN: "devtoken"` |
| `docs/security-controls.md` 会话 TTL 12h | **一致** | `internal/middleware/session.go`:18 `DefaultSessionTTL = 12 * time.Hour` |
| `docs/security-controls.md`:112 请求体上限 1 MiB | **一致** | `internal/config/config.go`:298 `DefaultMaxBodyBytes int64 = 1 << 20` |
| `docs/grpc-transport-design.md`:81 输出上限 16 MiB 由 `capWriter` 强制 | **一致** | `internal/agent/invoker.go`:39 `maxResponseBytes = 16 << 20 // 16 MiB` |
| `docs/grpc-transport-design.md`:31,56 组件 `qubesair.GrpcProxy` / `IssueRelayCert` / `RemoteEndpoints` | **一致** | `relay/transport/qubesair.GrpcProxy` 存在；`cmd/relay-bootstrap/main.go`:41 `issueService = "qubesair.IssueRelayCert"`；`cmd/list-endpoints/main.go`:4 注释 |
| `docs/TODO.md`:63 前端「20 个前端测试」 | **一致** | `grep -c "it("` 于 4 个 `*.test.ts` = 20 |
| `docs/TODO.md`:64 agent deb 安装/升级/卸载由 `make agent-deb-test`（Docker）覆盖、CI 有 `agent-package` job | **一致** | `Makefile`:195-196；`.github/workflows/build.yml`:91 `agent-package` 调 `scripts/test-agent-deb.sh` |
| `AGENTS.md`:31-33 复杂度 gocyclo≤15 / funlen 100 行 50 语句 | **一致** | 根 `.golangci.yml` `gocyclo.min-complexity: 15`、`funlen: lines: 100, statements: 50` |
| `docs/quickstart.md` / `local-dev.md` 的门禁命令 `make check-tools` / `pre-commit` / `audit` | **一致** | `Makefile`:76-79,70-74 |

## 2. error propagation（错误传播）

> 定义：失败是否被静默吞掉、是否被伪造成成功、是否有可观测的失败信号。

| ID | 观察 | 证据 | 判断 |
|---|---|---|---|
| EP-1 | **无伪造通过**：CI 与 Makefile 中未出现 `--no-fail`；`scripts/check-workflow-gates.mjs` 专门守住这条线并已接入 `make docs-check` | `Makefile`:139-140；`check-workflow-gates.mjs`:34-50（检 `-no-fail` / 浮动 action ref / 安全步骤上的 `continue-on-error`） | 良好 |
| EP-2 | **`|| true` 绕过门禁完整性扫描**：`dependency.yml`:82 `npm outdated || true` 用 `|| true` 掩盖非零退出，而 `check-workflow-gates.mjs` 只匹配 `-no-fail` / 浮动 ref / `continue-on-error`，不匹配 `|| true` | `dependency.yml`:82；`check-workflow-gates.mjs`:15-18 `SECURITY_STEP` 正则 + 三类检查 | **gap**（该步骤只是"列过时依赖"，风险低；但守护脚本存在盲区，`AGENTS.md`:22 明文禁止 `|| true`） |
| EP-3 | **撤销/失败关闭路径已覆盖**：agent 侧刷新失败拒绝授权，有正反例测试 | `docs/security-controls.md`:41；`internal/transport/grpc/revocation_resume_test.go`、`role_enforcement_test.go` | 良好 |
| EP-4 | **静默吞错误的白名单已显式审查**：`errcheck.check-blank: false` 并在配置里写明理由与替代方案 | 根 `.golangci.yml` `errcheck` 段注释 | 良好（已声明，非静默） |
| EP-5 | **测试依赖仓库根文件但无存在性门禁**：8 处 Go 测试用相对路径硬读仓库根文件，文件被改名/移动时测试失败于运行期，pre-commit 无"引用文件存在"检查 | `internal/transport/grpc/agent_e2e_test.go`、`issued_cert_e2e_test.go`（`../../../../../remote/qubes-rpc/qubesair.Ping`）；`internal/agent/{filecopy,rekeydata,exec}_service_test.go`、`invoker_test.go`；`internal/service/cloudinit_test.go`（`../../../../packaging/agent-deb/qubes-air-agent.service`） | **gap**（low-medium，Dev 需知悉） |
| EP-6 | **取消/超时的失败状态写入有独立 5 秒期限**，且写入失败也上报 | `docs/reliability-design.md`:14-15 | 良好（文档已声明契约） |

## 3. tech debt（技术债）

> 只列**实测到**的存量热点，全部给出行数或函数名证据。

| ID | 热点 | 实测证据 | 备注 |
|---|---|---|---|
| TD-1 | `(*Server).Tunnel` — 262 行 / gocyclo 44 | `console/backend/internal/transport/grpc/server.go`:347；gocyclo 输出 | 唯一同时超 gocyclo 与 funlen 的函数；`.go:346` 有 `//nolint:gocyclo,funlen // frame dispatch plus lifecycle, kept together deliberately` |
| TD-2 | `(*Config).loadFromEnv` — 199 行 / gocyclo **71**（全仓最高）；`(*Config).Validate` gocyclo 28 | `internal/config/config.go`:589、:793 | `.go:588`、`:792` 各有 `//nolint:gocyclo` 并注明"flat per-field sequence" |
| TD-3 | gocyclo >15 的存量函数共 **10 个**（含测试） | `gocyclo console/backend \| awk '$1>15' \| wc -l` = 10 | 测试侧有 `.golangci.yml` 的 `_test.go` 例外 |
| TD-4 | 1200 行级文件 4 个 | `service/qube_service.go` 1208、`service/certrenew.go` 1192、`cmd/server/main.go` 1179、`service/certrenewsched.go` 1047（`wc -l`） | 单文件多职责 |
| TD-5 | 前端最大组件 `QubeList.svelte` **970 行** | `wc -l console/frontend/src/components/*.svelte`；次大者 505/504/502 | 组件内聚度低 |
| TD-6 | 存量 `nolint` **24 处**，无"何时可移除"的退出条件 | `grep -rn nolint console/backend --include=*.go` = 24 | 均有具体理由（符合 `AGENTS.md`:40-42） |
| TD-7 | 源码内真·待办 4 处，全部落在"占位数据"路径 | `handler/billing_handler.go`:50,56；`handler/monitoring_handler.go`:53,55；另 `remote/qubes-rpc/qubesair.UnlockData`:26 有 hardening TODO | 对应 `OBS-01`/`UI-01`；UI 必须继续标记"未接入" |
| TD-8 | `make audit`（全量版）**已实测全绿但不在任何 CI workflow 中** | `make audit` 于 `fae0aea` 实测 exit 0（lint-all/gosec-all/complexity-all 0 issues；`npm audit` 0 vulnerabilities；svelte-check 0 error 0 warning）；`grep -rn "make audit" .github/workflows/` 无命中；对照 `Makefile`:73-74 | 全量门禁只在本地或人工触发，release 前依赖纪律——**门禁存在且绿，缺的是自动触发** |
| TD-9 | 覆盖率只有产物、没有门禁阈值 | `Makefile`:84-85 只 `-coverprofile=coverage.out`，无阈值校验；CI `lint.yml`:67 同样只上传 artifact | 覆盖率可无声下降 |

## 4. verification fidelity（验证保真度）— baseline

### 4.1 量化脚本通道：**未执行**

`scripts/verification-fidelity-check.ps1` 依赖 `pwsh`，本机无。以下为**等价统计**，逐条附命令。

### 4.2 "有测试但未进门禁"清单（等价统计结果）

**关键纠正（与原假设相反，按实测报告）**：

```
$ find console/backend -name '*_test.go' | wc -l   → 111
$ grep -n "test-race" -A1 Makefile                 → cd console/backend && go test -race ... ./...
```

`make test-race` 执行 `go test ./...` 于整个 `console/backend` 模块，**覆盖全部 111 个 Go 测试文件**。
即：在**文件级**不存在"有测试但未进门禁"的 Go 包。原任务描述里该假设不成立于文件粒度，
真正的缺口在**更细的粒度**（见下表）。

| 粒度 | 是否进 pre-commit | 是否进 CI | 证据 |
|---|---|---|---|
| Go 测试（111 文件 / 765 个 `Test*`） | ✅ `make test-race` | ✅ `lint.yml` `go-test` job（`go test -v -race`） | `Makefile`:84-85；`lint.yml`:67 |
| 前端 vitest（4 文件 / 20 用例） | ✅ `make frontend-check` → `npm run test` | ✅ `build.yml`:78 `npm run test` | `Makefile`:114；`build.yml`:78 |
| Go **覆盖率阈值** | ❌ 只生成 `coverage.out`，无阈值 | ❌ 只上传 artifact | `Makefile`:84-85；`lint.yml`:67-75 |
| Go 测试对**仓库根文件**的引用存在性 | ❌ 无检查（8 处硬编码相对路径） | ❌ 无检查 | 见 EP-5 |
| `make audit`（全量 lint/gosec/复杂度/shellcheck） | ⚠️ 独立目标，不在 `pre-commit` 链内 | ❌ 无 workflow 调用 | `Makefile`:73-74；`grep` 无命中 |
| `make agent-deb-test`（Docker 冒烟） | ❌ **刻意**排除（`Makefile`:193-194 注明需 Docker+网络） | ✅ `build.yml` `agent-package` job | `build.yml`:91-98 |
| `make frontend-audit`（全量 npm audit） | ⚠️ 仅依赖文件变化时跑 `frontend-audit-new` | ✅ `dependency.yml` `npm-dependencies` | `Makefile`:117-126；`dependency.yml`:78 |
| yamllint | ❌ 本机工具缺失 | ✅ `lint.yml` `yaml-lint` job | `lint.yml`:117-127 |

**结论**：真正未门禁的是**覆盖率**、**测试引用的仓库根文件存在性**、**全量 `make audit` 的自动触发**，
而不是"某些测试文件没被跑"。

### 4.3 baseline 指标（供 Sprint 2 起对比）

| 指标 | baseline 值 | 采集命令 |
|---|---|---|
| Go 模块包数 | 35 | `cd console/backend && go list ./... \| wc -l` |
| Go 测试文件数 | 111 | `find console/backend -name '*_test.go' \| wc -l` |
| Go `Test*` 函数数 | 765 | `grep -rn '^func Test' --include=*_test.go console/backend \| wc -l` |
| Go 语句覆盖率 | **60.7%** | `cd console/backend && go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out \| tail -1` |
| 0.0% 覆盖的函数数 | **331** | `go tool cover -func=coverage.out \| awk '$3=="0.0%"' \| wc -l` |
| 前端测试文件 / 用例 | 4 / 20 | `find console/frontend/src -name '*.test.ts'`; `grep -c 'it('` |
| 前端组件有测试比例 | 3 / 15（`LoginGate`、`QubeList`、`SettingsView`） | `ls console/frontend/src/components/` vs `*.test.ts` |
| 传输重连直测 | **0** | `grep '^func Test' console/backend --include=*_test.go -r \| grep -ci Reconnect` = 0 |
| `internal/qrexec` 覆盖率 | 32.1% | `go test ./...` 输出行 |
| 存量 gocyclo>15 函数 | 10 | `gocyclo console/backend \| awk '$1>15' \| wc -l` |
| 存量 `nolint` | 24 | `grep -rn nolint console/backend --include=*.go \| wc -l` |
| `govulncheck` 可达漏洞 | 0（1 个不可达） | `cd console/backend && govulncheck ./...` |
| 文档本地链接 | 137 全通过 | `node scripts/check-doc-links.mjs` |
| 测试可靠性关键词命中 | Cancel 2 / Timeout 7 / Disconnect 3 / Reconnect **0** / Restart 3 / Resume 14 / Retry 4 / Race 2 / Concurrent 3 | `grep '^func Test' --include=*_test.go -r console/backend \| grep -ci <kw>` |

### 4.4 `dev_self_tests_passed` / `l2_verification_passed` 现状

`l2_verification_passed` 本轮为空——planning 阶段尚未执行 L2。生产者只登记 baseline，
**不得**把本节的"测试全绿"读作 L2 通过：那是 `go test ./...` 单点结果，不等于 plan 的 required gate manifest。

## 5. 已有但未文档化的决策（从代码反推）

> 这些决策**真实生效**，但没有任何 `docs/*.md` 描述它们的取值。
> 判据：以 grep 在 `docs/` 全目录搜不到该常量名或其取值。

| ID | 生效决策（实测值） | 位置 | 文档覆盖 |
|---|---|---|---|
| UD-1 | 限流默认 **20 req/s、burst 40** | `internal/config/config.go`:490-491 | 未文档化（`docs/security-controls.md` 只提"限流"存在） |
| UD-2 | 传输 keepalive 默认 **20s** | `internal/transport/grpc/client.go`:61 | 未文档化 |
| UD-3 | 重连退避默认 **min 500ms / max 30s**（指数翻倍 + 抖动） | `internal/transport/grpc/client.go`:63-68,145-150 | 未文档化（`grep docs/ 500\|30s\|ReconnectMax` 无命中） |
| UD-4 | agent 调用默认超时 **2 分钟**，子进程 `WaitDelay` **2s** | `internal/agent/invoker.go`:35,228 | 未文档化 |
| UD-5 | pending renewal TTL **5 分钟** | `internal/agent/renewal.go`:54 | 未文档化 |
| UD-6 | job 流式响应最长 **5 分钟**（`streamMaxDuration`） | `internal/handler/job_handler.go`:172 | 未文档化 |
| UD-7 | Proxmox API ticket TTL **90 分钟**（`client.go` 与 `scheduler/proxmox.go` 各定义一份常量） | `internal/provider/proxmox/client.go`:49；`internal/scheduler/proxmox.go`:53 | 未文档化；**两处重复定义**是潜在漂移点 |
| UD-8 | Proxmox REST 默认超时 **30s**、任务轮询 **2s** | `internal/provider/proxmox/client.go`:77,281；`adapter.go`:80 | 未文档化 |
| UD-9 | agent 探测默认超时 **10s**；agent 就绪结算预算 **5 分钟 / 重试 15s** | `internal/service/agentprobe.go`:71；`internal/service/agenthealth.go`:27,29 | 未文档化 |
| UD-10 | 数据盘解锁默认超时 **60s**；解锁证书寿命 **5 分钟** | `internal/service/agentunlock.go`:48,33 | 未文档化 |
| UD-11 | bootstrap 默认超时 **60s**；bootstrap 重试 **base 15s / max 10min** | `internal/service/agentbootstrap.go`:66；`internal/service/bootstrapsched.go`:49-50 | 未文档化 |
| UD-12 | 证书续期默认超时 **30s**、重试 **base 15min / max 6h**、时钟偏移余量 **24h**、回签 **5min** | `internal/service/certrenew.go`:50,96；`certrenewsched.go`:115,125,134 | 未文档化 |
| UD-13 | MCP 默认 API 超时 **15s**；MCP 消息上限 **4 MiB**、响应体 **8 MiB** | `internal/mcp/client.go`:19,22；`protocol.go`:40 | `docs/mcp-design.md`:32 只提 API BodyLimit 1 MiB |
| UD-14 | agent 允许服务默认值 = **仅 `qubesair.Ping`**（cloud-init 注入 `QUBESAIR_ALLOW=qubesair.Ping`） | `internal/config/config.go`:207 注释；`packaging/agent-deb/qubes-air-agent.service`:18；测试 `service/cloudinit_test.go`:208 | `docs/architecture.md`:80 提"默认关闭"，未给默认值文本 |

**判据可复现**：对每个默认值的常量名做 `grep -rlE "<const>" docs/ | grep -v '^docs/sprint-1/'`，
在**既有**（非 Producer 本轮新增）文档中全部零命中：

```
[ticketTTL] [DefaultAPITimeout] [DefaultAgentProbeTimeout] [DefaultDataUnlockTimeout]
[DefaultBootstrapTimeout] [DefaultCertRenewalTimeout] [DefaultCallTimeout]
[ReconnectMin|ReconnectMax] [keepalive|KeepAlive] [RateLimitPerSec|rate_limit_per_sec]
[streamMaxDuration] [maxResponseBytes] [DefaultSessionTTL] [DefaultMaxBodyBytes]
→ 全部 NONE（仅 docs/sprint-1/ 本轮文件命中，已排除）
```

> 注意与 §1.2 的"无漂移"面区分：`DefaultSessionTTL`（12h）与 `DefaultMaxBodyBytes`（1 MiB）
> 的**取值**在 `docs/security-controls.md` 中有描述，但**常量名**零命中。
> 也就是说文档能对，但核查者无法从文档反查到代码位置——这正是 D-7 要补的缺口。

**处置**：UD-1 ~ UD-14 是 Sprint 1 T4 的输入（写入 `docs/` 或在 `runtime-context.md` 保留为登记项）。
本报告只**登记**，不改文档——漂移修复是 plan 里的任务，不是 drift check 的动作范围。

## 6. 与 plan 的衔接

| 本节发现 | 对应 plan 任务 |
|---|---|
| §4.2 覆盖率无门禁、§4.3 baseline 指标 | T1（基线体检 + gate 定义） |
| §4.3 重连直测 = 0、Cancel/Disconnect 稀疏 | T2（传输可靠性回归） |
| §4.3 前端 3/15 组件有测试 | T3（前端组件测试） |
| §1.1 CD-1~CD-5、§5 UD-1~UD-14 | T4（文档漂移修复） |
| §2 EP-2（`|| true` 盲区）、EP-5（引用存在性） | T5（门禁缺口修复） |
| §3 TD-1~TD-9 | 不在本 Sprint；`PROJECT_BRIEF.md` §9.2 已登记为风险 |
