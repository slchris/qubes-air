# 2026-10-19 告警持久化与加密 Webhook

基线：`origin/main` `7190f1b`（已含 PR #59 的设置读取/原子保存）。本记录覆盖从旧线移植的
告警持久化、告警确认与历史，以及加密 webhook 目标和 outbox 投递。

## 行为与边界

- SQLite 触发器把 Zone disconnected 与 agent unreachable 的 raised/resolved 转变写入
  `monitoring_alert_events`；资源删除时也记录 resolved，历史不随资源删除消失。
- `GET /monitoring/alerts` 从持久化的 Zone/Qube 健康状态生成活跃告警；
  `GET /monitoring/alerts/history` 分页读取转变历史；`POST /monitoring/alerts/:id/acknowledge`
  只接受当前活跃告警，并把操作者与时间写入 `alert_acknowledgements`。告警解除后下一次读取清除确认。
- Webhook 目标以 AES-256-GCM 加密，写成控制台自有 credential 行
  `qubes-air-alert-webhook`（ID `qubes-air-alert-webhook-target`，类型 `pki`），凭证 API 不可见。
  设置响应只返回 `webhookConfigured`，不返回 URL。
- 设置与加密目标在同一事务提交。启用、替换、禁用或写入失败时，设置、凭据和 session TTL
  一起保留原值；保存成功后才更新运行时 TTL。
- 只有 HTTPS、无 userinfo/fragment、且精确 `host[:port]` 出现在
  `notifications.webhook_allowed_hosts` 的地址才被接受；重定向被拒绝。dispatcher 自己解析
  主机名，要求每个地址都是 public unicast，并只拨刚校验过的 IP，拒绝 loopback、private、
  link-local、multicast 与 special-use；配置校验也拒绝这些地址的 IP 字面量。
- 启用 webhook 时，SQLite 触发器把新告警事件写入 `monitoring_alert_webhook_outbox`；
  后台 dispatcher 每 5 秒投递，失败按指数退避（上限 1 小时），完成记录保留 90 天。
- 未移植：前端告警历史/确认界面。API 已提供，UI 仍只展示活跃告警列表。

## 回归与检查

新增/修改测试：

- `internal/database/database.go`：schema v5，告警确认/事件/outbox 表与触发器。
- `internal/repository/alert_acknowledgement_repository_test.go`：确认持久化、幂等更新、
  告警解除后清除。
- `internal/repository/monitoring_alert_event_repository_test.go`：状态转变、资源删除、
  非状态编辑不产生事件、分页、outbox 仅在启用时入队、重试/完成/清理。
- `internal/repository/monitoring_alert_webhook_repository.go`：outbox 读取、标记、清理。
- `internal/repository/settings_repository_test.go`：设置与 webhook 凭据同事务回滚、
  跨数据库拒接。
- `internal/service/settings_service_test.go`：目标校验、启用/替换/禁用回滚、取消、并发保存、
  凭据写入失败回滚。
- `internal/service/alert_webhook_test.go`：投递、重试、允许主机、重定向、响应状态、
  loopback 拨号拒绝与非公开地址错误。
- `internal/netguard/netguard_test.go`：public unicast 分类（loopback/private/link-local/
  multicast/special-use）。
- `internal/database/webhook_credential_migration_test.go`：旧 webhook 行迁移到控制台命名空间。
- `internal/service/credential_service_test.go`：webhook 目标行不被凭证 API 列出、读取、修改或删除。
- `internal/handler/monitoring_handler_test.go`：健康状态生成告警、失败关闭、确认、历史分页、
  忽略非告警状态、无认证 subject 时拒绝确认。
- `internal/middleware/zoneauth_test.go`：`/monitoring` 是 fleet 路由，zone token 被 403。
- `internal/handler/settings_handler_test.go`：目标加密且脱敏、非白名单拒绝、禁用删除目标。
- `internal/config/config_test.go`：webhook 主机白名单校验，包括 loopback/private/link-local/
  CGNAT 字面量拒绝。

| 检查 | 结果 |
|---|---|
| `go test -race ./...` | 通过 |
| `make pre-commit` | exit 0；Go 覆盖率 74.5%；lint/gosec/complexity 0 issues；Svelte 0 errors / 0 warnings；255 个前端测试；npm audit 0；ShellCheck 无变更脚本；yamllint 13 文件；docs-check 51 项 |
| `make audit` | exit 0；全量 lint/gosec 0 issues；ShellCheck 32 文件；govulncheck 0 可达漏洞 |
| 环境 | 本机默认 Node 22.14.0 缺少 `zlib.createZstdDecompress`；门禁使用 mise 的 Node 22.19.0 |

## 限制

- 仅本机自动化验收，未在真实 PVE/Qubes 上触发告警或投递 webhook；真机验收仍属 OBS-01/QA-01。
- 前端告警确认与历史视图未接入，API 能力先于 UI。
- 安全审查发现的凭证命名空间与 SSRF 问题已修复，见[安全审查修复](2026-10-19-webhook-security-review.md)。
- 邮件与 2FA 仍未实现；本次只完成 webhook。
- 未做多 Console 进程间的运行时配置同步；设置 service 的互斥只覆盖单进程。
