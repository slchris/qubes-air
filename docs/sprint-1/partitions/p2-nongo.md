# p2-nongo 分区报告 — T3 / T4 / T5

> 分区：`p2-nongo`（Dev = Nova/Sage/Milo）
> 分支：`kixpower/sprint-1`（未 switch / 未 commit / 未 push / 未 stash）
> 任务：**T4 → T5 → T3**（顺序执行；T4/T5 共享 `make docs-check`）
> 写集合（本分区）：`console/frontend/src/**`、`docs/**`（排除 `docs/sprint-1/**` 除本文件）、
> `readme.md`、`scripts/**`、`.github/workflows/**`
> 基线：`fae0aea`（HEAD 未移动）

## 0. 结论速览

| 任务 | 状态 | 关键门禁实测 |
|---|---|---|
| T4 文档漂移 + 默认值落文档 | **完成**（D-1/D-2/D-3/D-6/D-7 已消解；D-4/D-5 已撤回并留证） | `make docs-check` **exit 0**（148 links / 8 fixtures / guard OK） |
| T5 门禁缺口（EP-2 + EP-5） | **完成**；存量 2 处 `\|\| true` 已处置，1 条记开放问题 | `make docs-check` **exit 0**；`cd console/backend && go test ./...` **exit 0** |
| T3 前端组件测试 | **完成**（2 个 0 测试组件，含 2 条取消/拒绝负向路径） | `npm run test` **35 passed**（改前 20）；`npm run check` **0 error 0 warning**；`npx tsc --noEmit` **exit 0**；`node --check` 两个 `.mjs` **exit 0**；`npm run build` **ok** |
| 合 | 本分区 3 个任务的 required gate + 聚合门禁 | `make pre-commit` **exit 0**（含 `git diff --check` / `go test -race` / lint / gosec / complexity / govulncheck / frontend / shellcheck / docs-check） |


## 1. 改动文件（本分区作者，逐个列全）

| 文件 | 动作 | 属于 |
|---|---|---|
| `docs/runtime-defaults.md` | 新增 | T4（D-6/D-7 落点） |
| `docs/architecture.md` | 改（D-2/D-3） | T4 |
| `docs/quickstart.md` | 改（D-1） | T4 |
| `docs/grpc-transport-design.md` | 改（D-3） | T4 |
| `docs/README.md` | 改（索引加 `runtime-defaults.md`） | T4 |
| `scripts/check-workflow-gates.mjs` | 改（EP-2：`\|\| true` 检测 + continue-on-error 步骤作用域修正） | T5 |
| `scripts/check-doc-links.mjs` | 改（EP-5：测试引用仓库文件存在性检查） | T5 |
| `.github/workflows/dependency.yml` | 改（`:81`/`:116` 两处 `\|\| true` 处置） | T5 |
| `console/frontend/src/components/MonitoringView.test.ts` | 新增（6 用例） | T3 |
| `console/frontend/src/components/CredentialList.test.ts` | 新增（9 用例） | T3 |
| `docs/sprint-1/partitions/p2-nongo.md` | 新增（本文件） | 交付物 |

**未触碰**（用 `git status --short` + `git diff --name-only` 反证）：

| 禁止项 | 反证 |
|---|---|
| `console/backend/**` 任何 `.go` | 本分区的改动清单就是 §1 那 11 个文件，**其中没有任何 `.go`**。注意 `git status --short` 里确实有两条 `.go`：`M console/backend/internal/qrexec/client_test.go` 与 `?? console/backend/internal/transport/grpc/reliability_test.go` —— 它们落在 **p1-go 的写集合**（`internal/transport/**`、`internal/qrexec/**`）内，是并行分区在同一工作树的改动；本分区从未 open-for-write 这两个文件，也没有 `git add`/`git checkout` 它们 |
| `*.svelte` | 无改动。T3 只新增 `.test.ts`；为验证负向用例有效性做的一次临时 mutation（删除 `CredentialList.svelte` 的 confirm 守卫）已用 `cp` 备份还原，`git diff --stat src/components/CredentialList.svelte` 为空 |
| 其它 `.ts` | 只新增 2 个 `*.test.ts`，未改任何既有 `.ts` |
| `Makefile` / `.golangci.yml` / `AGENTS.md` / `PROJECT_BRIEF.md` / `docs/sprint-1/{plan,progress,drift-check,runtime-context}.md` | 无改动（`git status` 不含这些路径） |
| `.ps1` | 未调用（本机无 pwsh） |

