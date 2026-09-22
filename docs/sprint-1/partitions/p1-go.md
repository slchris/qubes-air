# 分区报告 — p1-go（T1 + T2）

> 分区：**p1-go**（Dev）｜分支：`kixpower/sprint-1`｜基线 revision：`fae0aea5370cbd87247255022ab16d13ff7951df`
> 写集合：`console/backend/internal/transport/**`、`console/backend/internal/qrexec/**`、
> `console/backend/coverage.out`（本地产物，未跟踪/已被 `.gitignore:41` 忽略）、本文件
> 环境：macOS arm64、go 1.26.8；本机无 `pwsh` → **未调用任何 `.ps1`**
> 未 commit / 未 push / 未 switch / 未 stash / 未 rebase（编排器负责 synthesis 与提交）

---

## T1 — 全量质量基线体检与覆盖率/门禁矩阵

### T1.1 实测数据（当前 revision，**在 T2 新增测试之前**采集）

| 指标 | 实测值 | 采集命令 | 与 plan/progress 基线对照 |
|---|---|---|---|
| `go test -race -coverprofile=coverage.out ./...` | **exit 0（全绿）** | `cd console/backend && go test -race -coverprofile=coverage.out ./...` | 一致（基线全绿） |
| 语句覆盖率 total | **60.7%** | `go tool cover -func=coverage.out \| tail -1` | 与 `progress.md`/`drift-check.md` 的 60.7% **一致** |
| `0.0%` 覆盖函数数 | **331** | `go tool cover -func=coverage.out \| awk '$3=="0.0%"' \| wc -l` | 与基线 331 **一致** |
| `cover -func` 条目数 | 1029 | `go tool cover -func=coverage.out \| wc -l` | — |
| `coverage.out` 状态 | 未跟踪（`git ls-files --error-unmatch` 报 "did not match any file(s) known to git"） | — | 符合"不得 `git add`" |

> 该次运行覆盖 100% 的包（全部 `ok`），`internal/qrexec` 32.1%、`internal/transport/grpc` 74.1%，
> 与 `drift-check.md` §4.3 的包级数字一致 → 说明当前 revision 未漂移。

### T1.2 `internal/**` 0% 函数四类归类计数表

**归类判据（机械可复现，脚本：读 `go tool cover -func` + 源文件）**

| 步骤 | 判据 |
|---|---|
| a. 生成物 | 源文件 basename 以 `.pb.go` 结尾，或文件头含 `Code generated` |
| b. 无测试 | 该文件所在包目录下 `*_test.go` 数量 = 0 |
| c. 入口/接线 | 包内有测试，但函数体 ≤2 条语句，或函数名为 `New*`/`With*` 的构造器/选项/薄委托包装 |
| d. 有测试但未覆盖 | 包内有测试且函数体非薄包装（≥3 条语句），但无任何测试触达 |

**计数表（`internal/**`，共 247 个 0% 函数）**

| 类别 | 计数 | 代表函数（`文件:行`） |
|---|---|---|
| 生成物 | **81** | `internal/transport/relaypb/relay_transport.pb.go:82 Enum`、`:88 String`、`:92 Descriptor`（两个 protoc 生成文件：`relay_transport.pb.go` 74 个 + `relay_transport_grpc.pb.go` 7 个，均为 `Code generated ... DO NOT EDIT`） |
| 入口/接线 | **82** | `internal/transport/grpc/invoker.go:32 NewQrexecInvoker`、`internal/transport/grpc/server.go:114 NewServerWithQrexec`、`internal/qrexec/client.go:55 WithTimeout`、`internal/service/credential_service.go:21 List`（薄委托） |
| 无测试 | **0** | `internal/**` 中**唯一**没有 `_test.go` 的包目录是 `internal/transport/relaypb`（0 个测试文件），但它同时是 protoc 生成物，按判据 a 优先归入"生成物"；其余所有含 0% 函数的 internal 包都有测试（`handler` 6 / `service` 26 / `repository` 14 / `transport/grpc` 13 / `agent` 9 / `provider/proxmox` 7 / `middleware` 6 / `pki` 5 / `orchestrator` 4 / `mcp` 4 / 其余各 1）→ b 类集合为空 |
| 有测试但未覆盖 | **84** | `internal/transport/grpc/client.go:577 jitter`、`internal/qrexec/client.go:122 CallResult`、`internal/config/config.go:985 splitCSV`、`internal/agent/identity.go:207 ServerTLSConfig` |
| **合计** | **247** | 331（全仓）= 247（`internal/**`）+ **84**（`cmd/*` 入口包） |

