# 未加密数据盘的挂载身份

本阶段处理 DATA-02 / G-H13。已有启动脚本会把任意 `/data` 挂载当作成功，且在检查
已有挂载前探测甚至格式化 scsi1；挂载单元失败也没有阻止 agent 安装后的启动。

## 行为、边界和验收条件

- 将现有只读数据身份检查作为共享源码，供 cloud-init 和 agent 包交付。cloud-init
  在 agent 安装前提供固定 checker，不依赖包已安装，也不被包升级覆盖。
- 在任何探测或格式化前确认唯一 scsi1 root 块设备、安全的 root 所有 `/data` 目录，
  并拒绝错误、只读、重复、子目录或嵌套挂载，以及同一设备在其他位置的挂载。
- 已挂载的正确 ext4 直接成功；否则保留完整全零读取保护，格式化前及挂载前重新核验，
  挂载后再确认实际设备号、文件系统根、ext4 和读写属性。
- 当前规格要求持久数据盘。scsi1 缺失最多重试 15 次，每次等待 1 秒，之后失败并阻止 agent 启动；
  不能把缺盘当作成功。错误和歧义直接失败。
- 未加密模式给 agent 配置固定数据单元依赖和启动前复验，失败不会启动 workload。
  加密模式继续经 bootstrap 后的 UnlockData 解锁，不引入该依赖。
- 接口只接受固定阶段名称，不接收设备、目录或命令参数。provider 管理员、guest root
  和内核仍受信任；本阶段不承诺抵御其主动并发替换设备或绕过服务的行为。

验收要求：先保留旧脚本红回归；补设备/目录/挂载正反例及格式化前、挂载后失败路径；
在专用 QEMU 临时盘中执行真实格式化、挂载、文件保持和 systemd 失败门禁；确认原加密
检查与 agent 包未退化；通过 `make pre-commit audit` 后再提交。

## 验证证据

- 旧脚本执行回归明确失败：错误已有挂载被接受，空盘可先被格式化，挂载后未复验，
  且没有阻断 agent 的依赖；另保留缺盘等待后仍退出成功的红回归。
- Go 回归执行从实际 cloud-config YAML 解码得到的 shell，格式化和挂载以受控桩隔离。
  覆盖探测前拒绝、完整零读、格式化/挂载前状态变化、挂载后失败、幂等已有挂载、
  缺盘等待上限及 mount 非零退出；cloud-init 的 checker、unit 和 drop-in 按原字节交付。
- 25 项共享身份检查测试通过，包括原加密盘校验与新增未加密阶段；与 HOME 测试合跑
  共 37 项通过。ShellCheck 通过；加密 cloud-init 明确不交付未加密启动依赖。
- 专用 Debian 13 QEMU 最终运行成功，guest 标记与退出码 0 均核验。仅使用新建的两个
  256 MiB SCSI fixture；无宿主块设备、共享目录或入站转发。真实负例包括外来 tmpfs/ext4、
  嵌套/叠加/只读/绑定挂载、错误目录所有权与 symlink、歧义/非 root/非块/悬空/缺失设备，
  以及盘尾非零字节和真实 EIO。错误目录检查前后的空盘完整摘要一致。
- 同一 QEMU 中，正确零盘初始化成功，重复调用与卸载重挂载保持 UUID 和文件；真实
  systemd 验证了数据 unit 失败和已成功 oneshot 之后挂载变坏两条启动拒绝路径，修复挂载
  后可恢复。agent 主程序在此用固定 marker unit 替代，只验证 systemd 依赖与 ExecStartPre。
- 最终 Debian 包安装、升级、conffile 保留、校验和、原加密 checker 与卸载测试通过。
  包继续把共享 checker 安装到原 `/usr/lib/qubes-air/data-mount.py`；plain cloud-init 的
  `/usr/local/lib` 副本由 Console 交付，在 agent 安装前可用，包升级不会覆盖它。
- `make pre-commit audit` 全部通过，Go race 覆盖率 75.5%，前端零错误、零警告；
  本机/Linux lint、gosec、复杂度、依赖、ShellCheck、YAML 和文档链接门禁通过。

复现入口（镜像与 UEFI 参数使用[既有 QEMU 验收要求](2026-10-24-encrypted-data-mount.md)）：

```bash
python3 -m unittest discover -s remote/tests -p test_data_mount.py
python3 scripts/test-data-mount-guest.py \
  --image "$DEBIAN_IMAGE" --sha512 "$DEBIAN_SHA512" \
  --firmware-code "$UEFI_CODE" --firmware-vars "$UEFI_VARS" --plain-data
scripts/test-agent-deb.sh
make pre-commit audit
```

本阶段未部署 Console、改动生产磁盘或新建 provider 对象。既有实例的 cloud-init 不会自动
更新；生产未加密生命周期仍需以新 Console 生成的配置另行验收。
