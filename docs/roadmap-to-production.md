# 当前状态与路线图

更新日期：2026-10-25。本文区分已合入实现、发布制品与真机验收；逐项状态见
[生产可用性缺口](production-readiness-gaps.md)。生产闭环修复已合入，
[v0.2.1](https://github.com/slchris/qubes-air/releases/tag/v0.2.1) 已发布八项部署制品与
`SHA256SUMS`，包括 release URL 安装和 Proxmox 取消处理修复。主干另已合入
[DATA-02](reviews/2026-10-25-plain-data-mount-identity.md)，完成隔离挂载与启动门禁验收。
当前 release URL 的完整 provider 生命周期仍需独立真机验证。

项目已具备 Proxmox 核心闭环，仍适合受控实验与开发，尚不是通用生产发行版。

## 已有验收证据

[2026-10-23 闭环记录](reviews/2026-10-23-production-closure.md)包含可重复 Salt 部署、
Console 重启、Exec/FileCopy 完整 qrexec 正反例、1 MiB 文件跨 suspend/resume、精确授权的
测试对象 purge，以及真实离机归档/原 keyring/CA/签名吊销恢复与只读 provider 对账。
[最新恢复副本](reviews/2026-10-23-restored-agent-probe.md)已用原信任材料连接运行中 agent：
至健康 0.860 秒、至真实 mTLS Ping 0.962 秒；原 Console 仍提供吊销服务，不能外推为完整接管 RTO。

[网络资格](reviews/2026-10-23-network-eligibility.md)与
[purge 自动清理](reviews/2026-10-23-purge-residual-cleanup.md)已部署并通过专用对象生命周期；
[Console 持久入站](reviews/2026-10-23-console-ingress.md)已通过重启验证，
[ConnectTCP](reviews/2026-10-23-connecttcp-identity.md)已证明旧流中断失败、新调用恢复。

此前结果见[历史验收记录](reviews/validation-history.md)，操作入口见
[runbook](runbook-remotevm.md)。前两对象及新专用对象均已按精确授权清理，历史资源保留。

## 当前代码已落地

| 范围 | 当前行为 | 验收边界 |
|---|---|---|
| 原生编排 | `NativeExecutor` + Proxmox REST/SSH，`qube_infra` 记录资源身份；旧 Terraform 入口已移除 | 仅注册 Proxmox；GCP/AWS 不可置备 |
| agent 身份 | 实际入口强制角色与 CA 签名撤销状态；握手、恢复会话和长连接均检查 | 本轮真实首次 bootstrap、resume 证书换发及 purge 吊销已通过；缓存和重放时间界限见安全控制 |
| 远端服务 | 默认只启用 Ping；Exec 使用 JSON argv，FileCopy 使用目录描述符；均继承沙箱 | Exec/FileCopy 已通过完整 qrexec 正反例；允许程序自身仍需审查；ConnectTCP handler 已复验，完整 GUI 尚未闭环 |
| 请求与认证 | 浏览器用 HttpOnly、SameSite=Strict session；Bearer 保留给 CLI/MCP；变更请求要求 control scope；命名 token 可按 zones 限制对象，session 继承 | session TTL 默认 30 分钟，设置页可改为 5–1440 分钟并即时生效；webhook 目标加密存储并受精确 HTTPS 主机白名单约束，事件经 outbox 重试投递；2FA 与邮件通知尚未接入；zone token 不可用 fleet 端点 |
| API 安全基线 | 安全响应头、CORS、生产配置拒绝不安全默认值、请求体上限、限流、结构化操作者审计 | 不代表完整多租户身份体系或审计归档系统 |
| 数据销毁 | purge 原子记录永久意图并撤销身份，删除当前 key，逐资源核验并自动清理身份文件、端点/RemoteVM | 新专用对象无残留、重复请求不新增 destroy job；部分失败只允许继续 purge，历史备份里的密钥不随之删除 |
| 数据密钥 | 独立随机 256-bit DEK 是唯一解锁路径；旧盘首次解锁原子迁移到 DEK，master 只读且仅用于迁移 | 迁移完成前旧盘仍依赖 master；agent 需先允许 RekeyData，按盘真机核验归 QA-01 |
| 重启对账 | queued → failed、running → unknown，Qube → error；保留资源 checkpoint 及 purge 意图 | 显式重试原动作；不自动重放队列或跨进程接管 |
| 未加密数据盘 | cloud-init 共享身份检查；仅完整全零的新盘初始化，实际挂载身份通过后才允许 agent 启动 | [DATA-02](reviews/2026-10-25-plain-data-mount-identity.md) 的 QEMU、包回归与完整门禁通过；既有 cloud-init 不会自动更新，生产部署另验 |
| 传输结果 | stdout/stderr/exit code 独立传输；stdout 有界；已派发调用断线明确失败，不重放或跨连接续发旧帧 | 自动化覆盖取消/超时/重连，真机活跃流断线非零退出及新调用恢复通过；Relay 整机重启仍缺备用管理入口 |
| 备份恢复 | SQLite 一致快照、scrypt/AES-256-GCM 归档、覆盖保护与 schema 版本校验 | 离机恢复、只读对账、备份触发与恢复副本真实 agent Ping 通过；完整控制域/入口/吊销服务与单写入方接管仍未覆盖 |
| 工程门禁 | race、lint/gosec、复杂度、依赖扫描、前端、ShellCheck、文档与 workflow 检查 | 传输阶段 `make pre-commit` / `make audit` 通过，覆盖率 75.1%；Salt 56 个合约测试与全量 lint 通过，两仓合并 CI 全绿 |
| 前端测试 | vitest 组件/会话/API 单测接入 Makefile 和 CI（20 个测试文件，15 个组件都有测试文件）；Playwright 以模拟 API 覆盖登录→创建→实时日志→应用启动→挂起→purge 主路径（`make frontend-e2e`、Build CI） | E2E 不连真实 console/agent/集群；首次 bootstrap 本轮已通过；真实应用启动与取消场景仍属 QA-02 |

实现入口：`internal/provider`、`internal/orchestrator/native.go`、`internal/service/reconcile.go`、
`internal/service/qube_service.go`、`internal/service/datakey.go`、`internal/middleware`、
`internal/backup`（均位于 `console/backend/`）。

## 后续工作

[TODO 清单](TODO.md) 是优先级、依赖与验收条件的唯一维护入口。P0 代码已完成本轮加固，
配置与验收边界见[安全控制](security-controls.md)。REL-01/02 的恢复契约已落地，
见[生命周期验收](reviews/2026-09-20-lifecycle.md)。下一阶段修复 GUI 启动边界并完成桌面验收。
完整恢复接管、旧盘 DEK 迁移、SSH 带外指纹、Relay 重启与新 release URL 置备仍有独立前提；
用户未设定容量/RTO 硬指标，当前保留小规模实测。其他 provider 继续独立验收。

本轮门禁证据见[生产闭环记录](reviews/2026-10-23-production-closure.md)，
此前失败保留在[工作区检查记录](reviews/2026-09-20-workspace.md)；
每次提交仍必须重新执行当前阶段的 `make pre-commit`，发布和大范围安全合并还需 `make audit`。

## 不在当前承诺内

- 在线 VM 内存迁移；
- 把普通远端主机变成完整 Qubes dom0；
- 依赖云厂商删除或覆写来替代端到端加密；
- 在未验证前宣称 GCP/AWS 与 Proxmox 等价。
