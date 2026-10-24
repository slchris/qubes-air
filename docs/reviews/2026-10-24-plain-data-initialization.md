# 未加密盘的首次初始化边界

在加密盘检查修复后，复核 cloud-init 的未加密盘脚本发现同类问题：它忽略 `blkid` 错误，
只凭空 stdout 就格式化。执行旧脚本的回归复现了探测失败、无法读取和未知非零数据被初始化。

## 行为与验收条件

- 只接受 whole-disk ext4 作为已有文件系统；分区表、其他文件系统和不一致的探测结果直接失败。
- `blkid -p` 退出码 2 且无输出只进入进一步检查；它不构成格式化依据。
- 首次格式化前，以 `blockdev --getsize64` 取得严格正十进制大小，再用 `cmp -s -n` 将整个设备
  与 `/dev/zero` 对比。只有完整读取且全零才继续；短读、读取失败和任意位置的非零字节均拒绝。
- 已有 ext4 不做全盘扫描，也不重新格式化；mkfs 失败停止在 mount 前。cloud-init 显式安装 diffutils。
- 在宿主机及隔离 Linux 容器执行真实脚本的正反例，格式化、挂载命令用记录桩隔离；真实 cmp
  验证完整零文件、末尾非零字节、短读和读取错误。运行 pre-commit、audit 并检查生成脚本的 ShellCheck。

`cmp` 的成功、差异和错误状态见
[GNU Diffutils 文档](https://www.gnu.org/software/diffutils/manual/diffutils.html#Invoking-cmp)。
全零读取只证明检查时的设备内容；provider 管理员、guest root 和内核仍受信任，不能防御其主动
并发替换设备或修改内容。此保护未改变未加密盘原有的挂载身份逻辑，也不证明生产生命周期或桌面验收。

首次置备新增一次完整读取，耗时随盘容量和实际存储吞吐增长；正常 ext4 恢复不会支付这项成本。
检查失败应保留原盘排查，不能手工清零来绕过门禁；该操作会销毁原数据。

## 验证结果（2026-10-24）

- `TestPlainData*` 的 22 个行为场景通过，包含本机 race 与隔离 Debian 12 容器内的真实 cmp。
  运行入口：在 `console/backend` 执行 `go test -race ./internal/service -run '^TestPlainData'`。
- 从 Go 模板渲染的 shell 脚本通过 ShellCheck；`make pre-commit` 与 `make audit` 均通过，
  Go race 覆盖率 75.6%，前端 271 项测试、216 项 qrexec 合约和全部文档/安全/lint 门禁通过。
- 复用[加密盘验收](2026-10-24-encrypted-data-mount.md)的 Debian 13 镜像及 QEMU 隔离配置，
  仅对新建 256 MiB SCSI fixture 执行渲染后的未加密盘脚本：盘尾非零字节被拒绝且保持原值；
  新零盘完成真实 mkfs/mount；文件跨卸载重挂载保持；device-mapper `error` 设备导致真实 EIO
  后拒绝初始化，再接回原盘时文件仍保留。guest 成功标记与 QEMU 退出码 0 均已核验。

该阶段没有改动生产磁盘、部署 Console 或创建真实 provider 验收对象。
