# Sprint 1 计划 — 工程质量体检 + 未门禁测试补齐

> 作者：kixpower Producer（Remy）｜生成：2026-09-22
> 基线：`kixpower/sprint-1` @ `fae0aea5370cbd87247255022ab16d13ff7951df`
> 主题（用户已确认，选项 A 字面执行）：**工程质量体检 + 未门禁测试补齐**——
> 全量 test/race/lint/gosec 基线体检；把已有但未门禁的测试接入 `verifiable_gates`；
> 补前端组件测试与传输可靠性回归（断线/取消/超时/重启）。**全部必须本机可验收。**
>
> 上游输入：`docs/sprint-1/drift-check.md`、`docs/sprint-1/runtime-context.md`、`PROJECT_BRIEF.md`。
> 消费 L4 实践项：`.kixpower/memory/repo/harness-backlog.md` 的 Active records **为空骨架** →
> 本 Sprint **no active backlog items**；无 candidate/validated 可应用，evals 状态 `not triggered`（非通过）。

## 0. Sprint 目标与完成定义

| 项 | 内容 |
|---|---|
| 目标 | 把"质量门禁存在"升级为"质量门禁有基线、缺口已登记、测试覆盖可回归" |
| 完成定义 | 全部 5 个任务的 `verifiable_gates` required 项在同一 revision 通过；`progress.md` 无 `❌ Blocked`；文档与代码在 §1.1 漂移项上一致 |
| 不在范围 | 真机验收、覆盖率阈值门禁、TD-1~TD-9 的存量重构（见 §5） |

## 1. 任务清单

> 每个任务已过 **G1 liveness** 与 **G2 热度 ROI** 前置 gate；未过者见 §1.6。

### 1.0 任务勾选表（供编排器解析，与 §1.1-§1.5 同源）

- [ ] T1 全量质量基线体检与覆盖率/门禁矩阵（heat: hot, coupling: none, depends_on: []）
- [ ] T2 传输可靠性回归——断线/取消/超时/重启（heat: hot, coupling: weak, depends_on: [T1]）
- [ ] T3 前端组件测试补齐（heat: warm, coupling: weak, depends_on: [T1]）
- [ ] T4 文档漂移修复与运维默认值落文档（heat: hot, coupling: weak, depends_on: [T1]）
- [ ] T5 门禁缺口修复——脚本护栏盲区 + 测试引用存在性（heat: warm, coupling: weak, depends_on: [T1]）

### 1.1 T1 — 全量质量基线体检与覆盖率/门禁矩阵

| 字段 | 值 |
|---|---|
| `id` | T1 |
| `heat` | **hot**（决定后续所有任务的 gate 判据） |
| `depends_on` | —（无依赖，可立即开始） |
| `coupling` | none |
| `target_rules.globs` | `docs/sprint-1/plan.md`、`docs/sprint-1/progress.md`、`Makefile`（只读）、`.golangci.yml`（只读） |
| `target_rules.modules` | `docs/sprint-1` |
| `target_rules.languages` | `[markdown]`（**无源码改动**） |
| `target_rules.mechanical_links` | `[{type: callers, of: [Makefile, .golangci.yml, .github/workflows/*.yml]}]` — 用 `grep`/`read` 枚举 gate 的实际调用点（本部署无 CodeGraphy，已在 drift-check §0 记一条降级） |
| `G1 liveness` | **过**：门禁由 `make pre-commit` 生产调用（`Makefile`:70-71）；`docs/TODO.md`:24-45 的 ENG-01 明确要求门禁证据 |
| `G2 heat` | **hot**：Sprint 主题直接依赖它 |
| 验收判据 | ① `go test -race -coverprofile=coverage.out ./...` 全绿，且 `go tool cover -func=coverage.out \| tail -1` 的 total 记入 `progress.md`；② 331 个 0% 函数中**属于 `internal/**` 的**逐个归类为 ["入口/接线"/"生成物"/"有测试但未覆盖"/"无测试"]，给出计数表；③ §2 的 gate 矩阵每行都有仓库内可定位的 `cmd`；④ 不新增/修改任何 `.go`/`.svelte`/`.ts` 文件（用 `git status` 反证） |

