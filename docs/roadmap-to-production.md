# 当前状态与路线图

整理日期：2026-09-20。本文区分当前工作区实现、已有真机验收记录和待完成验收。
未提交代码不等于已发布版本；本轮没有重新执行真机 smoke 或恢复演练。

项目已具备 Proxmox 核心闭环，仍适合受控实验与开发，尚不是通用生产发行版。

## 已有验收证据

Proxmox 生命周期、RemoteVM/qrexec 与结构化传输结果已有现场记录，汇总见
[历史验收记录](reviews/validation-history.md)。它们没有为当前全部工作区改动提供统一版本的
回归证据，不能用作本轮发布验收。操作入口见 [runbook](runbook-remotevm.md)。

## 当前代码已落地

| 范围 | 当前行为 | 验收边界 |
|---|---|---|
| 原生编排 | `NativeExecutor` + Proxmox REST/SSH，`qube_infra` 记录资源身份；旧 Terraform 入口已移除 | 仅注册 Proxmox；GCP/AWS 不可置备 |
| agent 身份 | 实际入口强制角色与 CA 签名撤销状态；握手、恢复会话和长连接均检查 | 状态源可达性为新增部署要求；缓存和重放时间界限见安全控制，尚未真机部署 |
| 远端服务 | 默认只启用 Ping；Exec 使用 JSON argv，FileCopy 使用目录描述符；均继承沙箱 | 不保留 shell 文本/宿主 scope 入口；允许的程序自身仍需审查 |
| 请求与认证 | 浏览器用 HttpOnly、SameSite=Strict session；Bearer 保留给 CLI/MCP；变更请求要求 control scope；命名 token 可按 zones 限制对象，session 继承 | session TTL 默认 12h；设置页的 timeout/2FA/通知尚未接入；zone token 不可用 fleet 端点 |
| API 安全基线 | 安全响应头、CORS、生产配置拒绝不安全默认值、请求体上限、限流、结构化操作者审计 | 不代表完整多租户身份体系或审计归档系统 |
| 数据销毁 | purge 原子记录永久意图并撤销身份，删除当前 key，逐资源核验并清理端点/RemoteVM | 部分失败只允许继续 purge；不能保证清除历史备份里的密钥 |
| 数据密钥 | 独立随机 256-bit DEK 是唯一解锁路径；旧盘首次解锁原子迁移到 DEK，master 只读且仅用于迁移 | 迁移完成前旧盘仍依赖 master；agent 需先允许 RekeyData，按盘真机核验归 QA-01 |
| 重启对账 | queued → failed、running → unknown，Qube → error；保留资源 checkpoint 及 purge 意图 | 显式重试原动作；不自动重放队列或跨进程接管 |
| 传输结果 | stdout/stderr/exit code 独立传输；invoker stdout 达到 16 MiB 上限时中止 | 仍需断线、取消、超时和重启场景的自动化回归 |
| 备份恢复 | SQLite 一致快照、scrypt/AES-256-GCM 归档、覆盖保护与 schema 版本校验 | 有实现与单测，尚无离机恢复演练及 RTO 记录 |
| 工程门禁 | race、lint/gosec、复杂度、依赖扫描、前端、ShellCheck、文档与 workflow 检查 | 本轮 pre-commit/audit 已通过；不替代提交后真机回归 |
| 前端测试 | vitest 会话/API 单测接入 Makefile 和 CI | 尚缺组件/E2E 关键流程测试 |

实现入口：`internal/provider`、`internal/orchestrator/native.go`、`internal/service/reconcile.go`、
`internal/service/qube_service.go`、`internal/service/datakey.go`、`internal/middleware`、
`internal/backup`（均位于 `console/backend/`）。

## 后续工作

[TODO 清单](TODO.md) 是优先级、依赖与验收条件的唯一维护入口。P0 代码已完成本轮加固，
配置与验收边界见[安全控制](security-controls.md)。REL-01/02 的恢复契约已落地，
见[生命周期验收](reviews/2026-09-20-lifecycle.md)；接下来推进密钥/备份边界、恢复演练与真机回归。

本轮门禁证据见[P0 安全加固记录](reviews/2026-09-20-p0-security.md)，
此前失败保留在[工作区检查记录](reviews/2026-09-20-workspace.md)；
每次提交仍必须重新执行当前阶段的 `make pre-commit`，发布和大范围安全合并还需 `make audit`。

## 不在当前承诺内

- 在线 VM 内存迁移；
- 把普通远端主机变成完整 Qubes dom0；
- 依赖云厂商删除或覆写来替代端到端加密；
- 在未验证前宣称 GCP/AWS 与 Proxmox 等价。
