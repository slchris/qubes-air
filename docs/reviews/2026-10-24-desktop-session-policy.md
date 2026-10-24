# 桌面会话运行策略

本阶段目标是交付固定、非 root 的 Xpra 会话启动与停止入口，并验证服务端主动禁用共享内存。
该阶段未包含生产部署、数据盘 HOME 或 Qubes/Salt 接线；后续 HOME 与 systemd 证据见
[加密 HOME 记录](2026-10-24-encrypted-desktop-home.md)。

## 本阶段验收条件

- 仅 UID 1000 能启动固定 display、socket 和 Xauthority；不接受远端提供的命令、路径或环境。
- Xauthority 创建失败、版本不符、Xpra 退出或 broker 退出均失败关闭；停止会话先停止 broker。
- 真实客户端主动请求 mmap 时，服务端不协商共享内存，双窗口及重连仍正常。
- control、文件传输、剪贴板、音频、任意命令与额外 listener 保持关闭；mTLS/UID 边界不变。
- 使用签名索引中核验的固定 Xpra 包和满足回调修复要求的 PyGObject，保留实测范围。
- 补失败路径测试并运行 pre-commit、audit 与真实 Xpra 集成；包安装仍不自动启用桌面。

## 实现与证据

`qubes-air-desktop.service` 通过私有 D-Bus 会话启动固定 Python 入口，入口及 Xpra 都使用
`python3 -I`，不接受额外参数。只有现有 UID 1000 可以运行；HOME 来自该账户的 NSS 记录。
运行目录必须为 UID 1000/0700，Xauthority 必须由本次启动排他创建为 0600，已有文件或 symlink
均拒绝，不能覆盖正在运行的会话。Xpra 在固定 socket 开始监听且 `xdpyinfo` 认证成功后才启动 broker。

就绪轮询预算 20 秒，单次 socket/X11 探测另有 0.2/2 秒上限；探测超时继续检查总期限。
收到停止信号时先停止 broker，再停止 Xpra；
每个进程组先 SIGTERM、超时后 SIGKILL 并等待回收。任一子进程退出都会停止另一个并返回 69。
systemd 的 `KillMode=control-group` 负责整个应用 cgroup 与最终运行目录清理，停止上限为 25 秒。
包安装和升级不启用、不自动重启桌面，仍需单独的部署与维护步骤。

服务端 `--mmap=no` 配合上游的 `XPRA_ENFORCE_FEATURES=0` 保留导入、禁用实际子系统。
不修改上游包。全局 control 模块保持可导入和可处理请求，以避开 6.5.5 的缺失拒绝处理器问题；
唯一 socket 明确 `control=no,info=no`。禁用额外 dbus-launch、电源事件订阅和 SSH 升级等能力，
会话只保留固定 Xpra/broker socket、X11 的两种 Unix socket 和私有 D-Bus，无 TCP listener。

真实 GTK 客户端主动建立并提交可用 mmap 区域：旧 `--mmap=yes` 配置会返回协商成功，回归测试失败；
新配置返回空 mmap 能力，两个窗口仍可显示且客户端重连后原窗口 ID 不变。真实 hello 另检查剪贴板、
通知、文件传输、打印、shell、新命令、音频与客户端 shutdown 均未启用。control 请求明确返回拒绝。
停止、Xpra 退出和 broker 退出均通过实际进程验证；冷启动得到新的认证文件内容。

## 依赖与范围

测试镜像使用 Debian 13、Xpra **6.5.5-r0-1** 的六个 AMD64 上游包。
Trixie 的 InRelease 由先前核验的官方指纹
`B4993B57323148E37977E5D873254CAD17978FAF` 验签，再检查 Packages.gz 的 SHA256/大小，
最后逐包核对仓库内固定散列。实测 Python 3.13.5、PyGObject 3.50.0；运行入口拒绝其他 Xpra
版本或 PyGObject <3.44.2。
此依赖下未再出现 Bookworm 的 GLib callback 泄漏提示；短测试不等于长期运行稳定性验收。

完整 GUI 仍未闭环：本记录的容器阶段没有验证加密盘 HOME，后续隔离 QEMU 已补其持久性与停止
顺序，见[加密 HOME 记录](2026-10-24-encrypted-desktop-home.md)。生产 RemoteVM 会话启停、
Relay/qrexec 原生客户端、Salt/dom0 policy 仍待验收。容器原生客户端连接 Unix socket，
仅 Go 截图经 mTLS。桌面 unit 默认禁用，本阶段没有部署生产会话。

## 验证结果

| 检查 | 结果 |
|---|---|
| `make pre-commit`、`make audit` | 均通过；Go race 总覆盖率 75.5%，本机/Linux lint、安全扫描、前端/Shell/YAML/文档门禁通过 |
| 前端与 qrexec | 21 个前端测试文件、271 项测试，check/build 零错误和 warning；202 项 qrexec 合约测试及 12 项会话单元测试通过 |
| `make agent-deb-test` | 安装、升级、默认禁用、移除通过；包内 26 项桌面测试通过 |
| `make xpra-integration` | 真实窗口/重连、mmap/control 拒绝、能力协商、UID/mTLS、重复启动、进程故障停止和认证重建均通过 |
| Linux 集成标签 lint | `--build-tags=xpra_integration` 的 desktop/grpc/relay-call 为 0 issues |

能力协商与监听测试还覆盖了用户配置试图添加 TCP listener 的情形；固定启动环境拒绝该配置，
整个验收容器没有 TCP listener，也没有主机端口发布或额外容器权限。
