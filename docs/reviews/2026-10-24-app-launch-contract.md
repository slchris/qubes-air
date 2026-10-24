# 应用启动参数与失败结果

本阶段先修复 GUI 启动链的请求/响应契约，完整桌面权限与会话部署仍归 GUI-01。

## 行为与边界

- 应用 ID 来自不可信远端菜单或 API/MCP 调用者。浏览器、MCP、API/service 和 StartApp 统一要求
  `[A-Za-z0-9._+-]`、1–113 字节、不得为 `.` 或包含 `..`；15 字节服务前缀加 ID
  不能超过 Relay/agent 的 128 字节上限。拒绝必须发生在启动程序之前。
- StartApp 缺少/非法/多余参数返回非零状态；缺少桌面账户、非法 display 或启动 helper
  失败也返回非零状态。失败只写有界 stderr，stdout 不伪造成功；不回传 helper 的任意输出。
- stdin 不用于应用启动。UID 1000 缺失时不猜测其他用户。display 仅接受本机 X display。
  缺少 systemd-run 明确失败；移除不能可靠访问真实 HOME 的 runuser 回退。
- HTTP 层保留已有非零远端结果 → 502 的行为，前端展示错误，不自动重试；成功响应仅表示
  启动 helper 接受请求，不能据此宣称窗口可见或应用已持续运行。

## 验收条件

已先修改 Go、前端与 shell 回归，确认旧实现失败，再实现修复。覆盖 113/114 字节边界、
路径/点段/非 ASCII、额外参数、账户缺失、display 校验、helper 非零、大输出和 stdin 隔离；
API 拒绝不得触碰 transport。

旧实现的真实失败包括：API 把非法 ID 送到 transport、MCP 接受 114 字节 ID、前端菜单
仍展示传输无法接受的 ID，以及 StartApp 错误退出 0、stdout 回传 helper 诊断。
本地 `make pre-commit` 与 `make audit` 均通过，Go 覆盖率 75.1%；前端 check/build 零错误、
零 warning，21 个测试文件/271 项测试通过，完整 shell/qrexec 契约 211 项通过。
`npm run test:e2e` 的两条模拟 API 浏览器用例通过；生命周期用例覆盖远端启动错误展示、
失败不显示成功、不自动重试，以及显式重试成功。它不代表真机桌面验证。

本轮尚未部署 agent，生产行为仍以已部署 revision 为准；合并检查以本阶段 PR 为准。

## 尚未覆盖

此阶段不把 GUI-01 标为完成：现有 systemd-run 桌面启动仍越出 agent 沙箱，desktop launcher
仍需替换 shell Exec 解释；菜单枚举与启动解析需要统一，launcher 打包与非 root 桌面会话
需要接线。Xpra 部署/认证、dom0 GUI policy、单击启动、多窗口、退出与重连仍需真机验收。
