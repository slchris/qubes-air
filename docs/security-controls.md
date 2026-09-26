# P0 安全控制与部署配置

更新：2026-09-20。本文描述本轮代码改动；真机部署与恢复演练仍需单独验收。
这次改变了 agent 必填配置和 Exec 请求格式，不保留 shell 文本兼容入口。

## Agent 身份与吊销

Agent 服务端在每次 TLS 握手（包含恢复会话）校验 CA、ClientAuth 用途、证书有效期及
Relay/Console 角色。角色校验不依赖 CertRegistry 是否存在。Console/Relay 客户端仍校验
目标 agent 的证书角色与名称；本地 dom0 policy 和服务 allowlist 继续生效。

动数据盘密钥与首次身份的服务还要求专用的 console 身份：`qubesair.UnlockData` /
`RekeyData` 只对 CN 为 `console-unlock`、`qubesair.BeginBootstrap` / `CompleteBootstrap` 只对
`console-bootstrap` 的 console 角色证书执行；其它 CA 签发的 Relay/Console 证书（探测、续期、
relay-call）在进入 invoker 前被拒。

实际 agent 启动入口必须配置 `--revocation-url`；打包 unit 从
`QUBESAIR_REVOCATION_URL` 传入。Console 用以下配置把地址写进 cloud-init：

```yaml
orchestrator:
  enabled: true
  agent_revocation_url: https://console.example/pki/revocations
```

也可使用 `QUBES_AIR_AGENT_REVOCATION_URL`。地址必须能从 guest 访问；没有地址时，启用真实
编排的 Console 拒绝启动，agent 本身也拒绝缺少此配置的启动。旧 agent.env 需先由部署源更新，
不能在缺少配置时直接替换二进制。本仓库不自动修改 qubes-salt-config 或真实机器。

### 公共签名状态接口

`GET /pki/revocations` 不使用 Bearer token，只返回 CA 签名的撤销指纹列表与签发/过期时间。
它不返回姓名、Qube ID、原因、凭据或私钥，也不创建或轮换 CA。没有现成 CA 或查询失败时返回
503，不生成空列表冒充正常状态。

- 请求体上限为 0；非空或未知长度请求体返回 413。
- 服务端处理 deadline 为 5 秒，按客户端限制 5 次/秒、burst 10；超限返回 429。
- 最多发布 10,000 个未过期撤销指纹，响应不超过 1 MiB；超量明确失败，不截断列表。
- 响应带 `Cache-Control: no-store`；审计记录公开主体、来源、route/method、status 和时延，
  不记录请求体、响应内容或内部错误详情。

Agent 用已有公共 CA 校验精确签名数据。HTTPS 仍校验服务器证书；也允许无凭据的 HTTP 分发，
因为身份依据是 CA 签名。两种方式都拒绝重定向。HTTP 分发不能防止阻断或有效期内的旧快照重放。

成功读取缓存最多 15 秒，快照有效期最长 5 分钟，允许 30 秒时钟偏差；进程内拒绝时间倒退。
重启后重新取状态，没有持久化的“默认允许”列表。刷新失败、坏签名、过期、超量或回退状态均
拒绝授权，不回退到旧缓存。需要保持 guest 时钟准确。

长连接每分钟重新检查指纹和证书有效期，吊销/失效会取消调用并结束空闲读取。
正常状态更新的生效受 15 秒缓存和一分钟巡检间隔限制；被阻断或重放时按签名有效期和巡检
间隔收敛，不能宣称即时吊销。签名源必须从可信数据库读取完整状态。

这是撤销列表，不是远端完整注册表：CA 签发且角色合法、未列为撤销的临时 Console 证书仍可
使用。Console 临时探测证书不逐张登记；CA 泄露、逐对象授权、备份恢复后的撤销历史一致性
需要单独处置，见 [TODO](TODO.md)。

## Bootstrap 首次连接：token 派生公钥 pin

尚未拿到证书的 agent 只能出示自签名占位证书。占位证书的密钥由一次性 token 与 qube 名经
HKDF-SHA256 派生（Ed25519，`pki.NewBootstrapPlaceholderCertificate`）；console 签发 token 时
算出对应公钥的 SPKI SHA-256，存进 `bootstrap_tokens.placeholder_spki_sha256`，token 本身只存哈希。

