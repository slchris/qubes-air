# 当前状态与路线图

更新时间：2026-07-22。这里同时记录当前能力、代码审查发现和下一步顺序；不维护旧环境兼容计划。

## 已完成并真机验证

- Proxmox UI 置备、流式 job log 和 agent 健康探测；
- cloud-init 只投递公开 CA、单次 token 与 artifact digest，agent 私钥在 guest 生成；
- agent CSR 签发、mTLS、自动续期与 bootstrap 重试；
- compute/storage 分离，suspend/resume 挂回持久盘；
- LUKS 数据盘，密钥不保存到远端；
- Qubes R4.3 RemoteVM 自动注册；
- 独立 Relay CSR、端点自动同步和 `qubesair.GrpcProxy`；
- `Ping`、`Exec`、`FileCopy` 和 `ConnectTCP`；
- 控制台凭据 AES-256-GCM、多版本原子轮换；
- OpenTofu state 客户端加密和 S3/PostgreSQL backend 入口。

## 代码审查结论

### P0：先收紧远端信任边界

| 不足 | 当前证据 | 完成标准 |
|---|---|---|
| mTLS 只证明“同一 CA 签发”，没有证明调用方角色 | agent、Relay 和 Console 临时身份共用证书模板；agent server 未配置角色校验，`relay-call` 也未校验目标 agent 名称 | 为 agent server、Relay client、Console client 建立独立证书用途或带角色的 SAN；agent 只接受允许的调用方角色；客户端同时校验目标 Qube 身份 |
| 远端管理服务权限过宽 | Debian unit 默认启用 `Exec`、`FileCopy`、`UnlockData`；前两者通过 `systemd-run` 以宿主 root 执行，`FileCopy` 可操作任意绝对路径 | 默认只启用 `Ping`；特权操作进入独立、窄接口的 helper；限制命令和文件路径；每次调用留下 caller、target、service 和结果审计 |
| 证书同时被当作 client 和 server 使用 | 签发模板只有 `ClientAuth`，agent 作为 server 时靠 `ExtKeyUsageAny` 自定义验证 | 分离 server/client 身份并使用正确 EKU；删除为绕过用途校验而存在的宽松验证 |

完成这些修复前，不应把“dom0 policy 是唯一强制授权边界”当作已经完全成立：持有 fleet 证书的
内部节点目前可以直接连接另一个 agent。

### P1：修正数据销毁、身份与编排可靠性

| 不足 | 当前证据 | 完成标准 |
|---|---|---|
| 单个 Qube 不能 crypto-shred | 所有 LUKS key 由同一个 `qubes-air-luks-master` 和 Qube ID 派生；删除记录后仍可重新派生 | 改为可独立删除的 per-Qube 随机 DEK/secret，并提供迁移、备份、恢复和轮换流程 |
| “删除”只有 release，没有真正 purge | API DELETE 只销毁 compute 并保留数据盘、证书和数据库记录；`ActionDestroy` 没有用户入口 | 增加明确二次确认的 purge；销毁盘、撤销身份、清理 RemoteVM/endpoint/记录，并报告每一步结果 |
| job queue 重启后无法自动对账 | queue 在内存中；启动时把 queued/running job 标记为 `outcome unknown`，没有基于 state/provider 的自动 reconciliation | 持久化可恢复队列和幂等键；启动时执行 refresh/plan 与动作级对账，区分“失败”和“结果未知” |
| 响应大小限制在缓冲后才检查 | invoker 先把 stdout 全量写入 `bytes.Buffer`，再检查 16 MiB | 使用有界 writer/stream，在超限时中止服务，覆盖超量输出和取消测试 |
| 更新接口绕过创建校验 | Qube rename/spec update 直接写库，没有复用名称和规格校验 | 更新前执行与创建相同的名称、规格和状态约束，防止把无效对象写进 Terraform source of truth |

### P2：控制台安全与产品语义

- 当前只有一个静态 Bearer token；留空即关闭认证，前端还把 token 长期保存于 `localStorage`。
  需要短期 session、HttpOnly/SameSite cookie、操作级授权和明确操作者审计。
- 设置页中的 session timeout、2FA、邮件和 webhook 目前只保存配置，没有执行效果。实现前应隐藏或标记为
  “未接入”，避免形成错误安全预期。
- API 缺少统一的请求体上限、速率限制和 CSP/HSTS/frame 等安全响应头；生产模式应对空 token、宽
  CORS、未加密 state 等关键配置 fail closed，而不只是打印 warning。
- CORS 对不允许的 Origin 会返回 allowlist 第一项；应返回空并添加 `Vary: Origin`。
- `Exec`/`FileCopy` 通过文本 trailer 表示失败且恒返回成功，调用方无法稳定区分 transport 成功和业务
  失败。协议应分别传输 exit code、stdout 和 stderr。

### P3：补齐尚未完成的产品和工程能力

- 无缝桌面仍需验收 appmenu、单击启动、多窗口、退出状态和断线恢复。
- Proxmox 是唯一完整 provider；GCP 私网可达性未闭环，AWS 仍是占位实现，多 credential zone 也受
  单 provider 实例限制。完成前不宣称多云等价。
- 监控的 CPU/磁盘和账单 API 是 placeholder，GCP/AWS 容量查询未实现；UI 应隐藏占位结果或清楚标注。
- SQLite、CA、credential store 和 job log 构成单控制台故障域；需要经过演练的加密备份/恢复、schema
  migration 版本和 CA 灾难恢复流程。
- CI 中 `gosec -no-fail`、`tfsec --soft-fail`、`govulncheck continue-on-error` 会放过安全失败；构建仍用
  Terraform 1.5，而当前 state 加密路径要求 OpenTofu。关键扫描应阻断合并并固定 Action 版本。
- Go 测试覆盖较完整，但前端没有单元/E2E 测试，远端 qrexec 脚本和真实多主机网络链路也缺少自动化
  回归。补充前端交互、agent 包安装、超时/断线/重启和真机 smoke test。
- 删除未使用的 transport、非现行 dom0 安装入口和失效源码注释；文档链接与“已实现后仍写 TODO”检查进入
  CI。若项目按开源方式发布，还需要补充明确的仓库许可证和安全报告入口。

## 推荐执行顺序

1. 证书角色隔离、目标身份校验和 agent 侧调用方授权；
2. 收紧默认远端服务，拆分 root helper，并修复有界输出；
3. 实现 per-Qube key、真实 purge 和恢复/销毁演练；
4. 补 job reconciliation、更新校验与 API 安全基线；
5. 再完成桌面闭环、provider、监控账单和测试矩阵。

## 不在当前承诺内

- 在线 VM 内存迁移；
- 把普通远端主机变成完整 Qubes dom0；
- 依赖云厂商删除或覆写来替代端到端加密；
- 在未验证前宣称 GCP/AWS 与 Proxmox 等价。
