# 后续 TODO

更新：2026-10-23。勾选表示代码与自动化验收完成并已合入 `main`；不表示已部署或已通过真机验收。
2026-10-23 现场修复已由 Air PR #90（`7a5fc9a`）和 Salt PR #3（`cc8c11c`）合入，合并 CI 通过；
部分验收通过不代表整个 OPS-01/QA-01 完成。后续网络资格修复仍按本阶段状态记录。
当前能力见[路线图](roadmap-to-production.md)，历史检查见[记录索引](reviews/README.md)。
M0–M3 里程碑与 G-* 缺口的逐项状态见[生产可用性缺口](production-readiness-gaps.md)。
P0 实现与门禁证据见[安全加固记录](reviews/2026-09-20-p0-security.md)，
后续 REL-01/02 证据见[生命周期验收](reviews/2026-09-20-lifecycle.md)。

## P0：收紧实际执行边界

- [x] **SEC-01：agent 角色授权与吊销执行。** 角色检查不再依赖注册表；实际 agent 必填
  CA 签名撤销状态地址，刷新失败拒绝授权，长连接周期复查并结束空闲读取。
  已覆盖错误 CA、用途、角色、过期、会话恢复、既有连接吊销，以及状态篡改、回退、重启失败。
  缓存、重放窗口及临时 Console 证书边界见[安全控制](security-controls.md)。
- [x] **SEC-02：Exec 不能绕过允许的命令范围。** 改为有界 JSON argv 和直接 execve，
  FileCopy 使用目录描述符与 O_NOFOLLOW，继承 agent 沙箱。补齐命令文本拒绝、参数字面传递、
  输入输出上限、取消、超时、非零退出及文件路径边界测试；允许程序自身的能力仍需部署方审查。
- [x] **SEC-03：provider 管理连接认证。** Proxmox 执行器和调度器强制 HTTPS 验证，
  支持公共 CA 配置并拒绝重定向；SSH 强制 verified known_hosts。
  已覆盖合法配置、错误 CA/主机名/过期证书及未知、错误、撤销的 SSH host key。

## P1：可靠性与提交前收尾

- [x] **ENG-01：完整质量门禁与分组提交。** 工具链准备与完整 `make pre-commit`、
  `make audit` 已通过，见本轮安全加固记录；测试环境需要 Python 3 和整个仓库。
  2026-09-21 已按独立意图把工作区改动分成 11 组提交到本地 `main`：质量门禁/文档、
  provider、安全、会话、可靠性、备份、传输、接线、MCP、pingcheck、依赖；每组提交前
  重跑 `make pre-commit`，个人记忆与构建产物未纳入；随后经 PR #9（`5f0fd88`）合入 `main`，合并前第四轮 CI 21/21 全绿。
  受 `cmd/server/main.go`、`config.go`、`database.go` 等共享文件约束，中间提交不保证逐个可独立构建。2026-09-26 回退：直推的 `38b71c9`
  留下无调用方的 `runSSH`，`a011ce8` 上 Go Lint 失败；修复 `2d409fd` 与本次状态更正一起合入，合入后 `main` 上首轮 CI 全绿才关闭 G-A1。
- [x] **REL-01：purge 部分失败与逐资源对账。** 永久 purge 意图、原子撤销与发行拦截已接入；
  失败后仅可重试 purge，分别核验 compute、holder、数据盘、当前 snippet，端点/RemoteVM
  清理失败不报成功。残留资源与不明所有权保留记录供人工核对；故障注入及门禁证据见
  [生命周期验收](reviews/2026-09-20-lifecycle.md)，真机仍属 QA-01。
- [x] **REL-02：崩溃一致性与重复请求。** 创建前持久化资源 ID，丢失响应后复用并核验所有权；
  queued → failed、running → unknown，保留意图和身份供显式重试，不自动重放队列。
  补齐数据库重开、并发 claim、保存失败、超时及恢复测试，修复编辑覆盖在途状态的问题。
  完整约束见[恢复契约](reliability-design.md)；不包含跨 Console 进程接管。
