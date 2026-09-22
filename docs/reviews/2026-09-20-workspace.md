# 工作区检查记录

检查日期：2026-09-20。本记录是当日检查快照，不作为当前进度或部署入口。当前状态见[路线图](../roadmap-to-production.md)，后续工作见 [TODO](../TODO.md)。

## 范围与提交状态

本次整理只改文档、个人笔记中的失效引用和 Makefile 帮助文字；不改变运行时行为、认证或
基础设施，也不执行资源销毁。保留整理前已有的所有实现改动，未暂存或提交。

整理开始时，分支为 `main`，HEAD 为 `3b573c0`，与本地 `origin/main` 引用无差异；未 fetch。
已有 115 个已跟踪文件修改、34 个删除和 59 个未跟踪文件。这些是整理前快照，不是实时统计。
个人工作区文件 `IDENTITY.md`、`SOUL.md`、`USER.md`、`DREAMS.md` 和 `memory/` 仍留在本地；
提交实现前需单独检查其是否属于项目交付范围，不应随大批改动一起加入。

## 本阶段验收

- README、路线图、provider、架构、传输、bootstrap、销毁与恢复文档以当前代码为准。
- 明确区分代码落地、历史真机记录和未完成验收；去掉“P0/P1/P2 全部完成”的宽泛结论。
- 移除 provider 文档里的现场地址、资源编号、过期构建 pin 与已删除架构的部署步骤。
- 个人笔记失效链接保留为带来源缺失说明的文字；没有放宽文档扫描规则。
- 不新增 Go、前端或 shell 运行时行为；不为纯文字修改增加实现镜像测试。

## 检查结果

| 检查 | 结果 |
|---|---|
| `git diff --check HEAD` | 通过 |
| `make frontend-check` | 通过：npm ci、Svelte 0 error / 0 warning、生产构建、5/5 vitest 测试 |
| `make frontend-audit` | 通过：0 vulnerabilities |
| `make pre-commit` | **阻塞**：在 check-tools 阶段提示缺少 go；未执行后续完整门禁 |
| `make docs-check` | 通过：Markdown 本地链接与 CI workflow 门禁检查 |
| 容器 Go race 测试 | 首次全量测试中 `internal/service` 失败，其余有测试的包通过；补齐打包文件后，service 包单独 race 重跑通过 |
| `make audit` | 未执行；本次不是发布或合并 |
| 真机 smoke / 离机恢复 | 本次未执行；历史记录与下一轮条件见路线图 |

首次容器只挂载 `console/backend`，但 service 测试需要仓库中的
`packaging/agent-deb/qubes-air-agent.service`。补入原始打包文件后执行
`go test -mod=readonly -race -count=1 ./internal/service` 通过，没有修改测试或实现。
首次全量使用 `go test -mod=readonly -race -coverprofile=/tmp/qubes-air-coverage.out ./...`，
镜像为本地 `golang:1.26`（linux/amd64）；本轮未在完整挂载下重跑整个套件，因此不记录为
“一次完整全量通过”。后续容器门禁应挂载整个仓库并在 `console/backend` 下运行。

完整门禁尚未通过，不能准备提交。独立检查通过不能替代缺失的 lint、gosec、复杂度、
govulncheck 和 ShellCheck。需在具备仓库要求工具链的环境中重新运行 `make pre-commit`，
并在大范围安全/transport/PKI 合并前执行 `make audit`。

## 后续整理顺序

以下是审阅分组，不是已经完成的 commit；拆分后每一组仍需独立构建并通过门禁，不能遗漏
未跟踪的新文件或把依赖它们的改动提前提交。

1. 原生 provider、资源仓储、执行器及旧 Terraform/Ansible 入口清理。
2. PKI/agent 服务边界、结构化传输结果及对应安全测试。
3. purge、数据密钥、重启对账与失败路径测试。
4. Console session/API 边界、审计、前端交互与测试。
5. 备份恢复、工具链/CI，以及与各组实现一致的专题文档。

当前优先风险是远端 agent 角色/吊销检查未接入、provider SSH/TLS 校验、销毁部分失败与密钥备份、崩溃一致性；见
[路线图](../roadmap-to-production.md)。这些实现问题不在本次文字整理中静默修改。

## 同日后续：TODO 与历史文档整理

本轮仍只改 Markdown，没有修改安全实现、操作基础设施或创建提交。

- 新增 `docs/TODO.md`，以编号维护优先级、依赖与验收条件；路线图保留当前能力与边界。
- 本记录从 `docs/workspace-review.md` 移到 reviews，另汇总既有真机记录并注明证据限制；
  更新全部入口引用，不保留旧部署步骤作为历史安装入口。
- 修正 agent 包说明中 cloud-init 下发私钥的过时描述，改为 guest 内生成密钥与 CSR。
- 将 MCP 的阶段叙事改为当前接口和未实现边界；去掉重复 TODO。
- 核对 Exec 脚本发现首词 allowlist 后仍执行完整 shell，新增 SEC-02；修正示例为单一绝对
  路径命令并明确启用条件，不宣称 allowlist 已充分限制执行范围。
- 核对本轮 diff；`git diff --check HEAD` 与 `make docs-check` 通过。
- 再次执行 `make pre-commit`，仍在 check-tools 因缺少 go 失败。本轮没有重跑前端或 Go
  测试，上文结果仅属于前一轮，不能当作本轮完整门禁通过。

本轮验收目标是可追踪 TODO、当前文档与历史证据分离、入口链接有效。门禁环境 blocker
继续保留在 ENG-01；不降低检查规则，也不提交现有大批改动。