**包级分布（d 类 84 个）**：`internal/handler` 34、`repository` 17、`service` 9、`orchestrator` 5、
`scheduler` 4、`provider/proxmox` 3、`qrexec` 3、`transport/grpc` 3、`agent` 2、`config` 2、`mcp` 1、`provider` 1。

**两个可直接用于 Sprint 2 的结论**

1. **不存在"整个包没有测试"的缺口**（b 类 = 0；`internal/**` 唯一的无测试包是生成的 `relaypb`）→ 覆盖率缺口全在**函数/分支粒度**，
   与 `drift-check.md` §4.2 "文件级不存在未门禁测试"的结论互相印证。
2. **84 个 `cmd/*` 入口包 0% 函数**被 plan §5.2 明确排除（G2 cold, low-ROI），
   因此任何"覆盖率阈值门禁"若不做 `cmd/*` 排除，都会被入口包结构性拉低 → 支持 plan §1.6 把阈值门禁留到 Sprint 2。

### T1.3 plan §2 gate 矩阵复核（25 条 = 11 local_gate + 10 ci_gate + 4 manual_gate）

**结论：25/25 条 `cmd` 在仓库内可定位**（编排器实测一致）。逐条 `cmd` → 定位证据：

| gate id | plan cmd | 仓库内定位（实测） | 行号 |
|---|---|---|---|
| `g_diff_check` | `make diff-check` | `Makefile` 目标 `diff-check:` + `git diff --check $(BASE_REV) --` | 81-82 ✅精确 |
| `g_test_race` | `make test-race` | `Makefile` `test-race:` + `go test -race -coverprofile=coverage.out ./...` | 84-85 ✅ |
| `g_lint_new` | `make lint-new` | `Makefile` + `golangci-lint run --new-from-rev` | 87-88 ✅ |
| `g_gosec_new` | `make gosec-new` | `Makefile` + `--enable-only=gosec` | 91-92 ✅ |
| `g_complexity_new` | `make complexity-new` | `Makefile` + `--enable-only=gocyclo,funlen` | 94-95 ✅ |
| `g_vuln_check` | `make vuln-check` | `Makefile` + `govulncheck ./...` | 97-98 ✅ |
| `g_frontend_check` | `make frontend-check` | `Makefile` + `npm ci`/`npm run check`/`build`/`test`；warning 预算 `FRONTEND_WARNING_BUDGET ?= 0` | 103-114 ✅；**101** ✅ |
| `g_shellcheck_new` | `make shellcheck-new` | `Makefile`（shebang 过滤 + `shellcheck`） | 129-136 ✅ |
| `g_docs_check` | `make docs-check` | `Makefile` → `check-doc-links.mjs` + `check-workflow-gates.mjs` | 138-140 ✅ |
| `g_frontend_audit_new` | `make frontend-audit-new` | `Makefile`（依赖文件变更才跑 `npm audit --audit-level=high`） | 117-123 ✅ |
| `g_pre_commit` | `make pre-commit` | `Makefile` 汇总 10 项 | 70-71 ✅ |
| `g_agent_deb_test` | `make agent-deb-test` | `Makefile` → `scripts/test-agent-deb.sh`（CI 同入口 `build.yml`:100） | 195-196 ✅ |
| `ci_lint_go` | workflow `Lint` → job `golangci-lint` | `.github/workflows/lint.yml` | 19-46 ✅精确 |
| `ci_lint_gotest` | job `go-test` | `lint.yml` | 49-74 ✅精确 |
| `ci_lint_frontend` | job `frontend-lint` | `lint.yml` | 77-99 ✅精确 |
| `ci_lint_shellcheck` | job `shellcheck` | `lint.yml` | 102-114 ✅精确 |
| `ci_lint_yamllint` | job `yaml-lint` | `lint.yml` | **117-131**（plan 写 117-127，见下） |
| `ci_build_agent` | `build.yml` job `agent-package` | `.github/workflows/build.yml` | **91-100**（plan 写 91-98，见下） |
| `ci_docs` | `docs.yml` job `docs` | `.github/workflows/docs.yml` | **18-35**（plan §1.4 写 18-38，见下） |
| `ci_dependency_go` | job `go-dependencies` | `.github/workflows/dependency.yml` | 27-53 ✅精确 |
| `ci_security` | jobs `gosec`/`trivy`/`secrets-scan` | `.github/workflows/security.yml` | job 起始 25/54/79；**EOF=92**（plan §2.2 写 25-100，见下） |
| `m_*` ×4 | manual playthrough | 无 cmd（LLM playthrough） | n/a |

