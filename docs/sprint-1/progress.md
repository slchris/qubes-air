---
sprint: 1
status: in-progress
last_updated: 2026-09-22
completed_tasks: 0
total_tasks: 5
blocked_tasks: 0
open_issues: {P0: 0, P1: 0, P2: 0}
artifacts_changed_since_last_observe: [PROJECT_BRIEF.md, docs/sprint-1/plan.md, docs/sprint-1/progress.md, docs/sprint-1/drift-check.md, docs/sprint-1/runtime-context.md]
observe_fingerprint: fae0aea5370cbd87247255022ab16d13ff7951df
sprint_baseline_sha: fae0aea5370cbd87247255022ab16d13ff7951df
dev_self_tests_passed: []
l2_verification_passed: []
l2_verified_sha: null
l2_gate_manifest_sha256: PENDING_L2
l2_stash_refs: []
qa_started_sha: null
qa_verified_sha: null
qa_gate_manifest_sha256: null
qa_test_changes: []
ci_pending: false
qa_session_marker: docs/.kixpower-qa-session.json
topology_used: parallel          # 2 分区（p1-go=T1+T2 / p2-nongo=T3+T4+T5），文件级隔离 + 编排器 synthesis；未用 worktree（主树 node_modules 复用、单一提交目标）
task_sizing:
  task_count: 5
  dag_layers: 2
  dag_width: 4
  strong_coupling_count: 0
  historical_bug_per_sprint: 1
  base: 2
  coupling_bonus: 0
  bug_reserve: 1
  derived_commit_budget: 3
  hard_cap: 10
  warn_threshold: 7
  over_budget: false
blast_radius:
  commit_budget: 3
  max_commits: 3
  branch_required: true
  block_force_push: true
  block_destructive_sql: true
---

# Sprint 1 进度 — 工程质量体检 + 未门禁测试补齐

> 主题（用户确认，选项 A 字面执行）：全量 test/race/lint/gosec 基线体检；把已有但未门禁的测试
> 接入 `verifiable_gates`；补前端组件测试与传输可靠性回归（断线/取消/超时/重启）。**全部本机可验收。**
> 计划详见 [`plan.md`](plan.md)；Sprint 基线见 [`drift-check.md`](drift-check.md)；
> 运行态与漂移登记见 [`runtime-context.md`](runtime-context.md)；项目真相源见 `PROJECT_BRIEF.md`。
> 分支：`kixpower/sprint-1`（**不要**在 `main` 提交）。

## 任务清单

- [ ] **T1** 全量质量基线体检与覆盖率/门禁矩阵（hot；无依赖）
- [ ] **T2** 传输可靠性回归——断线 / 取消 / 超时 / 重启（hot；依赖 T1）
- [ ] **T3** 前端组件测试补齐（warm；依赖 T1）
- [ ] **T4** 文档漂移修复与运维默认值落文档（hot；依赖 T1）
- [ ] **T5** 门禁缺口修复——脚本护栏盲区 + 测试引用存在性（warm；依赖 T1）

## 门禁状态

| 层 | 状态 | 说明 |
|---|---|---|
| required local_gate（11 条） | **未开始** | 见 `plan.md` §2.1；`l2_verification_passed` 为空 |
| ci_gate（10 条） | **未开始** | 需 push 触发；未 push 前 QA 只能 `CONDITIONAL` + `ci_pending: true` |
| manual_gate（4 条） | **未开始** | 见 `plan.md` §2.3 |

> **不要**把"planning 期跑过一次 `go test ./...` 全绿"读成 L2 通过：那是单点结果，
> 不等于 plan 的 required gate manifest（11 条）在同一 revision 全过。

## 规划期基线（Producer 实测，2026-09-22，绑定 `fae0aea`）

