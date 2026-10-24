# 2026-10-23 离机恢复后连接运行中 agent

本次补齐[上一轮离机恢复](2026-10-23-production-closure.md)未覆盖的在线 agent Ping。
恢复目标是已授权的专用对象 `qa-cleanup-20261023`，ID
`cc8bb396-a05d-491c-b2f1-2300aeecb1dd`。原 Console 继续服务，恢复副本不接管编排。

## 范围与执行

确认对象 running/healthy、全局无活动 job 后，由现有备份 service 创建真实离机归档。
在 NAS 新建受限目录，核对归档大小和 SHA256，使用原归档口令和原 keyring 恢复；
未覆盖原 Console 数据库，也未创建替代 CA 或 keyring。

恢复副本使用与现场相同的 Console `06cfff3781d43f02783436f5d521b3029c5030f5`，
二进制 SHA256 为 `90cce94fd8376bee339544188d56ec17b04398514dc0c7dec8e49e3921fd3672`。
容器无发布端口，服务只绑定容器 loopback；保留出站网络以访问该测试 agent。
编排关闭，以下三个后台任务均显式设置为 0：

- `QUBES_AIR_AGENT_PROBE_INTERVAL_SECONDS`
- `QUBES_AIR_AGENT_CERT_RENEW_INTERVAL_SECONDS`
- `QUBES_AIR_AGENT_BOOTSTRAP_INTERVAL_SECONDS`

通过恢复副本的已认证 `GET /api/v1/qubes/<id>/reachable`，由实际 per-Qube prober
验证 agent 的 CA、角色、目标和证书登记并执行 Ping。返回目标对象的 pong，恢复库内
`agent_health` 为 healthy；agent certificate 和 job 总数均未增加，没有 provider 写操作。
结束后关闭并删除本次临时容器；恢复文件保存在受限目录供核验。

## 实测

| 项 | 结果 |
|---|---|
| 归档 | `qubesair-20261023T113921Z.qab`，290,996 字节 |
| SHA256 | `89c925077220b8228afe304f3c05d5e20e403e316f8efbe6fab3b92be9d554df` |
| 恢复数据 | 27 Qubes、106 jobs、30 agent certs、28 revoked、5 credentials、2 infra |
| SQLite integrity | `ok` |
| 原 keyring 解密 | 5 条凭据全部成功 |
| 原 CA | 与上一轮保存并核验的原 CA SHA256 一致 |
| 归档解包 | 0.314 秒 |
| 恢复开始至 Console 健康 | 0.860 秒 |
| 恢复开始至在线 agent Ping 成功 | 0.962 秒 |
| 新 agent 证书 / 新 job / provider 写操作 | 均为 0 |

计时前提是恢复主机、镜像、程序及密钥已就绪；这是当前小数据库的测量，不包含准备环境、
恢复数据卷或重建 Qubes 控制域。用户未提出生产规模/RTO/RPO 硬指标，当前按实测记录。

本次证明原信任材料恢复后可连接仍运行的 agent。原 Console 仍提供签名吊销状态，尚未
切换生产入口，也未证明原控制域完全丢失后的单一写入方接管。旧归档/恢复副本仍受既有
密钥留存策略约束，后续测试对象 purge 不会清除这些副本里的历史 DEK。