`git diff --check HEAD --`：**exit 0**（无尾随空格、无冲突标记；首次运行曾报
`scripts/check-doc-links.mjs:119: new blank line at EOF`，已删掉文末空行后复测通过）。

## 2. T4 — 文档漂移三态表（D-1 ~ D-7）

> 行号已**逐条回原文复核**，与 `runtime-context.md` §6.1/§6.3 的登记有若干偏差，已在"行号更正"列写明。

| ID | 三态 | 证据（改后文档 / 代码） | 行号更正 |
|---|---|---|---|
| **D-1** | **已消解（改文档）** | 文档改为：`` `Ping` 应返回单行 `pong <remote_name> <unix_ts>` `` → `docs/quickstart.md:63`。代码：`remote/qubes-rpc/qubesair.Ping:10`（契约注释 `stdout: 单行 "pong <remote_name> <unix_ts>"`）、`:20`（`remote_name` 取值）、`:22`（`printf 'pong %s %s\n'`） | 无（原登记正确） |
| **D-2** | **已消解（改文档）** | `docs/architecture.md:85` 的 UnlockData 行改写为"读取该 Qube 自己的 DEK；**没有解锁用的回退路径**"，并在 `:87-91` 增加"密钥语义"引注；新增 `qubesair.RekeyData` 行（`:86`）。代码：`internal/service/datakey.go:19-24`（master "only ever READ… never mints one"）、`:56-70`（`KeyFor`：callers must NOT substitute a derived key）、`:72-92`（`LegacyKeyFor` = "migration credential, not an unlock credential"）、`:220-237`（缺 master 报错）；`internal/service/agentunlock.go:144-155`（缺 DEK → 走 `migrate`）、`:265-342`（migrate = rekey 到 DEK，旧槽未移除则保持 pending） | architecture.md 的服务表实际是 **77-85 行**（7 行），`runtime-context.md:218` 写 `:74-83`、drift-check `CD-2` 写 `:83` 均为旧行号 |
| **D-3** | **已消解（改文档）** | `docs/architecture.md:93-98` 新增 policy 实际授权清单（含行号）；`docs/grpc-transport-design.md` 新增三节：`### UnlockData / RekeyData`、`### SSHProxy`、`### policy 授权但本仓库无脚本的服务`。policy 原文：`qubesair.Status`:48、`qubesair.Deploy`:54、`qubesair.SSHProxy`:68/:71、`qubesair.VaultRead`:88、`qubesair.GetCredential`:121 | architecture.md 表是 **7 行**不是"8 行表"（drift-check `CD-3`）；`grpc-transport-design.md` 服务契约原为 **62-92 行**（drift-check 写 `:62-92`，正确）；另发现表里漏了本仓库**已实现**的 `qubesair.RekeyData`（`remote/qubes-rpc/qubesair.RekeyData`），一并补入 |
| **D-4** | **已撤回（文档侧不成立）+ 仍存在（workflow 侧，已登记）** | `docs/**` 与 `readme.md` 全文**没有任何 Node 版本断言**（`grep -rn -i node docs/*.md readme.md` 只命中"需要 Node/npm 在 PATH"，`docs/local-dev.md:76`）→ `runtime-context.md:219` 的"docs 说单一 Node 版本（20）"无法复现，**撤回该断言**。真实存在的是 workflow 内部不一致：`lint.yml:15` / `build.yml:15` / `dependency.yml:23` = `'20'`，`docs.yml:29` = `'20'`，而 `release.yml:88-90` = `"22"` → 落 `docs/runtime-defaults.md` §3 + 开放问题 **O-3** | `release.yml` 的 Node 在 **:88-90**（drift-check `CD-4` 写 `:90`，指向值所在行，可接受） |
| **D-5** | **已撤回（文档其实没错）** | 根文件确实是 `readme.md`（`git ls-files \| grep -x readme.md` 命中）。但**没有任何文档把根文件写成 `README.md`**：全仓 `README.md` 命中项只有 `docs/README.md`、`docs/reviews/README.md`、`crypto/README.md`、`packaging/agent-deb/README.md`，**这四个文件真实存在且大小写正确**；`AGENTS.md:83,97`、`docs/reviews/2026-09-20-workspace.md:17` 写的是无扩展名的 `README`（叙述用词，非链接）。结论：无可修的引用错误 → **接受现状**（`m_root_readme` 要求的"明确记为接受现状"）。macOS 大小写不敏感的盲区由 CI 覆盖：`docs.yml:31-32` 在 `ubuntu-latest` 上跑同一脚本，Linux 是大小写敏感文件系统 | 无 |
| **D-6** | **已消解（新增文档）** | `docs/runtime-defaults.md` §2：`SchemaVersion = 2`（`database.go:97`）、`applySchemaVersion`（`:155-173`）、`addColumnIfMissing`（`:184-197`）、无外键设计理由（`:280-287`、`:321-328`）、**9 张表 / 7 个索引**逐行行号、备份侧版本校验交叉引用（`docs/disaster-recovery.md:77,81-85`） | `runtime-context.md:68` 写"表清单（10 张）"，`grep -c 'CREATE TABLE IF NOT EXISTS'` = **9**；该文件的表格自身也只列了 9 行 → 记为开放问题 **O-2**（该文件不在本分区写集合） |
| **D-7** | **已消解（新增文档）** | `docs/runtime-defaults.md` §1 按 `drift-check.md` §5 的 **UD-1 ~ UD-14 全 14 条**登记，每条给取值 + `文件:行号`（含 UD-1b/1c、UD-4b~4d、UD-9b/9c、UD-10b、UD-11b、UD-12b~12e、UD-13b/13c、UD-8b/8c 等同一 ID 下的多个取值）；§1.5 末给"文档已有取值但无常量名"的交叉引用 | 行号全部按当前代码复核；`drift-check` 的 `UD-7` 两处重复定义已确认为真（`client.go:49` / `scheduler/proxmox.go:53`） |

