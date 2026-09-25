# 2026-09-25 OPS-01 环境只读预检

本记录只包含目标 Console 与 Proxmox 的只读观察。没有读取或记录 API token、keyring、备份口令内容，
没有执行备份、恢复、PVE 创建/停止/销毁或部署变更。

## 观察结果

- Console `/health` 返回 healthy，数据库已连接。Console API 的只读 Zone 查询显示一个 Proxmox QA Zone
  为 connected；Zone 指向配置中的 PVE 集群。
- Console API 当前列出 12 个 Qube 记录，全部为 `purged`；当前没有可用于续测的活动 QA Qube。
- Console 上 `/secure/offhost` 不是挂载点，目录不存在；`backup.env` 不存在。
- 在 Console 的 Qubes Air 数据目录、配置的离机目录和常见挂载目录中，未找到 `*.qab` 或 `backup.env`。
- Console 的 `secrets.env` 文件存在，但本轮没有读取其内容。因此匹配的 encryption keyring 是否可用仍未验证。
- 当前 checkout 的权威 `qubes-salt-config` 配置将 `qubesair.backup.enabled` 设为 `False`，且备份二进制摘要为空；
  这是仓库配置状态，不单独证明目标 dom0 的运行时 pillar 与 checkout 完全一致。

## 结论与后续

OPS-01 的真实离机恢复暂时无法执行：没有挂载的离机介质、真实备份归档和备份口令；即使 Console keyring 文件存在，
也尚未证明其中密钥与任何真实归档匹配。需先在目标 Qubes 上配置并挂载专用离机备份介质，生成真实归档，
并通过独立保管路径取得匹配的归档口令与 keyring；之后才能在隔离临时路径验证恢复、CA/吊销状态、provider 对账和 RTO。

QA-01 后续若需新建测试 Qube，应使用专用 QA 名称与 Zone；任何 purge 前记录精确 Qube 名称和资源身份，
并按销毁门禁复核目标。