console 拨号时（`pki.BootstrapDialTLSConfig`）在每次握手的 `VerifyConnection` 里校验：pin 一致、
CN 为 `bootstrap-<qube>`、在有效期内、只有 digitalSignature + ServerAuth、自签名、恰好一个
agent 角色。任一不符即握手失败，console 不发出任何请求，所以冒充者既拿不到调用、也换不到
证书。agent 一侧仍要求客户端证书链到 cloud-init 下发的 CA，双向在第一帧之前都已认证。

- 没有 pin 就不拨号：未装配 pin provider、没有未兑换且未过期的 token、升级前签发的旧 token
  （pin 为空）都报 `not_configured`，原因写明“需要重新 provision”，不回退到不认证的握手。
  查 pin 本身失败（数据库错误）或库里的 pin 不是合法的 SHA-256（行损坏）报 `console_failed`：
  同样不拨号，但属于控制台侧故障，按常规退避重试，不记到 VM 头上。
- agent 在会话里交出的 token 必须正是派生本会话 pin 的那一枚，**在兑换之前**比对：通过了握手
  却交出另一台 qube 的 token（被盗或 user-data 混用）时报 `refused`，那枚 token 不被兑换、
  不签发也不下发任何证书。
- 读到 token 的人能派生同一把密钥，这与他能兑换 token 是同一个能力；token 的暴露面见
  [生产部署安全要求](deployment-requirements.md)第 8、9 条。
- 升级顺序：agent deb 必须先于 console 升级，在途 token 需重新 provision，见
  [升级与回滚](upgrade-rollback.md) §3。

## Console API 对象级授权

`auth.tokens[*].zones` 给命名 token 增加对象级白名单。`api_token` 与未写 `zones` 的 token 是
fleet-wide；浏览器 session 继承登录 token 的 `zones`，因此浏览器、CLI 与 MCP 走同一套服务端
判定，没有各自的旁路。

```yaml
auth:
  api_token: <管理员, fleet-wide>
  tokens:
    - name: zone-a-control
      token: <...>
      scope: control
      zones: [<zone-a-id>]
    - name: auditor
      token: <...>
      scope: read-only
```

判定规则：

- 白名单内对象的 `zones/:id`、`qubes/:id`（含 start/stop/release/purge）以及 job 详情/日志
  放行；其他 Zone 的对象与不存在的对象都返回 404，不泄露 ID 是否存在。
- 创建 Qube 时请求体的 `zone_id` 必须在白名单内，否则 403；创建 Zone 是 fleet 操作。
- `credentials`、`infrastructure`、`settings`、`monitoring`、`billing`、`status` 和 job 汇总
  列表是 fleet 端点，zone token 一律 403（不做半真半假的过滤视图）。
- `GET /zones` 与 `GET /qubes` 在查询层按白名单过滤，只返回可见对象。
- 所属关系无法解析（数据库故障、body 不可解析或超限）时失败关闭，不回退为放行。
- 审计记录 `subject` 与 `zone_scope`（`fleet`、ID 列表，未认证为 `none`，鉴权关闭为 `unrestricted`）；
  被拒绝的变更请求同样入库，见下文“Console API 审计”。
- `GET /session` 对任何已认证 scope 开放，只返回凭据解析出的 `subject`、`scope` 与 `zones`
  （fleet-wide 为空数组），带 `Cache-Control: no-store`（登录 `POST /session` 同样），不回显 token、session ID 或 cookie；
  未认证 401。它只报告、不授予任何权限，供 UI 判断哪些视图会被拒绝；GET 按设计不写审计。
- UI 侧可见性降级（显示层，判定仍在服务端）：确认 scope 之前不渲染控制台外壳；zone-scoped
  session 的 jobs/credentials/billing/monitoring 导航置灰，落到这些视图时回到 dashboard，
  dashboard 不请求也不显示 job 汇总，settings 只保留登录/登出。

边界：这是对象级隔离，不是完整多租户。fleet 端点对 zone token 整体不可用；没有 API 可以
扩大或缩小 token 的授权。`zones` 只接受精确 ID，`"*"` 会被配置校验拒绝。

## Console API 审计

审计中间件是 `/api/v1` 链的最外层，顺序为 Audit → BodyLimit → ScopedAuth → RateLimit →
RequireControl → RequireZones（`cmd/server/main.go` 的 `apiMiddleware`）。它在整条链跑完后
才读取身份，所以认证层解析出的主体仍能归属到记录上，而被任何一层拒绝的请求也都会留下记录。

