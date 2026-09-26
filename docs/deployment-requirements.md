# 生产部署安全要求

更新：2026-09-26。本文是"单操作者自用生产"（A 档）的**部署硬要求与核对清单**：它规定部署方
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
| 6 | **完整、长期、防篡改的审计留存由部署方接住** | 审计是 JSON lines 写到 **stderr**，这是唯一逐条完整的记录。console 另在库里（`audit_events`）保留 90 天，但那是有上限的副本：限流拒绝（429）与未认证且未成功的请求被抽样、超出只剩汇总计数，每类行数有硬上限，且库与 console 同机、可被改写（[安全控制](security-controls.md)“持久化审计”）。不接住 stderr，就没有超过 90 天、逐条完整或防篡改的审计 | `journalctl -u <unit> \| grep '"msg":"audit"'`（或你的日志文件）；确认有轮转与留存期（最好转发到异地），且每行含 request_id/authenticated/auth_method/auth_disabled/subject/source/method/route/object/object_truncated/status/outcome/zone scope；被拒绝的变更请求（401/403 等）同样有记录。库内副本：`sqlite3 <db> "SELECT COUNT(*), MIN(datetime(occurred_at/1e9,'unixepoch')) FROM audit_events"`，`/health` 的 `audit_trail` 应为 `ok` |
| 7 | **把"重启即全员登出"写进运维预期** | session 存在内存 map 里，console 重启后所有浏览器登录失效；依赖 session 的自动化会在重启后集体失败 | 重启 console，确认旧 session 请求得到 401；自动化改用 Bearer token（第 4 条） |
| 8 | **snippet 共享目录只导出给 PVE 节点**（仅适用于共享存储投递，即配置了 `agent_snippet_datastore`） | 该目录是 `0755`、文件是 `0644`，其中含**一次性 bootstrap token** 与公开 CA（无私钥）。机密性完全落在"谁能挂载这个 share"上，文件权限不提供保护 | 检查导出配置（NFS/SMB/PVE storage）的允许客户端列表只含 PVE 节点；确认它没有被挂进通用共享 |
| 9 | **一次性 bootstrap token 按 secret 保管** | 首次 bootstrap 时 console 按 token 派生的公钥 pin 认证 agent 的占位监听（G-H11），没有 token 的同网段第三方冒充不了 agent；但**读到 token 的人**能派生同一把占位密钥、也能兑换 token，所以 token 的机密性仍是这一步的全部前提 | token 的每一份副本（snippet、cloud-init 盘）都按第 8 条与下方“已知暴露面”收口；token 用后即失效、默认 1 小时过期 |
| 10 | **反代要么不挂，要么接受它的两个后果** | console 不信任任何代理头（`SetTrustedProxies(nil)`），`ClientIP` 是直连对端：挂反代后**所有请求共用一个限流桶**，审计来源只剩反代地址 | 从两个不同客户端各打一次接口，看审计里的 `source` 是否相同；相同即说明反代在中间，需要在反代侧限流与留痕 |
| 11 | **反代必须关闭响应缓冲** | job 日志是 SSE：5 分钟上限、每事件 30 秒写窗口，console 已发 `X-Accel-Buffering: no`；反代若缓冲响应，流式会退化成"跑完一次性返回" | 起一个长 job，观察日志是否逐行到达；nginx 需 `proxy_buffering off` |
| 12 | **备份/恢复按灾难恢复文档的判据执行** | 恢复判据是 `/health`；磁盘满、只读文件系统或库文件丢失必须让它变红，否则"恢复了"只是进程起来了 | 按[灾难恢复](disaster-recovery.md)跑一次恢复：`/health` 必须为 `healthy`，且要能真的提交一个 job。注意该探测的强度在 M1-14 落地后才成立（此前 `PingContext` 不碰库文件，磁盘满也报 healthy） |
| 13 | **升级与回滚按固定顺序** | console 二进制、web 资源、agent deb 有兼容边界；数据库 schema 前向单向，回滚等于恢复备份 | 按[升级与回滚](upgrade-rollback.md)执行：§3 的顺序（先备份，再控制台，再 agent）与 §3.1 的核对，回滚按 §4 区分 schema 是否已升；该 runbook 还没有在真机上演练过（其 §6） |

## 2. 已知暴露面（登记在案，不隐藏）

这些是当前实现里**已经知道、但本轮不修**的暴露面。写在这里是为了让部署决定建立在事实上，
而不是只留在代码注释或抑制理由里：

- **一次性 bootstrap token 落在 `0644` 文件**（要求 8）：只剩共享存储投递路径如此。文件权限不提供保护，安全性等于共享目录的导出策略；能否收紧取决于 share 类型与导出设置，需要部署方决定。默认的 SSH 上传路径现在把 snippet 写成 `0600`，前提是节点上的 `/var/lib/vz/snippets` 只有 SSH 登录名可写、没有默认 ACL：否则另一个账号可以抢先在临时文件路径上放置 FIFO，默认 ACL 也会让 `umask` 失效（两项的真机复核都见[验收清单](acceptance-real-machine.md)步骤 9）。改动前上传的旧文件仍是 `0644`，要到该 qube 下次 resume 或重新 provision 才会被替换。可以在节点上用 `find /var/lib/vz/snippets -maxdepth 1 -name 'qubes-air-*' -perm -o=r` 列出这些文件，再手工 `chmod 600`。
- **token 也在 VM 的 cloud-init 盘里**：PVE 把 user-data 生成进 compute VM 的 cloud-init 盘，guest 正是从这里读到 token。能读取这个卷的人，以及有权通过 PVE API 读取该 VM cloud-init 数据的账号，都能看到它。文件权限管不到这份副本，暴露窗口只由单次兑换和默认 1 小时 TTL 限定。
- **bootstrap 的对端认证完全建立在 token 上**（要求 9）：console 用 token 派生的公钥 pin 认证 agent，读到 token 即可冒充；这一步不再依赖网段可信，但依赖 token 不外泄。
- **库内审计是有界副本**（要求 6）：`audit_events` 保留 90 天、每类有行数上限，429 与未认证且未成功的请求被抽样；逐条完整与防篡改的留存仍只有 stderr 日志。
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

- 要求 6 的库内部分已实现（schema 4 的 `audit_events`，90 天留存，见[安全控制](security-controls.md)“持久化审计”）；异地或防篡改归档没有实现，仍由部署方转发 stderr 日志。
- 要求 13 依赖的升级/回滚 runbook 尚未成文；备份调度与留存策略同样待补。
- 要求 12 的探测强度依赖 M1-14（`/health` 真实读写探测 + 调度器心跳）合入；在那之前恢复判据只看进程是否起来。
- 真机验收（真机 lifecycle 冒烟、Exec/FileCopy 往返、suspend/resume 持久性）在本机没有 dom0 入口，
  因此本文清单目前只在"代码行为"层面可核对，未在真机复核。
