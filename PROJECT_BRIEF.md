---
project: qubes-air
content_language: repo
content_language_source: inferred
model_context_window: 128000
model_context_window_source: default
kixpower_mode: 0
current_sprint: 1
brief_revision: 1
brief_generated: 2026-09-22
brief_basis_sha: fae0aea5370cbd87247255022ab16d13ff7951df
verified_provider: proxmox
---

# PROJECT_BRIEF — Qubes Air

> 本文件由 kixpower Producer（Remy）在 mode 0（已有代码导入）下**从当前代码反推**。
> 叙述区跟相邻 `docs/*.md` 使用中文（`content_language: repo`）。
> 证据基线为 `brief_basis_sha`；任何与之不符的断言都应以当前代码为准。

## 1. 项目定位与使命

Qubes Air 让本地 Qubes AppVM 通过熟悉的 qrexec 接口使用远端普通 Linux VM，同时保留本地
dom0 的 policy 决策，并把基础设施凭据、传输身份和远端工作负载分开（来源：`docs/architecture.md` §目标）。

- 一句话：**在保持 dom0 授权根不变的前提下，把远端 VM 变成可被 qrexec 调用的工作负载。**
- 当前定位：受控实验与开发可用，**不是**通用生产发行版（来源：`docs/roadmap-to-production.md`:6）。
- 唯一完成真机闭环的 provider：Proxmox（来源：`AGENTS.md`:10）。

## 2. 当前状态评估

| 维度 | 实测证据（2026-09-22，HEAD `fae0aea`） | 状态 |
|---|---|---|
| Go 测试 | `go test -race -coverprofile=coverage.out ./...` 全绿；语句覆盖 **60.7%**；765 个 `Test*` 函数 | 绿，覆盖未知项仍多 |
| Go 包 | `go list ./...` = **35 个包**（13 `cmd/*` + 22 `internal/*`） | — |
| 前端测试 | vitest 20 个用例 / 4 个测试文件（`docs/TODO.md`:63 自述一致） | 薄，仅 3 个组件 |
| 本地门禁 | `make pre-commit`（11 个目标）、`make audit`（全量版）已在 `Makefile`:70-74 定义 | 存在且可跑 |
| CI | 7 个 workflow（build/codeql/dependency/docs/lint/release/security） | 存在 |
| 依赖漏洞 | `govulncheck ./...` → 0 个可达漏洞（1 个不可达） | 绿 |
| 复杂度存量 | `gocyclo` >15 的存量函数 **10 个**（含 `_test.go`），全部带 `//nolint` 并注明理由 | 已声明，非静默 |
| 文档 | `node scripts/check-doc-links.mjs` → 137 个本地链接全通过 | 绿 |
| 真机验收 | QA-01 Proxmox 生命周期已真机通过（`docs/TODO.md`:53-57） | 部分，剩余项见 §8 |

**总体判断**：工程质量面显著好于典型导入项目（门禁齐、真机有记录、文档成体系），
主要缺口集中在**测试覆盖度未知**与**文档/代码漂移未登记**两类，这正是 Sprint 1 的靶心。

## 3. 技术栈与版本矩阵

| 层 | 技术 | 版本证据 |
|---|---|---|
| 后端语言 | Go | `console/backend/go.mod`:3 `go 1.26.0`；本机 `go1.26.8` |
| Web 框架 | Gin | `go.mod` gin `v1.9.1` |
| 存储 | SQLite（cgo） | `go.mod` `mattn/go-sqlite3 v1.14.22` |
| RPC | gRPC / protobuf | `console/backend/proto/relay_transport.proto` |
| 前端 | Svelte 5 + TS 5.9 + Vite 7 | `console/frontend/package.json` devDependencies |
| 前端测试 | Vitest 5 + Testing Library + jsdom | `package.json`；配置 `vitest.config.ts` |
| 门禁工具 | golangci-lint 2.x / gosec / gocyclo / funlen / govulncheck / shellcheck | `Makefile`:65-98；根 `.golangci.yml` |
| CI 运行时 | Go 1.26、Node 20（build/lint/dependency/docs） | `.github/workflows/*.yml` env |

