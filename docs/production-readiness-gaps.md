# 生产可用性缺口清单

整理日期：2026-09-22。基准 revision：`4f52953`（`kixpower/sprint-1`，Sprint 1 收尾）。
本文只回答一个问题：**离"能拿它当生产用"还差什么**。所有结论附可回放命令或 `文件:行号`；
无法证实的写在 §5，不写成结论。

## 0. 结论

"生产可用"对本项目有两个不同高度的目标，混在一起谈会得出错误的距离感：

| 档 | 定义 | 当前 | 还差什么 | 粗估 |
|---|---|---|---|---|
| **A 受控自用生产** | 一个人在自己的 Qubes + Proxmox 环境上，长期用它跑真实工作负载：装得上、升级有契约、坏了能恢复、核心功能有真机证据 | **未达标** | 交付链未闭环（§2.A）、核心功能无真机正值（§2.B）、恢复演练未离机（§2.C）、§2.H 的 4 条 A-阻塞运行时缺陷 | 2 个 Sprint |
| **B 对外发布** | 陌生人按文档装起来能用：多用户身份、监控告警、桌面闭环、许可证与发布材料 | **未开始** | A 档全部 + §2.E / §2.G | A 档之上再 3+ Sprint |

一句话判断：**工程质量面已经明显好于同阶段项目，缺的不是"代码风格或门禁"，而是"产品闭环 + 运行时语义"**——
交付链从未在 CI 上跑过、核心功能（Exec/FileCopy）从没有端到端跑通过一次、恢复演练没离开过本机；
另有一次独立补盲审计（§2.H）在运行时行为上找到 3 处会在**正常路径**上直接打断生产的缺陷
（job 超时短于真机 provision 时长、purge 的不可逆步骤先于入队、健康检查实际不检查数据库）。

三个里程碑（详见 §3）：

- **M0 让当前这版可被信任**：push → CI 全绿 → 合并 → 真机冒烟。几乎不写代码，但它是其余一切的前置。
- **M1 补齐 A 档硬阻塞**：Exec/FileCopy 下发能力 + 真机正值、§2.H 的 5 条运行时缺陷（含 3 条正常路径缺陷）、离机恢复 + RTO、备份调度、升级回滚契约。
- **M2/M3 产品化**：§2.H 其余 5 条、多用户身份、监控告警、桌面闭环、发布治理、云 provider。

### 0.1 执行状态（2026-09-22 首轮）

M0 已启动。首轮 CI 的结果本身就是本清单最想要的证据：它把"积压期间没人看过"变成了三条具体的失败。

