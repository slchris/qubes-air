# P0 安全控制与部署配置

更新：2026-09-20。本文描述本轮代码改动；真机部署与恢复演练仍需单独验收。
这次改变了 agent 必填配置和 Exec 请求格式，不保留 shell 文本兼容入口。

## Agent 身份与吊销

Agent 服务端在每次 TLS 握手（包含恢复会话）校验 CA、ClientAuth 用途、证书有效期及
Relay/Console 角色。角色校验不依赖 CertRegistry 是否存在。Console/Relay 客户端仍校验
目标 agent 的证书角色与名称；本地 dom0 policy 和服务 allowlist 继续生效。

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
- zone token 可以 `PUT /zones/:id` 改名称和放置默认值（node、datastore、模板、bridge），但不能改
  决定凭据发往何处的字段：`config.endpoint`、`config.proxmox.credential_id`、
  `config.proxmox.ca_pem`、`config.gcp.credential_id`、`config.gcp.identity_bucket`、
  `config.gcp.service_account_email`。改动任一项返回 403，审计记为 `denied`。否则一个 zone 的
  操作员就能把控制台管理的 provider 凭据引到自己的服务器上（G-D9）。这项检查在凭据引用校验
  之前，所以不能拿它探测别的凭据 ID 是否存在。
- 创建或更新 Zone 时，`credential_id`（Proxmox 与 GCP）必须指向一条运维凭据。不存在的 ID 和
  控制台自有行（见下文“Console API 凭据”）返回同一个 422
  （`credential_id does not name a stored credential`），控制台行在审计里记为 `denied`。
- `credentials`、`infrastructure`、`settings`、`monitoring`、`billing`、`status` 和 job 汇总
  列表是 fleet 端点，zone token 一律 403（不做半真半假的过滤视图）。
- `GET /zones` 与 `GET /qubes` 在查询层按白名单过滤，只返回可见对象。
- 所属关系无法解析（数据库故障、body 不可解析或超限）时失败关闭，不回退为放行。
- 审计记录 `subject` 与 `zone_scope`（`fleet`、ID 列表，未认证为 `none`，鉴权关闭为 `unrestricted`）；
  被拒绝的变更请求同样入库，见下文“Console API 审计”。

边界：这是对象级隔离，不是完整多租户。fleet 端点对 zone token 整体不可用；没有 API 可以
扩大或缩小 token 的授权。`zones` 只接受精确 ID，`"*"` 会被配置校验拒绝。Zone 更新是先读后整体
替换，没有事务：zone token 的更新与 fleet 对同一 zone 的并发修改相撞时，可能把连接字段写回它读到
的旧值。那仍是 fleet 设过的值，不会是 zone token 自己选的。

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
  返回 500，记为 `error`。凭据 API 对控制台自有行的 404 和对保留命名空间的 403 同样记为
  `denied`，见下文“Console API 凭据”。
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

## Console API 凭据：控制台自有行不经 API

`credentials` 表存两类行。一类是运维方的 provider 凭据，zone 通过 `credential_id` 引用它们，
凭据 API 就是为管理它们而设。另一类是控制台自己的密钥：agent CA 证书与私钥
（`qubes-air-ca-cert`、`qubes-air-ca-key`）、只供旧盘迁移读取的 `qubes-air-luks-master`、
每个 Qube 的 DEK（`qubes-air-luks-key-<id>`）和迁移标记（`qubes-air-luks-legacy-slot-<id>`）。
两类行共用 keyring 和轮换工具，但后者不是运维对象：拿到 CA 私钥就能签发任意 agent 身份，
删掉一把 DEK 就是绕开 purge 流程的 crypto-shred。修复前凭据 API 不做区分：列表连同 ID 返回这些行，
`PUT`/`DELETE` 能改名或删除它们，`POST` 还能用控制台的名称建行，让控制台加载调用方给的 CA
或 DEK（见 G-D8）。

判定规则只定义在一处，即 `internal/models/credential_internal.go` 的 `IsConsoleCredential`：
类型为 `pki`，或名称以 `qubes-air-` 开头的行属于控制台。控制台写入的每一行两个条件都满足。
类型条件兜住将来某个没放进命名空间的新行；名称条件兜住会顶替控制台密钥的运维行，因为控制台
只按名称查找自己的密钥。控制台的查找、purge 删除 DEK 和迁移标记的读写都用同一个比较
`models.MatchesConsoleName`：忽略首尾空白，再按 Unicode 简单大小写折叠（`strings.EqualFold`）
比较。`IsConsoleCredential` 用同样的规则逐个字符比较前缀，所以凡是这个比较会当作控制台名称的
名称，都在保留范围内：`QUBES-AIR-CA-KEY`、用 `ſ`（U+017F，折叠为 `s`）或 `K`（U+212A，折叠为
`k`）拼出的名称都算。反过来，`strings.ToLower` 会把 `İ`（U+0130）变成 `i`，但大小写折叠不会，
所以 `qubes-aİr-luks-key-<id>` 是运维行；purge 以前用 `ToLower` 比较会把它一并删掉，现在不会，
同时会删掉所有对得上 DEK 或迁移标记名称的行，包括变体。