**已知不一致（登记，不在本 Sprint 修）**：`release.yml`:90 用 Node **22**，其余 workflow 用 Node 20；
本机 Node 为 **22.14.0**。

## 4. 关键架构决策与约束（已有代码反推）

| # | 决策 | 证据 | 约束含义 |
|---|---|---|---|
| AD-1 | 本地 dom0 是正常 RemoteVM 调用的唯一授权根，远端没有第二个 policy | `docs/architecture.md` §安全边界 1 | 任何"绕开 dom0"的设计都违反信任模型 |
| AD-2 | 不保留 Terraform/OpenTofu；`qube_infra` 表是资源身份唯一真源 | `docs/architecture.md` §存算分离；`internal/database/database.go`:288 | 资源身份必须落库后才允许产生副作用 |
| AD-3 | provider 生命周期拆成 `suspend`（销毁计算）+ `resume`（从模板重建并挂回同一数据盘） | `docs/architecture.md` §存算分离 | 数据盘是持久边界，计算是短生命周期 |
| AD-4 | agent 身份强制 CA 签名撤销状态；刷新失败拒绝授权 | `docs/security-controls.md`；`internal/agent/*` | 撤销状态源可达性是**新增部署要求** |
| AD-5 | 数据盘用独立随机 DEK；legacy master 只读、只用于迁移、**永不自动创建** | `internal/service/datakey.go`:19-24,72-81 | 缺 master 时迁移失败并报错，不得静默派生 |
| AD-6 | Exec 用有界 JSON argv + 直接 `execve`，无 shell；FileCopy 用目录描述符 + `O_NOFOLLOW` | `docs/security-controls.md`:97-100；`internal/agent/invoker.go` | 禁止回退到 shell 文本入口 |
| AD-7 | 请求取消后的失败状态写入有独立 5 秒期限 | `docs/reliability-design.md`:14-15 | 取消不是"不落状态"的理由 |
| AD-8 | Console 不进入数据面：只发布端点与签证书 | `docs/architecture.md` §数据面 | console 不进 RemoteVM 调用链路 |
| AD-9 | Qubes 侧部署的唯一权威来源是外部仓库 `qubes-salt-config` | `AGENTS.md`:12 | 本仓库不复制平行部署入口 |
| AD-10 | 只维护当前架构，不为未声明的旧环境保留兼容分支 | `AGENTS.md`:9 | 死代码应立即删除而非标记废弃 |

## 5. 仓库结构与模块地图

```
qubes-air/
├── console/
│   ├── backend/          Go 1.26 + Gin + SQLite + gRPC（35 个包，234 个 .go）
│   │   ├── cmd/          13 个入口：server / qubes-air-agent / qubes-air-mcp / relay-* / pingcheck …
│   │   ├── internal/     22 个包：agent audit backup config database handler keyring mcp
│   │   │                 middleware models orchestrator pki provider providerhttp qrexec
│   │   │                 repository scheduler service transport transport/grpc transport/relaypb
│   │   └── proto/        relay_transport.proto（service RelayTransport { rpc Tunnel(stream Frame) }）
│   ├── frontend/         Svelte 5 + TS 5.9 + Vite 7（16 个 .svelte，4 个测试文件）
│   └── qrexec/           —（目录存在，服务脚本实际在 remote/ 与 relay/）
├── crypto/               密钥生成脚本
├── dom0-scripts/         create-remotevm / create-sys-relay / policy.d/30-qubes-air.policy
├── remote/qubes-rpc/     远端 agent 服务：Ping Exec FileCopy RekeyData UnlockData + qubes.GetAppmenus/.StartApp
├── relay/transport/      Relay 侧服务：GrpcProxy SSHProxy ConnectTCP
├── packaging/agent-deb/  agent .deb 定义 + qubes-air-agent.service
├── packer/scripts/       —（仅 scripts，无模板）
├── salt/                 pillar 示例（权威 states 在 qubes-salt-config）
├── scripts/              门禁脚本：check-doc-links.mjs / check-workflow-gates.mjs / *-agent-deb.sh
└── docs/                 专题文档 + reviews/ 历史证据 + sprint-N/ 过程文档
```

