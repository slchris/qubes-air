# Proxmox 响应结束与取消同时发生

正式 URL 安装修复的 PR #104 通过全部检查后，合并提交的 Go Test 在
`TestNetworkPreflightCancellationAndTimeout` 失败：预期 `context.DeadlineExceeded`，
实际为网络响应 JSON 为空。其他四个 workflow 成功，发布仍停在 CI 门禁之前。

原因是请求取消与 HTTP 响应结束可能同时发生。`requestRaw` 在读取到正常 EOF 后直接
返回响应，不再次查看调用方 context，导致网络预检把空响应解释为 JSON 损坏；若响应
完整，也可能把已经取消的读取当成成功。

修复在有界读取结束后优先返回调用方取消/超时原因，保留错误链并关闭响应体，再处理
一般读取错误、大小限制与 HTTP 状态。认证、TLS、响应上限、网络资格检查和 provider
资源操作顺序均沿用原实现；该检查不宣称撤销已经完成的 provider 写操作。

先补确定性回归：响应体在返回正常 EOF 前取消或等待 context 到期，分别返回空内容、
不完整 JSON 和完整 JSON。修复前六种情况均错误地返回 nil error；修复后必须返回原始
context 错误、无响应字节，并关闭响应体。现有真实 HTTPS 取消/超时预检继续保留。

确定性回归和现有真实 HTTPS 取消/超时预检连续 100 次 race 运行通过。
`make pre-commit audit` 全部通过，包含 provider 全包 race、双平台 lint、完整安全扫描、
依赖检查、前端 0 error/0 warning、ShellCheck、YAML、文档和入口冒烟。
独立 PR 与合并后的 CI 仍需通过，之后才恢复修复版发布。
