# 历史检查与验收记录

这里保存有用的检查证据，不提供部署入口，也不表示当前工作区已经通过相同验收。
当前进度见[路线图](../roadmap-to-production.md)，待办见 [TODO](../TODO.md)。

| 记录 | 范围 | 限制 |
|---|---|---|
| [2026-10-23 生产闭环修复](2026-10-23-production-closure.md) | schema 只读预检、Salt 重复部署与 Console 重启；完整 qrexec 正反例、持久文件与已授权 purge；原 keyring/CA 离机恢复、只读对账与实测 RTO | 分支验收；生产规模/接管、节点网络调度、退役缓存自动回收、Relay 重启及 GUI 未闭环 |
| [2026-10-19 生产 console 部署与复验](2026-10-19-production-deploy.md) | 五次部署（`b73cae1`→`87ef8c1`→`66325fb`→`d4eb48c`→`e23265a`），schema 2→6，数据保留；四次完整 lifecycle；RemoteVM 注册预算（#78）、resume 地址落库（#79）、purge 状态文案（#82）与 Proxmox zone VLAN 标签（#85）；Exec/FileCopy agent 侧正反例、数据盘跨 resume 持久性；VLAN 4094 隔离与 qa-deploy-8 真机通过；LAN DHCP 静态段已排除；qa-deploy-9（2026-10-22）复验含 running purge 409 文案 | 手工部署；Salt pin 未更新；Exec/FileCopy 的 qrexec transport/dom0 policy 未验证；强制超时幂等未覆盖 |
| [2026-10-19 当前 main 真机测试](2026-10-19-current-main-real-machine.md) | 当前 HEAD 构建、独立实例、PVE credential/zone/capacity、job 日志写入与保留/过期、完整 lifecycle（provision→suspend→resume→suspend→purge）、证书轮换与销毁；定位并修复 completion 上下文 bug | 独立实例测试；Exec/FileCopy、数据持久性、生产部署升级与 RemoteVM 注册未覆盖 |
| [2026-10-19 告警确认与历史视图](2026-10-19-alert-ui.md) | Monitoring 视图的告警确认、操作者/时间显示与分页历史；失败路径测试 | 组件测试，无真实登录/触发器的 E2E 或真机验证 |
| [2026-10-19 Job 日志保留与恢复语义](2026-10-19-job-log-retention.md) | 30 天保留/每日清理、`log_created_at`/`log_pruned_at`、API `log_state` 五种状态与前端说明 | 本机自动化；未做换机恢复演练，未在真机观察恢复后的 UI |
| [2026-10-19 Webhook 安全审查修复](2026-10-19-webhook-security-review.md) | webhook 目标移入控制台凭据命名空间、DNS 解析 SSRF 防护、确认接口 fail closed 与 fleet 路由验证 | 本机自动化与变异测试；无真机投递/内网 DNS 场景 |
| [2026-10-19 告警持久化与加密 Webhook](2026-10-19-alert-webhook.md) | 告警事件/确认/历史、加密 webhook 目标、主机白名单与 outbox 重试 | 本机自动化，无真机告警/投递验证；前端确认与历史视图未接 |
| [2026-10-18 设置读取与原子保存最小移植](2026-10-18-settings-atomicity-port.md) | 设置单快照读取、错误传播、三组设置事务写入及失败路径测试 | 只覆盖无 webhook 的最小部分；本机自动化，无真机证据 |
| [2026-09-22 QA-01 Proxmox 回归](2026-09-22-qa01-proxmox.md) | 真机 provision→purge、RemoteVM 注册/注销、吊销状态；发现并修复 remotevm 命名缺陷 | Exec/FileCopy 正值与迁移未覆盖；known_hosts 为首次扫描 |
| [2026-09-21 本地隔离恢复演练](2026-09-21-ops01-restore.md) | 备份、隔离路径恢复、重启 Console、凭据/吊销状态校验 | 单机预演，非离机；RTO 不代表生产规模 |
| [2026-09-21 数据密钥迁移](2026-09-21-data-keys.md) | DEK 唯一解锁路径、旧盘原子迁移、迁移标记与失败重试 | 自动化验收；真机迁移与按盘核验属 QA-01 |
| [2026-09-20 生命周期可靠性](2026-09-20-lifecycle.md) | purge、持久化 checkpoint、重启与重复请求 | 2026-09-21 分组提交；显式重试契约，无新增真机验收 |
| [2026-09-20 P0 安全加固](2026-09-20-p0-security.md) | 三项 P0 实现、自动化回归及完整门禁 | 2026-09-21 分组提交；没有新增真机验收 |
| [2026-09-20 工作区检查](2026-09-20-workspace.md) | 文档整理、前端验证、容器 Go 测试与门禁 blocker | 当日未提交工作区快照，完整 pre-commit 未通过 |
| [已有真机验收汇总](validation-history.md) | 从原有文档整理的 Proxmox/RemoteVM 现场记录 | 非新验收；缺少统一源码版本和完整原始日志 |
| [2026-09-25 OPS-01 环境只读预检](2026-09-25-ops01-environment-preflight.md) | 只读检查 Console、离机备份路径和 Qubes 配置 | 无真实离机归档/keyring，不能执行真实恢复 |
| [2026-09-25 QA-01 Proxmox 部分回归](2026-09-25-qa01-proxmox-smoke.md) | 专用 Qube 的 provision/suspend/resume/release 与资源身份核对 | 未 purge；文件内容持久性、Exec/FileCopy、源码版本绑定未完成 |
| [2026-09-23 MCP-01 截图客户端与经批准的取帧](2026-09-23-mcp-xpra-screenshot.md) | Xpra 单次截图客户端、consent 状态机与 API、`console-desktop` 取帧身份、MCP 工具与审批页（2026-09-26 按本仓库实现重写） | 只有自动化测试；qube 内没有部署 Xpra 监听，真实 Xpra 互通未验证；输入仍未实现 |

2026-09-21 把记录对应的未提交工作区按独立意图分组提交到本地 `main`，2026-09-22 经 PR #9
（`5f0fd88`）合入 `origin/main`；记录正文保留当时的结论，提交事实以补充说明为准。

记录按实际发生的日期或已知范围命名。保留原始失败与重试，不回写成“一次全量通过”。
过期安装入口、现场 secret/地址、临时构建 pin 不归档到这里；历史代码通过 Git 查询。