来源：`find`/`ls` 实测 + `go list ./...` + `console/backend/proto/relay_transport.proto`:34-37。

## 6. 质量门禁与工具链现状

| 门禁 | 命令 | 真实来源 |
|---|---|---|
| 增量总门禁 | `make pre-commit` | `Makefile`:70-71（11 个目标串行） |
| 全量审计 | `make audit` | `Makefile`:73-74 |
| diff 空白/冲突标记 | `make diff-check` | `Makefile`:81-82 |
| Go 测试 + race + 覆盖 | `make test-race` | `Makefile`:84-85 |
| lint（增量/全量） | `make lint-new` / `make lint-all` | `Makefile`:87-88,142-143 |
| 安全扫描 | `make gosec-new` / `make gosec-all` | `Makefile`:91-92,145-146 |
| 复杂度 | `make complexity-new` / `-all` | `Makefile`:94-95,148-149 |
| 依赖漏洞 | `make vuln-check` | `Makefile`:97-98 |
| 前端 | `make frontend-check`（`npm ci` + `check` + `build` + `test`） | `Makefile`:103-114 |
| 前端依赖漏洞 | `make frontend-audit-new` / `-audit` | `Makefile`:117-126 |
| Shell | `make shellcheck-new` / `-all` | `Makefile`:129-136,151-153 |
| 文档 + CI 门禁完整性 | `make docs-check` | `Makefile`:138-140 |
| agent 包安装冒烟 | `make agent-deb-test`（Docker + 网络） | `Makefile`:195-196 |

**复杂度阈值（代码实测）**：`gocyclo.min-complexity: 15`、`funlen: lines 100 / statements 50`，
配置在**仓库根** `.golangci.yml`（`console/backend/.golangci.yml` 不存在）。测试文件对
`gocyclo/funlen/dupl/errcheck/gosec/noctx/gocritic/goconst/unparam` 有窄例外（`.golangci.yml` exclusions）。

**门禁环境缺口**：
- `yamllint` 本机缺失 → CI 的 `lint.yml` `yaml-lint` job 本地不可复现。
- 本机无 `pwsh` → kixpower 的 `skills/kixpower/**/*.ps1`（hooks / verification-fidelity-check）
  一律不可执行，等价检查改由 shell/grep/read 承担（见 `docs/sprint-1/drift-check.md` §4）。

## 7. 当前 Sprint（Sprint 1）

> 本节在 Sprint 收尾时由 Producer 改写为"已完成"。当前为 planning。

**主题**：工程质量体检 + 未门禁测试补齐（选项 A，字面执行）。

| 项 | 内容 |
|---|---|
| 范围 | 全量 test/race/lint/gosec 基线体检；把已有但未门禁的测试接入 `verifiable_gates`；补前端组件测试与传输可靠性回归（断线/取消/超时/重启） |
| 本机可验收 | 全部任务都必须在本机（macOS arm64）可跑通，无真机依赖 |
| 计划任务数 | 5（见 `docs/sprint-1/plan.md`） |
| 拓扑 | 见 `docs/sprint-1/plan.md` `task_dag.properties.recommended_topology` |
| 交付物 | `docs/sprint-1/{plan,progress,drift-check,runtime-context}.md` + 源码测试补强（Dev 执行） |

## 8. 下一 Sprint 候选池

| 候选 | 来源 | 为何不在 Sprint 1 |
|---|---|---|
| QA-01 剩余真机项（Exec/FileCopy 正值、suspend/resume 数据持久性、旧盘迁移、known_hosts 带外核对） | `docs/TODO.md`:56-57 | 需真实 Qubes/Proxmox 环境与 console 下发允许列表 |
| OPS-01 离机恢复与 CA 演练的真实离机归档 / 真实 keyring | `docs/TODO.md`:46-49 | 需第二台机器与真实 keyring |
| NET-01 静态 IP 池现场验收 | `docs/TODO.md`:50-52 | 需保留的测试网段 |
| GUI-01 无缝桌面闭环 | `docs/TODO.md`:61-62 | 需 Xpra + 真机 GUI |
| UI-01 设置接入（session timeout / 2FA / 邮件 / webhook） | `docs/TODO.md`:73-74 | 产品功能，非本轮质量主题 |
| OBS-01 真实监控、告警与账单 | `docs/TODO.md`:75-76 | 依赖外部数据源 |
| CLOUD-01/02 GCP/AWS 原生适配器 | `docs/TODO.md`:77-78 | 未通过同等验收前不得宣称可用 |
| QA-02 剩余：真实首次 bootstrap、应用启动 E2E、取消场景 | `docs/TODO.md`:66 | 部分可本机做，Sprint 1 已取"取消场景"进 T2 |