**行号偏差登记（7 条，含编排器已知 2 条）**

| # | plan 位置 | plan 行号 | 实测 | 判定 |
|---|---|---|---|---|
| D-a | §1.5 EP-2 / §2.2：`dependency.yml` 的 `\|\| true` | 82 | **81**（`npm outdated \|\| true`） | 偏移 −1；**且 plan 只登记 1 处，实际有 2 处**：`dependency.yml:116`（`go-licenses check ... \|\| true`，同一步骤还带 `continue-on-error: true`） |
| D-b | §1.5 / §2.1：`docs.yml` 调 `check-workflow-gates.mjs` | 36 | **35** | 偏移 −1；`docs.yml` 全文只有 35 行（EOF=35），plan 的 36 行不存在 |
| D-c | §1.4：CI `docs` job 范围 | `docs.yml`:18-38 | **18-35** | 上界超 EOF 3 行（job 起始行 18 精确） |
| D-d | §2.2：`ci_security` 范围 | `security.yml`:25-100 | job 起始 25/54/79，**EOF=92** | 上界超 EOF 8 行 |
| D-e | §2.2：`ci_build_agent` 范围 | `build.yml`:91-98 | **91-100**（job 起始行 91 精确；`run: scripts/test-agent-deb.sh` 在 **100**） | 上界 −2 |
| D-f | §2.2：`ci_lint_yamllint` 范围 | `lint.yml`:117-127 | **117-131**（job 起始行 117 精确；`config_file:` 在 131） | 上界 −4 |
| D-g | §2.2：`ci_dependency_go` 范围 | `dependency.yml`:27-53 | 27-53 ✅ | 精确 |

> 影响评估：**均为范围尾注偏差，不影响任何 gate 的可定位性**（job 起始行/目标定义行全部准确，
> `Makefile` 全部 15 处引用逐行精确）。D-a 的"第 2 处 `|| true`"是新增事实，
> 与 T5 的 `check-workflow-gates.mjs` 盲区修复直接相关：脚本只挡 `-no-fail`/浮动 ref/`continue-on-error`，
> `dependency.yml:81` 与 `:116` 两处 `|| true` 都绕过它。

### T1.4 反证：T1 **未新增/修改任何 `.go`/`.svelte`/`.ts` 文件**

T1 完成时（T2 动手前）原始输出：

```
$ git status --short
?? .kixpower/
?? DREAMS.md
?? IDENTITY.md
?? PROJECT_BRIEF.md
?? SOUL.md
?? USER.md
?? docs/.kixpower-current-sprint
?? docs/sprint-1/
?? kix-discipline/
?? memory/

$ git status --short -- '*.go' '*.svelte' '*.ts'
（空）
```

摘要：**源码改动 = 0**；`console/backend` 下无任何 tracked 修改；仅有 T1 允许刷新的未跟踪本地产物
`coverage.out`（且被 `.gitignore` 忽略，`git status` 不显示）。T1 只新增了本文件（markdown）。

---

## T2 — 传输可靠性回归（断线 / 取消 / 超时 / 重启）

### T2.0 动手前的两条**实际核对**（plan 行号/现有测试清单存在偏差）