- [x] **DATA-01：独立密钥与备份销毁边界。** 已移除静默派生：DEK 是唯一解锁路径，旧盘
  首次解锁经 `qubesair.RekeyData` 原子迁移（先存 DEK、后换键，失败保留旧键可重试），
  master 只读且仅用于迁移，不再自动创建。删除、快照与备份副本的保留/销毁影响见
  [凭据销毁](credential-destruction.md)与[灾难恢复](disaster-recovery.md)。
  验收边界：自动化覆盖迁移成功、部分失败、标记重试与拒绝路径；按盘真机核验密钥来源
  仍归 QA-01，未迁移盘不能宣称 crypto-shred。证据见
  [数据密钥迁移记录](reviews/2026-09-21-data-keys.md)。
- [ ] **OPS-01：离机恢复及 CA 演练。** 受控范围已通过（2026-10-23，分支验收）：
  真实离机归档、原 keyring 解密 4 条凭据、原 CA 指纹、27 条签名吊销记录、只读 provider
  对账、错口令/错 keyring/已有目标拒绝均完成。Console AppVM 重启及受控 timer 触发后，
  最新归档恢复至健康耗时 1.503 秒。见[闭环记录](reviews/2026-10-23-production-closure.md)。
  剩余：生产数据量 RTO、完整控制域重建与在线 agent 接管；小规模结果不覆盖这些场景。
- [x] **NET-01：静态 IP 池已撤销。** 按产品决定移除 Proxmox Zone 的 `ip_pool`/`gateway`
  配置和静态分配逻辑；新建 compute 使用 DHCP，恢复时保留已有网络配置。剩余真机网络验证纳入 QA-01。
- [ ] **QA-01：完整 Proxmox 回归记录。** 2026-10-23 分支验收已补齐此前被 dom0 policy
  阻断的 Exec/FileCopy 完整 qrexec 正反例，1 MiB 往返及 suspend/resume 后原文件读取均通过。
  当前 Console/agent `eb753f4`、handler `4455409`；生命周期、证书换发及两个已授权测试对象
  purge 完成，历史资源保留，临时精确授权已撤销。见[闭环记录](reviews/2026-10-23-production-closure.md)。
  节点网络资格修复已在当前分支补齐自动调度和 provider 分配前检查，真实只读探针证明六个
  节点中仅一个满足 zone 网络条件；完整门禁通过，合入与部署后生命周期仍待完成，见
  [网络资格记录](reviews/2026-10-23-network-eligibility.md)。purge 后 Console identity YAML
  与 Relay endpoint 缓存自动回收仍待实现；上一轮残留已手动清理。
  另待：旧盘 DEK 迁移、SSH 主机键独立带外核对、release URL 置备及 Relay 自身重启。
  历史验收见[生产部署记录](reviews/2026-10-19-production-deploy.md)与
  [当前 main 生命周期记录](reviews/2026-10-19-current-main-real-machine.md)。

## P2：产品与扩展

- [ ] **GUI-01：无缝桌面闭环。** 验收 appmenu、单击启动、多窗口、退出状态、断线恢复，
  明确 Xpra 与 RemoteVM 权限边界；已有服务原语不能替代完整桌面验收。
- [ ] **QA-02：交互和安装回归。** 已补：登录/session、创建/编辑 Qube、purge 确认、应用菜单、zone 创建只提供已实现 provider
  及后端拒绝的组件测试（2026-09-26 合并树上 vitest 20 个测试文件、238 个用例，随 pre-commit 与 CI 运行）；模拟 API 的 Playwright E2E 走完登录→创建→应用启动→挂起→purge
  （`make frontend-e2e`、CI `frontend-e2e` job）；agent deb 的安装、依赖解析、升级 conffile 保留、完整性、卸载与启动拒绝路径
  由 `make agent-deb-test`（Docker）覆盖，CI 有 `agent-package` job。本轮首次 bootstrap 已真机通过；剩余为真实环境下的应用启动 E2E 与取消场景。
