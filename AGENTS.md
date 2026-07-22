# Qubes Air 开发规则

本文件适用于整个仓库。它约束人工开发和自动化 agent；子目录如果以后增加更具体的
`AGENTS.md`，只补充该目录规则，不得放宽这里的安全与质量门禁。

## 1. 当前项目边界

- 这是持续开发中的项目，只维护当前架构，不为未声明的旧环境保留兼容分支、脚本或文档。
- Proxmox 是目前唯一完成真机闭环的 provider。GCP/AWS 未通过同等验收前，不得描述为可用或等价。
- Qubes 侧 Salt states 的权威来源是 `qubes-salt-config`；本仓库不复制一套平行部署入口。
- 架构、状态和安全结论必须以当前代码及可复现验证为准，不能把计划、placeholder 或 TODO 写成已实现。

## 2. 阶段开发流程

每个可独立验收的阶段遵循以下顺序：

1. 开始前查看 `git status`、相关实现、测试和文档，保留用户已有修改。
2. 写清本阶段的行为变化、信任边界、失败方式和验收条件；不要顺手扩大范围。
3. 实现代码，同时补测试、错误处理、日志和必要文档。安全控制必须有失败路径测试。
4. 完成后先自查 diff，再运行 `make pre-commit`。任何失败都必须修复或明确记录为本阶段 blocker，
   不得用 `--no-fail`、`|| true`、`continue-on-error` 或扩大 `nolint` 来伪造通过。
5. 只有门禁通过、文档与实现一致、没有 secret 或生成物混入时，才可以准备 commit。
6. 一个 commit 只表达一个完整意图；不要夹带格式化全仓、无关重构或用户未要求的文件。

`make pre-commit` 以 `HEAD` 为增量基线，确保当前阶段不新增质量债务。需要指定其他基线时使用：

```bash
make BASE_REV=origin/main TF_BIN=tofu pre-commit
```

里程碑、release、合并大范围安全/transport/PKI 改动前还必须运行 `make audit`。完整审计会检查
全部存量代码，不能用增量模式掩盖历史问题。

## 3. 提交前强制门禁

| 范围 | 命令/规则 |
|---|---|
| Diff | `git diff --check <base>` 必须通过；不得提交冲突标记、尾随空格或意外生成物 |
| Go 测试 | `go test -race -coverprofile=coverage.out ./...`；新增行为必须有成功、失败和边界测试 |
| Go lint | `golangci-lint`，配置以根目录 `.golangci.yml` 为准 |
| 安全扫描 | 显式运行 `gosec` linter；不得用 `-no-fail`；涉及依赖时运行 `govulncheck` |
| 复杂度 | `gocyclo` 最大 15；函数最大 100 行、50 条语句，由 `gocyclo`/`funlen` 强制 |
| Go 格式 | `gofmt`、`goimports` 由 `golangci-lint` formatter 检查 |
| 前端 | `npm ci && npm run check && npm run build`；必须 0 error、0 warning；依赖变化运行 `npm audit --audit-level=high` |
| Shell | 所有本阶段新增或修改的 shell/shebang 文件必须通过 ShellCheck |
| Terraform | OpenTofu `init -backend=false`、`validate`、`fmt -check -recursive` |
| 文档 | 本地 Markdown 链接必须存在；架构/流程图使用 Mermaid；命令和路径必须可验证 |

本地只有 Terraform 时，可用 `make TF_BIN=terraform pre-commit` 做 HCL 校验；涉及 state 加密、
backend 或 release 验收时必须使用 OpenTofu，不能用 Terraform 的成功替代。

## 4. Lint 和复杂度例外

- `//nolint` 只能贴在最小代码范围，并写清具体 linter 与技术原因，例如
  `//nolint:gocyclo // protocol frame dispatch is intentionally kept in one state machine`。
- “暂时先过 CI”“以后再重构”不是有效理由。能够拆分、改名或补 context 的，应直接修复。
- 新函数超过复杂度或长度阈值时优先拆成有业务名称的步骤，不能通过调高全局阈值解决。
- 测试可以使用 `.golangci.yml` 已定义的窄例外；生产代码不能复制测试例外。
- 修改 `.golangci.yml`、扫描排除项或质量阈值，必须在同一 commit 说明原因和风险影响。

## 5. 安全开发规则

- 不得把 provider credential、API token、CA/private key、LUKS key、bootstrap token、state
  passphrase 或真实基础设施地址提交到仓库、日志、测试 fixture 或前端 bundle。
- 所有外部输入在进入文件路径、命令参数、Terraform target、qrexec service、网络 endpoint 前使用
  allowlist 校验；禁止依赖 shell escaping 作为唯一保护。
- 禁止新增 `sh -c`、`bash -c`、任意绝对路径写入或 root helper，除非接口被明确收窄并有授权、审计
  与攻击面测试。
- TLS 必须同时验证 CA、证书用途、调用方角色和目标身份。使用 `InsecureSkipVerify` 时必须配置完整的
  `VerifyConnection`，并有错误 CA、错误角色、错误 target 和过期证书测试。
- cloud-init 和 Terraform state 不得包含私钥；一次性 token 也按 secret 处理。
- 删除、purge、密钥轮换、CA 操作和基础设施 destroy 必须显式确认目标、支持幂等，并报告部分失败。
- API 新端点必须定义认证、授权、请求体上限、超时、审计字段和敏感响应处理。

## 6. 测试要求

- Bug 修复先补能复现问题的测试，再修实现。
- 并发、队列、续期和状态转换必须覆盖 race、取消、超时、重启以及重复请求。
- PKI/mTLS 必须覆盖正反例，不能只有“正确证书连接成功”。
- provider 代码至少通过 fmt/validate；宣称可用前还要有真实 provider smoke test 和销毁验证。
- shell/qrexec 服务要覆盖输入为空、非法 service/path/argument、超量输入输出和非零退出。
- 前端交互变化至少通过 Svelte check/build；关键流程应补组件或 E2E 测试，而不是只依赖手工点击。

## 7. 文档与注释

- README 只保留定位、当前状态、最短开发入口、真机入口和文档索引；细节放到对应专题文档。
- 代码实现后，同一阶段删除对应 TODO/placeholder/“骨架”描述；未实现能力在 UI 和文档中明确标记。
- 不保留仅用于解释已移除环境的长篇历史。仍在仓库中的脚本必须符合当前架构，否则删除或拒绝执行。
- 架构图统一使用 Markdown Mermaid fenced block，不提交 ASCII 流程图截图。
- 修改文件名或删除文档后运行链接检查，并更新所有入口引用。

## 8. Commit 检查清单

提交前确认：

- [ ] 本阶段验收条件已经满足；
- [ ] `make pre-commit` 成功；
- [ ] 没有新增 lint、复杂度、安全扫描或前端 warning；
- [ ] 新行为有测试，安全边界有负向测试；
- [ ] README、专题文档、UI 文案与代码一致；
- [ ] diff 中没有 secret、真实环境配置、构建产物或无关修改；
- [ ] commit message 描述行为结果，而不是“update/fix stuff”。