## 9. 风险登记（已有代码特化）

> 每条给出**可复查的证据**；无法证实的一律标"未核实"，不写成结论。

### 9.1 文档与代码漂移

| ID | 风险 | 证据 | 严重度 |
|---|---|---|---|
| R-DOC-1 | 5 处实战发现漂移未登记，Dev/QA 可能按过时文档断言行为 | `docs/sprint-1/runtime-context.md` §6、`drift-check.md` §1 | **high** |
| R-DOC-2 | `docs/quickstart.md`:63 断言 Ping 返回 `pong`，实际返回 `pong <remote_name> <unix_ts>`（`remote/qubes-rpc/qubesair.Ping`:10,22） | 双方原文 | medium |
| R-DOC-3 | `docs/architecture.md`:83 把 `UnlockData` 描述为"仍有派生密钥回退路径"，而 `internal/service/datakey.go`:19-24,72-81 明确 master **只用于迁移、永不自动创建、缺 master 即报错** | 双方原文 | **high**（安全语义） |
| R-DOC-4 | `docs/architecture.md` 与 `docs/grpc-transport-design.md` 的服务表未覆盖 `dom0-scripts/policy.d/30-qubes-air.policy` 中实际授权的 `qubesair.Status` / `qubesair.Deploy` / `qubesair.SSHProxy` / `qubesair.VaultRead` / `qubesair.GetCredential` | policy 文件 :45-121 vs 两份 doc 的服务表 | medium |
| R-DOC-5 | CI 内 Node 版本不自洽：`release.yml`:90 = 22，其余 = 20 | workflow 原文 | low |
| R-DOC-6 | 根 README 实际文件名是小写 `readme.md`，`AGENTS.md` 与多处文档写 `README.md`；doc-link 检查不区分大小写所以不报警 | `git ls-files` 输出 | low |

> 复核边界：本清单是**抽样**（9 处文档面对代码核对），不是全仓审计。未覆盖的文档面标为未知。

### 9.2 历史包袱（真实大文件 / 高复杂度热点）

| ID | 热点 | 证据（实测） | 风险 |
|---|---|---|---|
| R-TECH-1 | `internal/transport/grpc/server.go` 的 `(*Server).Tunnel`：**262 行**、gocyclo **44**，靠 `//nolint:gocyclo,funlen // frame dispatch plus lifecycle, kept together deliberately` 保留 | `gocyclo -top`；`.go:346-347` | 帧分发 + 生命周期耦合在一个函数，改动回归面大；取消/断线路径难单测 |
| R-TECH-2 | `internal/config/config.go` 的 `(*Config).loadFromEnv`：**199 行**、gocyclo **71**（全仓最高）；`(*Config).Validate` gocyclo **28** | `gocyclo -top`；`.go:588-589,792` | 扁平 per-field 序列，新增配置项必然继续推高；配置回归只能靠表驱动测试 |
| R-TECH-3 | 1200 行级文件 4 个：`service/qube_service.go` 1208、`service/certrenew.go` 1192、`cmd/server/main.go` 1179、`service/certrenewsched.go` 1047 | `wc -l` | 单文件多职责，review/diff 信噪比低 |
| R-TECH-4 | `console/frontend/src/components/QubeList.svelte` **970 行**（占全部 .svelte 行数量级最大者），仅覆盖 7 个用例 | `wc -l`；`QubeList.test.ts` | 大组件 + 薄测试 = 改动高风险 |
| R-TECH-5 | 存量 `nolint` **24 处**；其中 `gocyclo/funlen` 豁免 5 处、`gosec` 豁免约 10 处 | `grep -rn nolint console/backend` | 豁免均已注明理由，但缺少"何时可移除"的退出条件 |
| R-TECH-6 | 源码内真·待办仅 **4 处**（`handler/billing_handler.go`:50,56；`handler/monitoring_handler.go`:53,55）+ 1 处脚本内 `remote/qubes-rpc/qubesair.UnlockData`:26 hardening TODO | `grep -rnE 'TODO\|FIXME\|XXX'` 去噪后 | 待办本身不重，但 4 处都在"假装有数据"的占位路径上，UI 必须继续标记未接入 |