**D-1~D-5 逐条复核的原始命令**（可复跑）：

```
grep -n "pong" docs/quickstart.md remote/qubes-rpc/qubesair.Ping
grep -n "回退路径\|UnlockData" docs/architecture.md
grep -n "legacy\|master\|DEK" console/backend/internal/service/datakey.go
grep -n "qubesair\.\|qubes\." dom0-scripts/policy.d/30-qubes-air.policy
grep -rn "node-version\|NODE_VERSION" .github/workflows/
grep -rn "README\.md" --include=*.md . | grep -v node_modules
grep -c 'CREATE TABLE IF NOT EXISTS' console/backend/internal/database/database.go
```

## 3. T5 — 门禁缺口

### 3.1 EP-2：`|| true` 检测（验收判据 ①）

`scripts/check-workflow-gates.mjs` 新增：

| 变更 | 内容 |
|---|---|
| 新规则 | `DISCARDED_STATUS = /\|\|\s*(true|:)(?=\s|$)/`，命中非注释行即判违规（`|| :` 是同一种丢弃，一并覆盖） |
| 规则输出 | 违规信息里回显实际匹配到的构造（`"|| true"` / `"|| :"`），不写死 |
| **修正既有缺陷** | `continue-on-error` 的安全步骤判定原本用**固定 8 行回看窗口**，会越过步骤边界读到上一个步骤的名字（既漏报也可能误报）。改为回看到**本步骤起始行**（`STEP_START` 锚定 `- name:/uses:/run:/…` 这些步骤键，避免把 `run: \|` 块内的 `- ` 行误当步骤边界） |
| 输出行 | 提示语改为逐条列出 4 条规则：`CI gates: no -no-fail, no "|| true", no floating action refs, no continue-on-error on scan steps`（原为 `no bypassed security scans`，那句覆盖不了 license 步骤，属过度承诺） |

**失败路径验证**（临时文件，**验证后已删除**）：构造 `.github/workflows/zz-guard-probe.yml`，一次跑出 4 类违规：

```
CI gate violations:
  .github/workflows/zz-guard-probe.yml:16 discards the command's exit status with "|| true", so the step cannot fail
  .github/workflows/zz-guard-probe.yml:24 discards the command's exit status with "|| :", so the step cannot fail
  .github/workflows/zz-guard-probe.yml:37 sets continue-on-error on a security scan step
  .github/workflows/zz-guard-probe.yml:55 pins an action to a floating branch: - uses: actions/checkout@main
exit=1
```