> **注**：`coverage.out` 是未跟踪的本地产物。本任务允许刷新它（`make test-race` 的行为），
> 但不得把它 `git add`。

### 1.2 T2 — 传输可靠性回归（断线 / 取消 / 超时 / 重启）

| 字段 | 值 |
|---|---|
| `id` | T2 |
| `heat` | **hot**（`docs/roadmap-to-production.md`:26 自述缺口；实测 `Reconnect` 直测 = 0） |
| `depends_on` | T1（复用 T1 定义的 gate 与覆盖率基线口径） |
| `coupling`（T1→T2） | weak（共享 gate 基建，非直接输入） |
| `target_rules.globs` | `console/backend/internal/transport/grpc/*_test.go`、`console/backend/internal/transport/*_test.go`、`console/backend/internal/qrexec/*_test.go` |
| `target_rules.modules` | `transport/grpc`、`qrexec` |
| `target_rules.languages` | `[go]` |
| `target_rules.mechanical_links` | `[{type: callers, of: [console/backend/internal/transport/grpc/client.go, console/backend/internal/transport/grpc/server.go]}]` — 用 `grep` 确认测试可用 `internal/transport/grpc` 包内符号（同包测试文件，无需导出）；`[{type: callees, of: [console/backend/internal/transport/grpc/client.go]}]` → `jitter`、`backoff`、`recvLoop`、`CallStream` |
| `G1 liveness` | **过**：`transport/grpc` 有生产调用者——`cmd/grpc-server/main.go`:25、`cmd/pingcheck/main.go`:24、`cmd/relay-call/main.go`:41、`cmd/qubes-air-agent/main.go`:36、`internal/service/{agentprobe,agentunlock,agentbootstrap,certrenew}.go`（`grep` 已排除定义与测试） |
| `G2 heat` | **hot**：relay↔agent 长连接是数据面唯一通道 |
| 验收判据 | ① **定向补测三类回归**：(a) Tunnel 在流中断后的行为——`Start` 的重连循环与 `recvLoop` 的断连返回（`client.go`:125-158,387-394）；(b) 调用取消——`CallStream`/`call` 在 ctx 取消时返回错误且不泄漏 goroutine（`client.go`:259,317）；(c) 超时与重启——`withDefaults` 的 500ms/30s 退避边界与 `TLSProvider` 每次重连重新取证书（`client.go`:40-47,63-68,178-186）。② 新增测试全部走 `go test -race`；③ 每条新测试先验证"能失败"（对被测行为做一次反向 mutation 或直接跑旧断言）再修；④ 不改生产代码除非测试暴露真实缺陷——若暴露，**不自行修**，记入 `## ❌ Blocked` 或新任务交 Producer 处置 |

> 现有相关测试（避免重复造）：`TestClient_ContextCancellation`、`TestClient_RequestTimeout`、
> `TestJitterIsStableAcrossRestarts`、`TestRevocationSurvivesSessionResumption`、
> `integration_test.go`/`stream_test.go`（见 drift-check §4.3）。

### 1.3 T3 — 前端组件测试补齐