- `GET /credentials` 只列出运维行，`total` 也只计这些行。
- `GET`、`PUT`、`DELETE /credentials/:id` 指向控制台行时，返回与不存在的 ID 相同的状态码和
  响应体（404，`{"error":"Credential not found"}`），行不做任何改动。审计把这类变更请求记为
  `denied`，而普通的不存在 ID 记为 `client_error`：运维方能看到这次尝试，调用方分不出两者。
- `POST /credentials` 的名称或类型落在保留范围内，或者 `PUT` 要把运维行改名进保留范围，返回
  403，不写入任何内容，审计记为 `denied`。改名检查在查找 ID 之前，所以答复与 ID 指向什么无关。
- 对不存在的 ID 做 `PUT`、`DELETE` 现在返回 404（此前是带内部错误文本的 500）。存储故障返回
  通用的 500（`{"error":"Internal Server Error"}`），细节只写服务端日志。
- `credentials` 仍是 fleet 端点：zone token 访问任何凭据路由（包括控制台行的 ID）一律 403；
  只读 token 的变更请求返回 403。两者的变更请求都记为 `denied`。
- 控制台自己的路径不经过这个视图：CA 的加载与首次创建、DEK 的生成与读取、迁移标记、purge 的
  crypto-shred（`DataKeyManager.DeleteDataKey`）都直接使用 repository；zone 按 `credential_id`
  读取 secret 也一样。

控制台读取自己的密钥时也不再“取最新的同名行”。CA 证书与私钥、DEK、legacy master 的查找，
吊销状态文档，以及 `issue-relay-cert`、`relay-call`、`pingcheck` 三个工具读 CA，都走同一条读取路径
`repository.ConsoleSecret`（读 CA 两半的是 `repository.LoadConsoleCA`，工具只在调用处 `log.Fatal`），
行由 `models.SelectConsoleRow` 选出，名称按 `models.MatchesConsoleName`（忽略首尾空白、Unicode
简单大小写折叠）比较。控制台对每个名称只写一行，名称逐字节等于规范写法、类型恰好是 `pki`。所以只要有第二行
能对上这个名称，或者唯一对上的那一行拼写或类型不对，查找就失败关闭：不读取任何密钥，返回并记录
一条带 `SECURITY: pki:` 前缀的错误，列出涉及行的 ID、名称和类型（不含密钥），最多列 8 行。
这些名称和类型是植入者写的，而这条错误会进日志、进 500 响应体，也能由未认证的
`GET /pki/revocations` 触发，所以每个字段最多引用 64 字节，截断处在引号外注明省略了多少字节，
整条错误不超过 8 KiB（8 行、每行名称 1 MiB 的实测为 3.6 KiB）。同一个名称下的冲突只要没变，
每个进程只记录一次；冲突变了（多了或少了一行）会再记一次。

运维影响：库里存在这样的行时，控制台拒绝加载 CA，也不会在它旁边新建 CA；对应 Qube 的 DEK 既不
读取也不新建。于是签发、续期、吊销状态文档、provision 和数据盘解锁都会失败，直到控制台停止时把
不是它写的那些行离线删除为止，见[升级与回滚](upgrade-rollback.md)的失败模式速查。

测试覆盖：`internal/models/credential_internal_test.go` 覆盖大小写折叠、空白和类型的正反例；
`internal/service/credential_service_test.go` 在真实加密库上覆盖列表过滤、控制台行 ID 的读/改/删
被拒且行不变、保留名称的创建与改名被拒、运维行正常增删改、拒绝之后 CA 与 DEK 与迁移标记仍可
加载且 purge 仍能删除 DEK，以及存储故障不被当成“不存在”。其中
`TestCredentialServiceCannotShadowConsoleSecrets` 是本改动要堵的攻击：去掉创建检查后，经 API
存入的 `Qubes-Air-CA-Cert`/`Qubes-Air-CA-Key` 会成为重启后控制台加载的 CA，预先存入的
`qubes-air-luks-key-<id>` 会被当作该 Qube 的 DEK，测试随即失败；`internal/handler/credential_handler_test.go`
在 HTTP 层逐字节比较控制台行与不存在 ID 的 404，并覆盖 403 与通用 500；
`cmd/server/credentials_api_test.go` 经生产中间件链检查审计 outcome 以及 zone、只读 token 的拒绝。