同一探针里还放了一个反例：某步骤名叫 `Run trivy`、**下一个**步骤才是带 `continue-on-error` 的普通
"Report outdated packages" 步骤 —— 该行**没有被报告**，证明步骤作用域修正消掉了旧窗口的误报；
同时 `gosec`/`trivy` 同名步骤自身带 `continue-on-error` 时仍被报告，没有把真检查一起放过。

删除取证：

```
$ rm -f .github/workflows/zz-guard-probe.yml
$ git status --short .github/workflows/      →  M .github/workflows/dependency.yml   （只有这一处）
$ ls .github/workflows/                      →  build.yml codeql.yml dependency.yml docs.yml lint.yml release.yml security.yml
$ grep -rn "zz-guard-probe\|Guard Probe" .github/ scripts/  →  （无输出）
```

### 3.2 存量 2 处 `|| true` 处置（验收判据 ② 的前置）

| 位置 | 原写法 | 处置 | CI 语义变化 |
|---|---|---|---|
| `.github/workflows/dependency.yml:80-88`（原 `:81`） | `run: npm outdated \|\| true` | 改为裸 `run: npm outdated` + 显式 `continue-on-error: true` + 注释说明"仅报告，不判定；真正的门禁是上面的 `npm audit --audit-level=high`"；步骤名补 `(report only)` | **job 结论不变**。改动前：`\|\| true` 让步骤恒绿；改动后失败会显示为"失败但被 continue-on-error 容忍"的步骤，job 仍为 success。这既是任务给的优先方案（"显式 continue-on-error + 注释"），也让容忍写在 YAML 里而不是藏在 shell 里 |
| `.github/workflows/dependency.yml:120-131`（原 `:116`） | `go-licenses check ./... --disallowed_types=forbidden,restricted \|\| true`（同步骤 `:117` 本来就有 `continue-on-error: true`） | 删掉 `\|\| true`，保留原有的 `continue-on-error: true`，并加注释把"非阻塞是有意的、待定夺"写在步骤上 | **job 结论不变**：该步骤原本就带 `continue-on-error: true`，`\|\| true` 是**冗余的二次吞码**；删掉它只改变可见性（失败会标红而不是绿），不改变通过/失败 |

`make docs-check` 在这两处改动后仍 **exit 0**（3.4 有原始输出）。

**本机对 `go-licenses` 的实测（决定"不改 blocking"的依据）**：

```
$ go install github.com/google/go-licenses@latest          → exit 0，装上 v1.6.0
$ cd console/backend && go-licenses check ./... --disallowed_types=forbidden,restricted
  ...（大量 E 级日志：Failed to find license for github.com/slchris/qubes-air/console/...）
  → exit 0
$ cd console/backend && go-licenses check ./... --disallowed_types=permissive
  → exit 0        ← 把"宽松许可"也判为 disallowed，仍然 exit 0
$ ls LICENSE*   → No such file or directory        ← 仓库根没有 LICENSE
```

结论（**这是选择"不改 blocking"的实际理由，不是"以后再修"**）：该命令在当前仓库形态下**即使改
成 blocking 也不会失败**，因为它无法为自身模块分类（根缺 LICENSE），`--disallowed_types` 形同虚设。
所以本轮只做"把静默吞码变成显式可审计"，**真正的修复（补 LICENSE + 确认 invocation 真能失败）**
记为开放问题 **O-4**，交 QA / Sprint 2 —— 在本机无 CI 等价环境的前提下擅自把它设为阻塞门禁，
要么制造一个永不触发的假门禁，要么引入无法本机验证的 CI 变红风险，两者都比现状差。

### 3.3 EP-5：8 处"测试硬读仓库根文件"的存在性检查（验收判据 ②）

方案选择与理由：

- 候选 A「改 `.go` 测试用 `findRepoRoot` 辅助函数」被排除：那 8 处里有 2 处在
  `console/backend/internal/transport/grpc/`（`agent_e2e_test.go`、`issued_cert_e2e_test.go`），
  属并行分区 p1-go 的写集合，同一工作树内改同一文件 = 跨分区写冲突。
