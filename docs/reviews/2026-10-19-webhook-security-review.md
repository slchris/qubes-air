# 2026-10-19 Webhook 安全审查修复

基线：`origin/main` `c5b66a5`（PR #60–#64 已合并）。推送后的自动安全审查报告 3 条发现；
本记录说明逐条处理结果。

## 发现 1 [HIGH] — Webhook 目标行不在控制台命名空间

- **问题**：目标以 `system:alert-webhook-target`（名称 `Alert webhook endpoint`，类型 `webhook`）
  存储。`models.IsConsoleCredential` 只隐藏类型 `pki` 或名称前缀 `qubes-air-` 的行，所以
  `CredentialService` 会把它当成运维行：control scope 的 token 可以列表、读取元数据、改名或删除。
  这正是 G-D8 在移植前登记过的遗留项。
- **修复**：改为控制台自有行 `qubes-air-alert-webhook`（ID
  `qubes-air-alert-webhook-target`，类型 `pki`）。新增启动迁移
  `migrateAlertWebhookCredential`，把旧行搬到新 ID/名称/类型，密文与 `key_version` 原样保留；
  如果新旧行同时存在，新行优先并删除旧行。`CredentialService` 现在对它是不可见/404。
- **验证**：`webhook_credential_migration_test.go` 覆盖迁移、冲突与重复启动；
  `credential_service_test.go` 的 rig 现在包含 webhook 行，列表/读/改/删测试均覆盖它。
  变异红：临时把插入改回旧名称/类型，`TestCredentialServiceRefusesConsoleRowIDs` 报
  “got nil”并指出旧行仍可读。

## 发现 2 [HIGH] — DNS 解析可把允许列表目标指向内网（SSRF）

- **问题**：允许列表只比较 URL 的精确 `host[:port]`，没有校验解析结果。一个允许的主机名如果
  解析到 loopback/private/link-local 地址，dispatcher 会把告警 JSON POST 到内网服务。
- **修复**：
  - 新增 `internal/netguard.IsPublicUnicast`，拒绝 loopback、private、link-local、multicast、
    unspecified 及 0/8、100.64/10、192.0.0/24、198.18/15、240/4 等 special-use 地址。
  - `notifications.webhook_allowed_hosts` 的配置校验拒绝非 public unicast 的 IP 字面量。
  - dispatcher 的 `safeWebhookDialContext` 自己解析主机名，要求每个解析结果都是 public
    unicast，只拨刚校验过的 IP；不会二次解析，因此 DNS 在“校验”和“连接”之间改变答案不能把
    目标移到内网。代理仍为 nil，重定向仍被拒绝；TLS 仍用原 URL 主机名做 SNI/证书校验。
  - 非公开地址使用固定错误文本 `webhook host resolves to a non-public address`，不写 URL 或
    内网地址到 outbox/log。
- **验证**：`netguard_test.go` 的地址分类表；`alert_webhook_test.go` 的 loopback 拨号拒绝
  与固定错误文本；配置测试覆盖 loopback/private/link-local/CGNAT 字面量。变异红：临时移除
  `DialContext`，loopback 测试从固定错误文本变为 `request failed` 并失败。

## 发现 3 [MEDIUM] — Monitoring 路由的 zone 授权与 anonymous 确认

- **核实**：`/api/v1/monitoring` 已列在 `middleware.fleetPrefixes`；`RequireZones` 对带 zone
  限制的凭据直接返回 403，只有 fleet 级凭据（或关闭认证的开发控制台）能到达 handler。因此
  “列出所有 zone”是 fleet 路由的既定语义，不是可被 zone token 利用的 IDOR。按 zone 过滤会把
  fleet 视图变成 half-truth，不做。
- **修复**：`AcknowledgeAlert` 在 `middleware.SubjectFromContext` 失败时返回 401，不再把操作者
  记成 `anonymous`。新增 handler 测试与 `zoneauth_test.go` 的 `/monitoring` fleet-token 200 /
  zone-token 403 用例。变异红：恢复 `subject = "anonymous"` 后，无 subject 测试从 401 变 200。

## 门禁

| 检查 | 结果 |
|---|---|
| `go test -race ./...` | 通过 |
| `make pre-commit` | exit 0；Go 覆盖率 74.5%；lint/gosec/complexity 0 issues；Svelte 0 errors / 0 warnings；255 个前端测试；npm audit 0；ShellCheck 无变更脚本；yamllint 13 文件；docs-check 51 项 |
| `make audit` | exit 0；全量 lint/gosec 0 issues；ShellCheck 33 文件；govulncheck 0 可达漏洞 |
| 环境 | Node 22.19.0（默认 22.14 缺 `zlib.createZstdDecompress`） |
