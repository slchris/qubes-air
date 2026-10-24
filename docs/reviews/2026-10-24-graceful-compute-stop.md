# 保留数据盘的正常关机边界

本阶段修复 suspend/release/purge 在删除 compute 前直接硬停 VM 的行为，为桌面 HOME 持久化提供
正常退出、刷盘与卸载的机会。它不自动保存应用尚未保存的文档，也不替代真实 guest 生命周期验收。

## 行为与验收条件

- compute 的所有权与 provider lock 检查继续生效；已删除的对象保持幂等。
- 运行中的非 HA compute 使用 Proxmox `status/shutdown`，明确 `forceStop=0`，guest 关机预算 60 秒。
- 控制台关机等待与复核共用 90 秒上限，更短调用方 deadline 优先；任务失败、超时、取消或状态查询
  失败均保留 VM，不回退硬停或执行删除。
- 删除前重新确认所有权、非 HA 状态和 VM 已停止；未知状态、缺失 HA 状态与 HA 管理对象失败关闭。
- 成功、失败、取消、重复请求和异常 provider 状态都补回归测试，完成 pre-commit/audit 门禁。
- 真实 guest 的会话退出、数据卸载与 suspend/resume 持久性仍随新验收对象验证。

API 语义核对于 Proxmox 官方 qemu-server 源码提交
`71ea0ac7192bb935f63cb64beb59e87d6cecd3bf` 的 `src/PVE/API2/Qemu.pm`：
`vm_shutdown` 明确接受 `timeout` 与 `forceStop`；HA 分支转交 HA manager，故不纳入本项目的
非强制正常关机保证。来源：[官方仓库](https://git.proxmox.com/?p=qemu-server.git)。

provider 管理员仍在信任边界内：多次状态读取不是原子锁，不允许同时改变 VM 所有权或 HA 配置。
删除请求不带 `purge` 或 `skiplock`；上述官方版本在 provider config 锁内再次拒绝运行中 VM
以及 HA 资源，避免状态检查之后重启的 VM 被强制删除。

## 验证结果

修复前新增回归已失败：旧实现仍调用 `status/stop`，接受未知/HA 状态，关机期间所有权或
锁发生变化时仍能删除，且 provider task 的等待没有独立上限。修复后以下场景通过：

- 正常关机参数、任务完成后的已停止复核、已停止跳过关机、删除后重复调用；
- 请求/任务失败、缺少任务 ID、取消和超时均不调用硬停或删除；虚拟时钟实测等待上限 90 秒，
  调用方 15 秒 deadline 会提前结束；
- 初始状态未知、HA 管理/缺失/无效 metadata，以及关机后的运行状态、HA、所有权、lock 或查询异常
  均拒绝删除；
- 失败后重试可回收后来已停止的 compute；编排层保留原 compute/data 身份，重建 executor 后可重试。

`go test -race` 的 provider/编排定向回归与定向完整 lint 通过；`make pre-commit`、`make audit`
均通过。全模块 race 覆盖率 75.6%，本机与 Linux lint、安全扫描、入口 mTLS 冒烟通过；
前端 21 个文件/271 项测试通过，check/build 零错误和 warning；202 项 qrexec 合约测试、
12 项桌面会话单元测试以及 Shell/YAML/文档门禁通过。

这证明 Console/provider 控制逻辑与失败保留行为；尚未执行新 guest 的正常关机、桌面退出、
加密 HOME 卸载及重建后文件持久性真机验收。
