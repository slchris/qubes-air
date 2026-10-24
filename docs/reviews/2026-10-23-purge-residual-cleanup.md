# 2026-10-23 purge 残留自动清理

阶段基线：节点网络资格检查由 PR #91 合入 `d7a9dbd`。自动清理由 Air PR #92
（`07b5856`）与 Salt PR #4（`6a389d8`）合入，Salt PR #5（`23d688d`）同步制品 pin。
部署和真机复验于 2026-10-23 至 24 日完成；对应合并提交的 CI 通过。

## 行为与验收条件

- provider 确认销毁后，purge 必须清理 Console 生成的身份 YAML 和 Relay 端点缓存，再记录成功。
- 身份文件只在配置的目录内操作，核对文件中绑定的 Qube ID/name；拒绝未知所有权、符号链接、
  非普通文件与超量文件。新生成文件携带公开所有权标记；既有未知文件保留并报告人工核对。
- 身份目录是受信任的 Console/存储管理目录，不能由普通 guest 写入；所有权标记不是数字签名。
- Relay 使用完整、有界、带版本和结束校验的 Console 快照；超过 100 个对象不得截断。
  拉取失败、数据畸形、重复名称或不完整响应时不删除缓存；定时刷新和显式清理串行执行。
- 仅处理专用 `/remote-endpoint/remote-*` 命名空间；清理请求不能携带路径、命令或任意目标。
- Qubes 部署、RPC 和 policy 只修改权威 `qubes-salt-config`，不新增平行部署入口。
- 部分清理失败保留错误和重试能力，不能提前写 `purged`；重复请求、取消、超时和重启后重试均需验证。
- 真机验收使用新建的专用对象，销毁前明确确认该对象；历史资源不纳入清理。

## 实现与回归

- Console 在 provider 成功后清空数据库 endpoint，要求 dom0 返回准确对象名及 Relay 清理
  确认，再回收身份文件、记录终态。旧脚本的普通成功回包、错对象和缺失确认均失败，
  防止两仓库升级不配套时提前报告 purge 成功。
- 新身份文件绑定公开的 ID/name 所有权标记。清理与签发共用可取消的锁；先验证全部候选，
  再检查文件是否改变并删除。共享 snippet lookup/reaper 同样使用所有权校验，防止
  `alpha-<12hex>` 的本地身份文件被当作 `alpha` 的旧共享文件误删。
- `list-endpoints` 只读打开已有 SQLite，单次查询最多 10,000 个运行对象；第 101 个对象不再
  被默认分页截断。完整版本、数量及摘要验证通过后 Relay 才更新缓存；合法空快照可回收
  最后一条 endpoint，读取失败或畸形快照不能冒充空 fleet。
- Relay timer 与显式 RPC 在拉取快照前取得同一把持久文件锁；只接受 dom0 的无参数服务调用
  和一行精确对象名。退役请求遇到仍在快照中的对象会拒绝；删除后重新列举确认，支持部分
  失败重试。其他 namespace、嵌套键和非 Air 键保留。
- 本轮通过真机只读帮助与输出核对发现：`qubesdb-list` 默认去掉查询前缀，必须使用 `-f`；
  修正测试替身后，原实现有 4 个失败回归，加入参数后通过。另核对 `qvm-tags VM list TAG`
  的退出状态契约，未使用不存在的 `check` 子命令。
- 新增回归先复现身份残留、端点分页截断、缓存残留、邻居名称碰撞及旧退役回包误接受。
  取消、锁超时、重启重试、并发旧快照、失败与超量输入路径均有覆盖。

## 门禁及部署边界

- Air `make pre-commit`、`make audit` 通过；Go race 总语句覆盖率 75.0%。本机和
  linux/amd64 lint/gosec、入口与 mTLS 冒烟、前端 check/build、Shell/YAML/文档检查通过。
  govulncheck 未发现代码或导入包受到漏洞影响；依赖模块级仍报告 1 个不可达项，本阶段未改依赖。
- Salt 41 个部署回归与全量 salt-lint、严格 yamllint、99 对 state/top 和修改/渲染 shell 检查通过。
  后续 PR CI 与部署结果另行记录，不以本地回归代替真机证据。
- 升级时先停止并排空旧 endpoint timer/service，再配套更新 Console lister、Relay helper、
  dom0 RPC/policy，最后恢复刷新。旧 shell 不认识新锁，不能与新 helper 同时运行。
- 未迁移既有未知所有权文件；它们会保留并要求人工对账。未重启 Relay，未清理历史对象。

## 真机生命周期与自动清理结果

用户明确授权对象 `qa-cleanup-20261023`，ID `cc8bb396-a05d-491c-b2f1-2300aeecb1dd`，
包括 holder 105、compute 106、该对象 resume 重建的 compute、2 GiB 数据卷及身份/端点。
历史 holder 107 不在清理范围。Console 初始使用 `06cfff3`，最终清理使用完成传输修复的
`2b62128`；lister 为 `06cfff3`，agent 为 `eb753f4`。

- 自动调度选中唯一具有合格 bridge/VLAN 的节点。首次 provision 因 Console 入站规则
  未持久化而失败，任务 `414c2243-4262-409d-9765-c4d304ac5dbb` 的失败记录保留。
  [入站修复](2026-10-23-console-ingress.md)部署和重启验证后，resume 成功并取得正式证书。
- 完整 qrexec Exec/FileCopy 17 项正反例通过，覆盖 argv、程序拒绝、空/畸形/超量输入、
  退出码、路径逃逸、符号链接以及失败替换保留原文件。
- 1,048,576 字节文件在 suspend/resume 后只读 pull，未重写；SHA256 保持
  `fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c83`，数据盘已挂载、
  外部 sentinel 未变。后续无效中断探针曾正常改写测试文件，未计作持久性或在途失败证据，
  说明见[传输记录](2026-10-23-connecttcp-identity.md)。
- 最终 suspend 任务 `06e610d2-aad5-4439-b1d3-72f0718f6441` 成功，随后 destroy 任务
  `bc18138c-91c6-4b22-8a69-109ebcaad8c7` 成功，Qube 状态为 `purged`、endpoint 为空。
- 只读核对：测试 VM、数据卷、provider snippet、Console identity YAML、dom0 RemoteVM、
  Relay endpoint 均不存在；infra 行、当前 DEK、未兑换 token、有效证书和活动 job 均为 0。
  两张 agent 证书已吊销，历史 holder 107 仍存在。未手动删除 identity 或 endpoint。
- 重复 purge 返回成功且状态保持 `purged`，destroy job 总数仍为 1；相同残留检查再次通过。

归档和隔离恢复副本内的历史密钥不随 purge 消除，继续遵循既有备份留存与销毁策略。
临时 Exec/FileCopy grant 已通过无测试 grant 的 scoped bundle 撤销，dom0 apply 为 3 成功、
0 失败，实际 diff 删除了指定对象的四条 allow/deny 规则。随后对相同 policy 运行 Salt
`test=True`，已安装文件处于正确状态且无待变更；默认 Exec/FileCopy 仍为 `ask`。
Console 最终健康、活动 job 为 0、离机挂载及备份 timer 正常。
