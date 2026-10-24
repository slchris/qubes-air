# 旧盘换钥的身份与完成判定

本阶段补强 DATA-01 / G-B4 的 guest 执行边界。原 RekeyData 会选取第一个 scsi1，未核验
已有 mapper 和 `/data`；JSON 字段转成 shell 行后才校验，可能使用被截短的密钥；特权 runner
失败可能没有 JSON 响应。此外，旧键探测的任意非零退出曾被当成旧键失效，可能误报迁移完成。

## 行为、信任边界与验收条件

- 固定执行已安装的 RekeyData 和共享只读 checker，移除设备环境覆盖。特权段在任何 key
  probe 或 keyslot 修改前确认唯一 scsi1 root 块设备；已有 mapper 必须匹配 LUKS UUID、
  backing device 和可用状态，已有 `/data` 必须通过安全目录、来源、文件系统根与读写检查。
  尚未打开 mapper 的旧盘可以迁移；缺盘、歧义、外来/嵌套/只读挂载均失败。
- 输入仍限 8192 字节，仅接受恰好含 `old`、`new` 两个字符串的 JSON 对象；拒绝重复字段、
  额外字段、非法类型、完整字符串中的换行与非法字符。验证完整字段后才生成内部两行输入。
- key probe 仅接受成功或明确的 passphrase 拒绝；参数、资源、设备和执行错误立即返回
  `probe_failed`，不得据此报告旧 keyslot 已移除。退出码依据
  [cryptsetup 2.7.5 文档](https://gitlab.com/cryptsetup/cryptsetup/-/blob/v2.7.5/man/cryptsetup.8.adoc#return-codes)。
  退出码 2 也表示权限拒绝；固定 root 执行、身份检查及可信 guest/provider 是该判定的前提，
  不承诺抵御受信主体在两次 probe 间主动改变设备或权限。
- runner 非零退出、无响应或失败前输出的部分响应统一返回 `runner_failed`。`probe_failed`、
  `runner_failed` 和 `change_failed` 都不承诺旧键仍有效：变更可能已经发生，完成状态尚未确认。
  保留 Console 中原 DEK 与迁移标记，后续使用同一对键重试，不重新生成密钥。
- 未改变调用方 mTLS/角色授权、Console 先存 DEK/标记再请求的顺序、root/systemd 信任边界，
  没有新设备参数、root helper 或部署入口。密钥不进入 argv、日志、cloud-init 或持久文件。

验收要求：先记录输入、身份、runner 与错误 probe 的红回归；修复后验证成功、拒绝、部分完成
和重试；专用 QEMU 中使用真实 cryptsetup、systemd-run 和包内 agent unit 沙箱验证 LUKS；
自查 diff，并通过包安装/升级测试与 `make pre-commit audit`。

## 验证记录

- 两组修复前红回归已保存：完整字段/重复字段/身份/runner 拒绝，以及错误 probe 导致误报完成。
  修复后定向 Go 回归通过，覆盖 probe 出错时无 keyslot 修改、修改后探测失败保留未知状态，
  以及相同键重试确认完成。runner 非零、空响应及非零前输出成功 JSON 均拒绝。
- 27 项共享 checker 测试与 ShellCheck 通过；原加密和未加密挂载检查保留。
- QEMU 首次运行因对尚未加载的单元执行 `reset-failed` 失败，未进入换钥；夹具改为完成后
  保持 oneshot loaded，再清理启动计数。最终代码重验通过，guest 成功标记、QEMU 退出码 0
  和结果文件均核验。真实 cryptsetup 验证 old→new、旧键拒绝、文件/UUID 跨 close/open
  保持、重复请求不改 LUKS header，以及两键均有效的残留槽经重试移除、最终只剩一个 slot。
  非 LUKS、非法输入、错误密钥、外来/嵌套/只读挂载、错误 mapper、歧义/缺失 scsi1 均拒绝。
- Debian 包安装、升级 conffile 保留、完整性、启动拒绝及卸载通过，包内 66 项 Python 测试
  和原生签名探测通过。
- `make pre-commit audit` 全部通过：Go race 总覆盖率 75.5%，本机/Linux lint、gosec、
  复杂度、ShellCheck、严格 YAML 与文档链接检查通过；前端 271 项测试与 check/build
  通过，零错误、零警告。govulncheck 没有报告可达漏洞，仍提示一项未被当前代码调用的
  依赖模块漏洞；不将此表述为依赖零漏洞。

QEMU 只使用新建的两个 256 MiB SCSI fixture，没有宿主磁盘透传、共享目录或入站转发。
fixture 运行前要求专用 marker、QEMU/KVM 身份、精确盘大小以及不存在原 mapper/`/data`。
请求中的随机测试键只短暂保存在已验证无 swap 的 `/run` tmpfs，调用后删除；检查 journal
不含测试键。包内 agent unit 的 root、NoNewPrivileges、PrivateTmp、ProtectHome 和
ProtectSystem 保持原值；fixture drop-in 只替换 ExecStart、去掉身份环境依赖、关闭自动
重启并使用 oneshot/RemainAfterExit 和 tmpfs stdin/stdout，以同步检查每次运行。

复现入口（镜像和 UEFI 参数见[既有 QEMU 验收要求](2026-10-24-encrypted-data-mount.md)）：

```bash
python3 -m unittest discover -s remote/tests -p test_data_mount.py
python3 scripts/test-data-mount-guest.py \
  --image "$DEBIAN_IMAGE" --sha512 "$DEBIAN_SHA512" \
  --firmware-code "$UEFI_CODE" --firmware-vars "$UEFI_VARS" --rekey-data
make agent-deb-test
make pre-commit audit
```

## 生产边界

本阶段没有执行生产旧盘换钥、删除 legacy master 或清理未获授权的测试对象。
隔离 Linux 证据不包含真实 agent mTLS/bootstrap、Console 迁移标记落库或 provider 生命周期。
G-B4 / QA-01 仍需具备旧盘与原 master、确认精确目标后，按[迁移 runbook](../runbook-qa01.md#5-数据盘迁移核验data-01)
验证新键成功、旧键拒绝、文件保持、Console 标记清除及后续生命周期；不能据本记录宣称生产
crypto-shred 已完成。既有 guest 需交付同时包含新 RekeyData 和 checker 的 agent 包。
