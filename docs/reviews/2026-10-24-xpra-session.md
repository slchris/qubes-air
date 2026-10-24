# 固定认证桌面流与真实 Xpra 互通

本阶段在已合并的非 root broker 上交付固定 Xpra Unix 通道及可复现的真实互通测试。
生产会话、持久 HOME、Salt/dom0 接线与真机 GUI 验收仍属项目后续工作。

## 行为与信任边界

- Console 与 Relay 使用 `qubesair.StreamDesktop`。agent 只连接
  `/run/qubes-air-desktop/xpra.sock`，连接后核验内核对端 UID 1000，失败时不发送应用数据。
  请求没有可配置路径、主机或端口。原桌面 TCP 10005 在 agent 和 ConnectTCP 两端被拒绝。
- mTLS CA、角色、目标、吊销和 Console 逐帧授权继续生效；专用桌面流只对 Relay 和
  `console-desktop` 开放。其他 Console 身份及带后缀的伪 service 在拨号前被拒绝。
- 新 Relay wrapper `qubesair.ConnectDesktop` 只接受一个 `remote-*` 名称，从 QubesDB 取 endpoint，
  校验 IPv4/端口数值，只去掉一次本地 `remote-` 前缀。stdin/stdout 原样流式转发，失败保留非零退出码。
- 本阶段的交付范围是源代码、CI 和容器验收；desktop unit 保持默认禁用。
  agent 的 ProtectHome/PrivateTmp 未变；broker 的 HOME 仍来自 UID 1000 的 NSS 配置。

## 真实包与可复现入口

`make xpra-integration` 在无特权、无发布端口的 Debian Bookworm AMD64 容器中执行。
`scripts/test-xpra-session.sh` 下载 Xpra **6.5.5-r0-1** 的 common/server/client/client-gtk3/codecs/x11
六个包，镜像在安装前按 `packaging/xpra-test/packages.sha256` 校验全部散列。CI Build 增加同一入口。
Go 测试通过显式 `xpra_integration` build tag 编译，缺少容器标记会失败。

这些散列来自 Xpra 官方 Bookworm 索引：先核验公开签名 key 的指纹
`B4993B57323148E37977E5D873254CAD17978FAF`，再用 `gpgv` 验证 2026-10-13 的 InRelease，
核对其 Packages.gz 的 SHA256/大小，再核对每个包的散列。后续复验直接使用仓库内固定散列；
下载的包、keyring、索引与任何运行身份均不提交。
参考：[官方安装入口](https://github.com/Xpra-org/xpra/wiki/Download)、
[Bookworm 源定义](https://raw.githubusercontent.com/Xpra-org/xpra/master/packaging/repos/bookworm/xpra.sources)、
[peercred 认证说明](https://github.com/Xpra-org/xpra/blob/master/docs/Usage/Authentication.md)。

容器启动 UID 1000 的 Xpra，在固定 socket 上配置 `peercred(uid=0:1000,allow-owner=no)`，
由真实 broker/GIO 启动两个 xclock 窗口，另一个 X display 上运行真实 GTK Xpra 客户端。
测试覆盖 root/UID 1000 接受、UID 1001 拒绝、无额外 TCP/Unix listener、control 请求拒绝、
原生客户端断开再连接时原有窗口保留，以及 Go 经 mTLS 取得并解码真实 PNG。
UID 1001 负例仅在一次性容器内临时放宽 socket 文件权限，使请求到达 Xpra 的 peercred 检查；
完成后立即恢复 0700 目录和 0600 socket。会话停止后固定拨号必须失败。

## 实际发现与部署限制

- Xpra 只复用已存在且可写的 Xauthority。测试先建立 UID 1000/0600 的固定文件，再由 Xpra 写入
  临时 cookie；仅设置环境变量会被 Xpra 的默认路径替代，broker 因此拒绝启动应用。
- 无缝窗口在 GUI 客户端映射前可能没有可捕获像素，Go 客户端会明确返回空截图错误。
  容器截图在真实 GTK 客户端显示窗口后执行，不能据此声称无客户端桌面也已就绪。
- Xpra 6.5.5 全局 `--control=no` 会触发缺失模块导入；测试保留模块并在唯一 socket 上设置
  `control=no,info=no`。全局 `--mmap=no` 同样导致窗口编码器导入失败，测试保留服务器 mmap 模块，
  GTK 客户端和 Go 截图不协商共享内存。**生产禁用 mmap 的完整策略仍需解决与验证**。
- Bookworm 的 PyGObject 3.42.2 会触发 Xpra 的 GLib callback 泄漏提示；Xpra 提示其 idle_add
  workaround 不能覆盖全部回调。本次短时互通不证明长期资源稳定性，生产依赖选择与持续运行验收仍未完成。
- 两个 display 都在同一个容器，原生 GUI 客户端通过 Unix socket 接入；mTLS 覆盖的是 Go 截图。
  这不等于原生 GUI 经 Relay/qrexec 的真机验收，也没有验证真实 dom0 policy。

## 验证记录

| 验证 | 结果 |
|---|---|
| `make pre-commit`、`make audit` | 全部通过；本机与 Linux/amd64 lint、gosec、govulncheck、入口/mTLS 冒烟、Shell/YAML/文档门禁通过 |
| Go race/覆盖率 | 通过；总语句覆盖率 **75.5%**，门槛 61% |
| 前端 | **21** 个测试文件、**271** 项测试通过；check/build 为 0 error / 0 warning；audit 通过 |
| qrexec shell 合约 | **202/202** 通过，包含 18 项专用桌面入口用例 |
| Linux 原生内核测试 | `go test -race ./internal/desktop` 通过，包含真实非桌面 UID listener 拒绝 |
| 显式集成标签 lint | Linux/amd64 `--build-tags=xpra_integration`，desktop/grpc/relay-call 均为 0 issues |
| `make xpra-integration` | 通过；两个原始窗口 ID 在客户端断开重连后保持不变，两轮 UID 与 PNG/mTLS 正反例、control 拒绝、唯一监听、停止后失败均通过 |

Xpra 测试只在容器内临时运行，六个固定包以 SHA256 校验；完整生产验收尚未通过。

## 后续闭环

1. 确定可用于生产的 Xpra/系统依赖与禁用 mmap 策略，交付协调 Xauthority 生命周期的非 root 会话。
2. 在已解锁的数据盘上建立固定 HOME，启动前核验挂载/所有权，停止会话必须早于卸载数据盘。
3. 在 `qubes-salt-config` 权威仓库交付 Relay 专用入口、relay-call 与精确 dom0 policy。
4. 新验收对象上验证单击、多窗口、退出/重连和 suspend/resume 持久性；清理使用该新对象的精确授权。
