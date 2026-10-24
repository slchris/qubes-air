# 加密 HOME 与桌面停止顺序

本阶段将桌面 HOME 固定为 `/data/home`，启动前核验加密盘并准备目录，停止挂载前结束桌面
服务及其应用。NSS 中 UID 1000 的 HOME 也必须是该固定路径；旧系统盘 HOME 不自动搬迁，
未正确配置时拒绝启动。

## 本阶段验收条件

- root 准备入口不接收参数或外部路径；必须先通过已有 LUKS/mapper/ext4 挂载检查。
- 只创建缺失的 `/data/home`，设置 UID/GID 1000 和 0700；已有目录只验证，不递归 chown、
  修复权限或跟随 symlink。错误拥有者、权限、类型或挂载均失败。
- 每次 systemd 启动桌面都重新检查。桌面绑定 `data.mount`，开始顺序为挂载、root 检查、
  非 root Xpra/broker；停止时先结束桌面 cgroup，再卸载。
- 上游 Xpra 的独立 proxy socket 与 service 必须 masked/inactive；入口只读核验，失败不创建 HOME。
- Xpra 与 broker/worker 只使用固定 HOME，工作目录不可回退到系统盘。包安装不创建用户、
  修改 NSS 或自动启用桌面；已启用的桌面在成功解锁后请求启动。
- 单元、qrexec、包安装与真实 Xpra 集成覆盖失败路径；隔离 QEMU 覆盖 systemd 与真实
  LUKS 挂载，验证文件保持和停止顺序。运行 pre-commit 与 audit。

guest root、provider 管理员、内核及桌面 UID 仍在各自现有信任边界内。此阶段的隔离环境
证据不等于生产 Salt/dom0 接线、实际 Qubes 窗口或真实 provider 生命周期验收。

## 启动与失败行为

包内新增固定 root 入口 `data-home.py`，无参数、无远端路径、无 shell。该入口先验证 NSS，
再调用已有只读挂载检查器，并核对上游 Xpra 两个单元的状态。外部进程各有 10 秒上限，查询
失败或超时即停止。新建目录使用 no-follow 文件描述符设置 owner；已有目录只检查，部分失败
保留现场，不把未知目录当成可自动修复的 HOME。主 supervisor 与 broker/worker 均拒绝其他
HOME，工作目录必须能切换到 `/data/home`。
已有 0700 HOME 使用 `lstat` 验证类型与身份，无需打开目录读取用户内容，因此重复准备也不需要
DAC override/read-search 能力；受限容器与真实 systemd 的重开路径均纳入回归。