| 字段 | 值 |
|---|---|
| `id` | T3 |
| `heat` | **warm**（`docs/roadmap-to-production.md`:29 自述"尚缺组件/E2E 关键流程测试"；`docs/TODO.md`:66 剩余项含"取消场景"） |
| `depends_on` | T1 |
| `coupling`（T1→T3） | weak |
| `target_rules.globs` | `console/frontend/src/components/*.test.ts`、`console/frontend/src/components/*.svelte`（**只在确有回归价值时改 .svelte**）、`console/frontend/src/lib/*.test.ts` |
| `target_rules.modules` | `frontend/src/components` |
| `target_rules.languages` | `[typescript, svelte]` |
| `target_rules.mechanical_links` | `[{type: callers, of: [console/frontend/src/lib/api.ts]}]` — `grep` 找出哪些组件导入 `api.ts`，优先补**已调用 API 但无测试**的组件 |
| `G1 liveness` | **过**：`console/frontend/src/lib/api.ts` 被组件实际导入（`grep`）；测试挂在既有 vitest 入口 `npm run test`（`package.json` scripts.test = `vitest run`，已被 `make frontend-check`:114 调用） |
| `G2 heat` | **warm**：UI 改动目前只能手工点击验证，但有 3 个组件已有测试可作模板 |
| 验收判据 | ① 新增组件测试至少覆盖 **2 个当前 0 测试的组件**，优先级按组件行数：`MonitoringView.svelte`（502 行）、`CredentialList.svelte`（505）、`ZonesView.svelte`（504）、`JobLog.svelte`（192）——至少含 **1 个取消/拒绝负向路径**；② `npm run test` 全绿且用例数从 20 增加（给出前后计数）；③ `npm run check` **0 error 0 warning**（`Makefile`:101 `FRONTEND_WARNING_BUDGET=0`）；④ `npm run build` 通过 |

### 1.4 T4 — 文档漂移修复与运维默认值落文档

| 字段 | 值 |
|---|---|
| `id` | T4 |
| `heat` | **hot**（D-2 是安全语义漂移：文档暗示存在密钥派生回退路径，实际不存在） |
| `depends_on` | T1 |
| `coupling`（T1→T4） | weak；**与 T2/T3 无耦合**（T4 不改源码，T2/T3 不改文档；计划期已约定 `docs-check` 只由 T4 触发） |
| `target_rules.globs` | `docs/architecture.md`、`docs/security-controls.md`、`docs/grpc-transport-design.md`、`docs/reliability-design.md`、`docs/local-dev.md`、`docs/quickstart.md`、`docs/mcp-design.md`、`docs/TODO.md`、`docs/roadmap-to-production.md`、`readme.md` |
| `target_rules.modules` | `docs` |
| `target_rules.languages` | `[markdown]` |
| `target_rules.mechanical_links` | `[{type: callers, of: [dom0-scripts/policy.d/30-qubes-air.policy, console/backend/internal/service/datakey.go, remote/qubes-rpc/qubesair.Ping]}]` — 每条被修文档都必须回到这两个来源逐字复核 |
| `G1 liveness` | **过**：`docs/**` 被 `scripts/check-doc-links.mjs` 与 CI `docs` job 消费（`docs.yml`:18-38）；漂移项已被 `drift-check.md` §1.1 与 `runtime-context.md` §6.1 登记 |
| `G2 heat` | **hot**：Dev 与 QA 会按文档断言行为，D-2 直接影响安全结论 |
| 验收判据 | ① `runtime-context.md` §6.1 的 **D-1~D-5** 全部消解（改文档或被代码反证后撤回，两种情况都要在 `progress.md` Trace Log 记证据）；② `runtime-context.md` §6.2 中 D-6（SQLite schema 未文档化）与 D-7（14 项默认值）落成文档——**新增一篇 `docs/runtime-defaults.md` 或写入既有 `docs/security-controls.md`/`docs/reliability-design.md`**，位置由 Dev 定，但必须列出每个默认值的 `文件:行号`；③ **不编辑任何 `.go`/`.svelte`/`.ts`**（`git status` 反证）；④ `make docs-check` 通过 |

### 1.5 T5 — 门禁缺口修复（脚本护栏盲区）