- 候选 B「在 `docs-check` 加脚本检查」被采纳。又因为 `Makefile`（`:138-140`）**不在本分区写集合**、
  无法新增 make 目标，检查只能落在 `make docs-check` 已调用的既有脚本里 → 加在
  `scripts/check-doc-links.mjs`（它本来就是"本地引用是否存在"的门禁）。

实现要点（`scripts/check-doc-links.mjs`）：

| 项 | 内容 |
|---|---|
| 匹配 | `/(?:os\.ReadFile\|os\.Open\|ioutil\.ReadFile\|filepath\.Abs)\(\s*"((?:\.\.\/)+[^"]*)"\s*\)/g` —— **只匹配读取调用 + 向上走的相对字面量** |
| 为何收紧 | 裸 `"../"` 字面量在本仓多为**路径穿越测试输入**（`"../firefox"`、`"../../etc/passwd"`、`s.path("../../etc/passwd")` 等 10 处），要求它们存在是荒谬的；收紧后**恰好命中 8 处**，与 `drift-check.md` EP-5 的清单一致 |
| 输出 | 新增一行 `Test fixture references: 8 repo files OK`；命中缺失时打印 `Missing repo files read by Go tests:` 并 `exit 1` |
| 原有用例 | 复用 `ignoredDirectories`，把 `markdownFiles` 泛化成 `filesUnder(dir, keep)`，markdown 侧行为不变（148 links 与改前口径一致） |

**命中清单（可复跑，摘要）**：

```
console/backend/internal/agent/exec_service_test.go:        ../../../../remote/qubes-rpc/qubesair.Exec
console/backend/internal/agent/filecopy_service_test.go:    ../../../../remote/qubes-rpc/qubesair.FileCopy
console/backend/internal/agent/invoker_test.go:             ../../../../remote/qubes-rpc/qubesair.Ping
console/backend/internal/agent/rekeydata_service_test.go:   ../../../../remote/qubes-rpc/qubesair.RekeyData
console/backend/internal/service/cloudinit_test.go:         ../../../../packaging/agent-deb/qubes-air-agent.service  (×2)
console/backend/internal/transport/grpc/agent_e2e_test.go:  ../../../../../remote/qubes-rpc/qubesair.Ping
console/backend/internal/transport/grpc/issued_cert_e2e_test.go: ../../../../../remote/qubes-rpc/qubesair.Ping
total = 8
```

**失败路径验证**（不碰仓库内任何 `.go`）：把脚本复制到 `/tmp/dsh-doclink-probe/scripts/`，配一个
假的 `console/backend/gone_test.go` 指向不存在的 `../../remote/qubes-rpc/qubesair.Gone`：

```
Markdown links: 0 local targets OK
Missing repo files read by Go tests:
  console/backend/gone_test.go -> ../../remote/qubes-rpc/qubesair.Gone
[exit code: 1]
```

验证后 `rm -rf /tmp/dsh-doclink-probe`（`/tmp` 内，仓库无残留）。

**`go test ./...` 全绿反证**（本分区未改任何 `.go`）：

```
$ cd console/backend && go test ./...          → exit 0
（36 行摘要：全部 ok / [no test files]，无 FAIL；internal/qrexec 0.380s、
  internal/transport/grpc 1.322s，其余 cached）
```

### 3.4 T5 门禁原始输出（任务要求的 `make docs-check`）

```
$ make docs-check
node scripts/check-doc-links.mjs
Markdown links: 148 local targets OK
Test fixture references: 8 repo files OK
node scripts/check-workflow-gates.mjs
CI gates: no -no-fail, no "|| true", no floating action refs, no continue-on-error on scan steps
exit=0
```

### 3.5 聚合门禁 `make pre-commit`（本分区最终实测通过）

```
$ make pre-commit          → pre_commit_exit=0
git diff --check HEAD --                                         OK
cd console/backend && go test -race -coverprofile=coverage.out ./...   OK（全 ok/[no test files]，无 FAIL）
lint-new / gosec-new / complexity-new / vuln-check               OK
cd console/frontend && npm ci && npm run check (warning 预算 0) && npm run build   OK
npm audit: frontend dependencies unchanged                       OK
ShellCheck: no changed shell files                               OK
node scripts/check-doc-links.mjs
Markdown links: 148 local targets OK
Test fixture references: 8 repo files OK
node scripts/check-workflow-gates.mjs
CI gates: no -no-fail, no "|| true", no floating action refs, no continue-on-error on scan steps
```