- [x] **AUTH-01：逐对象授权。** 命名 token 可带 `zones` 白名单，session 继承该限制；
  跨 zone 对象与不存在对象统一 404，fleet 端点对 zone token 返回 403，`GET /zones`、
  `GET /qubes` 在查询层过滤；审计记录 `subject` 与 `zone_scope`。判定与配置见
  [安全控制](security-controls.md#console-api-对象级授权)，自动化覆盖中间件允许/拒绝/
  失败关闭、repository 过滤、session 继承与配置校验。边界：不是完整多租户，fleet 端点
  不做按 zone 过滤。UI 侧可见性降级已做：`GET /session` 报告 zones，zone-scoped session
  置灰 fleet-only 视图并回到 dashboard（仅显示层，服务端判定不变）。
- [ ] **UI-01：设置接入。** 分别实现 session timeout、2FA、邮件、webhook 并提供端到端证据；
  未实现项目继续显示“未接入”，不可仅保存配置便勾选完成。已接入 session timeout：5–1440 分钟，
  保存即生效，已签发 session 只缩短不延长；旧库里的越界值在启动时告警并回退 30 分钟
  （端到端测试 `console/backend/cmd/server/session_timeout_test.go`）。已接入 webhook：目标以
  AES-256-GCM 存入 credentials、只接受精确 HTTPS 主机白名单、事件经 outbox 重试投递，设置响应只
  返回 `webhookConfigured`（见[记录](reviews/2026-10-19-alert-webhook.md)）。剩余：2FA、邮件；
  邮件与 2FA 在 UI 上禁用，服务端也拒绝开启。
- [ ] **OBS-01：真实监控、告警与账单。** 指标已接：Linux Console 主机指标与 Proxmox 运行中 Qube 的实时读数，均标来源与采样时间，缺失带原因不显示为零；UI 每分钟重取（单次 20 秒无应答按失败），Qube 读数与主机指标的采样时间超 2 分钟、超前 30 秒或缺失时各自标 stale。
  告警已接：Zone disconnected / agent unreachable 的 raised/resolved 由 SQLite 触发器持久化，
  提供确认、历史与加密 webhook outbox 重试（见[记录](reviews/2026-10-19-alert-webhook.md)）。
  仍缺：Proxmox 读数未在真机 PVE 上核对；费用数据源、其他 provider 的 Qube 指标与历史指标。
  前端告警确认/历史视图已接入 Monitoring 视图：确认按钮、操作者/时间、分页历史
  （见[记录](reviews/2026-10-19-alert-ui.md)）。
- [ ] **CLOUD-01：GCP 原生适配器。** 实现资源与可信网络路径，完成独立生命周期及销毁验收。
- [ ] **CLOUD-02：AWS 原生适配器。** 同样独立验收；不因 GCP 或 Proxmox 通过而视作可用。
- [ ] **MCP-01：桌面帧与输入。** 部分完成：desktop_frame_get 经 Console 逐帧人工批准（Desktop access 页 Allow/Deny/Stop）后读一帧，
  一次性 grant、`console-desktop` 身份与 agent 端口限制有自动化测试（[记录](reviews/2026-09-23-mcp-xpra-screenshot.md)）。剩余：qube 内 Xpra 监听与认证的部署、
  与真实 Xpra 的互通验收、输入（desktop_input_send 仍显式失败）、dom0 policy 层的接管提示；依赖 GUI-01 与 SEC-01。
- [ ] **MCP-02：可选 HTTP transport。** 先明确部署需求与监听/认证边界，再考虑 loopback 或
  受控网络入口；stdio 继续作为当前入口，不把候选设计当作已提供能力。
- [ ] **PUB-01：发布材料。** 决定开源时补许可证、安全报告入口；发布说明引用同版本构建、
  完整 audit 与真机结果，并明确 provider/桌面/恢复能力限制。

## 推荐推进顺序

Air/Salt 上一轮变更已合入。当前先完成节点网络资格检查，再修退役资源自动回收，之后补
OPS-01 的容量/接管、QA-01 的旧盘迁移/带外指纹/release URL，之后推进 GUI-01。
本轮完整 qrexec、数据持久性与小规模离机恢复已有证据，不再列为“缺少环境入口”。

完成任务时，在本清单勾选并附测试/验收记录；提交后补 commit 标识，同时更新路线图和相关专题文档。
后端入口相对 `console/backend/`；远端脚本路径相对仓库根目录。