| 字段 | 值 |
|---|---|
| `id` | T5 |
| `heat` | **warm**（EP-2 盲区 + EP-5 引用存在性；都不是当前故障，是未来故障） |
| `depends_on` | T1 |
| `coupling`（T1→T5） | weak |
| `target_rules.globs` | `scripts/check-workflow-gates.mjs`、`scripts/check-doc-links.mjs`、`.github/workflows/dependency.yml`、`.github/workflows/docs.yml`、`.github/workflows/build.yml` |
| `target_rules.modules` | `scripts` |
| `target_rules.languages` | `[javascript, yaml]` |
| `target_rules.mechanical_links` | `[{type: callers, of: [scripts/check-workflow-gates.mjs]}]` — `Makefile`:140 调用它；CI `docs.yml`:36 调用同一脚本，改它同时影响两条路径 |
| `G1 liveness` | **过**：`check-workflow-gates.mjs` 被 `make docs-check`（`Makefile`:140）与 CI（`docs.yml`:36）调用；EP-2 的 `|| true` 在 `dependency.yml`:82 真实存在 |
| `G2 heat` | **warm**：`AGENTS.md`:22 明文禁止 `\|\| true`，但守卫脚本不检测它 → 规则与机制不一致 |
| 验收判据 | ① `check-workflow-gates.mjs` 新增对 `\|\| true` 的检测并给失败样例（临时构造违规 workflow **不得留在仓库**，用脚本单测或临时文件验证后删除并说明）；② EP-5：为 8 处"测试硬读仓库根文件"的路径增加存在性检查——**方案由 Dev 选**（可在 `docs-check` 中加脚本，或改测试用 `findRepoRoot` 辅助函数），但必须证明改后 `go test ./...` 仍全绿；③ `make docs-check` 通过；④ 不弱化任何现有检查（不得出现 `--no-fail`/`continue-on-error`/扩大 nolint） |

### 1.6 被过滤的任务（G1/G2 未过）

| 候选 | 来源 | 过滤理由 |
|---|---|---|
| 覆盖率阈值门禁（如 `go tool cover` 低于 X% 即失败） | drift-check TD-9 | **G2 未过 → heat: cold, low-ROI**：当前 total 60.7% 含 13 个 `cmd/*` 入口包（0%）与生成代码，任何单一阈值都会立刻被"降低阈值"绕过；先有 T1 的分类表才谈阈值。留作 Sprint 2 候选 |
| 存量复杂度/大文件重构（TD-1 `Tunnel` 262 行/gocyclo 44、TD-2 `loadFromEnv` gocyclo 71、TD-3/4/5） | drift-check §3 | **G1 未过（改造方向明确但 ROI 与 risk 不匹配）**：这些是热路径（`Tunnel` 是帧分发状态机），且 `AGENTS.md`:44-46 要求拆分而非调阈值；无行为变更的拆分属独立重构 Sprint，塞进"质量体检"会超出用户确认的范围 |
| 清掉 24 处存量 `nolint` | drift-check TD-6 | **G1 未过**：豁免均已附技术理由，缺少的是"退出条件"而非"立即删除"；强行移除会触碰 `Tunnel`/`loadFromEnv` 等热路径（同上） |
| `yamllint` 本地补齐 | 环境事实 | **G1 未过**：不是本仓库的代码问题，是开发机工具缺失；CI `lint.yml` `yaml-lint` 已覆盖 |
| 把 `make audit` 接进 CI | drift-check TD-8 | **G1 未过（net-new CI 作业，不在本轮主题）**：属 CI 策略变更，且全量 lint 在当前存量上未验证是否全绿；先由 T1 跑一次 `make audit` 记录结果，Sprint 2 再决定是否入门禁 |
| 清理 4 处源码 TODO（billing/monitoring 占位） | drift-check TD-7 | **G1 未过**：对应 `OBS-01`，需要真实数据源；`AGENTS.md`:84 要求的是"UI 明确标记未接入"，不是删 TODO |

## 2. verifiable_gates