| 项 | plan 声称 | 实测 | 处置 |
|---|---|---|---|
| `client.go` 行号（`Start` 重连循环 / `recvLoop` / `call` / `CallStream` / `withDefaults` / `resolveTLS`） | 125-158、387-394、259、317、40-47、63-68、178-186 | `Start` 125-152、`recvLoop` 387-450（drop 分支 392-395）、`call` 259-309（ctx 分支 300-302）、`CallStream` 317-379（ctx 分支 366-369）、`withDefaults` 59-70、`resolveTLS` 160-175、`runOnce` 177-217 | 以代码为准（偏差 ≤8 行，未影响任何结论）；本报告全部引用实测行号 |
| §1.2 注"现有相关测试（避免重复造）"4 条 | `TestClient_ContextCancellation`、`TestClient_RequestTimeout`、`TestJitterIsStableAcrossRestarts`、`TestRevocationSurvivesSessionResumption` | **前 3 条不在 transport/qrexec**：`TestClient_RequestTimeout` = `internal/mcp/client_test.go:178`、`TestClient_ContextCancellation` = `internal/mcp/client_test.go:198`（测 MCP HTTP client，httptest）、`TestJitterIsStableAcrossRestarts` = `internal/service/certrenewsched_test.go:213`（测证书续期 `jitterFraction`，非 `grpc.jitter`）；只有 `TestRevocationSurvivesSessionResumption`（`internal/transport/grpc/revocation_resume_test.go:62`）在包内；`integration_test.go`/`stream_test.go` 存在 | **transport 侧这三类行为当时确无包内测试**（证据：`grpc.jitter` 覆盖率 0.0%、`CallStream`/`call` 无取消测试）→ T2 新建而非重复；本项按"plan 对现有测试的跨包误引"登记 |

### T2.1 改动清单（**仅测试文件**；生产代码 0 改动）

| 文件 | 状态 | 内容 |
|---|---|---|
| `console/backend/internal/transport/grpc/reliability_test.go` | **新增** | 8 个测试 + 6 个测试专用 helper（`fakeTunnel`/`dropDialer`/`gatedInvoker`/`startInvokerServer`/`waitFor`/`testClientConfig` + 3 个只读计数方法 `connected`/`inflightCount`/`streamCount`） |
| `console/backend/internal/qrexec/client_test.go` | 追加 | 2 个测试 + `ctxRunner` fake（imports 增 `time`） |

生产文件未被触碰（终态核对，`byte-identical`）：

```
$ shasum -a 256 internal/transport/grpc/client.go internal/transport/grpc/server.go internal/qrexec/client.go
a0e84892d37228607fde23ac67d11c4b744e0e06279a6f417680e76dc37ff21e  internal/transport/grpc/client.go
b5b7f625cd2edb53c848e251b88cde7313f09380399e1eec8ba6e406808e657c  internal/transport/grpc/server.go
ec79a82d634465e2144d27b4d0cbe78727f451e791ab82c2911aa59dc082ee4a  internal/qrexec/client.go
```

### T2.2 新增测试 × 覆盖行为 × 失败证据 × 还原证据