执行过程中出现过两次**与本分区产物无关或已被修复**的失败，如实记录：

| 次序 | 失败点 | 失败原因 | 处置 |
|---|---|---|---|
| 第 1 次 | `diff-check` | **本分区产物**：`scripts/check-doc-links.mjs:119: new blank line at EOF` | 已删除文末多余空行，复测 `git diff --check` exit 0 |
| 第 2 次 | `lint-new` | `Error: parallel golangci-lint is running` —— p1-go 正在同一工作树跑 `golangci-lint`，其互斥锁让本进程无法启动 linter（同一次运行里 `test-race` 已全绿） | 不抢锁、不改配置；待并行分区空闲后重跑 |
| 第 3 次 | — | **全部通过**（exit 0） | 见上方输出 |

未弱化既有检查：本轮不改 `--no-fail` / **不新增**任何 `continue-on-error`（licence 步骤那处是**原有**的，
本轮只是删掉与其冗余的 `|| true`）/ 未扩大 `nolint` / 未改 `.golangci.yml`。


## 4. T3 — 前端组件测试

### 4.1 覆盖选择

| 组件 | 行数 | 改前测试 | 本轮 |
|---|---|---|---|
| `MonitoringView.svelte` | 502 | 0 | **新增 6 用例** |
| `CredentialList.svelte` | 505 | 0 | **新增 9 用例** |
| `ZonesView.svelte` | 504 | 0 | 未覆盖（YAGNI：已满足"至少 2 个 0 测试组件"，且验收不要求） |
| `JobLog.svelte` | 192 | 0 | 未覆盖（同上） |

风格基线取自 `SettingsView.test.ts` / `LoginGate.test.ts` / `QubeList.test.ts` / `lib/api.test.ts`：
`vi.mock('../lib/api', async (importOriginal) => ({ ...actual, ... }))` + `vi.mocked(...)`、
`describe('<组件> <切面>')` 命名、`findByText/getByRole` 断言、每个用例前写"为什么需要这条"的注释。
**未改任何组件行为**。

### 4.2 用例清单（含负向路径）

`MonitoringView.test.ts`（6）：

| 用例 | 覆盖点 |
|---|---|
| renders the per-node numbers that describe the fleet, not the process | 集群容量表：`cpu_usage` 0..1 → `50.0%`、`mem_used/total` → `25.0%`、`7.0 GiB`（**防止两列口径被混淆**） |
| names why a zone reports no capacity instead of showing a bare HTTP reason | 负向：disconnected → `not connected`；connected 但调用失败 → `cluster unreachable`（区分两种"没容量"） |
| says so when no zone is configured… | 空态 |
| keeps the backend note with the numbers it describes | placeholder 横幅 + note 必须随数字一起出现（"Disk 0%" 曾被当成测量值） |
| shows an active alert with its severity and source | 告警渲染 |
| reports a failed metrics load and recovers on retry | **负向**：`ok:false` → 报错 + `Retry` 真的重发（断言 `apiFetch` 调用 2 次） |

`CredentialList.test.ts`（9）：

| 用例 | 覆盖点 |
|---|---|
| labels the credential type and says when it was never used | `proxmox` → `Proxmox` 标签、`lastUsed:null` → `Never` |
| states the empty case instead of rendering nothing | 空态 |
| reports a failed load and recovers on retry | 负向：加载失败 + Retry 恢复 |
| **sends nothing when the operator cancels the confirmation** | **取消负向路径**：`confirm` 返回 false → 断言 `confirm` 被调用 1 次、`apiFetch` **总计仍为 1 次**（只有首次加载）、且未对 `/credentials/c1` 发起任何请求、无 alert、卡片仍在 |
| **surfaces a refused delete instead of dropping the card** | **拒绝负向路径**：DELETE 返回 409 → `alert('Failed to delete')`，凭据仍在列表（它确实还存在） |
| deletes and refreshes the list once the confirmation is accepted | 正向：`/credentials/c1` + `method:'DELETE'`，之后刷新 → 共 3 次调用 |
| refuses to submit without a secret before calling the API | 负向：两次表单校验（`Name is required` / `Secret is required`）都没触达 API |
| posts to the API-relative path and closes the dialog on success | 回归钉子：URL 必须是 `/credentials`（`apiFetch` 自己会加 `/api/v1`，写绝对路径会变成 `/api/v1/api/v1/...` → 404）；含 `selectOptions(..., 'proxmox')`（Proxmox 必须可选） |
| keeps the dialog open and shows the backend refusal | 负向：后端 409 错误文案原样显示、弹窗不关 |