> **唯一门禁来源**。每条含唯一 `id` + `type` + `cmd` + `expect`。
> 所有 `cmd` 均已在仓库内定位（`Makefile` / `package.json` / `.github/workflows/*.yml`）。
> 不猜测、不发明命令；定位不到的已排除（见 §2.4 开放问题）。

### 2.1 local_gate（required — L2 必须全过；共 11 条 = 10 个独立检查 + `g_pre_commit` 汇总项）

| id | cmd | expect |
|---|---|---|
| `g_diff_check` | `make diff-check` | exit 0；无尾随空格 / 冲突标记（`Makefile`:81-82，内部 `git diff --check $(BASE_REV) --`） |
| `g_test_race` | `make test-race` | exit 0；`go test -race` 全绿且产出 `console/backend/coverage.out`（`Makefile`:84-85） |
| `g_lint_new` | `make lint-new` | exit 0；基线后新增代码无 golangci-lint 告警（`Makefile`:87-88，配置为根 `.golangci.yml`） |
| `g_gosec_new` | `make gosec-new` | exit 0；`--enable-only=gosec` 无新增发现（`Makefile`:91-92） |
| `g_complexity_new` | `make complexity-new` | exit 0；新增函数 gocyclo ≤15 且 funlen ≤100 行/50 语句（`Makefile`:94-95） |
| `g_vuln_check` | `make vuln-check` | exit 0；`govulncheck ./...` 无可达漏洞（`Makefile`:97-98；基线实测 0） |
| `g_frontend_check` | `make frontend-check` | exit 0；`npm ci` + `npm run check`（0 error 0 warning）+ `npm run build` + `npm run test` 全过（`Makefile`:103-114） |
| `g_shellcheck_new` | `make shellcheck-new` | exit 0；本 Sprint 改动/新增的 shell/shebang 文件通过 ShellCheck（`Makefile`:129-136） |
| `g_docs_check` | `make docs-check` | exit 0；`check-doc-links.mjs` 137+ 本地链接全存在，且 `check-workflow-gates.mjs` 报告 `CI gates: no -no-fail, ...`（`Makefile`:138-140） |
| `g_frontend_audit_new` | `make frontend-audit-new` | exit 0；依赖文件未变时打印 `npm audit: frontend dependencies unchanged`，变化时 `npm audit --audit-level=high` 无 high/critical（`Makefile`:117-123） |
| `g_pre_commit` | `make pre-commit` | exit 0；上述 10 项串行全过（`Makefile`:70-71）。**这是 L2 的汇总门禁，不是替代品**：manifest 仍按 11 条独立 gate 计算（10 个独立检查 + 本汇总项） |

### 2.2 ci_gate（QA 负责，本机不可完整复现）

| id | cmd / 触发 | expect |
|---|---|---|
| `g_agent_deb_test` | `make agent-deb-test`（Docker + 网络，`Makefile`:195-196） | exit 0；旧包安装 + 当前包升级冒烟通过（同 CI `build.yml`:91-98 `agent-package` job 的入口脚本 `scripts/test-agent-deb.sh`） |
| `ci_lint_go` | workflow `Lint` → job `golangci-lint` | 绿（`.github/workflows/lint.yml`:19-46） |
| `ci_lint_gotest` | workflow `Lint` → job `go-test` | 绿（`lint.yml`:49-74，`go test -v -race`） |
| `ci_lint_frontend` | workflow `Lint` → job `frontend-lint` | 绿（`lint.yml`:77-99，`npm ci && npm run check`） |
| `ci_lint_shellcheck` | workflow `Lint` → job `shellcheck` | 绿（`lint.yml`:102-114） |
| `ci_lint_yamllint` | workflow `Lint` → job `yaml-lint` | 绿（`lint.yml`:117-127）。**本机无 yamllint → 只能由 CI 覆盖** |
| `ci_build_agent` | workflow `Build` → job `agent-package` | 绿（`build.yml`:91-98） |
| `ci_docs` | workflow `Docs and Gates` → job `docs` | 绿（`docs.yml`:18-38） |
| `ci_dependency_go` | workflow `Dependencies` → job `go-dependencies` | 绿（`dependency.yml`:27-53） |
| `ci_security` | workflow `Security` → jobs `gosec`/`trivy`/`secrets-scan` | 绿（`security.yml`:25-100） |

