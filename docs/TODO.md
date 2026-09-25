# 后续 TODO

更新：2026-09-21。勾选表示本轮代码与自动化验收完成；部分条目已按分组提交落库，
但不表示已 push 或部署。
当前能力见[路线图](roadmap-to-production.md)，历史检查见[记录索引](reviews/README.md)。
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
  重跑 `make pre-commit`，个人记忆与构建产物未纳入，未 push。
  受 `cmd/server/main.go`、`config.go`、`database.go` 等共享文件约束，中间提交不保证
  逐个可独立构建。
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
- [ ] **OPS-01：离机恢复及 CA 演练。** 2026-09-21 已完成单机隔离路径预演，见
  [预演记录](reviews/2026-09-21-ops01-restore.md)。2026-09-25 只读环境预检发现目标 Console
  没有挂载的离机目录、真实归档或 `backup.env`，因此还不能做真实离机恢复；`secrets.env`
  存在但未读取，也没有证明 keyring 与归档匹配。详情见[环境预检记录](reviews/2026-09-25-ops01-environment-preflight.md)。
  剩余：真实离机归档、匹配的 keyring、provider 资源对账与 agent 信任校验，以及生产数据量下的 RTO 记录。
- [x] **NET-01：静态 IP 池已撤销。** 按产品决定移除 Proxmox Zone 的 `ip_pool`/`gateway`
  配置和静态分配逻辑；新建 compute 使用 DHCP，恢复时保留已有网络配置。剩余真机网络验证纳入 QA-01。
- [ ] **QA-01：完整 Proxmox 回归记录。** 2026-09-22 已在 homelab 真机完成生命周期回归并
  写入[记录](reviews/2026-09-22-qa01-proxmox.md)：provision→healthy→suspend→resume→release
  →purge 全通过，RemoteVM 注册/注销与吊销状态已验证，发现并修复 `remotevm` 本地命名缺陷
  （`1dbc87f`）。剩余：Exec/FileCopy 正值（需 console 下发允许列表）、suspend/resume 数据
  持久性、旧盘迁移与 known_hosts 带外核对。验收条件不变。
  2026-09-25 的部分回归另完成 provision/suspend/resume/release，确认数据卷身份保持且 agent 恢复健康；
  未执行 purge、未验证文件内容持久性，也未扩大 Exec/FileCopy 权限，且部署二进制无法绑定到源码 revision；
  详见[部分回归记录](reviews/2026-09-25-qa01-proxmox-smoke.md)。

## P2：产品与扩展

- [ ] **GUI-01：无缝桌面闭环。** 验收 appmenu、单击启动、多窗口、退出状态、断线恢复，
  明确 Xpra 与 RemoteVM 权限边界；已有服务原语不能替代完整桌面验收。
- [ ] **QA-02：交互和安装回归。** 已补：登录/session、创建 Qube、purge 确认及后端拒绝的
  组件测试（20 个前端测试，随 pre-commit 与 CI 运行）；agent deb 的安装、依赖解析、升级
  conffile 保留、完整性、卸载与启动拒绝路径由 `make agent-deb-test`（Docker）覆盖，
  CI 有 `agent-package` job。剩余：真实首次 bootstrap、应用启动 E2E 与取消场景。
- [x] **AUTH-01：逐对象授权。** 命名 token 可带 `zones` 白名单，session 继承该限制；
  跨 zone 对象与不存在对象统一 404，fleet 端点对 zone token 返回 403，`GET /zones`、
  `GET /qubes` 在查询层过滤；审计记录 `subject` 与 `zone_scope`。判定与配置见
  [安全控制](security-controls.md#console-api-对象级授权)，自动化覆盖中间件允许/拒绝/
  失败关闭、repository 过滤、session 继承与配置校验。边界：不是完整多租户，fleet 端点
  不做按 zone 过滤；未做 UI 侧可见性降级。
- [ ] **UI-01：设置接入。** 分别实现 session timeout、2FA、邮件、webhook 并提供端到端证据；
  未实现项目继续显示“未接入”，不可仅保存配置便勾选完成。
- [ ] **OBS-01：真实监控、告警与账单。** 接入真实数据源、刷新/失败状态和费用语义；验收
  数据来源可追溯、过期/缺失不伪装为正常值，然后移除对应 placeholder。
- [ ] **CLOUD-01：GCP 原生适配器。** 实现资源与可信网络路径，完成独立生命周期及销毁验收。
- [ ] **CLOUD-02：AWS 原生适配器。** 同样独立验收；不因 GCP 或 Proxmox 通过而视作可用。
- [ ] **MCP-01：桌面帧与输入。** 当前工具仍显式失败；实现协议客户端前定义可见接管提示、
  中断、policy 与输入授权。验收真实帧/输入和拒绝路径；依赖 GUI-01 与 SEC-01。
- [ ] **MCP-02：可选 HTTP transport。** 先明确部署需求与监听/认证边界，再考虑 loopback 或
  受控网络入口；stdio 继续作为当前入口，不把候选设计当作已提供能力。
- [ ] **PUB-01：发布材料。** 决定开源时补许可证、安全报告入口；发布说明引用同版本构建、
  完整 audit 与真机结果，并明确 provider/桌面/恢复能力限制。

## 推荐推进顺序

P0、REL-01/REL-02、工具链准备与 ENG-01 分组提交已完成。下一阶段推进
DATA-01/OPS-01 → QA-01，之后处理 GUI-01 和其余产品任务。这里的并列表示依赖关系，
不代表已经分派给其他 agent。

完成任务时，在本清单勾选并附测试/验收记录；提交后补 commit 标识，同时更新路线图和相关专题文档。
后端入口相对 `console/backend/`；远端脚本路径相对仓库根目录。