**负向用例有牙的反证（mutation）**：临时从 `CredentialList.svelte` 删掉
`if (!confirm(...)) return;` 守卫 → 该用例立刻失败（`expected "bound " to be called 1 times, but got 0 times`
于 `CredentialList.test.ts:108`），其余 8 条仍过；组件随即被完整还原（`git diff --stat` 为空）。
证明这条"取消路径"不是恒真断言。

### 4.3 T3 门禁原始输出

```
$ cd console/frontend && npm run test
 Test Files  6 passed (6)          ← 改前 4
      Tests  35 passed (35)        ← 改前 20（+15：MonitoringView 6 / CredentialList 9）
   Duration  4.76s
exit=0

$ cd console/frontend && npm run check
svelte-check found 0 errors and 0 warnings
exit=0                    ← 满足 Makefile:101 FRONTEND_WARNING_BUDGET=0

$ cd console/frontend && npm run build
vite v7.3.6 building client environment for production...
✓ 139 modules transformed.
dist/index.html 1.03 kB │ dist/assets/index-*.css 45.77 kB │ vendor-*.js 39.17 kB │ index-*.js 77.78 kB
✓ built in 1.26s
exit=0
```

> `console/frontend/dist/` 是构建产物（已 gitignore），不进 commit。

### 4.4 JS/TS 语法与类型检查（**不等同于 `npm run test`**）

本分区改了两个 `.mjs`、新增两个 `.ts`，所以补跑语言级检查。仓库**没有** eslint / prettier / biome
配置，也没有 `lint` 脚本或 `tsc` 依赖（`ls -a | grep -iE 'eslint|prettier|biome'` 在根与
`console/frontend` 均无命中，`git ls-files` 同样无命中）—— 配置在案的 JS/TS 检查就是
`package.json:10` 的 `check`（`svelte-check --tsconfig ./tsconfig.json`），本轮再加一次直接 `tsc`。

```
$ node --check scripts/check-doc-links.mjs            → exit 0
$ node --check scripts/check-workflow-gates.mjs       → exit 0
$ cd console/frontend && npx tsc --noEmit -p tsconfig.json   → exit 0
$ npx tsc --noEmit -p tsconfig.json --listFilesOnly | grep -E 'src/components/(CredentialList|MonitoringView)\.test\.ts'
  src/components/CredentialList.test.ts          ← 证明新测试文件真的进了 tsc 的 program
  src/components/MonitoringView.test.ts          （`tsconfig.json` include = `src/**/*.ts`）
$ npm run check（svelte-check）                       → found 0 errors and 0 warnings
```

**该项检查有牙的反证（mutation）**：把 `MonitoringView.test.ts:46` 的 `max_cpu: 8` 改成 `'eight'`，
`npx tsc --noEmit` 立刻报 `error TS2322: Type 'string' is not assignable to type 'number'.`（exit 2）；
还原后复跑 exit 0，`git diff --stat` 为空。

## 5. 开放问题（**不是"以后再修"，是逐条给了阻塞理由与下一步归属**）