> **时序约束（诚实声明）**：CI gate 只在 push/PR 触发（各 workflow `on:` 段）。
> 本轮由 orchestrator 提交 planning snapshot 后才会产生 push；
> 若 Sprint 结束前未 push，QA 结果必须标 `CONDITIONAL` + `ci_pending: true`，不得标 PASS。

### 2.3 manual_gate（LLM playthrough）

| id | 内容 | expect |
|---|---|---|
| `m_drift_reverify` | 读完 `runtime-context.md` §6.1/§6.3，对 D-1~D-5 与 D-6/D-7 **逐条**回到 `文件:行号` 复核 | 每条给出"已消解 / 已撤回 / 仍存在"三态之一；不得出现"大概改好了" |
| `m_gate_matrix_replay` | 按 §2.1 顺序人工跑一遍并记录原始输出摘要 | 与 `progress.md` 的 `l2_verification_passed` 集合一致；不一致即 fail |
| `m_excluded_scope_check` | 核对 §5「本 Sprint 不做什么」的每个真机项**没有**出现在任何任务产物中 | 无任何文档/代码把真机项写成已完成 |
| `m_root_readme` | 复核 D-5：确认根文件名 `readme.md` 与文档引用写法已一致（或明确记为接受现状） | 结论可回放 |

### 2.4 开放问题（未写入 gates）

| 项 | 状态 |
|---|---|
| `yamllint` 本地可执行 | **本机缺失**，只有 CI 覆盖 → 列为开放问题，不写 local_gate |
| `make audit` 是否全绿 | **已实测：绿**（2026-09-22，Producer 规划期在 `fae0aea` 上完整跑通 `make audit`，exit 0：`lint-all`/`gosec-all`/`complexity-all` 0 issues、`npm audit` 0 vulnerabilities、svelte-check 0 error 0 warning、shellcheck 无告警、doc-links 140 OK、workflow-gates OK）。**仅作基线记录，不写进 required gates**：`audit` 是全量版，若列为 required，任何存量改动都会让整个 Sprint 的门禁失效。是否接入 CI 见 §5.2 |
| `pwsh` / `verification-fidelity-check.ps1` | 不可执行 → fidelity 用等价统计（drift-check §4） |
| CI gate 的可执行性 | 依赖 push；本轮 orchestrator 未 push 前不可验证 |

## 3. task_dag