| 指标 | 值 | 采集命令 |
|---|---|---|
| Go `test -race` | 全绿 | `cd console/backend && go test -race -coverprofile=coverage.out ./...` |
| Go 语句覆盖率 | 60.7% | `go tool cover -func=coverage.out \| tail -1` |
| 0.0% 覆盖函数数 | 331 | `go tool cover -func=coverage.out \| awk '$3=="0.0%"' \| wc -l` |
| Go 测试文件 / `Test*` 函数 | 111 / 765 | `find` + `grep '^func Test'` |
| 前端 vitest | 4 文件 / 20 用例，全过 | `cd console/frontend && npm run test` |
| 前端组件有测试比例 | 3 / 15 | `ls components/` vs `*.test.ts` |
| `govulncheck` 可达漏洞 | 0（1 个不可达） | `cd console/backend && govulncheck ./...` |
| 文档本地链接 | 137 全通过 | `node scripts/check-doc-links.mjs` |
| 存量 gocyclo >15 函数 | 10 | `gocyclo console/backend \| awk '$1>15' \| wc -l` |
| 存量 `nolint` | 24 | `grep -rn nolint console/backend --include=*.go \| wc -l` |
| `Reconnect` 直测 | 0 | `grep '^func Test' --include=*_test.go -r console/backend \| grep -ci Reconnect` |
| **`make audit`（全量）** | **绿（exit 0）** | `make audit`；lint-all / gosec-all / complexity-all = 0 issues；`npm audit` 0 vulnerabilities；svelte-check 0 error 0 warning；doc-links 140 OK；workflow-gates OK |
| `make lint-new` / `gosec-new` / `complexity-new` / `shellcheck-new` | 绿（0 issues / no changed shell files） | 各目标单独实测 |

> `coverage.out` 是**未跟踪**的本地产物；Producer 已用 `make test-race` 等价命令把它刷新到 `fae0aea` 的实测值。
> 任何情况下**不得** `git add` 该文件。
> `make audit` 会重建 `console/frontend/dist/`（生成物）——同样不进 commit。

## L4 实践项消费（backlog）

`.kixpower/memory/repo/harness-backlog.md` 的 Active records 为**空骨架**（mode 0 首次导入，L4 之前无实践证据）。

| 项 | 结论 |
|---|---|
| candidate 可应用项 | 0 → **no active backlog items** |
| validated 可应用项 | 0 |
| evals 状态 | **not triggered**（不是"通过"：无 backlog 项即无可回归的 eval.trigger） |
| pending trial 登记 | 无 |

## Trace Log