| # | 测试名 | 覆盖行为（实测行号） | 反向 mutation | 失败证据（`go test -race -run`） | 还原证据 |
|---|---|---|---|---|---|
| (a)1 | `TestRecvLoopReturnsOnTunnelDrop` | `recvLoop` 在流中断时**返回该错误**，使 `Start` 能退避重连（`client.go:392-395`） | `return err` → `return nil` | `--- FAIL: TestRecvLoopReturnsOnTunnelDrop`；`reliability_test.go:212: recvLoop returned <nil>, want the stream's drop error transport is closing` | hash `a0e8489…` 复原、0 个 `MUTATION` 残留；复跑 PASS |
| (a)2 | `TestClearStreamFailsInflightCallAndPendingStream` | `clearStream` 断线清理契约：置空 `stream`、失败每个 inflight `pendingCall`、`close(ps.recv)` 并设 `ps.err`、清空两张表、之后 `send` 返回 `ErrNotConnected`（`client.go:546-561`） | 删除 `for _, pc := range pending { pc.done <- … }` 失败分支 | `--- FAIL`；`reliability_test.go:245: clearStream did not fail the in-flight call: it would wait across the reconnect` | 同上 |
| (a)3 | `TestClientReconnectsAfterConnectionDrop` | 真实 mTLS server + 自定义 `Dialer`：① 首连可服务；② 断链时**in-flight 调用被失败而非挂死**；③ 断链期间 `Call` 快速返回 `ErrNotConnected`（不挂到自身 deadline）；④ 链路恢复后 `Start` 重新 dial 并再次服务成功（`Start` 125-152、`runOnce` 177-217、`clearStream`） | 同 (a)1（`recvLoop` 吞错）与 (a)2（不失败 inflight） | M1/M2 的 FAIL 即本测试三条断言的敏感性证据（同源行为） | 同上 |
| (b)1 | `TestCallCancellationReturnsCtxErrorAndDrainsInflight` | `call` 在 ctx 取消时返回 `context.Canceled` 且**不挂起**，并清掉 `inflight` 注册（3 轮，`client.go:300-302`、`defer delete` 276-280） | `case <-ctx.Done(): return transport.Result{}, ctx.Err()` → `return transport.Result{}, nil` | `--- FAIL`；`reliability_test.go:394: iteration 0: Call error = <nil>, want context.Canceled` | 同上 |
| (b)2 | `TestCallStreamCancellationReturnsAndStopsStdinPump` | `CallStream` 在 ctx 取消时返回 `context.Canceled`、注销 `streams` 表项、且调用方关闭 stdin 后 **stdin 泵 goroutine 退出**（`runtime.NumGoroutine()` 回到基线；`client.go:317-379`，注销 332-336） | `defer func(){ … delete(c.streams, reqID) … }()` 的 delete → `_ = reqID` | `--- FAIL`；`reliability_test.go:438: 1 pending streams survived cancellation` | 同上 |
| (c)1 | `TestWithDefaultsReconnectBackoffBounds` | `withDefaults` 的 **20s keepalive / 500ms floor / 30s ceiling** 边界：未设/负值取默认、显式值（含正好 500ms/30s）保留、值接收者不改调用方配置（`client.go:59-70`） | `cfg.ReconnectMax = 30 * time.Second` → `time.Second` | `--- FAIL`；`unset_values_get_the_documented_defaults: ReconnectMax = 1s, want 30s`（+ negative 子用例同） | 同上 |
| (c)2 | `TestJitterStaysInsideBackoffEnvelope` | `jitter` 边界：非正值 → 0；500ms 与 30s 各 2000 次抽样都落在 `[d, d+10%]`，且**确实有抖动**（存在 > d 的抽样）→ 防重连风暴/防退避被拉长（`client.go:576-583`） | `return d - (delta / 2) + delta` → `return d + 3*delta` | `--- FAIL`；`reliability_test.go:538: jitter(500ms) = 795.956633ms, above the backoff ceiling 550ms` | 同上 |
| (c)3 | `TestTLSProviderRefetchedOnReconnect` | `TLSProvider` **每次连接尝试（含恢复隧道的那次）都重新取证书**：首连取到 → 断链后重连尝试仍取 → 重连成功后隧道真的可用（`resolveTLS` 160-175、`runOnce` 178-183） | `resolveTLS` 用 `sync.Once` 缓存首次结果（+2 字段） | `--- FAIL`；`reliability_test.go:592: timed out after 5s waiting for TLSProvider to be consulted while reconnecting` | 同上（两次 mutation 均 3/3 `byte-identical` 复原） |
| (c)4 | `TestCallAppliesConfiguredTimeout`（qrexec） | `qrexec.WithTimeout` 生效：调用方无 deadline 时，`Call` 仍按配置超时返回 `context.DeadlineExceeded`（`qrexec/client.go:55-61,113-115`） | `c.timeout = d` → `d * 100` | `--- FAIL`；`client_test.go:107: Call took 5.001283958s to honor the 50ms timeout` | hash `ec79a82…` 复原、0 残留；复跑 PASS |
| (c)5 | `TestCallResultPropagatesCancellation`（qrexec） | `CallResult` 把调用方取消当**传输失败**传播，绝不伪造成 exit 0 结果（`qrexec/client.go:122-139`） | `if err != nil { return Result{}, err }` → `return Result{ExitCode: 0}, nil` | `--- FAIL`；`client_test.go:136: CallResult error = <nil>, want context.Canceled` | 同上 |

