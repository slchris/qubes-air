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

新 relay-call 的真机中断与重连结果在配套部署后补充。直接运行 Relay handler 只覆盖 handler 与 mTLS
stream，不能代替 dom0 ConnectTCP policy、Xpra 认证、单击启动、多窗口及断线恢复验收。

前一次 FileCopy 中断探针不计作在途传输证据：普通 relay-call 先缓冲完整 stdin，随后才
拨号。保持 stdin 未关闭时重启 agent，并未形成在途 RPC；关闭 stdin 后发生的是一次
正常写入。文件跨 suspend/resume 的独立证据不受此探针影响。
