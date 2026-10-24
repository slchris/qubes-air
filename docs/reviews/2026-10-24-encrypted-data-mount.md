# 加密数据盘的挂载身份边界

本阶段在接入桌面 HOME 前，修复 UnlockData 仅凭 mapper 路径存在和 `/data` 是挂载点就返回
成功的问题。行为变化是：使用同一 scsi1 数据设备、LUKS UUID、内核 device-mapper 信息与当前
mount namespace 的挂载记录证明身份；无法证明时失败，不把其他挂载或裸系统盘当作数据盘。

## 本阶段边界与验收条件

- 检查入口只有固定操作名与固定路径，不接受远端路径、命令或环境覆盖；检查本身只读，不读取密钥。
- 只有唯一 scsi1 块设备可通过；已有 mapper 必须对应同一设备及 LUKS UUID，且未被 suspend。
- `/data` 必须是真实目录，由 root 持有且不可由普通用户替换；成功挂载必须为正确 mapper 的
  可写 ext4 根，不接受叠加、其他来源或隐藏其内容的子挂载。
- 已挂载、刚打开 mapper、完成挂载三个阶段都核验；失败不得继续格式化/挂载或声称解锁成功。
- 签名探测只接受底层库明确返回「未识别签名」作为初始化候选；读错、已识别的分区表或不支持的文件系统均
  拒绝，mkfs 失败也不得继续挂载。这不是对任意无已知签名用户数据的内容识别或覆盖授权。
- 补先失败的 shell 合约回归、只读检查器的正反例和隔离 Linux 内核上的真实 LUKS/mount 证据。
- 运行 pre-commit、audit 与包安装检查。此阶段不等于持久 HOME、systemd 停止顺序或生产 GUI 已验收。

provider 管理员、guest root 与内核仍在信任边界内；多次只读检查不防御它们主动并发替换设备或挂载。
挂载字段依据 [Linux mountinfo 文档](https://man7.org/linux/man-pages/man5/proc_pid_mountinfo.5.html)，
device-mapper 名称、UUID 与 suspend 状态依据
[内核 sysfs ABI](https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-block-dm)。

`blkid` 命令行的退出码 2 也包括打开或读取失败，不能据此允许格式化。固定检查入口通过
`libblkid.so.1` 的只读 low-level API 探测，启用 superblocks 与 partitions 两条检查链；只有
`blkid_do_safeprobe` 返回 1 才输出 `unrecognized`，错误和歧义返回非零。已识别的分区表输出
`other`，无分区表的 ext4 输出 `ext4`，调用方只接受精确的成功状态与对应 token。接口返回语义见
[util-linux 的 probe 实现与 API 文档](https://github.com/util-linux/util-linux/blob/v2.41.2/libblkid/src/probe.c)。
这不证明磁盘全零或用户数据不存在，初始化仍只适用于明确新建的数据盘。

## 可重复的 Linux 验收入口

使用官方 Debian 13 genericcloud AMD64 qcow2，并从可信的官方 SHA512SUMS 核对摘要；本轮镜像
SHA512 为 `16e0965312c56feb331049d45d2b1b5778f3f942a33d4300eedbb97527707f2bea87c0006f3e4cc7a25fadbff739c4e235a51fb9bcd1b66fd554f7f8e80ba45c`。
该镜像使用 UEFI。准备 QEMU 的 x86_64 UEFI code 与匹配的变量模板后执行：

```bash
python3 scripts/test-data-mount-guest.py \
  --image /path/to/debian-13-genericcloud-amd64.qcow2 \
  --sha512 <verified-sha512> \
  --firmware-code /path/to/edk2-x86_64-code.fd \
  --firmware-vars /path/to/edk2-i386-vars.fd
```

入口依赖 `qemu-system-x86_64`、`qemu-img` 和 `genisoimage`（Linux）或 `hdiutil`（macOS）。
它在私有临时目录创建新的 root overlay、两块 256 MiB SCSI 测试盘与 cloud-init ISO；guest 自带
独立内核，通过 NAT 下载发行版签名包，无主机共享目录、块设备透传或入站端口。磁盘操作只发生在
guest 内；测试入口要求专用 marker、QEMU 身份、精确盘大小及全新挂载状态。

最多运行 15 分钟；只有 guest 输出成功标记且 QEMU 成功退出才通过。日志与结果留在命令输出的
临时目录中。fixture 通过不等于生产 guest、加密 HOME 或 Qubes 桌面验收。

## 验证结果（2026-10-24）

| 检查 | 结果 |
|---|---|
| 先失败的回归 | 旧逻辑错误接受 tmpfs `/data`；读取失败的空输出会触发格式化，连 `blkid -p` 退出码 2 也不能修正该问题；新检查均拒绝 |
| `make pre-commit`、`make audit` | 均通过；Go race 总覆盖率 75.6%，本机与 Linux lint、安全、复杂度、Shell、YAML 和文档门禁通过 |
| 合约与单元测试 | 216 项 qrexec（含 47 项 UnlockData）、19 项挂载/原生探测单元测试、12 项桌面会话单元测试通过；前端 271 项测试与 check/build 通过，零错误和 warning |
| `make agent-deb-test` | Debian 12 安装、升级和移除通过；包内挂载测试及真实 `libblkid.so.1` 的无签名/打开失败检查通过；桌面仍默认禁用 |
| 隔离 QEMU Debian 13 | 首次 LUKS 创建/解锁、正确 ext4 身份、重复调用、关闭重开后文件保持通过；错误 mapper、tmpfs、子挂载、只读、所有权/权限与 symlink 均拒绝 |
| 真实读取失败 | 无 backing storage 的专用 device-mapper `error` fixture 返回 EIO；旧 CLI 返回退出码 2 且 stdout 为空，新原生探测拒绝；fixture 随后移除 |

Linux 验收使用内核 `6.12.113+deb13-cloud-amd64`、systemd `257.13`、cryptsetup `2.7.5`。
这些结果覆盖本阶段的挂载身份和初始化失败边界，未执行生产升级、生产 Qube 生命周期或桌面验收。