**9 次 mutation 全部产生 FAIL，随后全部还原**（每次失败输出均含具体断言与期望值）；
终态校验：3 个生产文件 hash 与 mutation 前**逐字节相同**，`grep -rn MUTATION internal/transport/ internal/qrexec/` = **0**。

### T2.3 `go test -race` 实测输出摘要

| 命令 | 结果 |
|---|---|
| `go test -race -count=1 -run '<8 个新测试>' ./internal/transport/grpc/` | `ok … 1.862s`（全 PASS） |
| `go test -race -count=1 -run 'TestCallAppliesConfiguredTimeout\|TestCallResultPropagatesCancellation' ./internal/qrexec/` | `ok … 1.392s`（全 PASS） |
| **稳定性**：`-race -count=10`（8 个 grpc 新测试） | `ok … 3.690s`（10/10 轮全绿） |
| **稳定性**：`-race -count=10`（2 个 qrexec 新测试） | `ok … 1.652s`（10/10 轮全绿） |
| 派单要求的分区 gate：`go test -race ./internal/transport/... ./internal/qrexec/...` | `ok transport 1.194s` / `ok transport/grpc 3.039s` / `ok qrexec 1.228s`，**exit 0** |
| 全量：`go test -race -coverprofile=coverage.out ./...` | **exit 0（全绿）**；`total` 60.9%~61.0%、`0.0%` 函数 328（两次运行一致） |
| 新代码门禁：`make BASE_REV=fae0aea lint-new gosec-new complexity-new` | `0 issues.` ×3，**exit 0** |

> 稳定性过程中发现并修掉了一次**测试自身的**时序缺陷（非生产缺陷）：
> `TestTLSProviderRefetchedOnReconnect` 最初把"断链期间 provider 调用数"取成一次性快照，
> 与 `Start` 的退避 sleep 竞争 → `-count=5` 时出现 `TLSProvider calls stayed at 2` 失败。
> 修法：改为对"重连期间至少再取一次证书"做有界等待，并以**首次连接**的计数为锚点做终态断言
> （单次尝试可能先 resolveTLS 再在链路恢复后完成，故不能以中途快照为准）。修后 `-count=10` 稳定全绿。

### T2.4 覆盖率变化（T2 前后，同一 revision）

> **测量噪声声明**：同一 revision 连续两次全量 `-race` 运行之间，`total` 出现 60.9%↔61.0%、
> `internal/transport/grpc` 出现 75.6%↔76.3%↔76.5% 的浮动（`0.0%` 函数数两次都为 328，稳定）。
> 根因：重连类测试里 `Start` 的尝试次数/分支（`resolveTLS` 报错、`Tunnel()` 报错、`send` 成功或失败）
> 依赖调度时序，单次运行覆盖到的语句集合因此不同，**不是**门禁不稳定（`go test -race` 两次都 exit 0）。
> 下表取两次实测的区间；`0.0%` 计数取两次一致的 328。

| 指标 | T1（T2 前） | T2 后（实测区间） | Δ |
|---|---|---|---|
| `total` | 60.7% | **60.9% ~ 61.0%** | +0.2 ~ +0.3pp |
| `0.0%` 函数数 | 331 | **328**（两次运行一致） | −3 |
| `internal/transport/grpc` 包 | 74.1% | **75.6% ~ 76.5%** | +1.5 ~ +2.4pp |
| `internal/qrexec` 包 | 32.1% | **51.8%**（两次运行一致） | +19.7pp |
| 被消除的 0% 函数 | — | `grpc/client.go jitter`（→100.0%）、`qrexec/client.go WithTimeout`、`qrexec/client.go CallResult` | 3 |

> 对 Sprint 2 的输入：如果把覆盖率写成**阈值门禁**，必须先把这类 ±0.1pp 的运行噪声计入容差，
> 否则门禁会偶发假红（本 Sprint plan §1.6 已把阈值门禁排除，此处只作为实测事实登记）。