边界：

- 本改动之前经 API 建的、名称在保留范围内或类型为 `pki` 的运维行，现在从 API 中消失。它们仍在
  库里，被 zone 引用时仍可使用。其中能对上控制台密钥名称的行会让对应查找失败关闭（见上），不会
  被当成真正的密钥。唯一识别不了的是：在控制台写入自己那一行之前，就以完全相同的名称和 `pki`
  类型存进去的一行。这时库里只有这一行，控制台会把它当作自己的。升级前按下文“升级前核查”执行。
- 更换 CA，以及在确认没有未迁移盘后删除 `qubes-air-luks-master`，都不再有 API 路径，只能在控制台
  停止时离线操作数据库；目前没有专用工具。
- zone 的 `credential_id` 不校验是否指向控制台行。调用方要先知道该行的 UUID，而 API 已不再给出。

### 升级前核查：找出不是控制台写的行

适用于从本修复之前的版本升级。修复之前，control scope 的 token 能经 API 在控制台的命名空间里
建行、改名。下面的查询只读，在控制台所在 qube 上对它的库执行（默认
`/rw/config/qubesair/qubes-air.db`，即[升级与回滚](upgrade-rollback.md) §3 备份命令的 `-db`）。
先把 SQL 存成文件，再运行
`sqlite3 -readonly -header -column /rw/config/qubesair/qubes-air.db < console-row-check.sql`：

<!-- console-row-check: TestConsoleRowCheckQueryFlagsPlantedRows 运行下面这段 SQL，改动时同步 -->
```sql
WITH c AS (
  SELECT id, name, type, created_at,
         lower(trim(name, ' ' || char(9, 10, 11, 12, 13))) AS folded
  FROM credentials
  WHERE lower(trim(name, ' ' || char(9, 10, 11, 12, 13))) LIKE 'qubes-air-%'
     OR lower(trim(type, ' ' || char(9, 10, 11, 12, 13))) = 'pki'
     OR length(name) <> length(CAST(name AS BLOB))
)
SELECT id, quote(name) AS name, quote(type) AS type, created_at,
       trim(
         CASE WHEN type IS NOT 'pki'
                OR NOT (name IN ('qubes-air-ca-cert', 'qubes-air-ca-key', 'qubes-air-luks-master')
                        OR (substr(name, 1, 19) = 'qubes-air-luks-key-'
                            AND substr(name, 20) IN (SELECT id FROM qubes))
                        OR (substr(name, 1, 27) = 'qubes-air-luks-legacy-slot-'
                            AND substr(name, 28) IN (SELECT id FROM qubes)))
              THEN 'NOT-CANONICAL ' ELSE '' END
         || CASE WHEN (SELECT count(*) FROM c AS d WHERE d.folded = c.folded) > 1
              THEN 'DUPLICATE ' ELSE '' END
         || CASE WHEN length(name) <> length(CAST(name AS BLOB))
              THEN 'NON-ASCII' ELSE '' END) AS flags
FROM c
ORDER BY folded, created_at;
```

查询列出名称（去掉首尾空白、按 ASCII 转小写后）以 `qubes-air-` 开头、类型为 `pki`（同样处理）、
或名称含非 ASCII 字符的每一行。`flags` 为空的行正是控制台自己会写的样子；其余按标记处理：

- `NOT-CANONICAL`：名称与规范写法不逐字节相同，或类型不恰好是 `pki`。规范写法只有
  `qubes-air-ca-cert`、`qubes-air-ca-key`、`qubes-air-luks-master`、`qubes-air-luks-key-<现存 Qube 的 id>`
  和 `qubes-air-luks-legacy-slot-<现存 Qube 的 id>`。控制台从不写别的名称或类型，所以这样的行
  不是它写的。DEK 或迁移标记指向 `qubes` 表里已不存在的 Qube 时也会带这个标记：可能是遗留，
  也可能是预先放进去的，需要按下文的审计和日志判断。
- `DUPLICATE`：同一个折叠后名称下不止一行。真正的那一行也会带上这个标记，因为它和冒充者在
  同一组里；按 `created_at`、审计和日志判断哪一行是控制台写的。
