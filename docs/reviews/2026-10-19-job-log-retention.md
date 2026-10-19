# 2026-10-19 Job 日志保留与恢复语义（M2-8 / G-H4）

基线：`origin/main` `0e473ea`（PR #67 已合并）。本阶段关闭 G-H4：job 日志从"只写不删、
恢复后静默为空"变为有保留期、有清理留痕、API/UI 能区分五种日志状态。

## 行为变化

- 数据库 schema 6 为 `jobs` 增加 `log_created_at` / `log_pruned_at`（可空，升级时
  `addColumnIfMissing`）。旧库升级后两条均为 NULL，表示"没有日志历史"，不是"日志丢失"。
- runner 打开日志文件后立即写入 `log_created_at`（首次写入胜出）。这次写入失败只记日志，
  不影响 job 执行：失去可观测性不应连带失去基础设施操作。
- 新增启动保留任务：启动时先跑一次，之后每 24 小时清理 mtime 早于 30 天前的
  `<数据库目录>/job-logs/*.log`。只处理合法 job id 的普通文件；目录、非 `.log` 文件和不合法
  文件名一律跳过。每个删除先在文件系统成功，再把时间写入 `jobs.log_pruned_at`；写失败会记录
  但不会把已删文件恢复。
- `GET /api/v1/jobs/:id/log` 新增 `log_state`：
  - `available`：文件存在；
  - `none`：没有文件，也没有创建/清理记录（运行中的 job 属正常，不附 note）；
  - `expired`：文件不存在且 `log_pruned_at` 有值；
  - `lost`：文件不存在、从未标记清理，但 `log_created_at` 有值（典型是换机恢复或日志目录丢失）；
  - `disabled`：控制台未启用 job 日志。
  `expired` / `lost` / `none`（已结束 job）附 `note` 说明原因。
- 前端 `JobLog` 渲染服务端 note（旧控制台无 note 时用本地兜底文案），失败 job 的
  `job.error` 仍显示在 note 下方。日志文件**不进入数据库备份**；恢复流程文档明确此边界。

## 信任边界与失败路径

- job id 在到达文件系统前仍由 `JobLogStore.path` 的 `..` / `filepath.IsLocal` / 正则三重
  校验；`Prune` 额外只接受 `^[A-Za-z0-9_-]{1,64}$` 命名的普通 `*.log`，不会删除目录或
  任意文件。
- 清理任务接受 context；被取消时返回已删除的 id 和 `context.Canceled`，调用方仍能为已删除
  文件留痕。`Dependencies.Close` 在关闭数据库前取消并等待该任务。
- 内存 `MemoryJobStore.Update` 改为保留日志时间戳，与 SQL `UPDATE` 的列集合一致。测试先暴露
  了替换整条记录导致 `log_created_at` 被 runner 完成写回抹掉的问题。
- 新测试覆盖：`JobLogStore.Exists` / `Prune`（旧文件、新文件、目录、非法名、取消）、
  repository 时间戳往返与未知 job、handler 五种状态与 note、schema 升级列一致性、runner
  创建留痕与留痕失败不改变 job 结果、wiring 实际清理并写回 `log_pruned_at`、前端五种状态。

## 未覆盖

- 没有执行真实"换机恢复 + 日志目录缺失"演练；`lost` 路径由 handler 单测和本机 wiring 测试
  证明，未在真机恢复后观察 UI。
- 保留期和清理间隔是编译期常量，没有配置键；需要不同保留期时再引入配置并补边界测试。
- 真机长 job 的日志流/清理交叉场景未观察（G-H6 仍保留该现场验证缺口）。

## 门禁

| 检查 | 结果 |
|---|---|
| `go test -race ./...` | 通过 |
| `make pre-commit` | exit 0；Go 覆盖率 74.6%；lint/gosec/complexity 0 issues；Svelte 0 errors / 0 warnings；261 个前端测试；npm audit 0；ShellCheck 无变更脚本；yamllint 13 文件；docs-check 与 workflow gate 通过 |
| `make audit` | exit 0；全量 lint/gosec 0 issues；Go 覆盖率 74.6%（下限 61%）；govulncheck 通过 |
| 环境 | Node 22.19.0；Go 1.26.5 |
