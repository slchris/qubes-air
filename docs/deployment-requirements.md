# 生产部署安全要求

更新：2026-09-22。本文是"单操作者自用生产"（A 档）的**部署硬要求与核对清单**：它规定部署方
必须显式满足什么，各控制自身的实现细节见 [P0 安全控制](security-controls.md)，本文不重复。

写这份清单的原因是：下面每一条**默认都不满足**，而且代码不会替你拒绝启动——例外是第 2 条
（`QUBES_AIR_PRODUCTION` 会让三类危险配置直接启动失败），以及**同一数据库上的第二个 console**：
启动时取的 `flock(2)` 单实例锁若已被别的进程持有，第二个进程会拒绝启动并报出锁文件路径与持锁 pid
（默认锁文件 `<database.dsn>.lock`，见 [runtime-defaults](runtime-defaults.md) UD-1i）。所以
"只跑一个实例"不再是需要部署方自己记住的约定；反过来，**不要**再把两个 console 指向同一数据库当作
可行部署方式。"怎么核对"列的端口按你的 `server.port` 替换，命令都在 console 主机上执行。

## 1. 硬要求清单

| # | 要求 | 不满足会怎样 | 怎么核对 |
|---|---|---|---|
| 1 | **对外监听必须是 TLS，或只监听 loopback**（`server.host: 127.0.0.1`） | 默认 `0.0.0.0:8080` 是**明文 HTTP**，登录态与 API token 走网络可被读取；`Production` 模式**不会**因为明文而拒绝启动 | 启动日志的 `Listen:` 与 `TLS:` 两行；`ss -ltnp \| grep <port>` 看监听地址是否落在 `127.0.0.1`；`curl -sI http://<host>:<port>/health` 从另一台机器应连不上（若只监听 loopback）。**参考部署已满足这条**：`qubes-salt-config` 的 `listen` 默认就是 loopback，并写明"控制台没有 TLS，token 是唯一屏障，绑 `0.0.0.0` 等于把未加密控制面发布给整个 LAN，走 SSH 端口转发访问"（`salt/config.jinja` 第 168-171 行） |
| 2 | **`QUBES_AIR_PRODUCTION=true`** | 空 API token（鉴权关闭）、内置开发加密密钥、通配 CORS 只会打 `SECURITY WARNING` 而继续运行；打开后这三类直接拒绝启动 | 故意留空 `QUBES_AIR_API_TOKEN` 启动，**必须失败**；启动日志不应出现 `SECURITY WARNING` 三连 |
| 3 | **session cookie 的 `Secure` 属性**（与要求 1 是同一件事的两面） | `secure` 直接绑在 `server.tls.enabled` 上，**没有**单独的开关：TLS 终结在反代时 console 看到的是明文请求，cookie 就不会带 `Secure`，浏览器可能明文回传登录态 | 登录后看响应头是否 `Set-Cookie: ...; Secure`。反代终结 TLS 的场景满足不了这条，所以要求 1 才要求 TLS 由 console 自己终结、或只监听 loopback |
| 4 | **独立 API token 并按用途分权** | 与浏览器登录共用一个全权 token，任何脚本泄露都等于全权泄露 | 用 scoped token 调 `/api/v1/*`：越权的对象应得 403 而不是 200，且审计里能看到被拒的 scope |
| 5 | **32 字节加密密钥 / 版本化 keyring 的保管** | 数据库备份**只含密文**，恢复时缺 keyring 等于备份不可用；丢失 `qubes-air-luks-master` 会让尚未迁移的旧加密盘无法解锁 | 按[凭据与轮换](credential-vault.md)核对：密钥不在仓库、不与备份同介质存放，且恢复演练时真的能用它解开一份备份 |
| 6 | **审计留存由部署方自己接住** | 审计是 JSON lines 写到 **stderr**，没有内置文件落地、轮转与留存期；console 重启或日志被截断即丢失 | `journalctl -u <unit> \| grep '"msg":"audit"'`（或你的日志文件）；确认有轮转与留存期，且每行含 subject/source/method/route/object/status/outcome/zone scope |
| 7 | **把"重启即全员登出"写进运维预期** | session 存在内存 map 里，console 重启后所有浏览器登录失效；依赖 session 的自动化会在重启后集体失败 | 重启 console，确认旧 session 请求得到 401；自动化改用 Bearer token（第 4 条） |
| 8 | **snippet 共享目录只导出给 PVE 节点** | 该目录是 `0755`、文件是 `0644`，其中含**一次性 bootstrap token** 与公开 CA（无私钥）。机密性完全落在"谁能挂载这个 share"上，文件权限不提供保护 | 检查导出配置（NFS/SMB/PVE storage）的允许客户端列表只含 PVE 节点；确认它没有被挂进通用共享 |
| 9 | **bootstrap 只能发生在可信网段内** | 首次 bootstrap 时 console 拨号**不认证对端**（无可 pin 的 CA/角色，`InsecureSkipVerify`），一次性 token 是唯一认证；同网段第三方若能读到 token 就能冒充 agent | 确认 bootstrap 期 console 与目标节点处在可信二层/网段；token 用后即失效，但仍按 secret 处理（见第 8 条） |
| 10 | **反代要么不挂，要么接受它的两个后果** | console 不信任任何代理头（`SetTrustedProxies(nil)`），`ClientIP` 是直连对端：挂反代后**所有请求共用一个限流桶**，审计来源只剩反代地址 | 从两个不同客户端各打一次接口，看审计里的 `source` 是否相同；相同即说明反代在中间，需要在反代侧限流与留痕 |
| 11 | **反代必须关闭响应缓冲** | job 日志是 SSE：5 分钟上限、每事件 30 秒写窗口，console 已发 `X-Accel-Buffering: no`；反代若缓冲响应，流式会退化成"跑完一次性返回" | 起一个长 job，观察日志是否逐行到达；nginx 需 `proxy_buffering off` |
| 12 | **备份/恢复按灾难恢复文档的判据执行** | 恢复判据是 `/health`；磁盘满、只读文件系统或库文件丢失必须让它变红，否则"恢复了"只是进程起来了 | 按[灾难恢复](disaster-recovery.md)跑一次恢复：`/health` 必须为 `healthy`，且要能真的提交一个 job。注意该探测的强度在 M1-14 落地后才成立（此前 `PingContext` 不碰库文件，磁盘满也报 healthy） |
| 13 | **升级与回滚按固定顺序** | console 二进制、web 资源、agent deb 有兼容边界；数据库 schema 前向单向，回滚等于恢复备份 | 升级/回滚 runbook **尚未成文**；在那之前按 [runtime-defaults](runtime-defaults.md) 与 schema 版本规则操作，并先备份 |