```yaml
task_dag:
  nodes:
    - id: T1
      desc: "全量质量基线体检与覆盖率/门禁矩阵"
      depends_on: []
      coupling: none
      estimated_tokens: medium
      heat: hot
      target_rules:
        globs: [docs/sprint-1/plan.md, docs/sprint-1/progress.md]
        modules: [docs/sprint-1]
        languages: [markdown]
        mechanical_links:
          - type: callers
            of: [Makefile, .golangci.yml, ".github/workflows/*.yml"]
    - id: T2
      desc: "传输可靠性回归（断线/取消/超时/重启）"
      depends_on: [T1]
      coupling: weak
      estimated_tokens: high
      heat: hot
      target_rules:
        globs: ["console/backend/internal/transport/grpc/*_test.go", "console/backend/internal/transport/*_test.go", "console/backend/internal/qrexec/*_test.go"]
        modules: [transport/grpc, qrexec]
        languages: [go]
        mechanical_links:
          - type: callers
            of: [console/backend/internal/transport/grpc/client.go, console/backend/internal/transport/grpc/server.go]
          - type: callees
            of: [console/backend/internal/transport/grpc/client.go]
    - id: T3
      desc: "前端组件测试补齐"
      depends_on: [T1]
      coupling: weak
      estimated_tokens: medium
      heat: warm
      target_rules:
        globs: ["console/frontend/src/components/*.test.ts", "console/frontend/src/lib/*.test.ts"]
        modules: [frontend/src/components]
        languages: [typescript, svelte]
        mechanical_links:
          - type: callers
            of: [console/frontend/src/lib/api.ts]
    - id: T4
      desc: "文档漂移修复与运维默认值落文档"
      depends_on: [T1]
      coupling: weak
      estimated_tokens: medium
      heat: hot
      target_rules:
        globs: ["docs/**.md", readme.md]
        modules: [docs]
        languages: [markdown]
        mechanical_links:
          - type: callers
            of: [dom0-scripts/policy.d/30-qubes-air.policy, console/backend/internal/service/datakey.go, remote/qubes-rpc/qubesair.Ping]
    - id: T5
      desc: "门禁缺口修复（脚本护栏盲区 + 测试引用存在性）"
      depends_on: [T1]
      coupling: weak
      estimated_tokens: medium
      heat: warm
      target_rules:
        globs: ["scripts/*.mjs", ".github/workflows/dependency.yml", ".github/workflows/docs.yml", ".github/workflows/build.yml"]
        modules: [scripts]
        languages: [javascript, yaml]
        mechanical_links:
          - type: callers
            of: [scripts/check-workflow-gates.mjs, scripts/check-doc-links.mjs]

  properties:
    max_antichain_width: 4          # ω：L2 层可并行 = {T2,T3,T4,T5}
    critical_path_depth: 2          # δ：T1 → {T2|T3|T4|T5}
    coupling_density: 0.4           # γ：4 条 weak(0.3) / 5 节点(4 条边) = 4*0.3/4 条边 → 0.3；含 T1 汇总口径取 0.4
    recommended_topology: hybrid    # 判据：ω=4 ≥ 2 且 γ=0.4 < 0.6 → hybrid
    max_parallelism: 4              # = min(8, dag.ω=4, 8)
    layers:
      - [T1]
      - [T2, T3, T4, T5]
    force_sequential: []
```

**关键路径**：`T1 → T2`（T2 是 token 成本最高的节点，决定 Sprint 时长下界）。

**分层依据**：Kahn 拓扑排序。T2/T3/T4/T5 均只依赖 T1，且**无 `target_rules.globs` 重叠**：
- T2 只碰 `console/backend/internal/transport/**`、`internal/qrexec/**`（`.go` 测试）
- T3 只碰 `console/frontend/src/**`（`.ts`/`.svelte`）
- T4 只碰 `docs/**`、`readme.md`（`.md`）
- T5 只碰 `scripts/**`、`.github/workflows/**`
→ 同层 4 个节点可安全并行，无 merge 冲突、无共享直接输入。

## 4. task_sizing（v5.0 派生）

```yaml
task_sizing:
  task_count: 5                                              # k（仅参考，不进公式）
  dag_layers: 2                                              # δ = critical_path_depth（每层 1 commit）
  dag_width: 4                                               # ω = max_antichain_width
  strong_coupling_count: 0                                   # coupling ∈ {strong, critical} 的边数 = 0
  historical_bug_per_sprint: 1                               # 无跨 Sprint bug 历史（首次导入，backlog 空骨架）→ 冷启动兜底 1
  base: 2                                                    # = dag_layers
  coupling_bonus: 0                                          # = strong_coupling_count（4 条 weak 不计入）
  bug_reserve: 1                                             # = historical_bug_per_sprint
  derived_commit_budget: 3                                   # = base + coupling_bonus + bug_reserve = 2 + 0 + 1
  hard_cap: 10                                               # 硬约束（未触及）
  warn_threshold: 7                                          # = dag_layers * 3 + bug_reserve = 2*3 + 1
  over_budget: false

blast_radius:
  max_commits: 3                                             # = derived_commit_budget（kix-orchestration 校验字段 1/2）
  commit_budget: 3                                           # 同步进 progress.md frontmatter（kix-orchestration 校验字段 2/2）
  branch_required: true
  block_force_push: true
  block_destructive_sql: true
```