- `NON-ASCII`：名称含非 ASCII 字符。控制台的名称全是 ASCII；`ſ`（U+017F）、`K`（U+212A）
  这类字符按大小写折叠会对上控制台的名称，而 SQLite 的 `lower()` 只处理 ASCII，所以这类行只能
  靠这个标记找出来。运维方自己用中文等非 ASCII 字符命名的凭据也会列出来（同时带
  `NOT-CANONICAL`）：名称如果不是把 `qubes-air-…` 换了几个形近字母的写法，就是运维行，可以不管。

`TestConsoleRowCheckQueryFlagsPlantedRows`（`internal/repository`）从本文件读出这段 SQL，
在一个放了大小写变体、`ſ` 和 `K` 变体、同名重复、错误类型、填充空白以及指向不存在 Qube 的
DEK 的临时库上运行，逐行核对标记。

查询发现不了一种行：在控制台写入自己那一行**之前**，就以完全相同的名称和 `pki` 类型存进去的
单独一行（例如在某个 Qube 的 DEK 生成前放进 `qubes-air-luks-key-<id>`，或在控制台第一次创建 CA
前放进 CA 两行）。库里只有这一行，它与控制台写的没有区别，控制台也会照常使用它。只能从库外
找线索：

- 审计：`route` 为 `/api/v1/credentials`、`method` 为 `POST`、`status` 为 201，或 `route` 为
  `/api/v1/credentials/:id`、`method` 为 `PUT`、`status` 为 200 的行。审计行不含名称，要按时间
  与 `object` 和库里的 `created_at`、`id` 对照。
- 控制台日志：每个真正由控制台生成的 DEK 都有一行 `pki: minted a per-qube data key for <qube id>`，
  CA 有一行 `pki: created a new agent CA`。有 DEK 行却找不到对应的 minted 日志，或 CA 行找不到
  created 日志，就要当作可疑。

这两类记录都只有在部署方保留了 journal 或审计日志时才存在（见
[生产部署安全要求](deployment-requirements.md) 第 6 条）。

发现可疑行时：

1. 停止控制台：`systemctl stop qubes-air-console`。
2. 备份：按[升级与回滚](upgrade-rollback.md) §3 第 1 步运行 `qubes-air-backup create`。
3. 离线删除不是控制台写的行，只按 `id` 删：
   `sqlite3 /rw/config/qubesair/qubes-air.db "DELETE FROM credentials WHERE id IN ('<id>', ...);"`，
   然后重跑上面的查询，确认 `flags` 全部为空。
4. 冒充的 CA 行如果曾经被使用过（重启后控制台用它签发过证书），就按 CA 泄露处理：按
   [灾难恢复](disaster-recovery.md)“CA 灾难恢复”更换 CA，所有 agent 重新 bootstrap。某个 Qube
   的 DEK 如果是预先放进去的，这块盘就是用调用方已知的密钥格式化的，应当视为已泄露，重新
   provision，不能只删这一行。

不要删掉真正的那一行：删 DEK 会让对应的盘不可恢复，只删 CA 的一半会让签发停止。

## Exec：JSON 参数列表

stdin 必须是 JSON 字符串数组，第一项为已允许的规范绝对可执行文件路径：

```bash
printf '%s\n' '["/usr/bin/id","-u"]' |
  qrexec-client-vm <remotevm> qubesair.Exec
```

必须显式启用 `QUBESAIR_ALLOW` 中的 Exec，并在 `QUBESAIR_EXEC_ALLOW` 中允许该可执行文件。两个白名单都由 console 随 qube 下发到 guest 的 `agent.env`：服务白名单来自 `agent_allowed_services`，路径白名单来自 `agent_exec_allow` / `agent_filecopy_roots`（或环境变量 `QUBES_AIR_EXEC_ALLOW` / `QUBES_AIR_FILECOPY_ROOTS`，冒号分隔）。console 在启动配置校验和渲染 cloud-init 时各校验一次路径白名单（必须是绝对、规范化、不含冒号与控制字符的路径），所以写错的值在启动或 provision 阶段就失败，而不是变成 guest 里一次被拒的调用。
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

升级要求：加密 Qube 的 agent 在下次解锁前必须允许 `qubesair.RekeyData`，否则迁移失败、
数据保持加密并在下次 resume 重试。迁移完成后 `qubes-air-luks-master` 只是只读的迁移材料，
可核验无未迁移盘后删除（凭据 API 看不到这一行，删除只能离线操作数据库，见上文“Console API
凭据”）；master 丢失会使未迁移盘无法解锁，也不会自动重建。

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