- 每个变更请求（GET/HEAD/OPTIONS 以外的方法，命中已注册路由）恰好写一行 JSON 到 stderr，
  无论成功还是被拒绝。唯一的例外是 handler panic：panic 越过审计中间件，由最外层的 gin
  Recovery 返回 500 并自行记录 panic，这个请求没有审计行。
- `outcome: denied`：认证失败（缺少、格式错误或未知的 Bearer，未知或过期的 session，登录
  token 错误）返回 401；只读 scope 发起变更请求返回 403；zone 判定拒绝返回 403 或 404。
  zone 判定对调用方回 404 以免泄露对象是否存在，审计里仍记为 `denied`。zone token 创建 Qube 时
  请求体读不出、无法解析或超过上限，按失败关闭返回 403，同样记为 `denied`；所属关系查询出错
  返回 500，记为 `error`。
- 限流拒绝（429）记为 `client_error`，不记为 `denied`：节流不是授权判定，把它混进 `denied`
  会冲淡运维按 `denied` 排查越权的结果。`status: 429` 已足以区分。
- 字段：`request_id`、`authenticated`、`subject`、`zone_scope`、`source`、`method`、`route`、
  `object`、`object_truncated`、`status`、`outcome`、`latency_ms`。
- 没有解析出凭据的请求（认证失败、登录请求）记为 `authenticated: false`、`subject: anonymous`、
  `zone_scope: none`，不会被写成 fleet 范围。鉴权关闭（没有配置任何 token）时请求同样记为
  `anonymous`，但它不会被拒绝、能触达所有 zone，所以记为 `zone_scope: unrestricted`。判断是否
  认证以 `authenticated` 为准，名为 `anonymous` 的 token 不会与之混淆。
- `object` 取路径参数 `:id`（没有时取 `:app`），最多保留前 128 字节，按 UTF-8 字符边界截断；
  截断时 `object_truncated: true`。
- `request_id` 由服务端生成（128 位随机），同一个值通过响应头 `X-Request-Id` 返回给调用方；
  客户端自带的 `X-Request-Id` 不采信，也不写入审计。
- 记录不包含任何请求头或请求体：Authorization、Bearer token、session cookie 以及登录请求体里的
  token 都不会进入审计。`source` 取直连对端地址，不信任代理头。
- 读请求不进审计，被拒绝的读请求也一样：读不改变状态，浏览器轮询产生的大量记录会淹没变更记录。
  gin 访问日志仍逐条记录每个请求的方法、路径、状态和来源。

限制：

- 单行长度有上界。审计在认证之前运行，未认证调用方能自由决定的只有 `object`（路径参数）；其余
  字段由服务端决定：已注册路由的方法和模板、配置里的 token 名和 zone 列表、连接的对端地址。
  `object` 截断到 128 字节，JSON 转义最多把 1 个字节变成 6 个，其余字段合计约 400 字节（构造的最坏情况实测 416 字节），所以
  一个未认证请求写出的审计行不超过 2 KiB（`TestAPIAuditBoundsOversizedObject` 按这个上界断言），
  远低于 journald 默认的单行上限（`LineMax=48K`），不会被拆成非 JSON 片段。已认证请求的行长还
  取决于配置里 zone 列表的长度，由管理员控制。
- 限流在认证之后，被认证拒绝的请求到不了限流器，所以这类变更请求不受限流，每次都会写一行审计
  （登录接口不做 Bearer 校验，仍按来源地址限流）。写入条数与访问日志同量级，每行受上一条的上界
  约束；做持久化审计（M2-2）之前需要重新评估写入量。

审计留存仍由部署方负责，见[生产部署安全要求](deployment-requirements.md)第 6 条。

## Exec：JSON 参数列表

stdin 必须是 JSON 字符串数组，第一项为已允许的规范绝对可执行文件路径：

```bash
printf '%s\n' '["/usr/bin/id","-u"]' |
  qrexec-client-vm <remotevm> qubesair.Exec
```