| 项 | 状态 | 证据 |
|---|---|---|
| M0-1 push + PR | 完成 | `kixpower/sprint-1` 已推，PR [#9](https://github.com/slchris/qubes-air/pull/9)；直接 push `main` 被 blast-radius 钩子拒绝，改走 PR |
| 首轮 CI | **21 个 check：18 通过 / 3 失败** | 通过项含 Go Test、Go Lint、Frontend Lint、Build(Go/Svelte)、Docs and Gates、CodeQL(go/js)、Dependencies(Go/NPM/License)、Trivy、ShellCheck、YAML Lint |
| M0-2 失败① Secret Scanning | 已修 `fa2b8a4` | 4 条全是占位 fixture；按值放行，理由与风险写在 `.gitleaks.toml` |
| M0-2 失败② Agent package install smoke | 已修 `3afbcd7` | 版本排序依赖 commit hash 首字符（字母开头 → `0.0.0+<hash>` → 被判降级）；`0~smoke-old` 修掉 |
| M0-2 失败③ GoSec Security Scan | 已修 `a118c3c` + `5f1bf72` | 29 条存量发现：28 条处理完、1 条（bootstrap G402）作为显式规则豁免登记（G-H11）；根因 G-F8 的门禁不等价一并修掉 |
| 第二轮 CI | 21 个 check：**19 通过 / 2 失败** | gosec 与 deb 冒烟已转绿；新失败是 Secret Scanning（gitleaks 版本差异，见 G-F9）与 CodeQL（4 条既有告警被重新归属，见下） |
| M0-2 CodeQL 4 条 | 已处置（dismiss） | 3 条 `go/disabled-certificate-check` 按 false positive（CodeQL 不建模 `VerifyConnection` 回调，而 `AGENTS.md` §5 要求的正是该回调）、第 4 条（bootstrap）按 won't fix 并引用 G-H11；查询保持开启，将来真出现无回调的 `InsecureSkipVerify` 仍会被抓 |
| M0-2 审查驱动的补修 | 已改（待提交） | 独立审查确认 18 条抑制理由成立，另查出并已修：relay-call:121 同类日志注入、agentprobe 注释把 EKU 写反、`AGENTS.md` 行号引用错、`pre-commit` 未跑 CI 那个 gosec 程序（新增 `gosec-ci-new` 增量接入）；新登记 G-F10（两处入口缺 G402 负例测试） |
| M0-2 本地全量门禁 | 通过 | `make audit` exit 0（含新 `gosec-ci`）；`make pre-commit` exit 0；`go vet ./...`、`gofmt -l` 干净；独立 `gosec@v2.29.0 -exclude-generated` 0 条 |
| 第四轮 CI | **21/21 全绿** | 三项历史失败（Secret Scanning、Docs and CI Gates、CodeQL）全部转绿，无一项靠抑制或跳过 |
| M0-3 合并 main | 已完成 `5f0fd88` | 21/21 全绿后用 **merge commit** 合并（不能用 squash/rebase：会改写 `4f52953` 这些已被 QA 记录的 revision，已核对仍在 main 历史里）；合并后在 main 上重跑 `make audit` **rc=0** |
| M1-11 job 超时 | 已完成 `1731d3d` | 默认 15 分钟 → 45 分钟并可配置；真机复现待 M0-5 |
| M1-13 XFF 可伪造 | 已完成 `f8e154a` | `SetTrustedProxies(nil)` + 负向测试；关掉修复即复现 |
| M0-5 / M1-2 / M1-3 / M1-4 真机项 | **环境阻塞** | 本机没有 dom0/Qubes 入口：`~/.ssh/config` 无 `mgmt-jump`，`chris-dev` 拒绝公钥。经 `NAS` 可确认 PVE `10.31.0.200:8006` 与 QA 吊销端点 `10.31.0.135:18080` 在线，但 lifecycle 冒烟必须在 Qubes 侧执行 |

## 1. 判定基线

**A 档（自用生产）判据**，六条全过才算"能用"：

1. 交付链闭环：目标 revision 被 CI 完整覆盖且全绿，产物可版本化获取（不依赖开发机局域网）。
2. 核心功能有真机正值：provision → 数据持久 → Exec/FileCopy → suspend/resume → purge 全链路在真机跑通并留记录。
3. 恢复有契约：离机归档 + 真实密钥保管 + 实测 RTO。
4. 升级有契约：console / agent / schema 的升级顺序、兼容边界与回滚路径成文并可执行。
5. 无已知 P0 未关闭；安全边界（TLS、撤销、审计留存）有部署要求且被满足。
6. 运维闭环：备份有调度、审计有留存、失败有可见性。

**B 档（对外发布）**追加：多用户身份（2FA/用户管理）、监控告警账单接真实数据源、无缝桌面、许可证与发布材料、外部安全审计、provider 可移植性。

**不在承诺内**（沿用[路线图](roadmap-to-production.md)第 45-50 行）：在线内存迁移、把普通主机变成完整 dom0、依赖云厂商删除替代端到端加密、未验证前宣称 GCP/AWS 等价。

## 2. 缺口清单

阻塞列含义：**A-阻塞** = 不解决就不能算 A 档；**A-需要** = A 档应有、可短期绕过；**B-阻塞** = 只挡对外发布；技术债 = 不挡可用性，挡长期回归风险。

### 0.2 M0 收尾结果（2026-09-22）

M0 的产出不是"CI 绿了"，而是**第一次把积压的 24 个 commit 交给 CI 后暴露了什么**：

1. **三个真实失败，没有一个能靠抑制关掉**：gitleaks 的配置发现路径、deb 升级测试的版本排序、29 条 gosec 存量发现。
2. **一类系统性根因**：本地门禁与 CI 不是同一程序——gosec 内嵌 vs 独立（G-F8）、gitleaks 8.30.1 vs action 捆绑的 8.24.3（G-F9）。
   两次都表现为"本地绿、CI 红"，第二次本地结论是**空证据**。已用 `gosec-ci`/`gosec-ci-new` 把 CI 那一侧接进本地门禁。
3. **两个顺带修掉的生产缺陷**：限流键/审计来源可被 `X-Forwarded-For` 伪造；编排 job 15 分钟超时短于自述的 15-25 分钟 provision。
4. **三项登记而非抹平的东西**：G-H11（bootstrap 无可 pin 身份，对 `AGENTS.md` §5 的显式豁免）、G-D7（snippet share 把一次性 token 落到 0644，
   转为 M1-7 的部署硬要求）、G-F10（两处 G402 校验缺负例测试）。

M0 **没能**闭合的两项是环境阻塞，不是判断：**M0-5**（真机 lifecycle 冒烟）与 **M0-6**（`qubes-salt-config` 的 QA-01 改动提交并打 tag）
都需要 dom0/Qubes 入口，本机没有。

### 2.A 交付链（最先做，且是其余一切的前置）

| ID | 缺口 | 证据 | 阻塞 | 验收条件 |
|---|---|---|---|---|
| G-A1 | 本地 `main` 领先 `origin/main` **20 个 commit**，`kixpower/sprint-1` 再领先 3 个；这批 commit（含 P0 安全加固、REL/DATA-01、QA-01 修复）从未被 CI 覆盖 | `git rev-list --left-right --count origin/main...main` → `0 20`；`git log --oneline origin/main..main` | **A-阻塞** | push 后 7 个 workflow 在目标 SHA 全绿；`docs/qa/qa-signoff-1.md` 的 `ci_pending` 转 PASS（该签署记录未纳入版本库，故只写路径不建链接） |
| G-A2 | 工作停在 `kixpower/sprint-1`，未合并回 `main`（`main` 是 20 commit 的另一个头） | `git merge-base --is-ancestor kixpower/sprint-1 main` → 否 | **A-阻塞** | sprint 分支合入 `main` 且合并后 CI 绿 |
| G-A3 | 过时分支未清理：`fix/security-audit` 的 1 MiB body cap 已被 `main` 的 `bodylimit` 中间件取代；`feat/mcp-server`（`fe827e4`）与 `origin/main`（`3b573c0`）**内容 tree 相同但 commit 不同**（同一条 message，不同 SHA），两者都已落后于本地 `main` | `git ls-tree -r main --name-only \| grep bodylimit` → `console/backend/internal/middleware/bodylimit.go`；`middleware/bodylimit.go:11-25`、`cmd/server/main.go:960`；`git rev-parse 'feat/mcp-server^{tree}' 'origin/main^{tree}'` 同值 | 技术债 | 两个分支删除或明确标注废弃 |
| G-A4 | 从未发布过任何版本：无 tag、无 release | `git tag -l` 为空 | A-需要（B-阻塞） | 至少一次 `v*` tag 走通 [release.yml](../.github/workflows/release.yml) 并产出 `SHA256SUMS` |

### 2.B 真机验收（A 档最大的一块）

| ID | 缺口 | 证据 | 阻塞 | 验收条件 |
|---|---|---|---|---|
| G-B1 | **console 的 provisioning 路径不下发 `QUBESAIR_EXEC_ALLOW` / `QUBESAIR_FILECOPY_ROOTS`**：cloud-init 只写 `QUBESAIR_REMOTE_NAME`/`LISTEN`/`ALLOW`/`REVOCATION_URL` 四个键。从 console 打开 `qubesair.Exec` 后，agent 侧仍因 allowlist 为空而拒绝全部调用 | `internal/service/cloudinit.go:299`；`remote/qubes-rpc/qubesair.Exec:60-62`（空 allowlist → `reject(..., 77)`）；全仓 grep 该两个变量只命中 remote 脚本、文档与测试 | **A-阻塞** | console 能随 qube 下发 Exec/FileCopy 白名单；真机 Exec 正值（`/usr/bin/id`）与负值（未允许程序）各有记录 |
| G-B2 | Exec/FileCopy 无真机正值验收——而这是本项目对用户的核心承诺 | [QA-01 记录](reviews/2026-09-22-qa01-proxmox.md) 第 66-69 行 | **A-阻塞** | 同 G-B1；FileCopy push/pull 往返在真机留证 |
| G-B3 | suspend/resume 的**数据持久性**未验证（只验证了数据盘保留与重新解锁，未验证文件真的还在） | 同上第 70 行 | **A-阻塞** | 写入文件 → suspend → resume → 读出同一内容，落记录 |
| G-B4 | 旧盘 DEK 迁移（DATA-01）无真机验证，环境里没有 per-qube DEK 之前的加密盘 | 同上第 71 行；[data-keys 记录](reviews/2026-09-21-data-keys.md) 第 40 行 | A-需要 | 按 [runbook §5](runbook-qa01.md) 在有旧盘的环境补迁移验收 |
| G-B5 | 节点 SSH known_hosts 是 `ssh-keyscan` 的 TOFU 结果，未与带外指纹核对；PVE 集群版本同样未带外核对 | QA-01 记录第 19、73 行 | A-需要（安全） | 带外取得指纹并替换 TOFU 结果，记录核对方式 |
| G-B6 | 真机证据的 revision 绑定：QA-01 跑在 `fae0aea`。**Sprint 1 未改任何生产代码**（diff 只有测试/文档/脚本），故代码面证据仍成立；但合并后任何 `.go` 改动都会使其失效 | `git diff --name-only fae0aea 4f52953`（无 `.go` 生产文件、无 `.svelte`） | A-需要 | 每次合并进 `main` 后重跑一次真机生命周期冒烟，记录新 SHA |

### 2.C 恢复与运维（A 档阻塞）

| ID | 缺口 | 证据 | 阻塞 | 验收条件 |
|---|---|---|---|---|
| G-C1 | OPS-01 剩余：离机归档、真实 keyring、provider 资源对账、agent 信任校验、生产数据量 RTO 全部未做 | [ops01 预演](reviews/2026-09-21-ops01-restore.md) 第 52-58 行；[TODO](TODO.md) 第 46-49 行 | **A-阻塞** | 归档经网络/介质到第二台机器，用真实 keyring 恢复，实测 RTO 与人工步骤 |
| G-C2 | 备份没有调度：仓库内只有 `qubes-air-backup` CLI 与 `internal/backup` 包，无 timer/cron 接线、无保留策略 | `grep -rn backup internal/scheduler/*.go cmd/server/main.go` 无命中；`cmd/` 下有 `qubes-air-backup` | **A-阻塞** | 备份有明确触发方式与保留策略，且被文档化为运维步骤 |
| G-C3 | 无 console 升级/回滚契约：schema 迁移是**前向单向**的（`user_version` 单调，备份拒绝更新版本），回滚的实际手段是"从备份恢复"，但没写进文档、也没演练过 | `internal/database/database.go:232`（`SchemaVersion`）、`:290-305`（打开更新的库时报错拒绝）；升级仅在 [quickstart](quickstart.md) 第 33 行一句话；[runbook](runbook-remotevm.md) §10 只覆盖 agent 发布与单 compute 故障 | **A-阻塞** | 一篇升级/回滚 runbook：console 二进制、web tarball、agent deb 的升级顺序与兼容边界；schema 升级前必做的备份；回滚=恢复备份并验证 |
| G-C4 | 产物分发仍依赖局域网 artifact store（`10.31.0.2`），离开该网段无法 bootstrap——这正是 [release.yml](../.github/workflows/release.yml) 存在的理由，但该 workflow 从未跑过 | `release.yml` 第 1-20 行自述；`docs/bootstrap-design.md`:66-72；G-A4 | A-需要（B-阻塞） | 用 release 制品（URL + SHA256）完成一次 provision，不依赖 LAN 地址 |
| G-C5 | artifact store 的认证/签名与发布审计未定义 | [bootstrap-design](bootstrap-design.md) 第 132 行 | B-阻塞 | digest 由可信通道下发 + 发布审计可追溯 |

### 2.D 安全与身份

| ID | 缺口 | 证据 | 阻塞 | 验收条件 |
|---|---|---|---|---|
| G-D1 | 单操作者模型：登录=粘贴 API token，无用户账户、无 2FA（UI 已如实标注"不可用"） | [SettingsView.svelte](../console/frontend/src/components/SettingsView.svelte) 第 238-249 行；[security-controls](security-controls.md) 第 82 行"不是完整多租户" | A-需要 / **B-阻塞** | 用户模型 + 2FA + 权限分层，含失败路径测试 |
| G-D2 | console 默认可以明文 HTTP 对外服务（TLS 是可选配置 `IsTLSEnabled`），session cookie 因此不能带 `Secure`；部署文档只要求"受限 CORS"，未把 TLS 或"仅 loopback"写成硬要求 | `cmd/server/main.go`:1256-1270（HTTP/HTTPS 二选一）；`handler/session_handler.go`:80-83（`secure` 由调用方决定） | **A-阻塞** | 生产部署要求成文（TLS 或仅本机监听），并在部署 checklist 中可核对 |
| G-D3 | 审计只有 `io.Writer` 记录器，无持久化、轮转、归档与留存期 | `internal/audit/audit.go`:45 `NewRecorder(w io.Writer)` | A-需要 / B-阻塞 | 审计落地（文件/DB）+ 轮转 + 留存策略 |
| G-D4 | 无外部安全审计/渗透测试；现有结论来自自查与 P0 加固记录 | [P0 安全记录](reviews/2026-09-20-p0-security.md) 范围自述 | B-阻塞 | 一次独立审计或明确声明"未审计" |
| G-D5 | session 存内存 map，console 重启即全员登出 | `internal/middleware/session.go`:39-52 | A-需要（写进运维预期即可，不一定要改） | 文档明确该行为，或改为持久 session |
| G-D6 | **真实基础设施地址已存在于公开历史**：仓库是 public，`10.31.0.x`（内网段、artifact store、节点名、QA-01 记录里的具体主机与吊销端点）出现在 8 个已公开文件与本次待推的 5 个新文件中，违反 `AGENTS.md` §5「不得提交真实基础设施地址」 | `git grep -lE "10\.31\.0\.[0-9]+" origin/main` → 8 个文件（含 `internal/config/config.go`、`.github/workflows/release.yml`）；待推范围新增 `docs/reviews/2026-09-22-qa01-proxmox.md` 等 5 个 | A-需要（已决策） | 2026-09-22 决定**接受**：增量暴露仅几个临时租约 IP，而改写 23 个 commit 会作废 `l2_verified_sha`/`qa_verified_sha` 整条信任链。后续新文档不得再写真实地址；是否做一次性历史清理由发布决策定 |
| G-D7 | snippet 共享目录/文件是 `0755`/`0644`，而文件里含**一次性 bootstrap token** 与公开 CA（无私钥）——机密性完全落在"谁能挂载这个 share"上；`AGENTS.md` §5 把一次性 token 按 secret 处理，这里等于用共享目录的导出策略代替文件权限 | `internal/service/cloudinit.go`:722（`MkdirAll` 0755）、`:741-763`（`writeSnippetAtomic`，落盘前 `Chmod` 0644）；gosec G301/G302 已按"读者是节点侧另一用户"的理由抑制 | A-需要 | 写进 M1-7 部署安全要求：该 share 只导出给 PVE 节点；并把"token 落在 0644 文件"作为已知暴露面登记，而不是只留在 `#nosec` 理由里 |

### 2.E 产品功能（B 档为主）

| ID | 缺口 | 证据 | 阻塞 | 验收条件 |
|---|---|---|---|---|
| G-E1 | UI-01：设置页的 session timeout / 2FA / 邮件 / webhook 存了不生效 | [SettingsView.svelte](../console/frontend/src/components/SettingsView.svelte) 第 213、238-240 行 | B-阻塞 | 每项接入并给出端到端证据；未接入项继续显示"未实现" |
| G-E2 | OBS-01：监控与账单是 placeholder（CPU/磁盘恒 0；无成本数据源） | `internal/handler/monitoring_handler.go`:53,55；`internal/handler/billing_handler.go`:11,51,56 | B-阻塞 | 接真实数据源；过期/缺失不得伪装为正常值；移除 placeholder |
| G-E3 | GUI-01：无缝桌面（appmenu、单击启动、多窗口、断线恢复）未闭环 | [TODO](TODO.md) 第 61-62 行 | B-阻塞 | 桌面闭环验收，含 Xpra 与 RemoteVM 权限边界 |
| G-E4 | 前端无 E2E 框架（无 playwright/cypress），QA-02 剩余"真实首次 bootstrap、应用启动 E2E、取消场景"只能手工 | `console/frontend/package.json` 无 E2E 依赖；[sprint-1 进展](sprint-1/progress.md) 第 343-347 行 | B-阻塞 | E2E 覆盖登录→创建→provision→purge 主路径 |
| G-E5 | MCP-01 桌面帧与输入仍显式失败；MCP-02 HTTP transport 未决 | [TODO](TODO.md) 第 79-82 行；`cmd/qubes-air-mcp/main.go`:40（`enableComputerUse` 的说明写明帧采集与输入注入未实现） | B-阻塞 | 定义可见接管提示/中断/输入授权后再实现 |
| G-E6 | CLOUD-01/02：GCP/AWS 原生适配器未实现（未验收前不得宣称可用） | [TODO](TODO.md) 第 77-78 行；`AGENTS.md` 第 10 行 | B-阻塞 | 各自独立生命周期 + 销毁验收 |
| G-E7 | UI 侧无 zone 可见性降级（AUTH-01 已声明边界） | [security-controls](security-controls.md) 第 82 行 | A-需要 | 越权对象在 UI 不可见或明确置灰 |

### 2.F 工程质量债（不挡可用性，挡长期回归风险）

| ID | 缺口 | 证据 | 阻塞 | 验收条件 |
|---|---|---|---|---|
| G-F1 | 覆盖率 61.0% 无阈值门禁；`cmd/*` 13 个入口包近乎 0% | [sprint-1 进展](sprint-1/progress.md) 第 102-104 行 | 技术债 | 覆盖率阈值门禁 + 入口包冒烟测试 |
| G-F2 | 热路径复杂度：`Tunnel` 262 行 / gocyclo 44；`loadFromEnv` 199 行 / gocyclo 71；`Validate` gocyclo 28。本阶段新增一个配置项即印证了这条判断：`loadFromEnv` 变为 **204 行 / gocyclo 73**（`gocyclo -top` 实测） | PROJECT_BRIEF §9.2 R-TECH-1/2；实测见 [runtime-defaults](runtime-defaults.md) 的 config.go 行号位移 | 技术债 | 按业务步骤拆分，不调阈值（`AGENTS.md` 第 44-46 行）；拆分前每加一个配置项都会继续推高 |
| G-F3 | 存量 `nolint` 24 处，无"何时可移除"的退出条件 | PROJECT_BRIEF §9.2 R-TECH-5 | 技术债 | 每处补退出条件或删除 |
| G-F4 | 前端 15 个组件仅 5 个有测试；`QubeList.svelte` 970 行只覆盖 7 个用例 | `console/frontend/src/components/`；PROJECT_BRIEF §9.2 R-TECH-4 | 技术债 | 大组件拆分 + 关键路径测试 |
| G-F5 | 工具链不一致：CI 内 Node 20 与 `release.yml` 的 22 并存；本机无 `yamllint`，`yaml-lint` job 本地不可复现 | PROJECT_BRIEF §3 已知不一致、§6 门禁环境缺口 | 技术债 | 统一运行时版本；补齐本地工具 |
| G-F6 | 文档遗留：服务表未覆盖 policy 实际授权的 5 个服务（R-DOC-4）；`UnlockData:4-6` 注释仍写 master 派生密钥（QA O-5）；`runtime-context.md` 写"10 张表"实为 9 表 7 索引（QA O-2） | PROJECT_BRIEF §9.1 R-DOC-4；QA 签署记录 O-2/O-5 | 技术债 | 逐条与代码比对后修正，附 `文件:行号` |
| G-F9 | **同一类"本地工具 ≠ CI 工具"再次出现，这次是 gitleaks 版本**：CI 里 `gitleaks-action` 固定捆绑 **8.24.3**，本机 CLI 是 **8.30.1**。`.gitleaks.toml` 用新式 `[[allowlists]]` 时，8.30.1 生效、8.24.3 **静默忽略**——配置被读入（日志有 `using gitleaks config from GITLEAKS_CONFIG env var`）、4 条 fixture 照报，于是"本地 0 漏"是**空证据**。已改为两边都认的单数 `[allowlist]` 并写进配置注释 | 实机复现：`GOBIN=/tmp/gl824 go install github.com/zricethezav/gitleaks/v8@v8.24.3`，同配置同范围下 8.24.3 报 4 条 / 8.30.1 报 0 条；改单数后两者都 `no leaks found`，且塞入真密钥仍被抓到 | A-已解除（配置已修） | 扫 `yaml`/`toml`/检查脚本类配置改动后，一律用 CI 那一侧的版本复验；把"CI 各 action 捆绑的工具版本"记进文档（与 G-F8 同一根因） |
| G-F10 | **两处新加抑制的 G402 校验没有负例测试**：`cmd/pingcheck` 与 `cmd/relay-call` 都没有 `_test.go`（实测 0 个），而 `AGENTS.md` 第 65-66 行要求 `InsecureSkipVerify` 必须有"错误 CA、错误角色、错误 target、过期证书"四类反例。两处校验代码本身经独立审查确认完整（CA + ServerAuth + `RoleOf == RoleAgent` + CN 都在，回调也确实装在会上线的 `tls.Config` 上），缺的是把它们钉住的测试 | 独立审查读代码确认 `cmd/pingcheck/main.go`:92-117、`cmd/relay-call/main.go`:342-370；对照 `internal/service/agentprobe_test.go` 已有错误 CA/无证书/垃圾证书/错误 target 四类负例，而这些包一个都没有 | A-需要（不挡 M0，挡"安全控制必须有失败路径测试"这条规则） | 把两处内联回调提成有名函数，按 agentprobe 的负例矩阵补齐四类；`internal/transport/grpc/role_enforcement_test.go` 只覆盖服务端配置，不能替代 |
| G-F7 | `go-licenses check` 是否转为 blocking 未定（移除 `\|\| true` 后仍带 `continue-on-error`） | [sprint-1 计划](sprint-1/plan.md) §1.5 开放问题 | 技术债 | 在 CI 上确认其真实退出状态后决定 |
| G-F8 | ~~本地与 CI 的安全扫描不是同一个程序~~ **已修（`5f1bf72`）**，门禁因此不等价：本地 `make gosec-all` 跑 golangci-lint 内嵌 gosec（认 `//nolint:gosec`，由 `.golangci.yml` 配置），CI 跑独立 `gosec@v2.29.0`（认 `#nosec`）。同一个 revision 本地 0 条、CI **29 条**。今日 CI 首跑才发现 | `Makefile`:145-146（golangci-lint）vs `.github/workflows/security.yml` 的 `gosec` job（`go install ...@v2.29.0` + `gosec -fmt sarif ./...`）；差异由 commit `6a2623e` 引入该 pin 时产生 | 已解除 | ✅ 新增 `make gosec-ci`（同版本 `@v2.29.0`、同参数、仅输出格式不同）并接进 `make audit`；`-exclude-generated` 两边一致，实测只排掉 2 个 protoc 生成文件（123→121 文件 / 29400→28491 行）。本地 `make audit` 与 CI 现对同一 revision 得到同一结论 |

### 2.G 发布治理（B 档阻塞）

| ID | 缺口 | 证据 | 阻塞 | 验收条件 |
|---|---|---|---|---|
| G-G1 | 无 `LICENSE`（PUB-01）；无 `SECURITY.md`、无 `CHANGELOG`、无漏洞报告入口 | `git ls-files` 匹配根级治理文件为空 | B-阻塞 | 决定开源则补许可证与安全报告入口 |
| G-G2 | 无版本兼容矩阵文档：proto 已区分 `protocol_version`（线协议）与 `build_version`（可观测），但没有成文的 console↔agent 兼容策略 | `console/backend/proto/relay_transport.proto`:60-69 | B-阻塞 | 兼容矩阵 + 升级顺序成文（与 G-C3 合并做） |
| G-G3 | 发布说明必须引用同版本构建、完整 `make audit` 与真机结果——流程未成文 | [TODO](TODO.md) 第 83-84 行 | B-阻塞 | 发布检查单 + 一次演练 |
| G-G4 | Qubes 侧（外部仓库 `qubes-salt-config`）在 QA-01 期间的改动**未提交**：known_hosts 固定、`mgmt.remotevm.register` state、console listen/cors/revocation/allowed_services 配置 | QA-01 记录第 59-64、77 行 | **A-阻塞** | 该仓库改动提交并打版本；否则部署不可复现 |

### 2.H 运行时行为（一次独立补盲审计的发现，已逐条回读代码验证）

> 本节不在原主题清单里。它的 10 条不属于 §2.F 的"技术债"：每一条要么会在真机的**正常路径**上直接失败，
> 要么让生产运维依赖的信号失效。**G-H1 / G-H3 / G-H5 是本清单里最具体的三个生产缺陷。**

| ID | 缺口 | 证据 | 阻塞 | 验收条件 |
|---|---|---|---|---|
| G-H1 | ~~编排 job 硬编码 15 分钟超时~~ **已修（`1731d3d`）**：原 `DefaultJobTimeout` 为 15 分钟且装配处没传 `RunnerConfig.Timeout`，而代码三处自述一次 provision 要 15-25 分钟。真机复现仍待 M0-5，本次依据是静态证据（proxmox `WaitTask` 只等到 ctx 过期，下层单请求 30s 不是约束点） | 修前：`git show fae0aea:console/backend/internal/orchestrator/runner.go` 第 135-138 行；修后 `internal/orchestrator/runner.go`:164-176（`RunnerConfig.Timeout`）、`:186`（`DefaultJobTimeout = 45 * time.Minute`）、`:328`（`context.WithTimeout(r.base, r.timeout)` 覆盖整个 job；本次 `Step` 插入后行号已重算）；`cmd/server/main.go`:525-536 显式传 `cfg.JobTimeoutSeconds`；配置项 `internal/config/config.go`:306、默认值 `:552`；自述时长 `internal/orchestrator/joblog.go`:15、`internal/handler/job_handler.go`:166 | 已解除 | ✅ 默认值登记进 [runtime-defaults](runtime-defaults.md) UD-1e；剩余：真机长 provision 不落 failed（M0-5） |
| G-H2 | `GET /health` 只做 `PingContext`，而 go-sqlite3 的 `Ping` 在连接对象非 nil 时直接返回 nil（不发 SQL、不碰库文件）——磁盘满/只读/库文件丢失时仍报 healthy；也不检查 worker、队列、巡检 | `cmd/server/main.go`:942、`:1096-1117`；`internal/database/database.go`:84-86；`mattn/go-sqlite3@v1.14.22/sqlite3_go18.go`:18-23；被 `docker-compose.yml`:52-55 当 liveness probe、[灾难恢复](disaster-recovery.md) 第 69 行当恢复判据 | **A-阻塞** | 健康检查真正执行一次读写探测并覆盖 worker/队列；恢复 checklist 的判据随之更新 |
| G-H3 | ~~purge 的不可逆步骤在入队之前执行~~ **已修（本次提交）**：原 `Purge` 把解除盘保护 / 吊销身份 / 删 DEK 放进 `preFn`，在 `Submit` 之前执行；队列满、runner 关闭或 job 落库失败时只回滚状态——数据已不可解密、没有 job、错误文本不提部分执行。现在这三步是 **destroy job 自己的第一步**：`Runner.Submit(..., steps...)` 把步骤随 job 交给单 worker，worker 先落 job 行（`Insert` → 状态 running）再执行步骤，最后才调 provider；入队被拒时盘保护未解除、DEK 未删、没有 destroy 调用，错误文本逐条说明"什么都没销毁"以及**已经发生**的部分（claim 的 `purge_withdraw_identity` 触发器已撤销授权、`purge_requested` 已置位，这一步不能靠重排消除）。步骤中途失败时 job 行列出已完成步骤（即 `AGENTS.md` 第 66 行的部分失败报告） | 修前：`git show origin/main:console/backend/internal/service/qube_service.go` 第 596-615、682-686、715-721 行 / `origin/main:console/backend/internal/orchestrator/runner.go` 第 187-219 行；修后 `internal/service/qube_service.go`:597-604（claim 后把 step 交给 job）、`:643-687`（三个步骤 + 部分失败记账）、`:816-820`（steps 交给 `Submit`）、`:851-860`（入队失败的显式说明）；`internal/orchestrator/runner.go`:71-99（`Step` / `queuedJob`）、`:229-256`（`Submit` 收 steps）、`:316-346`（worker 先步骤后 action）、`:368-380`（步骤失败即停）；回归测试 `internal/service/qube_purge_ordering_test.go` 与 `internal/orchestrator/runner_steps_test.go`；顺序说明同步到 [生命周期可靠性](reliability-design.md) 与 [凭据与密钥销毁](credential-destruction.md) | 已解除 | ✅ 入队被拒的两个真实分支（队列满 / runner 关闭）都有测试断言"零销毁 + 错误文本 + 存储状态一致"；剩余：真机生命周期复验（M0-5）；"拒绝入队后授权撤销与 `purge_requested` 已生效"是 claim 的设计事实，报告里如实声明，未假装可回滚。独立复验（不由实现方自述）：把 `claimPreparation{inJob: …}` 换回 `{beforeQueue: …}`（即修复前顺序）→ `TestPurge_RefusedEnqueueLeavesDataIntact` 的队列满/关闭两个子用例 FAIL；把步骤移到 provider 动作之后 → `TestJobStepsRunAfterTheRowIsRecordedAndBeforeTheAction` 与 `TestJobStepFailureSkipsTheProviderAction` FAIL；恢复后全绿。三条承重事实另行核对：`purge_withdraw_identity` 是 `AFTER UPDATE OF purge_requested` 触发器（同事务改 `agent_certs`/`bootstrap_tokens`）、`RevokeByQube` 的 `WHERE … revoked_at IS NULL`（第二遍匹配 0 行）、`DeleteDataKey` 文档自述幂等 |
| G-H4 | job 日志只写不删、无保留策略，也不在备份/恢复范围内：恢复后 `jobs` 表有历史而日志文件不存在，UI 静默显示空日志 | `internal/orchestrator/joblog.go`:59-103；`internal/backup/backup.go`:96-99（只对数据库 `VACUUM INTO`）；[灾难恢复](disaster-recovery.md) 第 43-45 行备份清单 | A-需要 | 保留/轮转策略 + 纳入备份清单；若明确不备份，UI 需能区分"无日志"与"日志丢失" |
| G-H5 | ~~限流键与审计来源 IP 可被 `X-Forwarded-For` 伪造~~ **已修（`f8e154a`）**：路由是 `gin.New()` 且全仓没有 `SetTrustedProxies`，而 gin v1.9.1 默认可信网段为 `0.0.0.0/0`、`::/0` | 修后 `cmd/server/main.go`:920（`configureTrustedProxies` → `SetTrustedProxies(nil)`）、`:932`（`setupRouter` 里调用）；负向测试 `cmd/server/security_test.go`；[runtime-defaults](runtime-defaults.md) UD-1d | 已解除 | ✅ 反向验证：把修复改成空操作后，新测试在"ClientIP 报的是伪造地址"和"换 XFF 就换到新桶"两条断言上均失败 |
| G-H6 | 实时 job 日志流被 15 秒 `WriteTimeout` 截断：handler 按 5 分钟设计，15 秒后写入必然失败，而 handler 丢弃写错误继续空转到 5 分钟——"干净结束 + 按 offset 重连"的契约不会发生 | `cmd/server/main.go`:1250-1251；`internal/handler/job_handler.go`:164-172、`:229-263`、`:284-290`；[runtime-defaults](runtime-defaults.md) 第 38 行登记的正是该不可达行为 | A-需要 | 流式响应不受整体 WriteTimeout 限制（或把上限改成可达值），并同步文档与前端回退逻辑 |
| G-H7 | 无单实例保护：启动即执行 `reconcileStrandedQubes` / `ReconcileUnfinishedJobs`，会把另一个仍在运行的实例的在途 job 标成 failed/unknown、qube 覆盖成 error；DSN 无排他锁，也无 flock/pidfile | `cmd/server/main.go`:317、`:499`；`internal/service/reconcile.go`:30-58；`internal/database/database.go`:62-64 | A-需要 | 排他锁/pidfile，或把"只跑一个实例"写成部署硬要求并在启动时自检 |
| G-H8 | console 二进制从不携带构建版本：`appVersion` 是编译期常量 `"0.1.0"`，`release.yml` 与 `Makefile` 都不注入；agent 侧反而有注入 | `cmd/server/main.go`:42（`appVersion` 常量）、`:63-70`（`--version` 与启动日志都打印该常量）、`:1153`（`/health` 的 `version` 字段）；[release.yml](../.github/workflows/release.yml) 第 106 行（`-ldflags` 只有 `-s -w`，不注入）；对照 `packaging/agent-deb/Dockerfile`:41-45 | A-需要（与 G-C3 同一件事） | 构建注入版本，`/health` 与 `--version` 反映真实 revision |
| G-H9 | agent 的 systemd 单元 5 次启动失败即永久放弃，且无告警路径；原因消失后不会自愈，需要人工 `systemctl reset-failed` | `packaging/agent-deb/qubes-air-agent.service`:10-11、`:32-33`；启动失败路径 `cmd/qubes-air-agent/main.go`:97-116 | A-需要 | 放弃状态对操作者可见，runbook 写明恢复步骤 |
| G-H10 | 置备规格无上下限校验（`validateQubeSpec` 只拒绝负数），而 PVE 磁盘**不能缩回**——一次笔误永久占用集群存储 | `internal/service/qube_service.go`:516-524；`internal/provider/proxmox/adapter.go`:170、`:232-235`、`:395-399` | A-需要 / B-阻塞 | 上下限校验（或 per-zone 配额），越界在 API 层拒绝 |
| G-H11 | **bootstrap 路径不认证对端，且这是对 `AGENTS.md` 规则的显式豁免**：console 拨号尚在 bootstrap 的 agent 时 `InsecureSkipVerify: true`（`:387`）且没有 `VerifyConnection`，对端只有进程内随机生成、随进程丢弃的自签名占位证书。代码注释说明了原因（"proves nothing and is trusted by nobody；token 才是认证"），token 也确实由 agent 出示并单次消费（console 不发送 token），所以不是"抄近路"；但 `AGENTS.md` 第 65-66 行要求 `InsecureSkipVerify` 必须配完整 `VerifyConnection`，而这里**没有任何可提前 pin 的身份**——豁免已写进 `:368-387` 的注释并在本行登记 | `internal/service/agentbootstrap.go`:368-387、`:266-276`（token 由 agent 出示）；`internal/agent/bootstrap.go`:406-414（占位证书）；`AGENTS.md` 第 65-66 行 | A-需要（豁免已登记，非静默） | 二选一：①把"首次 bootstrap 必须在受信 LAN 内 + token 单次 1 小时 TTL"写成部署硬要求（与 G-D7 同一枚 token）；②彻底修：占位证书密钥改由 token 经 HKDF 派生，console 据此 pin 对端公钥——无 CA 也能做到真正的对端认证。②需要真机验证，不在 M0 范围 |

## 3. TODO List

按里程碑排序（不是按主题）。每条给出验收判据；`依赖` 指必须先完成的前置。

### M0 — 让当前这版可被信任（几乎无代码，前置一切）

- [x] **M0-1** 把 `kixpower/sprint-1`（含本地 `main` 的 20 个 commit）push 到 `origin`，触发全部 workflow —— 已完成：首轮 21 个 check，18 通过 / 3 失败（G-A1）
- [x] **M0-2** 修掉 CI 暴露的问题（如有），每条失败都按真实原因修，不使用 `|| true`/`continue-on-error` —— 已完成：gitleaks 按值放行 `fa2b8a4`、deb 版本排序 `3afbcd7`、gosec 29 条 `a118c3c` + 门禁等价 `5f1bf72`；第二轮 CI 复验（G-A1）
- [x] **M0-3** 合并 `kixpower/sprint-1` → `main`，合并后重跑 `make audit` —— 已完成：`5f0fd88`（merge commit，保留 QA 记录的 revision），`main` 上 `make audit` rc=0（G-A2）
- [x] **M0-4** 清理过时分支 `fix/security-audit`、`feat/mcp-server` —— 已完成：PR #7 关闭并说明被 `bodylimit` 取代，两个远端分支删除（本地保留）（G-A3）
- [ ] **M0-5** 在 `main` 新 HEAD 上重跑一次真机生命周期冒烟（provision→suspend→resume→purge），刷新 revision 绑定 —— 依赖：M0-3（G-B6）
- [ ] **M0-6** 提交 `qubes-salt-config` 的 QA-01 期间改动并打 tag —— 依赖：无（G-G4）

### M1 — A 档硬阻塞（自用生产的最小闭环）

- [x] **M1-1** console 侧下发 Exec/FileCopy 白名单：新增 `agent_exec_allow` / `agent_filecopy_roots`（env `QUBES_AIR_EXEC_ALLOW` / `QUBES_AIR_FILECOPY_ROOTS`，冒号分隔，默认空=服务在 guest 内禁用），写入 cloud-init `agent.env`（空则整键省略）；路径规则（绝对、规范化、无冒号/控制字符、FileCopy 拒绝 `/`）在启动配置校验与渲染时**各校验一次**，两侧测试的变异验证分别有 6/7 个子用例失败 —— 真机正值/负值记录属 M1-2 —— 依赖：M0（G-B1）
- [ ] **M1-2** 真机补跑 Exec 正值/负值、FileCopy push/pull 往返 —— 依赖：M1-1（G-B2）
- [ ] **M1-3** 真机补跑 suspend/resume 数据持久性（写文件→suspend→resume→读回）—— 依赖：M1-2（G-B3）
- [ ] **M1-4** 离机恢复演练：归档经网络/介质到另一台机器，真实 keyring，记录实测 RTO 与人工步骤 —— 依赖：M0-6（G-C1）
- [ ] **M1-5** 备份调度与保留策略落地（timer/cron + 文档化）—— 依赖：无（G-C2）
- [x] **M1-6** 升级/回滚 runbook 成文：`docs/upgrade-rollback.md` 给出三个制品的 Salt 钉法、console↔relay/agent 协议兼容矩阵（版本集合而非相等判断，零 flag day；`BuildVersion` 只做观测）、schema 前向单向导致"回滚二进制≠回滚数据"、升级顺序（先备份）、两种回滚路径、失败模式速查；并写明今天**只能**用二进制 sha256 认构建（`/health.version` 是编译期常量 `0.1.0`，G-H8/M2-10） —— 依赖：无（G-C3、G-G2）
- [x] **M1-7** 生产部署安全要求成文：`docs/deployment-requirements.md` 逐条给出"默认不满足、代码不兜底"的硬要求、后果与可核对命令（含 G-D7 的 share 导出约束与 G-H11 的 bootstrap 窗口），并从 `docs/README.md` 与根 `README.md` 的安全提示接入（G-D2、G-D3、G-D5、G-D7）
- [ ] **M1-8** 带外核对节点 SSH 指纹与 PVE 集群版本，替换 TOFU 结果 —— 依赖：无（G-B5）
- [ ] **M1-9** 有旧盘时补 DEK 迁移真机验收 —— 依赖：真机环境（G-B4）
- [ ] **M1-10** 首次跑通 release：打 `v*` tag，产出 console/web/agent 制品 + `SHA256SUMS`，并用 release URL 完成一次 provision —— 依赖：M0（G-A4、G-C4）
- [x] **M1-11** 修 job 超时：`Timeout` 可配置、默认 45 分钟覆盖真机 provision 长尾，并登记进 `runtime-defaults.md` UD-1e —— 已完成（G-H1）；**真机复现仍待 M0-5**
- [x] **M1-12** purge 不可逆步骤与入队解耦：不可逆步骤成为 destroy job 的第一步（`Runner.Submit` 接受 `Step`，worker 在 job 行落库之后、provider 调用之前执行）；入队被拒时零销毁，错误文本同时说明"什么都没销毁"与 claim 已记录的部分；步骤中途失败时 job 行列出已完成步骤。测试：`internal/service/qube_purge_ordering_test.go`（拒绝入队 ×2 / 部分失败 / 幂等重试）、`internal/orchestrator/runner_steps_test.go`（顺序 / 失败跳过 action / 超时） —— 依赖：无（G-H3）；**已提交并独立复验**（两处变异红见 G-H3 行；真机生命周期属 M0-5）
- [x] **M1-13** 修 `X-Forwarded-For` 可伪造：显式不信任任何代理（`SetTrustedProxies(nil)`），负向测试证明伪造 XFF 既不改 `ClientIP` 也换不到新限流桶 —— 已完成（G-H5）
- [x] **M1-14** 让 `/health` 有真实语义：真实读写探测（建表/写 marker/读回 + `PRAGMA database_list` 与 `os.Stat` 识破"库文件已删仍可写"）、覆盖 job 调度器心跳（空闲 3 次丢拍 = 15s 判死；**正在执行 job 时预算 = 该 job 超时 + 15s**，避免长 provision 被误判而遭 compose 重启）、队列/运行数只做信息不做判据、未认证路由的写按 2s 窗口节流、恢复判据文档同步 —— 依赖：无（G-H2）
- [x] **M1-15** 修 job 日志流的 WriteTimeout 矛盾：流自己管每次事件的写截止时间（`streamWriteWindow` 30s，每事件重置），写失败即结束流而不是空转到 5 分钟；`runtime-defaults` 登记 UD-6b，前端回退逻辑核对后无需改动（G-H6）

### M2 — A 档收尾与可维护性

- [ ] **M2-1** UI 侧 zone 可见性降级 —— G-E7
- [ ] **M2-2** 审计落地（文件或 DB）+ 轮转 + 留存策略 —— G-D3
- [ ] **M2-3** 覆盖率阈值门禁 + `cmd/*` 入口包冒烟测试 —— G-F1
- [ ] **M2-4** 拆分 `Tunnel` / `loadFromEnv` / `Validate`，为 24 处 `nolint` 补退出条件 —— G-F2、G-F3
- [ ] **M2-5** 前端补测试与拆分 `QubeList.svelte`；引入 E2E 覆盖主路径 —— G-F4、G-E4
- [ ] **M2-6** 清理文档遗留（R-DOC-4、O-2、O-5），统一 Node 版本与本地工具链 —— G-F6、G-F5
- [ ] **M2-7** `go-licenses check` 转 blocking 的决策 —— G-F7
- [ ] **M2-8** job 日志保留/轮转策略，并决定是否纳入备份（含 UI 区分"无日志"与"日志丢失"）—— G-H4
- [ ] **M2-9** 单实例保护（排他锁/pidfile）或把"只跑一个实例"变成启动自检 —— G-H7
- [ ] **M2-10** console 构建注入版本，`/health` 与 `--version` 反映真实 revision —— G-H8
- [ ] **M2-11** agent 启动放弃状态对操作者可见（健康视图/告警）+ runbook 恢复步骤 —— G-H9
- [ ] **M2-12** qube 规格上下限校验（或 per-zone 配额），越界在 API 层拒绝 —— G-H10

### M3 — B 档（对外发布）

- [ ] **M3-1** 用户模型 + 2FA + 权限分层 —— G-D1
- [ ] **M3-2** UI-01 设置四项真正生效 —— G-E1
- [ ] **M3-3** OBS-01 监控/告警/账单接真实数据源 —— G-E2
- [ ] **M3-4** GUI-01 无缝桌面闭环（含 Xpra 权限边界）—— G-E3
- [ ] **M3-5** MCP-01 桌面帧与输入；MCP-02 HTTP transport 决策 —— G-E5
- [ ] **M3-6** CLOUD-01/02 GCP / AWS 适配器与独立销毁验收 —— G-E6
- [ ] **M3-7** 发布治理：LICENSE、SECURITY.md、CHANGELOG、发布检查单、artifact store 签名与发布审计 —— G-G1、G-G3、G-C5
- [ ] **M3-8** 外部安全审计 —— G-D4

## 4. 与既有入口的映射

本文不替代[任务清单](TODO.md)——那是唯一维护入口。映射关系：

| 本文 ID | 既有 ID | 说明 |
|---|---|---|
| G-B1..G-B4 | QA-01 剩余项 | 本文把 G-B1（下发能力）从 QA-01 里拆出来单列，因为它是**代码缺口**而非环境缺口 |
| G-C1 | OPS-01 | 同一条，本文补了"备份无调度"与"升级无契约"两条相邻缺口 |
| G-C2/C3 | 新增 | 原清单没有独立的备份调度与升级回滚条目 |
| G-A1..A4、G-G1..G4 | PUB-01 + 本文新增 | PUB-01 只覆盖发布材料，交付链状态此前散落在 sprint 文档里 |
| G-E1..E6 | UI-01 / OBS-01 / GUI-01 / QA-02 / MCP-01/02 / CLOUD-01/02 | 一一对应 |
| G-F1..F7 | PROJECT_BRIEF §9 风险登记 | 技术债未进 TODO.md，本文给它们编号以便排期 |
| G-H1..H10 | 无对应条目 | 来自独立补盲审计，**尚未进任何既有清单**；建议优先把 G-H1/H3/H5 收进 Sprint 2 |

## 5. 未核实与不确定

- **CI 真实结果未知**：本地 20 个 commit 从未 push，无法预判 7 个 workflow 是否全绿（G-A1 的执行结果本身就是证据）。
- **生产数据量下的 RTO 未知**：预演库约 115 KB，不能外推到真实规模。
- **未知漏洞面**：未做外部安全审计，现有安全结论只覆盖已检查的边界。
- **PVE 集群版本与节点指纹**未从带外渠道核对。
- **§2.H 的证据等级**：那 10 条来自一次与本清单作者独立的补盲审计，我逐条回读了本仓代码以及两个依赖的源码
  （`go-sqlite3@v1.14.22`、`gin@v1.9.1`）确认。但除库行为本身外，**均未在真机复现**——
  尤其 G-H1 是否真的触发取决于真实 provision 时长（代码自述 15-25 分钟，与 15 分钟超时重叠，属"有时会中"）。
  建议先按 M1-11 复现再修，不要把"应该会超时"当成已证实的故障。
- **G-B2/G-B3 的工作量**取决于 M1-1 的下发设计（配置放在 Zone 还是 Qube 层），尚未定；本文按"先在 Zone 层给默认值 + Qube 层可覆盖"估算为 M 级。
- 本文的 Sprint 数估算（A 档 2 个、B 档再 3+）是**排序用的粗估**，不是承诺；实际取决于真机环境可用性与 M0 暴露的 CI 问题数量。
- **三处只会出现在本地全盘扫描里的 gitleaks 假阳性**（`--no-git` 扫描整个工作树时命中，PR 范围的 commit 扫描不会命中，
  所以不挡 CI）：`internal/keyring/keyring_test.go`:12、`internal/repository/credential_repository_test.go`:15 是占位 key 字面量；
  `internal/pki/ca_test.go`:72 命中的是 PEM 头字面量 `-----BEGIN EC PRIVATE KEY-----`，而那段测试恰恰在断言 CA 私钥**不得**
  出现在 bundle 里。**故意不把它们加进 `.gitleaks.toml` 放行清单**：按值放行 PEM 头会把真正的 EC 私钥一并放过，
  按路径放行又会放过该文件里将来真被粘贴进来的密钥——记录在此，等它真的挡住某次 PR 时用 `regexTarget = "line"`
  精确到那一行代码再放行。
- **CodeQL 的行级归属副作用（已处置）**：给 4 处既有 `InsecureSkipVerify` 加抑制注释后，CodeQL 把这 4 条**既有**告警重新算作
  "本 PR 新增"（`Disabled TLS certificate check`），check run 因此失败。这不是新缺陷：3 处已有完整 `VerifyConnection`，
  按 false positive dismiss；bootstrap 那处按 won't fix dismiss 并引用 G-H11。**未采用**排除查询的做法——那会让整仓永久
  不再检查这一类问题，而 dismiss 只针对这 4 条、查询仍然生效。
- **工具版本差异是这一类问题的共同根因**：G-F8 是 gosec（内嵌 vs 独立），G-F9 是 gitleaks（8.30.1 vs action 捆绑的 8.24.3）。
- **gosec 这批抑制经过一次独立审查（另一上下文，同厂商）**，结论与它自己读到的代码都留在会话记录里：
  它**确认**了 18 条 `#nosec` 的理由都对该行成立、29 条发现与改动一一对应、bootstrap 那处是**披露**而非隐藏，
  并读了 gosec v2.29.0 的指令解析源码（`analyzer.go:1003-1025` 认规则 ID、`:927-943` 注册注释行范围）验证抑制机制。
  它**发现并已修**的问题：①`cmd/relay-call/main.go`:121 还有一处同类日志注入（`service` 未校验就进日志，
  gosec 的污点分析穿不过 `parseRelayTarget` 所以没报）；②`internal/service/agentprobe.go` 与 `agentprobe_test.go`
  的注释写成"agent 证书只带 ClientAuth"，而 `ekuForRole(RoleAgent)` 实际给的是 ServerAuth——与四行之下的抑制理由自相矛盾；
  ③本文件引用 `AGENTS.md` 行号写成 60-63，实际规则在 65-66；④`make pre-commit` 只跑内嵌 gosec（见 G-F10 与下面的门禁说明）。
  **局限**：该审查的全仓 gosec 复跑两次超时，所以"29 → 0"里的**全局 0** 依据是我的 v2.29.0 运行 + 它的逐条对应，
  不是它自己独立跑出来的；跨厂商那一路观察在产出前就失败了，因此这批安全判断**没有厂商独立的证据通道**。
  两次都出现"本地绿、CI 红"，且第二次本地结论是**空证据**。结论：凡扫描器配置改动，必须用 CI 侧的版本复验，不能只用本机 CLI。

## 6. 复现本文结论

```bash
# 交付链状态
git rev-list --left-right --count origin/main...main      # 0  20
git log --oneline origin/main..main | wc -l               # 20
git tag -l                                                # 空
git ls-files | grep -iE '^(LICENSE|SECURITY|CHANGELOG)'   # 空

# cloud-init 下发的 agent 环境键（无 EXEC_ALLOW / FILECOPY_ROOTS）
sed -n '268,280p' console/backend/internal/service/cloudinit.go

# Exec 空 allowlist 的行为
sed -n '58,66p' remote/qubes-rpc/qubesair.Exec

# Sprint 1 未改生产代码（真机证据是否仍绑定当前 HEAD）
git diff --name-only fae0aea 4f52953 | grep -E '\.go$|\.svelte$'   # 只应出现 _test.go

# §2.H：job 超时默认值 vs 代码自述的真机时长
grep -n "DefaultJobTimeout" console/backend/internal/orchestrator/runner.go   # 45m
grep -rn "15-25 minutes" console/backend                                      # provision 的真机时长
sed -n '525,536p' console/backend/cmd/server/main.go                          # RunnerConfig 现在显式传 Timeout

# §2.H：/health 的真实语义（依赖源码：Ping 不发 SQL）
sed -n '84,87p' console/backend/internal/database/database.go
sed -n '18,25p' "$(go env GOMODCACHE)/github.com/mattn/go-sqlite3@v1.14.22/sqlite3_go18.go"

# §2.H：purge 的不可逆步骤现在是 job 自己的第一步（入队被拒时不会执行）
sed -n '597,604p;643,660p' console/backend/internal/service/qube_service.go   # claim → 把 step 交给 job；三个步骤
sed -n '816,823p' console/backend/internal/service/qube_service.go            # steps 交给 Submit（在 job 行之后）
sed -n '71,98p;316,346p' console/backend/internal/orchestrator/runner.go      # Step 类型；worker 先步骤后 action
cd console/backend && go test -race -run 'TestPurge|TestJobStep' ./internal/service/ ./internal/orchestrator/

# §2.H：X-Forwarded-For 可伪造（gin 默认可信网段为 0.0.0.0/0）
grep -n "gin.New()" console/backend/cmd/server/main.go
grep -n "ClientIP()" console/backend/internal/middleware/ratelimit.go console/backend/internal/middleware/audit.go
sed -n '33,42p' "$(go env GOMODCACHE)/github.com/gin-gonic/gin@v1.9.1/gin.go"

# §2.H：过时分支的真实 SHA 与 tree
git rev-parse --short origin/main feat/mcp-server fix/security-audit
git rev-parse 'origin/main^{tree}' 'feat/mcp-server^{tree}'   # 同值 → 内容相同、commit 不同
```
