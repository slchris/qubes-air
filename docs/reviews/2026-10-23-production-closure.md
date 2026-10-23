# 2026-10-23 生产闭环修复与真机验收

本轮完成受控范围内的重复部署、离机恢复及 Exec/FileCopy 完整 qrexec 验收。
基线为 Air `6b40f52`、权威部署仓库 `qubes-salt-config` `464ef0f`；变更位于两个仓库的
`codex/production-closure` 分支，尚不代表已合入 `main` 或已发布新版。
部署仓库修复提交为 `8acedc8`；Air 的独立代码提交见下方制品绑定。
时间按 UTC 记录，现场主机的显示时区不统一。

## 行为、信任边界与验收条件

- 部署只由 `qubes-salt-config` 执行。Console/Web/agent/备份及 RPC helpers 必须绑定源码、
  版本与 SHA256；候选 Console 在修改活动文件前只读检查数据库。失败不得迁移或覆盖活动库。
  重复应用不得回退制品或无条件重启，AppVM 重启后必须仍能运行。
- 备份必须写入真实离机挂载点；独立归档口令与原 keyring 分别保管。恢复使用新的隔离目录，
  禁用编排、不接管生产，验证凭据、CA、吊销状态、provider 身份与耗时。
- dom0 只为指定 source、测试 RemoteVM、Exec/FileCopy 的空参数调用授权；guest 另行限制
  可执行程序与文件目录。成功和拒绝都必须经 dom0 → Relay → mTLS agent 路径验证。
- 两个测试对象的 suspend/resume 和最终 purge 已由用户按对象 ID、VM、数据卷明确授权。
  不清理历史对象，不轮换 CA/keyring；真实地址、配置、口令与私钥不进入仓库。

## 源码与制品绑定

数据库预检修复：`eb753f4`。8 个编译/打包制品均来自这一干净源码，版本
`0.0.0+qaeb753f4`，Go 1.26.10、linux/amd64；Console 的 `/health` 返回完整 revision
`eb753f4281e43ab3c637337d25670b44c1547d91`、`tree=clean`、schema 6。
GrpcProxy 的后续身份映射修复独立为 `4455409`，不能误称已包含在这些二进制里。

| 制品 | SHA256 |
|---|---|
| Console | `996d5f57da81db95b86a6560b43707ce431a1a3cbfa32e14ae12335805676c8d` |
| Web archive | `3bf6acfa36458497216f34e12a0426b7c4249db68a6a38fdf75ea20f0ddd8809` |
| agent deb | `51905328dae0f65c9516a8f2ef4face742f90532621ded21d63f924a982d9c0a` |
| Backup | `e526615df9ebf6473e676f6247ffe2d2e6838e885a12421a90c8ddbab2639078` |
| issue-relay-cert | `ab8567cd2b09f1973439e6157cb92878e8d55738f1d1a4a7464de97b6172c5ad` |
| list-endpoints | `1b5030765225be59e4ac5461f0b067eb026b82c9111d539e8acf6f838de8bae8` |
| relay-call | `5ab32769325402d0ddef027e4f69fdc2185f8d3b3b873d7fe5f42981314df6e6` |
| relay-bootstrap | `21db0bb299da58b45ab7ed810c9952ae8d33884a578e338aa24cf760dfe464b6` |
| GrpcProxy shell (`4455409`) | `36f4e2e17c36acb63a0845990b4a4a8251ca6aabc7f3dd7d296c38f236c244d5` |

部署仓库的 `salt/qubesair/artifacts.json` 保存同一组摘要。agent 从现场版本化制品地址安装，
不是 GitHub release URL；本轮没有验证已发布的 `v0.1.0`。

## 重复部署与重启

1. 回归测试先复现未来 schema 被拒绝前已从 1 张表增加到 15 张。修复将版本检查前移到
   WAL 设置和迁移之前，失败关闭连接。`--check-database PATH` 只读检查已有库及活动 WAL，
   不加载 Console/PKI/编排；覆盖缺失、损坏、目录、URI 特殊字符、空参数、取消与超时。
   真机当前库检查通过，未来 schema 被拒绝且文件字节不变。
2. 原 Salt 树已有其他配置，使用带固定文件/配置 allowlist、SHA 校验、备份回滚的 scoped
   installer 更新，保留无关目录及配置。两次 Console apply 均为 22 states、0 failures；
   第二次 PID 仍为 20917，证明未无条件重启。