必须显式启用 `QUBESAIR_ALLOW` 中的 Exec，并在 `QUBESAIR_EXEC_ALLOW` 中允许该可执行文件。两个白名单都由 console 随 qube 下发到 guest 的 `agent.env`：服务白名单来自 `agent_allowed_services`，路径白名单来自 `agent_exec_allow` / `agent_filecopy_roots`（或环境变量 `QUBES_AIR_EXEC_ALLOW` / `QUBES_AIR_FILECOPY_ROOTS`，冒号分隔）。console 在启动配置校验和渲染 cloud-init 时各校验一次整组授权（`qrexec.ValidateAgentGrants`）：服务名必须过传输层的名字白名单（不含逗号、空白与控制字符，否则会把一项拆成两项或在 `agent.env` 里多写一行）且不重复；路径必须是绝对、规范化、不含冒号与任何控制字符（含制表符、DEL）的路径且不重复。所以写错的值在启动或 provision 阶段就失败，而不是变成 guest 里一次被拒的调用。路径表不空、但服务白名单里没有对应的 `qubesair.Exec` / `qubesair.FileCopy` 时，agent 会整体拒绝该服务，这组路径什么也不授予：console 只在启动日志（`WARNING: agent grants:`）和渲染日志里告警，不拒绝启动。
默认仍只启用 Ping。限制为 64 KiB 请求、1～128 个参数、每项最多 4096 UTF-8 字节，禁止 NUL、
非法路径和服务 `+argument`。错误分别以非零退出码上报；stdout/stderr/exit code 保持独立。

服务直接 `execve` 一个程序，不运行 shell，不做变量替换、命令拼接、重定向或 systemd-run。
参数中的 shell 字符只作为普通参数。程序继承 agent 的沙箱，不能依靠该服务绕过 ProtectSystem、
ProtectHome 或 PrivateTmp。允许的程序自身若支持执行代码或修改系统，仍拥有对应能力；
不要把批准一个解释器、包管理器或通用管理命令误当成只批准安全子命令。

## FileCopy：同一沙箱内的目录边界

保留 `push <absolute-path>\n<body>` / `pull <absolute-path>\n` 协议。
必须显式启用服务与 `QUBESAIR_FILECOPY_ROOTS`；拒绝根目录 `/` 作为允许范围。

所有路径组件通过目录文件描述符和 O_NOFOLLOW 打开，在同一命名空间里校验并读写。
不跟随目录或读取目标符号链接；push 对最终符号链接执行原子替换，不写其指向的文件。
配置根目录自身也不得经过符号链接，应使用其真实路径并保证目录权限可信。

请求头最多 4096 字节，push/pull 文件最多 16 MiB。push 先验证有界内容，再创建 0600 临时文件、
同步并原子替换；超量输入不覆盖原文件。pull 只接受普通文件。目录不存在、沙箱禁止访问、
符号链接或读写失败均返回非零退出码。PrivateTmp 下的 `/tmp` 是 agent 私有视图，持久数据验收
应使用明确允许且可访问的数据挂载目录。

包依赖新增 Python 3。服务测试通过 Go 测试套件运行，测试环境也需要 Python 3 和完整仓库目录。

## 数据盘迁移：RekeyData

`qubesair.RekeyData` 把仍由旧 master 派生密钥加密的数据盘原子换成该 Qube 自己的随机
DEK。它只在首次解锁旧盘时由 Console 调用，请求是两个 base64 密钥的 JSON
（`{"old":...,"new":...}`），经与 UnlockData 相同的、按 `agent-<qube>` 固定对端的验证通道
下发；密钥只经 stdin 和进程替换传递，不进入 argv、不写盘，特权部分同样经 systemd-run。

- 不格式化、不覆盖：非 LUKS 盘、缺盘、两把钥匙都打不开时拒绝并上报原因。
- 顺序不可逆：先 `luksChangeKey` 原子替换；失败才退回“先 add 新键、验证新键可开、再
  remove 旧键”。任何失败路径都保留旧键可用，不会把数据锁死。
- 幂等：新键可用且旧键已不可用时直接报告成功，不碰容器。
- 旧 keyslot 未能移除时报告 `old_key_removed:false`，Console 写入迁移标记，之后每次解锁
  都重试删除；在删除成功前该盘仍可被 master 打开。
- 服务与 UnlockData 一样必须在 `QUBESAIR_ALLOW` 中显式启用，并新增 Python 3 解析依赖。
- 请求按原始字节计数，连同末尾换行最多 8192 字节，超出报 `oversize`，含 NUL 报 `malformed`；
  不会先去掉换行或 NUL 再计数，因而超量请求不会被截短后照常使用。
