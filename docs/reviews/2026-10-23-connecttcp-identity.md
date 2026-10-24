# 2026-10-23 ConnectTCP 身份映射与断线不重放

对已授权测试对象进行流连接验收时，guest 的 loopback echo 正常，但 ConnectTCP 在
返回任何数据前超时。核对现场 handler 与代码发现，它把本地 RemoteVM 名
`remote-<qube>` 直接传给 relay-call，TLS 因而期待 `agent-remote-<qube>`，而真实 agent
证书绑定的是 `agent-<qube>`。GrpcProxy 已有相同映射，ConnectTCP 尚未同步。

## 行为与边界

- QubesDB 查询继续使用完整的本地 RemoteVM 名；只在传给 relay-call 的远端身份中去掉
  一次 `remote-` 前缀。原 Qube 名若本身以 `remote-` 开头，这部分必须保留。
- 不修改 CA、角色、目标验证或 GUI 端口范围。身份不匹配仍拒绝连接，不通过放宽 TLS
  验证绕过问题；非法目标和端口仍在任何 endpoint 查询前拒绝。
- 运行时部署由权威 Salt 仓库配套同步 handler，不创建平行安装入口。

## 回归

先更新合约并在旧实现上观察失败：Air 的 6 个 ConnectTCP 用例失败，Salt 的普通目标及
双前缀目标两个子例失败。改为只去掉一次前缀后，Air 40 项 ConnectTCP 合约测试通过，
Salt 全量 55 项测试通过；其余覆盖包括非法名称/端口、端口边界、缺失 endpoint、3 MiB
双向字节保持和非零退出传递。Air `make pre-commit`、Salt 全量 lint/YAML、100 对 top/state
与修改 shell 的 ShellCheck 均通过。

Salt 的 scoped updater 最初漏投 ConnectTCP；补入精确文件白名单、制品交付与摘要拒绝
回归后，现场文件摘要与源码一致，真机已收到 4096 字节双向回包。完整 state apply 成功
不能替代运行时制品核验。

中断测试进一步发现：已建立流的 agent 重启后，relay-call 等待 20 秒仍未失败退出。
传输层把在途断线也标记为 `ErrNotConnected`，使 CLI 将已派发请求当作尚未连接重试；
普通有副作用的 RPC 同样存在重放风险。

修复契约：`ErrNotConnected` 只表示未派发且未读取 stdin，可以等待连接后重试；
已接受调用的发送失败或断线返回独立的 `ErrDisconnected`，远端执行结果可能未知。
调用的 header/body/EOS 与 stdin pump 固定使用原 tunnel，不能把剩余帧发送到重连后的
连接。CLI 在单次调用返回时取消自己的重连循环；新的独立调用仍可连接。

新回归先在旧实现上失败：真实 mTLS 的普通 RPC 和流调用都重试到总超时；断在
header/body 之后的四种组合还会返回可重试错误或把后续帧写入新连接。修复后的回归
同时检查不重放、明确失败、未派发重试、新调用恢复、各发送阶段失败与取消后的输入。

## 部署与真机验收

Air 修复 `2b621289d113af4dedc2bd50029b7f3e1a4909c2` 经 PR #93 合入 `b40d312`；
Salt 配套 `34a277f03a542d6ed67eb16291b746fc2fd20f8d` 经 PR #7 合入 `dcb03dc`。
两个合并提交的 CI 均通过。Air 完整 `make pre-commit` 和 `make audit` 通过，语句覆盖率
75.1%；Salt 56 项回归、全量 lint、严格 YAML 与 100 对 state/top 检查通过。

Go 1.26.10 从干净 Air 源码构建 linux/amd64 制品；Salt 清单和现场读回的 SHA256 一致：

| 制品 | SHA256 |
|---|---|
| Console | `6f387a38df66dfe48f045036414e0ac391d8287d48d9fd6bc8715f82396fbd7a` |
| relay-call | `daa16fe8bf0ce22ce1a058a7f987d3529ca2cac118ec320ff0bfda44c5df47af` |
| ConnectTCP handler | `0d3fd2b8d42c35dc92c3f6075b8dbf4b7e74ef983907df8ac8c41673146c451e` |

Relay apply 为 18 成功、0 失败，Console apply 为 39 成功、0 失败；Console 健康、
离机挂载和备份 timer 正常。以已授权对象 `qa-cleanup-20261023` 做真机验收：

- 通过实际 Relay handler 建立流并完成 4096 字节双向回包，再终止该对象的 agent 进程。
- agent PID 从 1372 变为 1547，旧流以 exit 1 结束，新独立调用以 exit 0 返回相同 4096 字节。
  数据 SHA256 为 `c8f5d0341d54d951a71b136e6e2afcb14d11ed8489a7ae126a8fee0df6ecf193`。
- 临时 loopback echo 服务停止、脚本移除；随后对象按既有精确授权最终 purge。

直接运行 Relay handler 只覆盖 handler 与 mTLS stream，不能代替 dom0 ConnectTCP policy、
Xpra 认证、单击启动、多窗口及 GUI 断线恢复验收，也没有重启 Relay 整机。

前一次 FileCopy 中断探针不计作在途传输证据：普通 relay-call 先缓冲完整 stdin，随后才
拨号。保持 stdin 未关闭时重启 agent，并未形成在途 RPC；关闭 stdin 后发生的是一次
正常写入。文件跨 suspend/resume 的独立证据不受此探针影响。