> 仍为 0% 的 transport 侧函数（T2 未覆盖，如实登记，不声称覆盖）：
> `qrexec/client.go:144 Run`、`:155 RunResult`（真机 `qrexec-client-vm` 执行体）、
> `grpc/client.go:461 handleReverse`、`grpc/frames.go:129 keepAliveFrame`、
> `grpc/invoker.go:32 NewQrexecInvoker`、`grpc/server.go:114 NewServerWithQrexec`、`:324 Stop`、
> `grpc/vaultcerts.go:40 FetchClientMTLS`、`:51 VaultTLSProvider`。

### T2.5 是否发现生产缺陷

**未发现生产缺陷**：9 组反向 mutation 都按预期把新测试打红，说明新断言绑定的都是当前实现的真实契约；
在 `-race`、`-count=10` 重复运行与真实 mTLS 端到端场景下，断线/取消/超时/重启四条路径
**行为与 `docs/reliability-design.md`/`docs/grpc-transport-design.md` 的声明一致**。

一处**如实登记的行为边界（非缺陷、非本 Sprint 范围）**：`CallStream` 在 ctx 取消后，
stdin 泵 goroutine 会一直阻塞在调用方的 `stdin.Read` 上，直到调用方关闭 stdin
（`client.go:348-363`）。这与 `stream_test.go` 的用法一致（调用方在结束时 `stdinW.Close()`），
新测试即按"调用方拥有 stdin"的契约断言（关闭后 goroutine 退出）。
如需"取消即无条件释放泵"，需要生产代码改造（增加以 ctx 中断 Read 的机制）——
**不在 T2 范围（不修改生产代码）**，作为观察项登记，供 Producer 决定是否立新任务。

## 门禁状态（本分区，实测）

| 项 | 命令 | 结果 |
|---|---|---|
| Go race（全量） | `cd console/backend && go test -race -coverprofile=coverage.out ./...` | **exit 0** |
| Go race（分区） | `go test -race ./internal/transport/... ./internal/qrexec/...` | **exit 0** |
| Go 格式（改动文件 + 全模块） | `gofmt -l internal/transport/grpc/reliability_test.go internal/qrexec/client_test.go`；`gofmt -l .`（`$(go env GOROOT)/bin/gofmt`，终态补跑） | **无输出**（0 个未格式化文件；`gofmt -d` 空） |
| Go vet（分区 + 全模块） | `go vet ./internal/transport/... ./internal/qrexec/...`；`go vet ./...`（终态补跑） | **exit 0**，无诊断 |
| lint（新增代码） | `make BASE_REV=fae0aea lint-new` | **0 issues**（含 gofmt/goimports formatter；首次报 3 处 `misspell`：`cancelled`/`cancelling` → 已改为 US 拼写后复跑 0） |
| gosec（新增代码） | `make BASE_REV=fae0aea gosec-new` | **0 issues** |
| 复杂度（新增代码） | `make BASE_REV=fae0aea complexity-new` | **0 issues** |
| Diff 卫生 | `git diff --check fae0aea --` | **无输出**（无尾随空格/冲突标记） |
| 分支 | `git status --short --branch` | `## kixpower/sprint-1`（未切换/未提交） |

> **补跑说明**：上表 `gofmt`/`go vet` 两行是本分区**终态**（所有编辑与 mutation 还原之后）补跑的——
> 早期一次 `go vet` 发生在 `misspell` 修正、`TestTLSProviderRefetchedOnReconnect` 时序重构与 9 轮
> mutation 之前，不能代表终态；现已对冻结状态重新执行并通过。

## ❌ Blocked

无。

## 边界声明（未做、不得声称完成）

- 真机项（OPS-01 离机归档/keyring、NET-01 静态 IP 池、QA-01 剩余 4 项、GUI-01、CLOUD-01/02）**未触碰、未声称完成**。
- `coverage.out` 保持本地未跟踪产物（`.gitignore:41`），**未 `git add`**。
- 覆盖率**阈值门禁**、存量复杂度重构、`cmd/*` 入口包补测：均按 plan §1.6/§5.2 排除，本分区未做。