- 两个服务都拒绝 `+argument`（RekeyData 报 `bad_argument`），特权半段还要求外层经
  `systemd-run --setenv` 传入的标记（`QUBESAIR_REKEY_INNER` / `QUBESAIR_UNLOCK_INNER`），
  调用方无法设置它。此前 `qubesair.RekeyData+__rekey` 会跳过外层全部校验，持有有效密钥的
  调用方把它同时作为 old 和 new 就会删掉唯一的 keyslot；`qubesair.UnlockData+__unlock` 会
  跳过空口令检查和 systemd-run。

升级要求：加密 Qube 的 agent 在下次解锁前必须允许 `qubesair.RekeyData`，否则迁移失败、
数据保持加密并在下次 resume 重试。迁移完成后 `qubes-air-luks-master` 只是只读的迁移材料，
可核验无未迁移盘后删除；master 丢失会使未迁移盘无法解锁，也不会自动重建。

## qrexec 服务脚本的测试

AGENTS.md §6 要求每个 shell/qrexec 服务覆盖空输入、非法 service/path/argument、超量输入输出
和非零退出。服务脚本按所在套件分工，`scripts/test-qrexec-services.sh` 的最后一个用例检查
`remote/qubes-rpc/`、`console/qrexec/`、`relay/transport/` 下的每个文件恰好归属一个套件：

| 服务 | 测试 |
|---|---|
| `qubesair.GrpcProxy`、`qubesair.ConnectTCP`、`qubesair.IssueRelayCert`、`qubesair.RemoteEndpoints`、`qubesair.UnlockData`、`qubes.GetAppmenus`、`qubes.StartApp` | `make qrexec-test`（`scripts/test-qrexec-services.sh`） |
| `qubesair.Exec`、`qubesair.FileCopy`、`qubesair.Ping`、`qubesair.RekeyData` | `console/backend/internal/agent/*_service_test.go`、`invoker_test.go` |
| `qubesair.SSHProxy` | **未覆盖**：它对 target 和 service 不做字符白名单（只靠 qrexec 自身的参数字符限制），service 经 ssh 在远端由 shell 解析；删除还是加固尚未决定 |

shell 套件在 `env -i` 与只含桩命令和少量无害工具的 PATH 下执行服务副本，宿主机上真实的
cryptsetup、mount、systemd-run、qubesdb-read 不可达。桩命令记录每次调用的 argv、环境和 stdin，
所以用例能断言被拒绝的请求没有执行任何命令、口令不出现在 argv 里、3 MiB 输入输出逐字节透传。
服务里写死的绝对路径（`/usr/local/bin` 辅助程序、`/dev` 下的数据盘、`/data`）只在测试副本里
改写到用例目录；改写没有命中就中止，不会退回真实系统路径。

包装脚本不读 stdin 时，输入上限由它调用的程序负责：`relay-call`、`issue-relay-cert` 目前用
`io.ReadAll` 读取 stdin，没有自己的上限。

## Proxmox 管理连接

资源执行器和容量调度器共用 HTTPS 客户端：拒绝明文 HTTP、URL 凭据、query/fragment 与所有
重定向；TLS 验证 CA、服务器用途、目标主机名和有效期，不再提供 InsecureSkipVerify 开关。

公共 CA 使用系统信任库；私有 CA 可写入 Zone 的 `config.proxmox.ca_pem`（只含公共证书）。
endpoint 的主机名或 IP 必须存在于服务器证书 SAN，不能用关闭验证修复名称不匹配。

SSH 上传配置：

```yaml
orchestrator:
  proxmox_ssh_key_file: /path/to/node-client-key
  proxmox_ssh_known_hosts_file: /path/to/verified-known_hosts
```

对应环境变量为 `QUBES_AIR_PROXMOX_SSH_KEY_FILE` 和
`QUBES_AIR_PROXMOX_SSH_KNOWN_HOSTS_FILE`。文件中的节点地址必须匹配实际连接的管理 IP/名称；
主机公钥应通过可信渠道核验，不能把未验证的扫描结果直接作为信任依据。
缺少信任文件、未知/错误/撤销的主机密钥会失败；握手受超时与取消控制。