固定 oneshot `qubes-air-desktop-home.service` 在桌面 device cgroup/namespace 外以 root 执行，
仅保留 `CAP_CHOWN`，启动上限 35 秒；它成功后回到 inactive，因此下次启动会重新检查。
主 Xpra/broker 仍以 UID/GID 1000 和原 sandbox 运行，`PrivateDevices=yes` 与 agent sandbox
没有放宽。`data.mount` 的条件要求 `/data` 已挂载，不能用 desktop 的依赖启动绕过 UnlockData。
该 mount 的等待上限为 5 秒，且不排序在 `local-fs.target` 前，避免缺失 mapper 时先阻塞启动、
导致网络 agent 无法就绪解锁。保留对 `umount.target` 的停止关系。
绑定和停止顺序依据 [systemd 的 BindsTo/After 语义](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html#BindsTo=)
及 [mount unit 语义](https://www.freedesktop.org/software/systemd/man/latest/systemd.mount.html)。

真实 PID1 验收发现上游 6.5.5 `xpra-server` 包的安装脚本自动启用 `xpra-server.socket`，
同时监听 TCP 14500 和 `/run/xpra/system`；此前无 systemd 的容器不能暴露该安装行为。
部署必须在安装 Xpra 前屏蔽 `xpra-server.socket` 与 `xpra-server.service`。每次桌面启动
重新要求二者 masked/inactive；此检查不自动停止其他服务，也不是持续网络监控。具体维护命令见
[包部署说明](../../packaging/agent-deb/README.md#encrypted-home-and-mount-lifecycle)。

包安装不创建 UID 1000、不改变其 NSS、不移动旧 HOME，也不启用桌面。只有已启用桌面的部署
才在 UnlockData 成功后请求异步启动。重复解锁不重启已有会话；启动请求被拒绝时仍返回盘的
成功结果，并附加 `desktop start request failed`。请求入队后的启动失败另见 unit journal，
不能把 `unlocked:true` 当成 GUI 就绪。

## 隔离 Linux 验收入口

使用[挂载身份记录](2026-10-24-encrypted-data-mount.md#可重复的-linux-验收入口)所列的可信
Debian 13 镜像、SHA512 与 UEFI/QEMU 前置。在仓库根目录构建本次 Linux broker，并把绝对路径
交给同一个隔离 runner：

```bash
(
  cd console/backend
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
    -o /tmp/qubes-air-desktop-home-test ./cmd/qubes-air-desktop
)
python3 scripts/test-data-mount-guest.py \
  --image /path/to/debian-13-genericcloud-amd64.qcow2 \
  --sha512 <verified-sha512> \
  --firmware-code /path/to/edk2-x86_64-code.fd \
  --firmware-vars /path/to/edk2-i386-vars.fd \
  --desktop-binary /tmp/qubes-air-desktop-home-test
```

仅该可选参数启用桌面检查。runner 创建新的 root overlay、两块 256 MiB SCSI 测试盘和 seed，
不传入主机磁盘、共享目录或入站端口。guest 核对专用 marker、QEMU 身份和磁盘尺寸，下载并
逐包验证仓库内六个 Xpra SHA256 pins；UID 1000 的 NSS 变化仅发生在 fixture 中。
测试使用真实 systemd、LUKS、Xpra、broker 与 xclock，文件和密钥均为临时生成。

## 验证范围

先失败的回归覆盖旧 supervisor 接受系统盘 HOME、缺失 HOME 仍启动、已启用桌面未收到解锁
后的启动请求，以及未屏蔽上游 Xpra 代理仍准备 HOME。单元检查覆盖错误 mount、owner、mode、
symlink、账户、查询失败和超时；Go worker 验证目录不存在时拒绝派生进程。

本阶段不包含旧 HOME 数据迁移、生产模板/账户配置、Salt/dom0 policy 或生产 Qubes GUI。
应用收到 SIGTERM 后可能仍不退出，25 秒后 systemd 会清理剩余进程；该停止顺序不保证保存
未保存的文档，也不构成断电容错或任意应用持久性保证。

## 验证结果（2026-10-24）

| 检查 | 结果 |
|---|---|
| `make pre-commit`、`make audit` | 最终版本均通过；Go race 覆盖率 75.6%，本机/Linux lint、安全、复杂度、Shell、YAML、文档门禁通过 |
| 合约与单元 | 219 项 qrexec（含 50 项 UnlockData）、12 项 HOME、19 项挂载和 13 项会话测试通过；前端 271 项测试与 check/build 通过，零错误和 warning |
| `make agent-deb-test` | 安装、升级、移除与默认不启用桌面通过；包内 58 项测试通过 |
| `make xpra-integration` | 固定 HOME 下真实窗口/重连、UID/mTLS、mmap/control 拒绝、故障停止与认证重建通过 |
| 最小能力容器 | 只保留 `CAP_CHOWN` 的 Linux 容器内，首次创建与再次验证 UID/GID 1000、0700 HOME 均通过 |
| 隔离 QEMU Debian 13 | 缺盘限时拒绝、错误 tmpfs 保留、未屏蔽代理拒绝、symlink/错误 owner 拒绝；真实 systemd/Xpra/broker 启动与应用 HOME/CWD 检查通过 |
| 重复与停止顺序 | 重复解锁保留 desktop MainPID；`systemctl stop data.mount` 先结束桌面及应用 cgroup，随后卸载；运行目录清理，系统盘无 HOME 回退 |
| 真实加密盘重开 | 关闭 mapper、重新 UnlockData 后，HOME 测试文件与应用 SIGTERM 时写入并 fsync 的停止标记保留；再次显示 xclock 并完成清理 |

QEMU 验收使用真实 PID1 与独立 Linux 内核，成功标记和 QEMU 退出码 0 同时满足。
它验证同一临时 guest 中的卸载/重开；没有把这些结果当作生产 compute 重建或 Qubes 原生窗口验收。