## 2. 已知暴露面（登记在案，不隐藏）

这些是当前实现里**已经知道、但本轮不修**的暴露面。写在这里是为了让部署决定建立在事实上，
而不是只留在代码注释或抑制理由里：

- **一次性 bootstrap token 落在 `0644` 文件**（要求 8）：文件权限不提供保护，安全性等于共享目录的导出策略。
- **bootstrap 不认证对端**（要求 9）：没有可 pin 的身份，一次性 token 是唯一认证。
- **审计无轮转与留存**（要求 6）：`internal/audit` 只有一个 `io.Writer` 记录器。
- **session 存内存**（要求 7）：重启即失效；这是运维预期，不一定要改。
- **明文 HTTP 是默认值**（要求 1）：TLS 是可选配置，默认关闭。

## 3. 与其它文档的关系

| 想知道什么 | 看哪里 |
|---|---|
| 各控制自身怎么实现（撤销源、Exec/FileCopy 边界、provider 身份） | [P0 安全控制](security-controls.md) |
| 密钥、证书、轮换的具体保管与操作 | [凭据与轮换](credential-vault.md) |
| Zone/VM/整机销毁时凭据怎么处理 | [凭据销毁](credential-destruction.md) |
| 故障域、备份格式、恢复步骤与 schema 规则 | [灾难恢复](disaster-recovery.md) |
| 限流/超时/退避等默认值与它们的 `文件:行号` | [运行期默认值](runtime-defaults.md) |

## 4. 尚未闭合的部分

- 要求 6 的**实现**（审计落文件或库 + 轮转 + 留存策略）还没做；本文只固定"部署方必须自己接住 stderr"。
- 要求 13 依赖的升级/回滚 runbook 尚未成文；备份调度与留存策略同样待补。
- 要求 12 的探测强度依赖 M1-14（`/health` 真实读写探测 + 调度器心跳）合入；在那之前恢复判据只看进程是否起来。
- 真机验收（真机 lifecycle 冒烟、Exec/FileCopy 往返、suspend/resume 持久性）在本机没有 dom0 入口，
  因此本文清单目前只在"代码行为"层面可核对，未在真机复核。
