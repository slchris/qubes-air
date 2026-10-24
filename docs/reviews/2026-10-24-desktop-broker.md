# 非 root 桌面启动边界

源码已用固定非 root broker 替换 StartApp 的动态 systemd-run，菜单和启动统一使用
GIO 的 Desktop Entry 解析与 ID 解析，尚未部署。完整 GUI-01 仍包含 Xpra 认证、桌面部署和真机验收。

## 接口与信任边界

- broker 以现有 UID 1000 运行，只使用该账户的 HOME 和系统应用目录；不再以 root 枚举其他用户目录。
- agent 的 GetAppmenus / StartApp 和 `qubes-desktop-run` 只连接固定 Unix socket，保留原 agent
  的 ProtectHome / PrivateTmp。请求只有菜单或受限应用 ID，不接受命令、路径、环境或网络目标。
- 服务端验证内核 peer UID，客户端验证 broker UID；请求、响应、等待时间和并发都有上限。
  未启用服务、错误 peer、畸形/过量数据或启动失败必须非零退出，不能重放已派发启动请求。
- GIO 在非 root worker 中读取 Desktop Entry，按标准处理嵌套 ID、覆盖、隐藏项与 Exec 参数。
  根进程不解释 metadata，不建立任意 root 临时 unit；启动结果只代表请求接受，不宣称窗口可见。
- broker unit 随 agent 包交付但不自动启用；其启动、停止和子进程生命周期独立于单次 qrexec 调用。
  现有桌面 HOME 的持久化与 Xpra 的 display/Xauthority/认证交付仍需后续部署阶段解决。

## 验收条件

先补回归，再实现：Unix 协议的正反例、peer/尺寸/超时/并发和重启，CLI 无服务时失败关闭，
真实 GIO 的目录优先级、嵌套 ID、隐藏项、参数与失败行为。Linux 隔离环境中检查实际启动 UID、
root 启动拒绝和进程在 RPC 返回后继续运行；安装包布局与依赖必须经安装测试验证。
阶段结束运行 `make pre-commit`、`make audit` 和 Linux 包/桌面集成测试，保留未部署的边界。

## 已实现与验证范围

- Go 协议/客户端/server：双向内核 peer UID 校验，512 字节请求、1 MiB wire 响应、10 秒
  deadline、4 个并发请求，拒绝过载。菜单最多 256 个应用，必需字段完整且不重复，Exec
  固定为 wrapper 加受限 ID；Icon 仅允许主题名称。取消、结果丢失或超时都不自动重放。
- root 和 UID 1000 可请求菜单/启动；daemon 与 Python worker 拒绝 root。worker 使用
  `/usr/bin/python3 -I` 和固定 session 环境，子应用三个标准 fd 指向 `/dev/null`。
- `qubes-air-desktop.service` 使用 UID 1000、NoNewPrivileges、空 capability 集合；
  应用由持久 unit 的 cgroup 管理，RPC 返回后继续运行。该 UID 下的应用共享桌面权限。
  unit 停止/重启会终止应用并清理运行目录与 Xauthority；broker 自身不删除任何残留 socket。
- agent 包交付客户端、worker、wrapper 和 unit，声明 GIO/D-Bus/X11 依赖。包安装/升级不启用
  桌面、不重启桌面应用；真实 systemd 维护失败不再被忽略，非 systemd 容器显式跳过在线操作。

本地 Go 与 Linux 专属测试已通过，覆盖 peer、尺寸、超时、取消、过载、重复显式请求、
残留/占用 socket、环境隔离与包 hook 失败。Debian bookworm 的已安装包通过 14 项集成测试：
实际 root/UID 1000 client 往返、错误 broker UID、root daemon 拒绝、重启后新请求，以及
GIO 嵌套 ID、用户覆盖、隐藏项、参数展开、启动 UID、缺失应用和 Xvfb 窗口在 worker 返回后存活。
测试夹具要求显式容器标记、容器环境和 root，直接在开发机调用会先拒绝，不能操作宿主运行目录。

完整 `make pre-commit`、`make audit` 与最终代码的包安装/升级/移除复验均通过。
本机 Go 总覆盖率 75.4%；前端 21 个文件/271 项测试，check/build 零错误和零 warning；
shell/qrexec 183 项通过。桌面 wrapper 的验证替换了旧的 root 解析与 systemd-run 桩测试，
协议/解析边界由 Go 与已安装包的 GIO 测试承担，不能仅按 shell 用例数量比较覆盖范围。

## 保留的缺口

没有部署新 agent、broker 或 Xpra，没有创建新生产 Qube。Xvfb 使用容器内临时认证材料，
它只证明本地 X11/GIO 行为，不证明 Xpra 认证、Relay GUI 转发、dom0 policy、
完整窗口退出/重连或 encrypted `/data` 上的持久 HOME。全局生产闭环目标保持未完成。