### 9.3 测试覆盖度未知项

| ID | 未知项 | 证据 | 影响 |
|---|---|---|---|
| R-COV-1 | `go test` 语句覆盖 60.7%，但 **331 个函数 0.0%**；`cmd/*` 几乎全为 0% | `go tool cover -func=coverage.out` | 入口/接线层无回归网，改动靠手工验证 |
| R-COV-2 | 传输可靠性未经门禁：`Reconnect` 关键字在测试名中 **0 次**；`Cancel` 2 次、`Disconnect` 3 次、`Restart` 3 次 | `grep "^func Test" \| grep -i` | `client.go`:125-158 的 reconnect/backoff/jitter 循环无直接回归测试 |
| R-COV-3 | `internal/qrexec` 是 Exec/FileCopy 的传输关键路径，仅 4 个 `Test*`、覆盖率 **32.1%** | `go test` 输出；`qrexec/*_test.go` | qrexec 服务边界回归薄 |
| R-COV-4 | 前端 19 个组件中只有 3 个有测试；`MonitoringView`(502)/`CredentialList`(505)/`ZonesView`(504) 全无 | `ls components/` vs `*.test.ts` | UI 回归只能靠手工点击 |
| R-COV-5 | 覆盖率为既有本地产物（`coverage.out` 未跟踪），无 CI 阈值门禁 | `Makefile`:84-85 只生成不校验 | 覆盖率可悄悄下降而不报警 |

## 10. 团队与角色分工

| 角色 | 代号 | 职责 | 写入范围 |
|---|---|---|---|
| Producer | Remy | 规划、drift check、runtime-context、plan/progress/done、Brief 7/8 节 | `PROJECT_BRIEF.md`、`docs/**`、`.kixpower/**` |
| Dev | Nova/Sage/Milo | 按 plan 实现、补测试、自测 | plan `target_rules` 内源码 + progress 任务行 |
| QA | Ivy | ci_gate + manual playthrough、独立签署 | 测试文件 + `docs/qa/qa-signoff-N.md` |
| Orchestrator | — | L2 门禁、Observe、L4、synthesis | progress 的 L2/Observe 字段、`hill-climbing.md` |

约束：Producer **绝不写源代码**；QA **不重跑 local_gate**；Orchestrator **不改 plan.md**。

## 11. 里程碑与演进路线

| 阶段 | 状态 | 证据 |
|---|---|---|
| P0 安全边界收紧（SEC-01/02/03） | 已完成 | `docs/TODO.md`:11-20 |
| P1 可靠性与提交前收尾（ENG-01/REL-01/REL-02/DATA-01） | 已完成（真机剩余归 QA-01） | `docs/TODO.md`:24-45 |
| OPS-01 / NET-01 | 未完成（需真实离机与网段） | `docs/TODO.md`:46-52 |
| QA-01 Proxmox 真机回归 | 真机主路径已过，剩余 4 项 | `docs/TODO.md`:53-57；`docs/reviews/2026-09-22-qa01-proxmox.md` |
| **Sprint 1：质量体检 + 未门禁测试补齐** | **planning** | 本 Brief §7 |
| P2 产品与扩展（GUI/UI/OBS/MCP/CLOUD/PUB） | 未开始 | `docs/TODO.md`:59-84 |

推荐推进顺序（沿用 `docs/TODO.md`:86-90）：本 Sprint 质量收口 → OPS-01/NET-01/QA-01 剩余真机项 → GUI-01 与其余产品任务。

