# 2026-10-18 设置读取与原子保存最小移植

目标基线：`origin/main` `15168ab`（2026-10-18）。本记录只覆盖从旧线工作区移植到
`origin/main` 的最小部分；旧线的加密 webhook 目标与 handler 串行化不在本次范围。

## 行为与边界

- `SettingsRepository.Get` 在一条查询中读取 general/notifications/security，结果来自同一
  SQLite 快照。未配置的组保留默认值；取消、超时、数据库错误和损坏 JSON 返回错误，不再把
  失败当成默认设置。
- `SettingsRepository.Update` 在单个事务中写入三组设置；任一组失败时全部回滚。
- 上游 `SettingsHandler` 已有 `updateMu` 和“保存成功后才应用 session TTL”的顺序控制。
  本次不重复修改 handler 逻辑，只补失败路径测试。
- 未移植：加密 webhook 目标、`UpdateWithWebhook`、凭据仓库 writer、服务层 webhook 迁移。
  这些依赖尚未进入 `origin/main` 的 `19e2ead`/`edb7f49`/`5e7fcb6` 特性与三张告警表。
- GATE-01 的 `main.go` 抽取和 `runSSH` 删除不适用：上游已删除 `runSSH`，且
  `initDependencies` 在当前 funlen 配置下通过（`15168ab` 的 Lint CI 为 success）。

## 回归与检查

新增/修改测试：

- `internal/repository/settings_repository_test.go`
  - 读取失败：取消、过期 deadline、数据库关闭、损坏 JSON；
  - 未配置组使用默认值；
  - 分别注入 general/notifications/security 写入失败，验证三组全部回滚；
  - 更新前取消，验证设置保持原值。
- `internal/handler/settings_handler_test.go`
  - `TestSettingsFailedSaveLeavesStoredSettingsAndSessionsUntouched`：security 写入失败时，
    已签发 session、TTL 和新 session 生命周期均不变，前两组也未提交。

| 检查 | 结果 |
|---|---|
| 定向 `go test -race`（repository/handler/service/server） | 通过 |
| `make pre-commit` | exit 0；Go 覆盖率 74.3%；lint/gosec/complexity 0 issues；Svelte 0 errors / 0 warnings；255 个前端测试；npm audit 0；ShellCheck 无变更脚本；yamllint 13 文件；docs-check 51 项 |
| `make audit` | exit 0；全量 lint/gosec 0 issues；ShellCheck 32 文件；govulncheck 0 可达漏洞 |
| 环境 | 本机默认 Node 22.14.0 缺少 `zlib.createZstdDecompress`，undici 8.12.1 启动 vitest worker 失败；门禁使用 mise 的 Node 22.19.0 |

## 限制

- 仅本机自动化验收，无真机证据。
- 未覆盖 webhook 凭据与设置的跨仓库事务；该能力仍属旧线特性，移植前不能宣称已在上游实现。
- 旧线 `docs/TODO.md`/`readme.md` 的能力描述未套用到上游；需要按上游当前状态另行更新。