3. Console RPC helpers 改为 root 所有的持久目录及启动链接。Relay 的可执行启动代码移到
   root 所有的独立目录，与 user 所有的私钥目录分开；缺任一 helper SHA 时拒绝应用。
4. 首次 Console AppVM 重启暴露实际失败：模板虽已安装 SSHFS，但仍在运行，AppVM 使用
   旧根卷快照。离机 mount 报 `Unknown parameter 'port'`，查证 AppVM 缺少 `/usr/bin/sshfs`，
   备份因 mount dependency 失败，未回落写本地目录。
5. 关闭更新过的模板提交根卷后，再次启动 Console。boot ID 改变；Console/Backup/RPC
   helpers 摘要一致，两个 RPC service 链接有效，Web 200，schema 6、worker alive、0 活动
   job，SSHFS mount active，备份 timer active，开机自动归档成功。启动命令有一次返回非零，
   后续 qvm-ls 和实际服务探针确认 AppVM 已运行；结论来自这些探针，不据命令返回值猜测。
   文档已补上“更新模板后先关模板、再重启 AppVM”的顺序。

Relay 本身未重启：它是唯一管理入口。本轮验证了其 Salt apply、证书检查和实际调用；
不能把 Console 的重启结果扩大成 Relay 重启验收。

## 完整 qrexec 正反例

现场先发现并修复两处真实故障：端点 lister 输出裸 Qube 名，Relay 原脚本只接受 `remote-*`，
导致缓存为空；补齐映射后，GrpcProxy 又把本地 `remote-<name>` 当成 agent 的证书身份，导致
TLS 失败。现在端点键始终加一个 `remote-`，拨号身份只去掉一个前缀；原名本身以 `remote-`
开头的回归也通过。CA、角色和目标校验均保留。

测试对象 `qa-closure-20261023-b` 的 guest 仅允许 `/usr/bin/printf` 与
`/data/qa-closure`。该测试配置在 guest 单独设置，未扩大整个 fleet 的默认权限。

| 验证 | 实际结果 |
|---|---|
| Ping；Exec 字面 UTF-8 参数 | exit 0，输出逐字一致 |
| 允许的 printf 自身失败 | exit 1 原样返回 |
| 非允许程序 | exit 126 |
| Exec 空输入、畸形 JSON、超过 64 KiB | exit 64 |
| FileCopy 1 MiB 二进制 push/pull | 字节完全一致 |
| 越界目录、符号链接 | exit 126 |
| 路径穿越、空输入、超长 header | exit 64 |
| 超过 16 MiB payload | transport exit 1；随后 pull 证明原文件未被替换 |
| 额外 qrexec service argument | dom0 拒绝，exit 126 |
| 未列入精确 grant 的调用方 | dom0 拒绝，exit 126 |

自动 runner 共 17 例，另有未授权调用方拒绝验证。正例确实经过 qrexec 与 Relay；guest exec
仅用于建立测试配置和旁证，未用来替代调用验收。越界测试的外部哨兵仍为原内容。
1 MiB 文件 SHA256：`fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c83`。

## 生命周期与已授权清理

- `qa-closure-20261023`（`8d5afa6c-7cad-4eb8-a302-a2b0973d0214`）：自动调度到缺少 zone
  bridge 的节点，置备失败，留下 holder 105、compute 106 与 2 GiB 数据卷。按授权 purge
  成功。这暴露网络可用性未纳入调度的缺口，不将其写成一次成功置备。
- `qa-closure-20261023-b`（`f3b5f885-d0ea-42d5-9c36-eb9ab5c468a2`）：显式选有 bridge 的
  节点，holder 108、compute 109。provision → suspend → resume → suspend → purge 均成功。
  挂起删除 compute、保留原数据卷；RemoteVM 元数据在 suspend 阶段按现有实现保留。
  resume 重建 compute（provider 复用了 VMID 109），旧证书吊销、新证书签发，agent 恢复
  healthy。重新开放该 guest 的窄测试 allowlist 后，通过 qrexec **先读原文件、未重新写入**，
  内容和摘要与挂起前一致。