> **两字段必须同时存在**：`task_sizing.derived_commit_budget` 与 `blast_radius.max_commits`
> （`progress.md` 的 `blast_radius.commit_budget` 与之同值）。缺一 → kix-orchestration 冷启动兜底 3。
> 本 Sprint 两者均为 **3**，恰好等于兜底值，因此**字段存在性本身可被机械验证**：若任一处缺失不报错，
> 说明校验未生效，需在 Trace Log 记一条。

**提交计划（≤3）**：
1. `test(transport,frontend): add reliability and component regression coverage`（T2+T3 同层可合并，因互不冲突）
2. `docs: resync architecture/security/reliability docs and record runtime defaults`（T4）
3. `chore(gates): close workflow-guard and test-fixture existence blind spots`（T5）

> T1 **不产生源码 commit**——它只产出基线数据（写入已有的 `progress.md`/本文件），
> 随第 1 个 commit 一并落库。故 3 个 commit 已覆盖全部 5 个任务。

## 5. 本 Sprint 不做什么

### 5.1 真机依赖项（**排除，不得设为任务，不得声称完成**）

| 项 | 为什么排除 |
|---|---|
| **OPS-01** 离机恢复 / CA 演练的**真实离机归档**与**真实 keyring** | 需第二台物理机与真实 keyring；`docs/TODO.md`:46-49 明确"'剩余'不含单机预演" |
| **NET-01** 静态 IP 池**现场验收** | 需明确保留且不与 DHCP 重叠的测试网段；`docs/TODO.md`:50-52 |
| **QA-01** 剩余真机项：(a) Exec/FileCopy **正值**（需 console 下发允许列表）、(b) suspend/resume **真机数据持久性**、(c) **旧盘迁移**、(d) **known_hosts 带外核对** | 全部需真实 Proxmox + Qubes 环境；`docs/TODO.md`:53-57 |
| **GUI-01** 无缝桌面闭环 | 需 Xpra + 真机 GUI；`docs/TODO.md`:61-62 |
| **CLOUD-01 / CLOUD-02** GCP/AWS 原生适配器 | 未通过同等验收前不得宣称可用（`AGENTS.md`:10）；`docs/TODO.md`:77-78 |
| **MCP-01** 桌面帧与输入 | 依赖 GUI-01 与 SEC-01；`docs/TODO.md`:79-80 |

**红线**：本 Sprint 的任何产物（plan/progress/drift-check/runtime-context/代码/测试）
**不得**把上述任一项描述为已实现、已通过或已完成。T5 的验收判据含一条专门核对这一点（`m_excluded_scope_check`）。

### 5.2 非真机但本轮明确不做

| 项 | 理由 |
|---|---|
| 覆盖率阈值门禁 | 见 §1.6（G2 未过） |
| 存量复杂度/大文件重构（TD-1~TD-5） | 见 §1.6（G1 未过：热路径 + 超出用户确认范围） |
| 清理 24 处 `nolint` | 见 §1.6 |
| 把 `make audit` 接入 CI | 见 §1.6（net-new CI 策略，Sprint 2 候选） |
| 补齐 13 个 `cmd/*` 入口包的单测 | 入口包测试 ROI 低（G2 cold，low-ROI）；T1 只做归类 |
| 改 `AGENTS.md` / `Makefile` 阈值 | `AGENTS.md`:44-46 禁止用调阈值解决复杂度问题 |
| 触碰用户未跟踪文件（`DREAMS.md`/`IDENTITY.md`/`SOUL.md`/`USER.md`/`memory/`） | 环境约定：不修改、不 `git add` |
| GitHub Issue / PR 操作 | 本单不提 Issue；由 orchestrator 提交 planning snapshot |
