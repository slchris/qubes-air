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

## Exec：JSON 参数列表

stdin 必须是 JSON 字符串数组，第一项为已允许的规范绝对可执行文件路径：

```bash
printf '%s\n' '["/usr/bin/id","-u"]' |
  qrexec-client-vm <remotevm> qubesair.Exec
```

必须显式启用 `QUBESAIR_ALLOW` 中的 Exec，并在 `QUBESAIR_EXEC_ALLOW` 中允许该可执行文件。
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
可核验无未迁移盘后删除；master 丢失会使未迁移盘无法解锁，也不会自动重建。

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