- purge 后两个对象的 provider VM/数据卷/snippet 均为 0，`qube_infra` 行为 0、端点为空、
  当前 DEK 为 0、有效 agent 证书为 0、RemoteVM 均消失。两张 agent 证书保留吊销审计记录。
  历史 holder 107 保留。重复 purge 返回成功，未新增 destroy job。
- purge 未自动删除 Console 本地生成的两份 identity YAML，也未清除 Relay 的旧 QubesDB
  endpoint；本轮按已确认的 ID/name 手动清理，再查无残留。自动回收仍待补，不能称为自动
  全资源清理。历史备份中的旧密钥副本也不因 purge 消失。
- scoped manifest 的测试 grant 已清空，dom0 policy apply 成功且删除四条对应 allow/deny
  行。回收后的额外调用进入默认 `ask` 策略并超时，不计作明确拒绝测试；明确拒绝证据来自
  上表中的有效目标/额外参数及未授权调用方测试。

## 离机备份与隔离恢复

NAS 采用单独的 SFTP 账户，限制在归档 chroot，禁止 shell/TTY/转发；SSH 使用固定身份、
严格 host-key 验证。真实 SFTP 可用、shell 拒绝。Console 备份服务先检查精确 mountpoint，
普通本地目录的负例在 create/prune 前失败。归档口令与原 keyring 分离保管，未生成替代 keyring。

开机自动归档后，再用一个受控临时 systemd timer 触发同一备份 service；新增归档，service
成功，临时 timer/service 已回收。常驻 daily timer 保持原配置并处于 waiting；未把这一受控
触发称为等待到下一次 daily 日历触发。最终归档在 Console 与 NAS 两端读回摘要一致：

| 项 | 结果 |
|---|---|
| 归档 | `qubesair-20261023T044441Z.qab`，286,900 字节 |
| SHA256 | `97f20a883302716c77db46ce1ac71be6a3d58ea51c6c6eb81b4f4dd3dc60a77f` |
| 恢复数据 | 26 Qubes、101 jobs、28 agent certs、27 revoked、4 credentials、1 retained infra |
| SQLite integrity | `ok` |
| 原 keyring 解密 | 4 条凭据全部成功 |
| 原 CA | 与在线 Relay 已信任的 CA 指纹一致 |
| 签名吊销清单 | 27 条验证通过 |
| 错口令 / 错 keyring / 已有目标 | 均拒绝；已有目标字节不变 |
| 归档解包 | 0.364 秒 |
| 恢复开始至隔离 Console 健康 | 1.503 秒 |
| 隔离恢复检查总耗时 | 1.949 秒，不含之后的 provider 对账 |

恢复 Console 在独立主机的新目录中以 `network=none`、编排关闭启动，完成后停止容器。
另启短生命周期只读探针，以恢复凭据认证 provider（登录取 ticket，此后资源查询仅 GET），
保持 HTTPS 主机名/CA 校验。历史 released 对象的 holder 107 存在、ownership 标记和数据卷
匹配、compute 为 0；没有变更 provider 或接管生产。

RTO 仅适用于这个小数据库，前提是恢复主机、镜像、程序与密钥都已就绪；不包含重建 Qubes
控制域、获取密钥、恢复持久数据卷或生产规模容量测试。本轮比对了既有信任 CA 和签名吊销
状态，没有从恢复 Console 对一个运行中 agent 执行在线接管/Ping。

## 门禁与剩余工作

- Air：先有失败回归，再修 schema/identity；`make pre-commit` 与 `make audit` 通过。
  Go race 总语句覆盖率 74.5%，前端 check/build 0 errors、0 warnings；GrpcProxy 55 例通过。
- Salt：21 个 deployment/rollback/policy/RPC 合约测试通过；全量 salt-lint、严格 yamllint、
  99 对 top/state 检查、修改及渲染 shell 的 ShellCheck 通过。没有扩大扫描排除项。
- 真机 state 成功不抹掉首次 bridge、权限、命名映射及模板快照失败；这些失败及修复顺序均
  保留在本记录。Salt 运行时的上游 Python 弃用提示不作为 state 失败，也不称为零警告日志。

尚未闭环：调度前校验 zone bridge/VLAN；退役端点与本地 identity YAML 自动回收；生产规模
RTO/完整接管；旧盘 DEK 迁移；SSH 主机键独立带外核对；release URL 置备；Relay 自身重启；
GUI/ConnectTCP 当前链路验收；GCP/AWS。它们不因本轮通过而自动关闭。