## 12. 关键约束与红线

1. 必须走 `make pre-commit`；禁止 `--no-fail` / `|| true` / `continue-on-error` 伪造通过（`AGENTS.md`:22-36）。
2. 安全控制必须有失败路径测试；PKI/mTLS 必须覆盖正反例（`AGENTS.md`:58-71）。
3. 不得把 provider credential、API token、CA/private key、LUKS key、bootstrap token 或真实基础设施地址提交进仓库、日志、fixture 或前端 bundle（`AGENTS.md`:52-55）。
4. 未实现能力在 UI 与文档中必须明确标记；计划/placeholder/TODO 不得写成已实现（`AGENTS.md`:11,84）。
5. 架构图统一用 Mermaid fenced block，不提交 ASCII 流程图（`AGENTS.md`:79）。
6. 复杂度硬约束：`gocyclo ≤ 15`，函数 ≤ 100 行 / 50 语句；不得靠调高全局阈值解决（`AGENTS.md`:31-33,44-46）。
7. 前端必须 0 error / 0 warning（`Makefile`:101 `FRONTEND_WARNING_BUDGET ?= 0`）。
8. 一个 commit 只表达一个完整意图（`AGENTS.md`:24-26）。
9. **Producer 自身红线**：只写文档，绝不编辑 `.go` / `.svelte` / `.ts` 源码或测试代码。

## 13. 验收标准与证据策略

| 层 | 判据 | 执行者 |
|---|---|---|
| Dev 自测 | 改动范围内 focused 测试通过；`dev_self_tests_passed` 记录 | Dev |
| L2 门禁 | plan 中**全部 required** `local_gate` 在同一 revision 通过；写 `l2_verified_sha`（40 位）+ `l2_gate_manifest_sha256` | Orchestrator |
| QA | 只跑 `ci_gate` + `manual_gate`；`qa_verified_sha == l2_verified_sha == HEAD` 且 `qa_test_changes` 为空才可 PASS | QA |
| 证据回放 | 每条 claim 附 `文件:行号` 或命令输出；无法证实 → 降级为"未核实"，不得标 blocking | 全角色 |

**红线**：门禁绿、测试通过、工具成功、代理自述**只支持其各自覆盖范围**；Sprint 完成必须有当前目标与约束的直接依据。
覆盖率数字以 `coverage.out` 为唯一来源，不从记忆或文档引用。

## 14. 附录：术语表与文档索引

| 术语 | 含义 |
|---|---|
| RemoteVM | dom0 中的一条元数据记录（`relayvm` + `transport_rpc` + `remote_name`），不是可启动的本地 VM |
| qrexec 服务 | 形如 `qubesair.<Verb>` 的调用端点，由 dom0 policy 与服务脚本共同定义 |
| DEK | per-Qube 独立随机 256-bit 数据加密密钥，LUKS 数据盘的唯一解锁路径 |
| legacy master | `qubes-air-luks-master`，只读、只用于旧盘迁移，永不自动创建 |
| L2 gate | deterministic 本地门禁（test/race/lint/gosec/复杂度/前端/文档） |
| verifiable_gates | plan.md 中唯一门禁来源，每条含 `id`/`type`/`cmd`/`expect` |

**文档索引（权威入口）**

| 主题 | 文档 |
|---|---|
| 当前能力与边界 | `docs/roadmap-to-production.md` |
| 任务与优先级唯一入口 | `docs/TODO.md` |
| 架构与信任边界 | `docs/architecture.md` |
| 安全控制与默认值 | `docs/security-controls.md` |
| 生命周期可靠性与恢复契约 | `docs/reliability-design.md` |
| RemoteVM gRPC 传输 | `docs/grpc-transport-design.md` |
| Provider 原生编排 | `docs/provider-design.md` |
| 本地开发 | `docs/local-dev.md`、`docs/quickstart.md` |
| 历史验收证据 | `docs/reviews/README.md` |
| 本 Sprint 过程文档 | `docs/sprint-1/{plan,progress,drift-check,runtime-context}.md` |