| ID | 问题 | 证据 | 为什么不本轮修 | 建议归属 |
|---|---|---|---|---|
| **O-1** | `ticketTTL = 90 * time.Minute` 在 `internal/provider/proxmox/client.go:49` 与 `internal/scheduler/proxmox.go:53` **各定义一份**，改一处不会同步另一处 | `grep -n "ticketTTL" console/backend` 两处 `const` | 属存量重构（drift-check TD 类），需行为等价性验证，超出 T3/T4/T5 范围 | Sprint 2（重构类） |
| **O-2** | `docs/sprint-1/runtime-context.md:68` 写"表清单（10 张）"，代码实际 `CREATE TABLE IF NOT EXISTS` = **9** 处（该文件自身表格也只列 9 行） | `grep -c` = 9 | 该文件在"绝不修改"清单内，本分区无写权限；已在 `docs/runtime-defaults.md` §2 按代码登记并留差异说明 | 编排器（synthesis 时改工件） |
| **O-3** | Node 版本分叉：`lint.yml:15`/`build.yml:15`/`dependency.yml:23`/`docs.yml:29` = 20，`release.yml:90` = **22**，且无任何文档说明是有意为之 | workflow 原文 | 改 `release.yml` 会变 release 构建行为；本机无法复现 release 构建（linux/amd64 + CGO），且 `release.yml` 不在 T5 的 glob 内 | QA / Sprint 2 |
| **O-4** | `go-licenses check` 是**非阻塞**步骤（本轮已把 `\|\| true` 换成显式 `continue-on-error`）；且该命令当前**即使把宽松许可判为 disallowed 也 exit 0**（根因：仓库根无 LICENSE，go-licenses 无法为自身模块分类） | §3.2 的三条实测（forbidden/restricted → 0；permissive → 0；`ls LICENSE*` 不存在） | 见 §3.2：改 blocking 在本机形态下是**假门禁**；真修复需补 LICENSE 并在 CI 等价环境确认失败路径 | QA / Sprint 2 |
| **O-5** | `remote/qubes-rpc/qubesair.UnlockData:5` 的注释仍写 "the console derives it (HKDF over its master secret + this qube's id)"，与 DEK 语义（派生键仅作迁移凭据）不符；D-2 的同类漂移在**脚本侧**仍未消解 | 脚本原文 `:5-6` vs `internal/service/datakey.go:56-92` | `remote/**` **不在本分区写集合**（属另一分区/编排器裁决） | 编排器 / 下一分区 |
| **O-6** | `check-workflow-gates.mjs` 的 `SECURITY_STEP` 关键词表不含 `license`，所以 licence 步骤上的 `continue-on-error` 不会被判违规 | 脚本 `:22` 的关键词表 | 若把 `license` 加进去，现存 `dependency.yml` 的 licence 步骤会**立刻**变红，而把它改成 blocking 已被 O-4 证明无意义 → 会产生一个本机无法验证的红门禁。本轮先保证输出提示语不再过度承诺（删掉 "no bypassed security scans"） | QA / Sprint 2（与 O-4 一起处置） |
| **O-7** | EP-5 检查的覆盖边界：只覆盖 `os.ReadFile`/`os.Open`/`ioutil.ReadFile`/`filepath.Abs` 的**字面量**参数；变量拼接、`os.Stat`、以及非 Go 测试（如 shell）的仓库文件引用不在覆盖内 | 脚本正则 | 扩大范围会引入对穿越测试输入的误报（本仓有 10 处），性价比本轮不成立 | Sprint 2 候选 |

## 6. 与 plan §5 红线的核对

本分区产物**没有**把任何真机项写成已实现/已完成：`docs/runtime-defaults.md` 只登记代码里的默认值；
新增的两篇文档段落只描述 policy 授权与脚本位置，并显式写明 `qubesair.Status`/`Deploy` 的脚本
**不在本仓库**（权威来源是外部 `qubes-salt-config`）；T3 的测试全部是 jsdom 组件测试，不声称任何
真机或 E2E 通过。OPS-01 / NET-01 / QA-01 剩余 / GUI-01 / CLOUD-01/02 在本文件中仅作"排除项"出现。

## 7. 复跑命令（供 QA 独立复核）

```bash
cd /Users/chris/git/slchris/project/qubes-air
make docs-check                                  # 期望 exit 0，三行输出：148 links / 8 fixtures / CI gates
cd console/backend && go test ./...              # 期望 exit 0
cd ../frontend && npm run test                   # 期望 6 files / 35 tests passed
npm run check                                    # 期望 found 0 errors and 0 warnings（svelte-check，含 .ts）
npx tsc --noEmit -p tsconfig.json                # 期望 exit 0（直接 TS 检查，覆盖新 .test.ts）
npm run build                                    # 期望 built
cd ../.. && node --check scripts/check-doc-links.mjs && node --check scripts/check-workflow-gates.mjs   # 期望 exit 0
make pre-commit                                  # 期望 exit 0（BASE_REV=HEAD；p1-go 不并发跑 golangci-lint 时）
git status --short                               # 本分区改动的 11 个文件（见 §1）
```