```yaml
- turn: 1
  stage: producer_planning
  stage_signal: "plan/progress/drift/runtime artifact 生成 + planning 状态迁移"
  actor: kixpower-producer (Remy)
  head_at_start: fae0aea5370cbd87247255022ab16d13ff7951df
  artifacts_written:
    - PROJECT_BRIEF.md
    - docs/sprint-1/plan.md
    - docs/sprint-1/progress.md
    - docs/sprint-1/drift-check.md
    - docs/sprint-1/runtime-context.md
  source_code_touched: false
  result: ok
  notes: >
    mode 0（已有代码导入）Producer 规划轮。drift check 按 Sprint 1 特例生成 baseline 报告
    （verification_fidelity: baseline），未传 -PrevSprint 0。有测试但未进门禁清单按文件粒度实测为
    空集（make test-race 覆盖全部 111 个 Go 测试文件），真实缺口改写为覆盖率阈值 / 测试引用存在性 /
    全量 audit 自动触发三项，见 drift-check.md §4.2。全部发现均附 file:line 证据。
- turn: 1
  stage: producer_planning
  stage_signal: "tooling adaptation：无 pwsh / 无 CodeGraphy"
  actor: kixpower-producer (Remy)
  result: ok
  notes: >
    verification-fidelity-check.ps1 不可执行（本机无 pwsh）→ fidelity 改用等价统计；
    CodeGraphy 缺失 → 依赖/影响面分析降级为 grep + read，无 index 步骤。两项均已在
    drift-check.md §0 与 plan.md §1.x 的 mechanical_links 注明。
- turn: 1
  stage: producer_planning
  stage_signal: "baseline gate evidence（全量 audit 实测）"
  actor: kixpower-producer (Remy)
  result: ok
  notes: >
    规划期在 fae0aea 上完整跑通 make audit（全量 lint/gosec/复杂度/vuln/前端/shellcheck/docs），
    exit 0，全部 0 issues。此结果回答 plan.md §2.4 的开放问题"make audit 是否全绿"= 是，
    并作为 Sprint 起点基线。仅登记，不把 audit 写进 required gates（全量门禁进 required 会让
    Sprint 门禁被存量改动整体拖垮）。副产物 console/frontend/dist/ 为生成物，不进 commit。
- turn: 1
  stage: producer_planning
  stage_signal: "corrected claim（自我反证）"
  actor: kixpower-producer (Remy)
  result: ok
  notes: >
    初稿把 QUBESAIR_AGENT_SECRET 写成"代码读取点存在"，复检发现只出现在测试
    (internal/agent/invoker_test.go:205)；初稿把 QubesDB /remote-endpoint 写成"本仓库无命中"，
    复检发现 relay/transport/qubesair.GrpcProxy:73 与 qubesair.ConnectTCP:43 实读该键。
    两处均已在 runtime-context.md 更正（前者降为未核对项 U-2，后者进入 §6.2 撤回表 X-1）。
    记此条以保留反证痕迹；不计 claim_evidence_failure（及时发现于交付前）。
- turn: 2
  agent: kixpower-orchestrator
  stage: observe
  artifacts_changed: [PROJECT_BRIEF.md, docs/sprint-1/plan.md, docs/sprint-1/progress.md, docs/sprint-1/drift-check.md, docs/sprint-1/runtime-context.md]
  artifacts_in_scope: 5
  artifacts_out_of_scope: 0
  completed_tasks_delta: 0
  issues_new: []
  estimated_tokens: ~30K
  result: ok
  timestamp: 2026-09-22T15:05:00Z
  notes: >
    Producer 返回后 Observe：5 个工件真实落盘（PROJECT_BRIEF 20KB / plan 349 行 / progress 174 行 /
    drift-check 184 行 / runtime-context 253 行）；progress.md frontmatter 完整（task_sizing +
    blast_radius 双字段在档）。**独立复核结论**（非采信简报）：① plan §2 的 25 条 gate，其 cmd
    全部在仓库内定位成功（Makefile 70-196 行目标 / package.json scripts / 6 个 workflow 的 job 名
    逐一命中）；② D-2 漂移成立（docs/architecture.md 近 83 行称 UnlockData「仍有派生密钥回退路径」，
    而 internal/service/datakey.go 明确 master 只读、永不自动创建、派生键是迁移凭据非解锁凭据）；
    ③ 19 处源码 TODO/FIXME、前端 20 用例、140 条本地链接、`readme.md` 小写名 与实测一致。
    **发现 3 处轻微引用偏差**（不影响 plan 有效性，已随 Dev 分派纠正）：
    `|| true` 实际在 .github/workflows/dependency.yml:81 与 :116（plan §1.5 写 :82 且只提 1 处）；
    docs.yml 调 check-workflow-gates.mjs 在 :35（plan 写 :36）；doc-links 当前 140 OK
    （progress.md 基线表写 137，属旧值）。
  orchestration_decision: >
    plan 的 recommended_topology=hybrid(ω=4) 落地为 2 个 Dev 分区并行 + 文件级隔离（非 worktree）：
    p1-go=T1+T2 / p2-nongo=T3+T4+T5。理由：T4 与 T5 共享 `make docs-check` 这条门禁（T5 改脚本会
    直接改变 T4 的 gate 结果），拆到不同并行分支会产生隐性耦合；合并到同一分区后由单执行者顺序消化。
    Dev 不分头提交，由编排器按 plan §4 的 3 条 commit 消息收口，避免同一工作树的 index 竞争。
```

## ❌ Blocked

<!-- 无阻塞项。若有，按 TEAM_CONVENTIONS 格式补：现象 / 已尝试 / 需要的决策点。 -->

## Sprint+1 候选（本 Sprint 范围外，不擅自扩展）

- 覆盖率阈值门禁（G2 cold, low-ROI：需先有 T1 的覆盖率分类表才能定阈值）
- 存量复杂度/大文件重构：`Tunnel` 262 行/gocyclo 44、`loadFromEnv` 199 行/gocyclo 71
- 把 `make audit` 接入 CI（先由 T1 记录一次实际结果）
- 补齐 13 个 `cmd/*` 入口包测试
- `yamllint` 本地工具补齐（本机缺失，仅 CI 覆盖）

## 真机项状态（**未完成，不得声称完成**）

OPS-01 真实离机归档/keyring · NET-01 静态 IP 池现场验收 · QA-01 剩余 4 项（Exec/FileCopy 正值、
suspend/resume 真机数据持久性、旧盘迁移、known_hosts 带外核对） · GUI-01 无缝桌面闭环 ·
CLOUD-01/02 —— 全部排除在本 Sprint 之外，详见 `plan.md` §5.1。
